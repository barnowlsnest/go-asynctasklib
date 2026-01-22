package task

import (
	"fmt"
	"time"
)

type (
	OptionFunc func(*Definition)

	// Builder is a struct that holds configuration options for constructing a Definition through a series of OptionFunc.
	Builder struct {
		opts []OptionFunc
	}
)

// NewBuilder creates a new Builder instance with the provided options.
func NewBuilder(opts ...OptionFunc) *Builder {
	var b Builder
	b.opts = make([]OptionFunc, 0, len(opts))
	for _, opt := range opts {
		if opts != nil {
			b.opts = append(b.opts, opt)
		}
	}
	return &b
}

func WithID(id uint64) OptionFunc {
	return func(d *Definition) {
		d.ID = id
	}
}

func WithName(name string) OptionFunc {
	return func(d *Definition) {
		d.Name = name
	}
}

func WithTaskFn(fn RunFunc) OptionFunc {
	return func(d *Definition) {
		d.TaskFn = fn
	}
}

func WithMaxRetries(retries int) OptionFunc {
	return func(d *Definition) {
		d.MaxRetries = retries
	}
}

func WithDelay(delay time.Duration) OptionFunc {
	return func(d *Definition) {
		d.Delay = delay
	}
}

func WithTimeout(duration time.Duration) OptionFunc {
	return func(d *Definition) {
		d.MaxDuration = duration
	}
}

func WithHooks(hooks *StateHooks) OptionFunc {
	return func(d *Definition) {
		d.Hooks = hooks
	}
}

// Build finalizes the configuration of a Definition using the provided options and validates all required fields.
func (b *Builder) Build() (*Definition, error) {
	var d Definition
	for _, opt := range b.opts {
		opt(&d)
	}
	switch {
	case d.ID == 0:
		return nil, ErrIDNotSet
	case d.TaskFn == nil:
		return nil, ErrTaskFnNotSet
	case d.MaxRetries <= 0:
		return nil, ErrMaxRetriesNotSet
	}

	if d.Name == "" {
		d.Name = fmt.Sprintf("task-%d", d.ID)
	}

	return &d, nil
}
