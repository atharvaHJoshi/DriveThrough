package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"drivethrough/pkg/response"
)

type visitor struct {
	count    int
	lastSeen time.Time
}

// RateLimiter is a fixed-window limiter with an in-process counter map.
//
// It is deliberately per-replica: each instance counts its own traffic, which
// avoids a Redis round trip on every request. If you need a cluster-wide limit,
// run a shared limiter in front of the API (nginx, API gateway) or swap the
// store behind Limiter.Allow.
type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	limit    int
	window   time.Duration
	stop     chan struct{}
	once     sync.Once
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		visitors: make(map[string]*visitor),
		limit:    limit,
		window:   window,
		stop:     make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

// Close stops the janitor goroutine.
func (rl *RateLimiter) Close() {
	rl.once.Do(func() { close(rl.stop) })
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.window)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stop:
			return
		case <-ticker.C:
			rl.mu.Lock()
			for k, v := range rl.visitors {
				if time.Since(v.lastSeen) > rl.window {
					delete(rl.visitors, k)
				}
			}
			rl.mu.Unlock()
		}
	}
}

// Allow records a hit for key and reports whether it is within the limit.
func (rl *RateLimiter) Allow(key string) bool {
	now := time.Now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, ok := rl.visitors[key]
	if !ok || now.Sub(v.lastSeen) > rl.window {
		rl.visitors[key] = &visitor{count: 1, lastSeen: now}
		return true
	}

	if v.count >= rl.limit {
		v.lastSeen = now
		return false
	}

	v.count++
	v.lastSeen = now
	return true
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.Allow(clientIP(r)) {
			w.Header().Set("Retry-After", strconv.Itoa(int(rl.window.Seconds())))
			response.Error(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many requests, slow down", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
