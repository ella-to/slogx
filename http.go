package slogx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type HttpHandler struct {
	log *FilterHandler
	mux *http.ServeMux
}

var _ http.Handler = (*HttpHandler)(nil)

func NewHttpHandler(log *FilterHandler) *HttpHandler {
	mux := http.NewServeMux()

	h := &HttpHandler{log: log, mux: mux}

	mux.HandleFunc("level", h.handleLogLevel)
	mux.HandleFunc("enable", h.handleLogEnable)
	mux.HandleFunc("trace-id", h.handleTraceId)

	return h
}

func (h *HttpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

func (h *HttpHandler) handleTraceId(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "POST":
		traceId := r.URL.Query().Get("trace-id")
		h.log.SetFilterTraceId(traceId)
		w.WriteHeader(http.StatusOK)

	case "DELETE":
		h.log.SetFilterTraceId("")
		w.WriteHeader(http.StatusOK)

	case "GET":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"trace_id": h.log.GetFilterTraceId(),
		})
	}
}
