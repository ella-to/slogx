package slogx

import (
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// LevelOff disables logging entirely for a scope (global or a package).
// It is just a very large slog.Level value so that no real record can meet
// its threshold.
const LevelOff slog.Level = math.MaxInt32

// levelSet is the filter backing store: a global minimum level plus a set of
// per-package (prefix) overrides. Thread-safe with an invalidating decision
// cache keyed by package path.
type levelSet struct {
	mu       sync.RWMutex
	global   slog.Level
	packages []packageLevel // sorted by descending pattern length for longest-prefix match

	// cache: package path -> slog.Level (effective min level for that package).
	cache atomic.Pointer[sync.Map]

	// floor is the minimum of global and every package level, cached so the
	// hot Enabled() path is a single atomic read.
	floor atomic.Int64
}

type packageLevel struct {
	Pattern string     `json:"package"`
	Level   slog.Level `json:"level"`
}

func newLevelSet(global slog.Level, pkgs []packageLevel) *levelSet {
	ls := &levelSet{global: global}
	ls.cache.Store(&sync.Map{})
	if len(pkgs) > 0 {
		ls.packages = append([]packageLevel(nil), pkgs...)
		sortByPatternLen(ls.packages)
	}
	ls.recomputeFloor()
	return ls
}

// EffectiveLevel returns the minimum level that records from pkg must meet in
// order to be emitted. If pkg is empty or has no matching package override,
// the global level is returned.
func (l *levelSet) EffectiveLevel(pkg string) slog.Level {
	if pkg != "" {
		if v, ok := l.cache.Load().Load(pkg); ok {
			return slog.Level(v.(int64))
		}
	}

	l.mu.RLock()
	eff := l.global
	for _, p := range l.packages {
		if pkg == p.Pattern || strings.HasPrefix(pkg, p.Pattern+"/") {
			eff = p.Level
			break // longest prefix wins (list is sorted by descending length)
		}
	}
	l.mu.RUnlock()

	if pkg != "" {
		l.cache.Load().Store(pkg, int64(eff))
	}
	return eff
}

// Allow reports whether a record at recLevel from pkg should be emitted.
func (l *levelSet) Allow(pkg string, recLevel slog.Level) bool {
	return recLevel >= l.EffectiveLevel(pkg)
}

// Floor returns the minimum of the global level and any package level. Used
// as a fast-path in slog.Handler.Enabled so slog can avoid building records
// that couldn't possibly pass any rule.
func (l *levelSet) Floor() slog.Level {
	return slog.Level(l.floor.Load())
}

// Global returns the current global level.
func (l *levelSet) Global() slog.Level {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.global
}

// Packages returns a copy of the current per-package overrides (sorted by
// pattern ascending for display).
func (l *levelSet) Packages() []packageLevel {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := append([]packageLevel(nil), l.packages...)
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return out
}

// SetGlobal updates the global level.
func (l *levelSet) SetGlobal(level slog.Level) {
	l.mu.Lock()
	l.global = level
	l.mu.Unlock()
	l.invalidate()
}

// SetPackage adds or replaces a per-package override.
func (l *levelSet) SetPackage(pattern string, level slog.Level) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return
	}
	l.mu.Lock()
	found := false
	for i, p := range l.packages {
		if p.Pattern == pattern {
			l.packages[i].Level = level
			found = true
			break
		}
	}
	if !found {
		l.packages = append(l.packages, packageLevel{Pattern: pattern, Level: level})
		sortByPatternLen(l.packages)
	}
	l.mu.Unlock()
	l.invalidate()
}

// UnsetPackage removes a per-package override.
func (l *levelSet) UnsetPackage(pattern string) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return
	}
	l.mu.Lock()
	for i, p := range l.packages {
		if p.Pattern == pattern {
			l.packages = append(l.packages[:i], l.packages[i+1:]...)
			break
		}
	}
	l.mu.Unlock()
	l.invalidate()
}

func (l *levelSet) invalidate() {
	l.cache.Store(&sync.Map{})
	l.recomputeFloor()
}

func (l *levelSet) recomputeFloor() {
	l.mu.RLock()
	f := l.global
	for _, p := range l.packages {
		if p.Level < f {
			f = p.Level
		}
	}
	l.mu.RUnlock()
	l.floor.Store(int64(f))
}

func sortByPatternLen(pkgs []packageLevel) {
	sort.Slice(pkgs, func(i, j int) bool {
		return len(pkgs[i].Pattern) > len(pkgs[j].Pattern)
	})
}

// -----------------------------------------------------------------------------
// level name <-> slog.Level conversion
// -----------------------------------------------------------------------------

// ParseLevel accepts a case-insensitive level name ("off", "debug", "info",
// "warn", "error") and returns the corresponding slog.Level (or LevelOff).
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "OFF":
		return LevelOff, nil
	case "DEBUG":
		return slog.LevelDebug, nil
	case "INFO":
		return slog.LevelInfo, nil
	case "WARN", "WARNING":
		return slog.LevelWarn, nil
	case "ERROR", "ERR":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("slogx: unknown level %q", s)
	}
}

// LevelName returns the canonical name for a slog.Level used in slogx
// config ("OFF", "DEBUG", "INFO", "WARN", "ERROR"). Offsets are preserved for
// levels that don't map to a canonical name.
func LevelName(l slog.Level) string {
	switch {
	case l >= LevelOff:
		return "OFF"
	case l == slog.LevelDebug:
		return "DEBUG"
	case l == slog.LevelInfo:
		return "INFO"
	case l == slog.LevelWarn:
		return "WARN"
	case l == slog.LevelError:
		return "ERROR"
	default:
		return l.String()
	}
}
