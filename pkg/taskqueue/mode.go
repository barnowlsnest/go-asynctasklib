package taskqueue

// Mode is the ordering discipline a Backend applies when selecting the next
// task to claim. It is a property of the backend, configured at construction.
type Mode int

const (
	// ModePriority orders by Priority descending, breaking ties FIFO by Seq
	// ascending.
	ModePriority Mode = iota
	// ModeFIFO orders strictly FIFO by Seq ascending; Priority is ignored.
	ModeFIFO
)
