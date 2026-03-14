package retry

import "time"

type linear struct {
	cfg config
}

func Linear(opts ...OptionFunc) Strategy {
	return &linear{cfg: applyConfig(opts)}
}

func (l *linear) Delay(attempt int) time.Duration {
	d := capDelay(l.cfg.baseDelay*time.Duration(attempt+1), l.cfg.maxDelay)
	if l.cfg.jitter {
		d = applyJitter(d)
	}
	return d
}
