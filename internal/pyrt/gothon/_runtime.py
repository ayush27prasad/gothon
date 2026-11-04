"""Worker loop. Go runs this as `python -m gothon._runtime <module>`.

Requests arrive on fd 3 and replies go out on fd 4, so stdout and stderr stay
free for the module's own prints and logs. When Go closes fd 3, the worker exits.
"""

from __future__ import annotations

import importlib
import json
import os
import struct
import sys
import traceback
from typing import Any

REQUEST, READY, OK, ERROR, RETIRE = 1, 2, 3, 4, 5
PROTOCOL = 2

_header = struct.Struct(">IB")
_ValidationError: Any = None
_loop: Any = None
_here = os.path.dirname(os.path.abspath(__file__))


def _internal(filename: str) -> bool:
    return (
        filename.startswith("<frozen")
        or os.path.dirname(os.path.abspath(filename)) == _here
        or f"{os.sep}asyncio{os.sep}" in filename
        or f"{os.sep}importlib{os.sep}" in filename
    )


def _run(coro: Any) -> Any:
    """Run a coroutine on the worker's event loop, created on first use."""
    global _loop
    if _loop is None:
        import asyncio

        _loop = asyncio.new_event_loop()
        asyncio.set_event_loop(_loop)
    return _loop.run_until_complete(coro)


def _error(stage: str, e: BaseException) -> bytes:
    """Encode an exception for Go, with the traceback starting at the user's code."""
    out: dict[str, Any] = {"stage": stage, "type": type(e).__name__, "message": str(e)}
    if _ValidationError is not None and isinstance(e, _ValidationError):
        # No traceback or input values: the caller only needs which fields failed.
        fields = json.loads(e.json(include_url=False, include_input=False))
        out["fields"] = fields
        out["message"] = "; ".join(
            f"{'.'.join(map(str, f['loc'])) or 'input'}: {f['msg']}" for f in fields
        )
    else:
        tb = e.__traceback__
        while tb is not None and tb.tb_next is not None and _internal(tb.tb_frame.f_code.co_filename):
            tb = tb.tb_next
        out["traceback"] = "".join(traceback.format_exception(type(e), e, tb))
    return json.dumps(out).encode()


def _rss_reader() -> Any:
    """Return a function giving this process's resident memory in bytes."""
    try:
        fd = os.open("/proc/self/statm", os.O_RDONLY)
        page = os.sysconf("SC_PAGE_SIZE")
        return lambda: int(os.pread(fd, 64, 0).split()[1]) * page
    except OSError:  # no /proc, e.g. macOS: use the peak instead
        import resource

        scale = 1 if sys.platform == "darwin" else 1024
        return lambda: resource.getrusage(resource.RUSAGE_SELF).ru_maxrss * scale


def _call(t: Any, body: bytes) -> tuple[int, bytes]:
    if t.input is not None:
        try:
            arg = t.input.validate_json(body)
        except _ValidationError as e:
            return ERROR, _error("input", e)
    try:
        result = t.fn(arg) if t.input is not None else t.fn()
        if t.is_async:
            result = _run(result)
    except Exception as e:
        return ERROR, _error("task", e)
    try:
        return OK, t.output.dump_json(result, warnings="error")
    except Exception as e:
        return ERROR, _error("output", e)


def main() -> int:
    global _ValidationError
    rin = os.fdopen(3, "rb")
    wout = os.fdopen(4, "wb")

    def send(kind: int, payload: bytes) -> None:
        wout.write(_header.pack(len(payload) + 1, kind))
        wout.write(payload)
        wout.flush()

    module = sys.argv[1]
    try:
        if sys.version_info < (3, 10):
            raise RuntimeError(f"gothon needs Python 3.10 or newer, {sys.executable} is {sys.version.split()[0]}")
        try:
            from pydantic import ValidationError
        except ImportError:
            raise RuntimeError(
                f"pydantic is not installed for {sys.executable}; "
                "add pydantic>=2.7 to the Python project (gothon init does this)"
            ) from None

        from . import _registry

        _ValidationError = ValidationError
        mod = importlib.import_module(module)
        if not _registry.tasks:
            raise RuntimeError(f"module {module!r} defines no @gothon.task functions")
    except BaseException as e:  # report every startup failure to Go before exiting
        send(ERROR, _error("import", e))
        return 1

    tasks = _registry.tasks
    info: dict[str, Any] = {"protocol": PROTOCOL, "pid": os.getpid(), "tasks": sorted(tasks)}
    if os.environ.get("GOTHON_SCHEMA_HASH"):
        try:
            from ._schema import export

            info["schema_hash"] = export(tasks, mod)["hash"]
        except Exception as e:
            send(ERROR, _error("import", e))
            return 1
    send(READY, json.dumps(info).encode())

    limit = int(os.environ.get("GOTHON_MAX_MEMORY") or 0)
    rss = _rss_reader() if limit > 0 else None

    while True:
        head = rin.read(5)
        if len(head) < 5:
            return 0  # Go closed the pipe
        size, kind = _header.unpack(head)
        frame = rin.read(size - 1)
        if kind != REQUEST or len(frame) < size - 1:
            return 2
        n = frame[0]
        name = frame[1 : 1 + n].decode()
        t = tasks.get(name)
        if t is None:
            err = {"stage": "dispatch", "type": "TaskNotFound", "message": f"no task named {name!r}"}
            send(ERROR, json.dumps(err).encode())
            continue
        reply = _call(t, frame[1 + n :])
        if rss is not None and (used := rss()) > limit:
            # Go retires this worker once it has the reply.
            send(RETIRE, json.dumps({"rss": used, "limit": limit}).encode())
        send(*reply)


if __name__ == "__main__":
    sys.exit(main())
