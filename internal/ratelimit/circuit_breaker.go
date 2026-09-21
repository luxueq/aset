package ratelimit

import (
	"sync"
	"time"
)

// CircuitBreakerState represents the state of the circuit breaker.
type CircuitBreakerState int

const (
	CircuitClosed CircuitBreakerState = iota
	CircuitOpen
	CircuitHalfOpen
)

func (s CircuitBreakerState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreaker is a circuit breaker for a downstream dependency (e.g. Redis
// in hybrid mode).
//
// State machine:
//
//	Closed --(N consecutive failures)--> Open --(timeout elapsed)--> HalfOpen
//	HalfOpen --(all probes succeed)--> Closed
//	HalfOpen --(any probe fails)--> Open
//
// The half-open state is intentionally conservative: instead of a single probe
// deciding recovery, it admits up to halfOpenMaxProbes concurrent probes and
// only closes once all of them succeed. A single failure immediately re-trips
// the circuit, avoiding the "one success → full flood" weakness of a
// single-probe design.
//
// Note: this tracks success/failure only. It does not implement progressive
// slow-start or latency/slow-call thresholds, which require a metrics signal
// the RateLimiter interface does not currently expose.
type CircuitBreaker struct {
	mu                sync.Mutex
	errorThreshold    int
	timeout           time.Duration
	halfOpenMaxProbes int

	failures int
	state    CircuitBreakerState
	openedAt time.Time

	// Half-open probe accounting.
	halfOpenInflight  int
	halfOpenSuccesses int
}

// NewCircuitBreaker creates a circuit breaker.
// - errorThreshold: consecutive failures in the closed state that trip it open
//   (default 5).
// - halfOpenMaxProbes: max concurrent probes admitted in half-open before the
//   breaker closes again (default 3).
// - timeout: how long the breaker stays open before allowing a half-open probe
//   (default 30s).
// Zero/negative values fall back to the defaults.
func NewCircuitBreaker(errorThreshold, halfOpenMaxProbes int, timeout time.Duration) *CircuitBreaker {
	if errorThreshold <= 0 {
		errorThreshold = 5
	}
	if halfOpenMaxProbes <= 0 {
		halfOpenMaxProbes = 3
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &CircuitBreaker{
		errorThreshold:    errorThreshold,
		timeout:           timeout,
		halfOpenMaxProbes: halfOpenMaxProbes,
		state:             CircuitClosed,
	}
}

// Allow reports whether a call may proceed.
//
// When closed, calls are always allowed. When open, calls are rejected until
// the timeout elapses, at which point the breaker transitions to half-open.
// When half-open, only up to halfOpenMaxProbes probes are admitted at a time;
// any excess is rejected.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		if time.Since(cb.openedAt) >= cb.timeout {
			cb.state = CircuitHalfOpen
			cb.resetHalfOpenLocked()
			return cb.admitProbeLocked()
		}
		return false
	case CircuitHalfOpen:
		return cb.admitProbeLocked()
	default:
		return false
	}
}

// admitProbeLocked admits one probe if a half-open slot is available.
func (cb *CircuitBreaker) admitProbeLocked() bool {
	if cb.halfOpenInflight >= cb.halfOpenMaxProbes {
		return false
	}
	cb.halfOpenInflight++
	return true
}

// RecordSuccess marks a successful call.
// In half-open, all probes must succeed before the breaker closes.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		cb.failures = 0
	case CircuitHalfOpen:
		if cb.halfOpenInflight > 0 {
			cb.halfOpenInflight--
			cb.halfOpenSuccesses++
		}
		if cb.halfOpenSuccesses >= cb.halfOpenMaxProbes {
			cb.state = CircuitClosed
			cb.failures = 0
			cb.resetHalfOpenLocked()
		}
	}
}

// RecordFailure marks a failed call.
// In half-open, a single failure immediately re-trips the circuit open.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		cb.failures++
		if cb.failures >= cb.errorThreshold {
			cb.state = CircuitOpen
			cb.openedAt = time.Now()
		}
	case CircuitHalfOpen:
		cb.state = CircuitOpen
		cb.openedAt = time.Now()
		cb.resetHalfOpenLocked()
	}
}

// resetHalfOpenLocked clears half-open probe accounting.
func (cb *CircuitBreaker) resetHalfOpenLocked() {
	cb.halfOpenInflight = 0
	cb.halfOpenSuccesses = 0
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() CircuitBreakerState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}
