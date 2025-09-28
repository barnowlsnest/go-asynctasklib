package task

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	
	"github.com/stretchr/testify/suite"
)

type TaskTestSuite struct {
	suite.Suite
	ctx context.Context
}

func (s *TaskTestSuite) SetupTest() {
	s.ctx = context.Background()
}

func TestTaskSuite(t *testing.T) {
	suite.Run(t, new(TaskTestSuite))
}

// Test data structures for parametrized tests
type definitionTestCase struct {
	name               string
	definition         Definition
	expectedTimeout    time.Duration
	expectedDelay      time.Duration
	expectedMaxRetries int
	shouldHaveCustomFn bool
	shouldReturnError  bool
	expectedDefaultErr error
}

type stateTestCase struct {
	name           string
	initialState   uint32
	expectedChecks map[string]bool
}

type executionTestCase struct {
	name          string
	taskFn        func(*Run) error
	expectedErr   error
	expectedState uint32
	shouldContain string
}

type timeoutTestCase struct {
	name          string
	timeout       time.Duration
	taskDuration  time.Duration
	expectTimeout bool
}

type cancellationTestCase struct {
	name           string
	setupCancel    func(*Task, context.Context) context.Context
	triggerCancel  func(*Task, context.CancelFunc)
	expectCanceled bool
}

type raceTestCase struct {
	name        string
	numWorkers  int
	operations  int
	description string
}

type retryTestCase struct {
	name          string
	maxRetries    int
	failuresCount int
	expectSuccess bool
	expectedError error
}

type delayTestCase struct {
	name        string
	delay       time.Duration
	expectDelay bool
	minDuration time.Duration
}

func (s *TaskTestSuite) TestNew() {
	testCases := []definitionTestCase{
		{
			name:               "default definition - no task function",
			definition:         Definition{ID: "test-default"},
			expectedTimeout:    defaultTimeout,
			expectedDelay:      0,
			expectedMaxRetries: 0,
			shouldHaveCustomFn: false,
			shouldReturnError:  true,
			expectedDefaultErr: ErrTaskFnNotSet,
		},
		{
			name: "custom timeout only - no task function",
			definition: Definition{
				ID:          "test-timeout",
				MaxDuration: time.Second * 5,
			},
			expectedTimeout:    time.Second * 5,
			expectedDelay:      0,
			expectedMaxRetries: 0,
			shouldHaveCustomFn: false,
			shouldReturnError:  true,
			expectedDefaultErr: ErrTaskFnNotSet,
		},
		{
			name: "custom function only",
			definition: Definition{
				ID:     "test-function",
				TaskFn: func(r *Run) error { return nil },
			},
			expectedTimeout:    defaultTimeout,
			expectedDelay:      0,
			expectedMaxRetries: 0,
			shouldHaveCustomFn: true,
			shouldReturnError:  false,
		},
		{
			name: "full definition",
			definition: Definition{
				ID:          "test-full",
				MaxDuration: time.Second * 10,
				TaskFn:      func(r *Run) error { return nil },
				Delay:       time.Millisecond * 100,
				MaxRetries:  3,
			},
			expectedTimeout:    time.Second * 10,
			expectedDelay:      time.Millisecond * 100,
			expectedMaxRetries: 3,
			shouldHaveCustomFn: true,
			shouldReturnError:  false,
		},
		{
			name: "zero timeout uses default",
			definition: Definition{
				ID:          "test-zero-timeout",
				MaxDuration: 0,
				TaskFn:      func(r *Run) error { return nil },
			},
			expectedTimeout:    defaultTimeout,
			expectedDelay:      0,
			expectedMaxRetries: 0,
			shouldHaveCustomFn: true,
			shouldReturnError:  false,
		},
		{
			name: "delay and retries without function",
			definition: Definition{
				ID:         "test-delay-retries",
				Delay:      time.Millisecond * 50,
				MaxRetries: 2,
			},
			expectedTimeout:    defaultTimeout,
			expectedDelay:      time.Millisecond * 50,
			expectedMaxRetries: 2,
			shouldHaveCustomFn: false,
			shouldReturnError:  true,
			expectedDefaultErr: ErrTaskFnNotSet,
		},
	}
	
	for _, tc := range testCases {
		s.Run(tc.name, func() {
			task := New(tc.definition)

			s.True(task.IsCreated())
			s.False(task.IsPending())
			s.False(task.IsStarted())
			s.False(task.IsDone())
			s.False(task.IsFailed())
			s.False(task.IsCanceled())
			s.False(task.IsInProgress())
			s.False(task.IsEnd())
			s.Equal(tc.expectedTimeout, task.timeout)
			s.Equal(tc.expectedDelay, task.delay)
			s.Equal(tc.expectedMaxRetries, task.maxRetries)
			s.Equal(tc.definition.ID, task.ID())
			s.Nil(task.Err())
			s.NotNil(task.fn)
			
			// Test default function behavior
			if !tc.shouldHaveCustomFn {
				err := task.Go(s.ctx)
				s.NoError(err)
				task.Await()
				if tc.shouldReturnError {
					s.True(task.IsFailed())
					s.Equal(tc.expectedDefaultErr, task.Err())
				}
			}
		})
	}
}

