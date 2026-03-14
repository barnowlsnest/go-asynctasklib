package retry

import (
	"crypto/rand"
	"math/big"
	"time"
)

const (
	defaultBaseDelay = 100 * time.Millisecond
	defaultMaxDelay  = 30 * time.Second
)

type (
	Strategy interface {
		Delay(attempt int) time.Duration
	}

	OptionFunc func(*config)

	config struct {
		baseDelay time.Duration
		maxDelay  time.Duration
		jitter    bool
	}
)

func defaultConfig() config {
	return config{
		baseDelay: defaultBaseDelay,
		maxDelay:  defaultMaxDelay,
		jitter:    false,
	}
}

func WithBaseDelay(d time.Duration) OptionFunc {
	return func(c *config) {
		c.baseDelay = d
	}
}

func WithMaxDelay(d time.Duration) OptionFunc {
	return func(c *config) {
		c.maxDelay = d
	}
}

func WithJitter(jitter bool) OptionFunc {
	return func(c *config) {
		c.jitter = jitter
	}
}

func capDelay(delay, maxDur time.Duration) time.Duration {
	if delay > maxDur {
		return maxDur
	}
	return delay
}

func applyJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(delay)+1))
	return time.Duration(n.Int64())
}

func applyConfig(opts []OptionFunc) config {
	cfg := defaultConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}
