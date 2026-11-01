package gothon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/ayush27prasad/gothon/internal/project"
)

// Runtime owns the worker pools of a program's Python modules: it holds the
// defaults they inherit and the budget of Python processes they share.
// A program usually creates one Runtime and starts every module through it.
type Runtime struct {
	defaults Defaults
	logger   *slog.Logger

	mu    sync.Mutex
	pools map[*Pool]struct{}
	used  int // workers reserved against MaxWorkers
}

// NewRuntime returns a Runtime with the given defaults. The zero Defaults use
// gothon's built-in defaults throughout.
func NewRuntime(d Defaults) *Runtime {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Runtime{defaults: d, logger: logger, pools: make(map[*Pool]struct{})}
}

// Shutdown stops every module gracefully, in parallel; see Pool.Shutdown.
func (rt *Runtime) Shutdown(ctx context.Context) error {
	pools := rt.snapshot()
	errs := make(chan error, len(pools))
	for _, p := range pools {
		go func() { errs <- p.Shutdown(ctx) }()
	}
	var all []error
	for range pools {
		if err := <-errs; err != nil {
			all = append(all, err)
		}
	}
	return errors.Join(all...)
}

// Close kills the workers of every module. Calls in progress fail with ErrClosed.
func (rt *Runtime) Close() error {
	for _, p := range rt.snapshot() {
		p.Close()
	}
	return nil
}

func (rt *Runtime) snapshot() []*Pool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	pools := make([]*Pool, 0, len(rt.pools))
	for p := range rt.pools {
		pools = append(pools, p)
	}
	return pools
}

// reserve claims n workers from the MaxWorkers budget for a new pool.
func (rt *Runtime) reserve(p *Pool, n int) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if max := rt.defaults.MaxWorkers; max > 0 && rt.used+n > max {
		return fmt.Errorf("gothon: module %q needs %d workers but only %d of MaxWorkers=%d are free",
			p.name, n, max-rt.used, max)
	}
	rt.used += n
	rt.pools[p] = struct{}{}
	return nil
}

// release returns a closed pool's workers to the budget.
func (rt *Runtime) release(p *Pool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if _, ok := rt.pools[p]; ok {
		delete(rt.pools, p)
		rt.used -= p.workers
	}
}

// locate finds the interpreter and module directory, from cfg or the project's gothon.json.
func (rt *Runtime) locate(cfg ModuleConfig) (python, dir string, err error) {
	python, dir = cfg.Python, cfg.Dir
	if python != "" && dir != "" {
		return python, dir, nil
	}
	root := rt.defaults.Root
	if root == "" {
		root = os.Getenv("GOTHON_ROOT")
	}
	var m *project.Manifest
	if root != "" {
		m, err = project.Load(root)
	} else {
		var wd string
		if wd, err = os.Getwd(); err == nil {
			root, m, err = project.Find(wd)
			if errors.Is(err, project.ErrNotFound) {
				// No project: plain library use.
				return first("python3", python), dir, nil
			}
		}
	}
	if err != nil {
		return "", "", err
	}
	return first(m.VenvPython(root), python), first(m.PythonDir(root), dir), nil
}

// Start launches the workers of one module in parallel and returns once all of
// them have imported it. ctx bounds startup only; workers live until the pool is
// closed or shut down.
func (rt *Runtime) Start(ctx context.Context, cfg ModuleConfig) (*Pool, error) {
	return startPool(ctx, rt, cfg)
}