func (s *TaskTestSuite) TestTaskStates() {
	stateTestCases := []stateTestCase{
		{
			name:         "CREATED state",
			initialState: CREATED,
			expectedChecks: map[string]bool{
				"IsCreated":    true,
				"IsPending":    false,
				"IsStarted":    false,
				"IsDone":       false,
				"IsFailed":     false,
				"IsCanceled":   false,
				"IsInProgress": false,
				"IsEnd":        false,
			},
		},
		{
			name:         "PENDING state",
			initialState: PENDING,
			expectedChecks: map[string]bool{
				"IsCreated":    false,
				"IsPending":    true,
				"IsStarted":    false,
				"IsDone":       false,
				"IsFailed":     false,
				"IsCanceled":   false,
				"IsInProgress": true,
				"IsEnd":        false,
			},
		},
		{
			name:         "STARTED state",
			initialState: STARTED,
			expectedChecks: map[string]bool{
				"IsCreated":    false,
				"IsPending":    false,
				"IsStarted":    true,
				"IsDone":       false,
				"IsFailed":     false,
				"IsCanceled":   false,
				"IsInProgress": true,
				"IsEnd":        false,
			},
		},
		{
			name:         "DONE state",
			initialState: DONE,
			expectedChecks: map[string]bool{
				"IsCreated":    false,
				"IsPending":    false,
				"IsStarted":    false,
				"IsDone":       true,
				"IsFailed":     false,
				"IsCanceled":   false,
				"IsInProgress": false,
				"IsEnd":        true,
			},
		},
		{
			name:         "FAILED state",
			initialState: FAILED,
			expectedChecks: map[string]bool{
				"IsCreated":    false,
				"IsPending":    false,
				"IsStarted":    false,
				"IsDone":       false,
				"IsFailed":     true,
				"IsCanceled":   false,
				"IsInProgress": false,
				"IsEnd":        true,
			},
		},
		{
			name:         "CANCELED state",
			initialState: CANCELED,
			expectedChecks: map[string]bool{
				"IsCreated":    false,
				"IsPending":    false,
				"IsStarted":    false,
				"IsDone":       false,
				"IsFailed":     false,
				"IsCanceled":   true,
				"IsInProgress": false,
				"IsEnd":        true,
			},
		},
	}
	
	for _, tc := range stateTestCases {
		s.Run(tc.name, func() {
			task := New(Definition{TaskFn: func(r *Run) error { return nil }})
			task.state.Store(tc.initialState)
			
			s.Equal(tc.expectedChecks["IsCreated"], task.IsCreated())
			s.Equal(tc.expectedChecks["IsPending"], task.IsPending())
			s.Equal(tc.expectedChecks["IsStarted"], task.IsStarted())
			s.Equal(tc.expectedChecks["IsDone"], task.IsDone())
			s.Equal(tc.expectedChecks["IsFailed"], task.IsFailed())
			s.Equal(tc.expectedChecks["IsCanceled"], task.IsCanceled())
			s.Equal(tc.expectedChecks["IsInProgress"], task.IsInProgress())
			s.Equal(tc.expectedChecks["IsEnd"], task.IsEnd())
		})
	}
}

func (s *TaskTestSuite) TestTaskGo() {
	s.Run("returns error for nil context", func() {
		task := New(Definition{TaskFn: func(r *Run) error { return nil }})
		err := task.Go(nil)
		s.Equal(ErrNilCtx, err)
	})
	
	s.Run("returns error when task is already in progress", func() {
		barrier := make(chan struct{})
		task := New(Definition{
			TaskFn: func(r *Run) error {
				<-barrier
				return nil
			},
		})
		
		err1 := task.Go(s.ctx)
		s.NoError(err1)
		
		err2 := task.Go(s.ctx)
		s.Equal(ErrTaskInProgress, err2)
		
		close(barrier)
		task.Await()
	})
	
	inProgressStates := []uint32{PENDING, STARTED}
	for _, state := range inProgressStates {
		s.Run(fmt.Sprintf("returns error when task is in %d state", state), func() {
			task := New(Definition{TaskFn: func(r *Run) error { return nil }})
			task.state.Store(state)
			
			err := task.Go(s.ctx)
			s.Equal(ErrTaskInProgress, err)
		})
	}
	
	s.Run("task transitions to PENDING before STARTED", func() {
		executed := make(chan struct{})
		task := New(Definition{
			TaskFn: func(r *Run) error {
				close(executed)
				return nil
			},
		})
		
		err := task.Go(s.ctx)
		s.NoError(err)
		
		// Task should briefly be in PENDING state, then STARTED
		<-executed
		task.Await()
		s.True(task.IsDone())
	})
}

