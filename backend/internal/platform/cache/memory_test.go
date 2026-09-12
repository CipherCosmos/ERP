package cache

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock, so TTL behaviour can be asserted
// exactly rather than by sleeping and hoping.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newTestCache builds a cache with no janitor and a controllable clock, so
// tests exercise expiry semantics without racing a background goroutine.
func newTestCache(t *testing.T) (*Memory, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	c := NewMemory(MemoryOptions{CleanupInterval: -1, Now: clock.Now})
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return c, clock
}

func TestGetMiss(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	if _, err := c.Get(ctx, "absent"); !errors.Is(err, ErrMiss) {
		t.Fatalf("Get on absent key = %v, want ErrMiss", err)
	}
}

func TestSetGet(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("Get = %q, want %q", got, "v")
	}
}

func TestTTLExpiry(t *testing.T) {
	c, clock := newTestCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "k", []byte("v"), 30*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Just before expiry the entry is still live.
	clock.Advance(29 * time.Second)
	if _, err := c.Get(ctx, "k"); err != nil {
		t.Fatalf("Get before expiry: %v, want hit", err)
	}

	// Past expiry it is a miss, even though the janitor is disabled: Get must
	// check the deadline itself, not rely on the sweep.
	clock.Advance(2 * time.Second)
	if _, err := c.Get(ctx, "k"); !errors.Is(err, ErrMiss) {
		t.Fatalf("Get after expiry = %v, want ErrMiss", err)
	}
}

func TestGetDropsExpiredEntry(t *testing.T) {
	c, clock := newTestCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "k", []byte("v"), time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}
	clock.Advance(2 * time.Second)

	if _, err := c.Get(ctx, "k"); !errors.Is(err, ErrMiss) {
		t.Fatalf("Get = %v, want ErrMiss", err)
	}
	// The lazy path should reclaim the bytes, not merely report a miss.
	if n := c.Len(); n != 0 {
		t.Fatalf("Len after expired Get = %d, want 0", n)
	}
}

func TestZeroTTLNeverExpires(t *testing.T) {
	c, clock := newTestCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	clock.Advance(1000 * time.Hour)

	if _, err := c.Get(ctx, "k"); err != nil {
		t.Fatalf("Get with zero TTL after a long wait: %v, want hit", err)
	}
}

func TestSetOverwritesTTL(t *testing.T) {
	c, clock := newTestCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "k", []byte("old"), 10*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}
	clock.Advance(9 * time.Second)
	if err := c.Set(ctx, "k", []byte("new"), 10*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The second Set restarts the clock; the original deadline must not apply.
	clock.Advance(5 * time.Second)
	got, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("Get = %q, want %q", got, "new")
	}
}

