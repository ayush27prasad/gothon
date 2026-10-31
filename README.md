# gothon

[![ci](https://github.com/ayush27prasad/gothon/actions/workflows/ci.yml/badge.svg)](https://github.com/ayush27prasad/gothon/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/ayush27prasad/gothon.svg)](https://pkg.go.dev/github.com/ayush27prasad/gothon)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Use Python libraries from your Go server, without running a Python server.

gothon runs your Python functions on a pool of warm worker processes that your Go
program starts and supervises. You write small Python modules with pydantic types,
gothon generates a typed Go client for each one, and your Go code calls Python
like any other dependency. There is no HTTP, no ports and no cgo.

> Status: early development (v0.x). APIs may change between minor versions.

## How it works

- **Warm workers.** Each Python module gets its own pool of processes. A worker
  imports the module once (load models and clients at module level) and then
  serves calls over pipes.
- **Go owns scheduling.** Calls wait for a free worker in arrival order. If a
  call's context ends while Python is still running, that worker is killed and
  replaced, so abandoned work never delays the next request. Crashed workers are
  replaced automatically.
- **Typed both ways.** Task inputs and outputs are pydantic models. `gothon gen`
  turns them into Go structs and a client. Each worker reports a hash of its task
  signatures at startup, and a client generated from older Python refuses to start.

## Requirements

- Go 1.23 or newer, on Linux or macOS
- [uv](https://docs.astral.sh/uv/), which also installs Python for you
- Python 3.10 or newer with pydantic 2.7 or newer (set up by `gothon init`)

## Quick start

From the root of your Go module:

```sh
go get -tool github.com/ayush27prasad/gothon/cmd/gothon   # Go 1.24+; or go install …/cmd/gothon@latest
go tool gothon init        # gothon.json, python/pyproject.toml, python/.venv via uv
go tool gothon add greeter # python/greeter.py and internal/py/greeter/greeter.go
```

`gothon add` creates a working example:

```python
# python/greeter.py
from pydantic import BaseModel, Field

import gothon


class GreetIn(BaseModel):
    name: str = Field(description="Who to greet.")


class GreetOut(BaseModel):
    message: str


@gothon.task
def greet(inp: GreetIn) -> GreetOut:
    """Return a greeting for the given name."""
    return GreetOut(message=f"Hello, {inp.name}!")
```

and generates a client you call from Go:

```go
import (
	"github.com/ayush27prasad/gothon"
	"example.com/yourapp/internal/py/greeter"
)

client, err := greeter.Start(ctx, gothon.Config{Workers: 4})
if err != nil {
	return err
}
defer client.Close()

out, err := client.Greet(ctx, greeter.GreetIn{Name: "gopher"})
```

Add Python dependencies with `uv add --project python <package>`. After changing
a task or model, run `go tool gothon gen`.

## Writing tasks

These conventions are what the generated code relies on. They are written so a
coding agent can follow them too.

- Mark each function Go calls with `@gothon.task` (or `@gothon.task(name="...")`).
- A task takes zero or one argument. Annotate the argument and the return type,
  ideally with pydantic models. They validate data in both directions and become
  the Go types.
- Put expensive setup (models, API clients) at module level. It runs once per
  worker, not once per call.
- `gothon gen` imports your module to read the types, with `GOTHON_GEN=1` set.
  Check it to skip heavy work such as loading model weights during generation.
- Read configuration through a pydantic-settings class, not `os.getenv` (see below).

### Type mapping

| Python | Go |
|---|---|
| `str`, `SecretStr`, URLs, `date`, `UUID` | `string` |
| `int` / `float` / `bool` | `int` / `float64` / `bool` |
| `datetime` | `time.Time` |
| `list[X]`, `set[X]` | `[]X` |
| `dict[str, X]` | `map[string]X` |
| `X \| None` | `*X` (slices and maps stay as they are) |
| pydantic model | struct, with field docs from `Field(description=...)` |
| `Enum` of strings or ints | named type with constants |
| `Literal["a", "b"]` | `string`, allowed values in a comment |
| `Any`, or no annotation | `any` |
| return type `None` | the method returns only `error` |

Fields with defaults in input models become pointers with `omitempty`. Leave one
nil and Python uses its default. Unions other than `X | None`, tuples and inline
objects are not supported; `gothon gen` fails and names the field.

## Settings and API keys

Libraries often need API keys or URLs. Declare them on a
[pydantic-settings](https://docs.pydantic.dev/latest/concepts/pydantic_settings/)
class in the module (`gothon add -settings <module>` scaffolds one, and
`gothon add` asks about it interactively):

```python
from pydantic import Field, SecretStr
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="BILLING_")

    api_key: SecretStr = Field(description="Payment provider key.")
    retries: int = 3


settings = Settings()
```

`gothon gen` generates a matching Go type. When a module has exactly one settings
class, `Start` requires it, so a caller can't forget to configure the module:

```go
client, err := billing.Start(ctx, gothon.Config{}, billing.Settings{
	APIKey: os.Getenv("STRIPE_KEY"), // from wherever your Go app keeps secrets
})
```

Each field is passed to the workers as the environment variable pydantic-settings
reads (prefixes and aliases included). Required settings that are missing stop
`Start` with a validation error naming the field. Generation never needs real
secrets: during `gothon gen`, missing required settings get placeholder values.
`gothon gen` also warns about direct `os.getenv` and `os.environ` reads.

Workers inherit the Go process's environment, so variables your Go app already has
are visible to Python as well.

## Errors

Every failure reported by Python is a `*gothon.PythonError`:

| `Stage` | Meaning |
|---|---|
| `import` | the module failed to import at startup, including missing settings |
| `input` | the input failed validation; `Fields` lists each failing field |
| `task` | the task raised; `Type`, `Message` and `Traceback` describe it |
| `output` | the return value did not match the declared type |
| `dispatch` | no task with that name |

Validation errors never echo input values, so user data does not end up in logs.
Other errors: `context.DeadlineExceeded` or `context.Canceled` when the call's
context ends; `gothon.ErrWorkerExited` when the worker died mid-call;
`gothon.ErrClosed` after `Close`; `gothon.ErrSchemaMismatch` from `Start` when the
Python tasks changed since the client was generated.

## Configuration

`gothon.Config` fields are all optional when you use a generated client:

| Field | Default |
|---|---|
| `Workers` | `runtime.GOMAXPROCS(0)`, which respects container CPU limits |
| `Python`, `Dir` | the project's `python/.venv` and `python/`, found through `gothon.json` |
| `Root` | `$GOTHON_ROOT`, else the nearest `gothon.json` at or above the working directory |
| `StartTimeout` | 60s per worker to import the module |
| `MaxFrame` | 64 MiB per response |
| `Env` | extra `KEY=VALUE` variables for the workers |
| `Logger` | `slog.Default()`; receives Python's stdout and stderr line by line |

The embedded Python runtime is unpacked into the user cache directory, falling
back to the temp directory. Set `GOTHON_CACHE_DIR` to choose the location.

## CLI

| Command | |
|---|---|
| `gothon init` | create `gothon.json` and a uv-managed Python project, then sync |
| `gothon sync` | create or update the Python environment |
| `gothon add [-settings] <module>` | scaffold a module and generate its client |
| `gothon gen [-check] [module...]` | regenerate clients; `-check` fails if any is stale (use in CI) |
| `gothon version` | print the gothon version |

`gothon.json` records where the Python project (`python`) and generated packages
(`out`) live, and which modules have clients.

## Deploying

Ship the Go binary next to the Python project and its environment. This
Dockerfile is tested with a read-only root filesystem and a non-root user:

```dockerfile
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api

FROM python:3.12-slim-bookworm
COPY --from=ghcr.io/astral-sh/uv:0.10.0 /uv /usr/local/bin/uv
WORKDIR /srv
COPY python/pyproject.toml python/uv.lock python/.python-version python/
RUN UV_PYTHON_DOWNLOADS=never uv sync --project python --frozen --no-dev
COPY gothon.json ./
COPY python/ python/
COPY --from=build /out/api /usr/local/bin/api
CMD ["api"]
```

Add `python/.venv` to `.dockerignore`. The final image provides the interpreter, so
its Python version must match `python/.python-version`. Run `gothon gen -check` in
CI so a stale client never ships.

## Without code generation

The pool works on its own with any JSON-compatible values:

```go
pool, err := gothon.Start(ctx, gothon.Config{Python: "python3", Dir: "py", Module: "greeter"})
var out struct{ Message string }
err = pool.Call(ctx, "greet", map[string]string{"name": "gopher"}, &out)

greet := gothon.NewTask[GreetIn, GreetOut](pool, "greet") // typed, hand-written
```

## Performance

Measured on an Apple M4 (`go test -bench .`), with pydantic validating every call
in both directions:

| | |
|---|---|
| Call overhead, one worker | 12.7 µs |
| Throughput, 8 workers | ~200k calls/s |
| 1 MB / 8 MB payload round trip | 2.1 ms / 17 ms |
| Start 8 workers | 90 ms (plus your module's import time) |

Expect higher numbers on typical cloud CPUs. Starting Python costs 10–150 ms
before your own imports, which is why workers stay warm.

## Development

```sh
uv venv --python 3.12 .venv && uv pip install --python .venv/bin/python -r testdata/requirements.txt
GOTHON_TEST_PYTHON=$PWD/.venv/bin/python GOTHON_TEST_CLI=1 go test -race ./...
go test ./internal/codegen -update   # after changing code generation
```

## License

gothon is released under the [MIT License](LICENSE). You may use, modify and
distribute it, including in commercial and closed-source projects, as long as the
copyright notice and license text are kept with it.

Copyright (c) 2026 Ayush Prasad
