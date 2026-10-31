"""Exports task signatures as JSON Schema for `gothon gen`.

Run as `python -m gothon._schema <module>`; prints JSON on stdout.
"""

from __future__ import annotations

import ast
import hashlib
import importlib
import inspect
import json
import os
import sys
import typing
from typing import Any

REF = "#/$defs/{model}"
_DOC_KEYS = {"title", "description", "examples", "doc"}
_NAME_MAPS = {"properties", "$defs", "defs", "patternProperties"}


def export(tasks: dict[str, Any], module: Any) -> dict[str, Any]:
    """Return every task's input and output schema and the module's settings
    classes, with named types collected in defs."""
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
    settings = _settings(module, defs)
    out = {"tasks": entries, "settings": settings, "defs": dict(sorted(defs.items()))}
    out["hash"] = digest(out)
    return out


def digest(export: dict[str, Any]) -> str:
    """Hash the shape of the signatures. Docs and titles are left out, so editing a
    docstring does not force regenerating Go code."""
    shape = _strip({k: export[k] for k in ("tasks", "settings", "defs")})
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


def _settings(module: Any, defs: dict[str, Any]) -> list[dict[str, Any]]:
    """Describe the pydantic-settings classes defined in module, with the exact
    environment variable each field is read from."""
    try:
        from pydantic_settings import BaseSettings
    except ImportError:
        return []
    classes = [
        v
        for v in vars(module).values()
        if isinstance(v, type) and issubclass(v, BaseSettings) and v.__module__ == module.__name__
    ]
    out = []
    for cls in sorted(classes, key=lambda c: c.__name__):
        cfg = cls.model_config
        prefix = cfg.get("env_prefix") or ""
        schema = cls.model_json_schema(ref_template=REF)
        for name, d in schema.pop("$defs", {}).items():
            _merge(defs, name, d)
        props = schema.get("properties", {})
        fields = []
        for name, f in cls.model_fields.items():
            alias = f.validation_alias if isinstance(f.validation_alias, str) else f.alias
            env = alias or prefix + name  # pydantic-settings ignores the prefix for aliases
            fields.append(
                {
                    "name": name,
                    "env": env if cfg.get("case_sensitive") else env.upper(),
                    "required": f.is_required(),
                    "schema": props.get(alias or name) or props.get(name) or {},
                }
            )
        out.append({"name": cls.__name__, "doc": inspect.cleandoc(cls.__dict__.get("__doc__") or ""), "fields": fields})
    return out


def env_reads(module: Any) -> list[str]:
    """Find direct environment reads (os.getenv, os.environ), which Go cannot see
    or set through generated code."""
    try:
        path = inspect.getsourcefile(module)
        tree = ast.parse(open(path).read())
    except (TypeError, OSError, SyntaxError):
        return []

    def is_(node: ast.AST, attr: str) -> bool:
        return (isinstance(node, ast.Name) and node.id == attr) or (
            isinstance(node, ast.Attribute) and node.attr == attr and isinstance(node.value, ast.Name) and node.value.id == "os"
        )

    def literal(node: Any) -> str:
        return repr(node.value) if isinstance(node, ast.Constant) and isinstance(node.value, str) else "a variable"

    found = []
    for node in ast.walk(tree):
        name = None
        if isinstance(node, ast.Call):
            f = node.func
            if is_(f, "getenv") or (isinstance(f, ast.Attribute) and f.attr == "get" and is_(f.value, "environ")):
                name = literal(node.args[0]) if node.args else "a variable"
        elif isinstance(node, ast.Subscript) and is_(node.value, "environ"):
            name = literal(node.slice)
        if name:
            found.append(
                f"{os.path.basename(path)}:{node.lineno}: reads {name} from the environment directly; "
                "declare it on a pydantic-settings class so Go sets it through the generated Settings type"
            )
    return found


def _merge(defs: dict[str, Any], name: str, schema: dict[str, Any]) -> None:
    if name in defs and defs[name] != schema:
        raise ValueError(f"two different types are named {name!r}; rename one so Go can tell them apart")
    defs[name] = schema


def _strip(v: Any, names: bool = False) -> Any:
    """Drop documentation keywords. Keys of name maps (properties, defs) are field
    and type names, which are kept even when they are called "title" or "doc"."""
    if isinstance(v, dict):
        if names:
            return {k: _strip(x) for k, x in v.items()}
        return {k: _strip(x, k in _NAME_MAPS) for k, x in v.items() if k not in _DOC_KEYS}
    if isinstance(v, list):
        return [_strip(x) for x in v]
    return v


_PLACEHOLDER = "https://gothon-gen.invalid/placeholder"  # valid as str, SecretStr and URL types


def _allow_missing_settings() -> None:
    """Let `Settings()` at module level succeed during generation without secrets:
    required settings that are missing get placeholder values. Only missing values
    are filled in; invalid ones still fail."""
    try:
        from pydantic import ValidationError
        from pydantic_settings import BaseSettings
    except ImportError:
        return
    original = BaseSettings.__init__

    def init(self: Any, *args: Any, **kwargs: Any) -> None:
        try:
            original(self, *args, **kwargs)
        except ValidationError as e:
            errors = e.errors()
            if not errors or any(err["type"] != "missing" or not err["loc"] for err in errors):
                raise
            by_key = {(f.alias or n): f for n, f in type(self).model_fields.items()}
            filled = dict(kwargs)
            for err in errors:
                key = str(err["loc"][0])
                ann = by_key[key].annotation if key in by_key else str
                origin = typing.get_origin(ann) or ann
                filled[key] = (
                    [] if origin in (list, set, frozenset, tuple)
                    else {} if origin is dict
                    else False if ann is bool
                    else 0 if ann in (int, float)
                    else _PLACEHOLDER
                )
            original(self, *args, **filled)

    BaseSettings.__init__ = init  # type: ignore[method-assign]


def main() -> int:
    out = os.fdopen(os.dup(1), "w")
    os.dup2(2, 1)  # anything the module prints goes to stderr, not into the JSON
    sys.stdout = sys.stderr
    os.environ["GOTHON_GEN"] = "1"  # lets modules skip heavy work during generation
    _allow_missing_settings()

    from . import _registry

    module = importlib.import_module(sys.argv[1])
    result = export(_registry.tasks, module)
    result["warnings"] = env_reads(module)
    json.dump(result, out, indent=2)
    out.write("\n")
    out.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main())
