/**
 * Live trip tracking over WebSocket.
 *
 * The browser cannot set headers on a WebSocket handshake, so the token travels
 * in the subprotocol header as "bearer.<token>" (see internal/middleware). A real
 * subprotocol name is offered alongside it, and the server selects that rather
 * than echoing the credential back.
 */

import { tokenStore } from './client'
import type { Location } from './types'

const PROTOCOL_NAME = 'drivethrough.v1'
const TOKEN_PREFIX = 'bearer.'

/** Events the hub broadcasts, mirrored from internal/websocket/hub.go. */
export interface TripLocationEvent {
  type: 'trip.location'
  trip_id: string
  location: {
    trip_id: string
    latitude: number
    longitude: number
    accuracy: number
    heading: number
    speed: number
    timestamp: string
  }
}

export interface TripEndedEvent {
  type: 'trip.ended'
  trip_id: string
  booking_id: string
}

export type TripEvent = TripLocationEvent | TripEndedEvent

export type ConnectionState = 'connecting' | 'open' | 'closed' | 'error'

export interface TripSocketOptions {
  tripId: string
  bookingId?: string
  onEvent: (event: TripEvent) => void
  onState?: (state: ConnectionState) => void
}

/**
 * A reconnecting subscription to one trip.
 *
 * Reconnection is exponential with a ceiling: the hub pings every WS_PING_INTERVAL
 * and drops a client that misses a pong, and a phone that loses signal will drop
 * too. Backing off avoids hammering the server while a device is offline, and the
 * ceiling keeps recovery reasonably quick once the network returns.
 */
export class TripSocket {
  private ws: WebSocket | null = null
  private attempt = 0
  private timer: number | null = null
  private disposed = false

  private readonly opts: TripSocketOptions

  constructor(opts: TripSocketOptions) {
    this.opts = opts
  }

  connect() {
    this.disposed = false
    this.open()
  }

  private open() {
    const token = tokenStore.access()
    if (!token) {
      this.opts.onState?.('error')
      return
    }

    const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const url = new URL(`${scheme}://${window.location.host}/api/v1/ws/live`)
    url.searchParams.set('trip_id', this.opts.tripId)
    if (this.opts.bookingId) url.searchParams.set('booking_id', this.opts.bookingId)

    this.opts.onState?.('connecting')

    try {
      this.ws = new WebSocket(url, [PROTOCOL_NAME, `${TOKEN_PREFIX}${token}`])
    } catch {
      this.opts.onState?.('error')
      this.scheduleReconnect()
      return
    }

    this.ws.onopen = () => {
      this.attempt = 0
      this.opts.onState?.('open')
    }

    this.ws.onmessage = (ev) => {
      try {
        this.opts.onEvent(JSON.parse(ev.data as string) as TripEvent)
      } catch {
        // A frame we cannot parse is not worth tearing the socket down for.
      }
    }

    this.ws.onerror = () => this.opts.onState?.('error')

    this.ws.onclose = () => {
      this.opts.onState?.('closed')
      this.scheduleReconnect()
    }
  }

  private scheduleReconnect() {
    if (this.disposed) return
    if (this.timer !== null) window.clearTimeout(this.timer)
    const delay = Math.min(1000 * 2 ** this.attempt, 15000)
    this.attempt += 1
    this.timer = window.setTimeout(() => this.open(), delay)
  }

  close() {
    this.disposed = true
    if (this.timer !== null) {
      window.clearTimeout(this.timer)
      this.timer = null
    }
    if (this.ws) {
      this.ws.onclose = null
      this.ws.onerror = null
      this.ws.close()
      this.ws = null
    }
  }
}

/** Latest known driver position, plus the samples seen during the ride. */
export interface TrackState {
  latest: Location | null
  path: Location[]
  updatedAt: string | null
}

export const initialTrackState: TrackState = { latest: null, path: [], updatedAt: null }
