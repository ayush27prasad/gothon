// Package project reads and writes gothon.json, which marks the root of a Go
// project that uses gothon and says where its Python code and generated Go code live.
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// File is the manifest's file name.
const File = "gothon.json"

// ErrNotFound is returned when no manifest exists.
var ErrNotFound = errors.New("gothon: no " + File + " found")

// Manifest is the content of gothon.json. Paths are relative to the project root.
type Manifest struct {
	Python  string   `json:"python"`  // the Python project (pyproject.toml, .venv, modules)
	Out     string   `json:"out"`     // where generated Go packages are written, one per module
	Modules []string `json:"modules"` // Python modules with generated Go clients
}

// Default returns the manifest written by gothon init.
func Default() *Manifest {
	return &Manifest{Python: "python", Out: "internal/py", Modules: []string{}}
}

// Load reads the manifest in root.
func Load(root string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(root, File))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w in %s", ErrNotFound, root)
	}
	if err != nil {
		return nil, err
	}
	m := Default()
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("gothon: parse %s: %w", filepath.Join(root, File), err)
	}
	return m, nil
}

// Find returns the nearest directory at or above dir that contains a manifest.
func Find(dir string) (string, *Manifest, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}
	for {
		m, err := Load(dir)
		if !errors.Is(err, ErrNotFound) {
			return dir, m, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil, ErrNotFound
		}
		dir = parent
	}
}

// Save writes the manifest to root.
func (m *Manifest) Save(root string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, File), append(b, '\n'), 0o644)
}

// PythonDir is the Python project directory.
func (m *Manifest) PythonDir(root string) string {
	return resolve(root, m.Python)
}

// VenvPython is the interpreter of the project's virtual environment.
func (m *Manifest) VenvPython(root string) string {
	return filepath.Join(m.PythonDir(root), ".venv", "bin", "python")
}

// OutDir is the directory holding the generated Go package for module.
func (m *Manifest) OutDir(root, module string) string {
	return filepath.Join(resolve(root, m.Out), module)
}

func resolve(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}
