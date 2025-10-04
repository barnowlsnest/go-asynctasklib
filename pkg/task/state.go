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
		onCreated  func(string, time.Time)
		onStarted  func(string, time.Time)
		onDone     func(string, time.Time)
		onTaskFn   func(string, time.Time)
		onFailed   func(string, time.Time, error)
		onPending  func(string, time.Time, int)
		onCanceled func(string, time.Time)
	}

	StateHookOpt func(sh *StateHooks) *StateHooks
)

func NewStateHooks(opts ...StateHookOpt) *StateHooks {
	hooks := &StateHooks{
		onCreated:  func(_ string, _ time.Time) {},
		onStarted:  func(_ string, _ time.Time) {},
		onDone:     func(_ string, _ time.Time) {},
		onTaskFn:   func(_ string, _ time.Time) {},
		onFailed:   func(_ string, _ time.Time, _ error) {},
		onPending:  func(_ string, _ time.Time, _ int) {},
		onCanceled: func(_ string, _ time.Time) {},
	}
	for _, h := range opts {
		hooks = h(hooks)
	}
	return hooks
}

func WhenCreated(fn func(string, time.Time)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onCreated = func(id string, when time.Time) {
			defer catchPanic()
			fn(id, when)
		}
		return sh
	}
}

func WhenStarted(fn func(string, time.Time)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onStarted = func(id string, when time.Time) {
			defer catchPanic()
			fn(id, when)
		}
		return sh
	}
}

func WhenDone(fn func(string, time.Time)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onDone = func(id string, when time.Time) {
			defer catchPanic()
			fn(id, when)
		}
		return sh
	}
}

func FromTaskFn(fn func(string, time.Time)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onTaskFn = func(id string, when time.Time) {
			defer catchPanic()
			fn(id, when)
		}
		return sh
	}
}

func WhenFailed(fn func(string, time.Time, error)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onFailed = func(id string, when time.Time, err error) {
			defer catchPanic()
			fn(id, when, err)
		}
		return sh
	}
}

func WhenPending(fn func(string, time.Time, int)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onPending = func(id string, when time.Time, attempt int) {
			defer catchPanic()
			fn(id, when, attempt)
		}
		return sh
	}
}

func WhenCanceled(fn func(string, time.Time)) StateHookOpt {
	return func(sh *StateHooks) *StateHooks {
		sh.onCanceled = func(id string, when time.Time) {
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
