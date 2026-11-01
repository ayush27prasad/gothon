package gothon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ayush27prasad/gothon/internal/frame"
)

const protocolVersion = 1

// worker is one Python process. Requests go to its fd 3 and replies come back on fd 4.
type worker struct {
	cmd    *exec.Cmd
	pid    int
	in     *os.File      // our end of the child's fd 3
	out    *os.File      // our end of the child's fd 4
	r      *bufio.Reader // buffered reads from out
	buf    []byte        // reused request buffer
	exited chan struct{} // closed once the process has exited

	killOnce sync.Once
	logsDone sync.WaitGroup
	tailMu   sync.Mutex
	tail     []string // last stderr lines, for startup failures
}

func startWorker(python, module string, env []string, logger *slog.Logger) (*worker, error) {
	reqR, reqW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	respR, respW, err := os.Pipe()
	if err != nil {
		closeAll(reqR, reqW)
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		closeAll(reqR, reqW, respR, respW)
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		closeAll(reqR, reqW, respR, respW, stdoutR, stdoutW)
		return nil, err
	}

	cmd := exec.Command(python, "-m", "gothon._runtime", module)
	cmd.Env = env
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	cmd.ExtraFiles = []*os.File{reqR, respW}
	// Own process group: a Ctrl-C in the terminal reaches Go, which then shuts
	// workers down, instead of hitting every Python process directly.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	err = cmd.Start()
	closeAll(reqR, respW, stdoutW, stderrW) // the child holds its own copies
	if err != nil {
		closeAll(reqW, respR, stdoutR, stderrR)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("gothon: python interpreter %q not found; run `gothon sync` to create the project environment: %w", python, err)
		}
		return nil, fmt.Errorf("gothon: start %s: %w", python, err)
	}

	w := &worker{
		cmd:    cmd,
		pid:    cmd.Process.Pid,
		in:     reqW,
		out:    respR,
		r:      bufio.NewReaderSize(respR, 64<<10),
		exited: make(chan struct{}),
	}
	log := logger.With("pid", w.pid)
	w.logsDone.Add(2)
	go w.pump(stdoutR, log.With("stream", "stdout"), false)
	go w.pump(stderrR, log.With("stream", "stderr"), true)
	return w, nil
}

// pump forwards the worker's output to the logger line by line until the process exits.
func (w *worker) pump(f *os.File, log *slog.Logger, keepTail bool) {
	defer w.logsDone.Done()
	defer f.Close()
	r := bufio.NewReaderSize(f, 16<<10)
	for {
		line, err := r.ReadSlice('\n') // long lines arrive in chunks instead of blocking the worker
		if s := strings.TrimRight(string(line), "\r\n"); s != "" {
			log.Info(s)
			if keepTail {
				w.tailMu.Lock()
				if w.tail = append(w.tail, s); len(w.tail) > 20 {
					w.tail = w.tail[1:]
				}
				w.tailMu.Unlock()
			}
		}
		if err != nil && err != bufio.ErrBufferFull {
			return
		}
	}
}

// stderrTail returns the last stderr lines, waiting briefly for output still in flight.
func (w *worker) stderrTail() string {
	done := make(chan struct{})
	go func() { w.logsDone.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
	}
	w.tailMu.Lock()
	defer w.tailMu.Unlock()
	return strings.Join(w.tail, "\n")
}

// awaitReady waits until the worker has imported the module and reports in.
func (w *worker) awaitReady(ctx context.Context, timeout time.Duration, schemaHash string) error {
	w.out.SetReadDeadline(time.Now().Add(timeout))
	stop := context.AfterFunc(ctx, w.interrupt)
	kind, payload, err := frame.Read(w.r, 1<<20)
	if !stop() {
		return ctx.Err()
	}
	w.out.SetReadDeadline(time.Time{})

	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return fmt.Errorf("gothon: worker not ready after %v", timeout)
	case err != nil:
		msg := "gothon: worker exited during startup"
		if tail := w.stderrTail(); tail != "" {
			msg += ":\n" + tail
		}
		return errors.New(msg)
	case kind == frame.Error:
		return decodeError("", payload)
	case kind != frame.Ready:
		return fmt.Errorf("gothon: unexpected frame kind %d during startup", kind)
	}
	var info struct {
		Protocol   int    `json:"protocol"`
		SchemaHash string `json:"schema_hash"`
	}
	if err := json.Unmarshal(payload, &info); err != nil || info.Protocol != protocolVersion {
		return fmt.Errorf("gothon: worker speaks protocol %d, want %d", info.Protocol, protocolVersion)
	}
	if schemaHash != "" && info.SchemaHash != schemaHash {
		return fmt.Errorf("%w (generated %s, module now %s); run `gothon gen`", ErrSchemaMismatch, schemaHash, info.SchemaHash)
	}
	return nil
}

// roundtrip sends one request and reads the reply. If ctx ends first, it returns an
// error and the worker must be killed: Python may still be running the task.
func (w *worker) roundtrip(ctx context.Context, task string, input []byte, maxFrame int) (byte, []byte, error) {
	w.buf = frame.AppendRequest(w.buf[:0], task, input)
	stop := context.AfterFunc(ctx, w.interrupt)
	_, err := w.in.Write(w.buf)
	var kind byte
	var payload []byte
	if err == nil {
		kind, payload, err = frame.Read(w.r, maxFrame)
	}
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if cap(w.buf) > 1<<20 {
		w.buf = nil // don't keep a large request buffer alive
	}
	return kind, payload, err
}

// interrupt unblocks a pending read or write on the worker's pipes.
func (w *worker) interrupt() {
	now := time.Now()
	w.in.SetWriteDeadline(now)
	w.out.SetReadDeadline(now)
}

// kill stops the worker and anything it started.
func (w *worker) kill() {
	w.killOnce.Do(func() {
		syscall.Kill(-w.pid, syscall.SIGKILL)
		w.in.Close()
	})
}

func (w *worker) isExited() bool {
	select {
	case <-w.exited:
		return true
	default:
		return false
	}
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		f.Close()
	}
}