func (s *TaskTestSuite) TestTaskDelay() {
	delayTestCases := []delayTestCase{
		{
			name:        "no delay",
			delay:       0,
			expectDelay: false,
			minDuration: 0,
		},
		{
			name:        "short delay",
			delay:       time.Millisecond * 50,
			expectDelay: true,
			minDuration: time.Millisecond * 40,
		},
		{
			name:        "long delay",
			delay:       time.Millisecond * 200,
			expectDelay: true,
			minDuration: time.Millisecond * 180,
		},
	}
	
	for _, tc := range delayTestCases {
		s.Run(tc.name, func() {
			executed := make(chan struct{})
			task := New(Definition{
				Delay: tc.delay,
				TaskFn: func(r *Run) error {
					close(executed)
					return nil
				},
			})
			
			start := time.Now()
			err := task.Go(s.ctx)
			s.NoError(err)
			<-executed
			duration := time.Since(start)
			
			if tc.expectDelay {
				s.GreaterOrEqual(duration, tc.minDuration)
			} else {
				s.Less(duration, time.Millisecond*10)
			}
			
			task.Await()
			s.True(task.IsDone())
		})
	}
}

func (s *TaskTestSuite) TestTaskGoRetry() {
	retryTestCases := []retryTestCase{
		{
			name:          "success on first try",
			maxRetries:    3,
			failuresCount: 0,
			expectSuccess: true,
			expectedError: nil,
		},
		{
			name:          "success after retries",
			maxRetries:    3,
			failuresCount: 2,
			expectSuccess: true,
			expectedError: nil,
		},
		{
			name:          "failure after all retries",
			maxRetries:    2,
			failuresCount: 3,
			expectSuccess: false,
			expectedError: ErrMaxRetriesExceeded,
		},
		{
			name:          "zero retries means no retry",
			maxRetries:    0,
			failuresCount: 1,
			expectSuccess: false,
			expectedError: ErrMaxRetriesNotSet,
		},
		{
			name:          "single retry success",
			maxRetries:    1,
			failuresCount: 0,
			expectSuccess: true,
			expectedError: nil,
		},
	}
	
	for _, tc := range retryTestCases {
		s.Run(tc.name, func() {
			attempts := 0
			task := New(Definition{
				MaxRetries: tc.maxRetries,
				TaskFn: func(r *Run) error {
					attempts++
					if attempts <= tc.failuresCount {
						return errors.New("simulated failure")
					}
					return nil
				},
			})
			
			err := task.GoRetry(s.ctx)
			
			if tc.expectSuccess {
				s.NoError(err)
				s.True(task.IsDone())
				s.Equal(tc.failuresCount+1, attempts)
			} else {
				s.Equal(tc.expectedError, err)
				s.True(task.IsFailed())
				s.Equal(tc.maxRetries, attempts)
			}
		})
	}
}

func (s *TaskTestSuite) TestTaskGoRetryWithContextCancellation() {
	s.Run("retry stops on context cancellation", func() {
		attempts := 0
		task := New(Definition{
			MaxRetries: 5,
			Delay:      time.Millisecond * 10,
			TaskFn: func(r *Run) error {
				attempts++
				return errors.New("always fail")
			},
		})
		
		ctx, cancel := context.WithCancel(s.ctx)
		go func() {
			time.Sleep(time.Millisecond * 50)
			cancel()
		}()
		
		err := task.GoRetry(ctx)
		s.Error(err)
		s.Contains(err.Error(), "task was cancelled")
		s.True(task.IsCanceled())
		s.Less(attempts, 6) // Should not complete all retries
	})
}

