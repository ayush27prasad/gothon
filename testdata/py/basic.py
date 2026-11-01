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
