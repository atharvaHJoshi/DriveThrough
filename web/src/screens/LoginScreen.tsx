import { useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import { useAuth } from '../state/AuthContext'
import { Alert, Field } from '../components/ui'

type Role = 'CUSTOMER' | 'DRIVER'

export function LoginScreen() {
  const { login } = useAuth()
  const [role, setRole] = useState<Role>('CUSTOMER')
  const [step, setStep] = useState<'phone' | 'code'>('phone')
  const [phone, setPhone] = useState('')
  const [name, setName] = useState('')
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [info, setInfo] = useState('')
  const codeRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (step === 'code') codeRef.current?.focus()
  }, [step])

  // Indian mobile numbers: 10 digits starting 6-9. Checked client-side only to
  // catch typos early; the server remains the authority.
  const phoneValid = /^[6-9]\d{9}$/.test(phone)

  async function requestCode() {
    if (!phoneValid) {
      setError('Enter a 10-digit mobile number starting 6-9')
      return
    }
    setBusy(true)
    setError('')
    try {
      await api.sendOtp(phone)
      setStep('code')
      setInfo(
        'Code sent. With SMS_PROVIDER=log it is printed in the API console, not texted.',
      )
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not send the code')
    } finally {
      setBusy(false)
    }
  }

  async function submit() {
    if (code.trim().length < 4) {
      setError('Enter the code you received')
      return
    }
    setBusy(true)
    setError('')
    try {
      // The backend stores the name at login, so a sensible placeholder is
      // better than an empty string: the driver reads it back at pickup.
      const displayName = name.trim() || (role === 'DRIVER' ? 'Driver' : 'Guest')
      await login(phone, code.trim(), displayName, role)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'That code did not work')
      setBusy(false)
    }
  }

  return (
    <div className="app">
      <div className="app-body" style={{ paddingTop: 40 }}>
        <div style={{ textAlign: 'center', marginBottom: 26 }}>
          <div style={{ fontSize: 34, marginBottom: 6 }}>🚗</div>
          <h1>DriveThrough</h1>
          <p className="muted" style={{ margin: '6px 0 0' }}>
            {role === 'CUSTOMER'
              ? 'Book a driver by the hour or by the trip'
              : 'Go online, accept rides, track earnings'}
          </p>
        </div>

        <div className="row" style={{ marginBottom: 16 }}>
          {(['CUSTOMER', 'DRIVER'] as Role[]).map((r) => (
            <button
              key={r}
              className={`btn ${role === r ? '' : 'secondary'}`}
              onClick={() => {
                setRole(r)
                setError('')
              }}
            >
              {r === 'CUSTOMER' ? 'I need a driver' : 'I am a driver'}
            </button>
          ))}
        </div>

        {error && (
          <div style={{ marginBottom: 12 }}>
            <Alert kind="error">{error}</Alert>
          </div>
        )}
        {info && (
          <div style={{ marginBottom: 12 }}>
            <Alert kind="info">{info}</Alert>
          </div>
        )}

        {step === 'phone' ? (
          <div className="card stack">
            <Field label="Mobile number">
              <input
                inputMode="numeric"
                autoComplete="tel"
                placeholder="9876543210"
                value={phone}
                maxLength={10}
                onChange={(e) => setPhone(e.target.value.replace(/\D/g, ''))}
                onKeyDown={(e) => e.key === 'Enter' && requestCode()}
              />
            </Field>

            <Field label="Your name" hint="Used by drivers to identify you at pickup.">
              <input
                placeholder={role === 'CUSTOMER' ? 'Your name' : 'Your full name'}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </Field>

            <button className="btn" disabled={busy || !phoneValid} onClick={requestCode}>
              {busy ? <span className="spinner" /> : 'Send code'}
            </button>
          </div>
        ) : (
          <div className="card stack fade-in">
            <Field label={`Code sent to +91 ${phone}`}>
              <input
                ref={codeRef}
                className="otp-input"
                inputMode="numeric"
                autoComplete="one-time-code"
                maxLength={10}
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
                onKeyDown={(e) => e.key === 'Enter' && submit()}
              />
            </Field>

            <button className="btn" disabled={busy || code.length < 4} onClick={submit}>
              {busy ? <span className="spinner" /> : 'Verify and continue'}
            </button>
            <button
              className="btn ghost"
              disabled={busy}
              onClick={() => {
                setStep('phone')
                setCode('')
                setInfo('')
              }}
            >
              Use a different number
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
