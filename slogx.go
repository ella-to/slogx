package slogx

import (
	"context"
	"log/slog"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
)

type FilterHandler struct {
	inner              slog.Handler
	defaultLevel       slog.Level
	minLevel           slog.Level // cached minimum level for Enabled() fast path
	matcher            *pathMatcher
	disabled           bool
	exclusiveFiltering bool // if true, only logs from paths with explicit rules are shown
	traceIdKey         any
	filterTraceId      string // if set, only logs with this trace ID will be shown
	mu                 sync.RWMutex
}

type FilterHandlerOpt func(*FilterHandler)

// WithDefaultLevel sets the default log level for all packages
func WithDefaultLevel(level slog.Level) FilterHandlerOpt {
	return func(h *FilterHandler) {
		h.defaultLevel = level
		h.recalcMinLevel()
	}
}

// WithLogLevel sets the log level for a specific package path
func WithLogLevel(path string, level slog.Level) FilterHandlerOpt {
	return func(h *FilterHandler) {
		h.matcher.add(path, level)
		h.recalcMinLevel()
	}
}

// WithExclusiveFiltering enables exclusive filtering mode.
// When enabled, only logs from paths that have explicit rules (set via WithLogLevel)
// will be shown. All other logs will be filtered out regardless of their level.
func WithExclusiveFiltering() FilterHandlerOpt {
	return func(h *FilterHandler) {
		h.exclusiveFiltering = true
	}
}

func NewFilterHandler(inner slog.Handler, opts ...FilterHandlerOpt) *FilterHandler {
	h := &FilterHandler{
		inner:              inner,
		defaultLevel:       slog.LevelInfo,
		minLevel:           slog.LevelInfo,
		matcher:            newPathMatcher(),
		disabled:           false,
		exclusiveFiltering: false,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// --- Runtime Configuration Methods ---

// SetEnabled enables or disables all logging
func (h *FilterHandler) SetEnabled(enabled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.disabled = !enabled
}

// SetDefaultLevel changes the default log level at runtime
func (h *FilterHandler) SetDefaultLevel(level slog.Level) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.defaultLevel = level
	h.recalcMinLevelLocked()
}

// SetLogLevel sets or updates the log level for a specific path at runtime
func (h *FilterHandler) SetLogLevel(path string, level slog.Level) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.matcher.set(path, level)
	h.recalcMinLevelLocked()
}

// RemoveLogLevel removes a path-specific log level, falling back to default
func (h *FilterHandler) RemoveLogLevel(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.matcher.remove(path)
	h.recalcMinLevelLocked()
}

// SetExclusiveFiltering enables or disables exclusive filtering mode at runtime
func (h *FilterHandler) SetExclusiveFiltering(exclusive bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.exclusiveFiltering = exclusive
}

// SetTraceIdKey configures the key used to extract trace ID from context
func (h *FilterHandler) SetTraceIdKey(key any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.traceIdKey = key
}

// SetFilterTraceId filters logs to only show those with the specified trace ID
// If set to empty string, disables trace ID filtering
func (h *FilterHandler) SetFilterTraceId(traceId string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.filterTraceId = traceId
}

// GetFilterTraceId returns the current trace ID filter
func (h *FilterHandler) GetFilterTraceId() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.filterTraceId
}

// GetConfig returns the current configuration (for debugging/inspection)
func (h *FilterHandler) GetConfig() (defaultLevel slog.Level, rules map[string]slog.Level, disabled bool, exclusiveFiltering bool, traceIdKey any, filterTraceId string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	rules = make(map[string]slog.Level, len(h.matcher.rules))
	for _, r := range h.matcher.rules {
		rules[r.prefix] = r.level
	}
	return h.defaultLevel, rules, h.disabled, h.exclusiveFiltering, h.traceIdKey, h.filterTraceId
}

func (h *FilterHandler) HttpHandler() http.Handler {
	return NewHttpHandler(h)
}

func (h *FilterHandler) recalcMinLevelLocked() {
	h.minLevel = h.defaultLevel
	for _, level := range h.matcher.levels() {
		if level < h.minLevel {
			h.minLevel = level
		}
	}
}

func (h *FilterHandler) recalcMinLevel() {
	h.minLevel = h.defaultLevel
	for _, level := range h.matcher.levels() {
		if level < h.minLevel {
			h.minLevel = level
		}
	}
}

func (h *FilterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.disabled {
		return false
	}
	return level >= h.minLevel
}

