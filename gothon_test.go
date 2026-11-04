package gothon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ayush27prasad/gothon/internal/project"
	"github.com/ayush27prasad/gothon/internal/pyrt"
	"github.com/ayush27prasad/gothon/internal/schema"
)

// testPython returns the interpreter for integration tests. Set GOTHON_TEST_PYTHON
// to a Python with pydantic installed; tests are skipped if it is unavailable.
func testPython(t *testing.T) string {
	t.Helper()
	py := os.Getenv("GOTHON_TEST_PYTHON")
	if py == "" {
		py = "python3"
	}
	if err := exec.Command(py, "-c", "import pydantic").Run(); err != nil {
		t.Skipf("no python with pydantic at %q (set GOTHON_TEST_PYTHON)", py)
	}
	return py
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func start(t *testing.T, module string, workers int) (*Pool, *logBuf) {
	t.Helper()
	logs := &logBuf{}
	rt := NewRuntime(Defaults{Logger: slog.New(slog.NewTextHandler(logs, nil))})
	p, err := rt.Start(context.Background(), ModuleConfig{
		Python:  testPython(t),
		Dir:     "testdata/py",
		Name:    module,
		Workers: workers,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, logs
}

func pid(t *testing.T, p *Pool) int {
	t.Helper()
	var n int
	if err := p.Call(context.Background(), "pid", nil, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCall(t *testing.T) {
	p, _ := start(t, "basic", 1)
	var out struct{ Sum int }
	if err := p.Call(context.Background(), "add", map[string]int{"a": 2, "b": 3}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Sum != 5 {
		t.Fatalf("sum = %d, want 5", out.Sum)
	}

	var echoed map[string]any
	if err := p.Call(context.Background(), "echo", map[string]any{"k": []int{1, 2}}, &echoed); err != nil {
		t.Fatal(err)
	}
	if len(echoed["k"].([]any)) != 2 {
		t.Fatalf("echo = %v", echoed)
	}
}

func TestErrors(t *testing.T) {
	p, _ := start(t, "basic", 1)
	ctx := context.Background()

	var pe *PythonError
	err := p.Call(ctx, "add", map[string]int{"a": 1, "b": -1}, nil)
	if !errors.As(err, &pe) || pe.Stage != StageInput || len(pe.Fields) != 1 || pe.Fields[0].Loc[0] != "b" {
		t.Fatalf("input validation: %#v", err)
	}
	if strings.Contains(pe.Message, "-1") {
		t.Fatalf("validation message leaks the input value: %q", pe.Message)
	}

	err = p.Call(ctx, "fail", "boom", nil)
	if !errors.As(err, &pe) || pe.Stage != StageTask || pe.Type != "ValueError" || pe.Message != "boom" {
		t.Fatalf("task error: %#v", err)
	}
	if !strings.Contains(pe.Traceback, `raise ValueError(msg)`) || strings.Contains(pe.Traceback, "_runtime.py") {
		t.Fatalf("traceback should show the task and hide the runtime:\n%s", pe.Traceback)
	}

	err = p.Call(ctx, "bad_output", nil, nil)
	if !errors.As(err, &pe) || pe.Stage != StageOutput {
		t.Fatalf("output validation: %#v", err)
	}

	err = p.Call(ctx, "nope", nil, nil)
	if !errors.As(err, &pe) || pe.Stage != StageDispatch {
		t.Fatalf("unknown task: %#v", err)
	}

	// The worker survives all of the above.
	pid(t, p)
}

func TestStartupErrors(t *testing.T) {
	py := testPython(t)
	var pe *PythonError
	_, err := NewRuntime(Defaults{}).Start(context.Background(), ModuleConfig{Python: py, Dir: "testdata/py", Name: "broken", Workers: 2})
	if !errors.As(err, &pe) || pe.Stage != StageImport || pe.Type != "ModuleNotFoundError" {
		t.Fatalf("broken module: %#v", err)
	}
	if strings.Contains(pe.Traceback, "importlib") || !strings.Contains(pe.Traceback, "broken.py") {
		t.Fatalf("import traceback should start at the module:\n%s", pe.Traceback)
	}
	_, err = NewRuntime(Defaults{}).Start(context.Background(), ModuleConfig{Python: py, Dir: "testdata/py", Name: "notasks", Workers: 1})
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, "no @gothon.task") {
		t.Fatalf("module without tasks: %#v", err)
	}
	_, err = NewRuntime(Defaults{}).Start(context.Background(), ModuleConfig{Python: "/no/such/python", Name: "basic", Workers: 1})
	if err == nil || !strings.Contains(err.Error(), "gothon sync") {
		t.Fatalf("missing interpreter: want a hint to run gothon sync, got %v", err)
	}
}

func TestTimeoutKillsWorker(t *testing.T) {
	p, _ := start(t, "basic", 1)
	before := pid(t, p)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	err := p.Call(ctx, "spin", 5000, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("timed-out call took %v", d)
	}

	// The next call gets a fresh worker instead of waiting behind the abandoned task.
	t0 = time.Now()
	after := pid(t, p)
	if after == before {
		t.Fatal("worker was not replaced")
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("call after timeout took %v", d)
	}
}

func TestCrashReplacesWorker(t *testing.T) {
	p, logs := start(t, "basic", 1)
	before := pid(t, p)
	if err := p.Call(context.Background(), "crash", nil, nil); !errors.Is(err, ErrWorkerExited) {
		t.Fatalf("want ErrWorkerExited, got %v", err)
	}
	if after := pid(t, p); after == before {
		t.Fatal("worker was not replaced")
	}
	if !strings.Contains(logs.String(), "worker exited, replacing it") {
		t.Fatalf("missing replacement log:\n%s", logs)
	}
}

func TestIdleWorkerKilledExternally(t *testing.T) {
	p, _ := start(t, "basic", 1)
	before := pid(t, p)
	syscall.Kill(before, syscall.SIGKILL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var after int
	for after == 0 || after == before {
		if err := p.Call(ctx, "pid", nil, &after); err != nil && !errors.Is(err, ErrWorkerExited) {
			t.Fatal(err)
		}
	}
}

func TestStartContextDoesNotOwnWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := NewRuntime(Defaults{}).Start(ctx, ModuleConfig{Python: testPython(t), Dir: "testdata/py", Name: "basic", Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cancel()
	time.Sleep(100 * time.Millisecond)
	pid(t, p)
}

func TestConcurrentCalls(t *testing.T) {
	p, _ := start(t, "basic", 4)
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out struct{ Sum int }
			if err := p.Call(context.Background(), "add", map[string]int{"a": i, "b": 1}, &out); err != nil {
				errs <- err
			} else if out.Sum != i+1 {
				errs <- errors.New("wrong sum")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestQueuedCallerGivesUp(t *testing.T) {
	p, _ := start(t, "basic", 1)
	go p.Call(context.Background(), "spin", 300, nil)
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.Call(ctx, "pid", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded while queued, got %v", err)
	}
	pid(t, p) // the busy worker is released to the next caller, not lost
}

func TestWorkerOutputIsLogged(t *testing.T) {
	p, logs := start(t, "basic", 1)
	if err := p.Call(context.Background(), "talk", nil, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "hello from stdout") {
		if time.Now().After(deadline) {
			t.Fatalf("print() output not logged:\n%s", logs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestClose(t *testing.T) {
	p, _ := start(t, "basic", 2)
	done := make(chan error, 1)
	go func() { done <- p.Call(context.Background(), "spin", 5000, nil) }()
	time.Sleep(100 * time.Millisecond)
	p.Close()
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatalf("in-flight call: want ErrClosed, got %v", err)
	}
	if err := p.Call(context.Background(), "pid", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after Close: want ErrClosed, got %v", err)
	}
}

func TestProjectDefaults(t *testing.T) {
	py := testPython(t)
	root := t.TempDir()
	pyDir, _ := filepath.Abs("testdata/py")
	m := project.Default()
	m.Python = pyDir
	if err := m.Save(root); err != nil {
		t.Fatal(err)
	}

	// Dir comes from the project.
	p, err := NewRuntime(Defaults{Root: root}).Start(context.Background(), ModuleConfig{Python: py, Name: "basic", Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	p.Close()

	// Python defaults to the project's venv, which does not exist yet.
	_, err = NewRuntime(Defaults{Root: root}).Start(context.Background(), ModuleConfig{Name: "basic", Workers: 1})
	if err == nil || !strings.Contains(err.Error(), filepath.Join(pyDir, ".venv")) || !strings.Contains(err.Error(), "gothon sync") {
		t.Fatalf("want missing-venv error with a hint, got %v", err)
	}

	// An explicit root without a manifest is an error.
	if _, err := NewRuntime(Defaults{Root: t.TempDir()}).Start(context.Background(), ModuleConfig{Name: "basic"}); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("want project.ErrNotFound, got %v", err)
	}
}

func TestSchemaHash(t *testing.T) {
	py := testPython(t)
	cfg := ModuleConfig{Python: py, Dir: "testdata/py", Name: "basic", Workers: 1, SchemaHash: "0000000000000000"}
	if _, err := NewRuntime(Defaults{}).Start(context.Background(), cfg); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("want ErrSchemaMismatch, got %v", err)
	}

	rt, _ := pyrt.InstallDefault()
	dir, _ := filepath.Abs("testdata/py")
	env := append(os.Environ(), "PYTHONPATH="+rt+string(os.PathListSeparator)+dir)
	e, err := schema.Run(context.Background(), py, env, "basic")
	if err != nil {
		t.Fatal(err)
	}
	cfg.SchemaHash = e.Hash
	p, err := NewRuntime(Defaults{}).Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
}

func TestSettingsValidationAtImport(t *testing.T) {
	_, err := NewRuntime(Defaults{}).Start(context.Background(), ModuleConfig{Python: testPython(t), Dir: "testdata/py", Name: "needs_config", Workers: 1})
	var pe *PythonError
	if !errors.As(err, &pe) || pe.Stage != StageImport || len(pe.Fields) != 1 || pe.Fields[0].Loc[0] != "api_key" {
		t.Fatalf("want import-stage validation error for api_key, got %#v", err)
	}

	p, err := NewRuntime(Defaults{}).Start(context.Background(), ModuleConfig{Python: testPython(t), Dir: "testdata/py", Name: "needs_config", Workers: 1, Env: []string{"NEEDS_CONFIG_API_KEY=secret"}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var n int
	if err := p.Call(context.Background(), "key_length", nil, &n); err != nil || n != 6 {
		t.Fatalf("key_length = %d, %v", n, err)
	}
}

func TestTimeoutLayers(t *testing.T) {
	py := testPython(t)
	rt := NewRuntime(Defaults{Timeout: 100 * time.Millisecond})
	p, err := rt.Start(context.Background(), ModuleConfig{
		Python: py, Dir: "testdata/py", Name: "basic", Workers: 1,
		Tasks: map[string]TaskConfig{"spin": {Timeout: -1}}, // no timeout for spin
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// The runtime default applies to a context without a deadline.
	t0 := time.Now()
	if err := p.Call(context.Background(), "echo", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Call(context.Background(), "spin", 300, nil); err != nil {
		t.Fatalf("task override of no timeout: %v", err)
	}
	if time.Since(t0) < 300*time.Millisecond {
		t.Fatal("spin did not run")
	}

	q, err := rt.Start(context.Background(), ModuleConfig{Python: py, Dir: "testdata/py", Name: "basic", Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	t0 = time.Now()
	if err := q.Call(context.Background(), "spin", 2000, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runtime default timeout: %v", err)
	}
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("timeout took %v", d)
	}
}

func TestMaxQueue(t *testing.T) {
	py := testPython(t)
	p, err := NewRuntime(Defaults{}).Start(context.Background(), ModuleConfig{
		Python: py, Dir: "testdata/py", Name: "basic", Workers: 1,
		Tasks: map[string]TaskConfig{"spin": {MaxQueue: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	go p.Call(context.Background(), "spin", 400, nil) // running
	time.Sleep(50 * time.Millisecond)
	go p.Call(context.Background(), "spin", 10, nil) // waiting: fills the queue
	time.Sleep(50 * time.Millisecond)

	t0 := time.Now()
	err = p.Call(context.Background(), "spin", 10, nil)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	if d := time.Since(t0); d > 20*time.Millisecond {
		t.Fatalf("ErrBusy should be immediate, took %v", d)
	}
	// Other tasks have no limit and still queue normally.
	if err := p.Call(context.Background(), "pid", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestHopelessCallRejected(t *testing.T) {
	p, _ := start(t, "basic", 1)
	for range 5 {
		if err := p.Call(context.Background(), "spin", 200, nil); err != nil {
			t.Fatal(err)
		}
	}
	before := pid(t, p)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	err := p.Call(ctx, "spin", 200, nil)
	if !errors.Is(err, ErrBusy) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want ErrBusy and DeadlineExceeded, got %v", err)
	}
	if d := time.Since(t0); d > 20*time.Millisecond {
		t.Fatalf("rejection should be immediate, took %v", d)
	}
	if after := pid(t, p); after != before {
		t.Fatal("a rejected call must not cost a worker")
	}
}

func TestLateWaiterRejected(t *testing.T) {
	p, _ := start(t, "basic", 1)
	for range 5 {
		if err := p.Call(context.Background(), "spin", 50, nil); err != nil {
			t.Fatal(err)
		}
	}
	go p.Call(context.Background(), "spin", 50, nil) // occupies the worker
	time.Sleep(5 * time.Millisecond)
	behind := make(chan error, 1)
	go func() {
		time.Sleep(5 * time.Millisecond) // queues after the late call
		behind <- p.Call(context.Background(), "pid", nil, nil)
	}()

	// When the worker frees up, this call has less time left than spin takes:
	// it is rejected then, and the worker goes to the next waiter.
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	err := p.Call(ctx, "spin", 50, nil)
	if !errors.Is(err, ErrBusy) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want ErrBusy and DeadlineExceeded, got %v", err)
	}
	if d := time.Since(t0); d > 65*time.Millisecond {
		t.Fatalf("rejected after %v, want as soon as the worker was free", d)
	}
	if err := <-behind; err != nil {
		t.Fatal(err)
	}
	if r := p.Stats().Restarts; r != 0 {
		t.Fatalf("%d restarts", r)
	}
}

func TestMaxConcurrency(t *testing.T) {
	py := testPython(t)
	p, err := NewRuntime(Defaults{}).Start(context.Background(), ModuleConfig{
		Python: py, Dir: "testdata/py", Name: "basic", Workers: 3,
		Tasks: map[string]TaskConfig{"spin": {MaxConcurrency: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	t0 := time.Now()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Call(context.Background(), "spin", 150, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	time.Sleep(30 * time.Millisecond)
	// spin holds one worker at most, so other tasks still get one right away.
	t1 := time.Now()
	if err := p.Call(context.Background(), "pid", nil, nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t1); d > 50*time.Millisecond {
		t.Fatalf("pid waited %v behind spin", d)
	}
	wg.Wait()
	if d := time.Since(t0); d < 450*time.Millisecond {
		t.Fatalf("3 spins with MaxConcurrency 1 took %v, want them run one at a time", d)
	}
}

func TestShutdownGraceful(t *testing.T) {
	p, logs := start(t, "basic", 2)
	result := make(chan error, 1)
	go func() { result <- p.Call(context.Background(), "spin", 300, nil) }()
	time.Sleep(50 * time.Millisecond)

	shutdown := make(chan error, 1)
	go func() { shutdown <- p.Shutdown(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	if err := p.Call(context.Background(), "pid", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call during shutdown: want ErrClosed, got %v", err)
	}
	if err := <-result; err != nil {
		t.Fatalf("in-flight call should finish, got %v", err)
	}
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "replacing") {
		t.Fatalf("workers should exit cleanly, not be replaced:\n%s", logs)
	}
}

func TestShutdownForced(t *testing.T) {
	p, _ := start(t, "basic", 1)
	result := make(chan error, 1)
	go func() { result <- p.Call(context.Background(), "spin", 5000, nil) }()
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	if err := p.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("forced shutdown took %v", d)
	}
	if err := <-result; !errors.Is(err, ErrClosed) {
		t.Fatalf("cut-off call: want ErrClosed, got %v", err)
	}
}

func TestDrainOnTimeout(t *testing.T) {
	py := testPython(t)
	p, err := NewRuntime(Defaults{}).Start(context.Background(), ModuleConfig{
		Python: py, Dir: "testdata/py", Name: "basic", Workers: 1, OnTimeout: Drain,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	before := pid(t, p)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	if err := p.Call(ctx, "spin", 400, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if d := time.Since(t0); d > 200*time.Millisecond {
		t.Fatalf("caller should get its error at the deadline, took %v", d)
	}
	// The worker finishes the task, then serves the next call: no restart.
	if after := pid(t, p); after != before {
		t.Fatal("Drain should keep the worker")
	}
	if d := time.Since(t0); d < 350*time.Millisecond {
		t.Fatalf("next call should wait for the drained task, took %v", d)
	}
}

func TestDrainTimeoutKills(t *testing.T) {
	py := testPython(t)
	p, err := NewRuntime(Defaults{}).Start(context.Background(), ModuleConfig{
		Python: py, Dir: "testdata/py", Name: "basic", Workers: 1,
		Tasks: map[string]TaskConfig{"spin": {OnTimeout: Drain, DrainTimeout: 100 * time.Millisecond}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	before := pid(t, p)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	p.Call(ctx, "spin", 5000, nil)
	if after := pid(t, p); after == before {
		t.Fatal("worker should be killed after DrainTimeout")
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("took %v; the drained task was not cut off", d)
	}
}

func TestThreadsPerWorker(t *testing.T) {
	py := testPython(t)
	rt := NewRuntime(Defaults{})
	env := func(cfg ModuleConfig) string {
		t.Helper()
		cfg.Python, cfg.Dir, cfg.Name = py, "testdata/py", "basic"
		p, err := rt.Start(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		var v string
		if err := p.Call(context.Background(), "getenv", "OMP_NUM_THREADS", &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got, want := env(ModuleConfig{Workers: 2}), strconv.Itoa(max(1, runtime.GOMAXPROCS(0)/2)); got != want {
		t.Fatalf("default threads = %s, want %s", got, want)
	}
	if got := env(ModuleConfig{Workers: 1, ThreadsPerWorker: 3}); got != "3" {
		t.Fatalf("ThreadsPerWorker = %s, want 3", got)
	}
	if got := env(ModuleConfig{Workers: 1, Env: []string{"OMP_NUM_THREADS=7"}}); got != "7" {
		t.Fatalf("Env should win, got %s", got)
	}
}

func TestMaxJobs(t *testing.T) {
	py := testPython(t)
	p, err := NewRuntime(Defaults{}).Start(context.Background(), ModuleConfig{
		Python: py, Dir: "testdata/py", Name: "basic", Workers: 1, MaxJobs: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	before := pid(t, p)
	if pid(t, p) != before || pid(t, p) != before {
		t.Fatal("worker replaced before MaxJobs")
	}
	if pid(t, p) == before {
		t.Fatal("worker not replaced after MaxJobs")
	}
	if s := p.Stats(); s.Recycled != 1 || s.Restarts != 0 {
		t.Fatalf("stats: recycled %d, restarts %d", s.Recycled, s.Restarts)
	}
}

func TestMaxMemory(t *testing.T) {
	py := testPython(t)
	p, err := NewRuntime(Defaults{}).Start(context.Background(), ModuleConfig{
		Python: py, Dir: "testdata/py", Name: "leak", Workers: 1, MaxMemory: 200 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	grow := func(mb int) int {
		t.Helper()
		var n int
		if err := p.Call(context.Background(), "grow", mb, &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := grow(1)
	if grow(1) != before {
		t.Fatal("worker replaced under MaxMemory")
	}
	if grow(250) != before {
		t.Fatal("the call that crosses MaxMemory should still be served")
	}
	if grow(1) == before {
		t.Fatal("worker not replaced after crossing MaxMemory")
	}
	if s := p.Stats(); s.Recycled != 1 || s.Restarts != 0 {
		t.Fatalf("stats: recycled %d, restarts %d", s.Recycled, s.Restarts)
	}
}

func TestStats(t *testing.T) {
	py := testPython(t)
	rt := NewRuntime(Defaults{})
	p, err := rt.Start(context.Background(), ModuleConfig{
		Python: py, Dir: "testdata/py", Name: "basic", Workers: 2,
		Tasks: map[string]TaskConfig{"spin": {MaxQueue: 1, MaxConcurrency: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()

	p.Call(ctx, "add", map[string]int{"a": 1, "b": 2}, nil)
	p.Call(ctx, "fail", "boom", nil)
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	p.Call(short, "spin", 2000, nil) // times out and kills a worker
	cancel()

	go p.Call(ctx, "spin", 300, nil) // running
	time.Sleep(50 * time.Millisecond)
	go p.Call(ctx, "spin", 10, nil) // waiting for the task slot
	time.Sleep(50 * time.Millisecond)
	p.Call(ctx, "spin", 10, nil) // rejected

	s := p.Stats()
	if s.Module != "basic" || s.Workers != 2 || s.Restarts != 1 || s.Waiting != 1 {
		t.Fatalf("pool stats: %+v", s)
	}
	if s.Idle+s.Busy+s.Starting != 2 || s.Busy < 1 {
		t.Fatalf("worker states: idle %d busy %d starting %d", s.Idle, s.Busy, s.Starting)
	}
	if a := s.Tasks["add"]; a.Calls != 1 || a.Errors != 0 || a.AvgTime <= 0 {
		t.Fatalf("add stats: %+v", a)
	}
	if f := s.Tasks["fail"]; f.Calls != 1 || f.Errors != 1 {
		t.Fatalf("fail stats: %+v", f)
	}
	if sp := s.Tasks["spin"]; sp.Timeouts != 1 || sp.Rejected != 1 || sp.Running != 1 || sp.Waiting != 1 {
		t.Fatalf("spin stats: %+v", sp)
	}
	if all := rt.Stats(); len(all) != 1 || all[0].Module != "basic" {
		t.Fatalf("runtime stats: %+v", all)
	}
}

func TestAsyncTasks(t *testing.T) {
	p, _ := start(t, "basic", 1)
	ctx := context.Background()
	var n int
	if err := p.Call(ctx, "async_double", 21, &n); err != nil || n != 42 {
		t.Fatalf("async_double = %d, %v", n, err)
	}

	var a, b int
	p.Call(ctx, "loop_id", nil, &a)
	p.Call(ctx, "loop_id", nil, &b)
	if a == 0 || a != b {
		t.Fatalf("calls should share the worker's event loop: %d vs %d", a, b)
	}

	var pe *PythonError
	err := p.Call(ctx, "async_fail", nil, nil)
	if !errors.As(err, &pe) || pe.Stage != StageTask || pe.Type != "RuntimeError" || pe.Message != "async boom" {
		t.Fatalf("async error: %#v", err)
	}
	if strings.Contains(pe.Traceback, "asyncio") || !strings.Contains(pe.Traceback, `raise RuntimeError("async boom")`) {
		t.Fatalf("traceback should start at the task:\n%s", pe.Traceback)
	}
}
