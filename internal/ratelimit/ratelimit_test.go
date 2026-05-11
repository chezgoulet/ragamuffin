package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestAllow_Unlimited(t *testing.T) {
	lim := New(false) // disabled
	allowed, retry := lim.Allow("/recall")
	if !allowed {
		t.Error("disabled limiter should always allow")
	}
	if !retry.IsZero() {
		t.Error("disabled limiter should return zero retry time")
	}
}

func TestAllow_NoLimitConfigured(t *testing.T) {
	lim := New(true)
	allowed, _ := lim.Allow("/nonexistent")
	if !allowed {
		t.Error("endpoint with no configured limit should be allowed")
	}
}

func TestAllow_Basic(t *testing.T) {
	lim := New(true)
	lim.SetLimit("/recall", 60)

	// First request should be allowed
	allowed, retry := lim.Allow("/recall")
	if !allowed {
		t.Error("first request should be allowed")
	}
	if !retry.IsZero() {
		t.Error("retry should be zero when allowed")
	}
}

func TestAllow_Burst(t *testing.T) {
	lim := New(true)
	lim.SetLimit("/recall", 5) // 5 req/min

	// Should allow up to capacity
	allowedCount := 0
	for i := 0; i < 5; i++ {
		if allowed, _ := lim.Allow("/recall"); allowed {
			allowedCount++
		}
	}
	if allowedCount != 5 {
		t.Errorf("expected 5 allowed, got %d", allowedCount)
	}

	// 6th should be denied
	allowed, retry := lim.Allow("/recall")
	if allowed {
		t.Error("6th request should be denied after exhausting burst")
	}
	if retry.IsZero() {
		t.Error("denied request should have retry time")
	}
	if retry.Before(time.Now()) {
		t.Error("retry time should be in the future")
	}
}

func TestAllow_RateLimited(t *testing.T) {
	lim := New(true)
	lim.SetLimit("/ask", 1) // 1 req/min — very restrictive

	// First allowed
	allowed, _ := lim.Allow("/ask")
	if !allowed {
		t.Fatal("first request should be allowed")
	}

	// Second denied (bucket empty)
	allowed, retry := lim.Allow("/ask")
	if allowed {
		t.Error("second request should be denied")
	}
	if retry.IsZero() {
		t.Error("denied request should have retry time")
	}
}

func TestAllow_Refill(t *testing.T) {
	lim := New(true)
	// 60 rpm = 1 per second
	lim.SetLimit("/recall", 60)

	// Empty the bucket
	for i := 0; i < 60; i++ {
		lim.Allow("/recall")
	}

	// Should be denied now
	allowed, _ := lim.Allow("/recall")
	if allowed {
		t.Skip("bucket didn't empty (clock resolution)")
	}

	// Wait for refill
	time.Sleep(1100 * time.Millisecond)

	// Should have at least 1 token now
	allowed, _ = lim.Allow("/recall")
	if !allowed {
		t.Error("should be allowed after refill period")
	}
}

func TestAllow_DifferentKeys(t *testing.T) {
	lim := New(true)
	lim.SetLimit("/recall", 5)
	lim.SetLimit("/ask", 2)

	// Exhaust /recall
	for i := 0; i < 5; i++ {
		if allowed, _ := lim.Allow("/recall"); !allowed {
			t.Fatalf("/recall request %d denied unexpectedly", i+1)
		}
	}

	// /recall should now be denied
	if allowed, _ := lim.Allow("/recall"); allowed {
		t.Error("/recall should be denied after exhausting")
	}

	// /ask should still be allowed (different bucket)
	if allowed, _ := lim.Allow("/ask"); !allowed {
		t.Error("/ask should still be allowed")
	}
}

func TestAllow_Concurrent(t *testing.T) {
	lim := New(true)
	lim.SetLimit("/recall", 1000)

	var wg sync.WaitGroup
	errors := make(chan string, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				allowed, _ := lim.Allow("/recall")
				if !allowed {
					errors <- "request denied under concurrent load"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}
}

func TestSetLimit_UpdatesExisting(t *testing.T) {
	lim := New(true)
	lim.SetLimit("/recall", 1)

	// Use the single token
	lim.Allow("/recall")

	// Should be denied
	if allowed, _ := lim.Allow("/recall"); allowed {
		t.Error("should be denied at limit=1")
	}

	// Increase limit
	lim.SetLimit("/recall", 1000)

	// Should now be allowed (new capacity)
	if allowed, _ := lim.Allow("/recall"); !allowed {
		t.Error("should be allowed after limit increase")
	}
}

func TestRetryAfter_Future(t *testing.T) {
	lim := New(true)
	lim.SetLimit("/recall", 1)

	lim.Allow("/recall") // consume token
	_, retry := lim.Allow("/recall")

	if retry.IsZero() {
		t.Fatal("expected retry time")
	}
	if !retry.After(time.Now()) {
		t.Errorf("retry time %v is not in the future", retry)
	}
	// Should be within the next minute
	if retry.After(time.Now().Add(61 * time.Second)) {
		t.Errorf("retry time %v is too far in the future", retry)
	}
}
