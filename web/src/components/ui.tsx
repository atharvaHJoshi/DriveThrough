import type { ReactNode } from 'react'
import type { BookingStatus, DriverStatus, PaymentStatus } from '../api/types'

/* ---- status presentation ---- */

const BOOKING_TONE: Record<BookingStatus, string> = {
  REQUESTED: 'blue',
  SEARCHING: 'amber',
  DRIVER_ASSIGNED: 'blue',
  DRIVER_EN_ROUTE: 'blue',
  DRIVER_ARRIVED: 'blue',
  OTP_VERIFIED: 'green',
  TRIP_STARTED: 'green',
  TRIP_PAUSED: 'amber',
  TRIP_RESUMED: 'green',
  TRIP_COMPLETED: 'green',
  CANCELLED: 'red',
  CANCELLED_BY_DRIVER: 'red',
  CANCELLED_BY_ADMIN: 'red',
  DRIVER_NO_SHOW: 'red',
  PAYMENT_FAILED: 'red',
  SUPPORT_ESCALATION: 'amber',
  ACCIDENT: 'red',
}

const BOOKING_LABEL: Record<BookingStatus, string> = {
  REQUESTED: 'Requested',
  SEARCHING: 'Finding driver',
  DRIVER_ASSIGNED: 'Driver assigned',
  DRIVER_EN_ROUTE: 'Driver on the way',
  DRIVER_ARRIVED: 'Driver arrived',
  OTP_VERIFIED: 'Verified',
  TRIP_STARTED: 'Trip in progress',
  TRIP_PAUSED: 'Paused',
  TRIP_RESUMED: 'Resumed',
  TRIP_COMPLETED: 'Completed',
  CANCELLED: 'Cancelled',
  CANCELLED_BY_DRIVER: 'Cancelled by driver',
  CANCELLED_BY_ADMIN: 'Cancelled by support',
  DRIVER_NO_SHOW: 'Driver no-show',
  PAYMENT_FAILED: 'Payment failed',
  SUPPORT_ESCALATION: 'With support',
  ACCIDENT: 'Accident',
}

/** Statuses after which no further transition is possible (internal/bookings/state.go). */
export const TERMINAL_BOOKING_STATUSES: BookingStatus[] = [
  'TRIP_COMPLETED',
  'CANCELLED',
  'CANCELLED_BY_DRIVER',
  'CANCELLED_BY_ADMIN',
  'DRIVER_NO_SHOW',
  'PAYMENT_FAILED',
  'ACCIDENT',
]

export function isTerminal(status: BookingStatus): boolean {
  return TERMINAL_BOOKING_STATUSES.includes(status)
}

/** Statuses where the trip is running and the customer should see live tracking. */
export const LIVE_TRACKING_STATUSES: BookingStatus[] = [
  'DRIVER_EN_ROUTE',
  'DRIVER_ARRIVED',
  'OTP_VERIFIED',
  'TRIP_STARTED',
  'TRIP_PAUSED',
  'TRIP_RESUMED',
]

export function isLiveTracking(status: BookingStatus): boolean {
  return LIVE_TRACKING_STATUSES.includes(status)
}

export function BookingStatusBadge({ status }: { status: BookingStatus }) {
  return <span className={`badge ${BOOKING_TONE[status] ?? ''}`}>{BOOKING_LABEL[status] ?? status}</span>
}

const DRIVER_TONE: Record<DriverStatus, string> = {
  ONLINE: 'green',
  OFFLINE: '',
  BUSY: 'amber',
  INACTIVE: 'red',
}

export function DriverStatusBadge({ status }: { status: DriverStatus }) {
  return <span className={`badge ${DRIVER_TONE[status] ?? ''}`}>{status}</span>
}

const PAYMENT_TONE: Record<PaymentStatus, string> = {
  INITIATED: 'blue',
  AUTHORIZED: 'blue',
  CAPTURED: 'green',
  VOIDED: '',
  FAILED: 'red',
  REFUNDED: 'amber',
  SETTLED: 'green',
}

export function PaymentStatusBadge({ status }: { status: PaymentStatus }) {
  return <span className={`badge ${PAYMENT_TONE[status] ?? ''}`}>{status}</span>
}

/* ---- layout primitives ---- */

export function Card({
  children,
  title,
  action,
  tight,
}: {
  children: ReactNode
  title?: string
  action?: ReactNode
  tight?: boolean
}) {
  return (
    <section className={`card${tight ? ' tight' : ''}`}>
      {(title || action) && (
        <header className="card-head">
          {title && <h2>{title}</h2>}
          {action}
        </header>
      )}
      {children}
    </section>
  )
}

export function Alert({
  kind = 'info',
  children,
}: {
  kind?: 'info' | 'error' | 'warn' | 'success'
  children: ReactNode
}) {
  return (
    <div className={`alert ${kind}`} role={kind === 'error' ? 'alert' : undefined}>
      {children}
    </div>
  )
}

export function Loading({ label = 'Loading' }: { label?: string }) {
  return (
    <div className="loading-center">
      <div className="row">
        <span className="spinner" />
        <span className="muted">{label}…</span>
      </div>
    </div>
  )
}

export function Empty({ icon, title, hint }: { icon: string; title: string; hint?: string }) {
  return (
    <div className="empty">
      <div className="big">{icon}</div>
      <div style={{ fontWeight: 600, color: 'var(--text-dim)' }}>{title}</div>
      {hint && <div className="faint" style={{ marginTop: 4 }}>{hint}</div>}
    </div>
  )
}

export function Field({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: ReactNode
}) {
  return (
    <div>
      <label>{label}</label>
      {children}
      {hint && <div className="faint" style={{ marginTop: 4 }}>{hint}</div>}
    </div>
  )
}

/** Rupee amounts are integer minor units throughout the API (never floats). */
export function rupees(amount: number | undefined | null): string {
  if (amount === undefined || amount === null) return '—'
  return `₹${amount.toLocaleString('en-IN')}`
}

export function timeAgo(iso: string): string {
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return ''
  const secs = Math.max(0, Math.round((Date.now() - then) / 1000))
  if (secs < 60) return `${secs}s ago`
  const mins = Math.round(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hours = Math.round(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return new Date(iso).toLocaleDateString()
}

export function shortId(id: string | undefined | null): string {
  if (!id) return '—'
  return id.slice(0, 8)
}

/** Extracts a human message from an unknown thrown value. */
export function errorMessage(err: unknown): string {
  if (err instanceof Error) return err.message
  return 'Something went wrong'
}
