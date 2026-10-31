package schema

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ayush27prasad/gothon/internal/pyrt"
)

func TestRun(t *testing.T) {
	py := os.Getenv("GOTHON_TEST_PYTHON")
	if py == "" {
		py = "python3"
	}
	if exec.Command(py, "-c", "import pydantic").Run() != nil {
		t.Skip("no python with pydantic (set GOTHON_TEST_PYTHON)")
	}
	rt, err := pyrt.Install(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.Abs("../../testdata/py")
	env := append(os.Environ(), "PYTHONPATH="+rt+string(os.PathListSeparator)+dir)

	e, err := Run(context.Background(), py, env, "basic")
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Hash) != 16 {
		t.Fatalf("hash = %q", e.Hash)
	}
	var add *Task
	for i := range e.Tasks {
		if e.Tasks[i].Name == "add" {
			add = &e.Tasks[i]
		}
	}
	if add == nil || add.Input.Ref != "#/$defs/AddIn" || add.Output.Ref != "#/$defs/AddOut" {
		t.Fatalf("add task = %+v", add)
	}
	in := e.Defs["AddIn"]
	if len(in.Properties) != 2 || in.Properties[0].Name != "a" || in.Properties[1].Name != "b" {
		t.Fatalf("AddIn properties out of order: %+v", in.Properties)
	}

	if _, err := Run(context.Background(), py, env, "broken"); err == nil {
		t.Fatal("broken module: want error")
	}
}
