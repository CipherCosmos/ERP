package middleware

import (
	"sync"
	"testing"
	"time"
)

func TestBurstIsAllowedThenRefused(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(1, 3)
	t.Cleanup(rl.Close)

	now := time.Now()
	for i := 1; i <= 3; i++ {
		if !rl.allow("client", now) {
			t.Fatalf("request %d of a burst of 3 was refused", i)
		}
	}
	if rl.allow("client", now) {
		t.Error("the fourth request in a burst of 3 should be refused")
	}
}

func TestTokensRefillOverTime(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(2, 2) // two per second
	t.Cleanup(rl.Close)

	start := time.Now()
	rl.allow("client", start)
	rl.allow("client", start)
	if rl.allow("client", start) {
		t.Fatal("the bucket should be empty")
	}

	// Half a second buys one token back.
	if !rl.allow("client", start.Add(500*time.Millisecond)) {
		t.Error("a token should have refilled after half a second")
	}
}

func TestClientsAreLimitedIndependently(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(1, 1)
	t.Cleanup(rl.Close)

	now := time.Now()
	if !rl.allow("first", now) {
		t.Fatal("the first client was refused")
	}
	// One client exhausting its bucket must not lock out another.
	if !rl.allow("second", now) {
		t.Error("a second client was refused because the first had spent its own allowance")
	}
}

func TestTheLastTokenIsSpentOnce(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(0, 1) // exactly one token, never refilled
	t.Cleanup(rl.Close)

	// The limiter this replaces refilled and spent under separate locks, so
	// two goroutines could both see the last token and both take it.
	const racers = 50
	var wg sync.WaitGroup
	results := make([]bool, racers)
	now := time.Now()
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = rl.allow("shared", now)
		}(i)
	}
	wg.Wait()

	allowed := 0
	for _, ok := range results {
		if ok {
			allowed++
		}
	}
	if allowed != 1 {
		t.Errorf("%d of %d racers got the single token, want exactly 1", allowed, racers)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(1, 1)
	rl.Close()
	// A double close must not panic on a closed channel; a caller should not
	// have to track whether it already stopped the limiter.
	rl.Close()
}