func (s *TaskTestSuite) TestTaskExecution() {
	executionTestCases := []executionTestCase{
		{
			name: "successful execution",
			taskFn: func(r *Run) error {
				return nil
			},
			expectedErr:   nil,
			expectedState: DONE,
		},
		{
			name: "task function returns error",
			taskFn: func(r *Run) error {
				return errors.New("task failed")
			},
			expectedErr:   errors.New("task failed"),
			expectedState: FAILED,
		},
		{
			name: "task function panics",
			taskFn: func(r *Run) error {
				panic("something went wrong")
			},
			expectedErr:   nil,
			expectedState: FAILED,
			shouldContain: "task panicked: something went wrong",
		},
		{
			name: "task function with context cancellation",
			taskFn: func(r *Run) error {
				<-r.Context().Done()
				return r.Context().Err()
			},
			expectedErr:   nil,
			expectedState: CANCELED,
			shouldContain: "task was cancelled",
		},
		{
			name: "task function returns nil explicitly",
			taskFn: func(r *Run) error {
				time.Sleep(time.Millisecond * 10)
				return nil
			},
			expectedErr:   nil,
			expectedState: DONE,
		},
	}
	
	for _, tc := range executionTestCases {
		s.Run(tc.name, func() {
			task := New(Definition{TaskFn: tc.taskFn})
			
			if tc.name == "task function with context cancellation" {
				ctx, cancel := context.WithCancel(s.ctx)
				err := task.Go(ctx)
				s.NoError(err)
				time.Sleep(time.Millisecond * 10)
				cancel()
			} else {
				err := task.Go(s.ctx)
				s.NoError(err)
			}
			
			task.Await()
			
			s.Equal(tc.expectedState, task.state.Load())
			
			if tc.expectedErr != nil {
				s.Equal(tc.expectedErr, task.Err())
			} else if tc.shouldContain != "" {
				s.Contains(task.Err().Error(), tc.shouldContain)
			} else {
				s.Nil(task.Err())
			}
		})
	}
}

func (s *TaskTestSuite) TestTaskTimeout() {
	timeoutTestCases := []timeoutTestCase{
		{
			name:          "task completes before timeout",
			timeout:       time.Second,
			taskDuration:  time.Millisecond * 10,
			expectTimeout: false,
		},
		{
			name:          "task times out",
			timeout:       time.Millisecond * 20,
			taskDuration:  time.Millisecond * 100,
			expectTimeout: true,
		},
		{
			name:          "task completes exactly at timeout boundary",
			timeout:       time.Millisecond * 50,
			taskDuration:  time.Millisecond * 40,
			expectTimeout: false,
		},
		{
			name:          "very short timeout",
			timeout:       time.Microsecond * 100,
			taskDuration:  time.Millisecond * 10,
			expectTimeout: true,
		},
		{
			name:          "zero duration task with short timeout",
			timeout:       time.Millisecond * 10,
			taskDuration:  0,
			expectTimeout: false,
		},
	}

	// Test case for timeout vs cancellation bug fix
	s.Run("cancellation during timeout should set CANCELED not FAILED", func() {
		task := New(Definition{
			MaxDuration: time.Millisecond * 100,
			TaskFn: func(r *Run) error {
				// Task will run longer than timeout
				time.Sleep(time.Millisecond * 200)
				return nil
			},
		})

		ctx, cancel := context.WithCancel(s.ctx)
		err := task.Go(ctx)
		s.NoError(err)

		// Cancel the context during timeout period
		go func() {
			time.Sleep(time.Millisecond * 50) // Cancel before timeout
			cancel()
		}()

		task.Await()

		// Should be CANCELED, not FAILED
		s.True(task.IsCanceled(), "Task should be CANCELED when context is cancelled during timeout")
		s.False(task.IsFailed(), "Task should not be FAILED when context is cancelled")
		s.Contains(task.Err().Error(), "task was cancelled")
	})
	
	for _, tc := range timeoutTestCases {
		s.Run(tc.name, func() {
			task := New(Definition{
				MaxDuration: tc.timeout,
				TaskFn: func(r *Run) error {
					if tc.taskDuration > 0 {
						time.Sleep(tc.taskDuration)
					}
					return nil
				},
			})
			
			err := task.Go(s.ctx)
			s.NoError(err)
			task.Await()
			
			if tc.expectTimeout {
				s.True(task.IsFailed())
				s.Equal(ErrTaskTimeout, task.Err())
			} else {
				s.True(task.IsDone())
				s.Nil(task.Err())
			}
		})
	}
}

