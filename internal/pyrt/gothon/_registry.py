from __future__ import annotations

import inspect
import typing
from typing import Any, Callable, TypeVar, overload

from pydantic import TypeAdapter

F = TypeVar("F", bound=Callable[..., Any])

tasks: dict[str, Task] = {}


class Task:
    """A registered function with pydantic adapters for its input and output."""

    def __init__(self, fn: Callable[..., Any], name: str) -> None:
        params = list(inspect.signature(fn).parameters.values())
        if len(params) > 1:
            raise TypeError(f"task {name!r} must take at most one argument, got {len(params)}")
        hints = typing.get_type_hints(fn)
        self.fn = fn
        self.name = name
        self.input = TypeAdapter(hints.get(params[0].name, Any)) if params else None
        self.output = TypeAdapter(hints.get("return", Any))


@overload
def task(fn: F, /) -> F: ...
@overload
def task(*, name: str | None = None) -> Callable[[F], F]: ...


def task(fn: Any = None, /, *, name: str | None = None) -> Any:
    """Register a function so Go can call it. Use as @task or @task(name="...").

    The function takes at most one argument. Its type hints, usually pydantic
    models, validate the input from Go and the output sent back.
    """

    def register(f: F) -> F:
        n = name or f.__name__
        if n in tasks:
            raise ValueError(f"duplicate task name {n!r}")
        if not n or len(n.encode()) > 255:
            raise ValueError(f"task name must be 1-255 bytes, got {n!r}")
        tasks[n] = Task(f, n)
        return f

    return register(fn) if fn is not None else register
