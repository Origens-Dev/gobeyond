package redisstore

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Origens-Dev/gobeyond/cache"
)

var (
	_ cache.Store            = (*Store)(nil)
	_ cache.Leaser           = (*Store)(nil)
	_ cache.TagBumpPublisher = (*Store)(nil)
	_ io.Closer              = (*Store)(nil)
)

// Store is GoBeyond's shared L2 cache.Store, backed by one Redis endpoint.
// See the package doc for the write-behind and tag-versioning design; Store
// itself is safe for concurrent use.
type Store struct {
	client     commander
	ownsClient bool
	tlsConfig  *tls.Config

	namespace string
	logger    *slog.Logger
	clock     func() time.Time

	writeTimeout time.Duration
	jobs         chan writeJob
	workers      sync.WaitGroup
	stats        poolStats

	closeMu sync.RWMutex
	closed  bool
}

// tagBumpMessage is the pub/sub payload BumpTag publishes and
// SubscribeTagBumps decodes.
type tagBumpMessage struct {
	Tag     string `json:"tag"`
	Version int64  `json:"version"`
}

// Get returns the record stored under key, treating a tag-invalidated entry
// the same as a missing one (Get, not Set, is where an L2 read-time
// invalidation belongs - see the package doc). ExpiresAt is derived from the
// key's remaining Redis TTL, not from anything stored in the payload.
func (s *Store) Get(ctx context.Context, key string) (cache.Record, bool, error) {
	payload, found, ttl, err := s.client.getWithTTL(ctx, key)
	if err != nil {
		return cache.Record{}, false, err
	}
	if !found {
		return cache.Record{}, false, nil
	}
	record, err := decodeRecord([]byte(payload))
	if err != nil {
		return cache.Record{}, false, err
	}
	if len(record.TagVersions) > 0 {
		current, err := s.currentTagVersions(ctx, record.TagVersions)
		if err != nil {
			return cache.Record{}, false, err
		}
		if !casAllows(current, record.TagVersions) {
			s.deleteAsync(key)
			return cache.Record{}, false, nil
		}
	}
	if ttl > 0 {
		record.ExpiresAt = s.clock().Add(ttl)
	}
	return record, true, nil
}

// currentTagVersions fetches the current counter for each tag in tags (as
// the decimal strings casAllows compares against), omitting tags whose
// counter key does not exist so casAllows treats them as "0".
func (s *Store) currentTagVersions(ctx context.Context, tags map[string]int64) (map[string]string, error) {
	names := make([]string, 0, len(tags))
	for tag := range tags {
		names = append(names, tag)
	}
	keys := make([]string, len(names))
	for i, tag := range names {
		keys[i] = tagKey(s.namespace, tag)
	}
	values, found, err := s.client.mget(ctx, keys)
	if err != nil {
		return nil, err
	}
	current := make(map[string]string, len(names))
	for i, tag := range names {
		if found[i] {
			current[tag] = values[i]
		}
	}
	return current, nil
}

// deleteAsync best-effort deletes an entry Get just found tag-invalidated. It
// runs on its own bounded, request-detached context because the request that
// discovered the staleness has already gotten its answer (a miss) and must
// not wait on cleanup.
func (s *Store) deleteAsync(key string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.writeTimeout)
		defer cancel()
		if err := s.client.del(ctx, key); err != nil {
			s.logger.Debug("redisstore: best-effort delete of invalidated entry failed", "key", key, "error", err)
		}
	}()
}

// Set enqueues record for a write-behind, write-time-CAS write and returns
// without talking to Redis - see the package doc. It therefore never returns
// cache.ErrStaleWrite; a stale write is instead silently rejected by the CAS
// and counted in Stats().Rejected. The only synchronous errors are input
// validation (ttl must be positive; the store's own TTL bound is Redis's PX,
// not a client-side clamp).
func (s *Store) Set(ctx context.Context, key string, record cache.Record, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("redisstore: Set requires a positive ttl")
	}
	payload, err := encodeRecord(record)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(record.TagVersions))
	for tag := range record.TagVersions {
		names = append(names, tag)
	}
	tagKeys := make([]string, len(names))
	expected := make([]int64, len(names))
	for i, tag := range names {
		tagKeys[i] = tagKey(s.namespace, tag)
		expected[i] = record.TagVersions[tag]
	}
	job := writeJob{
		ctx:              ctx,
		key:              key,
		payload:          string(payload),
		ttl:              ttl,
		tagKeys:          tagKeys,
		expectedVersions: expected,
	}

	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed {
		s.stats.dropped.Add(1)
		return nil
	}
	select {
	case s.jobs <- job:
		s.stats.enqueued.Add(1)
	default:
		s.stats.dropped.Add(1)
	}
	return nil
}