func (s *TaskTestSuite) TestTaskCancellation() {
	cancellationTestCases := []cancellationTestCase{
		{
			name: "cancel via Cancel method",
			setupCancel: func(task *Task, ctx context.Context) context.Context {
				return ctx
			},
			triggerCancel: func(task *Task, cancel context.CancelFunc) {
				task.Cancel()
			},
			expectCanceled: true,
		},
		{
			name: "cancel via context cancellation",
			setupCancel: func(task *Task, ctx context.Context) context.Context {
				newCtx, cancel := context.WithCancel(ctx)
				_ = cancel // We'll use the passed cancel function instead
				return newCtx
			},
			triggerCancel: func(task *Task, cancel context.CancelFunc) {
				cancel()
			},
			expectCanceled: true,
		},
		{
			name: "cancel via context timeout",
			setupCancel: func(task *Task, ctx context.Context) context.Context {
				newCtx, cancel := context.WithTimeout(ctx, time.Millisecond*20)
				_ = cancel
				return newCtx
			},
			triggerCancel: func(task *Task, cancel context.CancelFunc) {
				// Timeout will trigger automatically
			},
			expectCanceled: true,
		},
		{
			name: "cancel task that hasn't started",
			setupCancel: func(task *Task, ctx context.Context) context.Context {
				return ctx
			},
			triggerCancel: func(task *Task, cancel context.CancelFunc) {
				task.Cancel() // Cancel before starting
			},
			expectCanceled: true,
		},
		{
			name: "cancel during delay period",
			setupCancel: func(task *Task, ctx context.Context) context.Context {
				return ctx
			},
			triggerCancel: func(task *Task, cancel context.CancelFunc) {
				// Cancel shortly after starting (during delay)
				time.Sleep(time.Millisecond * 25)
				task.Cancel()
			},
			expectCanceled: true,
		},
	}
	
	for _, tc := range cancellationTestCases {
		s.Run(tc.name, func() {
			delay := time.Duration(0)
			if tc.name == "cancel during delay period" {
				delay = time.Millisecond * 100
			}
			
			started := make(chan struct{})
			task := New(Definition{
				Delay: delay,
				TaskFn: func(r *Run) error {
					close(started)
					<-r.Context().Done()
					return r.Context().Err()
				},
			})
			
			ctx, cancel := context.WithCancel(s.ctx)
			defer cancel()
			
			if tc.name == "cancel task that hasn't started" {
				tc.triggerCancel(task, cancel)
				s.True(task.IsCanceled())
				return
			}
			
			execCtx := tc.setupCancel(task, ctx)
			err := task.Go(execCtx)
			s.NoError(err)
			
			if tc.name == "cancel during delay period" {
				go tc.triggerCancel(task, cancel)
			} else if tc.name != "cancel via context timeout" {
				if delay == 0 {
					<-started
				}
				tc.triggerCancel(task, cancel)
			}
			
			task.Await()
			
			if tc.expectCanceled {
				s.True(task.IsCanceled())
				s.Contains(task.Err().Error(), "task was cancelled")
			}
		})
	}
}

func (s *TaskTestSuite) TestRunInterface() {
	s.Run("Run provides correct ID", func() {
		var runID string
		task := New(Definition{
			TaskFn: func(r *Run) error {
				runID = r.ID()
				return nil
			},
		})
		
		err := task.Go(s.ctx)
		s.NoError(err)
		task.Await()
		
		s.Equal(task.ID(), runID)
	})
	
	s.Run("Run provides correct context", func() {
		testKey := "test-key"
		testValue := "test-value"
		ctx := context.WithValue(s.ctx, testKey, testValue)
		
		var runCtx context.Context
		task := New(Definition{
			TaskFn: func(r *Run) error {
				runCtx = r.Context()
				return nil
			},
		})
		
		err := task.Go(ctx)
		s.NoError(err)
		task.Await()
		
		s.Equal(testValue, runCtx.Value(testKey))
	})
	
	s.Run("Run Cancel method works", func() {
		task := New(Definition{
			TaskFn: func(r *Run) error {
				r.Cancel()
				<-r.Context().Done()
				return r.Context().Err()
			},
		})
		
		err := task.Go(s.ctx)
		s.NoError(err)
		task.Await()
		
		s.True(task.IsCanceled())
	})
	
	s.Run("Run Cancel is context.CancelFunc", func() {
		var cancelFunc context.CancelFunc
		task := New(Definition{
			TaskFn: func(r *Run) error {
				cancelFunc = r.Cancel
				// Don't cancel to test the type
				return nil
			},
		})
		
		err := task.Go(s.ctx)
		s.NoError(err)
		task.Await()
		
		s.NotNil(cancelFunc)
		s.True(task.IsDone())
	})
	
	s.Run("Run methods return consistent values", func() {
		var (
			id1, id2         string
			ctx1, ctx2       context.Context
			cancel1, cancel2 context.CancelFunc
		)
		
		task := New(Definition{
			TaskFn: func(r *Run) error {
				id1 = r.ID()
				ctx1 = r.Context()
				cancel1 = r.Cancel
				time.Sleep(time.Millisecond * 10)
				id2 = r.ID()
				ctx2 = r.Context()
				cancel2 = r.Cancel
				return nil
			},
		})
		
		err := task.Go(s.ctx)
		s.NoError(err)
		task.Await()
		
		s.Equal(id1, id2)
		s.Equal(ctx1, ctx2)
		s.Equal(fmt.Sprintf("%p", cancel1), fmt.Sprintf("%p", cancel2))
	})
}

