package slogx

import (
	"fmt"
	"testing"
	"time"
)

func TestRingStoreAppendAndQueryByTrace(t *testing.T) {
	s := newRingStore(8)
	base := time.Now()
	for i := 0; i < 5; i++ {
		s.Append(Record{Time: base.Add(time.Duration(i) * time.Millisecond), Message: fmt.Sprintf("m%d", i), TraceID: "a"})
	}
	for i := 0; i < 3; i++ {
		s.Append(Record{Time: base.Add(time.Duration(i) * time.Millisecond), Message: fmt.Sprintf("m%d", i), TraceID: "b"})
	}

	got := s.Query(Query{TraceID: "a"})
	if len(got) != 5 {
		t.Fatalf("trace a: got %d want 5", len(got))
	}
	got = s.Query(Query{TraceID: "b"})
	if len(got) != 3 {
		t.Fatalf("trace b: got %d want 3", len(got))
	}
}

func TestRingStoreEvictionCleansIndex(t *testing.T) {
	s := newRingStore(4)
	for i := 0; i < 10; i++ {
		s.Append(Record{Time: time.Now().Add(time.Duration(i) * time.Millisecond), TraceID: fmt.Sprintf("t%d", i)})
	}

	// Only the most recent 4 traces should remain.
	got := s.Traces(100)
	if len(got) != 4 {
		t.Fatalf("expected 4 surviving traces, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.TraceID] = true
	}
	for i := 6; i < 10; i++ {
		if !seen[fmt.Sprintf("t%d", i)] {
			t.Errorf("expected t%d to survive", i)
		}
	}
	for i := 0; i < 6; i++ {
		if seen[fmt.Sprintf("t%d", i)] {
			t.Errorf("t%d should have been evicted", i)
		}
	}
}

func TestRingStoreQueryOrdered(t *testing.T) {
	s := newRingStore(8)
	now := time.Now()
	// Append out of time order on purpose.
	s.Append(Record{Time: now.Add(2 * time.Second), TraceID: "x", Message: "late"})
	s.Append(Record{Time: now.Add(1 * time.Second), TraceID: "x", Message: "mid"})
	s.Append(Record{Time: now, TraceID: "x", Message: "early"})
	got := s.Query(Query{TraceID: "x"})
	if got[0].Message != "early" || got[2].Message != "late" {
		t.Fatalf("not sorted by time ascending: %v", got)
	}
}

func TestRingStoreTracesSummary(t *testing.T) {
	s := newRingStore(8)
	s.Append(Record{Time: time.Now(), TraceID: "x", Level: 0, Message: "first"})
	s.Append(Record{Time: time.Now(), TraceID: "x", Level: 8, Message: "err"})
	sums := s.Traces(10)
	if len(sums) != 1 {
		t.Fatalf("want 1 summary, got %d", len(sums))
	}
	if sums[0].Count != 2 {
		t.Fatalf("count %d want 2", sums[0].Count)
	}
	if int(sums[0].MaxLevel) != 8 {
		t.Fatalf("maxLevel = %v want 8", sums[0].MaxLevel)
	}
}
