/**
 * Types mirroring the Go API surface.
 *
 * Every shape here corresponds to a struct in internal/models. Keeping them in one
 * file means a backend field rename shows up as a TypeScript error at the call
 * site rather than as `undefined` at runtime.
 */

export type UserRole = 'CUSTOMER' | 'DRIVER' | 'ADMIN'

export type BookingStatus =
  | 'REQUESTED'
  | 'SEARCHING'
  | 'DRIVER_ASSIGNED'
  | 'DRIVER_EN_ROUTE'
  | 'DRIVER_ARRIVED'
  | 'OTP_VERIFIED'
  | 'TRIP_STARTED'
  | 'TRIP_PAUSED'
  | 'TRIP_RESUMED'
  | 'TRIP_COMPLETED'
  | 'CANCELLED'
  | 'CANCELLED_BY_DRIVER'
  | 'CANCELLED_BY_ADMIN'
  | 'DRIVER_NO_SHOW'
  | 'PAYMENT_FAILED'
  | 'SUPPORT_ESCALATION'
  | 'ACCIDENT'

export type DriverStatus = 'OFFLINE' | 'ONLINE' | 'BUSY' | 'INACTIVE'
export type VerificationStatus =
  | 'PENDING'
  | 'SUBMITTED'
  | 'APPROVED'
  | 'REJECTED'
  | 'SUSPENDED'

export type BookingType = 'HOURLY' | 'TRIP' | 'ROUND_TRIP'
export type Transmission = 'MANUAL' | 'AUTOMATIC'
export type FuelType = 'PETROL' | 'DIESEL' | 'CNG' | 'ELECTRIC' | 'HYBRID'
export type VehicleClass = 'HATCHBACK' | 'SEDAN' | 'SUV' | 'LUXURY' | 'MUV'
export type PaymentStatus =
  | 'INITIATED'
  | 'AUTHORIZED'
  | 'CAPTURED'
  | 'VOIDED'
  | 'FAILED'
  | 'REFUNDED'
  | 'SETTLED'
export type DocumentType = 'AADHAAR' | 'PAN' | 'DRIVING_LICENCE' | 'ADDRESS_PROOF'

export interface User {
  id: string
  name: string
  phone: string
  email?: string
  role: UserRole
  is_active: boolean
  created_at: string
  updated_at: string
}

export interface TokenPair {
  access_token: string
  refresh_token: string
  /** Unix seconds at which the access token expires. */
  expires_at: number
}

export interface VehicleCapability {
  manual: boolean
  automatic: boolean
  hatchback: boolean
  sedan: boolean
  suv: boolean
  luxury: boolean
}

export interface Vehicle {
  id: string
  user_id: string
  registration_number: string
  make: string
  model: string
  year: number
  transmission: Transmission
  fuel_type: FuelType
  class: VehicleClass
  is_default: boolean
  created_at: string
  updated_at: string
}

export interface PriceBreakdown {
  driver_fee: number
  platform_fee: number
  protection_fee: number
  tax: number
  total: number
}

export interface PriceEstimate {
  driver_fee: number
  platform_fee: number
  protection_fee: number
  tax: number
  total: number
  breakdown: PriceBreakdown
}

export interface Booking {
  id: string
  customer_id: string
  driver_id?: string
  vehicle_id: string
  booking_type: BookingType
  duration_hours: number
  pickup_location: string
  pickup_lat: number
  pickup_lng: number
  destination?: string
  dest_lat?: number
  dest_lng?: number
  scheduled_at?: string
  status: BookingStatus
  estimated_price: number
  final_price?: number
  driver_fee: number
  platform_fee: number
  protection_fee: number
  tax: number
  created_at: string
  updated_at: string
}

export interface Location {
  latitude: number
  longitude: number
}

