package cache

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	cacheHitsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aigateway_cache_hits_total",
			Help: "Total cache hits, labeled by cache_type (exact|semantic).",
		},
		[]string{"cache_type"},
	)
	cacheMissesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aigateway_cache_misses_total",
			Help: "Total cache misses, labeled by cache_type (exact|semantic).",
		},
		[]string{"cache_type"},
	)
	cacheLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "aigateway_cache_latency_seconds",
			Help:    "Cache lookup latency in seconds, labeled by cache_type.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"cache_type"},
	)
)

func init() {
	prometheus.MustRegister(cacheHitsTotal, cacheMissesTotal, cacheLatency)
}

func recordCacheHit(cacheType string) {
	cacheHitsTotal.WithLabelValues(cacheType).Inc()
}

func recordCacheMiss(cacheType string) {
	cacheMissesTotal.WithLabelValues(cacheType).Inc()
}
