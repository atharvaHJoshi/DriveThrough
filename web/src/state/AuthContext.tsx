import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { ApiError, api, setSessionExpiredHandler, tokenStore } from '../api/client'
import type { TokenPair, User } from '../api/types'

interface AuthState {
  user: User | null
  loading: boolean
  login: (phone: string, code: string, name: string, role: 'CUSTOMER' | 'DRIVER') => Promise<User>
  logout: () => Promise<void>
  refreshUser: () => Promise<void>
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(() => tokenStore.user())
  const [loading, setLoading] = useState(true)

  // A refresh attempt that ultimately fails clears the session from inside the
  // client; the shell has to react or it keeps rendering a signed-in shell over
  // a user who is no longer authenticated.
  useEffect(() => {
    setSessionExpiredHandler(() => setUser(null))
    return tokenStore.subscribe(() => setUser(tokenStore.user()))
  }, [])

  // Re-validate a restored session on load. A token in localStorage can be
  // expired or revoked by a logout in another tab, so trusting it blindly would
  // show a signed-in shell that fails on the first request.
  useEffect(() => {
    let cancelled = false
    async function bootstrap() {
      if (!tokenStore.isAuthenticated()) {
        if (!cancelled) setLoading(false)
        return
      }
      try {
        const me = await api.me()
        tokenStore.saveUser(me)
        if (!cancelled) setUser(me)
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) tokenStore.clear()
        if (!cancelled) setUser(null)
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void bootstrap()
    return () => {
      cancelled = true
    }
  }, [])

  const login = useCallback(
    async (phone: string, code: string, name: string, role: 'CUSTOMER' | 'DRIVER') => {
      const res = await api.verifyOtp(phone, code, name, role)
      const tokens: TokenPair = res.tokens
      tokenStore.save(tokens, res.user)
      setUser(res.user)
      return res.user
    },
    [],
  )

  const logout = useCallback(async () => {
    try {
      await api.logout()
    } catch {
      // Logging out locally must succeed even if the server call does not,
      // otherwise a user can be trapped in a session they cannot end.
    }
    tokenStore.clear()
    setUser(null)
  }, [])

  const refreshUser = useCallback(async () => {
    const me = await api.me()
    tokenStore.saveUser(me)
    setUser(me)
  }, [])

  const value = useMemo(
    () => ({ user, loading, login, logout, refreshUser }),
    [user, loading, login, logout, refreshUser],
  )
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider')
  return ctx
}
