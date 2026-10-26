/**
 * HTTP client for the DriveThrough API.
 *
 * Two behaviours are worth knowing about:
 *
 *  1. Every response is wrapped in the backend's `{success, data, error}` envelope
 *     (pkg/response). This unwraps it, so callers get the payload or a typed
 *     ApiError rather than a raw Response.
 *
 *  2. A 401 triggers exactly one refresh attempt, and concurrent 401s share that
 *     single refresh. Without the de-duplication, a screen that fires several
 *     requests at once on an expired token would send N refreshes, and the
 *     blacklist plus rotation make that both wasteful and racy.
 */

import type {
  Booking,
  Driver,
  DriverDocument,
  DriverEarning,
  EarningsSummary,
  EmergencyContact,
  EndTripResult,
  InitiatePaymentResult,
  Location,
  Notification,
  Offer,
  Payment,
  PriceEstimate,
  PublicDriver,
  PriceBreakdown,
  Rating,
  TokenPair,
  Trip,
  User,
  UserRole,
  Vehicle,
  VehicleCapability,
  VehicleInspection,
} from './types'

const BASE = '/api/v1'

/** Error carrying the backend's stable code so callers can branch on it. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly detail?: unknown

  constructor(status: number, code: string, message: string, detail?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.detail = detail
  }
}

interface Envelope<T> {
  success: boolean
  data?: T
  error?: { code: string; message: string; detail?: unknown }
}

const ACCESS_KEY = 'dt.access_token'
const REFRESH_KEY = 'dt.refresh_token'
const USER_KEY = 'dt.user'

type Listener = (auth: { access: string; refresh: string } | null) => void
const authListeners = new Set<Listener>()

export const tokenStore = {
  access: () => localStorage.getItem(ACCESS_KEY),
  refresh: () => localStorage.getItem(REFRESH_KEY),
  user: (): User | null => {
    const raw = localStorage.getItem(USER_KEY)
    return raw ? (JSON.parse(raw) as User) : null
  },
  save(tokens: TokenPair, user?: User) {
    localStorage.setItem(ACCESS_KEY, tokens.access_token)
    localStorage.setItem(REFRESH_KEY, tokens.refresh_token)
    if (user) localStorage.setItem(USER_KEY, JSON.stringify(user))
    authListeners.forEach((fn) =>
      fn({ access: tokens.access_token, refresh: tokens.refresh_token }),
    )
  },
  saveUser(user: User) {
    localStorage.setItem(USER_KEY, JSON.stringify(user))
  },
  clear() {
    localStorage.removeItem(ACCESS_KEY)
    localStorage.removeItem(REFRESH_KEY)
    localStorage.removeItem(USER_KEY)
    authListeners.forEach((fn) => fn(null))
  },
  isAuthenticated: () => Boolean(localStorage.getItem(ACCESS_KEY)),
  /** Subscribe to login/logout so the shell can re-render. */
  subscribe(fn: Listener) {
    authListeners.add(fn)
    return () => {
      authListeners.delete(fn)
    }
  },
}

/** Called when a refresh fails and the session is unrecoverable. */
let onSessionExpired: () => void = () => {}
export function setSessionExpiredHandler(fn: () => void) {
  onSessionExpired = fn
}

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'DELETE'
  body?: unknown
  /** Set for the refresh call itself, to stop it recursing. */
  skipRefresh?: boolean
  /** Absolute URL, for endpoints outside /api/v1. */
  baseUrl?: string
}

/** Shared in-flight refresh so parallel 401s collapse into one network call. */
let refreshInFlight: Promise<boolean> | null = null

