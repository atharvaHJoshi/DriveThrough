import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../../api/client'
import { TripSocket, initialTrackState } from '../../api/tripSocket'
import type { ConnectionState } from '../../api/tripSocket'
import type { Booking, Location, Payment, Trip } from '../../api/types'
import {
  Alert,
  BookingStatusBadge,
  Card,
  Field,
  PaymentStatusBadge,
  errorMessage,
  isLiveTracking,
  isTerminal,
  rupees,
  shortId,
  timeAgo,
} from '../../components/ui'
import { TrackMap } from './TrackMap'
import { RatingPrompt } from './RatingPrompt'
import { SosButton } from './SosButton'

/**
 * Customer's view of one booking, covering the whole lifecycle: waiting for a
 * driver, the OTP handover, live tracking, payment, and rating.
 */
export function BookingDetailScreen({
  bookingId,
  onChanged,
}: {
  bookingId: string
  onChanged?: () => void
}) {
  const [booking, setBooking] = useState<Booking | null>(null)
  const [trip, setTrip] = useState<Trip | null>(null)
  const [payment, setPayment] = useState<Payment | null>(null)
  const [mockGateway, setMockGateway] = useState(false)
  const [otp, setOtp] = useState('')
  const [track, setTrack] = useState(initialTrackState)
  const [conn, setConn] = useState<ConnectionState>('closed')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const socketRef = useRef<TripSocket | null>(null)

  const load = useCallback(async () => {
    try {
      const res = await api.getBooking(bookingId)
      setBooking(res.booking)
      try {
        setPayment(await api.getPayment(bookingId))
      } catch {
        // No payment row yet is the normal case before Initiate.
        setPayment(null)
      }
      // The socket is addressed by trip id and the booking does not carry one,
      // so it is resolved here. 404 simply means the trip has not started yet.
      if (isLiveTracking(res.booking.status)) {
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

  // Poll while the booking is live so the status advances even when the customer
  // has no WebSocket connection (backgrounded tab, reconnecting socket).
  const pollable = booking !== null && !isTerminal(booking.status)
  useEffect(() => {
    if (!pollable) return
    const id = window.setInterval(() => void load(), 5000)
    return () => window.clearInterval(id)
  }, [pollable, load])

  // Subscribe to live location once the driver is on the way. The trip id is
  // resolved in load(); without it there is nothing to stream.
  useEffect(() => {
    if (!booking || !trip || !isLiveTracking(booking.status)) {
      socketRef.current?.close()
      socketRef.current = null
      return
    }
    socketRef.current?.close()
    const socket = new TripSocket({
      tripId: trip.id,
      bookingId: booking.id,
      onState: setConn,
      onEvent: (ev) => {
        if (ev.type === 'trip.location') {
          setTrack((prev) => {
            const loc: Location = {
              latitude: ev.location.latitude,
              longitude: ev.location.longitude,
            }
            const path = [...prev.path, loc]
            // Bound the polyline; a long trip would otherwise grow this array
            // without limit for the life of the component.
            return {
              latest: loc,
              path: path.slice(-500),
              updatedAt: ev.location.timestamp,
            }
          })
        } else if (ev.type === 'trip.ended') {
          setNotice('Trip ended')
          void load()
        }
      },
    })
    socket.connect()
    socketRef.current = socket
    return () => {
      socket.close()
      socketRef.current = null
    }
  }, [booking, trip, load])

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

  const needsOtp = booking?.status === 'DRIVER_ARRIVED'
  const canCancel = booking !== null && !isTerminal(booking.status) && !booking.scheduled_at

  const totals = useMemo(() => {
    if (!booking) return null
    return {
      driver: booking.driver_fee,
      platform: booking.platform_fee,
      protection: booking.protection_fee,
      tax: booking.tax,
      total: booking.final_price ?? booking.estimated_price,
    }
  }, [booking])

  if (!booking) {
    return <Card title="Loading booking…">{error && <Alert kind="error">{error}</Alert>}</Card>
  }

  return (
    <div className="stack">
      {error && <Alert kind="error">{error}</Alert>}
      {notice && <Alert kind="success">{notice}</Alert>}

      <Card
        title="Booking"
        action={<BookingStatusBadge status={booking.status} />}
      >
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
          <span className="k">Reference</span>
          <span className="v mono">{shortId(booking.id)}</span>
        </div>
        <div className="kv">
          <span className="k">Created</span>
          <span className="v">{timeAgo(booking.created_at)}</span>
        </div>
        {booking.scheduled_at && (
          <div className="kv">
            <span className="k">Scheduled for</span>
            <span className="v">{new Date(booking.scheduled_at).toLocaleString()}</span>
          </div>
        )}
      </Card>

      {/* Searching has no driver yet, so show why rather than an empty screen. */}
      {(booking.status === 'SEARCHING' || booking.status === 'REQUESTED') && (
        <Alert kind="info">
          Looking for a driver nearby. This usually takes under a minute; if nobody accepts, the
          search retries automatically.
        </Alert>
      )}

      {isLiveTracking(booking.status) && (
        <Card title="Live tracking" action={<span className={`badge ${conn === 'open' ? 'green' : 'amber'}`}>{conn}</span>}>
          <TrackMap
            pickup={{ latitude: booking.pickup_lat, longitude: booking.pickup_lng }}
            path={track.path}
            connection={conn}
          />
          {conn !== 'open' && (
            <div className="faint" style={{ marginTop: 8 }}>
              Location updates stream once the trip starts.
            </div>
          )}
        </Card>
      )}

      {/* Handover: the customer must read the code to the driver, and the code
          arrives by SMS, so it is never displayed here. */}
      {needsOtp && (
        <Card title="Verify the handover">
          <Alert kind="warn">
            Your driver has arrived. Check the SMS for your verification code and share it with
            them to begin the trip.
          </Alert>
          <div style={{ height: 12 }} />
          <Field label="Verification code">
            <input
              className="otp-input"
              inputMode="numeric"
              maxLength={8}
              value={otp}
              onChange={(e) => setOtp(e.target.value.replace(/\D/g, ''))}
              placeholder="••••••"
            />
          </Field>
          <div style={{ height: 12 }} />
          <button
            className="btn"
            disabled={busy === 'otp' || otp.length < 4}
            onClick={() =>
              act('otp', async () => {
                await api.verifyHandoverOtp(booking.id, otp)
                setOtp('')
              }, 'Code accepted. The driver can start the trip.')
            }
          >
            {busy === 'otp' ? <span className="spinner" /> : 'Verify code'}
          </button>
        </Card>
      )}

      {canCancel && (
        <button
          className="btn ghost"
          disabled={busy === 'cancel'}
          onClick={() =>
            act('cancel', async () => {
              await api.cancelBooking(booking.id, 'customer cancelled')
            }, 'Booking cancelled. Any payment hold has been released.')
          }
        >
          {busy === 'cancel' ? <span className="spinner" /> : 'Cancel booking'}
        </button>
      )}

      {/* Authorisation happens before the trip so the driver is assured of pay;
          capture happens server-side at trip end. */}
      {!payment && !isTerminal(booking.status) && (
        <Card title="Payment">
          <p className="muted" style={{ marginTop: 0 }}>
            Authorise {rupees(totals?.total ?? booking.estimated_price)} now. You are only charged
            when the trip completes.
          </p>
          <button
            className="btn"
            disabled={busy === 'pay'}
            onClick={() =>
              act(
                'pay',
                async () => {
                  const result = await api.initiatePayment(booking.id)
                  setMockGateway(result.is_mock)
                },
                'Payment authorised.',
              )
            }
          >
            {busy === 'pay' ? <span className="spinner" /> : 'Set up payment'}
          </button>
        </Card>
      )}

      {payment && (
        <Card title="Payment" action={<PaymentStatusBadge status={payment.status} />}>
          <div className="kv">
            <span className="k">Amount</span>
            <span className="v">{rupees(payment.amount)}</span>
          </div>
          <div className="kv">
            <span className="k">Method</span>
            <span className="v">{payment.method ?? '—'}</span>
          </div>
          {mockGateway && (
            <div className="faint" style={{ marginTop: 8 }}>
              Mock gateway: no Razorpay key is configured, so no real charge is made.
            </div>
          )}
        </Card>
      )}

      {booking.status === 'TRIP_COMPLETED' && <RatingPrompt bookingId={booking.id} />}

      {booking.status === 'TRIP_STARTED' && (
        <Card title="Fare">
          <div className="kv">
            <span className="k">Driver</span>
            <span className="v">{rupees(booking.driver_fee)}</span>
          </div>
          <div className="kv">
            <span className="k">Platform</span>
            <span className="v">{rupees(booking.platform_fee)}</span>
          </div>
          <div className="kv">
            <span className="k">Protection</span>
            <span className="v">{rupees(booking.protection_fee)}</span>
          </div>
          <div className="kv">
            <span className="k">GST</span>
            <span className="v">{rupees(booking.tax)}</span>
          </div>
          <div className="total">
            <span className="muted">Total</span>
            <span className="amount">{rupees(totals?.total ?? 0)}</span>
          </div>
        </Card>
      )}

      {isLiveTracking(booking.status) && (
        <SosButton
          tripId={trip?.id ?? null}
          fallback={
            track.latest ?? { latitude: booking.pickup_lat, longitude: booking.pickup_lng }
          }
        />
      )}
    </div>
  )
}
