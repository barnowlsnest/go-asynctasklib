package retry

import "time"

type constant struct {
	cfg config
}

func Constant(opts ...OptionFunc) Strategy {
	return &constant{cfg: applyConfig(opts)}
}

func (c *constant) Delay(_ int) time.Duration {
	d := capDelay(c.cfg.baseDelay, c.cfg.maxDelay)
	if c.cfg.jitter {
		d = applyJitter(d)
	}
	return d
}
