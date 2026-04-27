package slogx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

type HttpHandler struct {
	log *FilterHandler
	mux *http.ServeMux
}

var _ http.Handler = (*HttpHandler)(nil)

func NewHttpHandler(log *FilterHandler) *HttpHandler {
	mux := http.NewServeMux()

	h := &HttpHandler{log: log, mux: mux}

	mux.HandleFunc("/level", h.handleLogLevel)
	mux.HandleFunc("/enable", h.handleLogEnable)
	mux.HandleFunc("/trace-id", h.handleTraceID)

	return h
}

func (h *HttpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL != nil && r.URL.Path != "" && !strings.HasPrefix(r.URL.Path, "/") {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + r.URL.Path
		h.mux.ServeHTTP(w, r2)
		return
	}

	h.mux.ServeHTTP(w, r)
}

func (h *HttpHandler) handleLogLevel(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "POST":
		path := r.URL.Query().Get("path")
		levelStr := r.URL.Query().Get("level")

		var level slog.Level
		if err := level.UnmarshalText([]byte(levelStr)); err != nil {
			http.Error(w, "invalid level", http.StatusBadRequest)
			return
		}

		if path == "" || path == "default" {
			h.log.SetDefaultLevel(level)
		} else {
			h.log.SetLogLevel(path, level)
		}
		w.WriteHeader(http.StatusOK)

	case "DELETE":
		path := r.URL.Query().Get("path")
		h.log.RemoveLogLevel(path)
		w.WriteHeader(http.StatusOK)

	case "GET":
		defaultLevel, rules, disabled, exclusiveFiltering, _, filterTraceId := h.log.GetConfig()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"disabled":            disabled,
			"default":             defaultLevel.String(),
			"rules":               rules,
			"exclusive_filtering": exclusiveFiltering,
			"filter_trace_id":     filterTraceId,
		})
	}
}

func (h *HttpHandler) handleLogEnable(w http.ResponseWriter, r *http.Request) {
	enable := r.URL.Query().Get("enable") == "true"
	h.log.SetEnabled(enable)
	w.WriteHeader(http.StatusOK)
}

func (h *HttpHandler) handleTraceID(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "POST":
		traceID := r.URL.Query().Get("trace-id")
		h.log.SetFilterTraceID(traceID)
		w.WriteHeader(http.StatusOK)

	case "DELETE":
		h.log.SetFilterTraceID("")
		w.WriteHeader(http.StatusOK)

	case "GET":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"trace_id": h.log.GetFilterTraceID(),
		})
	}
}

// --- TraceID injection middleware ---

// TraceIDMiddleware is an HTTP middleware that reads a trace ID from the
// incoming request (query string and/or header) and injects it into the
// request context so downstream handlers and slogx's FilterHandler can use it.
type TraceIDMiddleware struct {
	contextKey any
	queryKey   string
	headerKey  string
}

// TraceIDMiddlewareOpt configures TraceIDMiddleware.
type TraceIDMiddlewareOpt func(*TraceIDMiddleware)

// WithQueryString configures the middleware to read the trace ID from the
// named query-string parameter.
func WithQueryString(key string) TraceIDMiddlewareOpt {
	return func(m *TraceIDMiddleware) { m.queryKey = key }
}

// WithHeaderKey configures the middleware to read the trace ID from the
// named HTTP header.
func WithHeaderKey(key string) TraceIDMiddlewareOpt {
	return func(m *TraceIDMiddleware) { m.headerKey = key }
}

// NewTraceIDMiddleware creates a middleware that extracts a trace ID and
// stores it in the request context under contextKey.  Configure at least one
// of WithQueryString or WithHeaderKey; query string takes priority over header
// when both are present.
//
// Use the same contextKey value that you pass to FilterHandler.SetTraceIDKey
// so the filter handler can match log records by trace ID.
func NewTraceIDMiddleware(contextKey any, opts ...TraceIDMiddlewareOpt) *TraceIDMiddleware {
	m := &TraceIDMiddleware{contextKey: contextKey}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Handler wraps next and injects the trace ID into the request context.
func (m *TraceIDMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var traceID string
		if m.queryKey != "" {
			traceID = r.URL.Query().Get(m.queryKey)
		}
		if traceID == "" && m.headerKey != "" {
			traceID = r.Header.Get(m.headerKey)
		}
		if traceID != "" {
			r = r.WithContext(context.WithValue(r.Context(), m.contextKey, traceID))
		}
		next.ServeHTTP(w, r)
	})
}
