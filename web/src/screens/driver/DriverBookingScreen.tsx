import { useCallback, useEffect, useState } from 'react'
import { api } from '../../api/client'
import type { Booking, Driver, Location, Trip } from '../../api/types'
import {
  Alert,
  BookingStatusBadge,
  Card,
  Field,
  errorMessage,
  rupees,
  shortId,
  timeAgo,
} from '../../components/ui'

/** Statuses for which a trip row exists, so the trip id can be resolved. */
const TRIP_STATUSES = [
  'OTP_VERIFIED',
  'TRIP_STARTED',
  'TRIP_PAUSED',
  'TRIP_RESUMED',
  'TRIP_COMPLETED',
]

/**
 * Driver-facing view of one booking, from assignment through to trip end.
 *
 * The actions offered depend on status, and each is gated on the driver being
 * assigned: the API enforces this too, but showing only the legal action avoids a
 * user discovering the rule by hitting a 403.
 *
 * A booking carries no trip id, but pause/resume/end/location are all addressed
 * by trip id, so the trip is resolved from the booking whenever one should exist.
 */
export function DriverBookingScreen({
  bookingId,
  onChanged,
}: {
  bookingId: string
  onChanged?: () => void
}) {
  const [booking, setBooking] = useState<Booking | null>(null)
  const [driver, setDriver] = useState<Driver | null>(null)
  const [trip, setTrip] = useState<Trip | null>(null)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [odo, setOdo] = useState('')
  const [fuel, setFuel] = useState('')
  const [reason, setReason] = useState('')

  const load = useCallback(async () => {
    try {
      const b = await api.getBooking(bookingId)
      setBooking(b.booking)
      try {
        setDriver(await api.driverProfile())
      } catch {
        setDriver(null)
      }
      if (TRIP_STATUSES.includes(b.booking.status)) {
        try {
          setTrip(await api.getTripForBooking(bookingId))
        } catch {
          setTrip(null)
        }
      } else {
        setTrip(null)
      }
    } catch (err) {
      setError(errorMessage(err))
    }
  }, [bookingId])

  useEffect(() => {
    void load()
  }, [load])

  // While a trip is live the server-side status changes on its own (paused,
  // completed by the customer closing out), so poll to reflect it.
  useEffect(() => {
    if (!booking) return
    const live = ['DRIVER_ASSIGNED', 'DRIVER_EN_ROUTE', 'DRIVER_ARRIVED', 'OTP_VERIFIED', 'TRIP_STARTED', 'TRIP_PAUSED', 'TRIP_RESUMED']
    if (!live.includes(booking.status)) return
    const id = window.setInterval(() => void load(), 7000)
    return () => window.clearInterval(id)
  }, [booking, load])

  async function act(name: string, fn: () => Promise<unknown>, message?: string) {
    setBusy(name)
    setError('')
    setNotice('')
    try {
      await fn()
      if (message) setNotice(message)
      await load()
      onChanged?.()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy('')
    }
  }

  const num = (v: string) => Number(v)

  if (!booking) {
    return <Card title="Loading booking…">{error && <Alert kind="error">{error}</Alert>}</Card>
  }

  const s = booking.status
  const inTrip = s === 'TRIP_STARTED' || s === 'TRIP_RESUMED'

  return (
    <div className="stack">
      {error && <Alert kind="error">{error}</Alert>}
      {notice && <Alert kind="success">{notice}</Alert>}

      <Card title="Booking" action={<BookingStatusBadge status={s} />}>
        <div className="kv">
          <span className="k">Pickup</span>
          <span className="v">{booking.pickup_location}</span>
        </div>
        {booking.destination && (
          <div className="kv">
            <span className="k">Destination</span>
            <span className="v">{booking.destination}</span>
          </div>
        )}
        <div className="kv">
          <span className="k">Duration</span>
          <span className="v">{booking.duration_hours}h</span>
        </div>
        <div className="kv">
          <span className="k">Your fee</span>
          <span className="v">{rupees(booking.driver_fee)}</span>
        </div>
        <div className="kv">
          <span className="k">Reference</span>
          <span className="v mono">{shortId(booking.id)}</span>
        </div>
        <div className="kv">
          <span className="k">Booked</span>
          <span className="v">{timeAgo(booking.created_at)}</span>
        </div>
        {driver && (
          <div className="kv">
            <span className="k">Your name</span>
            <span className="v">{driver.rating.toFixed(1)} ★ · {driver.total_trips} trips</span>
          </div>
        )}
      </Card>

      {/* Assigned: the driver commits to the pickup point. */}
      {s === 'DRIVER_ASSIGNED' && (
        <button
          className="btn"
          disabled={busy === 'enroute'}
          onClick={() =>
            act('enroute', async () => {
              await api.markEnRoute(booking.id)
            }, 'On your way. Head to the pickup point.')
          }
        >
          {busy === 'enroute' ? <span className="spinner" /> : 'Head to pickup'}
        </button>
      )}

      {s === 'DRIVER_EN_ROUTE' && (
        <button
          className="btn"
          disabled={busy === 'arrive'}
          onClick={() =>
            act('arrive', async () => {
              await api.markArrived(booking.id)
            }, 'Marked as arrived. Ask the customer for their OTP.')
          }
        >
          {busy === 'arrive' ? <span className="spinner" /> : "I've arrived"}
        </button>
      )}

      {s === 'DRIVER_ARRIVED' && (
        <Alert kind="warn">
          Waiting for the customer's OTP. Start the trip once they have shared it.
        </Alert>
      )}

      {s === 'OTP_VERIFIED' && (
        <Card title="Start the trip">
          <Field label="Starting odometer (km)">
            <input
              inputMode="numeric"
              value={odo}
              onChange={(e) => setOdo(e.target.value.replace(/\D/g, ''))}
              placeholder="e.g. 48210"
            />
          </Field>
          <div style={{ height: 12 }} />
          <Field label="Fuel level (%)">
            <input
              inputMode="numeric"
              value={fuel}
              onChange={(e) => setFuel(e.target.value.replace(/\D/g, ''))}
              placeholder="e.g. 80"
            />
          </Field>
          <div style={{ height: 12 }} />
          <button
            className="btn"
            disabled={busy === 'start' || !odo || !fuel}
            onClick={() =>
              act(
                'start',
                async () => {
                  const started = await api.startTrip({
                    booking_id: booking.id,
                    start_odometer: num(odo),
                    start_fuel: num(fuel),
                  })
                  setTrip(started)
                },
                'Trip started. Location sharing is now live.',
              )
            }
          >
            {busy === 'start' ? <span className="spinner" /> : 'Start trip'}
          </button>
        </Card>
      )}

      {inTrip && (
        <div className="stack">
          {trip && <DriverLocationPublisher tripId={trip.id} />}
          <div className="btn-row">
            <button
              className="btn secondary"
              disabled={busy === 'pause' || !trip}
              onClick={() =>
                act(
                  'pause',
                  async () => {
                    if (!trip) return
                    await api.pauseTrip(trip.id)
                  },
                  'Trip paused.',
                )
              }
            >
              {busy === 'pause' ? <span className="spinner" /> : 'Pause'}
            </button>
            <button
              className="btn"
              disabled={busy === 'end' || !trip || !odo || !fuel}
              onClick={() =>
                act(
                  'end',
                  async () => {
                    if (!trip) return
                    await api.endTrip(trip.id, {
                      end_odometer: num(odo),
                      end_fuel: num(fuel),
                    })
                  },
                  'Trip ended. Payment has been captured.',
                )
              }
            >
              {busy === 'end' ? <span className="spinner" /> : 'End trip'}
            </button>
          </div>
          <Card title="Trip readings" tight>
            <Field label="Odometer (km)">
              <input
                inputMode="numeric"
                value={odo}
                onChange={(e) => setOdo(e.target.value.replace(/\D/g, ''))}
              />
            </Field>
            <div style={{ height: 10 }} />
            <Field label="Fuel level (%)">
              <input
                inputMode="numeric"
                value={fuel}
                onChange={(e) => setFuel(e.target.value.replace(/\D/g, ''))}
              />
            </Field>
          </Card>
        </div>
      )}

      {s === 'TRIP_PAUSED' && (
        <button
          className="btn"
          disabled={busy === 'resume' || !trip}
          onClick={() =>
            act(
              'resume',
              async () => {
                if (!trip) return
                await api.resumeTrip(trip.id)
              },
              'Trip resumed.',
            )
          }
        >
          {busy === 'resume' ? <span className="spinner" /> : 'Resume trip'}
        </button>
      )}

      {/* Declining after committing loses the booking; the customer has already
          been told a driver is on the way. */}
      {(s === 'DRIVER_ASSIGNED' || s === 'DRIVER_EN_ROUTE') && (
        <Card title="Cannot make it">
          <Field label="Reason">
            <input
              placeholder="Vehicle breakdown"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
            />
          </Field>
          <div style={{ height: 12 }} />
          <button
            className="btn ghost"
            disabled={busy === 'cancel' || !reason.trim()}
            onClick={() =>
              act(
                'cancel',
                async () => {
                  await api.driverCancelBooking(booking.id, reason.trim())
                },
                'Booking cancelled. It has been flagged for review.',
              )
            }
          >
            {busy === 'cancel' ? <span className="spinner" /> : 'Cancel booking'}
          </button>
        </Card>
      )}

      {s === 'TRIP_COMPLETED' && (
        <Card title="Trip complete">
          <div className="kv">
            <span className="k">Earned</span>
            <span className="v">{rupees(booking.driver_fee)}</span>
          </div>
          <div className="faint" style={{ marginTop: 8 }}>
            Settled in your next earnings payout.
          </div>
        </Card>
      )}
    </div>
  )
}