async function refreshSession(): Promise<boolean> {
  const refresh = tokenStore.refresh()
  if (!refresh) return false

  if (!refreshInFlight) {
    refreshInFlight = (async () => {
      try {
        const res = await fetch(`${BASE}/auth/refresh`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ refresh_token: refresh }),
        })
        if (!res.ok) return false
        const body = (await res.json()) as Envelope<TokenPair>
        if (!body.success || !body.data) return false
        tokenStore.save(body.data)
        return true
      } catch {
        return false
      } finally {
        // Cleared in finally so a failed refresh does not wedge every later 401.
        refreshInFlight = null
      }
    })()
  }
  return refreshInFlight
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, skipRefresh, baseUrl = BASE } = opts
  const headers: Record<string, string> = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'

  const token = tokenStore.access()
  if (token) headers.Authorization = `Bearer ${token}`

  let res: Response
  try {
    res = await fetch(`${baseUrl}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    throw new ApiError(0, 'NETWORK_ERROR', 'Cannot reach the server. Is the API running?')
  }

  if (res.status === 204) return undefined as T

  let payload: Envelope<T> | null = null
  try {
    payload = (await res.json()) as Envelope<T>
  } catch {
    // A non-JSON body means something other than this API answered.
    throw new ApiError(res.status, 'BAD_RESPONSE', `Unexpected response (${res.status})`)
  }

  if (res.ok && payload.success) {
    return payload.data as T
  }

  const code = payload.error?.code ?? 'UNKNOWN'
  const message = payload.error?.message ?? `Request failed (${res.status})`

  if (res.status === 401 && !skipRefresh && tokenStore.refresh()) {
    const refreshed = await refreshSession()
    if (refreshed) {
      return request<T>(path, opts)
    }
    tokenStore.clear()
    onSessionExpired()
  }

  throw new ApiError(res.status, code, message, payload.error?.detail)
}

const get = <T>(path: string) => request<T>(path)
const post = <T>(path: string, body?: unknown) => request<T>(path, { method: 'POST', body })
const patch = <T>(path: string, body?: unknown) => request<T>(path, { method: 'PATCH', body })
const del = <T>(path: string) => request<T>(path, { method: 'DELETE' })

export const api = {
  // ---- auth ----
  sendOtp: (phone: string) => post<{ message: string }>('/auth/send-otp', { phone }),
  verifyOtp: (phone: string, code: string, name: string, role: UserRole) =>
    post<{ user: User; tokens: TokenPair }>('/auth/verify-otp', {
      phone,
      code,
      name,
      role,
    }),
  me: () => get<User>('/auth/me'),
  logout: () => post<null>('/auth/logout'),
  updateProfile: (name: string) => patch<User>('/users/me', { name }),

  // ---- vehicles ----
  listVehicles: () => get<Vehicle[]>('/vehicles'),
  createVehicle: (v: Partial<Vehicle>) => post<Vehicle>('/vehicles', v),
  updateVehicle: (id: string, v: Partial<Vehicle>) => patch<Vehicle>(`/vehicles/${id}`, v),
  deleteVehicle: (id: string) => del<null>(`/vehicles/${id}`),
  setDefaultVehicle: (id: string) => post<null>(`/vehicles/${id}/default`),

  // ---- bookings ----
  estimate: (durationHours: number, scheduledAt?: string) =>
    post<PriceEstimate>('/bookings/estimate', {
      duration_hours: durationHours,
      scheduled_at: scheduledAt ?? null,
    }),
  // POST /bookings answers {booking, price}; callers want the booking.
  createBooking: (input: {
    vehicle_id: string
    booking_type: string
    duration_hours: number
    pickup_location: string
    pickup_lat: number
    pickup_lng: number
    destination?: string
    dest_lat?: number
    dest_lng?: number
    scheduled_at?: string
  }) =>
    post<{ booking: Booking; price: PriceBreakdown }>('/bookings', input).then((r) => r.booking),
  listCustomerBookings: () => get<Booking[]>('/bookings'),
  // GET /bookings/{id} answers {booking, price}, not a bare booking.
  getBooking: (id: string) => get<{ booking: Booking; price: PriceBreakdown }>(`/bookings/${id}`),
  /** Resolves the trip for a booking so live tracking can subscribe. */
  getTripForBooking: (bookingId: string) => get<Trip>(`/bookings/${bookingId}/trip`),
  cancelBooking: (id: string, reason?: string) =>
    post<{ status: string }>(`/bookings/${id}/cancel`, { reason }),
  verifyHandoverOtp: (bookingId: string, otp: string) =>
    post<{ status: string }>(`/bookings/${bookingId}/verify-otp`, { otp }),

  // ---- driver ----
  registerDriver: (experienceYears: number, capabilities: VehicleCapability) =>
    post<Driver>('/driver/register', {
      experience_years: experienceYears,
      capabilities,
    }),
  driverProfile: () => get<Driver>('/driver/profile'),
  publicDriver: (id: string) => get<PublicDriver>(`/driver/${id}`),
  goOnline: () => post<Driver>('/driver/online', { online: true }),
  goOffline: () => post<Driver>('/driver/offline', { online: false }),
  updateCapabilities: (capabilities: VehicleCapability) =>
    patch<Driver>('/driver/capabilities', { capabilities }),
  updateDriverLocation: (lat: number, lng: number) =>
    post<null>('/driver/location', { lat, lng }),
  submitDocument: (d: {
    type: string
    document_number: string
    expiry_date?: string
    document_url?: string
  }) => post<{ status: string }>('/driver/documents', d),
  listDocuments: () => get<DriverDocument[]>('/driver/documents'),
  driverEarnings: () => get<EarningsSummary>('/driver/earnings'),
  listDriverBookings: () => get<Booking[]>('/driver/bookings'),
  currentBooking: () => get<Booking>('/driver/bookings/current'),
  /** Pending offers. This is how a driver sees work before being assigned. */
  listOffers: () => get<Offer[]>('/driver/offers'),
  acceptOffer: (bookingId: string, offerId: string) =>
    post<{ status: string; booking_id: string }>(`/driver/bookings/${bookingId}/accept`, {
      offer_id: offerId,
    }),
  rejectOffer: (bookingId: string, offerId: string) =>
    post<{ status: string }>(`/driver/bookings/${bookingId}/reject`, { offer_id: offerId }),
  driverCancelBooking: (bookingId: string, reason?: string) =>
    post<{ status: string }>(`/driver/bookings/${bookingId}/cancel`, { reason }),
  markEnRoute: (bookingId: string) => post<Booking>(`/driver/bookings/${bookingId}/en-route`),
  markArrived: (bookingId: string) => post<Booking>(`/driver/bookings/${bookingId}/arrived`),
  markNoShow: (bookingId: string, reason: string) =>
    post<Booking>(`/driver/bookings/${bookingId}/no-show`, { reason }),

  // ---- trips ----
  startTrip: (input: {
    booking_id: string
    start_odometer: number
    start_fuel: number
    pre_inspection?: {
      odometer: number
      fuel_level: number
      photos?: string[]
      damage_notes?: string
      has_damage?: boolean
    }
  }) => post<Trip>('/trips/start', input),
  pauseTrip: (tripId: string) => post<Trip>(`/trips/${tripId}/pause`),
  resumeTrip: (tripId: string) => post<Trip>(`/trips/${tripId}/resume`),
  endTrip: (
    tripId: string,
    input: {
      end_odometer: number
      end_fuel: number
      post_inspection?: {
        odometer: number
        fuel_level: number
        photos?: string[]
        damage_notes?: string
        has_damage?: boolean
      }
    },
  ) => post<EndTripResult>(`/trips/${tripId}/end`, input),
  recordTripLocation: (tripId: string, loc: Location & { speed?: number; heading?: number }) =>
    post<{ status: string }>(`/trips/${tripId}/location`, {
      // The trips handler decodes json:"lat"/"lng"; sending latitude/longitude
      // silently recorded a 0,0 fix that the location validator then rejected.
      lat: loc.latitude,
      lng: loc.longitude,
      speed: loc.speed ?? 0,
      heading: loc.heading ?? 0,
      accuracy: 0,
    }),
  tripInspections: (tripId: string) => get<VehicleInspection[]>(`/trips/${tripId}/inspections`),

  // ---- payments ----
  initiatePayment: (bookingId: string) =>
    post<InitiatePaymentResult>(`/bookings/${bookingId}/payment/initiate`),
  getPayment: (bookingId: string) => get<Payment>(`/bookings/${bookingId}/payment`),
  refundBooking: (bookingId: string, reason?: string) =>
    post<{ id: string; amount: number; status: string }>(`/bookings/${bookingId}/refund`, {
      reason,
    }),

  // ---- ratings ----
  rateBooking: (bookingId: string, rating: number, comment?: string, tags?: string[]) =>
    post<{ status: string }>(`/bookings/${bookingId}/rate`, { rating, comment, tags }),
  bookingRatings: (bookingId: string) => get<Rating[]>(`/bookings/${bookingId}/ratings`),

  // ---- safety ----
  triggerSos: (tripId: string | null, lat: number, lng: number, kind = 'EMERGENCY') =>
    post<{ id: string; status: string }>('/sos', {
      trip_id: tripId,
      lat,
      lng,
      kind,
    }),
  listEmergencyContacts: () => get<EmergencyContact[]>('/emergency-contacts'),
  addEmergencyContact: (name: string, phone: string, relation: string) =>
    post<EmergencyContact>('/emergency-contacts', { name, phone, relation }),

  // ---- notifications ----
  listNotifications: () => get<Notification[]>('/notifications'),
  markNotificationRead: (id: string) => post<{ status: string }>(`/notifications/${id}/read`),
  registerDeviceToken: (token: string, platform = 'web') =>
    post<{ status: string }>('/notifications/device-token', { token, platform }),
}

export const health = {
  live: () => fetch('/health').then((r) => r.json()),
  ready: () => fetch('/ready').then((r) => r.json()),
}
