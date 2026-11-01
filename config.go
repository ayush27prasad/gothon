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
	Workers      int
	StartTimeout time.Duration

	// Task defaults; see TaskConfig.
	MaxFrame int
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
	// StartTimeout bounds how long a worker may take to import the module. Default 60s.
	StartTimeout time.Duration

	// Task defaults for this module; see TaskConfig.
	MaxFrame int

	// Tasks configures individual tasks by name, overriding the module's defaults.
	Tasks map[string]TaskConfig
}

// TaskConfig configures calls to one task.
type TaskConfig struct {
	// MaxFrame caps the size of one response in bytes. Default 64 MiB.
	MaxFrame int
}

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
