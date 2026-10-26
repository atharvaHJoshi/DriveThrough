package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"drivethrough/internal/auth"
	"drivethrough/internal/config"
	"drivethrough/internal/middleware"
	"drivethrough/internal/models"
	"drivethrough/pkg/response"
)

// maxInboundMessageBytes caps what a client may send us. This endpoint is
// read-only for clients, so anything large is a mistake or an attack.
const maxInboundMessageBytes = 4096

// TripAuthorizer decides whether a user may observe a trip.
//
// The hub deliberately knows nothing about bookings or drivers: it accepts a
// callback so the module boundary stays one-way and the check lives with the
// data it needs. Without it, any authenticated user could pass an arbitrary
// trip_id and receive another customer's live location.
type TripAuthorizer interface {
	AuthorizeTripAccess(ctx context.Context, tripID, userID uuid.UUID) error
}

// Hub fans trip events out to the customers and drivers watching a trip.
type Hub struct {
	mu         sync.RWMutex
	byTrip     map[uuid.UUID]map[*client]bool
	register   chan *client
	unregister chan *client
	upgrader   websocket.Upgrader
	cfg        config.WSConfig

	authorizer TripAuthorizer
}

// SetAuthorizer installs the subscription access check. Until it is set the hub
// refuses new subscriptions rather than serving them unchecked, since a
// fail-open default here is a live-location leak.
func (h *Hub) SetAuthorizer(a TripAuthorizer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.authorizer = a
}

func (h *Hub) canAccess(ctx context.Context, tripID, userID uuid.UUID) error {
	h.mu.RLock()
	a := h.authorizer
	h.mu.RUnlock()

	if a == nil {
		return fmt.Errorf("websocket: no trip authorizer configured")
	}
	return a.AuthorizeTripAccess(ctx, tripID, userID)
}

type client struct {
	hub       *Hub
	conn      *websocket.Conn
	send      chan []byte
	tripID    uuid.UUID
	bookingID uuid.UUID
	userID    uuid.UUID
}

func NewHub(cfg config.WSConfig) *Hub {
	allowed := make(map[string]bool, len(cfg.AllowedOrigins))
	allowAll := len(cfg.AllowedOrigins) == 0
	for _, o := range cfg.AllowedOrigins {
		if o == "*" {
			allowAll = true
			continue
		}
		allowed[strings.TrimRight(strings.ToLower(o), "/")] = true
	}

	return &Hub{
		byTrip:     make(map[uuid.UUID]map[*client]bool),
		register:   make(chan *client),
		unregister: make(chan *client),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  cfg.ReadBuffer,
			WriteBufferSize: cfg.WriteBuffer,
			CheckOrigin: func(r *http.Request) bool {
				if allowAll {
					return true
				}
				origin := strings.TrimRight(r.Header.Get("Origin"), "/")
				return allowed[strings.ToLower(origin)]
			},
		},
		cfg: cfg,
	}
}

type ClientEvent interface {
	EventName() string
}

type LocationEvent struct {
	Type     string                `json:"type"`
	TripID   uuid.UUID             `json:"trip_id"`
	Location models.LocationUpdate `json:"location"`
}

func (e LocationEvent) EventName() string { return e.Type }

type TripEndedEvent struct {
	Type      string    `json:"type"`
	TripID    uuid.UUID `json:"trip_id"`
	BookingID uuid.UUID `json:"booking_id"`
}

func (e TripEndedEvent) EventName() string { return e.Type }

// offeredSubprotocols flattens the comma-separated Sec-WebSocket-Protocol request
// headers a client may send.
func offeredSubprotocols(r *http.Request) []string {
	var offered []string
	for _, entry := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(entry, ",") {
			if p = strings.TrimSpace(p); p != "" {
				offered = append(offered, p)
			}
		}
	}
	return offered
}

