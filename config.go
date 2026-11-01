package gothon

import (
	"log/slog"
	"time"
)

// Configuration comes in three layers: Defaults for the whole Runtime, ModuleConfig
// for one Python module, and TaskConfig for one task. For each setting the most
// specific non-zero value wins; when every layer leaves it zero, gothon's built-in
// default applies.

// Defaults configures a Runtime and supplies defaults for every module and task.
type Defaults struct {
	// Root is the project directory holding gothon.json. If empty, $GOTHON_ROOT is
	// used, then the nearest gothon.json at or above the working directory. The
	// project tells modules where their interpreter and code are.
	Root string
	// Logger receives worker output and lifecycle events. Default slog.Default().
	Logger *slog.Logger
	// MaxWorkers caps the Python processes of all modules together. Starting a
	// module that would exceed it fails. Default: no cap.
	MaxWorkers int

	// Module defaults; see ModuleConfig.
	Workers          int
	ThreadsPerWorker int
	StartTimeout     time.Duration

	// Task defaults; see TaskConfig.
	Timeout      time.Duration
	MaxQueue     int
	MaxFrame     int
	OnTimeout    TimeoutPolicy
	DrainTimeout time.Duration
}

// ModuleConfig configures the worker processes of one Python module and the
// defaults for its tasks. Generated clients fill in Name, SchemaHash and Tasks.
type ModuleConfig struct {
	// Name is the Python module that defines the tasks, e.g. "ner".
	Name string
	// Python is the interpreter. Default: the project's .venv, else "python3".
	Python string
	// Dir is added to PYTHONPATH so the module can be imported from it.
	// Default: the project's Python directory.
	Dir string
	// Env adds KEY=VALUE environment variables for the workers.
	Env []string
	// SchemaHash, if set, must match the hash of the module's task signatures, so
	// generated Go types never silently drift from the Python models.
	SchemaHash string

	// Workers is the number of Python processes. Default runtime.GOMAXPROCS(0).
	Workers int
	// ThreadsPerWorker sets OMP_NUM_THREADS, OPENBLAS_NUM_THREADS, MKL_NUM_THREADS,
	// VECLIB_MAXIMUM_THREADS and NUMEXPR_NUM_THREADS for the workers. Libraries such
	// as numpy and PyTorch otherwise start one thread per core in every worker,
	// oversubscribing the CPU. Variables set in Env take precedence.
	// Default: GOMAXPROCS divided by Workers, at least 1.
	ThreadsPerWorker int
	// StartTimeout bounds how long a worker may take to import the module. Default 60s.
	StartTimeout time.Duration

	// Task defaults for this module; see TaskConfig.
	Timeout      time.Duration
	MaxQueue     int
	MaxFrame     int
	OnTimeout    TimeoutPolicy
	DrainTimeout time.Duration

	// Tasks configures individual tasks by name, overriding the module's defaults.
	Tasks map[string]TaskConfig
}

// TaskConfig configures calls to one task.
type TaskConfig struct {
	// Timeout bounds each call: it is the deadline when the caller's context has
	// none, and caps a later one. A call that runs out of time while Python is
	// working kills that worker. To avoid that, a call whose remaining time is less
	// than half the task's typical run time is rejected before it reaches Python,
	// with an error matching both ErrBusy and context.DeadlineExceeded.
	// Negative means no timeout. Default: none.
	Timeout time.Duration
	// MaxQueue is how many calls of the task may wait for a worker. A call beyond
	// that fails at once with ErrBusy, so an overloaded server can shed load
	// instead of queueing until every request times out. Negative means no limit.
	// Default: no limit.
	MaxQueue int
	// MaxFrame caps the size of one response in bytes. Default 64 MiB.
	MaxFrame int
	// OnTimeout decides what happens to a worker whose call runs out of time while
	// Python is still working. Default Kill.
	OnTimeout TimeoutPolicy
	// DrainTimeout is the longest a task may keep running after its call timed out
	// under the Drain policy; then the worker is killed anyway. Default 1 minute.
	DrainTimeout time.Duration
	// MaxConcurrency is how many workers the task may occupy at once, so a slow
	// task cannot starve the module's other tasks. Calls over the limit wait (and
	// count toward MaxQueue). Default: no limit beyond the module's workers.
	MaxConcurrency int
}

// TimeoutPolicy is what happens to a worker whose call runs out of time.
type TimeoutPolicy int

const (
	// Kill kills the worker and starts a replacement. The caller's slot is free at
	// once, at the cost of a restart: right for modules that start quickly.
	Kill TimeoutPolicy = iota + 1
	// Drain lets the task finish out of rotation, then returns the worker to the
	// pool. The caller still gets its error at once. Right for modules that are
	// slow to start, such as those loading large models, where a restart costs
	// more than waiting.
	Drain
)

// first returns the first non-zero value, most specific first, or def if all are zero.
func first[T comparable](def T, values ...T) T {
	var zero T
	for _, v := range values {
		if v != zero {
			return v
		}
	}
	return def
}
