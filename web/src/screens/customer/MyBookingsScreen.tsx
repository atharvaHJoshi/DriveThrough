import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '../../api/client'
import type { Booking } from '../../api/types'
import {
  BookingStatusBadge,
  Card,
  Empty,
  Loading,
  errorMessage,
  isTerminal,
  rupees,
  shortId,
  timeAgo,
} from '../../components/ui'

/**
 * The customer's home: every booking, newest first, with the active one called
 * out so it is one tap away. Polls while anything is in flight so a status change
 * surfaces without a manual refresh.
 */
export function MyBookingsScreen() {
  const navigate = useNavigate()
  const [bookings, setBookings] = useState<Booking[] | null>(null)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      setBookings(await api.listCustomerBookings())
      setError('')
    } catch (err) {
      setError(errorMessage(err))
      setBookings([])
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    if (!bookings?.some((b) => !isTerminal(b.status))) return
    const id = window.setInterval(() => void load(), 8000)
    return () => window.clearInterval(id)
  }, [bookings, load])

  if (bookings === null) return <Loading label="Loading bookings" />

  const active = bookings.filter((b) => !isTerminal(b.status))
  const past = bookings.filter((b) => isTerminal(b.status))

  return (
    <div className="stack">
      {error && <Card title="Could not load bookings">{error}</Card>}

      <button className="btn" onClick={() => navigate('/bookings/new')}>
        + Book a driver
      </button>

      {bookings.length === 0 && (
        <Empty
          icon="🚗"
          title="No bookings yet"
          hint="Book a professional driver for your own car — hourly, one-way, or round trip."
        />
      )}

      {active.length > 0 && (
        <Card title="Active">
          <div className="stack sm">
            {active.map((b) => (
              <BookingRow key={b.id} booking={b} onClick={() => navigate(`/bookings/${b.id}`)} />
            ))}
          </div>
        </Card>
      )}

      {past.length > 0 && (
        <Card title="Past">
          <div className="stack sm">
            {past.map((b) => (
              <BookingRow key={b.id} booking={b} onClick={() => navigate(`/bookings/${b.id}`)} />
            ))}
          </div>
        </Card>
      )}
    </div>
  )
}

function BookingRow({ booking, onClick }: { booking: Booking; onClick: () => void }) {
  return (
    <button className="btn ghost" style={{ justifyContent: 'space-between' }} onClick={onClick}>
      <span style={{ textAlign: 'left' }}>
        <span style={{ display: 'block' }}>{booking.pickup_location}</span>
        <span className="faint">
          {shortId(booking.id)} · {timeAgo(booking.created_at)} · {rupees(booking.final_price ?? booking.estimated_price)}
        </span>
      </span>
      <BookingStatusBadge status={booking.status} />
    </button>
  )
}
