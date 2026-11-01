// Command gothon sets up and maintains the Python side of a Go project that uses
// github.com/ayush27prasad/gothon.
package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"text/template"
	"unsafe"

	"github.com/ayush27prasad/gothon/internal/codegen"
	"github.com/ayush27prasad/gothon/internal/project"
	"github.com/ayush27prasad/gothon/internal/pyrt"
	"github.com/ayush27prasad/gothon/internal/schema"
)

const usage = `gothon manages the Python modules a Go project calls.

Usage:
  gothon init [-python-dir DIR] [-out DIR] [-python-version V]
                         set up gothon.json and a uv-managed Python project
  gothon sync            create or update the Python environment with uv
  gothon add [-settings] <module>
                         add a Python module and generate its Go client
  gothon gen [-check] [module...]
                         regenerate Go clients from the Python type hints
  gothon version         print the gothon version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "init":
		err = runInit(args)
	case "sync":
		err = runSync()
	case "add":
		err = runAdd(args)
	case "gen":
		err = runGen(args)
	case "version":
		fmt.Println(version())
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "gothon: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

//go:embed templates/pyproject.toml.tmpl
var pyprojectTmpl string

//go:embed templates/module.py.tmpl
var moduleTmpl string

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	pyDir := fs.String("python-dir", "python", "directory of the Python project, relative to the Go module root")
	out := fs.String("out", "internal/py", "directory for generated Go packages")
	pyVersion := fs.String("python-version", "3.12", "Python version for uv to install and use")
	fs.Parse(args)

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	goMod, err := goModulePath(root)
	if err != nil {
		return err
	}

	m, err := project.Load(root)
	switch {
	case errors.Is(err, project.ErrNotFound):
		m = project.Default()
		m.Python, m.Out = *pyDir, *out
		if err := m.Save(root); err != nil {
			return err
		}
		fmt.Println("created", project.File)
	case err != nil:
		return err
	default:
		fmt.Println(project.File, "already exists")
	}

	dir := m.PythonDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(filepath.Base(goMod)), "-"), "-")
	files := []struct{ name, content string }{
		{"pyproject.toml", render(pyprojectTmpl, map[string]string{"Name": name + "-python"})},
		{".python-version", *pyVersion + "\n"},
		{".gitignore", ".venv/\n__pycache__/\n"},
	}
	for _, f := range files {
		if created, err := writeNew(filepath.Join(dir, f.name), f.content); err != nil {
			return err
		} else if created {
			fmt.Println("created", filepath.Join(m.Python, f.name))
		}
	}
	return sync(root, m)
}

func runSync() error {
	root, m, err := findProject()
	if err != nil {
		return err
	}
	return sync(root, m)
}

// sync creates or updates the Python environment with uv, which also installs
// the Python version the project asks for.
func sync(root string, m *project.Manifest) error {
	if err := uvRun("sync", "--project", m.PythonDir(root)); err != nil {
		return err
	}

	// Make `import gothon` resolve inside the venv, for editors and for running
	// modules directly. At run time the library puts its own copy first.
	rt, err := pyrt.InstallDefault()
	if err != nil {
		return err
	}
	out, err := exec.Command(m.VenvPython(root), "-c", "import sysconfig; print(sysconfig.get_paths()['purelib'])").Output()
	if err != nil {
		return fmt.Errorf("gothon: locate site-packages: %w", err)
	}
	pth := filepath.Join(strings.TrimSpace(string(out)), "gothon.pth")
	if err := os.WriteFile(pth, []byte(rt+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Println("python environment ready:", m.VenvPython(root))
	return nil
}

// uvRun runs uv, which manages the Python environment and installs Python itself.
func uvRun(args ...string) error {
	uv, err := exec.LookPath("uv")
	if err != nil {
		return errors.New(`gothon: uv is required to manage the Python environment. Install it with
  curl -LsSf https://astral.sh/uv/install.sh | sh
or see https://docs.astral.sh/uv/getting-started/installation/, then run: gothon sync`)
	}
	cmd := exec.Command(uv, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gothon: uv %s: %w", args[0], err)
	}
	return nil
}

// isTerminal reports whether f is an interactive terminal (not a pipe or /dev/null).
func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), ioctlGetTermios, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

