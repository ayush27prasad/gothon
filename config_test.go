package gothon

import (
	"context"
	"strings"
	"testing"
)

func TestFirst(t *testing.T) {
	if got := first(64, 0, 0, 0); got != 64 {
		t.Fatalf("all unset: %d", got)
	}
	if got := first(64, 0, 8, 16); got != 8 {
		t.Fatalf("most specific set value should win: %d", got)
	}
	if got := first(64, -1, 8); got != -1 {
		t.Fatalf("negative is an explicit value: %d", got)
	}
}

func TestTaskSettingsLayering(t *testing.T) {
	rt := NewRuntime(Defaults{MaxFrame: 100})
	p := &Pool{rt: rt, tasks: map[string]*task{}, cfg: ModuleConfig{
		MaxFrame: 200,
		Tasks:    map[string]TaskConfig{"big": {MaxFrame: 300}},
	}}
	if got := p.task("big").maxFrame; got != 300 {
		t.Fatalf("task setting: %d", got)
	}
	if got := p.task("other").maxFrame; got != 200 {
		t.Fatalf("module default: %d", got)
	}
	p.cfg.MaxFrame = 0
	p.tasks = map[string]*task{}
	if got := p.task("other").maxFrame; got != 100 {
		t.Fatalf("runtime default: %d", got)
	}
	p.rt = NewRuntime(Defaults{})
	p.tasks = map[string]*task{}
	if got := p.task("other").maxFrame; got != 64<<20 {
		t.Fatalf("built-in default: %d", got)
	}
}

func TestMaxWorkers(t *testing.T) {
	py := testPython(t)
	rt := NewRuntime(Defaults{MaxWorkers: 3})
	cfg := ModuleConfig{Python: py, Dir: "testdata/py", Name: "basic"}

	cfg.Workers = 2
	a, err := rt.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Workers = 2
	if _, err := rt.Start(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "only 1 of MaxWorkers=3") {
		t.Fatalf("over budget: %v", err)
	}
	a.Close()
	b, err := rt.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("budget not returned on Close: %v", err)
	}
	rt.Close()
	if err := b.Call(context.Background(), "pid", nil, nil); err == nil {
		t.Fatal("Runtime.Close should close every pool")
	}
}
