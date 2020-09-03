package obfuscate

import (
	"fmt"
	"time"

	"github.com/dgraph-io/ristretto"
)

// cacheTippingPoint maps query length to minimum hit ratio needed by the
// cache in order for it to be more performant than the "no cache at all" scenario.
//
// e.g. 200 => 0.3 means that for 1000 obfuscations of a 200 character query, at
// least a 30% cache hit rate is needed for the algorithm to be efficient.
//
// The map is used to automatically disable the cache when it's becoming counterproductive
// to performance.
//
// The values are obtained using BenchmarkQueryCacheTippingPoint.
var cacheTippingPoint = map[int]float64{
	30:   0.5,
	200:  0.3,
	925:  0.3,
	1712: 0.2,
	4200: 0.1,
}

func newQueryCache() *ristretto.Cache {
	cache, err := ristretto.NewCache(&ristretto.Config{
		Metrics: true,
		// We know that both cache keys and values will have a maximum
		// length of 5K, so one entry (key + value) will be 10K maximum.
		// At worst case scenario, a 5M cache should fit at least 500 queries.
		MaxCost: 5 * 1024 * 1024,
		// An appromixation worst-case scenario when the cache is filled of small
		// queries averaged as being of length 19 (SELECT * FROM users), we would
		// be able to fit 263K of them into 5MB of cost.
		// We multiply the value by x10 as advised in the ristretto.Config documentation.
		NumCounters: 3 * 1000 * 1000,
		// 64 is the recommended default value
		BufferItems: 64,
	})
	if err != nil {
		panic(fmt.Errorf("Error starting obfuscator query cache: %v", err))
	}
	var stopped bool
	go func() {
		tick := time.NewTicker(10 * time.Second)
		mx := cache.Metrics
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				if stopped {
					return
				}
				if mx.Hits()+mx.Misses() < 1000 {
					// not enough data
					continue
				}
			}
		}
	}()
	return cache
}
