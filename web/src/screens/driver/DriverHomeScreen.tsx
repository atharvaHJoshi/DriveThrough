import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ApiError, api } from '../../api/client'
import type { Booking, Driver, Offer, OfferBooking, VehicleCapability } from '../../api/types'
import {
  Alert,
  Card,
  DriverStatusBadge,
  Empty,
  Field,
  Loading,
  errorMessage,
  rupees,
} from '../../components/ui'

const ACTIVE_STATUSES = [
  'DRIVER_ASSIGNED',
  'DRIVER_EN_ROUTE',
  'DRIVER_ARRIVED',
  'OTP_VERIFIED',
  'TRIP_STARTED',
  'TRIP_PAUSED',
  'TRIP_RESUMED',
]

const DEFAULT_CAPABILITIES: VehicleCapability = {
  manual: true,
  automatic: true,
  hatchback: true,
  sedan: true,
  suv: false,
  luxury: false,
}

/**
 * Driver landing screen: availability, live offers, the current job, and
 * earnings. Offers are polled rather than pushed, because in-app push requires a
 * service worker that is out of scope here and the offer window is short.
 */
export function DriverHomeScreen() {
  const navigate = useNavigate()
  const [driver, setDriver] = useState<Driver | null>(null)
  const [registered, setRegistered] = useState(true)
  const [offers, setOffers] = useState<Offer[]>([])
  const [active, setActive] = useState<Booking | null>(null)
  const [recent, setRecent] = useState<Booking[]>([])
  const [earnings, setEarnings] = useState(0)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      let profile: Driver
      try {
        profile = await api.driverProfile()
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) {
          setRegistered(false)
          setDriver(null)
          return
        }
        throw err
      }
      setRegistered(true)
      setDriver(profile)
      const [offers, current, history, pay] = await Promise.all([
        api.listOffers().catch(() => []),
        api.currentBooking().catch(() => null),
        api.listDriverBookings().catch(() => []),
        api.driverEarnings().catch(() => ({ total: 0, entries: null, limit: 0, offset: 0 })),
      ])
      setOffers(offers ?? [])
      setActive(current)
      setRecent((history ?? []).slice(0, 8))
      setEarnings(pay.total)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  // Offers expire in seconds, so poll frequently while the driver is online.
  useEffect(() => {
    if (driver?.status !== 'ONLINE') return
    const id = window.setInterval(() => void load(), 8000)
    return () => window.clearInterval(id)
  }, [driver?.status, load])

  async function act(name: string, fn: () => Promise<unknown>) {
    setBusy(name)
    setError('')
    try {
      await fn()
      await load()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy('')
    }
  }

  if (loading) return <Loading label="Loading" />

  if (!registered) {
    return <DriverRegistration onDone={load} />
  }

  const online = driver?.status === 'ONLINE'

  return (
    <div className="stack">
      {error && <Alert kind="error">{error}</Alert>}

      <Card
        title="Status"
        action={driver && <DriverStatusBadge status={driver.status} />}
      >
        <div className="kv">
          <span className="k">Verification</span>
          <span className="v">{driver?.verification_status}</span>
        </div>
        <div className="kv">
          <span className="k">Rating</span>
          <span className="v">
            {driver?.rating.toFixed(1)} ★ · {driver?.total_trips} trips
          </span>
        </div>
        <div className="kv">
          <span className="k">Earnings</span>
          <span className="v">{rupees(earnings)}</span>
        </div>
        <div style={{ height: 12 }} />
        <button
          className={`btn ${online ? 'secondary' : 'success'}`}
          disabled={busy === 'avail'}
          onClick={() =>
            act('avail', async () => {
              await (online ? api.goOffline() : api.goOnline())
            })
          }
        >
          {busy === 'avail' ? <span className="spinner" /> : online ? 'Go offline' : 'Go online'}
        </button>
        {online && <OnlineLocationPublisher />}
      </Card>

      {active && ACTIVE_STATUSES.includes(active.status) && (
        <Card title="Current job" action={<span className="badge blue">Active</span>}>
          <div className="kv">
            <span className="k">Pickup</span>
            <span className="v">{active.pickup_location}</span>
          </div>
          <div className="kv">
            <span className="k">Status</span>
            <span className="v">{active.status}</span>
          </div>
          <div className="kv">
            <span className="k">Fee</span>
            <span className="v">{rupees(active.driver_fee)}</span>
          </div>
          <div style={{ height: 12 }} />
          <button className="btn" onClick={() => navigate(`/driver/bookings/${active.id}`)}>
            Open job
          </button>
        </Card>
      )}

      <Card title={`Offers${offers.length ? ` (${offers.length})` : ''}`}>
        {!online ? (
          <div className="faint">Go online to receive offers near you.</div>
        ) : offers.length === 0 ? (
          <div className="faint">No offers right now. New requests appear here automatically.</div>
        ) : (
          <div className="stack">
            {offers.map((o) => (
              <OfferCard
                key={o.id}
                offer={o}
                busy={busy}
                onAccept={() =>
                  act(
                    `accept:${o.id}`,
                    async () => {
                      await api.acceptOffer(o.booking_id, o.id)
                      navigate(`/driver/bookings/${o.booking_id}`)
                    },
                  )
                }
                onReject={() =>
                  act(`reject:${o.id}`, async () => {
                    await api.rejectOffer(o.booking_id, o.id)
                  })
                }
              />
            ))}
          </div>
        )}
      </Card>

      <Card title="Recent jobs">
        {recent.length === 0 ? (
          <Empty icon="🧭" title="No jobs yet" hint="Accepted bookings and completed trips show up here." />
        ) : (
          <div className="stack sm">
            {recent.map((b) => (
              <button
                key={b.id}
                className="btn ghost"
                style={{ justifyContent: 'space-between' }}
                onClick={() => navigate(`/driver/bookings/${b.id}`)}
              >
                <span>{b.pickup_location}</span>
                <span className="faint">{b.status}</span>
              </button>
            ))}
          </div>
        )}
      </Card>
    </div>
  )
}