func (h *FilterHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.disabled {
		return nil
	}

	effectiveLevel := h.defaultLevel
	hasExplicitRule := false

	if r.PC != 0 {
		frames := runtime.CallersFrames([]uintptr{r.PC})
		if frame, _ := frames.Next(); frame.PC != 0 {
			if level, ok := h.matcher.match(frame.Function); ok {
				effectiveLevel = level
				hasExplicitRule = true
			}
		}
	}

	// In exclusive mode, only logs from paths with explicit rules are shown
	if h.exclusiveFiltering && !hasExplicitRule {
		return nil
	}

	if r.Level < effectiveLevel {
		return nil
	}

	// Check if trace ID filtering is enabled and this log matches
	if h.filterTraceId != "" {
		if !h.logContainsTraceId(ctx, r) {
			return nil
		}
	}

	return h.inner.Handle(ctx, r)
}

// logContainsTraceId checks if the log record contains the filter trace ID
func (h *FilterHandler) logContainsTraceId(ctx context.Context, r slog.Record) bool {
	// Extract trace ID from context if key is set
	if h.traceIdKey != nil && ctx != nil {
		if traceId := ctx.Value(h.traceIdKey); traceId != nil {
			if traceIdStr, ok := traceId.(string); ok && traceIdStr == h.filterTraceId {
				return true
			}
		}
	}

	// Check if trace ID is in the record attributes
	var found bool
	r.Attrs(func(attr slog.Attr) bool {
		// Check if this attribute matches the trace ID
		if h.compareAttrToTraceId(attr) {
			found = true
			return false
		}
		return true
	})
	return found
}

// compareAttrToTraceId recursively checks if an attribute contains the filter trace ID
func (h *FilterHandler) compareAttrToTraceId(attr slog.Attr) bool {
	// If the value is a group, recursively check group members
	if attr.Value.Kind() == slog.KindGroup {
		for _, groupAttr := range attr.Value.Group() {
			if h.compareAttrToTraceId(groupAttr) {
				return true
			}
		}
		return false
	}

	// Check if this attribute's value matches the filter trace ID
	if attr.Value.Kind() == slog.KindString {
		if attr.Value.String() == h.filterTraceId {
			return true
		}
	}

	return false
}

func (h *FilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return &FilterHandler{
		inner:              h.inner.WithAttrs(attrs),
		defaultLevel:       h.defaultLevel,
		minLevel:           h.minLevel,
		matcher:            h.matcher,
		disabled:           h.disabled,
		exclusiveFiltering: h.exclusiveFiltering,
		traceIdKey:         h.traceIdKey,
		filterTraceId:      h.filterTraceId,
	}
}

func (h *FilterHandler) WithGroup(name string) slog.Handler {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return &FilterHandler{
		inner:              h.inner.WithGroup(name),
		defaultLevel:       h.defaultLevel,
		minLevel:           h.minLevel,
		matcher:            h.matcher,
		disabled:           h.disabled,
		exclusiveFiltering: h.exclusiveFiltering,
		traceIdKey:         h.traceIdKey,
		filterTraceId:      h.filterTraceId,
	}
}

// pathMatcher uses a sorted slice for binary search prefix matching
// This is faster than a trie for typical use cases (< 100 rules)
// and has better cache locality
type pathMatcher struct {
	rules []pathRule
}

type pathRule struct {
	prefix string
	level  slog.Level
}

func newPathMatcher() *pathMatcher {
	return &pathMatcher{rules: make([]pathRule, 0, 16)}
}

func (m *pathMatcher) add(prefix string, level slog.Level) {
	m.rules = append(m.rules, pathRule{prefix: prefix, level: level})
	m.sortRules()
}

func (m *pathMatcher) set(prefix string, level slog.Level) {
	for i := range m.rules {
		if m.rules[i].prefix == prefix {
			m.rules[i].level = level
			return
		}
	}
	m.add(prefix, level)
}

func (m *pathMatcher) remove(prefix string) {
	for i := range m.rules {
		if m.rules[i].prefix == prefix {
			m.rules = append(m.rules[:i], m.rules[i+1:]...)
			return
		}
	}
}

func (m *pathMatcher) sortRules() {
	sort.Slice(m.rules, func(i, j int) bool {
		if len(m.rules[i].prefix) != len(m.rules[j].prefix) {
			return len(m.rules[i].prefix) > len(m.rules[j].prefix)
		}
		return m.rules[i].prefix < m.rules[j].prefix
	})
}

func (m *pathMatcher) match(path string) (slog.Level, bool) {
	// Linear scan through sorted rules (longest first)
	// For typical rule counts (< 50), this beats more complex structures
	for i := range m.rules {
		if strings.HasPrefix(path, m.rules[i].prefix) {
			return m.rules[i].level, true
		}
	}
	return 0, false
}

func (m *pathMatcher) levels() []slog.Level {
	levels := make([]slog.Level, len(m.rules))
	for i := range m.rules {
		levels[i] = m.rules[i].level
	}
	return levels
}
