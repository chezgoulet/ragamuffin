// Package ratelimit provides a per-endpoint token-bucket rate limiter.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter tracks rate limits for multiple keys using token buckets.
type Limiter struct {
	mu      sync.Mutex
	limits  map[string]int // key → requests per minute
	buckets map[string]*bucket
	enabled bool
}

type bucket struct {
	tokens   float64
	lastTime time.Time
	rate     float64 // tokens per second
	capacity float64
}

// New creates a new Limiter.
func New(enabled bool) *Limiter {
	return &Limiter{
		limits:  make(map[string]int),
		buckets: make(map[string]*bucket),
		enabled: enabled,
	}
}

// SetLimit configures the rate limit (requests per minute) for a key.
// If a bucket already exists for this key, its capacity is updated.
func (l *Limiter) SetLimit(key string, rpm int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	oldCapacity := l.limits[key]
	l.limits[key] = rpm
	if b, ok := l.buckets[key]; ok {
		newCapacity := float64(rpm)
		oldCap := float64(oldCapacity)
		if newCapacity > oldCap {
			b.tokens += newCapacity - oldCap
		}
		b.rate = float64(rpm) / 60.0
		b.capacity = newCapacity
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
	}
}

// Allow returns true if the request is allowed for the given key.
// Returns (allowed bool, retryAfter time.Time).
func (l *Limiter) Allow(key string) (bool, time.Time) {
	if !l.enabled {
		return true, time.Time{}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	rpm, ok := l.limits[key]
	if !ok {
		return true, time.Time{}
	}

	now := time.Now()
	b, exists := l.buckets[key]
	if !exists {
		b = &bucket{
			tokens:   float64(rpm),
			lastTime: now,
			rate:     float64(rpm) / 60.0,
			capacity: float64(rpm),
		}
		l.buckets[key] = b
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(b.lastTime).Seconds()
	b.tokens += elapsed * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.lastTime = now

	if b.tokens >= 1 {
		b.tokens--
		return true, time.Time{}
	}

	// Calculate when the next token will be available
	retryAfter := now.Add(time.Duration((1-b.tokens)/b.rate) * time.Second)
	return false, retryAfter
}
