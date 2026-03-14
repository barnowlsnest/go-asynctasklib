package retry

import (
	"math"
	"time"
)

type exponentialBackoff struct {
	cfg config
}

func ExponentialBackoff(opts ...OptionFunc) Strategy {
	return &exponentialBackoff{cfg: applyConfig(opts)}
}

func (e *exponentialBackoff) Delay(attempt int) time.Duration {
	if e.cfg.baseDelay <= 0 {
		return 0
	}
	if attempt < 0 || attempt >= 63 {
		return e.finalDelay()
	}

	shift := time.Duration(1) << uint(attempt) //nolint:gosec // overflow protected by attempt < 63 guard
	if shift <= 0 || int64(shift) > math.MaxInt64/int64(e.cfg.baseDelay) {
		return e.finalDelay()
	}

	d := capDelay(e.cfg.baseDelay*shift, e.cfg.maxDelay)
	if e.cfg.jitter {
		d = applyJitter(d)
	}
	return d
}

func (e *exponentialBackoff) finalDelay() time.Duration {
	d := e.cfg.maxDelay
	if e.cfg.jitter {
		d = applyJitter(d)
	}
	return d
}
