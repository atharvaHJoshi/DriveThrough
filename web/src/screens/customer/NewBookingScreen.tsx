import { useCallback, useEffect, useState } from 'react'
import { api } from '../../api/client'
import type { Booking, PriceEstimate, Vehicle } from '../../api/types'
import { Alert, Card, Empty, Field, Loading, errorMessage, rupees } from '../../components/ui'

/**
 * Pickup coordinates.
 *
 * The backend stores lat/lng and does no geocoding: GOOGLE_MAPS_API_KEY is
 * parsed but unused, so there is no address lookup to call. The UI therefore lets
 * the customer type a place name and supply coordinates explicitly, which is also
 * honest about what the service can do.
 *
 * The preset below is Koramangala, Bengaluru — a dense area with drivers — so the
 * matching path actually finds candidates rather than silently returning zero.
 */
const PRESETS = [
  { label: 'Koramangala, Bengaluru', lat: 12.9352, lng: 77.6245 },
  { label: 'Indiranagar, Bengaluru', lat: 12.9784, lng: 77.6408 },
  { label: 'MG Road, Bengaluru', lat: 12.9756, lng: 77.6068 },
  { label: 'Whitefield, Bengaluru', lat: 12.9698, lng: 77.7500 },
]

export function NewBookingScreen({ onCreated }: { onCreated: (b: Booking) => void }) {
  const [vehicles, setVehicles] = useState<Vehicle[] | null>(null)
  const [vehicleId, setVehicleId] = useState('')
  const [hours, setHours] = useState(2)
  const [scheduled, setScheduled] = useState('')
  const [pickupLabel, setPickupLabel] = useState(PRESETS[0].label)
  const [pickup, setPickup] = useState({ lat: PRESETS[0].lat, lng: PRESETS[0].lng })
  const [destination, setDestination] = useState('')
  const [estimate, setEstimate] = useState<PriceEstimate | null>(null)
  const [busy, setBusy] = useState(false)
  const [estimating, setEstimating] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    api
      .listVehicles()
      .then((vs) => {
        setVehicles(vs)
        const preferred = vs.find((v) => v.is_default) ?? vs[0]
        if (preferred) setVehicleId(preferred.id)
      })
      .catch((err) => {
        setError(errorMessage(err))
        setVehicles([])
      })
  }, [])

  // The estimate carries the night surcharge, which depends on the pickup hour,
  // so a scheduled booking is quoted against its scheduled time.
  const refreshEstimate = useCallback(async () => {
    setEstimating(true)
    try {
      setEstimate(await api.estimate(hours, scheduled ? new Date(scheduled).toISOString() : undefined))
    } catch {
      setEstimate(null)
    } finally {
      setEstimating(false)
    }
  }, [hours, scheduled])

  useEffect(() => {
    void refreshEstimate()
  }, [refreshEstimate])

  async function submit() {
    if (!vehicleId) {
      setError('Add a vehicle before booking')
      return
    }
    setBusy(true)
    setError('')
    try {
      const booking = await api.createBooking({
        vehicle_id: vehicleId,
        booking_type: 'HOURLY',
        duration_hours: hours,
        pickup_location: pickupLabel,
        pickup_lat: pickup.lat,
        pickup_lng: pickup.lng,
        destination: destination.trim() || undefined,
        scheduled_at: scheduled ? new Date(scheduled).toISOString() : undefined,
      })
      onCreated(booking)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  if (vehicles === null) return <Loading label="Loading" />

  if (vehicles.length === 0) {
    return (
      <Empty
        icon="🚗"
        title="Add a vehicle first"
        hint="A booking is for a specific car, so the matcher knows which transmission and class to look for."
      />
    )
  }

  return (
    <div className="stack">
      {error && <Alert kind="error">{error}</Alert>}

      <Card title="Vehicle">
        <Field label="Booking for">
          <select value={vehicleId} onChange={(e) => setVehicleId(e.target.value)}>
            {vehicles.map((v) => (
              <option key={v.id} value={v.id}>
                {v.make} {v.model} · {v.registration_number} · {v.transmission}
              </option>
            ))}
          </select>
        </Field>
      </Card>

      <Card title="When">
        <Field label="Duration">
          <select value={hours} onChange={(e) => setHours(Number(e.target.value))}>
            {[1, 2, 3, 4, 6, 8, 12, 24].map((h) => (
              <option key={h} value={h}>
                {h} hour{h > 1 ? 's' : ''}
              </option>
            ))}
          </select>
        </Field>

        <div style={{ height: 12 }} />

        <Field label="Schedule" hint="Leave empty to start looking for a driver immediately.">
          <input
            type="datetime-local"
            value={scheduled}
            onChange={(e) => setScheduled(e.target.value)}
          />
        </Field>
      </Card>

      <Card title="Where">
        <Field label="Pickup">
          <select
            value={pickupLabel}
            onChange={(e) => {
              const p = PRESETS.find((x) => x.label === e.target.value)
              if (p) {
                setPickupLabel(p.label)
                setPickup({ lat: p.lat, lng: p.lng })
              }
            }}
          >
            {PRESETS.map((p) => (
              <option key={p.label} value={p.label}>
                {p.label}
              </option>
            ))}
          </select>
        </Field>
        <div className="faint mono" style={{ marginTop: 6 }}>
          {pickup.lat.toFixed(4)}, {pickup.lng.toFixed(4)}
        </div>

        <div style={{ height: 12 }} />

        <Field label="Destination" hint="Optional. Used as a note for the driver.">
          <input
            placeholder="Kempegowda Airport"
            value={destination}
            onChange={(e) => setDestination(e.target.value)}
          />
        </Field>
      </Card>

      <Card title="Fare estimate">
        {estimate ? (
          <>
            <div className="kv">
              <span className="k">Driver ({hours}h)</span>
              <span className="v">{rupees(estimate.driver_fee)}</span>
            </div>
            <div className="kv">
              <span className="k">Platform</span>
              <span className="v">{rupees(estimate.platform_fee)}</span>
            </div>
            <div className="kv">
              <span className="k">Protection</span>
              <span className="v">{rupees(estimate.protection_fee)}</span>
            </div>
            <div className="kv">
              <span className="k">GST</span>
              <span className="v">{rupees(estimate.tax)}</span>
            </div>
            <div className="total">
              <span className="muted">Total</span>
              <span className="amount">{rupees(estimate.total)}</span>
            </div>
            {scheduled && (
              <div className="faint" style={{ marginTop: 8 }}>
                Includes the night surcharge if pickup falls between 22:00 and 06:00.
              </div>
            )}
          </>
        ) : (
          <div className="faint">{estimating ? 'Calculating…' : 'No estimate available.'}</div>
        )}
      </Card>

      <button className="btn" onClick={submit} disabled={busy || !vehicleId}>
        {busy ? <span className="spinner" /> : scheduled ? 'Schedule booking' : 'Find a driver'}
      </button>

      <div className="faint" style={{ textAlign: 'center' }}>
        Payment is authorised now and captured when the trip ends.
      </div>
    </div>
  )
}
