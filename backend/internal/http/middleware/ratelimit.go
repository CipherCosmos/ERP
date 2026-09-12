package middleware

import (
	"net/http"
	"sync"
	"time"

	"gbos/internal/platform/httperr"

	"github.com/gin-gonic/gin"
)

// RateLimiter is a per-client token bucket.
//
// It is in-process, which is correct for the single-instance deployment this
// system targets. Behind two replicas the effective limit becomes N times what
// is configured, so a Redis-backed counter would be needed — the cache
// interface in internal/platform/cache exists for that day.
type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*bucket

	rate  float64 // tokens added per second
	burst float64 // maximum tokens held

	stop chan struct{}
	once sync.Once
}

type bucket struct {
	tokens float64
	seen   time.Time
}

// NewRateLimiter returns a limiter allowing burst requests immediately and
// rate requests per second thereafter.
func NewRateLimiter(rate, burst float64) *RateLimiter {
	rl := &RateLimiter{
		visitors: make(map[string]*bucket),
		rate:     rate,
		burst:    burst,
		stop:     make(chan struct{}),
	}
	go rl.evictIdle()
	return rl
}

// Close stops the eviction goroutine.
//
// The limiter this replaces had a `for { time.Sleep(time.Minute) }` loop with
// no way to stop it, so every limiter ever constructed leaked a goroutine for
// the life of the process.
func (rl *RateLimiter) Close() {
	rl.once.Do(func() { close(rl.stop) })
}

func (rl *RateLimiter) evictIdle() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stop:
			return
		case now := <-ticker.C:
			rl.mu.Lock()
			for key, b := range rl.visitors {
				if now.Sub(b.seen) > 3*time.Minute {
					delete(rl.visitors, key)
				}
			}
			rl.mu.Unlock()
		}
	}
}

// allow refills the caller's bucket and takes a token if one is available.
//
// Refill and spend happen under one lock. The limiter this replaces released
// the lock between the two, so two goroutines could both see the last token
// and both spend it.
func (rl *RateLimiter) allow(key string, now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.visitors[key]
	if !ok {
		rl.visitors[key] = &bucket{tokens: rl.burst - 1, seen: now}
		return true
	}

	b.tokens += now.Sub(b.seen).Seconds() * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.seen = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Limit rejects a caller who is over their allowance.
func (rl *RateLimiter) Limit() gin.HandlerFunc {
	return func(c *gin.Context) {
		// ClientIP is only trustworthy because the engine is configured with
		// SetTrustedProxies. Without that, Gin believes any X-Forwarded-For a
		// caller sends, and rotating the header walks straight past this.
		if !rl.allow(c.ClientIP(), time.Now()) {
			c.Header("Retry-After", "1")
			httperr.Respond(c, httperr.New(http.StatusTooManyRequests, "rate_limited",
				"too many requests — slow down and try again"))
			c.Abort()
			return
		}
		c.Next()
	}
}
