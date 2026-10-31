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

REQUEST, READY, OK, ERROR = 1, 2, 3, 4
PROTOCOL = 1

_header = struct.Struct(">IB")
_ValidationError: Any = None


def _error(stage: str, e: BaseException, skip: int = 0) -> bytes:
    """Encode an exception for Go. skip drops runtime frames from the traceback."""
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
        for _ in range(skip):
            if tb is not None:
                tb = tb.tb_next
        out["traceback"] = "".join(traceback.format_exception(type(e), e, tb))
    return json.dumps(out).encode()


def _call(t: Any, body: bytes) -> tuple[int, bytes]:
    if t.input is not None:
        try:
            arg = t.input.validate_json(body)
        except _ValidationError as e:
            return ERROR, _error("input", e)
    try:
        result = t.fn(arg) if t.input is not None else t.fn()
    except Exception as e:
        return ERROR, _error("task", e, skip=1)
    try:
        return OK, t.output.dump_json(result, warnings="error")
    except Exception as e:
        return ERROR, _error("output", e, skip=1)


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
        importlib.import_module(module)
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

            info["schema_hash"] = export(tasks)["hash"]
        except Exception as e:
            send(ERROR, _error("import", e))
            return 1
    send(READY, json.dumps(info).encode())

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
        send(*_call(t, frame[1 + n :]))


if __name__ == "__main__":
    sys.exit(main())
