package cache

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"
)

// EnvDeployPrefix names the environment variable the deployment injects with
// this deploy's cache namespace (see infra/opentofu/compute.tf). The prefix is
// the tenant isolation boundary inside a possibly shared cache instance
// (Locked decision 15).
const EnvDeployPrefix = "GOBEYOND_CACHE_KEY_PREFIX"

// Runtime defaults. MaxStale bounds how long a stale entry may be served while
// a refresh runs; RefreshTimeout bounds the detached refresh itself and
// doubles as the TTL of the lease that keeps other instances from refreshing
// the same key at the same time.
const (
	DefaultMaxStale       = 60 * time.Second
	DefaultRefreshTimeout = 15 * time.Second
	EnvCacheGeneration    = "GOBEYOND_CACHE_GENERATION"
)

const refreshLeaseSuffix = "#refresh"

// RuntimeConfig describes the cache handle a server installs once at startup.
// A Runtime built from it is the only thing that knows the deploy prefix and
// BuildID, which is why cache.Load and cache.Revalidate* need one on the
// request's RequestScope: those two values are part of every key and must not
// be re-derived (or guessed) per call site.
type RuntimeConfig struct {
	// DeployPrefix namespaces this deploy's keys. Required.
	DeployPrefix string
	// BuildID namespaces this build's value shapes. runtime.New fills it in
	// from its own configuration so the two can never disagree.
	BuildID string
	// Generation changes when application data is invalidated. It is separate
	// from BuildID and route topology revisions.
	Generation string
	// Store is the byte tier to read and write, usually Tiered(l1, l2).
	// Required.
	Store Store
	// MaxStale bounds the stale-while-revalidate window past an entry's
	// revalidate deadline. It also sets the entry's hard TTL, which is
	// Revalidate + MaxStale.
	MaxStale time.Duration
	// RefreshTimeout bounds a background refresh and the lease guarding it.
	RefreshTimeout time.Duration
	Logger         *slog.Logger
	// Clock overrides time.Now, for tests.
	Clock func() time.Time
}

// Runtime is the installed cache handle. It is safe for concurrent use and is
// shared by every request the server serves.
type Runtime struct {
	deployPrefix   string
	buildID        string
	generation     string
	store          Store
	maxStale       time.Duration
	refreshTimeout time.Duration
	logger         *slog.Logger
	now            func() time.Time

	flight     flightGroup
	refreshing inFlightSet
}

// NewRuntime validates config and returns the handle to install on request
// scopes. It fails rather than defaulting the prefix or BuildID: a cache whose
// namespace is guessed can serve one deploy's or one build's data to another.
func NewRuntime(config RuntimeConfig) (*Runtime, error) {
	if config.DeployPrefix == "" {
		return nil, errors.New("cache: runtime requires a deploy prefix")
	}
	if config.BuildID == "" {
		return nil, errors.New("cache: runtime requires a build ID")
	}
	if config.Store == nil {
		return nil, errors.New("cache: runtime requires a store")
	}
	runtime := &Runtime{
		deployPrefix:   config.DeployPrefix,
		buildID:        config.BuildID,
		generation:     config.Generation,
		store:          config.Store,
		maxStale:       config.MaxStale,
		refreshTimeout: config.RefreshTimeout,
		logger:         config.Logger,
		now:            config.Clock,
	}
	if runtime.maxStale <= 0 {
		runtime.maxStale = DefaultMaxStale
	}
	if runtime.refreshTimeout <= 0 {
		runtime.refreshTimeout = DefaultRefreshTimeout
	}
	if runtime.logger == nil {
		runtime.logger = slog.Default()
	}
	if runtime.now == nil {
		runtime.now = time.Now
	}
	return runtime, nil
}

// DeployPrefix returns the namespace every key this runtime builds starts with.
func (rt *Runtime) DeployPrefix() string { return rt.deployPrefix }

// BuildID returns the build namespace every key this runtime builds carries.
func (rt *Runtime) BuildID() string { return rt.buildID }

func (rt *Runtime) Generation() string { return rt.generation }

// Store returns the byte tier this runtime reads and writes.
func (rt *Runtime) Store() Store { return rt.store }

// DeployPrefixFromEnv returns the deploy cache namespace the platform injected,
// or "" when this deployment has no shared cache configured.
func DeployPrefixFromEnv() string { return os.Getenv(EnvDeployPrefix) }

func GenerationFromEnv() string { return os.Getenv(EnvCacheGeneration) }

// acquireRefreshLease reports whether this instance should run a background
// refresh. A store without leases, or a lease attempt that errors, answers yes:
// the lease exists to keep every instance from recomputing the same value at
// once, and refusing to refresh because the coordination channel is unavailable
// would turn an optimization into an outage.
func (rt *Runtime) acquireRefreshLease(ctx context.Context, key string) bool {
	leaser, ok := rt.store.(Leaser)
	if !ok {
		return true
	}
	granted, err := leaser.AcquireLease(ctx, key+refreshLeaseSuffix, rt.refreshTimeout)
	if err != nil {
		rt.logger.Warn("cache refresh lease unavailable", "error", err)
		return true
	}
	return granted
}

// inFlightSet tracks the keys this process is already refreshing in the
// background, so a stale entry served to a burst of requests spawns one
// refresh goroutine rather than one per request.
type inFlightSet struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func (s *inFlightSet) enter(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		s.keys = make(map[string]struct{})
	}
	if _, running := s.keys[key]; running {
		return false
	}
	s.keys[key] = struct{}{}
	return true
}

func (s *inFlightSet) leave(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, key)
}