func TestBackgroundEviction(t *testing.T) {
	clock := newFakeClock()
	c := NewMemory(MemoryOptions{CleanupInterval: time.Millisecond, Now: clock.Now})
	defer c.Close()

	ctx := context.Background()
	for i := range 10 {
		if err := c.Set(ctx, fmt.Sprintf("k%d", i), []byte("v"), time.Second); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if n := c.Len(); n != 10 {
		t.Fatalf("Len = %d, want 10", n)
	}

	// Move past the TTL and let the janitor run. Nothing reads these keys, so
	// only the background sweep can remove them.
	clock.Advance(2 * time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for c.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := c.Len(); n != 0 {
		t.Fatalf("Len after eviction sweep = %d, want 0", n)
	}
}

func TestDelete(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	for _, k := range []string{"a", "b", "c"} {
		if err := c.Set(ctx, k, []byte("v"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if err := c.Delete(ctx, "a", "c"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := c.Get(ctx, "a"); !errors.Is(err, ErrMiss) {
		t.Errorf("Get(a) = %v, want ErrMiss", err)
	}
	if _, err := c.Get(ctx, "b"); err != nil {
		t.Errorf("Get(b) = %v, want hit", err)
	}

	// Deleting an absent key is not an error.
	if err := c.Delete(ctx, "never-existed"); err != nil {
		t.Errorf("Delete on absent key: %v", err)
	}
}

func TestDeletePrefix(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	keys := []string{
		"products:store:1:page:1",
		"products:sku:ABC",
		"vendors:store:1",
	}
	for _, k := range keys {
		if err := c.Set(ctx, k, []byte("v"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	if err := c.DeletePrefix(ctx, "products:"); err != nil {
		t.Fatalf("DeletePrefix: %v", err)
	}
	for _, k := range []string{"products:store:1:page:1", "products:sku:ABC"} {
		if _, err := c.Get(ctx, k); !errors.Is(err, ErrMiss) {
			t.Errorf("Get(%q) = %v, want ErrMiss", k, err)
		}
	}
	if _, err := c.Get(ctx, "vendors:store:1"); err != nil {
		t.Errorf("unrelated namespace was evicted: %v", err)
	}
}

func TestDeletePrefixEmptyClearsAll(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	for _, k := range []string{"a", "b"} {
		if err := c.Set(ctx, k, []byte("v"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if err := c.DeletePrefix(ctx, ""); err != nil {
		t.Fatalf("DeletePrefix: %v", err)
	}
	if n := c.Len(); n != 0 {
		t.Fatalf("Len = %d, want 0", n)
	}
}

// TestValueIsCopied guards the property that makes this interface swappable for
// Redis: a caller must never be handed the cache's own storage.
func TestValueIsCopied(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	original := []byte("hello")
	if err := c.Set(ctx, "k", original, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Mutating the slice that was passed in must not change what is stored.
	original[0] = 'J'
	got, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("caller mutated stored value: got %q, want %q", got, "hello")
	}

	// Mutating the slice that was returned must not change what is stored.
	got[0] = 'J'
	again, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(again) != "hello" {
		t.Fatalf("returned slice aliases storage: got %q, want %q", again, "hello")
	}
}

func TestCanceledContext(t *testing.T) {
	c, _ := newTestCache(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.Get(ctx, "k"); !errors.Is(err, context.Canceled) {
		t.Errorf("Get with cancelled ctx = %v, want context.Canceled", err)
	}
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("Set with cancelled ctx = %v, want context.Canceled", err)
	}
	if err := c.Delete(ctx, "k"); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete with cancelled ctx = %v, want context.Canceled", err)
	}
	if err := c.DeletePrefix(ctx, "k"); !errors.Is(err, context.Canceled) {
		t.Errorf("DeletePrefix with cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	c := NewMemory(MemoryOptions{CleanupInterval: time.Millisecond})

	for i := range 3 {
		if err := c.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}

	ctx := context.Background()
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); !errors.Is(err, ErrClosed) {
		t.Errorf("Set after Close = %v, want ErrClosed", err)
	}
	if _, err := c.Get(ctx, "k"); !errors.Is(err, ErrClosed) {
		t.Errorf("Get after Close = %v, want ErrClosed", err)
	}
}

// TestCloseStopsJanitor is the regression test for the leak this package exists
// to avoid: the rate limiter's cleanup goroutine has no stop channel, so every
// instance runs forever. Close must actually reap the goroutine.
func TestCloseStopsJanitor(t *testing.T) {
	before := runtime.NumGoroutine()

	caches := make([]*Memory, 20)
	for i := range caches {
		caches[i] = NewMemory(MemoryOptions{CleanupInterval: time.Millisecond})
	}
	for _, c := range caches {
		if err := c.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	// Close waits on the WaitGroup, so the janitors have already returned; give
	// the scheduler a moment to retire them before counting.
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutines leaked: %d before, %d after 20 open/close cycles", before, after)
	}
}

// TestConcurrentAccess is the -race workhorse: every method hammered from many
// goroutines at once, including the janitor sweeping underneath.
func TestConcurrentAccess(t *testing.T) {
	clock := newFakeClock()
	c := NewMemory(MemoryOptions{CleanupInterval: time.Millisecond, Now: clock.Now})
	defer c.Close()

	ctx := context.Background()
	const workers = 16
	const iterations = 200

	var hits atomic.Int64
	var wg sync.WaitGroup

	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range iterations {
				key := fmt.Sprintf("products:%d:%d", w, i%10)
				switch i % 5 {
				case 0, 1:
					if err := c.Set(ctx, key, []byte("value"), time.Minute); err != nil {
						t.Errorf("Set: %v", err)
						return
					}
				case 2:
					// Read a key in this worker's private namespace, written
					// just below. The shared "products:" keys are being
					// deleted by other iterations, so a hit on them is not
					// guaranteed; this probe makes the assertion at the end
					// meaningful without weakening the race coverage above.
					probe := fmt.Sprintf("worker:%d:probe", w)
					if err := c.Set(ctx, probe, []byte("value"), time.Minute); err != nil {
						t.Errorf("Set: %v", err)
						return
					}
					if _, err := c.Get(ctx, probe); err == nil {
						hits.Add(1)
					} else {
						t.Errorf("Get on private probe key: %v, want hit", err)
						return
					}
					if _, err := c.Get(ctx, key); err != nil && !errors.Is(err, ErrMiss) {
						t.Errorf("Get: %v", err)
						return
					}
				case 3:
					if err := c.Delete(ctx, key); err != nil {
						t.Errorf("Delete: %v", err)
						return
					}
				case 4:
					if err := c.DeletePrefix(ctx, fmt.Sprintf("products:%d:", w)); err != nil {
						t.Errorf("DeletePrefix: %v", err)
						return
					}
				}
				if i%50 == 0 {
					clock.Advance(time.Second)
				}
			}
		}(w)
	}
	wg.Wait()

	if hits.Load() == 0 {
		t.Error("no cache hits recorded; the test exercised nothing")
	}
}

// TestConcurrentCloseDuringUse checks that Close racing with live traffic
// neither panics nor trips the race detector.
func TestConcurrentCloseDuringUse(t *testing.T) {
	c := NewMemory(MemoryOptions{CleanupInterval: time.Millisecond})
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				key := fmt.Sprintf("k%d", i%20)
				// Errors are expected once Close lands; only ErrClosed and
				// ErrMiss are acceptable.
				if err := c.Set(ctx, key, []byte("v"), time.Minute); err != nil && !errors.Is(err, ErrClosed) {
					t.Errorf("Set: %v", err)
					return
				}
				if _, err := c.Get(ctx, key); err != nil &&
					!errors.Is(err, ErrClosed) && !errors.Is(err, ErrMiss) {
					t.Errorf("Get: %v", err)
					return
				}
			}
		}()
	}

	time.Sleep(2 * time.Millisecond)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()
}

func TestJSONRoundTrip(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	type product struct {
		SKU   string  `json:"sku"`
		Price float64 `json:"price"`
	}
	want := product{SKU: "ABC-123", Price: 19.99}

	if err := SetJSON(ctx, c, "products:sku:ABC-123", want, time.Minute); err != nil {
		t.Fatalf("SetJSON: %v", err)
	}
	got, err := GetJSON[product](ctx, c, "products:sku:ABC-123")
	if err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if got != want {
		t.Fatalf("GetJSON = %+v, want %+v", got, want)
	}
}

func TestGetJSONMiss(t *testing.T) {
	c, _ := newTestCache(t)

	_, err := GetJSON[int](context.Background(), c, "absent")
	if !errors.Is(err, ErrMiss) {
		t.Fatalf("GetJSON on absent key = %v, want ErrMiss", err)
	}
}

// TestGetJSONDecodeFailure documents that an incompatible stored value is an
// error rather than a miss: silently re-fetching would hide a key that needs
// versioning after a type change.
func TestGetJSONDecodeFailure(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "k", []byte(`{"sku":"ABC"}`), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	_, err := GetJSON[int](ctx, c, "k")
	if err == nil {
		t.Fatal("GetJSON with an incompatible value returned no error")
	}
	if errors.Is(err, ErrMiss) {
		t.Fatal("decode failure was reported as ErrMiss")
	}
}
