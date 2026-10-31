package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFind(t *testing.T) {
	root := t.TempDir()
	m := Default()
	m.Modules = []string{"ner"}
	if err := m.Save(root); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "internal", "api")
	os.MkdirAll(sub, 0o755)

	got, gm, err := Find(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != root || len(gm.Modules) != 1 || gm.Python != "python" {
		t.Fatalf("Find = %q %+v", got, gm)
	}
	if want := filepath.Join(root, "python", ".venv", "bin", "python"); gm.VenvPython(got) != want {
		t.Fatalf("VenvPython = %q, want %q", gm.VenvPython(got), want)
	}
	if want := filepath.Join(root, "internal", "py", "ner"); gm.OutDir(got, "ner") != want {
		t.Fatalf("OutDir = %q, want %q", gm.OutDir(got, "ner"), want)
	}
}

func TestFindMissing(t *testing.T) {
	if _, _, err := Find(t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
