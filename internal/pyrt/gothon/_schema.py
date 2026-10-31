"""Exports task signatures as JSON Schema for `gothon gen`.

Run as `python -m gothon._schema <module>`; prints JSON on stdout.
"""

from __future__ import annotations

import hashlib
import importlib
import inspect
import json
import os
import sys
from typing import Any

REF = "#/$defs/{model}"
_DOC_KEYS = {"title", "description", "examples"}


def export(tasks: dict[str, Any]) -> dict[str, Any]:
    """Return every task's input and output schema, with named types collected in defs."""
    defs: dict[str, Any] = {}
    entries = []
    for name in sorted(tasks):
        t = tasks[name]
        entries.append(
            {
                "name": name,
                "doc": inspect.getdoc(t.fn) or "",
                "input": _schema(t.input, defs) if t.input is not None else None,
                "output": _schema(t.output, defs),
            }
        )
    out = {"tasks": entries, "defs": dict(sorted(defs.items()))}
    out["hash"] = digest(out)
    return out


def digest(export: dict[str, Any]) -> str:
    """Hash the shape of the signatures. Docs and titles are left out, so editing a
    docstring does not force regenerating Go code."""
    shape = _strip({"tasks": export["tasks"], "defs": export["defs"]})
    for t in shape["tasks"]:
        t.pop("doc", None)
    canonical = json.dumps(shape, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(canonical.encode()).hexdigest()[:16]


def _schema(adapter: Any, defs: dict[str, Any]) -> dict[str, Any]:
    s = adapter.json_schema(ref_template=REF)
    for name, d in s.pop("$defs", {}).items():
        _merge(defs, name, d)
    if s.get("type") == "object" and "properties" in s and "title" in s:
        # A model at the top level: store it with the others and refer to it by name.
        _merge(defs, s["title"], s)
        return {"$ref": REF.format(model=s["title"])}
    return s


def _merge(defs: dict[str, Any], name: str, schema: dict[str, Any]) -> None:
    if name in defs and defs[name] != schema:
        raise ValueError(f"two different types are named {name!r}; rename one so Go can tell them apart")
    defs[name] = schema


def _strip(v: Any) -> Any:
    if isinstance(v, dict):
        return {k: _strip(x) for k, x in v.items() if k not in _DOC_KEYS}
    if isinstance(v, list):
        return [_strip(x) for x in v]
    return v


def main() -> int:
    out = os.fdopen(os.dup(1), "w")
    os.dup2(2, 1)  # anything the module prints goes to stderr, not into the JSON
    sys.stdout = sys.stderr

    from . import _registry

    importlib.import_module(sys.argv[1])
    json.dump(export(_registry.tasks), out, indent=2)
    out.write("\n")
    out.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main())
