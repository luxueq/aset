package router

import (
	"errors"
	"math/rand"
	"strings"
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

// RouteRule maps a model to an ordered provider pool and a routing strategy.
// A rule matches a request whose model string equals the rule's Model,
// either in full ("openai/gpt-4") or as the bare model segment ("gpt-4").
type RouteRule struct {
	Model     string
	Providers []string
	Strategy  string
}

// Config holds router configuration.
type Config struct {
	DefaultStrategy string
	Routes          []RouteRule
}

// ErrNoHealthyProvider is returned when no healthy provider is available.
var ErrNoHealthyProvider = errors.New("no healthy provider available")

// Router selects providers based on configured strategies.
type Router struct {
	strategies      map[string]Strategy
	defaultStrategy string
	routes          []RouteRule
	mu              sync.RWMutex
	counter         int
	providers       []*ProviderStatus
	byName          map[string]*ProviderStatus
}

// New creates a new Router with all strategies registered.
func New(cfg Config) *Router {
	defaultStrategy := cfg.DefaultStrategy
	if defaultStrategy == "" {
		defaultStrategy = "roundrobin"
	}

	r := &Router{
		strategies:      make(map[string]Strategy),
		defaultStrategy: defaultStrategy,
		routes:          cfg.Routes,
		byName:          make(map[string]*ProviderStatus),
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
	r.byName = make(map[string]*ProviderStatus, len(providers))
	for _, p := range providers {
		r.byName[p.Name] = p
	}
}

// Select picks a provider using the specified strategy (or default).
func (r *Router) Select(strategyName string, req *provider.CanonicalRequest) (*ProviderStatus, error) {
	r.mu.RLock()
	providers := r.providers
	if strategyName == "" {
		strategyName = r.defaultStrategy
	}
	r.mu.RUnlock()

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

// Candidates returns an ordered list of providers eligible to serve a
// request for the given model. The first entry is the strategy's primary
// choice; the remaining entries are the failover order (stable pool order).
//
// Resolution order:
//  1. A matching route rule (full model match, then bare model match)
//     defines the provider pool and strategy.
//  2. An explicit "provider/model" request pins that single provider.
//  3. A bare model routes across all registered providers with the
//     default strategy.
func (r *Router) Candidates(model string) []*ProviderStatus {
	r.mu.RLock()
	pool := r.resolvePool(model)
	strategyName := r.strategyFor(model)
	r.mu.RUnlock()

	healthy := filterHealthy(pool)
	if len(healthy) == 0 {
		return nil
	}

	strategy, ok := r.strategies[strategyName]
	if !ok {
		strategy = r.strategies[r.defaultStrategy]
	}

	first, err := strategy.Select(healthy, &provider.CanonicalRequest{Model: model})
	if err != nil || first == nil {
		return healthy
	}

	candidates := make([]*ProviderStatus, 0, len(healthy))
	candidates = append(candidates, first)
	for _, p := range healthy {
		if p != first {
			candidates = append(candidates, p)
		}
	}
	return candidates
}

// resolvePool returns the ordered provider pool for a model.
// Caller must hold r.mu.
func (r *Router) resolvePool(model string) []*ProviderStatus {
	if rule := r.matchRoute(model); rule != nil {
		pool := make([]*ProviderStatus, 0, len(rule.Providers))
		for _, name := range rule.Providers {
			if p, ok := r.byName[name]; ok {
				pool = append(pool, p)
			}
		}
		return pool
	}

	// Explicit "provider/model" pins the named provider.
	if provName, _, ok := splitProviderModel(model); ok {
		if p, exists := r.byName[provName]; exists {
			return []*ProviderStatus{p}
		}
		return nil
	}

	// Bare model: all providers in registration order.
	return append([]*ProviderStatus(nil), r.providers...)
}

// matchRoute finds the first route rule matching the model (full string
// first, then the bare model segment). Caller must hold r.mu.
func (r *Router) matchRoute(model string) *RouteRule {
	bare := ""
	if _, rest, ok := splitProviderModel(model); ok {
		bare = rest
	}
	for i := range r.routes {
		if r.routes[i].Model == model {
			return &r.routes[i]
		}
		if bare != "" && r.routes[i].Model == bare {
			return &r.routes[i]
		}
	}
	return nil
}

// strategyFor returns the strategy name for a model. Caller must hold r.mu.
func (r *Router) strategyFor(model string) string {
	if rule := r.matchRoute(model); rule != nil && rule.Strategy != "" {
		return rule.Strategy
	}
	return r.defaultStrategy
}

// splitProviderModel splits "provider/model" into its parts.
func splitProviderModel(model string) (prov, bare string, ok bool) {
	if i := strings.Index(model, "/"); i >= 0 {
		return model[:i], model[i+1:], true
	}
	return "", "", false
}

// Status returns a copy of the provider's current runtime status.
func (r *Router) Status(name string) (ProviderStatus, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.byName[name]
	if !ok {
		return ProviderStatus{}, false
	}
	return *p, true
}

// RecordSuccess records a successful request to a provider.
func (r *Router) RecordSuccess(name string, latency time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.byName[name]
	if !ok {
		return
	}
	p.TotalCount++
	p.FailStreak = 0
	// Exponential moving average for latency
	if p.AvgLatency == 0 {
		p.AvgLatency = latency
	} else {
		p.AvgLatency = time.Duration(float64(p.AvgLatency)*0.8 + float64(latency)*0.2)
	}
}

// RecordFailure records a failed request to a provider.
func (r *Router) RecordFailure(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.byName[name]
	if !ok {
		return
	}
	p.TotalCount++
	p.ErrorCount++
	p.FailStreak++
	if p.TotalCount > 0 {
		p.ErrorRate = float64(p.ErrorCount) / float64(p.TotalCount)
	}
	// Unhealthy when the passive error rate exceeds 50%.
	if p.ErrorRate > 0.5 {
		p.Healthy = false
	}
}

// MarkHealthy marks a provider as healthy (used by health check).
func (r *Router) MarkHealthy(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.byName[name]
	if !ok {
		return
	}
	p.Healthy = true
	p.FailStreak = 0
	p.LastCheck = time.Now()
}

// MarkUnhealthy marks a provider as unhealthy.
func (r *Router) MarkUnhealthy(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.byName[name]
	if !ok {
		return
	}
	p.Healthy = false
	p.LastCheck = time.Now()
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
