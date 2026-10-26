import { useState } from 'react'
import { api } from '../../api/client'
import { Alert, Card, errorMessage } from '../../components/ui'

/** Post-trip rating. Shown once, and skipped entirely if the API says the
 *  customer has already rated this booking. */
export function RatingPrompt({ bookingId }: { bookingId: string }) {
  const [stars, setStars] = useState(0)
  const [review, setReview] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState(false)

  if (done) return null

  async function submit() {
    setBusy(true)
    setError('')
    try {
      await api.rateBooking(bookingId, stars, review.trim() || undefined)
      setDone(true)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card title="How was your trip?">
      {error && <Alert kind="error">{error}</Alert>}
      <div className="row" style={{ gap: 4 }}>
        {[1, 2, 3, 4, 5].map((n) => (
          <button
            key={n}
            className={`star ${n <= stars ? 'on' : ''}`}
            onClick={() => setStars(n)}
            aria-label={`${n} star${n > 1 ? 's' : ''}`}
          >
            ★
          </button>
        ))}
      </div>
      <div style={{ height: 12 }} />
      <textarea
        rows={3}
        placeholder="Anything you want the driver to know?"
        value={review}
        onChange={(e) => setReview(e.target.value)}
      />
      <div style={{ height: 12 }} />
      <button className="btn" disabled={busy || stars === 0} onClick={submit}>
        {busy ? <span className="spinner" /> : 'Submit rating'}
      </button>
    </Card>
  )
}