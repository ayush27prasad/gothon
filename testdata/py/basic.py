import asyncio
import os
import time

from pydantic import BaseModel, Field

import gothon


class AddIn(BaseModel):
    a: int
    b: int = Field(ge=0)


class AddOut(BaseModel):
    sum: int


@gothon.task
def add(inp: AddIn) -> AddOut:
    return AddOut(sum=inp.a + inp.b)


@gothon.task
def echo(x):
    return x


@gothon.task
def spin(ms: int) -> int:
    end = time.perf_counter() + ms / 1000
    while time.perf_counter() < end:
        pass
    return os.getpid()


@gothon.task
def pid() -> int:
    return os.getpid()


@gothon.task
def fail(msg: str) -> None:
    raise ValueError(msg)


@gothon.task
def bad_output() -> AddOut:
    return {"sum": "not a number"}


@gothon.task
def crash() -> None:
    os._exit(1)


@gothon.task(name="talk")
def print_and_log() -> str:
    print("hello from stdout")
    return "ok"


@gothon.task
def getenv(name: str) -> str | None:
    return os.environ.get(name)


@gothon.task
async def async_double(x: int) -> int:
    await asyncio.sleep(0.01)
    return x * 2


@gothon.task
async def async_fail() -> None:
    await asyncio.sleep(0)
    raise RuntimeError("async boom")


@gothon.task
async def loop_id() -> int:
    return id(asyncio.get_running_loop())
