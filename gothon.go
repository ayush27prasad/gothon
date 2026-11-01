// Package gothon runs Python functions from Go on a pool of warm worker processes.
//
// Each worker is a Python process that imports one module once and then serves
// calls over pipes. Go owns all scheduling: a call waits for a free worker in
// arrival order, a worker that crashes is replaced, and a call whose context
// ends while Python is still running kills that worker, so abandoned work never
// delays the next call.
package gothon

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ayush27prasad/gothon/internal/frame"
	"github.com/ayush27prasad/gothon/internal/pyrt"
)

// SupportPackageIsVersion1 is referenced by generated code, which then fails to
// compile against a version of this package too old to support it.
const SupportPackageIsVersion1 = true

// Pool runs calls on the worker processes of one Python module. It is safe for
// concurrent use. Create one with Runtime.Start.
type Pool struct {
	rt           *Runtime
	cfg          ModuleConfig
	name         string
	python       string
	env          []string
	workers      int
	startTimeout time.Duration
	logger       *slog.Logger

	tasksMu sync.Mutex
	tasks   map[string]*task // resolved per-task settings, created on first use

	mu      sync.Mutex
	idle    []*worker            // ready for a call
	waiters list.List            // of chan *worker, first come first served
	live    map[*worker]struct{} // every running process, so Close can kill them

	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup // watchers
}

func startPool(ctx context.Context, rt *Runtime, cfg ModuleConfig) (*Pool, error) {
	if cfg.Name == "" {
		return nil, errors.New("gothon: ModuleConfig.Name is required")
	}
	python, dir, err := rt.locate(cfg)
	if err != nil {
		return nil, err
	}
	d := rt.defaults
	p := &Pool{
		rt:           rt,
		cfg:          cfg,
		name:         cfg.Name,
		python:       python,
		workers:      first(runtime.GOMAXPROCS(0), cfg.Workers, d.Workers),
		startTimeout: first(60*time.Second, cfg.StartTimeout, d.StartTimeout),
		logger:       rt.logger.With("module", cfg.Name),
		tasks:        make(map[string]*task),
		closed:       make(chan struct{}),
		live:         make(map[*worker]struct{}),
	}

	pyrtDir, err := pyrt.InstallDefault()
	if err != nil {
		return nil, fmt.Errorf("gothon: install python runtime: %w", err)
	}
	pythonPath := []string{pyrtDir}
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		pythonPath = append(pythonPath, abs)
	}
	if v := os.Getenv("PYTHONPATH"); v != "" {
		pythonPath = append(pythonPath, v)
	}
	env := append(os.Environ(), "PYTHONUNBUFFERED=1", "PYTHONFAULTHANDLER=1")
	if cfg.SchemaHash != "" {
		env = append(env, "GOTHON_SCHEMA_HASH=1")
	}
	env = append(env, cfg.Env...)
	p.env = append(env, "PYTHONPATH="+strings.Join(pythonPath, string(os.PathListSeparator)))

	if err := rt.reserve(p, p.workers); err != nil {
		return nil, err
	}
	errs := make(chan error, p.workers)
	for range p.workers {
		go func() {
			w, err := p.spawn(ctx)
			if err == nil {
				p.release(w)
			}
			errs <- err
		}()
	}
	var firstErr error
	for range p.workers {
		if err := <-errs; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		p.Close()
		return nil, firstErr
	}
	return p, nil
}

// task holds the resolved settings and live counters of one task.
type task struct {
	name     string
	timeout  time.Duration // <= 0: none
	maxQueue int           // <= 0: no limit
	maxFrame int

	waiting atomic.Int64 // calls waiting for a worker
}

// wait counts a call as waiting, or rejects it with ErrBusy if the queue is full.
// The returned function stops counting it.
func (t *task) wait() (func(), error) {
	if n := t.waiting.Add(1); t.maxQueue > 0 && n > int64(t.maxQueue) {
		t.waiting.Add(-1)
		return nil, fmt.Errorf("%w: %d calls to %q are already waiting", ErrBusy, t.maxQueue, t.name)
	}
	return func() { t.waiting.Add(-1) }, nil
}

// task returns the settings for name, resolving them from the config layers on first use.
func (p *Pool) task(name string) *task {
	p.tasksMu.Lock()
	defer p.tasksMu.Unlock()
	if t, ok := p.tasks[name]; ok {
		return t
	}
	tc, mc, d := p.cfg.Tasks[name], p.cfg, p.rt.defaults
	t := &task{
		name:     name,
		timeout:  first(0, tc.Timeout, mc.Timeout, d.Timeout),
		maxQueue: first(0, tc.MaxQueue, mc.MaxQueue, d.MaxQueue),
		maxFrame: first(64<<20, tc.MaxFrame, mc.MaxFrame, d.MaxFrame),
	}
	p.tasks[name] = t
	return t
}

