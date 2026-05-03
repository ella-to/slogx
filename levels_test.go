package slogx

import (
	"log/slog"
	"testing"
)

func TestLevelSetBasicThreshold(t *testing.T) {
	ls := newLevelSet(slog.LevelInfo, nil)
	cases := []struct {
		level slog.Level
		want  bool
	}{
		{slog.LevelDebug, false},
		{slog.LevelInfo, true},
		{slog.LevelWarn, true},
		{slog.LevelError, true},
	}
	for _, c := range cases {
		if got := ls.Allow("ella.to/x", c.level); got != c.want {
			t.Errorf("Allow(%v)=%v want %v", c.level, got, c.want)
		}
	}
}

func TestLevelSetGlobalOff(t *testing.T) {
	ls := newLevelSet(LevelOff, nil)
	if ls.Allow("anything", slog.LevelError) {
		t.Errorf("expected all records to be dropped when global is OFF")
	}
}

func TestLevelSetPerPackageOverride(t *testing.T) {
	ls := newLevelSet(slog.LevelInfo, []packageLevel{
		{Pattern: "ella.to/quiet", Level: LevelOff},
		{Pattern: "ella.to/verbose", Level: slog.LevelDebug},
	})

	if ls.Allow("ella.to/quiet", slog.LevelError) {
		t.Errorf("quiet package should be silenced")
	}
	if ls.Allow("ella.to/quiet/sub", slog.LevelError) {
		t.Errorf("sub-package inherits override")
	}
	if !ls.Allow("ella.to/verbose", slog.LevelDebug) {
		t.Errorf("verbose should accept debug")
	}
	if ls.Allow("ella.to/other", slog.LevelDebug) {
		t.Errorf("non-matching package falls back to INFO and drops DEBUG")
	}
}

func TestLevelSetLongestPrefixWins(t *testing.T) {
	ls := newLevelSet(slog.LevelInfo, []packageLevel{
		{Pattern: "ella.to", Level: slog.LevelError},
		{Pattern: "ella.to/app/api", Level: slog.LevelDebug},
	})
	// Under "ella.to" but not "ella.to/app/api" -> ERROR threshold.
	if ls.Allow("ella.to/app", slog.LevelInfo) {
		t.Errorf("ella.to prefix should require ERROR")
	}
	// Under "ella.to/app/api" -> DEBUG threshold.
	if !ls.Allow("ella.to/app/api/users", slog.LevelDebug) {
		t.Errorf("longest prefix ella.to/app/api should allow DEBUG")
	}
}

func TestLevelSetDynamicUpdates(t *testing.T) {
	ls := newLevelSet(slog.LevelInfo, nil)
	if ls.Allow("p", slog.LevelDebug) {
		t.Fatalf("expected debug dropped initially")
	}
	ls.SetGlobal(slog.LevelDebug)
	if !ls.Allow("p", slog.LevelDebug) {
		t.Fatalf("expected debug allowed after SetGlobal")
	}
	ls.SetPackage("p", LevelOff)
	if ls.Allow("p", slog.LevelError) {
		t.Fatalf("expected silenced after SetPackage OFF")
	}
	ls.UnsetPackage("p")
	if !ls.Allow("p", slog.LevelDebug) {
		t.Fatalf("expected debug allowed after UnsetPackage")
	}
}

func TestLevelSetFloorReflectsMinimum(t *testing.T) {
	ls := newLevelSet(slog.LevelInfo, []packageLevel{
		{Pattern: "p", Level: slog.LevelDebug},
	})
	if ls.Floor() != slog.LevelDebug {
		t.Fatalf("floor = %v want DEBUG", ls.Floor())
	}
	ls.UnsetPackage("p")
	if ls.Floor() != slog.LevelInfo {
		t.Fatalf("floor = %v want INFO after unset", ls.Floor())
	}
}

func TestParseLevelRoundTrip(t *testing.T) {
	for _, name := range []string{"OFF", "DEBUG", "INFO", "WARN", "ERROR"} {
		lvl, err := ParseLevel(name)
		if err != nil {
			t.Fatalf("parse %q: %v", name, err)
		}
		if LevelName(lvl) != name {
			t.Fatalf("round trip %q -> %v -> %q", name, lvl, LevelName(lvl))
		}
	}
	if _, err := ParseLevel("nope"); err == nil {
		t.Fatalf("expected error for invalid level")
	}
}
