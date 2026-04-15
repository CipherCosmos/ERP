package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type visitor struct {
	tokens    float64
	maxTokens float64
	refillRate float64 // tokens per second
	lastSeen  time.Time
}

type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
}

func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{
		visitors: make(map[string]*visitor),
	}
	// Cleanup stale entries every minute
	go func() {
		for {
			time.Sleep(time.Minute)
			rl.mu.Lock()
			for ip, v := range rl.visitors {
				if time.Since(v.lastSeen) > 3*time.Minute {
					delete(rl.visitors, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *RateLimiter) getVisitor(ip string, maxTokens, refillRate float64) *visitor {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, exists := rl.visitors[ip]
	if !exists {
		v = &visitor{
			tokens:    maxTokens,
			maxTokens: maxTokens,
			refillRate: refillRate,
			lastSeen:  time.Now(),
		}
		rl.visitors[ip] = v
		return v
	}

	// Refill tokens based on elapsed time
	elapsed := time.Since(v.lastSeen).Seconds()
	v.tokens += elapsed * v.refillRate
	if v.tokens > v.maxTokens {
		v.tokens = v.maxTokens
	}
	v.lastSeen = time.Now()
	return v
}

// GlobalRateLimit limits requests per IP: maxTokens bucket with refillRate tokens/second.
func (rl *RateLimiter) GlobalRateLimit(maxTokens, refillRate float64) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		v := rl.getVisitor(ip, maxTokens, refillRate)

		rl.mu.Lock()
		if v.tokens < 1 {
			rl.mu.Unlock()
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			c.Abort()
			return
		}
		v.tokens--
		rl.mu.Unlock()

		c.Next()
	}
}

// LoginRateLimit is a stricter limiter for login endpoints.
func (rl *RateLimiter) LoginRateLimit() gin.HandlerFunc {
	loginLimiter := NewRateLimiter()
	return func(c *gin.Context) {
		ip := c.ClientIP()
		// 10 attempts per minute = 10 tokens, refill at 10/60 = 0.167 per second
		v := loginLimiter.getVisitor(ip, 10, 0.167)

		loginLimiter.mu.Lock()
		if v.tokens < 1 {
			loginLimiter.mu.Unlock()
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many login attempts, try again later"})
			c.Abort()
			return
		}
		v.tokens--
		loginLimiter.mu.Unlock()

		c.Next()
	}
}
