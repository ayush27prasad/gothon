"""Fixture covering the types `gothon gen` supports."""

from datetime import datetime
from enum import Enum
from typing import Any, Literal, Optional

from pydantic import BaseModel, Field

import gothon


class Color(str, Enum):
    RED = "red"
    GREEN = "green"


class Item(BaseModel):
    """A line item."""

    sku: str = Field(description="Stock keeping unit.")
    qty: int = 1
    price: float
    tags: list[str] = []
    color: Color | None = None


class Order(BaseModel):
    order_id: str
    items: list[Item]
    meta: dict[str, int] = {}
    note: Optional[str] = None
    placed_at: datetime
    status: Literal["new", "paid"] = "new"
    extra: Any = None


class Summary(BaseModel):
    order_id: str
    total: float
    item_count: int
    colors: list[Color]
    first_item: Item | None
    placed_at: datetime
    url: str


@gothon.task
def summarize(order: Order) -> Summary:
    """Total up an order."""
    return Summary(
        order_id=order.order_id,
        total=sum(i.price * i.qty for i in order.items),
        item_count=len(order.items),
        colors=sorted({i.color for i in order.items if i.color}, key=lambda c: c.value),
        first_item=order.items[0] if order.items else None,
        placed_at=order.placed_at,
        url=f"https://example.com/orders/{order.order_id}",
    )


@gothon.task
def double(x: int) -> int:
    return x * 2


@gothon.task
def names() -> list[str]:
    return ["a", "b"]


@gothon.task
def touch(order_id: str) -> None:
    return None


@gothon.task
def echo_any(x):
    return x
