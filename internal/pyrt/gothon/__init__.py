"""Python side of gothon: mark functions with @gothon.task so Go can call them."""

from ._registry import task

__all__ = ["task"]
