package ratelimit

import (
	"sync"
	"time"
)

// quotaResetTime returns the next window boundary for a quota metric.
// Daily quota resets at UTC midnight; monthly quota resets at the first day of
// the next UTC month. Fixed calendar boundaries give a consistent, monotonic
// window across all instances (modulo clock skew).
func quotaResetTime(now time.Time, metric Metric) time.Time {
	now = now.UTC()
	if metric == MetricMonthlyQuota {
		return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
}

// quotaTTL returns the seconds until the next window boundary (min 1).
func quotaTTL(now time.Time, metric Metric) int64 {
	ttl := int64(time.Until(quotaResetTime(now, metric)).Seconds())
	if ttl < 1 {
		ttl = 1
	}
	return ttl
}

// quotaLimit returns the configured quota limit for a quota metric, or 0 when
// the metric is not a quota metric or the limit is unset (unlimited).
func quotaLimit(levelCfg *LevelConfig, metric Metric) int64 {
	if levelCfg == nil {
		return 0
	}
	switch metric {
	case MetricDailyQuota:
		return levelCfg.DailyQuota
	case MetricMonthlyQuota:
		return levelCfg.MonthlyQuota
	default:
		return 0
	}
}

// isQuotaMetric reports whether metric is a fixed-window quota metric.
func isQuotaMetric(metric Metric) bool {
	return metric == MetricDailyQuota || metric == MetricMonthlyQuota
}

// QuotaWindow tracks cumulative token consumption over a fixed calendar window
// (daily or monthly) with a hard cap. It backs local-mode quota metrics.
//
// It uses a mutex (not atomics) because the counter has a rollover reset at
// window boundaries — see RateBucket/SlidingWindow which make the same choice
// for stateful windows. The critical section is a single add, checked once per
// request, so the mutex cost is negligible.
type QuotaWindow struct {
	mu     sync.Mutex
	used   int64
	metric Metric
	reset  time.Time
}

func newQuotaWindow(now time.Time, metric Metric) *QuotaWindow {
	return &QuotaWindow{
		metric: metric,
		reset:  quotaResetTime(now, metric),
	}
}

// rollover resets the counter if the current window has elapsed.
// Caller must hold q.mu.
func (q *QuotaWindow) rollover(now time.Time) {
	if !now.Before(q.reset) {
		q.used = 0
		q.reset = quotaResetTime(now, q.metric)
	}
}

// add consumes delta units if doing so stays within limit (0 = unlimited).
// Returns false and consumes nothing when it would exceed the limit.
func (q *QuotaWindow) add(now time.Time, delta, limit int64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rollover(now)
	if limit > 0 && q.used+delta > limit {
		return false
	}
	q.used += delta
	return true
}

// adjust corrects pre-deducted usage by a delta (positive or negative),
// flooring at zero.
func (q *QuotaWindow) adjust(now time.Time, delta int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rollover(now)
	q.used += delta
	if q.used < 0 {
		q.used = 0
	}
}

// remaining returns the remaining quota, or -1 when unlimited.
func (q *QuotaWindow) remaining(now time.Time, limit int64) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rollover(now)
	if limit <= 0 {
		return -1
	}
	remaining := limit - q.used
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}
