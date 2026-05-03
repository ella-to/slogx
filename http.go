package slogx

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HeaderTraceID is the HTTP header the middleware reads/writes to carry a
// trace id across hops.
const HeaderTraceID = "X-TRACE-ID"

// QueryTraceID is the URL query parameter that overrides the header when
// present. Matches the user-facing spec.
const QueryTraceID = "log_trace_id"

// HttpHandler returns the admin/UI handler for the currently active slogx.
// Setup must have been called first.
//
// Endpoints (all JSON unless noted):
//
//	GET  /                       -> embedded index.html
//	GET  /traces?limit=100       -> [TraceSummary, ...]
//	GET  /logs?traceId=...&limit -> [Record, ...]
//	GET  /levels                 -> { "global": "INFO", "packages": [...] }
//	PATCH /levels                -> { "global"?: "DEBUG",
//	                                  "set"?: {"pkg":"DEBUG"},
//	                                  "unset"?: ["pkg"] }
func HttpHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /logs", handleLogs)
	mux.HandleFunc("GET /traces", handleTraces)

	mux.HandleFunc("GET /levels", handleGetLevels)
	mux.HandleFunc("PATCH /levels", handlePatchLevels)

	// Static UI. The index page is served at the root; other assets (if any)
	// under /static/*.
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(mustSub(uiFS, "ui")))))
	mux.HandleFunc("GET /{$}", handleIndex)

	return mux
}

// Middleware establishes a trace-id context and a root span for each inbound
// HTTP request so that logs emitted during request processing show up under a
// single trace in the UI.
//
// Trace-id resolution order:
//  1. ?log_trace_id=...     (override)
//  2. X-TRACE-ID header     (propagation)
//  3. generated
//
// The resolved trace-id is also echoed on the response header.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := strings.TrimSpace(r.URL.Query().Get(QueryTraceID))
		if traceID == "" {
			traceID = strings.TrimSpace(r.Header.Get(HeaderTraceID))
		}

		ctx := r.Context()
		if traceID != "" {
			ctx = WithTraceID(ctx, traceID)
		}
		ctx = Context(ctx)

		w.Header().Set(HeaderTraceID, TraceID(ctx))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// -----------------------------------------------------------------------------
// handlers
// -----------------------------------------------------------------------------

func handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(uiFS, "ui/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The UI ships embedded in the binary; disable caching so library upgrades
	// always deliver the latest JS without a hard-refresh.
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	_, _ = w.Write(data)
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	h := getActive()
	if h == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "slogx not initialized"})
		return
	}
	q := Query{
		TraceID: r.URL.Query().Get("traceId"),
		Limit:   parseInt(r.URL.Query().Get("limit"), 1000),
	}
	if s := r.URL.Query().Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			q.Since = t
		}
	}
	records := h.Store().Query(q)
	writeJSON(w, http.StatusOK, records)
}

func handleTraces(w http.ResponseWriter, r *http.Request) {
	h := getActive()
	if h == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "slogx not initialized"})
		return
	}
	limit := parseInt(r.URL.Query().Get("limit"), 100)
	writeJSON(w, http.StatusOK, h.Store().Traces(limit))
}

// levelsResponse is the JSON representation of the level set (global + per
// package), using human-readable names ("OFF", "DEBUG", "INFO", "WARN",
// "ERROR").
type levelsResponse struct {
	Global   string          `json:"global"`
	Packages []packageLevelJ `json:"packages"`
}

type packageLevelJ struct {
	Package string `json:"package"`
	Level   string `json:"level"`
}

type patchLevelsBody struct {
	Global *string           `json:"global,omitempty"`
	Set    map[string]string `json:"set,omitempty"`
	Unset  []string          `json:"unset,omitempty"`
}

func handleGetLevels(w http.ResponseWriter, r *http.Request) {
	h := getActive()
	if h == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "slogx not initialized"})
		return
	}
	writeJSON(w, http.StatusOK, snapshotLevels(h.Levels()))
}

func handlePatchLevels(w http.ResponseWriter, r *http.Request) {
	h := getActive()
	if h == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "slogx not initialized"})
		return
	}
	var body patchLevelsBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	levels := h.Levels()

	if body.Global != nil {
		lvl, err := ParseLevel(*body.Global)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		levels.SetGlobal(lvl)
	}
	for pkg, name := range body.Set {
		lvl, err := ParseLevel(name)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		levels.SetPackage(pkg, lvl)
	}
	for _, pkg := range body.Unset {
		levels.UnsetPackage(pkg)
	}

	writeJSON(w, http.StatusOK, snapshotLevels(levels))
}

func snapshotLevels(ls *levelSet) levelsResponse {
	pkgs := ls.Packages()
	out := levelsResponse{
		Global:   LevelName(ls.Global()),
		Packages: make([]packageLevelJ, 0, len(pkgs)),
	}
	for _, p := range pkgs {
		out.Packages = append(out.Packages, packageLevelJ{Package: p.Pattern, Level: LevelName(p.Level)})
	}
	return out
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func parseInt(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
