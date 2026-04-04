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

**slogx** extends Go's `log/slog` with per-package log level filtering, file output with rotation, trace ID filtering, and an HTTP API for runtime control.

</div>

## Installation

```bash
go get ella.to/slogx@v0.0.1
```

for command line tools

```bash
go install ella.to/slogx/cmd/slogx@v0.0.1
```

## FilterHandler

`FilterHandler` wraps any `slog.Handler` and adds the ability to set different log levels for different packages. It inspects the caller's package path at log time and applies the matching rule.

```go
inner := slog.NewJSONHandler(os.Stdout, nil)

handler := slogx.NewFilterHandler(inner,
    slogx.WithDefaultLevel(slog.LevelInfo),
    slogx.WithLogLevel("myapp/db", slog.LevelDebug),
    slogx.WithLogLevel("myapp/http", slog.LevelWarn),
)

slog.SetDefault(slog.New(handler))
```

In this setup, most of your app logs at Info and above, but the `db` package logs everything down to Debug, and the `http` package only logs Warn and above.

### Exclusive Filtering

When you only want to see logs from specific packages and silence everything else:

```go
handler := slogx.NewFilterHandler(inner,
    slogx.WithLogLevel("myapp/auth", slog.LevelDebug),
    slogx.WithExclusiveFiltering(),
)
```

Only logs from `myapp/auth` will appear. Everything else is dropped regardless of level.

### Runtime Configuration

All settings can be changed while the application is running:

```go
handler.SetDefaultLevel(slog.LevelDebug)
handler.SetLogLevel("myapp/payments", slog.LevelDebug)
handler.RemoveLogLevel("myapp/payments")
handler.SetEnabled(false)  // disable all logging
handler.SetEnabled(true)   // re-enable
handler.SetExclusiveFiltering(true)
```

### Trace ID Filtering

Filter logs to only show entries that carry a specific trace ID. Useful when debugging a particular request in a noisy production environment.

```go
type traceKey struct{}

handler.SetTraceIdKey(traceKey{})
handler.SetFilterTraceId("request-abc-123")

// Only logs where the context contains traceKey{} = "request-abc-123"
// or where an attribute value matches "request-abc-123" will pass through.
```

Clear the filter to go back to normal:

```go
handler.SetFilterTraceId("")
```

## FileHandler

`FileHandler` writes structured JSON logs to files with automatic rotation. Each run gets its own timestamped directory, and files rotate when they hit a size or record count limit.

```go
fileHandler, err := slogx.NewFileHandler("/var/log/myapp",
    slogx.WithMaxBytes(50 * 1024 * 1024),  // rotate at 50 MB
    slogx.WithMaxRecords(100_000),           // or at 100k records
)
defer fileHandler.Close()

slog.SetDefault(slog.New(fileHandler))
```

Directory structure:

```
/var/log/myapp/
  2025-03-25_14-30-00/
    00001.log
    00002.log
    00003.log
  2025-03-24_09-15-00/
    00001.log
```

Each log line is a JSON object written by `slog.JSONHandler`, so it works with any log aggregation tool.

### Combining with FilterHandler

The common pattern is to wrap `FileHandler` with `FilterHandler`:

```go
fileHandler, _ := slogx.NewFileHandler("/var/log/myapp")
filterHandler := slogx.NewFilterHandler(fileHandler,
    slogx.WithDefaultLevel(slog.LevelInfo),
    slogx.WithLogLevel("myapp/db", slog.LevelDebug),
)

slog.SetDefault(slog.New(filterHandler))
```

### Reading Logs Back

List available sessions, then read logs with pagination:

```go
sessions, err := slogx.ListSessions("/var/log/myapp")
// sessions[0].Name      = "2025-03-25_14-30-00"
// sessions[0].FileCount = 3

reader, err := slogx.NewLogReader(sessions[0].Path, 100) // 100 entries per page

page, err := reader.ReadPage(1, 0) // file 1, line offset 0
for _, entry := range page.Entries {
    fmt.Printf("[%s] %s: %s\n", entry.Level, entry.Time.Format(time.RFC3339), entry.Message)
}

// Or read everything
entries, err := reader.ReadAll()

// Or stream with context cancellation
entryCh, errCh := reader.StreamLogs(ctx)
for entry := range entryCh {
    fmt.Println(entry.Message)
}
```

## HTTP API

Expose `FilterHandler` controls over HTTP for runtime log management. Mount it as a sub-handler on your existing mux:

```go
mux.Handle("/debug/log/", http.StripPrefix("/debug/log/", handler.HttpHandler()))
```

### Endpoints

**GET /level** — Returns current config as JSON:
```json
{
  "disabled": false,
  "default": "INFO",
  "rules": {"myapp/db": "DEBUG"},
  "exclusive_filtering": false,
  "filter_trace_id": ""
}
```

**POST /level?path=myapp/db&level=DEBUG** — Set log level for a path (use `path=default` for the default level)

**DELETE /level?path=myapp/db** — Remove a path-specific rule

**POST /enable?enable=true** — Enable or disable all logging

**POST /trace-id?trace-id=abc-123** — Filter logs by trace ID

**DELETE /trace-id** — Clear trace ID filter

**GET /trace-id** — Get current trace ID filter

## License

MIT — see [LICENSE](LICENSE) for details.