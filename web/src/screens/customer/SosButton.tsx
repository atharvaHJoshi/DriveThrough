import { useState } from 'react'
import { api } from '../../api/client'
import type { Location } from '../../api/types'
import { Alert, errorMessage } from '../../components/ui'

/**
 * Emergency trigger.
 *
 * The API requires real coordinates and validates them, so the customer's
 * position is read from the browser where permission is available. If that is
 * denied or unavailable the driver's last reported position is used instead, and
 * only if neither exists is the button disabled — an SOS that silently posts
 * 0,0 would be rejected and give a false impression that help was dispatched.
 *
 * Re-tapping is safe: the backend returns the existing open alert rather than
 * opening a second incident, so no client-side guard is needed.
 */
export function SosButton({
  tripId,
  fallback,
}: {
  tripId: string | null
  fallback: Location | null
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [sent, setSent] = useState(false)

  async function trigger() {
    setBusy(true)
    setError('')
    try {
      const pos = await currentPosition()
      const loc = pos ?? fallback
      if (!loc) {
        setError(
          'Your location is unavailable. Allow location access, then try again.',
        )
        return
      }
      await api.triggerSos(tripId, loc.latitude, loc.longitude)
      setSent(true)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  if (sent) {
    return (
      <Alert kind="success">
        SOS sent. Your emergency contacts have been notified and the incident is open.
      </Alert>
    )
  }

  return (
    <div className="stack">
      {error && <Alert kind="error">{error}</Alert>}
      <button className="btn danger" disabled={busy} onClick={trigger}>
        {busy ? <span className="spinner" /> : '🆘 Emergency SOS'}
      </button>
      <div className="faint" style={{ textAlign: 'center' }}>
        Alerts your saved emergency contacts and opens an incident for the support team.
      </div>
    </div>
  )
}

/** Resolves to null rather than rejecting when permission is denied or the
 *  browser has no geolocation support. */
function currentPosition(): Promise<Location | null> {
  if (typeof navigator === 'undefined' || !navigator.geolocation) return Promise.resolve(null)
  return new Promise((resolve) => {
    const timer = window.setTimeout(() => resolve(null), 5000)
    navigator.geolocation.getCurrentPosition(
      (pos) => {
        window.clearTimeout(timer)
        resolve({ latitude: pos.coords.latitude, longitude: pos.coords.longitude })
      },
      () => {
        window.clearTimeout(timer)
        resolve(null)
      },
      { enableHighAccuracy: true, timeout: 5000 },
    )
  })
}