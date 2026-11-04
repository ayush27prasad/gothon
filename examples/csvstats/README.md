# csvstats

A Go HTTP service that describes CSV files with [pandas](https://pandas.pydata.org).
Go serves HTTP; gothon keeps four warm Python workers with pandas already imported.

```
examples/csvstats
├── main.go                   HTTP server: POST /describe, GET /healthz
├── gothon.json               where the Python project and generated code live
├── python/
│   ├── stats.py              the task, with pydantic models for its input and output
│   ├── pyproject.toml        Python dependencies, managed by uv
│   └── uv.lock
└── internal/py/stats/        generated Go client: gothon gen
```

## Run it

Needs Go and [uv](https://docs.astral.sh/uv/).

```sh
cd examples/csvstats
uv sync --project python   # create python/.venv with pandas and pydantic
go run .
```

```sh
printf 'city,temp\nPune,31.5\nDelhi,38.5\nPune,29\n' |
  curl --data-binary @- localhost:8080/describe
```

```json
{"rows":3,"columns":[
  {"name":"city","dtype":"str","missing":0,"distinct":2,"numeric":null,
   "top":[{"value":"Pune","count":2},{"value":"Delhi","count":1}]},
  {"name":"temp","dtype":"float64","missing":0,"distinct":3,
   "numeric":{"mean":33,"std":4.92,"min":29,"p50":31.5,"max":38.5},"top":[]}]}
```

A malformed CSV returns 400 with pandas' message, a full queue returns 503, and
`GET /healthz` returns the pool's `Stats`.

## How it was made

```sh
gothon init           # gothon.json and a uv project in python/
gothon add stats      # python/stats.py and its generated client
uv add --project python pandas
# write the task in python/stats.py, then:
gothon gen
```

Here `gothon` runs from this repository as
`go run github.com/ayush27prasad/gothon/cmd/gothon`.