// Call runs task with in encoded as JSON and decodes the result into out, which may
// be nil. If ctx ends, or the task's Timeout passes, while Python is still running
// the task, the worker is killed and replaced, and Call returns the context error.
// If too many calls are already waiting, Call fails at once with ErrBusy.
func (p *Pool) Call(ctx context.Context, task string, in, out any) error {
	if task == "" || len(task) > frame.MaxTaskName {
		return fmt.Errorf("gothon: task name must be 1-%d bytes, got %q", frame.MaxTaskName, task)
	}
	input, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("gothon: encode input for %q: %w", task, err)
	}
	t := p.task(task)
	if t.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.timeout)
		defer cancel()
	}
	w, err := p.acquire(ctx, t)
	if err != nil {
		return err
	}

	kind, payload, err := w.roundtrip(ctx, task, input, t.maxFrame)
	if err != nil {
		w.kill() // its watcher starts a replacement
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case p.isClosed():
			return ErrClosed
		case errors.Is(err, frame.ErrTooLarge):
			return fmt.Errorf("gothon: response from %q: %w", task, err)
		default:
			return fmt.Errorf("%w while running %q: %v", ErrWorkerExited, task, err)
		}
	}
	p.release(w)

	switch kind {
	case frame.OK:
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(payload, out); err != nil {
			return fmt.Errorf("gothon: decode output of %q: %w", task, err)
		}
		return nil
	case frame.Error:
		return decodeError(task, payload)
	default:
		return fmt.Errorf("gothon: unexpected frame kind %d from %q", kind, task)
	}
}

// Close kills all workers and waits for them to exit. Calls in progress fail with ErrClosed.
func (p *Pool) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		close(p.closed)
		for w := range p.live {
			w.kill()
		}
		p.idle = nil
		p.mu.Unlock()
	})
	p.wg.Wait()
	p.rt.release(p)
	return nil
}

// acquire takes an idle worker, or queues until one is released.
func (p *Pool) acquire(ctx context.Context, t *task) (*worker, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.isClosed() {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	if n := len(p.idle); n > 0 {
		w := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return w, nil
	}
	done, err := t.wait()
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	defer done()
	ch := make(chan *worker, 1)
	e := p.waiters.PushBack(ch)
	p.mu.Unlock()

	select {
	case w := <-ch:
		return w, nil
	case <-ctx.Done():
		p.leave(e, ch)
		return nil, ctx.Err()
	case <-p.closed:
		p.leave(e, ch)
		return nil, ErrClosed
	}
}

// leave removes a waiter that gave up, passing on a worker it was handed meanwhile.
func (p *Pool) leave(e *list.Element, ch chan *worker) {
	p.mu.Lock()
	p.waiters.Remove(e)
	p.mu.Unlock()
	select {
	case w := <-ch:
		p.release(w)
	default:
	}
}

// release hands a worker to the longest waiting caller, or marks it idle.
func (p *Pool) release(w *worker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.isClosed() || w.isExited() {
		return // Close kills it, or its watcher is replacing it
	}
	if e := p.waiters.Front(); e != nil {
		p.waiters.Remove(e)
		e.Value.(chan *worker) <- w
		return
	}
	p.idle = append(p.idle, w)
}

// spawn starts a worker, registers it, and waits until it is ready.
func (p *Pool) spawn(ctx context.Context) (*worker, error) {
	w, err := startWorker(p.python, p.name, p.env, p.logger)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.isClosed() {
		p.mu.Unlock()
		w.kill()
		w.cmd.Wait()
		return nil, ErrClosed
	}
	p.live[w] = struct{}{}
	p.wg.Add(1)
	p.mu.Unlock()

	ready := make(chan struct{})
	go p.watch(w, ready)
	if err := w.awaitReady(ctx, p.startTimeout, p.cfg.SchemaHash); err != nil {
		w.kill()
		return nil, err
	}
	close(ready)
	return w, nil
}

// watch waits for a worker to exit, for any reason, and replaces it if it was serving.
func (p *Pool) watch(w *worker, ready <-chan struct{}) {
	defer p.wg.Done()
	err := w.cmd.Wait()
	close(w.exited)
	w.kill()
	w.out.Close()
	p.mu.Lock()
	delete(p.live, w)
	p.idle = slices.DeleteFunc(p.idle, func(x *worker) bool { return x == w })
	p.mu.Unlock()

	select {
	case <-ready:
	default:
		return // never became ready; whoever called spawn handles the failure
	}
	if p.isClosed() {
		return
	}
	p.logger.Warn("gothon: worker exited, replacing it", "pid", w.pid, "err", err)
	p.replace()
}

// replace starts a new worker, retrying with backoff until it succeeds or the pool closes.
func (p *Pool) replace() {
	backoff := 100 * time.Millisecond
	for {
		w, err := p.spawn(context.Background())
		if err == nil {
			p.release(w)
			return
		}
		if p.isClosed() {
			return
		}
		p.logger.Error("gothon: failed to start replacement worker", "err", err, "retry_in", backoff)
		select {
		case <-time.After(backoff):
		case <-p.closed:
			return
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}

func (p *Pool) isClosed() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}
