package router

import (
	"errors"
)

// ErrAllProvidersFailed is returned when every failover attempt errored.
var ErrAllProvidersFailed = errors.New("all providers failed")

// FailoverConfig controls automatic failover behavior (D-10).
type FailoverConfig struct {
	// Enabled turns cross-provider failover on. When false, a provider
	// error is returned to the client as-is.
	Enabled bool `yaml:"enabled"`
	// MaxProviders is the maximum number of providers attempted per
	// request (default 3). Note: per-provider retries are a separate
	// concern handled by the retry layer.
	MaxProviders int `yaml:"max_providers"`
	// ReturnOnError inverts the behavior: when true, the first provider
	// error is returned to the client instead of failing over.
	ReturnOnError bool `yaml:"return_on_error"`
}

// DefaultFailoverConfig returns the default failover settings (D-10:
// auto failover, max 3 providers).
func DefaultFailoverConfig() FailoverConfig {
	return FailoverConfig{
		Enabled:      true,
		MaxProviders: 3,
		ReturnOnError: false,
	}
}

// normalized returns the config with defaults applied.
func (c FailoverConfig) normalized() FailoverConfig {
	cfg := c
	if cfg.MaxProviders <= 0 {
		cfg.MaxProviders = 3
	}
	return cfg
}

// Attempt records a single failover attempt outcome.
type Attempt struct {
	Provider string
	Err      error
}

// RouteWithFailover walks the candidate list for a model, invoking try
// for each provider until one succeeds.
//
// Semantics:
//   - With failover disabled or ReturnOnError set, the first provider's
//     result (success or error) is returned immediately.
//   - Otherwise, up to MaxProviders candidates are tried in order; every
//     failed attempt is reported to the router (RecordFailure /
//     RecordSuccess) for passive health tracking; exhaustion returns
//     ErrAllProvidersFailed with the per-provider attempt log.
//
// The candidate list comes from r.Candidates(model): strategy primary
// first, then remaining healthy providers in stable pool order.
func (r *Router) RouteWithFailover(
	model string,
	cfg FailoverConfig,
	try func(candidate *ProviderStatus) error,
) (selected *ProviderStatus, attempts []Attempt, err error) {
	cfg = cfg.normalized()

	candidates := r.Candidates(model)
	if len(candidates) == 0 {
		return nil, nil, ErrNoHealthyProvider
	}

	if !cfg.Enabled || cfg.ReturnOnError {
		c := candidates[0]
		if err := try(c); err != nil {
			r.RecordFailure(c.Name)
			return nil, []Attempt{{Provider: c.Name, Err: err}}, err
		}
		r.RecordSuccess(c.Name, 0)
		return c, nil, nil
	}

	if len(candidates) > cfg.MaxProviders {
		candidates = candidates[:cfg.MaxProviders]
	}

	attempts = make([]Attempt, 0, len(candidates))
	for _, c := range candidates {
		attemptErr := try(c)
		if attemptErr == nil {
			r.RecordSuccess(c.Name, 0)
			return c, attempts, nil
		}
		r.RecordFailure(c.Name)
		attempts = append(attempts, Attempt{Provider: c.Name, Err: attemptErr})
	}

	return nil, attempts, ErrAllProvidersFailed
}