func (s *TaskTestSuite) TestRaceConditions() {
	raceTestCases := []raceTestCase{
		{
			name:        "concurrent state reads",
			numWorkers:  50,
			operations:  100,
			description: "Multiple goroutines reading task state concurrently",
		},
		{
			name:        "concurrent task creation",
			numWorkers:  20,
			operations:  50,
			description: "Multiple goroutines creating tasks concurrently",
		},
		{
			name:        "concurrent execution and cancellation",
			numWorkers:  10,
			operations:  20,
			description: "Tasks being executed and cancelled concurrently",
		},
		{
			name:        "concurrent retry operations",
			numWorkers:  10,
			operations:  20,
			description: "Multiple retry operations running concurrently",
		},
	}
	
	for _, tc := range raceTestCases {
		s.Run(tc.name, func() {
			var wg sync.WaitGroup
			var counter int64
			
			switch tc.name {
			case "concurrent state reads":
				task := New(Definition{
					TaskFn: func(r *Run) error {
						time.Sleep(time.Millisecond * 50)
						return nil
					},
				})
				
				err := task.Go(s.ctx)
				s.NoError(err)
				
				for i := 0; i < tc.numWorkers; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for j := 0; j < tc.operations; j++ {
							_ = task.IsCreated()
							_ = task.IsPending()
							_ = task.IsStarted()
							_ = task.IsDone()
							_ = task.IsFailed()
							_ = task.IsCanceled()
							_ = task.IsInProgress()
							_ = task.IsEnd()
							_ = task.Err()
							_ = task.ID()
							atomic.AddInt64(&counter, 1)
						}
					}()
				}
				
				wg.Wait()
				task.Await()
			
			case "concurrent task creation":
				tasks := make([]*Task, tc.numWorkers*tc.operations)
				index := int64(0)
				
				for i := 0; i < tc.numWorkers; i++ {
					wg.Add(1)
					go func(workerID int) {
						defer wg.Done()
						for j := 0; j < tc.operations; j++ {
							task := New(Definition{
								ID: fmt.Sprintf("race-task-%d-%d", workerID, j),
								TaskFn: func(r *Run) error {
									return nil
								},
								MaxRetries: 2,
								Delay:      time.Microsecond * 10,
							})
							idx := atomic.AddInt64(&index, 1) - 1
							tasks[idx] = task
							atomic.AddInt64(&counter, 1)
						}
					}(i)
				}
				
				wg.Wait()
				
				// Verify all tasks are unique
				ids := make(map[string]bool)
				for _, task := range tasks {
					if task != nil {
						id := task.ID()
						s.False(ids[id], "Duplicate task ID found")
						ids[id] = true
					}
				}
			
			case "concurrent execution and cancellation":
				for i := 0; i < tc.numWorkers; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for j := 0; j < tc.operations; j++ {
							task := New(Definition{
								TaskFn: func(r *Run) error {
									select {
									case <-r.Context().Done():
										return r.Context().Err()
									case <-time.After(time.Millisecond * 30):
										return nil
									}
								},
							})
							
							err := task.Go(s.ctx)
							s.NoError(err)
							
							// Randomly cancel some tasks
							if j%3 == 0 {
								go func() {
									time.Sleep(time.Millisecond * 10)
									task.Cancel()
								}()
							}
							
							task.Await()
							s.True(task.IsEnd())
							atomic.AddInt64(&counter, 1)
						}
					}()
				}
				
				wg.Wait()
			
			case "concurrent retry operations":
				for i := 0; i < tc.numWorkers; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for j := 0; j < tc.operations; j++ {
							attempts := 0
							task := New(Definition{
								MaxRetries: 2,
								TaskFn: func(r *Run) error {
									attempts++
									if attempts <= 1 {
										return errors.New("fail")
									}
									return nil
								},
							})
							
							err := task.GoRetry(s.ctx)
							s.NoError(err)
							s.True(task.IsDone())
							atomic.AddInt64(&counter, 1)
						}
					}()
				}
				
				wg.Wait()
			}
			
			expectedOperations := int64(tc.numWorkers * tc.operations)
			s.Equal(expectedOperations, counter, "Not all operations completed")
		})
	}
}

