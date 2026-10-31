"""Python side of gothon: mark functions with @gothon.task so Go can call them."""

from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from ._registry import task
else:

    def task(*args: Any, **kwargs: Any) -> Any:
        # Imported lazily so the runtime can check the environment (Python version,
        # pydantic) and report problems before anything imports pydantic.
        from ._registry import task as register

        return register(*args, **kwargs)


__all__ = ["task"]
