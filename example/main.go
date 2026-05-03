// Example HTTP service wired with slogx.
//
// Run:
//
//	go run ./example
//
// Then try:
//
//	curl -H "X-TRACE-ID: my-trace-1" http://localhost:8080/sum?a=3&b=4&c=5
//	curl http://localhost:8080/slow
//	curl http://localhost:8080/fanout   # spawns goroutines; UI shows "go" chips
//
// Open the debugging UI:
//
//	http://localhost:8080/_slogx/
package main

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"ella.to/slogx"
)

func main() {
	// The example's log calls live in package main (the Go runtime reports
	// that package simply as "main"). Here we lower it to DEBUG while leaving
	// the global default at INFO -- try changing these from the UI's Levels
	// tab at runtime.
	slogx.Setup(
		slogx.GlobalLevel(slog.LevelInfo),
		slogx.PackageLevel("main", slog.LevelDebug),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sum", handleSum)
	mux.HandleFunc("GET /slow", handleSlow)
	mux.HandleFunc("GET /fanout", handleFanout)

	mux.Handle("/_slogx/", http.StripPrefix("/_slogx", slogx.HttpHandler()))

	slog.Info("server starting", "addr", ":8080")
	if err := http.ListenAndServe(":8080", slogx.Middleware(mux)); err != nil {
		slog.Error("server failed", "err", err)
	}
}

func handleSum(w http.ResponseWriter, r *http.Request) {
	ctx := slogx.Context(r.Context())

	nums := parseInts(ctx, r.URL.Query()["a"], r.URL.Query()["b"], r.URL.Query()["c"])
	total := Sum(ctx, nums...)

	slog.InfoContext(ctx, "sum handler done", "total", total)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"total":` + strconv.Itoa(total) + `}`))
}

func handleSlow(w http.ResponseWriter, r *http.Request) {
	ctx := slogx.Context(r.Context())
	slog.InfoContext(ctx, "slow handler start")
	nested(ctx, 3)
	slog.WarnContext(ctx, "slow handler finishing", "note", "took a while")
	_, _ = w.Write([]byte("ok"))
}

func handleFanout(w http.ResponseWriter, r *http.Request) {
	ctx := slogx.Context(r.Context())
	slog.InfoContext(ctx, "fanning out")

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ctx := slogx.Context(ctx)
			slog.InfoContext(ctx, "worker started", "id", id)
			time.Sleep(time.Duration(10+id*5) * time.Millisecond)
			slog.InfoContext(ctx, "worker done", "id", id)
		}(i)
	}
	wg.Wait()
	slog.InfoContext(ctx, "all workers joined")
	_, _ = w.Write([]byte("ok"))
}

func nested(ctx context.Context, depth int) {
	ctx = slogx.Context(ctx)
	slog.DebugContext(ctx, "nested", "depth", depth)
	if depth <= 0 {
		slog.InfoContext(ctx, "reached bottom")
		return
	}
	time.Sleep(10 * time.Millisecond)
	nested(ctx, depth-1)
}

func parseInts(ctx context.Context, groups ...[]string) []int {
	ctx = slogx.Context(ctx)
	out := make([]int, 0)
	for _, g := range groups {
		for _, s := range g {
			n, err := strconv.Atoi(s)
			if err != nil {
				slog.WarnContext(ctx, "skipping invalid int", "value", s, "err", err.Error())
				continue
			}
			out = append(out, n)
		}
	}
	slog.DebugContext(ctx, "parsed ints", "count", len(out))
	return out
}

// Sum adds up its arguments. It shows the canonical slogx usage pattern: shadow
// ctx on the very first line.
func Sum(ctx context.Context, xs ...int) int {
	ctx = slogx.Context(ctx)
	slog.InfoContext(ctx, "summing", "n", len(xs))

	total := 0
	for _, x := range xs {
		total += x
	}

	slog.InfoContext(ctx, "sum complete", "total", total)
	return total
}
