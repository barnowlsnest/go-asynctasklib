package workerpool

import "time"

// snapshot is the O(1) load reading the scaler takes once per tick. It is the
// pure input to decide; all fields are plain values so decide has no
// dependency on goroutines, channels, or real time.
type snapshot struct {
	joined   int   // currently joined workers (JoinedCount)
	idle     int   // idle joined workers parked on claims (PendingClaims)
	backlog  int   // queued jobs not yet dispatched (len backlog)
	now      int64 // unix nanos at snapshot time
	lastUp   int64 // unix nanos of the last applied scale-up
	lastDown int64 // unix nanos of the last applied scale-down
}

// decision is the action decide returns for a tick.
type decision int

const (
	hold decision = iota // do nothing this tick
	up                   // add workers (see returned step)
	down                 // remove one worker
)

// decide applies pool-level scaling policy: bounds, cooldowns, idle headroom,
// and backlog pressure. It is pure and table-tested. Per-worker safety
// (busy-protection and idle period) is handled separately by pickVictim. The
// second return is the number of workers to add for an up decision, clamped to
// the remaining headroom below MaxSize; it is 0 for hold and down.
func decide(s snapshot, cfg AutoScaleConfig) (action decision, workers int) {
	switch {
	case s.shouldScaleUp(cfg):
		return up, s.scaleUpStep(cfg)
	case s.shouldScaleDown(cfg):
		return down, 0
	default:
		return hold, 0
	}
}

// shouldScaleUp reports whether load justifies adding workers: there is room
// below MaxSize, work is queued with too little idle headroom to absorb it, and
// the scale-up cooldown has elapsed.
func (s snapshot) shouldScaleUp(cfg AutoScaleConfig) bool {
	belowCeiling := s.joined < cfg.MaxSize
	underPressure := s.backlog > 0 && s.idle <= cfg.IdleHeadroom
	cooldownElapsed := s.now-s.lastUp >= cfg.ScaleUpCooldown.Nanoseconds()
	return belowCeiling && underPressure && cooldownElapsed
}

// shouldScaleDown reports whether the pool is over-provisioned: above MinSize,
// nothing queued, more idle workers than the headroom margin, and the
// scale-down cooldown has elapsed.
func (s snapshot) shouldScaleDown(cfg AutoScaleConfig) bool {
	aboveFloor := s.joined > cfg.MinSize
	overProvisioned := s.backlog == 0 && s.idle > cfg.IdleHeadroom
	cooldownElapsed := s.now-s.lastDown >= cfg.ScaleDownCooldown.Nanoseconds()
	return aboveFloor && overProvisioned && cooldownElapsed
}

// scaleUpStep is the number of workers to add on a scale-up, clamped to the
// remaining room below MaxSize.
func (s snapshot) scaleUpStep(cfg AutoScaleConfig) int {
	step := cfg.ScaleUpStep
	if room := cfg.MaxSize - s.joined; step > room {
		step = room
	}
	return step
}

// idleCandidate is the narrow view pickVictim needs over a worker. The concrete
// *worker[T] satisfies it via its IsRunning and LastActiveAt methods.
type idleCandidate interface {
	IsRunning() bool
	LastActiveAt() int64
}

// pickVictim returns the index of the longest-idle removable candidate, or
// (-1, false) if none qualify. A candidate is removable only when it is not
// running a job (busy-worker protection) and has been idle for at least
// idlePeriod. It is pure and table-tested with test candidates.
func pickVictim(candidates []idleCandidate, now int64, idlePeriod time.Duration) (int, bool) {
	best := -1
	for i, candidate := range candidates {
		if candidate.IsRunning() {
			continue
		}
		if now-candidate.LastActiveAt() < idlePeriod.Nanoseconds() {
			continue
		}
		if best == -1 || candidate.LastActiveAt() < candidates[best].LastActiveAt() {
			best = i
		}
	}
	return best, best != -1
}

// joinOneIdle subscribes the first not-yet-joined worker. It returns
// ErrMaxPoolSize when no spare worker exists, which in normal operation cannot
// happen (the up-decision gates joined < MaxSize and MaxSize workers are
// pre-created); surfacing it makes that invariant violation visible rather than
// silent. Only the scaler goroutine calls this.
func (pool *WorkerPool[T]) joinOneIdle() error {
	pool.mu.Lock()
	var target *worker[T]
	for _, candidate := range pool.workers {
		if _, joined := pool.joinedIDs[candidate.ID()]; !joined {
			target = candidate
			break
		}
	}
	pool.mu.Unlock()

	if target == nil {
		return ErrMaxPoolSize
	}

	if err := target.join(pool.ctx(), pool.availableWorkers); err != nil {
		return err
	}

	pool.mu.Lock()
	pool.joinedIDs[target.ID()] = struct{}{}
	pool.mu.Unlock()
	return nil
}

// runScaler is the autoscaler goroutine. It snapshots load each tick, asks
// decide for an action, and applies it via join/leave. It owns lastUp/lastDown
// (single goroutine, no synchronization) and exits on pool context cancel or
// shutdown, closing scalerDone so close can safely reclaim workers.
func (pool *WorkerPool[T]) runScaler(tick <-chan time.Time, now func() int64) {
	defer close(pool.scalerDone)

	cfg := pool.cfg.AutoScale
	var lastUp, lastDown int64

	for {
		select {
		case <-pool.ctx().Done():
			return
		case <-pool.shutdown:
			return
		case <-tick:
			snap := snapshot{
				joined:   pool.JoinedCount(),
				idle:     pool.availableWorkers.PendingClaims(),
				backlog:  len(pool.backlog),
				now:      now(),
				lastUp:   lastUp,
				lastDown: lastDown,
			}

			switch action, step := decide(snap, cfg); action {
			case up:
				var added int
				for range step {
					if err := pool.joinOneIdle(); err != nil {
						// ctx/shutdown failures resolve on the next select;
						// other failures already fired SubscribeFailed. Stop
						// the batch and do not advance the cooldown.
						break
					}
					added++
				}
				if added > 0 {
					lastUp = snap.now
				}
			case down:
				// Implemented in Task 5; no-op until then.
			case hold:
			}
		}
	}
}
