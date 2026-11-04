package gothon

import "time"

// Hooks receive events as they happen, to export metrics or traces without gothon
// depending on a metrics library. They run synchronously on gothon's goroutines,
// so they must be fast and safe for concurrent use. Either may be nil.
type Hooks struct {
	// OnCall runs once every call has finished, successfully or not.
	OnCall func(CallEvent)
	// OnWorker runs when a worker process starts, exits or fails to start.
	OnWorker func(WorkerEvent)
}

// CallEvent describes one finished call.
type CallEvent struct {
	Module, Task string
	// Wait is the time spent queued for a worker or a task slot.
	Wait time.Duration
	// Run is the time spent in Python, until the reply or the timeout. Zero if
	// the call never reached a worker, e.g. when rejected with ErrBusy.
	Run time.Duration
	// Err is the error Call returned, nil on success. Classify it with errors.Is
	// and errors.As: ErrBusy, context.DeadlineExceeded, *PythonError, and so on.
	Err error
}

// WorkerState is what happened to a worker in a WorkerEvent.
type WorkerState string

const (
	// WorkerStarted: the worker imported the module and is ready for calls.
	WorkerStarted WorkerState = "started"
	// WorkerStartFailed: the worker could not start; Err says why.
	WorkerStartFailed WorkerState = "start_failed"
	// WorkerExited: the worker crashed or was killed, e.g. after a timeout, and
	// is being replaced.
	WorkerExited WorkerState = "exited"
	// WorkerRecycled: the worker reached MaxJobs or MaxMemory and is being replaced.
	WorkerRecycled WorkerState = "recycled"
)

// WorkerEvent describes a change in one worker process.
type WorkerEvent struct {
	Module string
	PID    int // zero if the process never started
	State  WorkerState
	// Took is how long the worker took to start, for WorkerStarted.
	Took time.Duration
	// Jobs is how many calls the worker served, for WorkerExited and WorkerRecycled.
	Jobs int
	// Err is why the worker exited or failed to start.
	Err error
}
