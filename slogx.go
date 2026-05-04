// Package slogx extends the standard log/slog package with:
//   - Hierarchical trace/span context propagation via Context(ctx).
//   - Static and dynamic include/exclude filtering by Go package import path.
//   - A pluggable Store for in-process log retention.
//   - An HTTP admin + UI handler for live debugging.
//
// Typical setup:
//
//	func main() {
//	    slogx.Setup(
//	        slogx.Includes("ella.to/example"),
//	        slogx.Excludes("example.com/noise"),
//	    )
//	    // ...
//	}
//
// Inside any function that accepts a context, the caller establishes a new
// span by shadowing ctx:
//
//	func Sum(ctx context.Context, xs ...int) int {
//	    ctx = slogx.Context(ctx)
//	    slog.InfoContext(ctx, "summing", "n", len(xs))
//	    // ...
//	}
package slogx

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Format selects the serialization used for the stdout/file sink.
type Format int

const (
	FormatJSON Format = iota
	FormatText
)

// Attribute keys injected into every record.
const (
	AttrTraceID   = "_traceId"
	AttrParentID  = "_parentId"
	AttrSpanPath  = "_spanPath"
	AttrGoroutine = "_goroutine"
)

// Default configuration values.
const (
	DefaultRingBufferSize = 10_000
)

// Option configures Setup.
type Option func(*config)

type config struct {
	globalLevel    slog.Level
	packageLevels  []packageLevel
	output         io.Writer
	format         Format
	store          Store
	ringBufferSize int
	addSource      bool
}

// GlobalLevel sets the minimum level for records from packages that have no
// explicit override. Use LevelOff to turn logging off by default (handy when
// you want to opt-in specific packages with PackageLevel).
func GlobalLevel(l slog.Level) Option {
	return func(c *config) { c.globalLevel = l }
}

// Level is an alias for GlobalLevel. Kept for ergonomic parity with slog.
func Level(l slog.Level) Option {
	return GlobalLevel(l)
}

// PackageLevel sets a per-package (prefix) minimum level that overrides the
// global level. Longest matching prefix wins. Pass LevelOff to silence a
// package entirely.
//
//	slogx.PackageLevel("ella.to/noisy", slogx.LevelOff)
//	slogx.PackageLevel("ella.to/app/api", slog.LevelDebug)
func PackageLevel(pattern string, l slog.Level) Option {
	return func(c *config) {
		c.packageLevels = append(c.packageLevels, packageLevel{Pattern: pattern, Level: l})
	}
}

// Output sets the destination for the stdout-style sink. Default: os.Stdout.
// Pass io.Discard to disable the sink entirely.
func Output(w io.Writer) Option {
	return func(c *config) { c.output = w }
}

// WithFormat selects JSON or text output for the stdout-style sink.
func WithFormat(f Format) Option {
	return func(c *config) { c.format = f }
}

// WithStore installs a custom Store. Default is an in-memory ring buffer.
func WithStore(s Store) Option {
	return func(c *config) { c.store = s }
}

// RingBufferSize sets the capacity of the default ring buffer store.
// Ignored if WithStore was also provided.
func RingBufferSize(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.ringBufferSize = n
		}
	}
}

// AddSource controls whether the underlying slog sink includes source info.
func AddSource(v bool) Option {
	return func(c *config) { c.addSource = v }
}

// activeHandler is the handler wired in by the most recent Setup call.
// It is kept so HttpHandler() and Middleware() can reach the shared filter/store.
var activeHandler atomic.Pointer[Handler]

// Setup installs slogx as the default slog logger and returns the installed
// Handler. It is safe to call Setup more than once (the previous handler is
// replaced).
func Setup(opts ...Option) *Handler {
	cfg := &config{
		output:         os.Stdout,
		format:         FormatJSON,
		globalLevel:    slog.LevelInfo,
		ringBufferSize: DefaultRingBufferSize,
		addSource:      true,
	}
	for _, o := range opts {
		o(cfg)
	}
	if cfg.store == nil {
		cfg.store = newRingStore(cfg.ringBufferSize)
	}

	h := newHandler(cfg)
	activeHandler.Store(h)
	slog.SetDefault(slog.New(h))
	return h
}

func getActive() *Handler {
	return activeHandler.Load()
}

type ctxKey int

const (
	traceIDKey ctxKey = iota
	spanIDKey
	spanPathKey
	birthGIDKey       // goroutine id recorded at the span's creation
	concurrentKey     // true if this span (or any ancestor) was created on a different goroutine than its parent
	spanNamePathKey   // slash-joined short caller names, e.g. "Open/Prepare"
	spanDetailPathKey // pipe-joined detail strings, e.g. "pkg.Open:file.go:10|pkg.Prepare:file.go:42"
)

// ContextOption configures the behaviour of a Context call.
type ContextOption func(*contextOpts)

type contextOpts struct {
	extraSkip int
}

// WithWrapFunc tells Context that it is being called from inside a helper /
// wrapper function (e.g. event.Context, SlogxPropagator.Inject) that should
// itself be transparent to the span name. Each call to WithWrapFunc adds one
// extra frame to skip when resolving the caller name, so the recorded span
// points at the real application function rather than the helper.
//
// Example – inside bus.Event.Context:
//
//	return slogx.Context(ctx, slogx.WithWrapFunc())
func WithWrapFunc() ContextOption {
	return func(o *contextOpts) { o.extraSkip++ }
}