// negotiateSubprotocol chooses the value the server echoes in the 101 response.
//
// Per RFC 6455 a server that is offered subprotocols must select one of them,
// and a browser aborts the connection if it sends none back. A browser client
// cannot set an Authorization header, so it smuggles its token through the
// subprotocol header as "bearer.<token>" (see middleware.AuthMiddleware); that
// entry is a credential, not a protocol name we want to advertise.
//
// A real protocol name such as "drivethrough.v1" is preferred so the response
// carries a stable identifier. When the client offered nothing but the credential
// entry, that entry itself has to be echoed, because refusing to select one would
// fail the handshake for a client that did nothing wrong.
func negotiateSubprotocol(offered []string) string {
	for _, p := range offered {
		if !strings.HasPrefix(p, middleware.WebSocketTokenPrefix) {
			return p
		}
	}
	if len(offered) > 0 {
		return offered[0]
	}
	return ""
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	tripID, err := uuid.Parse(r.URL.Query().Get("trip_id"))
	if err != nil {
		response.BadRequest(w, "trip_id query param required", nil)
		return
	}

	claims, ok := auth.UserFrom(r.Context())
	if !ok {
		response.Unauthorized(w, "authentication required")
		return
	}

	// Authorise before upgrading. Once the 101 handshake completes an
	// unauthorised client is already receiving frames, so the check has to
	// happen while an error response can still be sent.
	if err := h.canAccess(r.Context(), tripID, claims.UserID); err != nil {
		slog.Warn("websocket subscription denied", "trip_id", tripID, "user_id", claims.UserID, "error", err)
		response.Forbidden(w, "not authorised to view this trip")
		return
	}

	// The upgrader is copied per connection because Subprotocols is what makes the
	// server select one; the shared copy stays empty, which would fail the
	// handshake against any browser.
	upgrader := h.upgrader
	if proto := negotiateSubprotocol(offeredSubprotocols(r)); proto != "" {
		upgrader.Subprotocols = []string{proto}
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("websocket upgrade failed", "error", err)
		return
	}

	bookingID, _ := uuid.Parse(r.URL.Query().Get("booking_id"))

	c := &client{
		hub:       h,
		conn:      conn,
		send:      make(chan []byte, h.cfg.SendBuffer),
		tripID:    tripID,
		bookingID: bookingID,
		userID:    claims.UserID,
	}

	h.register <- c

	go c.writePump()
	go c.readPump()
}

func (h *Hub) BroadcastTripLocation(tripID, bookingID uuid.UUID, loc models.LocationUpdate) {
	event := LocationEvent{Type: "trip.location", TripID: tripID, Location: loc}
	h.broadcast(tripID, event)
}

func (h *Hub) PublishTripEnded(tripID, bookingID uuid.UUID) {
	event := TripEndedEvent{Type: "trip.ended", TripID: tripID, BookingID: bookingID}
	h.broadcast(tripID, event)
}

func (h *Hub) broadcast(tripID uuid.UUID, event ClientEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	h.mu.RLock()
	clients := h.byTrip[tripID]
	for c := range clients {
		select {
		case c.send <- data:
		default:
		}
	}
	h.mu.RUnlock()
}

// EventLoop owns all mutation of the client registry. It must run in a single
// goroutine; Start is the only supported way to launch it.
func (h *Hub) EventLoop() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			if h.byTrip[c.tripID] == nil {
				h.byTrip[c.tripID] = make(map[*client]bool)
			}
			h.byTrip[c.tripID][c] = true
			h.mu.Unlock()
			slog.Debug("client connected", "trip_id", c.tripID, "user_id", c.userID)

		case c := <-h.unregister:
			h.mu.Lock()
			if clients, ok := h.byTrip[c.tripID]; ok {
				if _, exists := clients[c]; exists {
					delete(clients, c)
					close(c.send)
				}
				if len(clients) == 0 {
					delete(h.byTrip, c.tripID)
				}
			}
			h.mu.Unlock()
			slog.Debug("client disconnected", "trip_id", c.tripID)
		}
	}
}

func (c *client) readPump() {
	defer func() {
		c.hub.unregister <- c
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(maxInboundMessageBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(c.hub.cfg.PongTimeout))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(c.hub.cfg.PongTimeout))
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (c *client) writePump() {
	ticker := time.NewTicker(c.hub.cfg.PingInterval)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.hub.cfg.WriteTimeout))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.hub.cfg.WriteTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
