package middleware

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"drivethrough/internal/auth"
	"drivethrough/internal/models"
	"drivethrough/pkg/response"
)

// MaxRequestBodyBytes caps how much a client may send. Every endpoint in this
// service takes small JSON documents; without a cap a single request can hold a
// connection and stream unbounded data into memory during decoding.
const MaxRequestBodyBytes = 1 << 20 // 1 MiB

// requestIDKey is the context key carrying the request id. Exposing it to
// handlers lets a log line or a support reply be tied to the exact request
// without relying on the response header alone.
type requestIDKey struct{}

// RequestIDFrom returns the id assigned to the current request.
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rid := r.Header.Get("X-Request-ID")
		if rid == "" {
			rid = newRequestID()
		}
		w.Header().Set("X-Request-ID", rid)

		// Bound the body before any handler can read it.
		r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)

		ctx := context.WithValue(r.Context(), requestIDKey{}, rid)
		r = r.WithContext(ctx)

		srw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(srw, r)

		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", srw.status,
			"bytes", srw.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", rid,
		)
	})
}

// SecurityHeaders sets conservative defaults for a JSON API.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// The API returns JSON and is never rendered as a document, so the
		// sniffing and framing vectors are closed off outright.
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Auth is carried in a bearer token, never a cookie, so nothing may be
		// cached by an intermediary on our behalf.
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// TrustedProxyRealIP resolves the client address for the rate limiter.
//
// The rate limiter keys on the remote address, so honouring X-Forwarded-For from
// any peer would let a caller rotate a forged header and bypass every limit.
// When no proxy is configured the raw peer address is used, which is correct for
// a directly exposed service.
func TrustedProxyRealIP(trustedProxies []string) func(http.Handler) http.Handler {
	trusted := make(map[string]bool, len(trustedProxies))
	for _, p := range trustedProxies {
		p = strings.TrimSpace(p)
		if p != "" {
			trusted[p] = true
		}
	}
	// A blank list means nothing is trusted, not everything.
	trustForwarded := len(trusted) > 0

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if trustForwarded {
				if peer, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && trusted[peer] {
					if real := firstForwardedFor(r.Header.Get("X-Forwarded-For")); real != "" {
						r.RemoteAddr = net.JoinHostPort(real, "0")
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// firstForwardedFor returns the left-most address, which is the original client
// as recorded by the first proxy in the chain.
func firstForwardedFor(header string) string {
	if header == "" {
		return ""
	}
	first, _, _ := strings.Cut(header, ",")
	first = strings.TrimSpace(first)
	if net.ParseIP(first) == nil {
		return ""
	}
	return first
}

func newRequestID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "req-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "req-" + hex.EncodeToString(buf[:])
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.wrote {
		return
	}
	r.wrote = true
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.wrote = true
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Headers exposes the wrapped writer's header map. Implementations that inspect
// for Flusher, Hijacker, or Pusher unwrap the chain and would otherwise conclude
// the writer does not support them.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Hijack lets the WebSocket upgrade pass through the logging wrapper. Without
// it, every /ws/live connection would fail to upgrade.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hj.Hijack()
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Pusher keeps HTTP/2 server push working through the wrapper.
func (r *statusRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := r.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func RecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srw := &statusRecorder{ResponseWriter: w, status: 200}

		defer func() {
			err := recover()
			if err == nil {
				return
			}
			// A panic is never the client's fault, but the connection has to be
			// closed rather than left half-written.
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// A handler that panicked on a client timeout is expected.
				slog.Warn("panic during request timeout", "request_id", RequestIDFrom(r.Context()))
				return
			}

			slog.Error("panic recovered",
				"error", err,
				"request_id", RequestIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"stack", string(debug.Stack()),
			)

			if srw.wrote {
				// The response is already committed, so no error body can be
				// added. Logged above; nothing further is safe to do.
				return
			}
			response.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR", "something went wrong", nil)
		}()

		next.ServeHTTP(srw, r)
	})
}

// WebSocketProtocolTokenHeader is the RFC 6455 request header a browser uses to
// carry a subprotocol name during the upgrade handshake.
const WebSocketProtocolTokenHeader = "Sec-WebSocket-Protocol"

// WebSocketTokenPrefix marks the subprotocol entry that carries the access token.
const WebSocketTokenPrefix = "bearer."

// extractBearerToken returns the bearer token for a request, or an error
// describing why no usable credential was present.
//
// Two transports are accepted. The Authorization header is the ordinary path and
// works for every server-to-server and native client. The WebSocket subprotocol
// header exists because the browser WebSocket API cannot set request headers at
// all: without it no browser can ever authenticate a live-tracking socket, since
// /api/v1/ws/live has no other credential channel. The token is sent as
// "bearer.<token>" so a client can still request a real subprotocol name
// alongside it, which a bare token would prevent.
//
// The subprotocol fallback is deliberately narrow. It applies only when the
// Authorization header is entirely absent, and a malformed Authorization header
// still fails rather than falling back, so a broken header cannot be used to
// quietly switch credential channels.
func extractBearerToken(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if header != "" {
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return "", errors.New("invalid authorization header")
		}
		if parts[1] == "" {
			return "", errors.New("empty bearer token")
		}
		return parts[1], nil
	}

	for _, entry := range r.Header.Values(WebSocketProtocolTokenHeader) {
		// A client may offer several subprotocols comma-separated.
		for _, candidate := range strings.Split(entry, ",") {
			candidate = strings.TrimSpace(candidate)
			if token, ok := strings.CutPrefix(candidate, WebSocketTokenPrefix); ok && token != "" {
				return token, nil
			}
		}
	}

	return "", errors.New("missing authorization header")
}

func AuthMiddleware(issuer auth.TokenIssuer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := extractBearerToken(r)
			if err != nil {
				response.Unauthorized(w, err.Error())
				return
			}

			if bl := issuer.Blacklist(); bl != nil {
				blacklisted, err := bl.IsBlacklisted(r.Context(), token)
				if err != nil {
					slog.Warn("blacklist check failed", "error", err)
				}
				if blacklisted {
					response.Unauthorized(w, "token has been revoked")
					return
				}
			}

			claims, err := issuer.Parse(token)
			if err != nil {
				response.Unauthorized(w, "invalid or expired token")
				return
			}

			ctx := auth.WithUser(r.Context(), claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func RequireRole(roles ...models.UserRole) func(http.Handler) http.Handler {
	allowed := make(map[models.UserRole]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := auth.UserFrom(r.Context())
			if !ok {
				response.Unauthorized(w, "authentication required")
				return
			}
			if !allowed[claims.Role] {
				response.Forbidden(w, "insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
