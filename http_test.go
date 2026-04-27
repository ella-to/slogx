package slogx

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newMountedDebugLogMux(t *testing.T) (*FilterHandler, *http.ServeMux) {
	t.Helper()

	handler := NewFilterHandler(slog.NewJSONHandler(io.Discard, nil))
	mux := http.NewServeMux()
	mux.Handle("/debug/log/", http.StripPrefix("/debug/log/", handler.HttpHandler()))

	return handler, mux
}

func TestNewHttpHandler_LevelRouteWithStripPrefix(t *testing.T) {
	handler, mux := newMountedDebugLogMux(t)

	setReq := httptest.NewRequest(http.MethodPost, "/debug/log/level?path=myapp/db&level=DEBUG", nil)
	setRes := httptest.NewRecorder()
	mux.ServeHTTP(setRes, setReq)

	if setRes.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, setRes.Code)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/debug/log/level", nil)
	getRes := httptest.NewRecorder()
	mux.ServeHTTP(getRes, getReq)

	if getRes.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, getRes.Code)
	}

	var body struct {
		Rules map[string]string `json:"rules"`
	}
	if err := json.NewDecoder(getRes.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if got := body.Rules["myapp/db"]; got != "DEBUG" {
		t.Fatalf("expected myapp/db level DEBUG, got %q", got)
	}

	_, rules, _, _, _, _ := handler.GetConfig()
	if got := rules["myapp/db"]; got != slog.LevelDebug {
		t.Fatalf("expected internal rule level %v, got %v", slog.LevelDebug, got)
	}
}

func TestNewHttpHandler_EnableRouteWithStripPrefix(t *testing.T) {
	handler, mux := newMountedDebugLogMux(t)

	req := httptest.NewRequest(http.MethodPost, "/debug/log/enable?enable=false", nil)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, res.Code)
	}

	_, _, disabled, _, _, _ := handler.GetConfig()
	if !disabled {
		t.Fatal("expected handler to be disabled")
	}
}

func TestNewHttpHandler_TraceIDRouteWithStripPrefix(t *testing.T) {
	handler, mux := newMountedDebugLogMux(t)

	setReq := httptest.NewRequest(http.MethodPost, "/debug/log/trace-id?trace-id=abc-123", nil)
	setRes := httptest.NewRecorder()
	mux.ServeHTTP(setRes, setReq)

	if setRes.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, setRes.Code)
	}

	if got := handler.GetFilterTraceID(); got != "abc-123" {
		t.Fatalf("expected filter trace id abc-123, got %q", got)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/debug/log/trace-id", nil)
	getRes := httptest.NewRecorder()
	mux.ServeHTTP(getRes, getReq)

	if getRes.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, getRes.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(getRes.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if got := body["trace_id"]; got != "abc-123" {
		t.Fatalf("expected trace_id abc-123, got %q", got)
	}

	clearReq := httptest.NewRequest(http.MethodDelete, "/debug/log/trace-id", nil)
	clearRes := httptest.NewRecorder()
	mux.ServeHTTP(clearRes, clearReq)

	if clearRes.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, clearRes.Code)
	}

	if got := handler.GetFilterTraceID(); got != "" {
		t.Fatalf("expected empty filter trace id, got %q", got)
	}
}

func TestTraceIDMiddleware_QueryString(t *testing.T) {
	type ctxKey struct{}
	key := ctxKey{}

	m := NewTraceIDMiddleware(key, WithQueryString("trace-id"))

	var captured string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, ok := r.Context().Value(key).(string); ok {
			captured = v
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/?trace-id=abc-123", nil)
	m.Handler(next).ServeHTTP(httptest.NewRecorder(), req)

	if captured != "abc-123" {
		t.Fatalf("expected trace id abc-123, got %q", captured)
	}
}

func TestTraceIDMiddleware_Header(t *testing.T) {
	type ctxKey struct{}
	key := ctxKey{}

	m := NewTraceIDMiddleware(key, WithHeaderKey("X-Trace-ID"))

	var captured string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, ok := r.Context().Value(key).(string); ok {
			captured = v
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Trace-ID", "header-trace-456")
	m.Handler(next).ServeHTTP(httptest.NewRecorder(), req)

	if captured != "header-trace-456" {
		t.Fatalf("expected trace id header-trace-456, got %q", captured)
	}
}

func TestTraceIDMiddleware_QueryPriorityOverHeader(t *testing.T) {
	type ctxKey struct{}
	key := ctxKey{}

	m := NewTraceIDMiddleware(key, WithQueryString("tid"), WithHeaderKey("X-Trace-ID"))

	var captured string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, ok := r.Context().Value(key).(string); ok {
			captured = v
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/?tid=from-query", nil)
	req.Header.Set("X-Trace-ID", "from-header")
	m.Handler(next).ServeHTTP(httptest.NewRecorder(), req)

	if captured != "from-query" {
		t.Fatalf("expected query value from-query, got %q", captured)
	}
}

func TestTraceIDMiddleware_NoTraceID(t *testing.T) {
	type ctxKey struct{}
	key := ctxKey{}

	m := NewTraceIDMiddleware(key, WithQueryString("tid"), WithHeaderKey("X-Trace-ID"))

	passed := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passed = true
		if v := r.Context().Value(key); v != nil {
			t.Errorf("expected no trace id in context, got %v", v)
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	m.Handler(next).ServeHTTP(httptest.NewRecorder(), req)

	if !passed {
		t.Fatal("next handler was not called")
	}
}
