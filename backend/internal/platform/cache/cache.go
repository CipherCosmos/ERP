// Package cache defines the caching interface the application codes against,
// plus an in-process TTL implementation.
//
// # Why []byte and not any
//
// The interface stores []byte rather than arbitrary Go values. That is a
// deliberate constraint, and it is the whole reason this interface exists.
//
// Redis is deferred for this deployment: 1-3 stores on a single VPS, where
// Postgres absorbs the load and every extra service is one more thing to run,
// secure, back up and page someone about. But it will not be deferred forever,
// and an interface that stores `any` cannot be backed by Redis without changing
// every caller — a map returned from an in-process cache is the same object the
// last caller had, whereas Redis hands back bytes that must be decoded. Code
// written against `any` silently depends on shared mutable state, and the day
// Redis arrives that dependency surfaces as a class of aliasing bugs.
//
// Storing bytes means the in-process implementation behaves like a remote one
// today: values are copied in and out, and callers must serialise. GetJSON and
// SetJSON make that a one-liner.
//
// # Choosing a key
//
// Namespace keys by domain with a colon separator, so that DeletePrefix can
// invalidate a whole family after a write:
//
//	products:store:42:page:1
//	products:sku:ABC-123
//
// After any product write in store 42, call DeletePrefix(ctx, "products:").
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrMiss is returned by Get when the key is absent or expired. It is a normal
// outcome, not a failure: test for it with errors.Is and fall through to the
// source of truth.
var ErrMiss = errors.New("cache: key not found")

// Cache is a key-value store with per-entry expiry.
//
// Implementations must be safe for concurrent use.
//
// Every method takes a context so that a network-backed implementation can
// honour cancellation and deadlines. The in-process implementation checks it
// too, so that code does not start depending on a cache that never fails.
type Cache interface {
	// Get returns the value for key, or ErrMiss if it is absent or expired.
	// The returned slice belongs to the caller and may be modified.
	Get(ctx context.Context, key string) ([]byte, error)

	// Set stores value under key for ttl. A ttl of zero or less stores the
	// entry without expiry. Implementations copy value; the caller may reuse
	// the slice.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// Delete removes keys. Removing a key that is not present is not an error.
	Delete(ctx context.Context, keys ...string) error

	// DeletePrefix removes every key beginning with prefix. This is the
	// invalidation primitive: one write to a table drops the whole family of
	// cached reads derived from it.
	//
	// An empty prefix removes everything.
	DeletePrefix(ctx context.Context, prefix string) error

	// Close releases resources and stops any background goroutine. It is
	// idempotent.
	Close() error
}

// GetJSON reads key and decodes it into T.
//
// It returns ErrMiss on a miss, so the calling shape is:
//
//	p, err := cache.GetJSON[Product](ctx, c, key)
//	switch {
//	case err == nil:
//	    return p, nil
//	case errors.Is(err, cache.ErrMiss):
//	    // fall through to the database
//	default:
//	    // a real cache failure; log it and fall through anyway
//	}
//
// A decode failure is reported as an error rather than as a miss, because it
// means something wrote an incompatible value under this key — usually a type
// changed without the key being versioned, which is worth noticing.
func GetJSON[T any](ctx context.Context, c Cache, key string) (T, error) {
	var zero T
	raw, err := c.Get(ctx, key)
	if err != nil {
		return zero, err
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return zero, fmt.Errorf("cache: decode %q: %w", key, err)
	}
	return value, nil
}

// SetJSON encodes value as JSON and stores it under key for ttl.
func SetJSON[T any](ctx context.Context, c Cache, key string, value T, ttl time.Duration) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cache: encode %q: %w", key, err)
	}
	return c.Set(ctx, key, raw, ttl)
}
