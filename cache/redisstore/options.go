package redisstore

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Environment variables FromEnv reads. Names and defaulting behavior mirror
// what infra/opentofu/compute.tf injects into the task's environment when a
// cache is provisioned, so the runtime can wire a Store from the deployment
// without any deploy-specific code.
const (
	// EnvEndpoint is the ElastiCache Serverless endpoint host. Its absence
	// is the signal FromEnv uses to report "no cache configured".
	EnvEndpoint = "GOBEYOND_CACHE_ENDPOINT"
	// EnvPort is the endpoint's port; DefaultPort applies when unset.
	EnvPort = "GOBEYOND_CACHE_PORT"
	// EnvKeyPrefix seeds Options.Namespace.
	EnvKeyPrefix = "GOBEYOND_CACHE_KEY_PREFIX"
	// EnvUsername and EnvPassword seed Options.Username / Options.Password
	// for Redis AUTH. Either or both may be unset.
	EnvUsername = "GOBEYOND_CACHE_USERNAME"
	EnvPassword = "GOBEYOND_CACHE_PASSWORD"
	// EnvTLS force-disables TLS for local development against a plaintext
	// Redis. It is read only when Options.DisableTLS was not already set to
	// true explicitly, and only a value that parses as boolean false (e.g.
	// "false", "0") disables TLS; any other value, including unset, leaves
	// the secure default in place.
	EnvTLS = "GOBEYOND_CACHE_TLS"
)

// DefaultPort is the port FromEnv assumes when EnvPort is unset.
const DefaultPort = "6379"

// Defaults for the owned redis.UniversalClient's network timeouts.
// ElastiCache Serverless sits behind a VPC hop, so both are a little more
// generous than go-redis's own defaults.
const (
	DefaultDialTimeout = 2 * time.Second
	DefaultReadTimeout = 1 * time.Second
)

// Options configures a Store. The zero value is not valid on its own: New
// requires either Addr (to build an owned client) or Client (an injected
// one).
type Options struct {
	// Addr is the "host:port" of a single Redis endpoint. Required unless
	// Client is set. ElastiCache Serverless exposes one endpoint that
	// transparently scales, so this package targets a single address rather
	// than a cluster topology.
	Addr string
	// Namespace scopes tag-version keys and the tag-bump channel to one
	// deploy; see the package doc. Entry keys are not namespaced here - the
	// caller already did that (cache.RouteKey / cache.DataKey).
	Namespace string
	// Username and Password authenticate to Redis via AUTH. Both are
	// optional; ElastiCache Serverless with an auth token uses Password
	// only ("default" user).
	Username, Password string
	// TLS overrides the TLS config used to dial Redis. When nil and
	// DisableTLS is false, New builds one itself with ServerName set to
	// Addr's host, matching ElastiCache Serverless's requirement that
	// clients speak TLS with a verifiable certificate.
	TLS *tls.Config
	// DisableTLS connects in plaintext, for local development against a
	// Redis started without TLS. It must never be set in a deployment that
	// talks to ElastiCache Serverless, which requires TLS.
	DisableTLS bool
	// Client injects a pre-built redis.UniversalClient (e.g. a cluster
	// client, or a client shared with other packages) and takes ownership
	// away from Store: Close will not close it. When set, Addr, Username,
	// Password, TLS, and DisableTLS are ignored - the client is already
	// fully configured.
	Client redis.UniversalClient
	// WriteWorkers and WriteQueue bound the write-behind pool: WriteWorkers
	// goroutines drain a channel buffered to WriteQueue. DefaultWriteWorkers
	// / DefaultWriteQueue apply when either is <= 0.
	WriteWorkers int
	WriteQueue   int
	// WriteTimeout bounds one queued write's Redis round trip, applied to a
	// context detached from the caller's (see the package doc's write-behind
	// section). DefaultWriteTimeout applies when <= 0.
	WriteTimeout time.Duration
	// DialTimeout and ReadTimeout configure the owned client's network
	// timeouts; ignored when Client is set. Defaults above apply when <= 0.
	DialTimeout time.Duration
	ReadTimeout time.Duration
	// Logger receives write-behind failures and other best-effort-operation
	// warnings. slog.Default() applies when nil.
	Logger *slog.Logger
	// Clock overrides time.Now, for tests.
	Clock func() time.Time
}

