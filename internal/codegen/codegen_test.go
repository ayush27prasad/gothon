package codegen

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ayush27prasad/gothon/internal/pyrt"
	"github.com/ayush27prasad/gothon/internal/schema"
)

var update = flag.Bool("update", false, "rewrite the golden client in internal/testgen")

func TestGoName(t *testing.T) {
	for in, want := range map[string]string{
		"order_id":     "OrderID",
		"placedAt":     "PlacedAt",
		"HTTPServer":   "HTTPServer",
		"user_ids":     "UserIDs",
		"api_url":      "APIURL",
		"echo_any":     "EchoAny",
		"2fa":          "X2fa",
		"Page_Item_":   "PageItem",
		"x-request-id": "XRequestID",
	} {
		if got := goName(in); got != want {
			t.Errorf("goName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnsupported(t *testing.T) {
	union := &schema.Export{Tasks: []schema.Task{{
		Name:   "f",
		Input:  &schema.Schema{AnyOf: []*schema.Schema{{Type: "string"}, {Type: "integer"}}},
		Output: &schema.Schema{Type: "null"},
	}}}
	if _, err := Generate(union, Options{Module: "m"}); err == nil || !strings.Contains(err.Error(), "f input: unions") {
		t.Fatalf("union: want a located error, got %v", err)
	}
}

// TestGolden generates the client for testdata/py/typed.py and compares it with
// internal/testgen/typed, which is compiled and exercised end to end.
func TestGolden(t *testing.T) {
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
	e, err := schema.Run(context.Background(), py, env, "typed")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Generate(e, Options{Module: "typed", Version: "v0.0.0-test"})
	if err != nil {
		t.Fatal(err)
	}

	golden := "../testgen/typed/typed.go"
	if *update {
		os.MkdirAll(filepath.Dir(golden), 0o755)
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("generated client differs from %s; run go test ./internal/codegen -update", golden)
	}
}
