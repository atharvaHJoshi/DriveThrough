package middleware

import (
	"net"
	"net/http"
	"strings"
)

// CORSMiddleware allows a configurable origin list. Passing nil or ["*"]
// keeps the permissive behaviour needed for local development, while
// production supplies SERVER_ALLOWED_ORIGINS and gets a strict allowlist.
func CORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowAll := len(allowedOrigins) == 0
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o == "*" {
			allowAll = true
			continue
		}
		allowed[strings.TrimRight(strings.ToLower(o), "/")] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimRight(r.Header.Get("Origin"), "/")

			switch {
			case allowAll:
				w.Header().Set("Access-Control-Allow-Origin", "*")
			case allowed[strings.ToLower(origin)]:
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}

			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "600")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP extracts the caller address used as the rate limiter key.
//
// RemoteAddr is deliberately the only source. TrustedProxyRealIP has already
// rewritten it from X-Forwarded-For, but only when the peer was configured as a
// trusted proxy. Falling back to the raw header here would let any caller forge
// its own limiter key, so the header is ignored outright.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}
