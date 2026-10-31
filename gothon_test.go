package gothon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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
	p, err := Start(context.Background(), Config{
		Python:  testPython(t),
		Dir:     "testdata/py",
		Module:  module,
		Workers: workers,
		Logger:  slog.New(slog.NewTextHandler(logs, nil)),
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
	_, err := Start(context.Background(), Config{Python: py, Dir: "testdata/py", Module: "broken", Workers: 2})
	if !errors.As(err, &pe) || pe.Stage != StageImport || pe.Type != "ModuleNotFoundError" {
		t.Fatalf("broken module: %#v", err)
	}
	_, err = Start(context.Background(), Config{Python: py, Dir: "testdata/py", Module: "notasks", Workers: 1})
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, "no @gothon.task") {
		t.Fatalf("module without tasks: %#v", err)
	}
	_, err = Start(context.Background(), Config{Python: "/no/such/python", Module: "basic", Workers: 1})
	if err == nil {
		t.Fatal("missing interpreter: want error")
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
	p, err := Start(ctx, Config{Python: testPython(t), Dir: "testdata/py", Module: "basic", Workers: 1})
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