func (s *TaskTestSuite) TestEdgeCases() {
	s.Run("task ID uniqueness under high load", func() {
		numTasks := 1000
		ids := make(map[string]bool)
		var mu sync.Mutex

		var wg sync.WaitGroup
		for i := 0; i < numTasks; i++ {
			wg.Add(1)
			go func(taskID int) {
				defer wg.Done()
				task := New(Definition{
					ID:     fmt.Sprintf("test-task-%d", taskID),
					TaskFn: func(r *Run) error { return nil },
				})
				id := task.ID()

				mu.Lock()
				s.False(ids[id], "Duplicate ID found: %v", id)
				ids[id] = true
				mu.Unlock()
			}(i)
		}

		wg.Wait()
		s.Equal(numTasks, len(ids))
	})

	s.Run("empty ID is handled correctly", func() {
		task := New(Definition{
			ID:     "",
			TaskFn: func(r *Run) error { return nil },
		})
		s.Equal("", task.ID())
	})
	
	s.Run("multiple Await calls are safe", func() {
		task := New(Definition{
			TaskFn: func(r *Run) error {
				time.Sleep(time.Millisecond * 50)
				return nil
			},
		})
		
		err := task.Go(s.ctx)
		s.NoError(err)
		
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				task.Await()
			}()
		}
		
		wg.Wait()
		s.True(task.IsDone())
	})
	
	s.Run("context cancellation race with task completion", func() {
		for i := 0; i < 50; i++ {
			ctx, cancel := context.WithCancel(s.ctx)
			task := New(Definition{
				TaskFn: func(r *Run) error {
					time.Sleep(time.Microsecond * 100)
					return nil
				},
			})
			
			err := task.Go(ctx)
			s.NoError(err)
			
			go cancel()
			
			task.Await()
			s.True(task.IsEnd())
		}
	})
	
	s.Run("panic in task function is properly recovered", func() {
		panicMessages := []interface{}{
			"string panic",
			42,
			errors.New("error panic"),
			nil,
			struct{ msg string }{"struct panic"},
		}
		
		for _, panicMsg := range panicMessages {
			task := New(Definition{
				TaskFn: func(r *Run) error {
					panic(panicMsg)
				},
			})
			
			err := task.Go(s.ctx)
			s.NoError(err)
			task.Await()
			
			s.True(task.IsFailed())
			s.Contains(task.Err().Error(), "task panicked")
		}
	})
	
	s.Run("retry with zero maxRetries", func() {
		task := New(Definition{
			MaxRetries: 0,
			TaskFn: func(r *Run) error {
				return errors.New("always fail")
			},
		})
		
		err := task.GoRetry(s.ctx)
		s.Error(err)
	})
	
	s.Run("delay with immediate cancellation", func() {
		task := New(Definition{
			Delay: time.Millisecond * 100,
			TaskFn: func(r *Run) error {
				return nil
			},
		})
		
		ctx, cancel := context.WithCancel(s.ctx)
		cancel() // Cancel immediately
		
		err := task.Go(ctx)
		s.Error(err)
		task.Await()
		
		s.True(task.IsCanceled())
	})
}

func (s *TaskTestSuite) TestTaskWait() {
	s.Run("Await blocks until task completes", func() {
		var completed int32
		task := New(Definition{
			TaskFn: func(r *Run) error {
				time.Sleep(time.Millisecond * 50)
				atomic.StoreInt32(&completed, 1)
				return nil
			},
		})
		
		err := task.Go(s.ctx)
		s.NoError(err)
		
		s.Equal(int32(0), atomic.LoadInt32(&completed))
		task.Await()
		s.Equal(int32(1), atomic.LoadInt32(&completed))
		s.True(task.IsDone())
	})
	
	s.Run("Await returns immediately if task not started", func() {
		task := New(Definition{TaskFn: func(r *Run) error { return nil }})
		
		start := time.Now()
		task.Await()
		duration := time.Since(start)
		
		s.Less(duration, time.Millisecond*10)
		s.True(task.IsCreated())
	})
	
	s.Run("Await works for all end states", func() {
		endStates := []struct {
			name     string
			setupFn  func() *Task
			expected uint32
		}{
			{
				name: "DONE",
				setupFn: func() *Task {
					task := New(Definition{TaskFn: func(r *Run) error { return nil }})
					err := task.Go(s.ctx)
					s.NoError(err)
					return task
				},
				expected: DONE,
			},
			{
				name: "FAILED",
				setupFn: func() *Task {
					task := New(Definition{TaskFn: func(r *Run) error { return errors.New("fail") }})
					err := task.Go(s.ctx)
					s.NoError(err)
					return task
				},
				expected: FAILED,
			},
			{
				name: "CANCELED",
				setupFn: func() *Task {
					task := New(Definition{TaskFn: func(r *Run) error { <-r.Context().Done(); return r.Context().Err() }})
					err := task.Go(s.ctx)
					s.NoError(err)
					task.Cancel()
					return task
				},
				expected: CANCELED,
			},
		}
		
		for _, tc := range endStates {
			s.Run(tc.name, func() {
				task := tc.setupFn()
				task.Await()
				s.Equal(tc.expected, task.state.Load())
			})
		}
	})
}

