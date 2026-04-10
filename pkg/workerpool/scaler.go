package workerpool

import "time"

// runScaler owns the AutoScale mode scaling loop. It wakes on its ticker or
// on an explicit scale request from the dispatch path and adjusts the set of
// Joined workers based on backlog pressure and idle time.
func (pool *WorkerPool[T]) runScaler() {
	ticker := time.NewTicker(pool.cfg.ScaleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-pool.ctx().Done():
			return
		case <-ticker.C:
			pool.scaleCheck()
		case <-pool.scaleReq:
			pool.scaleCheck()
		}
	}
}

// requestScale nudges the scaler to re-evaluate immediately. The buffered
// channel gives us coalescing: repeated requests collapse into a single wake.
func (pool *WorkerPool[T]) requestScale() {
	select {
	case pool.scaleReq <- struct{}{}:
	default:
	}
}

func (pool *WorkerPool[T]) scaleCheck() {
	toJoin, toLeave := pool.planScale()

	for _, w := range toJoin {
		if err := w.Join(pool.ctx(), pool.availableWorkers); err != nil {
			// Roll the optimistic join back so the next tick can try again.
			pool.mu.Lock()
			delete(pool.joinedIDs, w.ID())
			pool.mu.Unlock()
		}
	}

	for _, w := range toLeave {
		// Best-effort leave; if it times out the scaler will see it on
		// the next tick and retry.
		_ = w.Leave(pool.availableWorkers, pool.cfg.SubmitTimeout)
	}
}

// planScale decides, under the pool mutex, which workers to Join and which
// to Leave on this scaling tick. The actual Join / Leave calls happen
// outside the lock so we never block scaleCheck on worker shutdown.
func (pool *WorkerPool[T]) planScale() (toJoin, toLeave []*Worker[T]) {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	joined := len(pool.joinedIDs)
	pending := len(pool.backlog)

	// Scale up: backlog is building and we still have room.
	if pending > 0 && joined < pool.cfg.MaxSize {
		room := pool.cfg.MaxSize - joined
		for _, w := range pool.workers {
			if room == 0 {
				break
			}
			if _, ok := pool.joinedIDs[w.ID()]; ok {
				continue
			}
			pool.joinedIDs[w.ID()] = struct{}{}
			toJoin = append(toJoin, w)
			joined++
			room--
		}
		return toJoin, nil
	}

	// Scale down: leave workers that are idle beyond the threshold, but
	// keep at least MinSize joined.
	if joined > pool.cfg.MinSize {
		now := time.Now().UnixNano()
		threshold := pool.cfg.IdleTimeout.Nanoseconds()
		for _, w := range pool.workers {
			if joined <= pool.cfg.MinSize {
				break
			}
			if _, ok := pool.joinedIDs[w.ID()]; !ok {
				continue
			}
			if w.IsRunning() {
				continue
			}
			if now-w.LastActiveAt() < threshold {
				continue
			}
			delete(pool.joinedIDs, w.ID())
			toLeave = append(toLeave, w)
			joined--
		}
	}

	return toJoin, toLeave
}
