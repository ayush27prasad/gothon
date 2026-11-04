"""Summary statistics for CSV files, with pandas. Called from Go through the
generated client in internal/py/stats.

Conventions:
- Mark each function Go calls with @gothon.task.
- A task takes at most one argument. Annotate it and the return type, ideally
  with pydantic models: they validate the data and become the Go types.
- Load expensive things (models, clients) at module level. That runs once per
  worker process, not once per call.
- After changing a task or model, run `gothon gen` to update the Go client.
"""

import io
import math

import pandas as pd
from pydantic import BaseModel, Field

import gothon


class DescribeIn(BaseModel):
    csv: str = Field(description="CSV text with a header row.", max_length=10_000_000)
    top: int = Field(default=3, ge=1, le=20, description="Most common values to list for text columns.")


class Numeric(BaseModel):
    mean: float | None
    std: float | None
    min: float
    p50: float
    max: float


class Count(BaseModel):
    value: str
    count: int


class Column(BaseModel):
    name: str
    dtype: str
    missing: int
    distinct: int
    numeric: Numeric | None = Field(description="Set for numeric columns.")
    top: list[Count] = Field(description="Most common values, for text columns.")


class DescribeOut(BaseModel):
    rows: int
    columns: list[Column]


def _num(x: float) -> float | None:
    return None if math.isnan(x) else float(x)


@gothon.task
def describe(inp: DescribeIn) -> DescribeOut:
    """Describe each column of a CSV: type, missing values, and its distribution."""
    try:
        df = pd.read_csv(io.StringIO(inp.csv))
    except (pd.errors.ParserError, pd.errors.EmptyDataError) as e:
        raise ValueError(f"not a valid CSV: {e}") from None

    columns = []
    for name in df.columns:
        s = df[name]
        col = Column(
            name=str(name),
            dtype=str(s.dtype),
            missing=int(s.isna().sum()),
            distinct=int(s.nunique()),
            numeric=None,
            top=[],
        )
        if pd.api.types.is_numeric_dtype(s) and s.notna().any():
            col.numeric = Numeric(
                mean=_num(s.mean()),
                std=_num(s.std()),
                min=float(s.min()),
                p50=float(s.median()),
                max=float(s.max()),
            )
        else:
            counts = s.astype("string").value_counts().head(inp.top)
            col.top = [Count(value=str(v), count=int(c)) for v, c in counts.items()]
        columns.append(col)
    return DescribeOut(rows=len(df), columns=columns)
