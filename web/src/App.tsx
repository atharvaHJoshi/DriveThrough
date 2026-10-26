import { Navigate, NavLink, Route, Routes, useNavigate } from 'react-router-dom'
import { useAuth } from './state/AuthContext'
import { Loading } from './components/ui'
import { LoginScreen } from './screens/LoginScreen'
import { MyBookingsScreen } from './screens/customer/MyBookingsScreen'
import { NewBookingScreen } from './screens/customer/NewBookingScreen'
import { BookingDetailScreen } from './screens/customer/BookingDetailScreen'
import { VehiclesScreen } from './screens/customer/VehiclesScreen'
import { DriverHomeScreen } from './screens/driver/DriverHomeScreen'
import { DriverBookingScreen } from './screens/driver/DriverBookingScreen'

/**
 * Top-level gate. Everything past login is rendered inside a shell whose routes
 * and tab bar depend on the signed-in role, so a customer never sees driver
 * affordances and vice versa.
 */
function App() {
  const { user, loading } = useAuth()

  if (loading) {
    return (
      <div className="app">
        <div className="app-body">
          <Loading label="Starting" />
        </div>
      </div>
    )
  }

  if (!user) {
    return (
      <Routes>
        <Route path="*" element={<LoginScreen />} />
      </Routes>
    )
  }

  return <Shell role={user.role} />
}

function Shell({ role }: { role: string }) {
  const { user, logout } = useAuth()

  const tabs =
    role === 'DRIVER'
      ? [{ to: '/', label: 'Home', icon: '🧭' }]
      : [
          { to: '/', label: 'Rides', icon: '🧾', end: true },
          { to: '/bookings/new', label: 'Book', icon: '🚗' },
          { to: '/vehicles', label: 'Cars', icon: '🔑' },
        ]

  return (
    <div className="app">
      <header className="app-header">
        <div className="brand">
          <span className="dot" />
          DriveThrough
        </div>
        <div className="row" style={{ gap: 10 }}>
          <span className="faint">{user?.name}</span>
          <button className="btn ghost sm" onClick={() => void logout()}>
            Sign out
          </button>
        </div>
      </header>

      <main className="app-body">
        {role === 'DRIVER' ? <DriverRoutes /> : <CustomerRoutes />}
      </main>

      {tabs.length > 1 && (
        <nav className="tabbar">
          {tabs.map((t) => (
            <NavLink
              key={t.to}
              to={t.to}
              end={t.end}
              className={({ isActive }) => (isActive ? 'active' : '')}
            >
              <span className="ico">{t.icon}</span>
              {t.label}
            </NavLink>
          ))}
        </nav>
      )}
    </div>
  )
}

function CustomerRoutes() {
  const navigate = useNavigate()
  return (
    <Routes>
      <Route path="/" element={<MyBookingsScreen />} />
      <Route
        path="/bookings/new"
        element={<NewBookingScreen onCreated={(b) => navigate(`/bookings/${b.id}`)} />}
      />
      <Route
        path="/bookings/:id"
        element={<BookingDetailRoute />}
      />
      <Route path="/vehicles" element={<VehiclesScreen />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

function BookingDetailRoute() {
  const navigate = useNavigate()
  // Reuse the router param by rendering the detail screen through a wrapper so
  // the screen itself stays router-agnostic.
  const id = window.location.pathname.split('/').pop() ?? ''
  return <BookingDetailScreen bookingId={id} onChanged={() => navigate('/')} />
}

function DriverRoutes() {
  return (
    <Routes>
      <Route path="/" element={<DriverHomeScreen />} />
      <Route path="/driver" element={<DriverHomeScreen />} />
      <Route path="/driver/bookings/:id" element={<DriverBookingRoute />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

function DriverBookingRoute() {
  const id = window.location.pathname.split('/').pop() ?? ''
  return <DriverBookingScreen bookingId={id} />
}

export default App
