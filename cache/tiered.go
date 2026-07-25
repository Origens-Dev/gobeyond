package cache

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"time"
)

// TieredOptions configures the composite store. The zero value is valid.
type TieredOptions struct {
	// Logger receives L1/L2 transport failures, which are degradations rather
	// than request failures: a tier that cannot answer is treated as a miss.
	Logger *slog.Logger
	// Clock overrides time.Now, for tests.
	Clock func() time.Time
}

type tieredStore struct {
	l1     Store
	l2     Store
	logger *slog.Logger
	now    func() time.Time
}

var (
	_ Store     = (*tieredStore)(nil)
	_ Leaser    = (*tieredStore)(nil)
	_ io.Closer = (*tieredStore)(nil)
)

// Tiered composes a fast local tier with a shared one: reads go L1 then L2 and
// populate L1 on the way back, writes go to both (L1 synchronously, L2 however
// that store chooses - the Redis tier writes behind), and tag bumps go to the
// shared tier first because it is authoritative.
//
// A nil l2 is the supported degraded mode, not an error: a deployment with no
// shared cache endpoint configured runs L1-only, and every caller above this
// point behaves identically.
func Tiered(l1, l2 Store, options TieredOptions) Store {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := options.Clock
	if now == nil {
		now = time.Now
	}
	return &tieredStore{l1: presentTier(l1), l2: presentTier(l2), logger: logger, now: now}
}

// presentTier normalizes a tier that is a nil pointer inside a non-nil
// interface to a plain nil. Wiring code naturally produces one - a
// redisstore.FromEnv that reported "no cache configured" assigned into a
// cache.Store variable - and the difference is invisible at the call site, so
// catching it here is cheaper than a panic on the first request.
func presentTier(tier Store) Store {
	if tier == nil {
		return nil
	}
	value := reflect.ValueOf(tier)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil
	}
	return tier
}

func (t *tieredStore) Get(ctx context.Context, key string) (Record, bool, error) {
	if t.l1 != nil {
		record, hit, err := t.l1.Get(ctx, key)
		if err != nil {
			t.logger.Warn("cache L1 read failed", "error", err)
		} else if hit {
			return record, true, nil
		}
	}
	if t.l2 == nil {
		return Record{}, false, nil
	}
	record, hit, err := t.l2.Get(ctx, key)
	if err != nil || !hit {
		return Record{}, false, err
	}
	if t.l1 != nil {
		if ttl := record.ExpiresAt.Sub(t.now()); ttl > 0 {
			if err := t.l1.Set(ctx, key, record, ttl); err != nil && !errors.Is(err, ErrStaleWrite) {
				t.logger.Warn("cache L1 write-back failed", "error", err)
			}
		}
	}
	return record, true, nil
}

func (t *tieredStore) Set(ctx context.Context, key string, record Record, ttl time.Duration) error {
	var errs []error
	if t.l1 != nil {
		if err := t.l1.Set(ctx, key, record, ttl); err != nil {
			errs = append(errs, err)
		}
	}
	if t.l2 != nil {
		if err := t.l2.Set(ctx, key, record, ttl); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (t *tieredStore) Delete(ctx context.Context, key string) error {
	var errs []error
	if t.l1 != nil {
		errs = append(errs, t.l1.Delete(ctx, key))
	}
	if t.l2 != nil {
		errs = append(errs, t.l2.Delete(ctx, key))
	}
	return errors.Join(errs...)
}

// TagVersions answers from the shared tier when there is one: those counters
// are the ones the L2 write-time compare-and-set will be checked against, so
// fencing a fill with anything else would let a write slip through.
func (t *tieredStore) TagVersions(ctx context.Context, tags []string) (map[string]int64, error) {
	if t.l2 != nil {
		return t.l2.TagVersions(ctx, tags)
	}
	if t.l1 != nil {
		return t.l1.TagVersions(ctx, tags)
	}
	return map[string]int64{}, nil
}

// BumpTag bumps the shared tier first and then drops the local one. Both are
// synchronous: an action must not return until this instance can no longer
// serve the invalidated entries and every other instance's next L2 read sees
// the new version.
func (t *tieredStore) BumpTag(ctx context.Context, tag string) error {
	var errs []error
	if t.l2 != nil {
		errs = append(errs, t.l2.BumpTag(ctx, tag))
	}
	if t.l1 != nil {
		errs = append(errs, t.l1.BumpTag(ctx, tag))
	}
	return errors.Join(errs...)
}

// AcquireLease prefers the shared tier so a refresh is deduplicated across
// instances, and falls back to the local one so an L1-only deployment still
// deduplicates within the process.
func (t *tieredStore) AcquireLease(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	for _, tier := range []Store{t.l2, t.l1} {
		if tier == nil {
			continue
		}
		if leaser, ok := tier.(Leaser); ok {
			return leaser.AcquireLease(ctx, key, ttl)
		}
	}
	return true, nil
}

func (t *tieredStore) Close() error {
	var errs []error
	for _, tier := range []Store{t.l1, t.l2} {
		if tier == nil {
			continue
		}
		if closer, ok := tier.(io.Closer); ok {
			errs = append(errs, closer.Close())
		}
	}
	return errors.Join(errs...)
}
