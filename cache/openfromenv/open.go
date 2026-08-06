// Package openfromenv provides the supported cache constructor: a bounded
// in-process L1, an optional Redis L2 when GOBEYOND_CACHE_* is set, and the
// tag-bump watcher that drops local copies early when L2 is present.
//
// It lives in a subpackage so cache can keep its Store interface free of an
// import cycle with cache/memstore and cache/redisstore.
package openfromenv

import (
	"cmp"
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/Origens-Dev/gobeyond/cache"
	"github.com/Origens-Dev/gobeyond/cache/memstore"
	"github.com/Origens-Dev/gobeyond/cache/redisstore"
)

// OpenFromEnv builds the deployment cache assembly:
//
//  1. bounded memstore L1 (default MaxEntries/MaxBytes/MaxTTL)
//  2. redisstore.FromEnv — absent endpoint is success (L1-only)
//  3. cache.Tiered(l1, l2)
//  4. WatchTagBumps when L2 is present
//
// The returned RuntimeConfig is ready for runtime.Config.Cache (BuildID is
// filled by runtime.New). Close cancels the tag-bump watcher and closes the
// Redis client when one was opened; it is safe to call more than once.
func OpenFromEnv() (config *cache.RuntimeConfig, close func() error, err error) {
	return Open(Options{})
}

// Options configures Open. The zero value is the supported deployment default.
type Options struct {
	// Logger receives tiered-store warnings. slog.Default applies when nil.
	Logger *slog.Logger
	// Memstore overrides L1 bounds. The zero value uses memstore defaults.
	Memstore memstore.Options
	// Redis overrides redisstore.FromEnv fields. The zero value reads the
	// GOBEYOND_CACHE_* environment entirely.
	Redis redisstore.Options
}

// Open is OpenFromEnv with explicit options (tests, custom logger).
func Open(opts Options) (config *cache.RuntimeConfig, closeFn func() error, err error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Redis.Logger == nil {
		opts.Redis.Logger = logger
	}

	local := memstore.New(opts.Memstore)

	var shared cache.Store
	var redisCloser io.Closer
	store, configured, err := redisstore.FromEnv(opts.Redis)
	if err != nil {
		return nil, nil, err
	}
	if configured {
		shared = store
		redisCloser = store
	} else {
		logger.Warn("cache L2 unavailable; serving with bounded in-process L1 only")
	}

	tiered := cache.Tiered(local, shared, cache.TieredOptions{Logger: logger})

	ctx, cancel := context.WithCancel(context.Background())
	if shared != nil {
		go func() {
			if watchErr := cache.WatchTagBumps(ctx, local, shared); watchErr != nil && ctx.Err() == nil {
				logger.Warn("cache tag-bump watcher stopped", "error", watchErr)
			}
		}()
	}

	var once sync.Once
	var closeErr error
	closeFn = func() error {
		once.Do(func() {
			cancel()
			if redisCloser != nil {
				closeErr = redisCloser.Close()
			}
		})
		return closeErr
	}

	return &cache.RuntimeConfig{
		DeployPrefix: cmp.Or(cache.DeployPrefixFromEnv(), "local"),
		Generation:   cache.GenerationFromEnv(),
		Store:        tiered,
		Logger:       logger,
	}, closeFn, nil
}
