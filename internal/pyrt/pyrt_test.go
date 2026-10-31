package pyrt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstall(t *testing.T) {
	root := t.TempDir()
	dir, err := Install(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"__init__.py", "_registry.py", "_runtime.py"} {
		if _, err := os.Stat(filepath.Join(dir, "gothon", f)); err != nil {
			t.Fatal(err)
		}
	}
	again, err := Install(root)
	if err != nil || again != dir {
		t.Fatalf("second install: %q %v, want %q", again, err, dir)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("want only the install dir under root, got %d entries", len(entries))
	}
}
