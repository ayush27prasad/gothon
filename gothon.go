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
	"time"

	"github.com/ayush27prasad/gothon/internal/frame"
	"github.com/ayush27prasad/gothon/internal/project"
	"github.com/ayush27prasad/gothon/internal/pyrt"
)

// Config describes one pool of workers serving one Python module.
type Config struct {
	// Root is the project directory holding gothon.json. If empty, $GOTHON_ROOT is
	// used, then the nearest gothon.json at or above the working directory. The
	// project supplies the defaults for Python and Dir.
	Root string
	// Python is the interpreter. Default: the project's .venv, else "python3".
	Python string
	// Dir is added to PYTHONPATH so Module can be imported from it.
	// Default: the project's Python directory.
	Dir string
	// Module is the Python module that defines the tasks, e.g. "ner".
	Module string
	// Workers is the number of Python processes. Default runtime.GOMAXPROCS(0).
	Workers int
	// StartTimeout bounds how long a worker may take to import Module. Default 60s.
	StartTimeout time.Duration
	// MaxFrame caps the size of one response in bytes. Default 64 MiB.
	MaxFrame int
	// Env adds KEY=VALUE environment variables for the workers.
	Env []string
	// Logger receives worker output and lifecycle events. Default slog.Default().
	Logger *slog.Logger
}

// Pool runs calls on a fixed number of Python workers. It is safe for concurrent use.
type Pool struct {
	cfg Config
	env []string

	mu      sync.Mutex
	idle    []*worker            // ready for a call
	waiters list.List            // of chan *worker, first come first served
	live    map[*worker]struct{} // every running process, so Close can kill them

	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup // watchers
}

// Start launches cfg.Workers workers in parallel and returns once all of them have
// imported the module. ctx bounds startup only; workers live until Close.
func Start(ctx context.Context, cfg Config) (*Pool, error) {
	if cfg.Module == "" {
		return nil, errors.New("gothon: Config.Module is required")
	}
	if err := cfg.fromProject(); err != nil {
		return nil, err
	}
	if cfg.Workers <= 0 {
		cfg.Workers = runtime.GOMAXPROCS(0)
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = 60 * time.Second
	}
	if cfg.MaxFrame <= 0 {
		cfg.MaxFrame = 64 << 20
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	rt, err := pyrt.Install(runtimeRoot())
	if err != nil {
		return nil, fmt.Errorf("gothon: install python runtime: %w", err)
	}
	pythonPath := []string{rt}
	if cfg.Dir != "" {
		dir, err := filepath.Abs(cfg.Dir)
		if err != nil {
			return nil, err
		}
		pythonPath = append(pythonPath, dir)
	}
	if v := os.Getenv("PYTHONPATH"); v != "" {
		pythonPath = append(pythonPath, v)
	}
	env := append(os.Environ(), "PYTHONUNBUFFERED=1", "PYTHONFAULTHANDLER=1")
	env = append(env, cfg.Env...)
	env = append(env, "PYTHONPATH="+strings.Join(pythonPath, string(os.PathListSeparator)))

	p := &Pool{
		cfg:    cfg,
		env:    env,
		closed: make(chan struct{}),
		live:   make(map[*worker]struct{}),
	}
	errs := make(chan error, cfg.Workers)
	for range cfg.Workers {
		go func() {
			w, err := p.spawn(ctx)
			if err == nil {
				p.release(w)
			}
			errs <- err
		}()
	}
	var first error
	for range cfg.Workers {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}
	if first != nil {
		p.Close()
		return nil, first
	}
	return p, nil
}

// Call runs task with in encoded as JSON and decodes the result into out, which may
// be nil. If ctx ends while Python is still running the task, the worker is killed
// and replaced, and Call returns ctx.Err().
func (p *Pool) Call(ctx context.Context, task string, in, out any) error {
	if task == "" || len(task) > frame.MaxTaskName {
		return fmt.Errorf("gothon: task name must be 1-%d bytes, got %q", frame.MaxTaskName, task)
	}
	input, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("gothon: encode input for %q: %w", task, err)
	}
	w, err := p.acquire(ctx)
	if err != nil {
		return err
	}

	kind, payload, err := w.roundtrip(ctx, task, input, p.cfg.MaxFrame)
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
	return nil
}

// acquire takes an idle worker, or queues until one is released.
func (p *Pool) acquire(ctx context.Context) (*worker, error) {
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
	w, err := startWorker(&p.cfg, p.env)
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
	if err := w.awaitReady(ctx, p.cfg.StartTimeout, p.cfg.MaxFrame); err != nil {
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
	p.cfg.Logger.Warn("gothon: worker exited, replacing it", "module", p.cfg.Module, "pid", w.pid, "err", err)
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
		p.cfg.Logger.Error("gothon: failed to start replacement worker", "module", p.cfg.Module, "err", err, "retry_in", backoff)
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

// fromProject fills Python and Dir from the project's gothon.json.
func (cfg *Config) fromProject() error {
	if cfg.Python != "" && cfg.Dir != "" {
		return nil
	}
	root := cfg.Root
	if root == "" {
		root = os.Getenv("GOTHON_ROOT")
	}
	var m *project.Manifest
	var err error
	if root != "" {
		m, err = project.Load(root)
	} else if wd, wdErr := os.Getwd(); wdErr == nil {
		root, m, err = project.Find(wd)
		if errors.Is(err, project.ErrNotFound) {
			// No project: plain library use.
			if cfg.Python == "" {
				cfg.Python = "python3"
			}
			return nil
		}
	} else {
		err = wdErr
	}
	if err != nil {
		return err
	}
	if cfg.Dir == "" {
		cfg.Dir = m.PythonDir(root)
	}
	if cfg.Python == "" {
		cfg.Python = m.VenvPython(root)
	}
	return nil
}

// runtimeRoot is where the embedded Python package is unpacked.
func runtimeRoot() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "gothon")
	}
	return filepath.Join(os.TempDir(), "gothon")
}
