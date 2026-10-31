// Package pyrt ships the Python half of gothon inside the Go module.
package pyrt

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed gothon/*.py
var files embed.FS

// DefaultRoot is where the runtime is unpacked: $GOTHON_CACHE_DIR if set, else the
// user cache directory, else the temporary directory (e.g. no $HOME in a container).
func DefaultRoot() string {
	if dir := os.Getenv("GOTHON_CACHE_DIR"); dir != "" {
		return dir
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "gothon")
	}
	return filepath.Join(os.TempDir(), "gothon")
}

// Install writes the embedded Python package under root and returns the directory
// to put on PYTHONPATH. Each version of the files gets its own directory, written
// to a temporary name and renamed into place, so concurrent processes and upgrades
// never see a half-written package.
func Install(root string) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := files.ReadFile(path)
		h.Write([]byte(path))
		h.Write(b)
		return err
	})
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, hex.EncodeToString(h.Sum(nil))[:16])
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(root, ".tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := os.CopyFS(tmp, files); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if _, statErr := os.Stat(dir); statErr == nil {
			return dir, nil // another process installed it first
		}
		return "", err
	}
	return dir, nil
}
