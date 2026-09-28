package ratelimit

import (
	"context"
	"time"
)

// Dimension represents a rate limiting dimension.
type Dimension string

const (
	DimensionGlobal Dimension = "global"
	DimensionTenant Dimension = "tenant"
	DimensionModel  Dimension = "model"
	DimensionAPIKey Dimension = "api_key"
)

// Metric represents different rate limiting metrics.
// LLM 网关需要四件套：RPM + TPM(input) + TPM(output) + Concurrency
// 再加上总量配额（日/月），即 GW-09 的预算上限。
type Metric string

const (
	MetricRPM          Metric = "rpm"           // 请求频率
	MetricTPMInput     Metric = "tpm:input"     // 输入 token
	MetricTPMOutput    Metric = "tpm:output"    // 输出 token
	MetricConcurrency  Metric = "concurrency"   // 并发数
	MetricDailyQuota   Metric = "quota:daily"   // 日配额（token 总量）
	MetricMonthlyQuota Metric = "quota:monthly" // 月配额（token 总量）
)

// DimensionKeys holds the keys for all rate limiting dimensions.
type DimensionKeys struct {
	Global string `json:"global"`  // typically "global"
	Tenant string `json:"tenant"`  // e.g., "tenant:123"
	Model  string `json:"model"`   // e.g., "model:openai:gpt-4"
	APIKey string `json:"api_key"` // e.g., "key:abc123"
}

// LimitResult represents the result of a rate limit check.
type LimitResult struct {
	Allowed    bool          `json:"allowed"`
	Dimension  Dimension     `json:"dimension,omitempty"` // which dimension blocked (if not allowed)
	Metric     Metric        `json:"metric,omitempty"`    // which metric exceeded
	RetryAfter time.Duration `json:"retry_after,omitempty"`
	Limit      int64         `json:"limit"`     // limit value (for response header)
	Remaining  int64         `json:"remaining"` // remaining quota
}

// RateLimiter is the unified interface for all rate limiting modes.
// 使用门面模式，底层支持多种实现（Local/Redis/Hybrid/Sharded）
type RateLimiter interface {
	// Check checks if a request is allowed.
	// Returns: result with allowed status, retry-after, remaining quota
	Check(ctx context.Context, keys DimensionKeys, metric Metric, cost int64) (*LimitResult, error)

	// Release releases concurrency quota (call after request completes).
	Release(ctx context.Context, keys DimensionKeys) error

	// Correct corrects pre-deducted quota with actual usage.
	// Called after streaming response completes.
	Correct(ctx context.Context, keys DimensionKeys, metric Metric, preDeducted, actual int64) error

	// GetAvailable returns available quota for a dimension/metric.
	GetAvailable(ctx context.Context, keys DimensionKeys, metric Metric) (int64, error)

	// Reset clears rate limit data (for testing or admin operations).
	Reset(ctx context.Context, keys DimensionKeys) error

	// Close cleans up resources.
	Close() error
}
