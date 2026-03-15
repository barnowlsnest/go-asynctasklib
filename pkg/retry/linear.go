package retry

import "time"

// Linear implements Strategy with linearly increasing delay between retries.
type Linear struct {
	cfg config
}

// NewLinear creates a Linear retry strategy.
func NewLinear(opts ...OptionFunc) *Linear {
	return &Linear{cfg: applyConfig(opts)}
}

// Delay returns baseDelay * (attempt + 1), capped at maxDelay.
func (l *Linear) Delay(attempt int) time.Duration {
	d := capDelay(l.cfg.baseDelay*time.Duration(attempt+1), l.cfg.maxDelay)
	if l.cfg.jitter {
		d = applyJitter(d)
	}
	return d
}
