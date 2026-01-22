package task

import (
	"time"
)

const (
	CREATED = iota
	PENDING
	STARTED
	DONE
	FAILED
	CANCELED
)

type (
	StateHooks struct {
		onCreated  func(uint64, time.Time)
		onStarted  func(uint64, time.Time)
		onDone     func(uint64, time.Time)
		onTaskFn   func(uint64, time.Time)
		onFailed   func(uint64, time.Time, error)
		onPending  func(uint64, time.Time, int)
		onCanceled func(uint64, time.Time)
	}

	StateHookOpt func(sh *StateHooks) *StateHooks
)

func newNoopHooks() *StateHooks {
	return &StateHooks{
		onCreated:  func(_ uint64, _ time.Time) {},
		onStarted:  func(_ uint64, _ time.Time) {},
		onDone:     func(_ uint64, _ time.Time) {},
		onTaskFn:   func(_ uint64, _ time.Time) {},
		onFailed:   func(_ uint64, _ time.Time, _ error) {},
		onPending:  func(_ uint64, _ time.Time, _ int) {},
		onCanceled: func(_ uint64, _ time.Time) {},
	}
}

func NewStateHooks(opts ...StateHookOpt) *StateHooks {
	hooks := newNoopHooks()
	for _, h := range opts {
		hooks = h(hooks)
	}
	return hooks
}

func WhenCreated(fn func(uint64, time.Time)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onCreated = func(id uint64, when time.Time) {
			defer catchPanic()
			fn(id, when)
		}
		return sh
	}
}

func WhenStarted(fn func(uint64, time.Time)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onStarted = func(id uint64, when time.Time) {
			defer catchPanic()
			fn(id, when)
		}
		return sh
	}
}

func WhenDone(fn func(uint64, time.Time)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onDone = func(id uint64, when time.Time) {
			defer catchPanic()
			fn(id, when)
		}
		return sh
	}
}

func FromTaskFn(fn func(uint64, time.Time)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onTaskFn = func(id uint64, when time.Time) {
			defer catchPanic()
			fn(id, when)
		}
		return sh
	}
}

func WhenFailed(fn func(uint64, time.Time, error)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onFailed = func(id uint64, when time.Time, err error) {
			defer catchPanic()
			fn(id, when, err)
		}
		return sh
	}
}

func WhenPending(fn func(uint64, time.Time, int)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onPending = func(id uint64, when time.Time, attempt int) {
			defer catchPanic()
			fn(id, when, attempt)
		}
		return sh
	}
}

func WhenCanceled(fn func(uint64, time.Time)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onCanceled = func(id uint64, when time.Time) {
			defer catchPanic()
			fn(id, when)
		}
		return sh
	}
}

func catchPanic() {
	// silently ignore panics in hooks
	_ = recover()
}
