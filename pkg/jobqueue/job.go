package jobqueue

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	JobPending JobStatus = iota
	JobRunning
	JobCompleted
	JobFailed
)

type (
	JobAttributes struct {
		Name           string
		MessageGroupId string
		EnqueueAt      time.Time
		ExpireDeadline time.Time
		MaxRetries     int
		RawDedupToken  []byte
	}

	JobRun struct {
		id       uuid.UUID
		err      error
		workerID uint64
		startAt  time.Time
		endAt    time.Time
	}

	JobAttempts = map[uuid.UUID]*JobRun
	JobStatus   int

	Job struct {
		runID      uuid.UUID
		seq        uint64
		priority   int
		visibleAt  time.Time
		acquireAt  time.Time
		releaseAt  time.Time
		attempts   JobAttempts
		attr       *JobAttributes
		status     JobStatus
		rawPayload []byte
	}

	JobOptionFunc func(*Job)
)

func WithJobAttributes(attr *JobAttributes) JobOptionFunc {
	return func(job *Job) {
		job.attr = attr
	}
}

func WithJobVisibleAt(visibleAt time.Time) JobOptionFunc {
	return func(job *Job) {
		job.visibleAt = visibleAt
	}
}

func WithJobRawPayload(rawPayload []byte) JobOptionFunc {
	return func(job *Job) {
		job.rawPayload = rawPayload
	}
}

func WithJobPriority(priority int) JobOptionFunc {
	return func(job *Job) {
		job.priority = priority
	}
}

func NewJob(seq uint64, opts ...JobOptionFunc) (*Job, error) {
	job := &Job{
		seq:       seq,
		attempts:  make(JobAttempts),
		status:    JobPending,
		visibleAt: time.Time{},
	}

	for _, opt := range opts {
		opt(job)
	}

	if err := job.validate(); err != nil {
		return nil, err
	}

	return job, nil
}

func (job *Job) Seq() uint64 {
	return job.seq
}

func (job *Job) Priority() int {
	return job.priority
}

func (job *Job) VisibleAt() time.Time {
	return job.visibleAt
}

func (job *Job) RawPayload() []byte {
	return job.rawPayload
}

func (job *Job) Attr() *JobAttributes {
	return job.attr
}

func (job *Job) Status() JobStatus {
	return job.status
}

func (job *Job) Acquire(at time.Time) {
	job.acquireAt = at
	job.releaseAt = time.Time{}
}

func (job *Job) Release(at time.Time) time.Duration {
	job.releaseAt = at

	return job.releaseAt.Sub(job.acquireAt)
}

func (job *Job) IsAcquired() bool {
	return !job.acquireAt.IsZero() && job.releaseAt.IsZero()
}

func (job *Job) Attempts() JobAttempts {
	return job.attempts
}

func (job *Job) Run(workerID uint64, runID uuid.UUID, startAt time.Time) {
	job.status = JobRunning
	job.runID = runID
	job.attempts[job.runID] = &JobRun{
		workerID: workerID,
		id:       job.runID,
		startAt:  startAt,
		endAt:    time.Time{},
	}
}

func (job *Job) Complete(at time.Time) {
	run, exists := job.attempt()
	if !exists {
		return
	}

	run.endAt = at
	job.status = JobCompleted
}

func (job *Job) Fail(at time.Time, err error) {
	run, exists := job.attempt()
	if !exists {
		return
	}

	run.err = err
	run.endAt = at
	job.status = JobFailed
}

func (job *Job) Requeue(visibleAt time.Time) {
	job.status = JobPending
	job.visibleAt = visibleAt
	job.runID = uuid.UUID{}
	job.acquireAt = time.Time{}
	job.releaseAt = time.Time{}
}

func (job *Job) IsExpired(now time.Time) bool {
	if job.attr == nil {
		return false
	}

	if job.attr.ExpireDeadline.IsZero() {
		return false
	}

	return job.attr.ExpireDeadline.Before(now)
}

func (job *Job) IsVisible(now time.Time) bool {
	if job.visibleAt.IsZero() {
		return true
	}

	return job.visibleAt.Before(now)
}

func (job *Job) StatusString() string {
	switch job.status {
	case JobPending:
		return "pending"
	case JobRunning:
		return "running"
	case JobCompleted:
		return "completed"
	case JobFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func (run *JobRun) ID() uuid.UUID {
	return run.id
}

func (run *JobRun) WorkerID() uint64 {
	return run.workerID
}

func (run *JobRun) Err() error {
	return run.err
}

func (run *JobRun) StartAt() time.Time {
	return run.startAt
}

func (run *JobRun) EndAt() time.Time {
	return run.endAt
}

func (attr *JobAttributes) validate() error {
	return nil
}

func (job *Job) validate() error {
	if job.attr == nil {
		return errors.New("job attributes cannot be nil")
	}

	if err := job.attr.validate(); err != nil {
		return err
	}

	return nil
}

func (job *Job) attempt() (*JobRun, bool) {
	if len(job.attempts) == 0 {
		return nil, false
	}

	run, exists := job.attempts[job.runID]
	if !exists {
		return nil, false
	}

	return run, true
}
