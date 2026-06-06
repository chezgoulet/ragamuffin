package extraction

import (
	"sync"
	"time"
)

// CooldownTracker enforces a minimum interval between extractions per session.
type CooldownTracker struct {
	interval time.Duration
	mu       sync.Mutex
	last     map[string]time.Time
}

// NewCooldownTracker creates a tracker. intervalSec is the cooldown in seconds.
func NewCooldownTracker(intervalSec int) *CooldownTracker {
	return &CooldownTracker{
		interval: time.Duration(intervalSec) * time.Second,
		last:     make(map[string]time.Time),
	}
}

// TryAcquire returns true if the session is allowed to extract (not in cooldown).
// Lazy eviction: removes entries older than 2× the cooldown interval.
func (ct *CooldownTracker) TryAcquire(sessionID string) bool {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	if ct.interval <= 0 {
		ct.last[sessionID] = time.Now()
		return true
	}

	now := time.Now()
	evictBefore := now.Add(-2 * ct.interval)

	// Lazy eviction: sweep stale entries
	for sid, last := range ct.last {
		if last.Before(evictBefore) {
			delete(ct.last, sid)
		}
	}

	last, ok := ct.last[sessionID]
	if ok && now.Sub(last) < ct.interval {
		return false
	}

	ct.last[sessionID] = now
	return true
}

// ResetCooldown clears the cooldown for a session.
func (ct *CooldownTracker) ResetCooldown(sessionID string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	delete(ct.last, sessionID)
}
