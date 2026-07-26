package residency

import (
	"container/list"
	"time"
)

// entry is one resident value. Its list element lives in the probation
// segment until the entry's first post-insert hit promotes it, and in the
// protected segment afterwards (until demotion or removal).
type entry[V any] struct {
	key        string
	value      V
	weight     int64
	lastAccess time.Time
	protected  bool
	elem       *list.Element
}

// insertLocked adds a freshly loaded value at the probation MRU position and
// re-enforces the budgets. Callers must hold c.mu.
func (c *Cache[V]) insertLocked(key string, value V, weight int64) {
	if old, ok := c.entries[key]; ok {
		c.removeEntryLocked(old)
	}
	e := &entry[V]{key: key, value: value, weight: weight, lastAccess: c.now()}
	e.elem = c.probation.PushFront(e)
	c.entries[key] = e
	c.bytes += weight
	c.enforceBudgetsLocked()
}

// touchLocked records a hit: protected entries move to their segment's MRU
// position, probation entries are promoted to protected. Promotion may push
// the protected segment over its share of the budgets, in which case its LRU
// entries are demoted back to probation (at the probation MRU position, so
// they get one more chance before eviction). Callers must hold c.mu.
func (c *Cache[V]) touchLocked(e *entry[V]) {
	if e.protected {
		c.protected.MoveToFront(e.elem)
		return
	}
	c.probation.Remove(e.elem)
	e.protected = true
	e.elem = c.protected.PushFront(e)
	c.protectedBytes += e.weight
	for (c.protected.Len() > c.protectedMaxEntries || c.protectedBytes > c.protectedMaxBytes) && c.protected.Len() > 1 {
		back := c.protected.Back()
		demoted := back.Value.(*entry[V])
		c.protected.Remove(back)
		demoted.protected = false
		demoted.elem = c.probation.PushFront(demoted)
		c.protectedBytes -= demoted.weight
	}
}

// enforceBudgetsLocked evicts until both the entry and byte budgets hold.
// Callers must hold c.mu.
func (c *Cache[V]) enforceBudgetsLocked() {
	for len(c.entries) > c.maxEntries || c.bytes > c.maxBytes {
		if !c.evictOneLocked() {
			return
		}
	}
}

// evictOneLocked drops the best eviction candidate: the probation LRU entry,
// falling back to the protected LRU entry when probation is empty. It only
// removes the cache's reference; callers already holding the value keep a
// valid one. Callers must hold c.mu.
func (c *Cache[V]) evictOneLocked() bool {
	victim := c.probation.Back()
	if victim == nil {
		victim = c.protected.Back()
	}
	if victim == nil {
		return false
	}
	c.removeEntryLocked(victim.Value.(*entry[V]))
	c.evictions++
	return true
}

// removeEntryLocked unlinks e from its segment and the key map and returns
// its weight to the budgets. Callers must hold c.mu.
func (c *Cache[V]) removeEntryLocked(e *entry[V]) {
	if e.protected {
		c.protected.Remove(e.elem)
		c.protectedBytes -= e.weight
	} else {
		c.probation.Remove(e.elem)
	}
	delete(c.entries, e.key)
	c.bytes -= e.weight
}
