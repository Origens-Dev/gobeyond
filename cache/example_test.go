package cache_test

import (
	"log"

	"github.com/Origens-Dev/gobeyond/cache/openfromenv"
	"github.com/Origens-Dev/gobeyond/runtime"
)

// Example_wiring shows the supported cache assembly: OpenFromEnv builds a
// bounded local tier, attaches the shared Redis tier only when the platform
// injected an endpoint, and starts the tag-bump subscription that drops local
// copies early. Nothing here changes shape when the shared tier is absent.
func Example_wiring() {
	cacheConfig, closeCache, err := openfromenv.OpenFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	defer closeCache()

	_, err = runtime.New(runtime.Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Cache:        cacheConfig,
	})
	if err != nil {
		log.Print(err)
	}
	// Output:
}
