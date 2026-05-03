package slogx

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"testing"
)

func TestHandlerEnrichesAttrsFromContext(t *testing.T) {
	s := newRingStore(16)
	h := Setup(
		Output(io.Discard),
		WithStore(s),
		Level(slog.LevelDebug),
	)
	defer activeHandler.Store(nil)

	ctx := Context(context.Background())
	slog.InfoContext(ctx, "hello", "k", "v")

	got := h.Store().Query(Query{})
	if len(got) != 1 {
		t.Fatalf("want 1 record got %d", len(got))
	}
	r := got[0]
	if r.TraceID == "" {
		t.Errorf("expected TraceID")
	}
	if r.ParentID == "" {
		t.Errorf("expected ParentID")
	}
	if r.SpanPath == "" {
		t.Errorf("expected SpanPath")
	}
	if r.Attrs["k"] != "v" {
		t.Errorf("user attrs missing, got %v", r.Attrs)
	}
}

func TestHandlerNestedContextChainsSpanPath(t *testing.T) {
	s := newRingStore(16)
	Setup(Output(io.Discard), WithStore(s), Level(slog.LevelDebug))
	defer activeHandler.Store(nil)

	ctx := Context(context.Background())
	inner := Context(ctx)
	slog.InfoContext(inner, "inner")

	got := s.Query(Query{})
	if len(got) != 1 {
		t.Fatalf("want 1 got %d", len(got))
	}
	parts := strings.Split(got[0].SpanPath, "/")
	if len(parts) != 2 {
		t.Fatalf("span path expected 2 segments, got %q", got[0].SpanPath)
	}
	if got[0].ParentID != parts[1] {
		t.Fatalf("ParentID=%q should equal last span path segment %q", got[0].ParentID, parts[1])
	}
}

func TestHandlerMarksGoroutineRecords(t *testing.T) {
	s := newRingStore(16)
	Setup(Output(io.Discard), WithStore(s), Level(slog.LevelDebug))
	defer activeHandler.Store(nil)

	ctx := Context(context.Background())
	slog.InfoContext(ctx, "on parent")

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx := Context(ctx)
		slog.InfoContext(ctx, "on child")
		// A further nested span on the same goroutine should still be concurrent
		// (the flag is sticky for all descendants).
		inner := Context(ctx)
		slog.InfoContext(inner, "deeper on child")
	}()
	<-done

	slog.InfoContext(ctx, "back on parent")

	all := s.Query(Query{})
	var parent, child, deeper, after int
	for _, r := range all {
		switch r.Message {
		case "on parent":
			parent++
			if r.Concurrent {
				t.Errorf("parent record should not be concurrent")
			}
		case "on child":
			child++
			if !r.Concurrent {
				t.Errorf("child record should be concurrent")
			}
		case "deeper on child":
			deeper++
			if !r.Concurrent {
				t.Errorf("deeper record should be concurrent")
			}
		case "back on parent":
			after++
			if r.Concurrent {
				t.Errorf("post-join parent record should not be concurrent")
			}
		}
	}
	if parent != 1 || child != 1 || deeper != 1 || after != 1 {
		t.Fatalf("missing records: parent=%d child=%d deeper=%d after=%d", parent, child, deeper, after)
	}
}

func TestHandlerSilencesPackageWithLevelOff(t *testing.T) {
	s := newRingStore(16)
	Setup(
		Output(io.Discard),
		WithStore(s),
		GlobalLevel(slog.LevelDebug),
		PackageLevel("ella.to/slogx", LevelOff),
	)
	defer activeHandler.Store(nil)

	ctx := Context(context.Background())
	slog.InfoContext(ctx, "should be dropped")

	got := s.Query(Query{})
	if len(got) != 0 {
		t.Fatalf("expected 0 records (silenced), got %d: %v", len(got), got)
	}
}

func TestHandlerPackageLevelRaisesThreshold(t *testing.T) {
	s := newRingStore(16)
	Setup(
		Output(io.Discard),
		WithStore(s),
		GlobalLevel(slog.LevelDebug),
		PackageLevel("ella.to/slogx", slog.LevelWarn),
	)
	defer activeHandler.Store(nil)

	ctx := Context(context.Background())
	slog.InfoContext(ctx, "info dropped")
	slog.WarnContext(ctx, "warn kept")
	slog.ErrorContext(ctx, "error kept")

	got := s.Query(Query{})
	if len(got) != 2 {
		t.Fatalf("expected 2 records (warn+error), got %d", len(got))
	}
	if got[0].Message != "warn kept" || got[1].Message != "error kept" {
		t.Fatalf("unexpected records: %+v", got)
	}
}

func TestHandlerGlobalOffWithPackageOptIn(t *testing.T) {
	s := newRingStore(16)
	Setup(
		Output(io.Discard),
		WithStore(s),
		GlobalLevel(LevelOff),
		PackageLevel("ella.to/slogx", slog.LevelInfo),
	)
	defer activeHandler.Store(nil)

	ctx := Context(context.Background())
	slog.InfoContext(ctx, "opted in")

	got := s.Query(Query{})
	if len(got) != 1 {
		t.Fatalf("expected 1 record (package-opt-in), got %d", len(got))
	}
}

func TestResolvePackage(t *testing.T) {
	// Call runtime.Callers via a helper whose pc we can test.
	pc := pcOfThisFunction()
	got := resolvePackage(pc)
	if got != "ella.to/slogx" {
		t.Fatalf("resolvePackage = %q want %q", got, "ella.to/slogx")
	}
}

//go:noinline
func pcOfThisFunction() uintptr {
	return getPC()
}

//go:noinline
func getPC() uintptr {
	var pcs [1]uintptr
	n := runtime.Callers(1, pcs[:])
	if n == 0 {
		return 0
	}
	return pcs[0]
}
