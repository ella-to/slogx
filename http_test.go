package slogx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddlewareTraceIDPrecedence(t *testing.T) {
	Setup(Output(io.Discard))
	defer activeHandler.Store(nil)

	var gotHeader, gotQuery, gotGenerated string

	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/h":
			gotHeader = TraceID(r.Context())
		case "/q":
			gotQuery = TraceID(r.Context())
		case "/g":
			gotGenerated = TraceID(r.Context())
		}
	}))

	// header only
	r := httptest.NewRequest("GET", "/h", nil)
	r.Header.Set(HeaderTraceID, "from-header")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if gotHeader != "from-header" {
		t.Errorf("header trace id: got %q want %q", gotHeader, "from-header")
	}
	if w.Header().Get(HeaderTraceID) != "from-header" {
		t.Errorf("response header not echoed")
	}

	// query overrides header
	r = httptest.NewRequest("GET", "/q?log_trace_id=from-query", nil)
	r.Header.Set(HeaderTraceID, "from-header")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if gotQuery != "from-query" {
		t.Errorf("query override failed: got %q", gotQuery)
	}

	// neither: generated
	r = httptest.NewRequest("GET", "/g", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if gotGenerated == "" {
		t.Errorf("expected generated trace id")
	}
	if w.Header().Get(HeaderTraceID) != gotGenerated {
		t.Errorf("response header mismatch")
	}
}

func TestHttpHandlerLevels(t *testing.T) {
	Setup(Output(io.Discard))
	defer activeHandler.Store(nil)

	mux := HttpHandler()

	// GET initial state
	r := httptest.NewRequest("GET", "/levels", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("GET /levels: status %d", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["global"] != "INFO" {
		t.Fatalf("initial global: %v", got["global"])
	}

	// PATCH: set global OFF and opt a package in at DEBUG
	body := bytes.NewBufferString(`{"global":"OFF","set":{"ella.to/app":"DEBUG"}}`)
	r = httptest.NewRequest("PATCH", "/levels", body)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("PATCH /levels: status %d body %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["global"] != "OFF" {
		t.Fatalf("global not updated: %v", got["global"])
	}
	pkgs, _ := got["packages"].([]any)
	found := false
	for _, p := range pkgs {
		m := p.(map[string]any)
		if m["package"] == "ella.to/app" && m["level"] == "DEBUG" {
			found = true
		}
	}
	if !found {
		t.Fatalf("package override not applied: %v", pkgs)
	}

	// PATCH: unset the package
	body = bytes.NewBufferString(`{"unset":["ella.to/app"]}`)
	r = httptest.NewRequest("PATCH", "/levels", body)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	pkgs, _ = got["packages"].([]any)
	for _, p := range pkgs {
		m := p.(map[string]any)
		if m["package"] == "ella.to/app" {
			t.Fatalf("package still present after unset: %v", pkgs)
		}
	}

	// PATCH with an invalid level name yields 400
	body = bytes.NewBufferString(`{"global":"CHATTY"}`)
	r = httptest.NewRequest("PATCH", "/levels", body)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("invalid level should 400, got %d", w.Code)
	}
}

func TestHttpHandlerLogsByTraceID(t *testing.T) {
	Setup(Output(io.Discard))
	defer activeHandler.Store(nil)

	ctx := Context(context.Background())
	traceID := TraceID(ctx)
	slog.InfoContext(ctx, "hello world")

	mux := HttpHandler()
	r := httptest.NewRequest("GET", "/logs?traceId="+traceID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "hello world") {
		t.Fatalf("log missing: %s", w.Body.String())
	}
}