export interface Driver {
  id: string
  user_id: string
  status: DriverStatus
  verification_status: VerificationStatus
  rating: number
  total_trips: number
  total_ratings: number
  experience_years: number
  acceptance_rate: number
  cancellation_rate: number
  total_offers: number
  accepted_offers: number
  total_cancellations: number
  can_drive_manual: boolean
  can_drive_automatic: boolean
  can_drive_hatchback: boolean
  can_drive_sedan: boolean
  can_drive_suv: boolean
  can_drive_luxury: boolean
  current_location: Location | null
  earnings_total: number
  created_at: string
  updated_at: string
}

export interface PublicDriver {
  id: string
  status: DriverStatus
  verification_status: VerificationStatus
  rating: number
  total_trips: number
  total_ratings: number
  experience_years: number
  acceptance_rate: number
  cancellation_rate: number
  can_drive_manual: boolean
  can_drive_automatic: boolean
  can_drive_hatchback: boolean
  can_drive_sedan: boolean
  can_drive_suv: boolean
  can_drive_luxury: boolean
}

export interface Trip {
  id: string
  booking_id: string
  driver_id: string
  vehicle_id: string
  started_at: string
  ended_at?: string
  start_odometer: number
  end_odometer?: number
  start_fuel: number
  end_fuel?: number
  total_distance: number
  status: 'ACTIVE' | 'PAUSED' | 'COMPLETED'
  created_at: string
  updated_at: string
}

export interface EndTripResult {
  Trip: Trip
  distance_km: number
  duration: number
  final_price: number
  price_breakdown: PriceBreakdown
}

export interface VehicleInspection {
  id: string
  trip_id: string
  type: 'PRE_TRIP' | 'POST_TRIP'
  odometer: number
  fuel_level: number
  photos: string[]
  damage_notes?: string
  has_damage: boolean
  created_at: string
}

export interface Payment {
  id: string
  booking_id: string
  customer_id: string
  amount: number
  platform_fee: number
  driver_amount: number
  authorization: number
  protection_fee: number
  tax: number
  status: PaymentStatus
  method?: 'CARD' | 'UPI' | 'NETBANKING' | 'WALLET'
  gateway_reference: string
  gateway_order_id: string
  created_at: string
  updated_at: string
}

/** Response of POST /bookings/{id}/payment/initiate. */
export interface InitiatePaymentResult {
  payment: Payment
  order_id: string
  key_id: string
  is_mock: boolean
}

export interface Rating {
  id: string
  booking_id: string
  from_user: string
  to_user: string
  rating: number
  comment?: string
  tags?: string[]
  created_at: string
}

export interface Notification {
  id: string
  user_id: string
  type: string
  title: string
  body: string
  data?: Record<string, string>
  is_read: boolean
  created_at: string
}

export interface DriverEarning {
  id: string
  driver_id: string
  booking_id: string
  amount: number
  status: 'PENDING' | 'PAID' | 'FAILED'
  created_at: string
}

export interface EmergencyContact {
  id: string
  user_id: string
  name: string
  phone: string
  relation: string
  created_at: string
}

export interface DriverDocument {
  id: string
  driver_id: string
  type: DocumentType
  document_number: string
  status: 'PENDING' | 'VERIFIED' | 'REJECTED'
  expiry_date?: string
  document_url: string
  verified_at?: string
  created_at: string
}

/** An offer pushed to a driver. The notification body carries these fields. */
export interface OfferPayload {
  booking_id: string
  offer_id: string
  pickup: { lat: number; lng: number }
  pickup_text: string
  vehicle: Vehicle
  duration_hours: number
  fare: number
  expires_in_seconds: number
}

/** The slice of a booking a driver sees while judging an offer. */
export interface OfferBooking {
  pickup_location: string
  pickup_lat: number
  pickup_lng: number
  destination?: string
  duration_hours: number
  driver_fee: number
  booking_type: BookingType
}

/** A live offer from GET /driver/offers. */
export interface Offer {
  id: string
  booking_id: string
  driver_id: string
  status: string
  distance_m: number
  booking: OfferBooking
  expires_at: string
}

/** Response of GET /driver/earnings. */
export interface EarningsSummary {
  total: number
  entries: DriverEarning[] | null
  limit: number
  offset: number
}
