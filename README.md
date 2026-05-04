```
░██████╗██╗░░░░░░█████╗░░██████╗░██╗░░██╗
██╔════╝██║░░░░░██╔══██╗██╔════╝░╚██╗██╔╝
╚█████╗░██║░░░░░██║░░██║██║░░██╗░░╚███╔╝░
░╚═══██╗██║░░░░░██║░░██║██║░░╚██╗░██╔██╗░
██████╔╝███████╗╚█████╔╝╚██████╔╝██╔╝╚██╗
╚═════╝░╚══════╝░╚════╝░░╚═════╝░╚═╝░░╚═╝
```
<div align="center">

[![Go Reference](https://pkg.go.dev/badge/ella.to/slogx.svg)](https://pkg.go.dev/ella.to/slogx)
[![Go Report Card](https://goreportcard.com/badge/ella.to/slogx)](https://goreportcard.com/report/ella.to/slogx)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
q
`slogx` is a small extension on top of Go's standard `log/slog` package that adds:

- **Hierarchical tracing** via a single call at the top of any function
  (`ctx = slogx.Context(ctx)`). Every log record emitted through the default
  `slog` logger is automatically decorated with `_traceId`, `_parentId`, and
  `_spanPath` attributes.
- **Per-package log levels** (plus a global level), configurable at startup
  and changeable live at runtime.
- **A pluggable `Store`** (default: in-memory ring buffer) that retains
  records for later inspection.
- **An HTTP admin handler** exposing a REST API and a single-page web UI that
  shows logs in a live, collapsible span tree.

You still write `slog.InfoContext(ctx, ...)` / `slog.DebugContext(...)` just
like you always have. slogx only requires two touch points:

1. `slogx.Setup(...)` once at program start.
2. `ctx = slogx.Context(ctx)` on the first line of any function that accepts
   a `context.Context`.

## Install

```bash
go get ella.to/slogx@v0.1.0
```

Requires Go 1.22+ (uses method-based mux patterns and generics in std).

## Quick start

```go
package main

import (
    "context"
    "log/slog"
    "net/http"

    "ella.to/slogx"
)

func main() {
    slogx.Setup(
        slogx.GlobalLevel(slog.LevelInfo),
        slogx.PackageLevel("ella.to/app/api", slog.LevelDebug), // chatty only here
        slogx.PackageLevel("example.com/noisy", slogx.LevelOff), // silenced
    )

    mux := http.NewServeMux()
    mux.HandleFunc("GET /hello", hello)
    mux.Handle("/_slogx/", http.StripPrefix("/_slogx", slogx.HttpHandler()))

    http.ListenAndServe(":8080", slogx.Middleware(mux))
}

func hello(w http.ResponseWriter, r *http.Request) {
    ctx := slogx.Context(r.Context())
    greet(ctx, "world")
    w.Write([]byte("ok"))
}

func greet(ctx context.Context, who string) {
    ctx = slogx.Context(ctx)
    slog.InfoContext(ctx, "greeting", "who", who)
}
```

Then visit `http://localhost:8080/_slogx/` for the debugging UI.

A full runnable example lives in [`example/`](example/main.go).

## Context & hierarchy

Every `slogx.Context(ctx)` call:

1. Generates a new span id.
2. On the first call for a trace, also generates a fresh trace id.
3. Appends the span id to a slash-joined ancestor chain.

Every record emitted through the default `slog` logger is enriched with:

| Attribute     | Description                                                              |
|---------------|--------------------------------------------------------------------------|
| `_traceId`    | Stable id shared across the entire trace.                                |
| `_parentId`   | Innermost span id (the scope that emitted the log).                      |
| `_spanPath`   | Slash-joined chain of ancestor span ids.                                 |
| `_goroutine`  | `true` if the record was emitted from a span that crossed a `go` boundary (sticky for all descendants). |

The UI infers the span tree entirely from `_spanPath` - no synthetic
"span-start" marker log is ever emitted.

### Goroutines

No new API is needed. The canonical rule still holds -- call
`ctx = slogx.Context(ctx)` as the first line of the goroutine:

```go
go func() {
    ctx := slogx.Context(ctx)
    slog.InfoContext(ctx, "worker started")
}()
```

`slogx.Context` records the goroutine id at span creation. If the parent
span was born on a different goroutine, the new span is flagged as
concurrent; every record emitted from it (or any further descendant span)
carries `_goroutine: true` and shows a green "go" chip in the UI. The flag
is sticky, so nested calls inside the goroutine are also clearly marked.

## HTTP middleware

`slogx.Middleware(next)` establishes a root trace + span per inbound request.
Trace id resolution order:

1. `?log_trace_id=...` query parameter (override),
2. `X-TRACE-ID` HTTP header (propagation),
3. newly generated if neither is present.

The resolved id is echoed back on the response `X-TRACE-ID` header so clients
can correlate.

## Admin API

`slogx.HttpHandler()` returns a `http.Handler` with:

- `GET /`                 - single-page UI (embedded).
- `GET /traces?limit=100` - most-recent trace summaries.
- `GET /logs?traceId=...` - filtered records for a trace.
- `GET /levels`           - `{ "global": "INFO", "packages": [{ "package": "...", "level": "..." }] }`
- `PATCH /levels`         - `{"global"?:"DEBUG","set"?:{"pkg":"DEBUG"},"unset"?:["pkg"]}`

Mount it wherever you like (put it behind your own auth in production):

```go
mux.Handle("/_slogx/", http.StripPrefix("/_slogx", slogx.HttpHandler()))
```

## Levels

slogx filters by **per-package log level** on top of a **global** default:

- `GlobalLevel(level)` sets the default minimum level for packages that have
  no explicit override.
- `PackageLevel(pattern, level)` overrides the level for a package (longest
  matching prefix wins).
- Valid levels are `slog.LevelDebug`, `slog.LevelInfo`, `slog.LevelWarn`,
  `slog.LevelError`, and `slogx.LevelOff` (silences a scope entirely).
- Matching is **prefix-based on Go import paths**: `ella.to/app` also covers
  `ella.to/app/api`.

Common recipes:

```go
// Only show warnings+ by default, but debug for one hot package:
slogx.Setup(
    slogx.GlobalLevel(slog.LevelWarn),
    slogx.PackageLevel("ella.to/app/api", slog.LevelDebug),
)

// Opt-in mode: everything off except a handful of packages:
slogx.Setup(
    slogx.GlobalLevel(slogx.LevelOff),
    slogx.PackageLevel("ella.to/app",   slog.LevelInfo),
    slogx.PackageLevel("ella.to/app/db", slog.LevelDebug),
)

// Silence noise while keeping a normal global default:
slogx.Setup(
    slogx.PackageLevel("example.com/noisy", slogx.LevelOff),
)
```

Levels can also be edited live from the UI's **Levels** tab, or via `PATCH
/levels`. Changes take effect immediately for subsequent records.

> **Gotcha:** The Go runtime reports `package main` as just `"main"`, not
> the module path of the binary. If your app is `ella.to/myapp` with
> `package main`, address it as `"main"` in `PackageLevel`.

## Setup options

| Option | Purpose | Default |
|---|---|---|
| `GlobalLevel(l)` / `Level(l)` | Default min level for packages with no override | `slog.LevelInfo` |
| `PackageLevel(pattern, l)` | Per-package (prefix) min level override | (none) |
| `Output(w)` | Where the stdout-style sink writes | `os.Stdout` |
| `WithFormat(FormatJSON\|FormatText)` | Sink encoding | `FormatJSON` |
| `AddSource(bool)` | Include source info in sink output | `true` |
| `WithStore(s)` | Custom `Store` implementation | in-memory ring |
| `RingBufferSize(n)` | Capacity of default ring store | `10_000` |

## Custom store

Implement `slogx.Store` and pass it to `Setup(slogx.WithStore(mine))`:

```go
type Store interface {
    Append(r Record)
    Query(q Query) []Record
    Traces(limit int) []TraceSummary
}
```

## ella.to/jsonrpc integration

`jsonrpc.NewSlogxPropagator()` (defined in `ella.to/jsonrpc`) returns a
`*SlogxPropagator` that implements `ContextPropagator`. Since
`ella.to/jsonrpc` already imports `ella.to/slogx`, the propagator lives in
the jsonrpc package where `Extract` and `Inject` naturally belong.

Pass it to `jsonrpc.WithContextPropagation` on **both** the client and the
server. The propagator transparently carries the current slogx trace-id
across the call boundary so that logs emitted inside an RPC handler appear as
child spans of the client's trace in the slogx UI.

### How it works

| Transport | Client sends                                    | Server reads                                    |
|-----------|-------------------------------------------------|-------------------------------------------------|
| HTTP      | `X-Rpc-Meta-_traceId` request header            | Same header, stripped by jsonrpc middleware      |
| Raw       | `{"requests":[…],"metadata":{"_traceId":"…"}}` | Metadata envelope unwrapped by jsonrpc server   |

`Inject` (server side) restores the original trace-id and opens a new slogx
span via `slogx.Context(ctx)`, so every log record from the handler inherits
the client trace but gets its own span label.

### HTTP example

```go
// ---- server ----
package main

import (
    "context"
    "log/slog"
    "net/http"

    "ella.to/jsonrpc"
    "ella.to/slogx"
)

type handler struct{}

func (h *handler) Handle(ctx context.Context, req *jsonrpc.Request) *jsonrpc.Response {
    ctx = slogx.Context(ctx) // optional extra span inside the handler
    slog.InfoContext(ctx, "handling RPC", "method", req.Method)
    return req.CreateResponse("ok")
}

func main() {
    slogx.Setup()

    mux := http.NewServeMux()
    mux.Handle("/_slogx/", http.StripPrefix("/_slogx", slogx.HttpHandler()))
    mux.Handle("/rpc", jsonrpc.NewHTTPHandler(
        &handler{},
        jsonrpc.WithContextPropagation(jsonrpc.NewSlogxPropagator()),
    ))

    http.ListenAndServe(":8080", slogx.Middleware(mux))
}

// ---- client ----
func callServer(ctx context.Context) {
    ctx = slogx.Context(ctx) // establishes (or continues) a trace
    slog.InfoContext(ctx, "about to call RPC")

    client := jsonrpc.NewHTTPClient("http://localhost:8080/rpc",
        jsonrpc.WithContextPropagation(jsonrpc.NewSlogxPropagator()),
    )

    req := &jsonrpc.Request{Method: "hello"}
    client.Call(ctx, req)

    slog.InfoContext(ctx, "RPC done")
    // All three log records (before call, inside handler, after call)
    // share the same trace-id and appear together in the slogx UI.
}
```

### Raw (peer-to-peer) example

```go
// ---- server ----
func serveRaw(conn io.ReadWriteCloser) {
    server := jsonrpc.NewRawServer(conn, &handler{},
        jsonrpc.WithContextPropagation(jsonrpc.NewSlogxPropagator()),
    )
    server.Serve(context.Background())
}

// ---- client ----
func callRaw(ctx context.Context, conn io.ReadWriteCloser) {
    ctx = slogx.Context(ctx)

    client := jsonrpc.NewRawClient(conn,
        jsonrpc.WithContextPropagation(jsonrpc.NewSlogxPropagator()),
    )
    defer client.Close()

    req := &jsonrpc.Request{Method: "hello"}
    client.Call(ctx, req)
}
```

## Notes / out of scope

- No W3C `traceparent` propagation (only `X-TRACE-ID`).
- No built-in persistence; plug in your own `Store` for SQLite, etc.
- No authentication on the admin handler - mount it behind your own middleware.

## License

MIT — see [LICENSE](LICENSE) for details.