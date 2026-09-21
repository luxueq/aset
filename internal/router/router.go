package router

import (
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/luxueq/aigateway/internal/provider"
)

// ProviderStatus holds runtime status for a provider.
type ProviderStatus struct {
	Name       string
	Healthy    bool
	AvgLatency time.Duration
	ErrorRate  float64
	Weight     int
	CostPer1K  float64
	ErrorCount int
	TotalCount int
	LastCheck  time.Time
	FailStreak int
}

// Strategy defines the interface for routing strategies.
type Strategy interface {
	Name() string
	Select(providers []*ProviderStatus, req *provider.CanonicalRequest) (*ProviderStatus, error)
}

// Router selects providers based on configured strategies.
type Router struct {
	strategies      map[string]Strategy
	defaultStrategy string
	mu              sync.RWMutex
	counter         int
	providers       []*ProviderStatus
}

// Config holds router configuration.
type Config struct {
	DefaultStrategy string `yaml:"default_strategy"`
}

// ErrNoHealthyProvider is returned when no healthy provider is available.
var ErrNoHealthyProvider = errors.New("no healthy provider available")

// New creates a new Router with all strategies registered.
func New(cfg Config) *Router {
	defaultStrategy := cfg.DefaultStrategy
	if defaultStrategy == "" {
		defaultStrategy = "roundrobin"
	}

	r := &Router{
		strategies:      make(map[string]Strategy),
		defaultStrategy: defaultStrategy,
	}

	r.strategies["roundrobin"] = &RoundRobinStrategy{router: r}
	r.strategies["latency"] = &LatencyStrategy{}
	r.strategies["cost"] = &CostStrategy{}
	r.strategies["weighted"] = &WeightedStrategy{}

	return r
}

// SetProviders sets the list of providers to route to.
func (r *Router) SetProviders(providers []*ProviderStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = providers
}

// Select picks a provider using the specified strategy (or default).
func (r *Router) Select(strategyName string, req *provider.CanonicalRequest) (*ProviderStatus, error) {
	r.mu.RLock()
	providers := r.providers
	r.mu.RUnlock()

	if strategyName == "" {
		strategyName = r.defaultStrategy
	}

	strategy, ok := r.strategies[strategyName]
	if !ok {
		strategy = r.strategies[r.defaultStrategy]
	}

	// Filter to healthy providers only
	healthy := filterHealthy(providers)
	if len(healthy) == 0 {
		return nil, ErrNoHealthyProvider
	}

	return strategy.Select(healthy, req)
}

// RecordSuccess records a successful request to a provider.
func (r *Router) RecordSuccess(name string, latency time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, p := range r.providers {
		if p.Name == name {
			p.TotalCount++
			p.FailStreak = 0
			// Exponential moving average for latency
			if p.AvgLatency == 0 {
				p.AvgLatency = latency
			} else {
				p.AvgLatency = time.Duration(float64(p.AvgLatency)*0.8 + float64(latency)*0.2)
			}
			break
		}
	}
}

// RecordFailure records a failed request to a provider.
func (r *Router) RecordFailure(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, p := range r.providers {
		if p.Name == name {
			p.TotalCount++
			p.ErrorCount++
			p.FailStreak++
			if p.TotalCount > 0 {
				p.ErrorRate = float64(p.ErrorCount) / float64(p.TotalCount)
			}
			// Mark unhealthy after 3 consecutive failures or >50% error rate
			if p.FailStreak >= 3 || p.ErrorRate > 0.5 {
				p.Healthy = false
			}
			break
		}
	}
}

// MarkHealthy marks a provider as healthy (used by health check).
func (r *Router) MarkHealthy(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, p := range r.providers {
		if p.Name == name {
			p.Healthy = true
			p.FailStreak = 0
			p.LastCheck = time.Now()
			break
		}
	}
}

// MarkUnhealthy marks a provider as unhealthy.
func (r *Router) MarkUnhealthy(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, p := range r.providers {
		if p.Name == name {
			p.Healthy = false
			p.LastCheck = time.Now()
			break
		}
	}
}

func filterHealthy(providers []*ProviderStatus) []*ProviderStatus {
	var healthy []*ProviderStatus
	for _, p := range providers {
		if p.Healthy {
			healthy = append(healthy, p)
		}
	}
	return healthy
}

// --- Strategy Implementations ---

// RoundRobinStrategy cycles through providers sequentially.
type RoundRobinStrategy struct {
	router *Router
}

func (s *RoundRobinStrategy) Name() string { return "roundrobin" }

func (s *RoundRobinStrategy) Select(providers []*ProviderStatus, req *provider.CanonicalRequest) (*ProviderStatus, error) {
	if len(providers) == 0 {
		return nil, ErrNoHealthyProvider
	}
	s.router.mu.Lock()
	idx := s.router.counter % len(providers)
	s.router.counter++
	s.router.mu.Unlock()
	return providers[idx], nil
}

// LatencyStrategy selects the provider with the lowest average latency.
type LatencyStrategy struct{}

func (s *LatencyStrategy) Name() string { return "latency" }

func (s *LatencyStrategy) Select(providers []*ProviderStatus, req *provider.CanonicalRequest) (*ProviderStatus, error) {
	if len(providers) == 0 {
		return nil, ErrNoHealthyProvider
	}
	best := providers[0]
	for _, p := range providers[1:] {
		if p.AvgLatency < best.AvgLatency {
			best = p
		}
	}
	return best, nil
}

// CostStrategy selects the cheapest provider.
type CostStrategy struct{}

func (s *CostStrategy) Name() string { return "cost" }

func (s *CostStrategy) Select(providers []*ProviderStatus, req *provider.CanonicalRequest) (*ProviderStatus, error) {
	if len(providers) == 0 {
		return nil, ErrNoHealthyProvider
	}
	best := providers[0]
	for _, p := range providers[1:] {
		if p.CostPer1K < best.CostPer1K {
			best = p
		}
	}
	return best, nil
}

// WeightedStrategy selects providers based on their weight.
type WeightedStrategy struct{}

func (s *WeightedStrategy) Name() string { return "weighted" }

func (s *WeightedStrategy) Select(providers []*ProviderStatus, req *provider.CanonicalRequest) (*ProviderStatus, error) {
	if len(providers) == 0 {
		return nil, ErrNoHealthyProvider
	}

	totalWeight := 0
	for _, p := range providers {
		w := p.Weight
		if w <= 0 {
			w = 1
		}
		totalWeight += w
	}

	r := rand.Intn(totalWeight)
	for _, p := range providers {
		w := p.Weight
		if w <= 0 {
			w = 1
		}
		r -= w
		if r < 0 {
			return p, nil
		}
	}

	return providers[0], nil
}