/**
 * Publishes the driver's position while a trip runs.
 *
 * The API only accepts updates from the assigned driver within a distance the
 * previous fix allows, so the browser's own position is sent rather than a
 * simulated one. If permission is denied the trip continues to work — the
 * customer's map simply stops advancing — and this reports that plainly instead
 * of failing silently.
 */
function DriverLocationPublisher({ tripId }: { tripId: string }) {
  const [state, setState] = useState<'idle' | 'live' | 'denied' | 'unsupported'>('idle')
  const [sent, setSent] = useState(0)

  useEffect(() => {
    if (typeof navigator === 'undefined' || !navigator.geolocation) {
      setState('unsupported')
      return
    }
    const id = window.setInterval(() => {
      navigator.geolocation.getCurrentPosition(
        (pos) => {
          const loc: Location & { speed: number; heading: number } = {
            latitude: pos.coords.latitude,
            longitude: pos.coords.longitude,
            speed: (pos.coords.speed ?? 0) * 3.6,
            heading: pos.coords.heading ?? 0,
          }
          void api
            .recordTripLocation(tripId, loc)
            .then(() => {
              setSent((n) => n + 1)
              setState('live')
            })
            .catch(() => setState('denied'))
        },
        () => setState('denied'),
        { enableHighAccuracy: true },
      )
    }, 10000)
    return () => window.clearInterval(id)
  }, [tripId])

  if (state === 'unsupported') {
    return (
      <Alert kind="warn">
        This browser cannot share location, so the customer will not see your route.
      </Alert>
    )
  }
  if (state === 'denied') {
    return (
      <Alert kind="warn">
        Location is blocked. Allow it in your browser settings so the customer can follow the trip.
      </Alert>
    )
  }
  return (
    <Alert kind="info">
      Sharing your location{sent > 0 ? ` · ${sent} update${sent === 1 ? '' : 's'} sent` : ''}
    </Alert>
  )
}
