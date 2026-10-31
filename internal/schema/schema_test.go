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

// A field named like a JSON Schema doc keyword must still count toward the hash.
func TestHashKeepsFieldNamedTitle(t *testing.T) {
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
	script := `
from pydantic import BaseModel
from gothon import _registry
from gothon._schema import export

def hash_for(t):
    class Doc(BaseModel):
        title: t
        description: str = "x"
    _registry.tasks.clear()
    def f(d: Doc) -> None: ...
    _registry.tasks["f"] = _registry.Task(f, "f")
    return export(_registry.tasks)["hash"]

assert hash_for(str) != hash_for(int), "type change of a field named title was ignored"
`
	cmd := exec.Command(py, "-c", script)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+rt)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
