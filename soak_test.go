package gothon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The soak and overload tests run only when GOTHON_SOAK is set to a duration:
//
//	GOTHON_SOAK=30m go test -run 'Soak|Overload' -v -timeout 0
//
// The soak runs for that long; the overload test takes about a minute regardless.

func soakDuration(t *testing.T) time.Duration {
	t.Helper()
	v := os.Getenv("GOTHON_SOAK")
	if v == "" {
		t.Skip("set GOTHON_SOAK=<duration> to run")
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("GOTHON_SOAK: %v", err)
	}
	return d
}

// latencies collects call durations from many goroutines.
type latencies struct {
	mu sync.Mutex
	d  []time.Duration
}

func (l *latencies) add(d time.Duration) {
	l.mu.Lock()
	l.d = append(l.d, d)
	l.mu.Unlock()
}

// take returns the durations collected so far, sorted, and starts over.
func (l *latencies) take() []time.Duration {
	l.mu.Lock()
	d := l.d
	l.d = nil
	l.mu.Unlock()
	slices.Sort(d)
	return d
}

func pct(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(float64(len(sorted)-1)*q)]
}

func ms(d time.Duration) string { return strconv.FormatFloat(float64(d)/1e6, 'f', 2, 64) }

func openFDs(t *testing.T) int {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func workerPIDs(p *Pool) []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	var pids []int
	for w := range p.live {
		pids = append(pids, w.pid)
	}
	return pids
}

