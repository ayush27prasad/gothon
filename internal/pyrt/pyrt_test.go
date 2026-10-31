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

func TestInstallDefault(t *testing.T) {
	override := t.TempDir()
	t.Setenv("GOTHON_CACHE_DIR", override)
	dir, err := InstallDefault()
	if err != nil || filepath.Dir(dir) != override {
		t.Fatalf("with GOTHON_CACHE_DIR: %q %v", dir, err)
	}

	// An unwritable cache directory falls back to the temporary directory.
	t.Setenv("GOTHON_CACHE_DIR", "")
	t.Setenv("HOME", "/nonexistent")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("TMPDIR", t.TempDir())
	dir, err = InstallDefault()
	if err != nil || filepath.Dir(filepath.Dir(dir)) != os.Getenv("TMPDIR") {
		t.Fatalf("fallback: %q %v", dir, err)
	}
}
