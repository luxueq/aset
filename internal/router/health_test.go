package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHealthMonitor_MarksUnhealthyAfter3ConsecutiveFailures verifies the
// active health check criterion: 3 consecutive check failures mark the
// provider unhealthy and exclude it from routing.
func TestHealthMonitor_MarksUnhealthyAfter3ConsecutiveFailures(t *testing.T) {
	t.Parallel()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	r := New(Config{})
	r.SetProviders([]*ProviderStatus{
		{Name: "openai", Healthy: true},
		{Name: "anthropic", Healthy: true},
	})

	m := NewHealthMonitor(r, map[string]string{"openai": srv.URL}, time.Hour)
	ctx := context.Background()

	// Failures 1 and 2: still healthy (streak not yet 3).
	m.CheckAll(ctx)
	m.CheckAll(ctx)
	if st, _ := r.Status("openai"); !st.Healthy {
		t.Error("openai marked unhealthy after 2 consecutive failures, want 3-streak threshold")
	}

	// Failure 3: unhealthy.
	m.CheckAll(ctx)
	st, ok := r.Status("openai")
	if !ok {
		t.Fatal("Status(openai) not found")
	}
	if st.Healthy {
		t.Error("openai still healthy after 3 consecutive check failures, want unhealthy")
	}

	// Unhealthy provider is excluded from routing.
	for _, c := range r.Candidates("gpt-4") {
		if c.Name == "openai" {
			t.Error("unhealthy openai still returned as candidate")
		}
	}
}

// TestHealthMonitor_RecoversOnSuccess verifies one successful check resets
// the failure streak and marks the provider healthy again.
func TestHealthMonitor_RecoversOnSuccess(t *testing.T) {
	t.Parallel()

	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := New(Config{})
	r.SetProviders([]*ProviderStatus{{Name: "openai", Healthy: true}})

	m := NewHealthMonitor(r, map[string]string{"openai": srv.URL}, time.Hour)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		m.CheckAll(ctx)
	}
	if st, _ := r.Status("openai"); st.Healthy {
		t.Fatal("openai should be unhealthy after 3 failures")
	}

	fail = false
	m.CheckAll(ctx)
	st, _ := r.Status("openai")
	if !st.Healthy {
		t.Error("openai not healthy after successful check, want recovery")
	}
	if m.Streak("openai") != 0 {
		t.Errorf("failure streak after recovery = %d, want 0", m.Streak("openai"))
	}
}

// TestHealthMonitor_TransportErrorCountsAsFailure verifies a dead endpoint
// (connection refused) counts as a check failure.
func TestHealthMonitor_TransportErrorCountsAsFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // now unreachable

	r := New(Config{})
	r.SetProviders([]*ProviderStatus{{Name: "openai", Healthy: true}})

	m := NewHealthMonitor(r, map[string]string{"openai": url}, time.Hour)
	for i := 0; i < 3; i++ {
		m.CheckAll(context.Background())
	}

	if st, _ := r.Status("openai"); st.Healthy {
		t.Error("unreachable endpoint did not mark provider unhealthy after 3 checks")
	}
}

// TestHealthMonitor_StartRunsUntilCancel verifies the periodic loop
// respects context cancellation and performs an immediate first round.
func TestHealthMonitor_StartRunsUntilCancel(t *testing.T) {
	t.Parallel()

	var calls int
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 3 {
			close(done)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	r := New(Config{})
	r.SetProviders([]*ProviderStatus{{Name: "openai", Healthy: true}})

	m := NewHealthMonitor(r, map[string]string{"openai": srv.URL}, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.Start(ctx)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("health monitor did not perform 3 checks within 2s")
	}

	cancel()

	// No further checks after cancellation (give the loop a moment to exit).
	baseline := calls
	time.Sleep(50 * time.Millisecond)
	if calls > baseline+2 {
		t.Errorf("checks continued after cancellation: %d -> %d", baseline, calls)
	}
}

// TestRouter_WindowedErrorRate verifies passive monitoring: error rate is
// computed over the sliding window, a single transient failure does not
// mark a provider unhealthy, and > 50% over enough samples does.
func TestRouter_WindowedErrorRate(t *testing.T) {
	t.Parallel()

	r := newTestRouter()

	// One transient failure: healthy (1 sample < MinSamples, rate 100%).
	r.RecordFailure("openai")
	if st, _ := r.Status("openai"); !st.Healthy {
		t.Fatal("single transient failure marked openai unhealthy, want MinSamples guard")
	}

	// 2 failures + 1 success = 66.7% over 3 samples: unhealthy.
	r.RecordFailure("openai")
	r.RecordSuccess("openai", time.Millisecond)
	st, _ := r.Status("openai")
	if st.Healthy {
		t.Error("openai still healthy with 66.7% windowed error rate, want unhealthy")
	}

	// Recover: MarkHealthy resets health; subsequent successes drive the
	// windowed rate down.
	r.MarkHealthy("openai")
	r.RecordSuccess("openai", time.Millisecond)
	r.RecordSuccess("openai", time.Millisecond)
	// Window now: F,F,S,S,S = 40% — below threshold, and already healthy.
	st, _ = r.Status("openai")
	if !st.Healthy {
		t.Error("openai unhealthy with 40% windowed error rate, want healthy")
	}
}

// TestRouter_WindowExpiry verifies events older than the window stop
// counting toward the error rate.
func TestRouter_WindowExpiry(t *testing.T) {
	t.Parallel()

	// Tiny window so events expire immediately.
	r := New(Config{Window: time.Millisecond, MinSamples: 3})
	r.SetProviders([]*ProviderStatus{{Name: "openai", Healthy: true}})

	r.RecordFailure("openai")
	time.Sleep(5 * time.Millisecond)

	// New success: old failure expired, window holds only the success.
	r.RecordSuccess("openai", time.Millisecond)
	st, _ := r.Status("openai")
	if st.ErrorRate > 0 {
		t.Errorf("ErrorRate = %f, want 0 (old failures expired from window)", st.ErrorRate)
	}
}

// TestRouter_SmallWindowKeepsRecentTail verifies the bounded-tail guard:
// with a long window and many events, only the most recent 100 count.
func TestRouter_SmallWindowKeepsRecentTail(t *testing.T) {
	t.Parallel()

	r := New(Config{Window: time.Hour}) // nothing expires
	r.SetProviders([]*ProviderStatus{{Name: "openai", Healthy: true}})

	// 100 successes then 50 failures: recent 100 events are 50 S + 50 F.
	for i := 0; i < 100; i++ {
		r.RecordSuccess("openai", time.Millisecond)
	}
	for i := 0; i < 50; i++ {
		r.RecordFailure("openai")
	}

	st, _ := r.Status("openai")
	if got, want := st.ErrorRate, 0.5; got != want {
		t.Errorf("ErrorRate = %f, want %f (recent tail 50F/100)", got, want)
	}
	if !st.Healthy {
		t.Error("exactly 50% must not mark unhealthy (threshold is > 50%)")
	}
}
