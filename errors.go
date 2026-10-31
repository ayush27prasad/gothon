package gothon

import (
	"encoding/json"
	"errors"
	"fmt"
)

var (
	// ErrClosed is returned by calls on a pool that has been closed.
	ErrClosed = errors.New("gothon: pool closed")
	// ErrWorkerExited is returned when the worker process died while running a call.
	ErrWorkerExited = errors.New("gothon: worker exited")
)

// Stages at which Python can fail, reported in PythonError.Stage.
const (
	StageImport   = "import"   // importing the module at startup
	StageDispatch = "dispatch" // no task with the requested name
	StageInput    = "input"    // the input did not match the task's type hints
	StageTask     = "task"     // the task function raised
	StageOutput   = "output"   // the return value did not match the task's type hints
)

// PythonError is a failure reported by the Python side.
type PythonError struct {
	Task      string       `json:"-"`         // empty for startup failures
	Stage     string       `json:"stage"`     // one of the Stage constants
	Type      string       `json:"type"`      // exception class, e.g. "ValueError"
	Message   string       `json:"message"`   // str(exception)
	Traceback string       `json:"traceback"` // empty for validation errors
	Fields    []FieldError `json:"fields"`    // set for pydantic validation errors
}

// FieldError is one failed check from pydantic validation.
type FieldError struct {
	Loc  []any  `json:"loc"`  // path to the field, e.g. ["items", 0, "price"]
	Msg  string `json:"msg"`  // human-readable reason
	Type string `json:"type"` // pydantic error type, e.g. "missing"
}

func (e *PythonError) Error() string {
	if e.Task == "" {
		return fmt.Sprintf("gothon: %s failed: %s: %s", e.Stage, e.Type, e.Message)
	}
	return fmt.Sprintf("gothon: task %q failed at %s: %s: %s", e.Task, e.Stage, e.Type, e.Message)
}

func decodeError(task string, payload []byte) error {
	e := &PythonError{Task: task}
	if err := json.Unmarshal(payload, e); err != nil {
		return fmt.Errorf("gothon: undecodable error from worker: %w", err)
	}
	return e
}
