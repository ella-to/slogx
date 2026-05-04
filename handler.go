package slogx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Handler is slogx's custom slog.Handler. It:
//   - enriches every record with _traceId, _parentId, _spanPath from ctx,
//   - filters records by a global + per-package level set,
//   - fans out to a user-visible slog sink (JSON/text to an io.Writer) and a
//     pluggable Store for the UI.
type Handler struct {
	sink   slog.Handler // child sink for stdout/file output; may be nil when output=Discard
	levels *levelSet
	store  Store

	// attrs/groups captured by WithAttrs/WithGroup so we can apply them to
	// Records that we also mirror into Store.
	attrs  []slog.Attr
	groups []string

	// pcCache maps program counter -> resolved package path to avoid repeated
	// frame resolution on the hot path.
	pcCache *sync.Map // uintptr -> string
}

func newHandler(cfg *config) *Handler {
	levels := newLevelSet(cfg.globalLevel, cfg.packageLevels)
	h := &Handler{
		levels:  levels,
		store:   cfg.store,
		pcCache: &sync.Map{},
	}

	// Sink's minimum level tracks the floor of the level set dynamically so
	// records that couldn't pass any rule are never even built by slog.
	opts := &slog.HandlerOptions{
		Level:     dynamicLeveler{levels: levels},
		AddSource: cfg.addSource,
	}

	out := cfg.output
	if out == nil {
		out = io.Discard
	}
	if out != io.Discard {
		switch cfg.format {
		case FormatText:
			h.sink = slog.NewTextHandler(out, opts)
		default:
			h.sink = slog.NewJSONHandler(out, opts)
		}
	}
	return h
}

// Levels returns the handler's live level set. Exposed for the admin HTTP API.
func (h *Handler) Levels() *levelSet { return h.levels }

// Store returns the handler's live Store. Exposed for the admin HTTP API.
func (h *Handler) Store() Store { return h.store }

// Enabled implements slog.Handler. Uses the level-set floor so slog can skip
// record construction entirely when no rule could possibly allow the level.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.levels.Floor()
}

// dynamicLeveler adapts a levelSet to slog.Leveler for the sink's level opt.
type dynamicLeveler struct{ levels *levelSet }

func (d dynamicLeveler) Level() slog.Level { return d.levels.Floor() }

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	pkg := h.packageOf(r.PC)

	if !h.levels.Allow(pkg, r.Level) {
		return nil
	}

	traceID := TraceID(ctx)
	spanID := SpanID(ctx)
	spanPath := SpanPath(ctx)
	spanNamePath := SpanNamePath(ctx)
	spanDetailPath := SpanDetailPath(ctx)
	concurrent := Concurrent(ctx)

	if traceID != "" {
		r.AddAttrs(slog.String(AttrTraceID, traceID))
	}
	if spanID != "" {
		r.AddAttrs(slog.String(AttrParentID, spanID))
	}
	if spanPath != "" {
		r.AddAttrs(slog.String(AttrSpanPath, spanPath))
	}
	if concurrent {
		r.AddAttrs(slog.Bool(AttrGoroutine, true))
	}

	if h.store != nil {
		h.store.Append(h.toRecord(r, pkg, traceID, spanID, spanPath, spanNamePath, spanDetailPath, concurrent))
	}

	if h.sink != nil {
		return h.sink.Handle(ctx, r)
	}
	return nil
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nh := h.clone()
	nh.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	if h.sink != nil {
		nh.sink = h.sink.WithAttrs(attrs)
	}
	return nh
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := h.clone()
	nh.groups = append(append([]string{}, h.groups...), name)
	if h.sink != nil {
		nh.sink = h.sink.WithGroup(name)
	}
	return nh
}

func (h *Handler) clone() *Handler {
	return &Handler{
		sink:    h.sink,
		levels:  h.levels,
		store:   h.store,
		attrs:   h.attrs,
		groups:  h.groups,
		pcCache: h.pcCache,
	}
}

// packageOf resolves the Go import path of the function at pc. It caches the
// result to keep the hot path cheap.
func (h *Handler) packageOf(pc uintptr) string {
	if pc == 0 {
		return ""
	}
	if v, ok := h.pcCache.Load(pc); ok {
		return v.(string)
	}
	pkg := resolvePackage(pc)
	h.pcCache.Store(pc, pkg)
	return pkg
}

// resolvePackage converts a program counter into a Go import path.
//
// Given a fully-qualified function name like:
//
//	ella.to/example/sub.(*Server).Handle.func1
//	ella.to/example/sub.Sum
//
// we want "ella.to/example/sub" in both cases. The rule: the import path is
// everything up to the last '/' plus the portion of the final segment before
// the first '.'.
func resolvePackage(pc uintptr) string {
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return ""
	}
	name := fn.Name()
	slash := strings.LastIndex(name, "/")
	var prefix, tail string
	if slash >= 0 {
		prefix = name[:slash+1]
		tail = name[slash+1:]
	} else {
		tail = name
	}
	dot := strings.Index(tail, ".")
	if dot < 0 {
		return prefix + tail
	}
	return prefix + tail[:dot]
}

// toRecord converts a slog.Record (plus ctx-derived fields) into a Record
// suitable for the Store.
func (h *Handler) toRecord(r slog.Record, pkg, traceID, spanID, spanPath, spanNamePath, spanDetailPath string, concurrent bool) Record {
	attrs := make(map[string]any)
	for _, pre := range h.attrs {
		attrs[pre.Key] = attrValue(pre.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case AttrTraceID, AttrParentID, AttrSpanPath, AttrGoroutine:
			// already promoted to top-level fields
			return true
		}
		attrs[a.Key] = attrValue(a.Value)
		return true
	})

	var source string
	if r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		if fr, _ := fs.Next(); fr.File != "" {
			source = fmt.Sprintf("%s:%d", trimGoPath(fr.File), fr.Line)
		}
	}

	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}

	return Record{
		Time:           t,
		Level:          r.Level,
		Message:        r.Message,
		Source:         source,
		Package:        pkg,
		TraceID:        traceID,
		ParentID:       spanID,
		SpanPath:       spanPath,
		SpanNamePath:   spanNamePath,
		SpanDetailPath: spanDetailPath,
		Concurrent:     concurrent,
		Attrs:          attrs,
	}
}

// attrValue converts a slog.Value to a JSON-friendly Go value.
func attrValue(v slog.Value) any {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	case slog.KindGroup:
		g := v.Group()
		m := make(map[string]any, len(g))
		for _, a := range g {
			m[a.Key] = attrValue(a.Value)
		}
		return m
	case slog.KindAny:
		return v.Any()
	default:
		return v.String()
	}
}

// trimGoPath shortens a source path down to something like
// "pkg/path/file.go" by stripping a common "/src/" or GOPATH-style prefix.
// It's purely cosmetic for the UI.
func trimGoPath(file string) string {
	if i := strings.LastIndex(file, "/src/"); i >= 0 {
		return file[i+5:]
	}
	return file
}