// Delete removes key synchronously; unlike Set it has no CAS to lose, so
// there is no reason to defer it to the write pool.
func (s *Store) Delete(ctx context.Context, key string) error {
	return s.client.del(ctx, key)
}

// TagVersions returns the current version of each requested tag, 0 for a tag
// whose counter key does not exist.
func (s *Store) TagVersions(ctx context.Context, tags []string) (map[string]int64, error) {
	keys := make([]string, len(tags))
	for i, tag := range tags {
		keys[i] = tagKey(s.namespace, tag)
	}
	values, found, err := s.client.mget(ctx, keys)
	if err != nil {
		return nil, err
	}
	versions := make(map[string]int64, len(tags))
	for i, tag := range tags {
		if !found[i] {
			versions[tag] = 0
			continue
		}
		version, err := strconv.ParseInt(values[i], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("redisstore: tag %q counter %q is not an integer: %w", tag, values[i], err)
		}
		versions[tag] = version
	}
	return versions, nil
}

// BumpTag increments tag's counter and best-effort publishes the new version
// so other instances' L1 can drop the tag's entries early. Tag counter keys
// are given no expiry: they are a handful of bytes each, one per tag ever
// used, and an expired counter would silently reset a tag to version 0 -
// resurrecting entries a bump was meant to invalidate rather than merely
// losing the pub/sub optimization a TTL'd key would only cost.
func (s *Store) BumpTag(ctx context.Context, tag string) error {
	key := tagKey(s.namespace, tag)
	version, err := s.client.incr(ctx, key)
	if err != nil {
		return err
	}
	message, err := json.Marshal(tagBumpMessage{Tag: tag, Version: version})
	if err != nil {
		return err
	}
	if err := s.client.publish(ctx, tagBumpChannel(s.namespace), string(message)); err != nil {
		s.logger.Warn("redisstore: tag bump publish failed", "tag", tag, "error", err)
	}
	return nil
}

// SubscribeTagBumps decodes BumpTag's broadcasts and invokes onBump for each
// one, blocking until ctx is canceled. It returns nil in that case (a
// canceled subscription is the normal shutdown path, not a failure); a
// non-nil error means the subscription itself broke. A malformed message is
// logged and skipped rather than propagated.
func (s *Store) SubscribeTagBumps(ctx context.Context, onBump func(tag string, version int64)) error {
	return s.client.subscribe(ctx, tagBumpChannel(s.namespace), func(payload string) {
		var msg tagBumpMessage
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			s.logger.Warn("redisstore: dropping malformed tag bump message", "error", err)
			return
		}
		onBump(msg.Tag, msg.Version)
	})
}

// AcquireLease reports whether the caller now holds key's lease, via a
// SET NX PX so at most one caller across every instance ever wins it.
func (s *Store) AcquireLease(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	token, err := leaseToken()
	if err != nil {
		return false, err
	}
	return s.client.setNX(ctx, key, token, ttl)
}

// leaseToken returns a random value for a lease key. Its only requirement is
// uniqueness enough to be a harmless SET payload; nothing ever reads it back.
func leaseToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// Stats snapshots the write-behind pool's lifetime counters.
func (s *Store) Stats() Stats {
	return s.stats.snapshot()
}

// Close stops accepting new writes, waits for in-flight and already-queued
// writes to drain, and - only when Store built the client itself, per
// Options.Client's contract - closes it. Calling Close more than once is
// safe.
func (s *Store) Close() error {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil
	}
	s.closed = true
	close(s.jobs)
	s.closeMu.Unlock()

	s.workers.Wait()
	if s.ownsClient {
		return s.client.close()
	}
	return nil
}
