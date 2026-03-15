package retry

import (
	"math"
	"time"
)

// ExponentialBackoff implements Strategy with exponentially increasing delay between retries.
type ExponentialBackoff struct {
	cfg config
}

// NewExponentialBackoff creates an ExponentialBackoff retry strategy.
func NewExponentialBackoff(opts ...OptionFunc) *ExponentialBackoff {
	return &ExponentialBackoff{cfg: applyConfig(opts)}
}

// Delay returns baseDelay * 2^attempt, capped at maxDelay.
func (e *ExponentialBackoff) Delay(attempt int) time.Duration {
	if e.cfg.baseDelay <= 0 || attempt < 0 {
		return 0
	}

	multiplier := math.Pow(2, float64(attempt))
	delay := float64(e.cfg.baseDelay) * multiplier

	d := e.cfg.maxDelay
	if delay < float64(e.cfg.maxDelay) {
		d = time.Duration(delay)
	}
	if e.cfg.jitter {
		d = applyJitter(d)
	}
	return d
}
