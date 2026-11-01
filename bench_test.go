package gothon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func benchPool(b *testing.B, workers int) *Pool {
	b.Helper()
	rt := NewRuntime(Defaults{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	p, err := rt.Start(context.Background(), ModuleConfig{
		Python:  testPython(&testing.T{}),
		Dir:     "testdata/py",
		Name:    "basic",
		Workers: workers,
	})
	if err != nil {
		b.Skip(err)
	}
	b.Cleanup(func() { p.Close() })
	return p
}

func BenchmarkCall(b *testing.B) {
	p := benchPool(b, 1)
	add := NewTask[addIn, addOut](p, "add")
	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		if _, err := add.Call(ctx, addIn{A: 1, B: 2}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCallParallel(b *testing.B) {
	p := benchPool(b, 8)
	add := NewTask[addIn, addOut](p, "add")
	ctx := context.Background()
	b.SetParallelism(8) // 8 × GOMAXPROCS callers, more than there are workers
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := add.Call(ctx, addIn{A: 1, B: 2}); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkPayload(b *testing.B) {
	p := benchPool(b, 1)
	ctx := context.Background()
	for _, size := range []int{1 << 10, 100 << 10, 1 << 20, 8 << 20} {
		in := strings.Repeat("x", size)
		b.Run(fmt.Sprintf("%dKB", size>>10), func(b *testing.B) {
			b.SetBytes(int64(size))
			for range b.N {
				var out string
				if err := p.Call(ctx, "echo", in, &out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkStart(b *testing.B) {
	py := testPython(&testing.T{})
	for _, workers := range []int{1, 8} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			for range b.N {
				t0 := time.Now()
				p, err := NewRuntime(Defaults{}).Start(context.Background(), ModuleConfig{Python: py, Dir: "testdata/py", Name: "basic", Workers: workers})
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(time.Since(t0).Milliseconds()), "ms/start")
				p.Close()
			}
		})
	}
}