// Context returns a derived context that starts a new span. On the first call
// (no trace-id in ctx) a new trace-id is also generated. The returned context
// carries:
//
//   - trace id (stable for the whole trace),
//   - current span id (this new span),
//   - span path (slash-joined ancestor chain, including this span),
//   - goroutine-concurrency flag (true if this span was created on a
//     different goroutine than its parent, or inherited from such a span).
//
// Use at the start of any function that accepts ctx:
//
//	ctx = slogx.Context(ctx)
//
// When calling from inside a helper that should be invisible in the span
// name, pass WithWrapFunc() for each wrapper layer:
//
//	ctx = slogx.Context(ctx, slogx.WithWrapFunc())
func Context(ctx context.Context, opts ...ContextOption) context.Context {
	o := contextOpts{}
	for _, opt := range opts {
		opt(&o)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	traceID, _ := ctx.Value(traceIDKey).(string)
	if traceID == "" {
		traceID = newID()
		ctx = context.WithValue(ctx, traceIDKey, traceID)
	}

	spanID := newID()

	// Capture caller info. skip=1 resolves to the direct caller of Context.
	// Each WithWrapFunc() adds one more frame to skip past wrapper helpers.
	spanName, spanDetail := callerInfo(1 + o.extraSkip)

	prevPath, _ := ctx.Value(spanPathKey).(string)
	var newPath string
	if prevPath == "" {
		newPath = spanID
	} else {
		newPath = prevPath + "/" + spanID
	}

	prevNamePath, _ := ctx.Value(spanNamePathKey).(string)
	var newNamePath string
	if prevNamePath == "" {
		newNamePath = spanName
	} else {
		newNamePath = prevNamePath + "/" + spanName
	}

	prevDetailPath, _ := ctx.Value(spanDetailPathKey).(string)
	var newDetailPath string
	if prevDetailPath == "" {
		newDetailPath = spanDetail
	} else {
		newDetailPath = prevDetailPath + "|" + spanDetail
	}

	curGID := goid()
	concurrent, _ := ctx.Value(concurrentKey).(bool)
	if parentGID, ok := ctx.Value(birthGIDKey).(int64); ok && parentGID != curGID {
		concurrent = true
	}

	ctx = context.WithValue(ctx, spanIDKey, spanID)
	ctx = context.WithValue(ctx, spanPathKey, newPath)
	ctx = context.WithValue(ctx, spanNamePathKey, newNamePath)
	ctx = context.WithValue(ctx, spanDetailPathKey, newDetailPath)
	ctx = context.WithValue(ctx, birthGIDKey, curGID)
	if concurrent {
		ctx = context.WithValue(ctx, concurrentKey, true)
	}
	return ctx
}

// callerInfo returns the short function/method name and a detail string for
// the function skip frames above callerInfo's own caller. skip=1 means the
// direct caller of callerInfo (i.e. the caller of Context).
func callerInfo(skip int) (shortName, detail string) {
	pc, file, line, ok := runtime.Caller(skip + 1)
	if !ok {
		return "?", ""
	}
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return "?", ""
	}
	fullName := fn.Name()
	// shortName: everything after the last dot, e.g.:
	//   "ella.to/sqlite.(*Conn).Prepare" → "Prepare"
	//   "main.handleSum"                 → "handleSum"
	shortName = fullName
	if i := strings.LastIndex(fullName, "."); i >= 0 {
		shortName = fullName[i+1:]
	}
	detail = fullName + ":" + trimGoPath(file) + ":" + strconv.Itoa(line)
	return shortName, detail
}

// Concurrent reports whether ctx belongs to a span that runs on a different
// goroutine than the span that created its parent context (i.e. it was
// reached by crossing a `go` statement). Once set, it stays set for all
// descendant spans.
func Concurrent(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(concurrentKey).(bool)
	return v
}

// WithTraceID seeds a trace id into ctx without starting a new span. It is
// used by Middleware to honor inbound X-TRACE-ID headers before the root
// Context call.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if traceID == "" {
		return ctx
	}
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceID returns the current trace id from ctx, or "" if none.
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(traceIDKey).(string)
	return s
}

// SpanID returns the current (innermost) span id from ctx, or "" if none.
func SpanID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(spanIDKey).(string)
	return s
}

// SpanPath returns the slash-joined ancestor chain of span ids from ctx.
func SpanPath(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(spanPathKey).(string)
	return s
}

// SpanNamePath returns the slash-joined short caller names for the current
// span ancestry, e.g. "Open/Prepare". Falls back to "" if not set.
func SpanNamePath(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(spanNamePathKey).(string)
	return s
}

// SpanDetailPath returns the pipe-joined detail strings (fullFuncName:file:line)
// for each span in the current ancestry. Falls back to "" if not set.
func SpanDetailPath(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(spanDetailPathKey).(string)
	return s
}

// newID returns a lexicographically sortable 26-character ID (ULID-like:
// 48-bit millisecond timestamp + 80 bits of randomness, Crockford-ish base32).
// We use the standard library base32 alphabet without padding for simplicity.
var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func newID() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	_, _ = rand.Read(b[6:])
	return strings.ToLower(idEncoding.EncodeToString(b[:]))
}