function OfferCard({
  offer,
  busy,
  onAccept,
  onReject,
}: {
  offer: Offer
  busy: string
  onAccept: () => void
  onReject: () => void
}) {
  const remaining = useCountdown(offer.expires_at)
  const b: OfferBooking = offer.booking

  return (
    <div className="card tight">
      <div className="row between">
        <strong>{b.pickup_location}</strong>
        <span className={`offer-timer ${remaining <= 5 ? 'urgent' : ''}`}>{remaining}s</span>
      </div>
      <div className="faint" style={{ marginTop: 4 }}>
        {b.duration_hours}h · {rupees(b.driver_fee)}
        {b.destination ? ` · to ${b.destination}` : ''}
      </div>
      <div className="btn-row" style={{ marginTop: 10 }}>
        <button className="btn ghost" disabled={busy === `reject:${offer.id}`} onClick={onReject}>
          {busy === `reject:${offer.id}` ? <span className="spinner" /> : 'Decline'}
        </button>
        <button className="btn" disabled={busy === `accept:${offer.id}`} onClick={onAccept}>
          {busy === `accept:${offer.id}` ? <span className="spinner" /> : 'Accept'}
        </button>
      </div>
    </div>
  )
}

/** Seconds left before an offer expires, ticking down once per second. */
function useCountdown(expiresAt: string): number {
  const target = new Date(expiresAt).getTime()
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [])
  return Math.max(0, Math.round((target - now) / 1000))
}

/** Publishes the driver's position while online so matching can find them. */
function OnlineLocationPublisher() {
  useEffect(() => {
    if (typeof navigator === 'undefined' || !navigator.geolocation) return
    const push = () => {
      navigator.geolocation.getCurrentPosition(
        (pos) => {
          void api.updateDriverLocation(pos.coords.latitude, pos.coords.longitude).catch(() => {})
        },
        () => {},
        { enableHighAccuracy: true },
      )
    }
    push()
    const id = window.setInterval(push, 30000)
    return () => window.clearInterval(id)
  }, [])
  return (
    <div className="faint" style={{ marginTop: 8 }}>
      Sharing your location so nearby requests reach you.
    </div>
  )
}

function DriverRegistration({ onDone }: { onDone: () => Promise<void> }) {
  const [years, setYears] = useState(2)
  const [caps, setCaps] = useState<VehicleCapability>(DEFAULT_CAPABILITIES)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const toggle = (key: keyof VehicleCapability) => setCaps((c) => ({ ...c, [key]: !c[key] }))

  async function submit() {
    setBusy(true)
    setError('')
    try {
      await api.registerDriver(years, caps)
      await onDone()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  const LABELS: Record<keyof VehicleCapability, string> = {
    manual: 'Manual',
    automatic: 'Automatic',
    hatchback: 'Hatchback',
    sedan: 'Sedan',
    suv: 'SUV',
    luxury: 'Luxury',
  }

  return (
    <div className="stack">
      <Card title="Become a driver">
        <Alert kind="info">
          Register to start receiving driving requests. You can update these any time.
        </Alert>
        <div style={{ height: 12 }} />
        <Field label="Experience (years)">
          <input
            inputMode="numeric"
            value={String(years)}
            onChange={(e) => setYears(Number(e.target.value.replace(/\D/g, '') || '0'))}
          />
        </Field>
        <div style={{ height: 12 }} />
        <label>What can you drive?</label>
        <div className="row wrap" style={{ gap: 8 }}>
          {(Object.keys(LABELS) as (keyof VehicleCapability)[]).map((k) => (
            <button
              key={k}
              className={`btn sm ${caps[k] ? '' : 'secondary'}`}
              onClick={() => toggle(k)}
            >
              {LABELS[k]}
            </button>
          ))}
        </div>
      </Card>
      {error && <Alert kind="error">{error}</Alert>}
      <button className="btn" disabled={busy} onClick={submit}>
        {busy ? <span className="spinner" /> : 'Register as a driver'}
      </button>
    </div>
  )
}
