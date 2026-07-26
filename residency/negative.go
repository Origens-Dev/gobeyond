package residency

import (
	"container/list"
	"errors"
	"time"
)

// ImmutableError marks err as an immutable failure: one that cannot heal
// within the current build, such as a record digest mismatch or a decode
// error over immutable bytes. Only errors marked immutable enter the
// negative cache; transient failures (I/O, deadline) are always retried.
// The returned error unwraps to err.
func ImmutableError(err error) error {
	if err == nil {
		return nil
	}
	return &immutableError{err: err}
}

// IsImmutable reports whether err (or an error it wraps) is marked as an
// immutable failure, either via ImmutableError or by implementing
// ImmutableFailure() bool returning true.
func IsImmutable(err error) bool {
	var marked interface{ ImmutableFailure() bool }
	return errors.As(err, &marked) && marked.ImmutableFailure()
}

type immutableError struct{ err error }

func (e *immutableError) Error() string          { return e.err.Error() }
func (e *immutableError) Unwrap() error          { return e.err }
func (e *immutableError) ImmutableFailure() bool { return true }

// negativeEntry remembers an immutable load failure for one key so repeated
// requests do not re-run a decode that cannot succeed this build.
type negativeEntry struct {
	err     error
	expires time.Time
	elem    *list.Element // element in negativeOrder; its value is the key
}

// negativeLookupLocked returns the remembered failure for key, dropping it
// first if its TTL has lapsed. Callers must hold c.mu.
func (c *Cache[V]) negativeLookupLocked(key string) (error, bool) {
	ne, ok := c.negative[key]
	if !ok {
		return nil, false
	}
	if !c.now().Before(ne.expires) {
		c.removeNegativeLocked(key)
		return nil, false
	}
	return ne.err, true
}

// storeNegativeLocked remembers an immutable failure, evicting the oldest
// remembered failures to stay within the bound. Callers must hold c.mu.
func (c *Cache[V]) storeNegativeLocked(key string, err error) {
	if c.negativeMax <= 0 {
		return
	}
	expires := c.now().Add(c.negativeTTL)
	if existing, ok := c.negative[key]; ok {
		existing.err = err
		existing.expires = expires
		c.negativeOrder.MoveToBack(existing.elem)
		return
	}
	for len(c.negative) >= c.negativeMax {
		oldest := c.negativeOrder.Front()
		if oldest == nil {
			break
		}
		c.removeNegativeLocked(oldest.Value.(string))
	}
	ne := &negativeEntry{err: err, expires: expires}
	ne.elem = c.negativeOrder.PushBack(key)
	c.negative[key] = ne
}

// removeNegativeLocked forgets the remembered failure for key, if any.
// Callers must hold c.mu.
func (c *Cache[V]) removeNegativeLocked(key string) {
	ne, ok := c.negative[key]
	if !ok {
		return
	}
	c.negativeOrder.Remove(ne.elem)
	delete(c.negative, key)
}