func ask(question string) bool {
	fmt.Print(question)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

var moduleName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// reserved names would shadow gothon itself or be invalid as a Go package.
var reserved = []string{
	"gothon", "break", "case", "chan", "const", "continue", "default", "defer", "else",
	"fallthrough", "for", "func", "go", "goto", "if", "import", "interface", "map",
	"package", "range", "return", "select", "struct", "switch", "type", "var",
}

func runAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	withSettings := fs.Bool("settings", false, "include a pydantic-settings class for configuration such as API keys")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: gothon add [-settings] <module>")
	}
	module := fs.Arg(0)
	if !moduleName.MatchString(module) || slices.Contains(reserved, module) {
		return fmt.Errorf("gothon: %q is not a valid module name; use lowercase letters, digits and underscores", module)
	}
	root, m, err := findProject()
	if err != nil {
		return err
	}
	python, err := venvPython(root, m)
	if err != nil {
		return err
	}
	// Modules are imported ahead of the standard library, so a clash would break Python.
	check := fmt.Sprintf("import sys, keyword; sys.exit(%q in sys.stdlib_module_names or keyword.iskeyword(%q))", module, module)
	if exec.Command(python, "-c", check).Run() != nil {
		return fmt.Errorf("gothon: %q is a Python keyword or standard library module; pick another name", module)
	}

	path := filepath.Join(m.PythonDir(root), module+".py")
	_, statErr := os.Stat(path)
	isNew := errors.Is(statErr, os.ErrNotExist)
	if isNew && !*withSettings && isTerminal(os.Stdin) {
		*withSettings = ask(fmt.Sprintf("Does %s need settings such as API keys or URLs? [y/N] ", module))
	}
	if isNew && *withSettings {
		if err := uvRun("add", "--project", m.PythonDir(root), "pydantic-settings"); err != nil {
			return err
		}
	}
	data := map[string]any{"Settings": *withSettings, "EnvPrefix": strings.ToUpper(module) + "_"}
	if created, err := writeNew(path, render(moduleTmpl, data)); err != nil {
		return err
	} else if created {
		fmt.Println("created", filepath.Join(m.Python, module+".py"))
	}
	if !slices.Contains(m.Modules, module) {
		m.Modules = append(m.Modules, module)
		if err := m.Save(root); err != nil {
			return err
		}
	}
	if err := generate(root, m, python, []string{module}, false); err != nil {
		return err
	}

	goMod, _ := goModulePath(root)
	rel, _ := filepath.Rel(root, m.OutDir(root, module))
	start := fmt.Sprintf("%s.Start(ctx, rt, %s.Config{})", module, module)
	if *withSettings {
		start = fmt.Sprintf("%s.Start(ctx, rt, %s.Config{}, %s.Settings{})", module, module, module)
	}
	fmt.Printf(`
Next:
  - write tasks in %s
  - add Python dependencies with: uv add --project %s <package>
  - after changing tasks or models, run: gothon gen

Use it from Go:
  import %q

  rt := gothon.NewRuntime(gothon.Defaults{}) // one per program
  client, err := %s
  defer client.Close()
`, filepath.Join(m.Python, module+".py"), m.Python, goMod+"/"+filepath.ToSlash(rel), start)
	if !*withSettings && isNew {
		fmt.Printf(`
Needs API keys or other configuration later? Add a pydantic-settings class to the
module (uv add --project %s pydantic-settings) and run gothon gen. Go then gets a
typed %s.Settings struct, and Start takes it.
`, m.Python, module)
	}
	return nil
}

func runGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	check := fs.Bool("check", false, "fail if a generated client is out of date instead of writing it")
	fs.Parse(args)
	root, m, err := findProject()
	if err != nil {
		return err
	}
	python, err := venvPython(root, m)
	if err != nil {
		return err
	}
	modules := fs.Args()
	if len(modules) == 0 {
		modules = m.Modules
	}
	if len(modules) == 0 {
		return errors.New("gothon: no modules yet; add one with: gothon add <module>")
	}
	return generate(root, m, python, modules, *check)
}

func generate(root string, m *project.Manifest, python string, modules []string, check bool) error {
	rt, err := pyrt.InstallDefault()
	if err != nil {
		return err
	}
	env := append(os.Environ(), "PYTHONPATH="+rt+string(os.PathListSeparator)+m.PythonDir(root))
	var stale []string
	for _, module := range modules {
		e, err := schema.Run(context.Background(), python, env, module)
		if err != nil {
			return err
		}
		for _, w := range e.Warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
		src, err := codegen.Generate(e, codegen.Options{Module: module, Version: version()})
		if err != nil {
			return fmt.Errorf("gothon: %s: %w", module, err)
		}
		path := filepath.Join(m.OutDir(root, module), module+".go")
		rel, _ := filepath.Rel(root, path)
		if check {
			if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, src) {
				stale = append(stale, rel)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, src, 0o644); err != nil {
			return err
		}
		fmt.Println("generated", rel)
	}
	if len(stale) > 0 {
		return fmt.Errorf("gothon: out of date, run gothon gen: %s", strings.Join(stale, ", "))
	}
	return nil
}

func findProject() (string, *project.Manifest, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", nil, err
	}
	root, m, err := project.Find(wd)
	if errors.Is(err, project.ErrNotFound) {
		return "", nil, errors.New("gothon: no gothon.json here or above; run gothon init in your Go module root")
	}
	return root, m, err
}

func venvPython(root string, m *project.Manifest) (string, error) {
	if py := os.Getenv("GOTHON_PYTHON"); py != "" {
		return py, nil
	}
	py := m.VenvPython(root)
	if _, err := os.Stat(py); err != nil {
		return "", fmt.Errorf("gothon: no Python environment at %s; run gothon sync", filepath.Dir(filepath.Dir(py)))
	}
	return py, nil
}

func goModulePath(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", errors.New("gothon: no go.mod here; run gothon init in your Go module root")
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`), nil
		}
	}
	return "", errors.New("gothon: go.mod has no module line")
}

// writeNew writes a file unless it already exists, so user edits are never overwritten.
func writeNew(path, content string) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return true, err
}

func render(tmpl string, data any) string {
	var b strings.Builder
	template.Must(template.New("").Parse(tmpl)).Execute(&b, data)
	return b.String()
}

// version reports the gothon module version, whether this binary was built with
// `go install` (main module) or `go tool` (a dependency of the user's module).
func version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}
	const path = "github.com/ayush27prasad/gothon"
	if bi.Main.Path == path {
		return bi.Main.Version
	}
	for _, d := range bi.Deps {
		if d.Path == path {
			if d.Replace != nil {
				return "(devel)"
			}
			return d.Version
		}
	}
	return "(devel)"
}
