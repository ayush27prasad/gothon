package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkflow runs init, add and gen in a fresh Go module, then builds and runs a
// program that calls the generated client. It needs uv and network access (or a
// warm uv cache), so it only runs with GOTHON_TEST_CLI=1.
func TestWorkflow(t *testing.T) {
	if os.Getenv("GOTHON_TEST_CLI") != "1" {
		t.Skip("set GOTHON_TEST_CLI=1 to run (needs uv)")
	}
	repo, _ := filepath.Abs("../..")
	app := t.TempDir()
	run := func(dir, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	run(app, "go", "mod", "init", "example.com/app")
	run(app, "go", "mod", "edit", "-require=github.com/ayush27prasad/gothon@v0.0.0", "-replace=github.com/ayush27prasad/gothon="+repo)

	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	if err := os.Chdir(app); err != nil {
		t.Fatal(err)
	}
	if err := runInit(nil); err != nil {
		t.Fatal(err)
	}
	if err := runAdd([]string{"greeter"}); err != nil {
		t.Fatal(err)
	}
	if err := runAdd([]string{"json"}); err == nil {
		t.Fatal("add json: want error for a standard library name")
	}
	if err := runGen([]string{"-check"}); err != nil {
		t.Fatal(err)
	}

	os.MkdirAll(filepath.Join(app, "cmd", "demo"), 0o755)
	os.WriteFile(filepath.Join(app, "cmd", "demo", "main.go"), []byte(`package main

import (
	"context"
	"fmt"

	"example.com/app/internal/py/greeter"
	"github.com/ayush27prasad/gothon"
)

func main() {
	c, err := greeter.Start(context.Background(), gothon.Config{Workers: 1})
	if err != nil {
		panic(err)
	}
	defer c.Close()
	out, err := c.Greet(context.Background(), greeter.GreetIn{Name: "gopher"})
	if err != nil {
		panic(err)
	}
	fmt.Println(out.Message)
}
`), 0o644)
	run(app, "go", "mod", "tidy")
	if out := run(filepath.Join(app, "cmd", "demo"), "go", "run", "."); !strings.Contains(out, "Hello, gopher!") {
		t.Fatalf("unexpected output: %s", out)
	}
}
