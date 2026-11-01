package gothon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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