// New creates a Store. Callers that do not have a pre-built client typically
// use FromEnv instead, which also decides whether a Store should exist at
// all for this deployment.
func New(opts Options) (*Store, error) {
	if opts.Client == nil && opts.Addr == "" {
		return nil, fmt.Errorf("redisstore: New requires Options.Addr or Options.Client")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	workers := opts.WriteWorkers
	if workers <= 0 {
		workers = DefaultWriteWorkers
	}
	queueSize := opts.WriteQueue
	if queueSize <= 0 {
		queueSize = DefaultWriteQueue
	}
	writeTimeout := opts.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = DefaultWriteTimeout
	}

	ownsClient := opts.Client == nil
	client := opts.Client
	var tlsConfig *tls.Config
	if ownsClient {
		built, resolvedTLS, err := newUniversalClient(opts)
		if err != nil {
			return nil, err
		}
		client, tlsConfig = built, resolvedTLS
	}

	store := &Store{
		client:       newRedisCommander(client),
		ownsClient:   ownsClient,
		tlsConfig:    tlsConfig,
		namespace:    opts.Namespace,
		logger:       logger,
		clock:        clock,
		writeTimeout: writeTimeout,
		jobs:         make(chan writeJob, queueSize),
	}
	store.startWorkers(workers)
	return store, nil
}

// newUniversalClient builds the redis.UniversalClient New owns, plus the TLS
// config it resolved (returned separately so tests can inspect it without
// reaching into the client, which does not expose its config).
func newUniversalClient(opts Options) (redis.UniversalClient, *tls.Config, error) {
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil {
		return nil, nil, fmt.Errorf("redisstore: invalid Addr %q: %w", opts.Addr, err)
	}
	tlsConfig := opts.TLS
	if tlsConfig == nil && !opts.DisableTLS {
		tlsConfig = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	}
	dialTimeout := opts.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = DefaultDialTimeout
	}
	readTimeout := opts.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = DefaultReadTimeout
	}
	client := redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:       []string{opts.Addr},
		Username:    opts.Username,
		Password:    opts.Password,
		TLSConfig:   tlsConfig,
		DialTimeout: dialTimeout,
		ReadTimeout: readTimeout,
	})
	return client, tlsConfig, nil
}

// FromEnv builds a Store from the environment a GoBeyond deployment injects
// (see the Env constants). It returns (nil, false, nil) when EnvEndpoint is
// unset or empty, which is not an error: the caller degrades to an L1-only
// cache. Fields already set on opts win over the corresponding environment
// variable, so a caller can override any single piece (e.g. inject a test
// Client, or force a different Namespace) while still picking up the rest
// from the environment.
func FromEnv(opts Options) (*Store, bool, error) {
	endpoint := os.Getenv(EnvEndpoint)
	if endpoint == "" {
		return nil, false, nil
	}
	if opts.Addr == "" {
		port := os.Getenv(EnvPort)
		if port == "" {
			port = DefaultPort
		}
		opts.Addr = net.JoinHostPort(endpoint, port)
	}
	if opts.Namespace == "" {
		opts.Namespace = os.Getenv(EnvKeyPrefix)
	}
	if opts.Username == "" {
		opts.Username = os.Getenv(EnvUsername)
	}
	if opts.Password == "" {
		opts.Password = os.Getenv(EnvPassword)
	}
	if !opts.DisableTLS {
		if enabled, ok := parseBoolEnv(os.Getenv(EnvTLS)); ok && !enabled {
			opts.DisableTLS = true
		}
	}
	store, err := New(opts)
	if err != nil {
		return nil, false, err
	}
	return store, true, nil
}

// parseBoolEnv parses raw as a bool, with ok reporting whether raw was a
// recognized boolean at all (an empty or garbage value is "unset", not
// "false").
func parseBoolEnv(raw string) (value, ok bool) {
	if raw == "" {
		return false, false
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false
	}
	return parsed, true
}
