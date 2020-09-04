package obfuscate

import (
	"fmt"
	"time"

	"github.com/DataDog/datadog-agent/pkg/trace/config"
	"github.com/DataDog/datadog-agent/pkg/trace/metrics"
	"github.com/dgraph-io/ristretto"
)

// queryCache is a wrapper on top of *ristretto.Cache which additionally
// sends metrics (hits and misses) every 10 seconds.
type queryCache struct {
	*ristretto.Cache

	close chan struct{}
	off   bool
}

// Close gracefully closes the cache.
func (c *queryCache) Close() {
	if c.off {
		return
	}
	c.close <- struct{}{}
	<-c.close
}

// Get wraps (*ristretto.Cache).Get with the ability for it to be a no-op.
func (c *queryCache) Get(key interface{}) (interface{}, bool) {
	if c.off {
		return nil, false
	}
	return c.Cache.Get(key)
}

// Set wraps (*ristretto.Cache).Set with the ability for it to be a no-op.
func (c *queryCache) Set(key, value interface{}, cost int64) bool {
	if c.off {
		return false
	}
	return c.Cache.Set(key, value, cost)
}

func (c *queryCache) statsLoop() {
	defer func() { c.close <- struct{}{} }()

	tick := time.NewTicker(10 * time.Second)
	mx := c.Cache.Metrics
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			metrics.Gauge("datadog.trace_agent.ofuscation.sql_cache.hits", float64(mx.Hits()), nil, 1)
			metrics.Gauge("datadog.trace_agent.ofuscation.sql_cache.misses", float64(mx.Misses()), nil, 1)
		case <-c.close:
			c.Cache.Close()
			return
		}
	}
}

// newQueryCache returns a new queryCache.
func newQueryCache() *queryCache {
	cfg := &ristretto.Config{
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
	}
	rcache, err := ristretto.NewCache(cfg)
	if err != nil {
		panic(fmt.Errorf("Error starting obfuscator query cache: %v", err))
	}
	c := queryCache{
		Cache: rcache,
		close: make(chan struct{}),
		off:   !config.HasFeature("sql_cache"),
	}
	if c.off {
		rcache.Close()
	} else {
		go c.statsLoop()
	}
	return &c
}
