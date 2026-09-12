package cache

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrClosed is returned by a Memory cache used after Close. Silently succeeding
// would hide a lifecycle bug — a cache still being written to after shutdown
// began is a goroutine that outlived its owner.
var ErrClosed = errors.New("cache: closed")

// DefaultCleanupInterval is how often Memory sweeps expired entries when
// MemoryOptions.CleanupInterval is unset.
const DefaultCleanupInterval = time.Minute

// MemoryOptions configures NewMemory.
type MemoryOptions struct {
	// CleanupInterval is the period between eviction sweeps. A negative value
	// disables the background goroutine entirely, leaving only the lazy expiry
	// that Get performs. Zero means DefaultCleanupInterval.
	CleanupInterval time.Duration

	// Now overrides the clock. Tests inject a controllable one so that TTL
	// behaviour can be asserted without sleeping.
	Now func() time.Time
}

type entry struct {
	value []byte

	// expiresAt is the zero time for an entry that never expires.
	expiresAt time.Time
}

func (e entry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && now.After(e.expiresAt)
}

// Memory is an in-process Cache backed by a map guarded by a mutex, with a
// background goroutine that evicts expired entries.
//
// Close stops that goroutine and waits for it to exit. This is not incidental:
// the rate limiter in internal/middleware spawns its cleanup as
//
//	go func() { for { time.Sleep(time.Minute); ... } }()
//
// which cannot be stopped, cannot be waited on, and keeps its map alive for the
// life of the process. Every test that constructs one leaks a goroutine, so
// `-race` and goroutine-leak detection are permanently noisy. Memory takes a
// stop channel and a WaitGroup so that Close is a real shutdown.
//
// Entries stored with a non-positive TTL never expire, so a caller that only
// ever uses ttl <= 0 will grow this map without bound. Give cached entries a
// TTL, or call Delete/DeletePrefix on write.
type Memory struct {
	mu    sync.RWMutex
	items map[string]entry

	now func() time.Time

	// stop is closed by Close to signal the janitor. closeOnce makes Close
	// idempotent; done is closed by the janitor as it exits.
	closeOnce sync.Once
	stop      chan struct{}
	wg        sync.WaitGroup

	// closed is read under mu so that Get and Set observe it consistently.
	closed bool
}

// Compile-time proof that Memory satisfies the interface.
var _ Cache = (*Memory)(nil)

// NewMemory returns a running in-process cache. The caller owns it and must
// Close it; the usual shape is a defer in main next to the pool's Close.
func NewMemory(opts MemoryOptions) *Memory {
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	m := &Memory{
		items: make(map[string]entry),
		now:   now,
		stop:  make(chan struct{}),
	}

	interval := opts.CleanupInterval
	if interval == 0 {
		interval = DefaultCleanupInterval
	}
	if interval > 0 {
		m.wg.Add(1)
		go m.janitor(interval)
	}
	return m
}

// janitor sweeps expired entries until stop is closed.
func (m *Memory) janitor(interval time.Duration) {
	defer m.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.evictExpired()
		}
	}
}

// evictExpired removes every entry whose TTL has passed.
func (m *Memory) evictExpired() {
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()
	for key, e := range m.items {
		if e.expired(now) {
			delete(m.items, key)
		}
	}
}

// Get returns the value for key, or ErrMiss.
//
// Expiry is checked here as well as in the janitor, so a TTL is honoured to the
// instant rather than to the next sweep.
func (m *Memory) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	e, ok := m.items[key]
	closed := m.closed
	m.mu.RUnlock()

	if closed {
		return nil, ErrClosed
	}
	if !ok {
		return nil, ErrMiss
	}
	if e.expired(m.now()) {
		// Drop it now rather than waiting for the sweep, so a hot expired key
		// does not keep its bytes alive.
		m.mu.Lock()
		if cur, still := m.items[key]; still && cur.expired(m.now()) {
			delete(m.items, key)
		}
		m.mu.Unlock()
		return nil, ErrMiss
	}

	// Copy on the way out. The caller may modify what it receives, and a
	// network-backed cache would have handed it a fresh slice; behaving the
	// same way here means code cannot come to depend on the difference.
	out := make([]byte, len(e.value))
	copy(out, e.value)
	return out, nil
}

// Set stores value under key for ttl.
func (m *Memory) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Copy on the way in, so a caller reusing its buffer cannot mutate what is
	// cached.
	stored := make([]byte, len(value))
	copy(stored, value)

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = m.now().Add(ttl)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	m.items[key] = entry{value: stored, expiresAt: expiresAt}
	return nil
}

// Delete removes keys.
func (m *Memory) Delete(ctx context.Context, keys ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	for _, key := range keys {
		delete(m.items, key)
	}
	return nil
}

// DeletePrefix removes every key beginning with prefix. An empty prefix clears
// the cache.
func (m *Memory) DeletePrefix(ctx context.Context, prefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if prefix == "" {
		m.items = make(map[string]entry)
		return nil
	}
	for key := range m.items {
		if strings.HasPrefix(key, prefix) {
			delete(m.items, key)
		}
	}
	return nil
}

// Len reports how many entries are held, including any that have expired but
// not yet been swept. For tests and metrics.
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.items)
}

// Close stops the janitor, waits for it to exit, and drops every entry.
//
// It is idempotent and safe to call concurrently with any other method. After
// it returns, no goroutine started by this cache is running — which is what
// makes it safe to call from a test without leaking into the next one.
func (m *Memory) Close() error {
	m.closeOnce.Do(func() {
		close(m.stop)
		m.wg.Wait()

		m.mu.Lock()
		m.closed = true
		m.items = nil
		m.mu.Unlock()
	})
	return nil
}
