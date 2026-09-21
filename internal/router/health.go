package router

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// HealthMonitor performs active health checks against provider /models
// endpoints on a fixed interval. A provider is marked unhealthy after 3
// consecutive check failures (D-12) and healthy again after one success.
type HealthMonitor struct {
	router  *Router
	client  *http.Client
	urls    map[string]string // provider name -> check URL
	interval time.Duration

	mu     sync.Mutex
	streak map[string]int // consecutive check failures per provider
}

// NewHealthMonitor creates a HealthMonitor. checkURLs maps provider names
// to the endpoint probed for liveness (typically {base}/models).
func NewHealthMonitor(r *Router, checkURLs map[string]string, interval time.Duration) *HealthMonitor {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &HealthMonitor{
		router:   r,
		client:   &http.Client{Timeout: 5 * time.Second},
		urls:     checkURLs,
		interval: interval,
		streak:   make(map[string]int),
	}
}

// Start launches the periodic check loop until ctx is cancelled. It
// performs an immediate first round so status is fresh at startup.
func (m *HealthMonitor) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()

		m.CheckAll(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.CheckAll(ctx)
			}
		}
	}()
}

// CheckAll runs one active check round against every configured URL.
func (m *HealthMonitor) CheckAll(ctx context.Context) {
	var wg sync.WaitGroup
	for name, url := range m.urls {
		wg.Add(1)
		go func(name, url string) {
			defer wg.Done()
			m.Check(ctx, name, url)
		}(name, url)
	}
	wg.Wait()
}

// Check probes a single provider endpoint and updates router health
// state: unhealthy after 3 consecutive failures, healthy after a success.
// Status 200-299 counts as healthy; any other status or transport error
// counts as a failure.
func (m *HealthMonitor) Check(ctx context.Context, name, url string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		m.recordFailure(name)
		return
	}

	resp, err := m.client.Do(req)
	if err != nil {
		m.recordFailure(name)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		m.recordSuccess(name)
	} else {
		m.recordFailure(name)
	}
}

func (m *HealthMonitor) recordSuccess(name string) {
	m.mu.Lock()
	m.streak[name] = 0
	m.mu.Unlock()
	m.router.MarkHealthy(name)
}

func (m *HealthMonitor) recordFailure(name string) {
	m.mu.Lock()
	m.streak[name]++
	failures := m.streak[name]
	m.mu.Unlock()

	// 3 consecutive health check failures mark the provider unhealthy.
	if failures >= 3 {
		m.router.MarkUnhealthy(name)
	}
}

// Streak returns the current consecutive-failure count for a provider
// (exposed for tests and diagnostics).
func (m *HealthMonitor) Streak(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streak[name]
}