// rss returns the total and largest resident memory of the processes, in MiB.
func rss(pids []int) (total, largest float64) {
	if len(pids) == 0 {
		return 0, 0
	}
	list := make([]string, len(pids))
	for i, pid := range pids {
		list[i] = strconv.Itoa(pid)
	}
	out, _ := exec.Command("ps", "-o", "rss=", "-p", strings.Join(list, ",")).Output() // fails if one has just exited
	for _, f := range strings.Fields(string(out)) {
		kb, _ := strconv.ParseFloat(f, 64)
		total += kb / 1024
		largest = max(largest, kb/1024)
	}
	return total, largest
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// TestSoak runs a mixed load against one pool for GOTHON_SOAK while workers are
// crashed, timed out and killed from outside, and checks that latency, memory,
// file descriptors, goroutines and processes stay bounded.
func TestSoak(t *testing.T) {
	dur := soakDuration(t)
	py := testPython(t)
	const (
		workers = 4
		callers = 32
	)
	every := min(max(dur/30, 5*time.Second), time.Minute)

	runtime.GC()
	goroutines0, fds0 := runtime.NumGoroutine(), openFDs(t)
	rt := NewRuntime(Defaults{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	p, err := rt.Start(context.Background(), ModuleConfig{
		Python: py, Dir: "testdata/py", Name: "basic", Workers: workers, MaxJobs: 2000,
		Tasks: map[string]TaskConfig{
			"spin":         {Timeout: 2 * time.Second},
			"async_double": {OnTimeout: Drain, DrainTimeout: time.Second},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	var (
		addLat     latencies
		ops        atomic.Int64
		casualties atomic.Int64 // calls on a worker killed from outside: expected
		unexpected atomic.Int64
		errMu      sync.Mutex
		errSamples []string
	)
	fail := func(op string, err error) {
		if unexpected.Add(1) <= 10 {
			errMu.Lock()
			errSamples = append(errSamples, fmt.Sprintf("%s: %v", op, err))
			errMu.Unlock()
		}
	}
	check := func(op string, err error) bool {
		if err == nil {
			return true
		}
		if errors.Is(err, ErrWorkerExited) {
			casualties.Add(1)
		} else {
			fail(op, err)
		}
		return false
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	payload := strings.Repeat("x", 64<<10)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ops.Add(1)
				switch n := rand.IntN(100); {
				case n < 70:
					a, b := rand.IntN(1000), rand.IntN(1000)
					var out addOut
					t0 := time.Now()
					err := p.Call(ctx, "add", addIn{A: a, B: b}, &out)
					if check("add", err) {
						addLat.add(time.Since(t0))
						if out.Sum != a+b {
							fail("add", fmt.Errorf("%d+%d = %d", a, b, out.Sum))
						}
					}
				case n < 80:
					var out string
					if check("echo", p.Call(ctx, "echo", payload, &out)) && len(out) != len(payload) {
						fail("echo", fmt.Errorf("got %d bytes back, sent %d", len(out), len(payload)))
					}
				case n < 88:
					check("spin", p.Call(ctx, "spin", 2, nil))
				case n < 93:
					var pe *PythonError
					if err := p.Call(ctx, "fail", "boom", nil); !errors.As(err, &pe) || pe.Stage != StageTask {
						check("fail", fmt.Errorf("want a task-stage PythonError, got %w", err))
					}
				default:
					// Drained timeouts, and rejections once the task's run time is known.
					cctx, cancel := context.WithTimeout(ctx, time.Duration(5+rand.IntN(25))*time.Millisecond)
					var out int
					err := p.Call(cctx, "async_double", 21, &out)
					cancel()
					switch {
					case errors.Is(err, context.DeadlineExceeded):
					case check("async_double", err) && out != 42:
						fail("async_double", fmt.Errorf("got %d", out))
					}
				}
			}
		}()
	}

	// Chaos: every 250ms, crash a worker, time out a call inside Python, or kill a
	// worker's process group from outside.
	seen := map[int]bool{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			switch i % 3 {
			case 0:
				if err := p.Call(context.Background(), "crash", nil, nil); !errors.Is(err, ErrWorkerExited) {
					fail("crash", fmt.Errorf("want ErrWorkerExited, got %v", err))
				}
			case 1:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
				err := p.Call(ctx, "spin", 1000, nil)
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					fail("spin timeout", fmt.Errorf("want DeadlineExceeded, got %v", err))
				}
			case 2:
				if pids := workerPIDs(p); len(pids) > 0 {
					syscall.Kill(-pids[rand.IntN(len(pids))], syscall.SIGKILL)
				}
			}
		}
	}()

	type sample struct {
		heap, rssTotal, rssMax float64
		fds, goroutines        int
	}
	var samples []sample
	t.Logf("%-6s %9s %8s %8s %8s %8s %6s %6s %5s %5s %8s %8s %8s",
		"time", "ops/s", "add p50", "add p99", "p99.9", "max", "rstrt", "recyc", "fds", "gor", "heapMiB", "rssMiB", "maxRSS")
	began, lastOps := time.Now(), int64(0)
	for time.Since(began) < dur {
		time.Sleep(min(every, dur-time.Since(began)))
		pids := workerPIDs(p)
		for _, pid := range pids {
			seen[pid] = true
		}
		var m runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m)
		total, largest := rss(pids)
		s := sample{float64(m.HeapInuse) / (1 << 20), total, largest, openFDs(t), runtime.NumGoroutine()}
		samples = append(samples, s)
		lat, st, n := addLat.take(), p.Stats(), ops.Load()
		t.Logf("%-6s %9.0f %8s %8s %8s %8s %6d %6d %5d %5d %8.1f %8.1f %8.1f",
			time.Since(began).Round(time.Second), float64(n-lastOps)/every.Seconds(),
			ms(pct(lat, .5)), ms(pct(lat, .99)), ms(pct(lat, .999)), ms(pct(lat, 1)),
			st.Restarts, st.Recycled, s.fds, s.goroutines, s.heap, s.rssTotal, s.rssMax)
		lastOps = n
		if p99 := pct(lat, .99); p99 > 100*time.Millisecond {
			t.Errorf("add p99 %v", p99)
		}
	}
	close(stop)
	wg.Wait()

	st := p.Stats()
	t.Logf("calls %d, worker casualties %d, restarts %d, recycled %d, unexpected errors %d",
		ops.Load(), casualties.Load(), st.Restarts, st.Recycled, unexpected.Load())
	for _, s := range errSamples {
		t.Error(s)
	}

	// The pool settles back to all workers idle and serving.
	deadline := time.Now().Add(10 * time.Second)
	for st = p.Stats(); st.Idle != workers || st.Waiting != 0; st = p.Stats() {
		if time.Now().After(deadline) {
			t.Fatalf("pool did not settle: %+v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := p.Call(context.Background(), "add", addIn{A: 1, B: 2}, nil); err != nil {
		t.Fatal(err)
	}

	// Nothing grows with the number of calls or restarts.
	first, last := samples[0], samples[len(samples)-1]
	for _, s := range samples {
		if s.fds > fds0+8*workers+16 {
			t.Errorf("%d open files, %d before start: leaking file descriptors", s.fds, fds0)
		}
	}
	if last.heap > 2*first.heap+16 {
		t.Errorf("Go heap grew from %.1f to %.1f MiB", first.heap, last.heap)
	}
	if last.rssMax > 1.5*first.rssMax+10 {
		t.Errorf("largest worker grew from %.1f to %.1f MiB", first.rssMax, last.rssMax)
	}
	if last.goroutines > first.goroutines+callers {
		t.Errorf("goroutines grew from %d to %d", first.goroutines, last.goroutines)
	}

	// Close leaves no processes, goroutines or files behind.
	for _, pid := range workerPIDs(p) {
		seen[pid] = true
	}
	p.Close()
	deadline = time.Now().Add(5 * time.Second)
	for {
		var left []int
		for pid := range seen {
			if alive(pid) {
				left = append(left, pid)
			}
		}
		g, f := runtime.NumGoroutine(), openFDs(t)
		if len(left) == 0 && g <= goroutines0+2 && f <= fds0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after Close: workers still alive %v, goroutines %d (was %d), files %d (was %d)",
				left, g, goroutines0, f, fds0)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestOverload offers a pool five times the load it can serve and checks that it
// keeps serving at capacity with bounded latency, sheds the excess quickly,
// loses no workers to timeouts, and recovers once the load drops.
func TestOverload(t *testing.T) {
	soakDuration(t)
	py := testPython(t)
	const (
		workers = 2
		work    = 10 // ms per call
		timeout = 100 * time.Millisecond
	)
	capacity := float64(workers) * 1000 / work // calls per second

	for _, mode := range []struct {
		name     string
		maxQueue int
	}{{"MaxQueue=4", 4}, {"unbounded queue", -1}} {
		t.Run(mode.name, func(t *testing.T) {
			p, err := NewRuntime(Defaults{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Start(context.Background(), ModuleConfig{
				Python: py, Dir: "testdata/py", Name: "basic", Workers: workers,
				Tasks: map[string]TaskConfig{"spin": {Timeout: timeout, MaxQueue: mode.maxQueue}},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			for range 10 { // learn the run time
				if err := p.Call(context.Background(), "spin", work, nil); err != nil {
					t.Fatal(err)
				}
			}

			type phase struct {
				name    string
				load    float64 // multiple of capacity
				seconds int
			}
			type result struct {
				ok, reject       []time.Duration
				timeouts, others int
				sent             int
				restarts         uint64
			}
			run := func(ph phase) result {
				var (
					mu  sync.Mutex
					res result
					wg  sync.WaitGroup
				)
				restarts := p.Stats().Restarts
				interval := time.Duration(float64(time.Second) / (capacity * ph.load))
				end := time.Now().Add(time.Duration(ph.seconds) * time.Second)
				for next := time.Now(); next.Before(end); next = next.Add(interval) {
					time.Sleep(time.Until(next))
					res.sent++
					wg.Add(1)
					go func() {
						defer wg.Done()
						t0 := time.Now()
						err := p.Call(context.Background(), "spin", work, nil)
						d := time.Since(t0)
						mu.Lock()
						defer mu.Unlock()
						switch {
						case err == nil:
							res.ok = append(res.ok, d)
						case errors.Is(err, ErrBusy):
							res.reject = append(res.reject, d)
						case errors.Is(err, context.DeadlineExceeded):
							res.timeouts++
						default:
							res.others++
							t.Error(err)
						}
					}()
				}
				wg.Wait()
				slices.Sort(res.ok)
				slices.Sort(res.reject)
				res.restarts = p.Stats().Restarts - restarts
				return res
			}

			t.Logf("%-9s %7s %9s %8s %8s %8s %8s %8s %6s", "phase", "sent/s", "served/s", "ok p50", "ok p99", "rejected", "rej p99", "timeouts", "kills")
			results := map[string]result{}
			for _, ph := range []phase{{"baseline", 0.5, 3}, {"overload", 5, 10}, {"recovery", 0.5, 3}} {
				r := run(ph)
				results[ph.name] = r
				t.Logf("%-9s %7.0f %9.0f %8s %8s %7.1f%% %8s %8d %6d", ph.name,
					float64(r.sent)/float64(ph.seconds), float64(len(r.ok))/float64(ph.seconds),
					ms(pct(r.ok, .5)), ms(pct(r.ok, .99)), 100*float64(len(r.reject))/float64(r.sent),
					ms(pct(r.reject, .99)), r.timeouts, r.restarts)
			}

			base, over, rec := results["baseline"], results["overload"], results["recovery"]
			if served := float64(len(over.ok)) / 10; served < 0.7*capacity {
				t.Errorf("served %.0f calls/s under overload, capacity is %.0f", served, capacity)
			}
			for name, r := range results {
				if r.restarts != 0 {
					t.Errorf("%s: %d workers killed by timeouts", name, r.restarts)
				}
			}
			if p99 := pct(rec.ok, .99); p99 > 3*pct(base.ok, .99)+5*time.Millisecond {
				t.Errorf("recovery p99 %v, baseline %v", p99, pct(base.ok, .99))
			}
			if mode.maxQueue > 0 {
				if p99 := pct(over.ok, .99); p99 > timeout/2 {
					t.Errorf("served calls p99 %v under overload with a bounded queue", p99)
				}
				if p99 := pct(over.reject, .99); p99 > 5*time.Millisecond {
					t.Errorf("rejections p99 %v, want immediate", p99)
				}
			}
		})
	}
}