// Benchmark tests
func BenchmarkTaskCreation(b *testing.B) {
	def := Definition{
		TaskFn: func(r *Run) error { return nil },
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = New(def)
	}
}

func BenchmarkTaskExecution(b *testing.B) {
	ctx := context.Background()
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		task := New(Definition{
			TaskFn: func(r *Run) error { return nil },
		})
		
		err := task.Go(ctx)
		if err != nil {
			b.Fatal(err)
		}
		task.Await()
	}
}

func BenchmarkTaskExecutionWithDelay(b *testing.B) {
	ctx := context.Background()
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		task := New(Definition{
			Delay:  time.Microsecond * 10,
			TaskFn: func(r *Run) error { return nil },
		})
		
		err := task.Go(ctx)
		if err != nil {
			b.Fatal(err)
		}
		task.Await()
	}
}

func BenchmarkTaskRetry(b *testing.B) {
	ctx := context.Background()
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		attempts := 0
		task := New(Definition{
			MaxRetries: 2,
			TaskFn: func(r *Run) error {
				attempts++
				if attempts <= 1 {
					return errors.New("fail")
				}
				return nil
			},
		})
		
		err := task.GoRetry(ctx)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConcurrentTaskExecution(b *testing.B) {
	ctx := context.Background()
	
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			task := New(Definition{
				TaskFn: func(r *Run) error { return nil },
			})
			
			err := task.Go(ctx)
			if err != nil {
				b.Fatal(err)
			}
			task.Await()
		}
	})
}

func BenchmarkStateChecks(b *testing.B) {
	task := New(Definition{
		TaskFn: func(r *Run) error {
			time.Sleep(time.Millisecond * 10)
			return nil
		},
	})
	
	ctx := context.Background()
	err := task.Go(ctx)
	if err != nil {
		b.Fatal(err)
	}
	
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			task.IsCreated()
			task.IsPending()
			task.IsStarted()
			task.IsDone()
			task.IsFailed()
			task.IsCanceled()
			task.IsInProgress()
			task.IsEnd()
		}
	})
	
	task.Await()
}

func BenchmarkMemoryAllocation(b *testing.B) {
	ctx := context.Background()
	
	b.ResetTimer()
	b.ReportAllocs()
	
	for i := 0; i < b.N; i++ {
		task := New(Definition{
			TaskFn: func(r *Run) error { return nil },
		})
		
		err := task.Go(ctx)
		if err != nil {
			b.Fatal(err)
		}
		task.Await()
	}
}

// Memory leak detection test
func (s *TaskTestSuite) TestMemoryLeak() {
	s.Run("no goroutine leaks", func() {
		initialGoroutines := runtime.NumGoroutine()
		
		// Create and run many tasks
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				task := New(Definition{
					TaskFn: func(r *Run) error {
						time.Sleep(time.Millisecond)
						return nil
					},
				})
				
				err := task.Go(s.ctx)
				s.NoError(err)
				task.Await()
			}()
		}
		
		wg.Wait()
		
		// Give time for goroutines to cleanup
		time.Sleep(time.Millisecond * 100)
		runtime.GC()
		time.Sleep(time.Millisecond * 100)
		
		finalGoroutines := runtime.NumGoroutine()
		
		// Allow for some variance but should be close to initial count
		s.InDelta(initialGoroutines, finalGoroutines, 5,
			"Potential goroutine leak detected: initial=%d, final=%d",
			initialGoroutines, finalGoroutines)
	})
	
	s.Run("no goroutine leaks with retries", func() {
		initialGoroutines := runtime.NumGoroutine()
		
		// Create and run many retry tasks
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				attempts := 0
				task := New(Definition{
					MaxRetries: 2,
					TaskFn: func(r *Run) error {
						time.Sleep(time.Millisecond)
						attempts++
						if attempts <= 1 {
							return errors.New("fail")
						}
						return nil
					},
				})
				
				err := task.GoRetry(s.ctx)
				s.NoError(err)
			}()
		}
		
		wg.Wait()
		
		// Give time for goroutines to cleanup
		time.Sleep(time.Millisecond * 100)
		runtime.GC()
		time.Sleep(time.Millisecond * 100)
		
		finalGoroutines := runtime.NumGoroutine()
		
		// Allow for some variance but should be close to initial count
		s.InDelta(initialGoroutines, finalGoroutines, 5,
			"Potential goroutine leak detected: initial=%d, final=%d",
			initialGoroutines, finalGoroutines)
	})
}
