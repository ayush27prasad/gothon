package gothon

import "context"

// Task is a typed handle to one Python task. In and Out should mirror the task's
// pydantic input and output models.
type Task[In, Out any] struct {
	pool *Pool
	name string
}

// NewTask returns a typed handle for the task registered in Python under name.
func NewTask[In, Out any](p *Pool, name string) Task[In, Out] {
	return Task[In, Out]{pool: p, name: name}
}

// Call runs the task. See Pool.Call.
func (t Task[In, Out]) Call(ctx context.Context, in In) (Out, error) {
	var out Out
	err := t.pool.Call(ctx, t.name, in, &out)
	return out, err
}
