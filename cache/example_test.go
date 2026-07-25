package cache_test

import (
	"cmp"
	"context"
	"log"

	"github.com/Origens-Dev/gobeyond/cache"
	"github.com/Origens-Dev/gobeyond/cache/memstore"
	"github.com/Origens-Dev/gobeyond/cache/redisstore"
	"github.com/Origens-Dev/gobeyond/runtime"
)

// Example_wiring shows the whole cache assembly a deployment needs: a local
// tier always, the shared tier only when the platform injected an endpoint,
// and the tag-bump subscription that drops local copies early. Nothing here
// changes shape when the shared tier is absent.
func Example_wiring() {
	local := memstore.New(memstore.Options{})

	var shared cache.Store
	if store, configured, err := redisstore.FromEnv(redisstore.Options{}); err != nil {
		log.Fatal(err)
	} else if configured {
		shared = store
	}

	store := cache.Tiered(local, shared, cache.TieredOptions{})
	go func() {
		if err := cache.WatchTagBumps(context.Background(), local, shared); err != nil {
			log.Print(err)
		}
	}()

	_, err := runtime.New(runtime.Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Cache: &cache.RuntimeConfig{
			DeployPrefix: cmp.Or(cache.DeployPrefixFromEnv(), "local"),
			Store:        store,
		},
	})
	if err != nil {
		log.Print(err)
	}
	// Output:
}
