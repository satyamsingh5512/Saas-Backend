package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// visitor tracks the rate limiter and last-seen time for a single client IP.
type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// IPRateLimiter is a simple in-memory, per-IP token bucket rate limiter.
// Suitable for single-instance deployments. For multi-instance deployments,
// replace with a shared store (e.g. Redis-backed limiter).
type IPRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rate     rate.Limit
	burst    int
}

// NewIPRateLimiter creates a limiter allowing `requestsPerMinute` requests
// per minute per IP, with a burst allowance of `burst`.
func NewIPRateLimiter(requestsPerMinute int, burst int) *IPRateLimiter {
	l := &IPRateLimiter{
		visitors: make(map[string]*visitor),
		rate:     rate.Limit(float64(requestsPerMinute) / 60.0),
		burst:    burst,
	}
	go l.cleanupLoop()
	return l
}

func (l *IPRateLimiter) getVisitor(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	v, exists := l.visitors[ip]
	if !exists {
		limiter := rate.NewLimiter(l.rate, l.burst)
		l.visitors[ip] = &visitor{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}

	v.lastSeen = time.Now()
	return v.limiter
}

// cleanupLoop periodically evicts visitors that haven't been seen recently
// to prevent unbounded memory growth.
func (l *IPRateLimiter) cleanupLoop() {
	for {
		time.Sleep(time.Minute)
		l.mu.Lock()
		for ip, v := range l.visitors {
			if time.Since(v.lastSeen) > 3*time.Minute {
				delete(l.visitors, ip)
			}
		}
		l.mu.Unlock()
	}
}

// Middleware returns a gin.HandlerFunc that rejects requests exceeding the
// configured per-IP rate with HTTP 429.
func (l *IPRateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if !l.getVisitor(ip).Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests, please try again later"})
			return
		}
		c.Next()
	}
}
