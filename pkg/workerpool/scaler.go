package workerpool

import (
	"context"
	"time"
)

type scalerPool[T any] interface {
	poolCtx() context.Context
	activeWorkers() int
	pendingClaims() int
	joinOne() error
	leaveLRU(timeout time.Duration) error
}

type scaler[T any] struct {
	pool   scalerPool[T]
	cfg    *AutoScaleConfig
	stopCh chan struct{}
	doneCh chan struct{}

	lastScaleAt time.Time
	idleSince   time.Time // zero when not currently idle
}

func newScaler[T any](p scalerPool[T], cfg *AutoScaleConfig) *scaler[T] {
	return &scaler[T]{
		pool:   p,
		cfg:    cfg,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

func (s *scaler[T]) start() {
	go s.loop()
}

func (s *scaler[T]) stop() {
	close(s.stopCh)
	<-s.doneCh
}

func (s *scaler[T]) loop() {
	defer close(s.doneCh)

	ticker := time.NewTicker(s.cfg.ScaleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-s.pool.poolCtx().Done():
			return
		case now := <-ticker.C:
			s.evaluate(now)
		}
	}
}

func (s *scaler[T]) cooledDown(now time.Time) bool {
	return s.lastScaleAt.IsZero() || now.Sub(s.lastScaleAt) >= s.cfg.Cooldown
}

func (s *scaler[T]) evaluate(now time.Time) {
	active := s.pool.activeWorkers()
	if active == 0 {
		return
	}

	idle := s.pool.pendingClaims()
	util := float64(active-idle) / float64(active)

	if s.tryScaleUp(now, active, util) {
		return
	}

	s.tryScaleDown(now, active, idle)
}

func (s *scaler[T]) tryScaleUp(now time.Time, active int, util float64) bool {
	if util < s.cfg.ScaleUpThreshold || active >= s.cfg.MaxSize || !s.cooledDown(now) {
		return false
	}

	s.idleSince = time.Time{}
	for range s.cfg.StepUp {
		if s.pool.activeWorkers() >= s.cfg.MaxSize {
			break
		}
		if err := s.pool.joinOne(); err != nil {
			break
		}
	}
	s.lastScaleAt = now

	return true
}

func (s *scaler[T]) tryScaleDown(now time.Time, active, idle int) {
	if idle < active || active <= s.cfg.MinSize {
		if idle < active {
			s.idleSince = time.Time{}
		}
		return
	}

	if s.idleSince.IsZero() {
		s.idleSince = now
	}

	if now.Sub(s.idleSince) < s.cfg.ScaleDownIdle || !s.cooledDown(now) {
		return
	}

	for range s.cfg.StepDown {
		if s.pool.activeWorkers() <= s.cfg.MinSize {
			break
		}
		if err := s.pool.leaveLRU(defaultLeaveTimeout); err != nil {
			break
		}
	}
	s.lastScaleAt = now
}
