package slogx

import (
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Record is the serialized form of a slog.Record as retained by a Store.
type Record struct {
	Time       time.Time      `json:"time"`
	Level      slog.Level     `json:"level"`
	Message    string         `json:"message"`
	Source     string         `json:"source,omitempty"`
	Package    string         `json:"package,omitempty"`
	TraceID    string         `json:"traceId,omitempty"`
	ParentID   string         `json:"parentId,omitempty"`
	SpanPath   string         `json:"spanPath,omitempty"`
	Concurrent bool           `json:"concurrent,omitempty"`
	Attrs      map[string]any `json:"attrs,omitempty"`
}

// Query restricts which records Query returns.
type Query struct {
	TraceID string
	Since   time.Time
	Limit   int
}

// TraceSummary is a lightweight aggregate used by the UI index.
type TraceSummary struct {
	TraceID   string    `json:"traceId"`
	FirstTime time.Time `json:"firstTime"`
	LastTime  time.Time `json:"lastTime"`
	Count     int       `json:"count"`
	RootMsg   string    `json:"rootMessage,omitempty"`
	MaxLevel  slog.Level `json:"maxLevel"`
}

// Store retains log records for later inspection.
type Store interface {
	Append(r Record)
	Query(q Query) []Record
	Traces(limit int) []TraceSummary
}

// ringStore is a bounded in-memory Store implemented as a circular buffer
// plus a secondary map index keyed by TraceID.
type ringStore struct {
	mu    sync.RWMutex
	buf   []Record
	size  int
	next  int    // next write position
	count int    // number of live records (<= size)
	seq   uint64 // monotonically increasing record counter

	// traceIndex maps traceId -> slice of positions in buf that currently
	// belong to that trace. Positions are cleaned up on overwrite.
	traceIndex map[string][]int

	// posSeq[i] is the seq assigned to buf[i]; used so stale index entries
	// (from a position that has been overwritten) can be detected.
	posSeq []uint64
}

func newRingStore(size int) *ringStore {
	if size <= 0 {
		size = DefaultRingBufferSize
	}
	return &ringStore{
		buf:        make([]Record, size),
		size:       size,
		traceIndex: make(map[string][]int),
		posSeq:     make([]uint64, size),
	}
}

// Append writes r into the ring, evicting the oldest entry if full.
func (s *ringStore) Append(r Record) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pos := s.next
	if s.count == s.size {
		old := s.buf[pos]
		if old.TraceID != "" {
			s.removeFromIndex(old.TraceID, pos)
		}
	} else {
		s.count++
	}

	s.seq++
	s.buf[pos] = r
	s.posSeq[pos] = s.seq

	if r.TraceID != "" {
		s.traceIndex[r.TraceID] = append(s.traceIndex[r.TraceID], pos)
	}

	s.next = (s.next + 1) % s.size
}

func (s *ringStore) removeFromIndex(traceID string, pos int) {
	idx := s.traceIndex[traceID]
	for i, p := range idx {
		if p == pos {
			idx = append(idx[:i], idx[i+1:]...)
			break
		}
	}
	if len(idx) == 0 {
		delete(s.traceIndex, traceID)
	} else {
		s.traceIndex[traceID] = idx
	}
}

// Query returns records matching q ordered by time ascending.
func (s *ringStore) Query(q Query) []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()

	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}

	var out []Record
	if q.TraceID != "" {
		positions := s.traceIndex[q.TraceID]
		out = make([]Record, 0, len(positions))
		for _, p := range positions {
			r := s.buf[p]
			if !q.Since.IsZero() && r.Time.Before(q.Since) {
				continue
			}
			out = append(out, r)
		}
	} else {
		out = make([]Record, 0, s.count)
		// Iterate from oldest to newest.
		start := (s.next - s.count + s.size) % s.size
		for i := 0; i < s.count; i++ {
			r := s.buf[(start+i)%s.size]
			if !q.Since.IsZero() && r.Time.Before(q.Since) {
				continue
			}
			out = append(out, r)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Traces summarizes recent traces, most-recent first.
func (s *ringStore) Traces(limit int) []TraceSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	sums := make([]TraceSummary, 0, len(s.traceIndex))
	for id, positions := range s.traceIndex {
		if len(positions) == 0 {
			continue
		}
		sum := TraceSummary{TraceID: id, Count: len(positions)}
		first := s.buf[positions[0]]
		sum.FirstTime = first.Time
		sum.LastTime = first.Time
		sum.RootMsg = first.Message
		sum.MaxLevel = first.Level
		for _, p := range positions {
			r := s.buf[p]
			if r.Time.Before(sum.FirstTime) {
				sum.FirstTime = r.Time
				sum.RootMsg = r.Message
			}
			if r.Time.After(sum.LastTime) {
				sum.LastTime = r.Time
			}
			if r.Level > sum.MaxLevel {
				sum.MaxLevel = r.Level
			}
		}
		sums = append(sums, sum)
	}

	sort.Slice(sums, func(i, j int) bool { return sums[i].LastTime.After(sums[j].LastTime) })
	if len(sums) > limit {
		sums = sums[:limit]
	}
	return sums
}
