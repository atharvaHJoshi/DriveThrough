import { useCallback, useEffect, useState } from 'react'
import { api } from '../../api/client'
import type { FuelType, Transmission, Vehicle, VehicleClass } from '../../api/types'
import { Alert, Card, Empty, Field, Loading, errorMessage, rupees } from '../../components/ui'

const TRANSMISSIONS: Transmission[] = ['MANUAL', 'AUTOMATIC']
const FUEL_TYPES: FuelType[] = ['PETROL', 'DIESEL', 'CNG', 'ELECTRIC', 'HYBRID']
const CLASSES: VehicleClass[] = ['HATCHBACK', 'SEDAN', 'SUV', 'LUXURY', 'MUV']

const EMPTY_FORM = {
  registration_number: '',
  make: '',
  model: '',
  year: String(new Date().getFullYear()),
  transmission: 'MANUAL' as Transmission,
  fuel_type: 'PETROL' as FuelType,
  class: 'HATCHBACK' as VehicleClass,
}

export function VehiclesScreen() {
  const [vehicles, setVehicles] = useState<Vehicle[] | null>(null)
  const [form, setForm] = useState(EMPTY_FORM)
  const [showForm, setShowForm] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      setVehicles(await api.listVehicles())
    } catch (err) {
      setError(errorMessage(err))
      setVehicles([])
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function create() {
    const year = Number(form.year)
    if (!form.registration_number.trim() || !form.make.trim() || !form.model.trim()) {
      setError('Registration, make, and model are required')
      return
    }
    if (!Number.isInteger(year) || year < 1990 || year > new Date().getFullYear() + 1) {
      setError('Enter a valid year')
      return
    }
    setBusy(true)
    setError('')
    try {
      await api.createVehicle({ ...form, year, registration_number: form.registration_number.toUpperCase() })
      setForm(EMPTY_FORM)
      setShowForm(false)
      await load()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  async function makeDefault(v: Vehicle) {
    setError('')
    try {
      await api.setDefaultVehicle(v.id)
      await load()
    } catch (err) {
      setError(errorMessage(err))
    }
  }

  async function remove(v: Vehicle) {
    setError('')
    try {
      await api.deleteVehicle(v.id)
      await load()
    } catch (err) {
      setError(errorMessage(err))
    }
  }

  if (vehicles === null) return <Loading label="Loading vehicles" />

  return (
    <div className="stack">
      {error && <Alert kind="error">{error}</Alert>}

      {vehicles.length === 0 && !showForm && (
        <Empty
          icon="🚗"
          title="No vehicles yet"
          hint="Add the car you want a driver for. Matching picks drivers by class and transmission."
        />
      )}

      {vehicles.map((v) => (
        <Card key={v.id} tight>
          <div className="row between">
            <div>
              <div className="row" style={{ gap: 7 }}>
                <strong>{v.make} {v.model}</strong>
                {v.is_default && <span className="badge blue">Default</span>}
              </div>
              <div className="faint mono" style={{ marginTop: 3 }}>
                {v.registration_number} · {v.class} · {v.transmission} · {v.fuel_type} · {v.year}
              </div>
            </div>
            <div className="row" style={{ gap: 6 }}>
              {!v.is_default && (
                <button className="btn ghost sm" onClick={() => makeDefault(v)}>
                  Default
                </button>
              )}
              <button className="btn ghost sm" onClick={() => remove(v)} aria-label="Remove vehicle">
                ✕
              </button>
            </div>
          </div>
        </Card>
      ))}

      {showForm ? (
        <Card title="Add a vehicle">
          <div className="stack">
            <Field label="Registration number">
              <input
                placeholder="KA01AB1234"
                value={form.registration_number}
                onChange={(e) => setForm({ ...form, registration_number: e.target.value })}
              />
            </Field>
            <div className="row" style={{ gap: 10 }}>
              <Field label="Make">
                <input
                  placeholder="Maruti"
                  value={form.make}
                  onChange={(e) => setForm({ ...form, make: e.target.value })}
                />
              </Field>
              <Field label="Model">
                <input
                  placeholder="Swift"
                  value={form.model}
                  onChange={(e) => setForm({ ...form, model: e.target.value })}
                />
              </Field>
            </div>
            <div className="row" style={{ gap: 10 }}>
              <Field label="Year">
                <input
                  inputMode="numeric"
                  value={form.year}
                  onChange={(e) => setForm({ ...form, year: e.target.value.replace(/\D/g, '') })}
                />
              </Field>
              <Field label="Class">
                <select
                  value={form.class}
                  onChange={(e) => setForm({ ...form, class: e.target.value as VehicleClass })}
                >
                  {CLASSES.map((c) => (
                    <option key={c} value={c}>{c}</option>
                  ))}
                </select>
              </Field>
            </div>
            <div className="row" style={{ gap: 10 }}>
              <Field label="Transmission">
                <select
                  value={form.transmission}
                  onChange={(e) =>
                    setForm({ ...form, transmission: e.target.value as Transmission })
                  }
                >
                  {TRANSMISSIONS.map((t) => (
                    <option key={t} value={t}>{t}</option>
                  ))}
                </select>
              </Field>
              <Field label="Fuel">
                <select
                  value={form.fuel_type}
                  onChange={(e) => setForm({ ...form, fuel_type: e.target.value as FuelType })}
                >
                  {FUEL_TYPES.map((f) => (
                    <option key={f} value={f}>{f}</option>
                  ))}
                </select>
              </Field>
            </div>
            <div className="btn-row">
              <button className="btn secondary" onClick={() => setShowForm(false)} disabled={busy}>
                Cancel
              </button>
              <button className="btn" onClick={create} disabled={busy}>
                {busy ? <span className="spinner" /> : 'Save vehicle'}
              </button>
            </div>
          </div>
        </Card>
      ) : (
        <button className="btn secondary" onClick={() => setShowForm(true)}>
          + Add vehicle
        </button>
      )}

      {vehicles.length > 0 && (
        <div className="faint" style={{ textAlign: 'center' }}>
          Bookings use {vehicles.find((v) => v.is_default)?.registration_number ?? 'no default vehicle'}.
          The default is set on the first vehicle you add.
        </div>
      )}
      <div className="faint" style={{ textAlign: 'center' }}>
        Fares start from {rupees(0)} plus driver, platform, protection, and GST.
      </div>
    </div>
  )
}
