# Changelog

gothon follows [Semantic Versioning](https://semver.org). Releases are git tags
(`vX.Y.Z`). Until 1.0, a minor version may contain breaking changes.

After upgrading gothon, run `gothon gen`: generated clients record the version that
produced them, and a stale client refuses to start.

## Unreleased

### Added

- Worker pool of warm Python processes over pipes: first-come-first-served
  dispatch, kill-and-replace when a call's context ends, automatic replacement of
  crashed workers, parallel startup.
- `@gothon.task` with pydantic validation of inputs and outputs, and `PythonError`
  with the failing stage, exception type, traceback and field errors.
- `gothon` CLI built on uv: `init`, `sync`, `add`, `gen`, `version`.
- Typed Go clients generated from pydantic models, with a `Tasks` interface for
  fakes and a schema hash that stops a stale client from starting.
- pydantic-settings support: a generated `Settings` type that `Start` requires, and
  warnings for direct environment reads.
- Startup checks for the Python version, pydantic and the project environment.
- `Runtime` with three configuration layers (runtime, module, task), typed
  per-task settings in generated clients, and a process budget across modules.
- Per-call `Timeout`, `MaxQueue` with `ErrBusy`, and rejection of calls that
  cannot finish before their deadline.
- `MaxConcurrency` per task, `OnTimeout: Drain` with `DrainTimeout`,
  `ThreadsPerWorker`, and worker recycling by `MaxJobs` and `MaxMemory`.
- Graceful `Shutdown` for a pool or the whole runtime.
- `Stats` for pools and tasks, and `Hooks` for call and worker events.
- `async def` tasks, run on a persistent event loop per worker.
