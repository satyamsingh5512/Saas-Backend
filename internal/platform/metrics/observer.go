package metrics

// CacheObserver is the subset of Registry the cache layer reports to.
// It exists so internal/platform/cache can record hit/miss counters without
// importing the full registry type or changing constructor signatures:
// routes.Setup assigns the live Registry to DefaultObserver once, and the
// cache package calls ObserveHit/ObserveMiss, which are nil-safe no-ops
// until then (and in unit tests that never set an observer).
type CacheObserver interface {
	RecordCacheHit()
	RecordCacheMiss()
}

// DefaultObserver receives cache hit/miss reports. Assigned once at startup.
var DefaultObserver CacheObserver

// ObserveHit records one cache hit. Safe to call with no observer set.
func ObserveHit() {
	if DefaultObserver != nil {
		DefaultObserver.RecordCacheHit()
	}
}

// ObserveMiss records one cache miss (including fail-open fallbacks).
// Safe to call with no observer set.
func ObserveMiss() {
	if DefaultObserver != nil {
		DefaultObserver.RecordCacheMiss()
	}
}
