package slogx

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileHandler_BasicLogging(t *testing.T) {
	tmpDir := t.TempDir()

	handler, err := NewFileHandler(tmpDir)
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}
	defer handler.Close()

	logger := slog.New(handler)

	logger.Info("test message 1", "key", "value")
	logger.Warn("test message 2", "count", 42)
	logger.Error("test message 3", "error", "something went wrong")

	if err := handler.Flush(); err != nil {
		t.Fatalf("failed to flush: %v", err)
	}

	sessions, err := ListSessions(tmpDir)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	reader, err := NewLogReader(sessions[0].Path, 10)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}

	entries, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("failed to read all entries: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].Message != "test message 1" {
		t.Errorf("expected message 'test message 1', got '%s'", entries[0].Message)
	}
	if entries[1].Level != "WARN" {
		t.Errorf("expected level 'WARN', got '%s'", entries[1].Level)
	}
	if entries[2].Message != "test message 3" {
		t.Errorf("expected message 'test message 3', got '%s'", entries[2].Message)
	}
}

func TestFileHandler_RotationByCount(t *testing.T) {
	tmpDir := t.TempDir()

	handler, err := NewFileHandler(tmpDir, WithMaxRecords(5))
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}
	defer handler.Close()

	logger := slog.New(handler)

	for i := 0; i < 12; i++ {
		logger.Info("message", "index", i)
	}

	handler.Flush()

	sessions, err := ListSessions(tmpDir)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}

	stats, err := GetSessionStats(sessions[0].Path)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}

	if len(stats) != 3 {
		t.Fatalf("expected 3 files, got %d", len(stats))
	}

	if stats[0].FileName != "00001.log" {
		t.Errorf("expected first file '00001.log', got '%s'", stats[0].FileName)
	}
	if stats[1].FileName != "00002.log" {
		t.Errorf("expected second file '00002.log', got '%s'", stats[1].FileName)
	}
	if stats[2].FileName != "00003.log" {
		t.Errorf("expected third file '00003.log', got '%s'", stats[2].FileName)
	}
}

func TestFileHandler_RotationBySize(t *testing.T) {
	tmpDir := t.TempDir()

	handler, err := NewFileHandler(tmpDir, WithMaxBytes(500))
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}
	defer handler.Close()

	logger := slog.New(handler)

	for i := 0; i < 20; i++ {
		logger.Info("this is a longer message to fill up the file faster", "index", i, "data", "some extra data here")
	}

	handler.Flush()

	sessions, err := ListSessions(tmpDir)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}

	stats, err := GetSessionStats(sessions[0].Path)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}

	if len(stats) < 2 {
		t.Errorf("expected at least 2 files due to size rotation, got %d", len(stats))
	}
}

func TestLogReader_Pagination(t *testing.T) {
	tmpDir := t.TempDir()

	handler, err := NewFileHandler(tmpDir, WithMaxRecords(10))
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}

	logger := slog.New(handler)

	for i := 0; i < 25; i++ {
		logger.Info("message", "index", i)
	}

	handler.Flush()
	handler.Close()

	sessions, err := ListSessions(tmpDir)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}

	reader, err := NewLogReader(sessions[0].Path, 7)
	if err != nil {
		t.Fatalf("failed to create reader: %v", err)
	}

	page1, err := reader.ReadPage(1, 0)
	if err != nil {
		t.Fatalf("failed to read page 1: %v", err)
	}

	if len(page1.Entries) != 7 {
		t.Errorf("expected 7 entries in page 1, got %d", len(page1.Entries))
	}
	if !page1.HasMore {
		t.Error("expected HasMore to be true for page 1")
	}

	page2, err := reader.ReadPage(page1.FileIndex, page1.LineOffset)
	if err != nil {
		t.Fatalf("failed to read page 2: %v", err)
	}

	if len(page2.Entries) != 7 {
		t.Errorf("expected 7 entries in page 2, got %d", len(page2.Entries))
	}

	allEntries := append(page1.Entries, page2.Entries...)
	nextFileIdx := page2.FileIndex
	nextLineOffset := page2.LineOffset

	for page2.HasMore {
		page, err := reader.ReadPage(nextFileIdx, nextLineOffset)
		if err != nil {
			t.Fatalf("failed to read next page: %v", err)
		}
		allEntries = append(allEntries, page.Entries...)
		nextFileIdx = page.FileIndex
		nextLineOffset = page.LineOffset
		page2 = page
	}

	if len(allEntries) != 25 {
		t.Errorf("expected total 25 entries, got %d", len(allEntries))
	}
}

func TestLogReader_StreamLogs(t *testing.T) {
	tmpDir := t.TempDir()

	handler, err := NewFileHandler(tmpDir)
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}

	logger := slog.New(handler)

	for i := 0; i < 10; i++ {
		logger.Info("stream test", "i", i)
	}

	handler.Flush()
	handler.Close()

	sessions, _ := ListSessions(tmpDir)
	reader, _ := NewLogReader(sessions[0].Path, 100)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	entries, errs := reader.StreamLogs(ctx)

	count := 0
	for entry := range entries {
		if entry.Message != "stream test" {
			t.Errorf("unexpected message: %s", entry.Message)
		}
		count++
	}

	select {
	case err := <-errs:
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	default:
	}

	if count != 10 {
		t.Errorf("expected 10 entries, got %d", count)
	}
}

func TestTailLogs(t *testing.T) {
	tmpDir := t.TempDir()

	handler, err := NewFileHandler(tmpDir, WithMaxRecords(5))
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}

	logger := slog.New(handler)

	for i := 0; i < 15; i++ {
		logger.Info("message", "index", i)
	}

	handler.Flush()
	handler.Close()

	sessions, _ := ListSessions(tmpDir)

	entries, err := TailLogs(sessions[0].Path, 5)
	if err != nil {
		t.Fatalf("failed to tail logs: %v", err)
	}

	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}

	for i, entry := range entries {
		expectedIdx := float64(10 + i)
		if idx, ok := entry.Attrs["index"].(float64); !ok || idx != expectedIdx {
			t.Errorf("expected index %v, got %v", expectedIdx, entry.Attrs["index"])
		}
	}
}

func TestSearchSession(t *testing.T) {
	tmpDir := t.TempDir()

	handler, err := NewFileHandler(tmpDir)
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}

	logger := slog.New(handler)

	logger.Info("normal message")
	logger.Warn("warning message")
	logger.Error("error message", "code", 500)
	logger.Info("another normal message")
	logger.Error("another error", "code", 404)

	handler.Flush()
	handler.Close()

	sessions, _ := ListSessions(tmpDir)

	results, err := SearchSession(sessions[0].Path, SearchCriteria{Level: "ERROR"}, 0)
	if err != nil {
		t.Fatalf("failed to search: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 error entries, got %d", len(results))
	}

	results, err = SearchSession(sessions[0].Path, SearchCriteria{Message: "another"}, 0)
	if err != nil {
		t.Fatalf("failed to search: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 entries with 'another', got %d", len(results))
	}
}

func TestMultiHandler(t *testing.T) {
	tmpDir := t.TempDir()

	fileHandler, err := NewFileHandler(tmpDir)
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}
	defer fileHandler.Close()

	tmpDir2 := t.TempDir()
	fileHandler2, err := NewFileHandler(tmpDir2)
	if err != nil {
		t.Fatalf("failed to create second file handler: %v", err)
	}
	defer fileHandler2.Close()

	multi := NewMultiHandler(fileHandler, fileHandler2)

	logger := slog.New(multi)
	logger.Info("multi handler test", "key", "value")

	multi.Flush()

	for _, dir := range []string{tmpDir, tmpDir2} {
		sessions, err := ListSessions(dir)
		if err != nil {
			t.Fatalf("failed to list sessions: %v", err)
		}
		if len(sessions) != 1 {
			t.Errorf("expected 1 session in %s, got %d", dir, len(sessions))
		}

		reader, _ := NewLogReader(sessions[0].Path, 10)
		entries, _ := reader.ReadAll()
		if len(entries) != 1 {
			t.Errorf("expected 1 entry in %s, got %d", dir, len(entries))
		}
	}
}

func TestLogIterator(t *testing.T) {
	tmpDir := t.TempDir()

	handler, err := NewFileHandler(tmpDir, WithMaxRecords(5))
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}

	logger := slog.New(handler)

	for i := 0; i < 12; i++ {
		logger.Info("iterator test", "i", i)
	}

	handler.Flush()
	handler.Close()

	sessions, _ := ListSessions(tmpDir)

	iter, err := NewLogIterator(sessions[0].Path)
	if err != nil {
		t.Fatalf("failed to create iterator: %v", err)
	}
	defer iter.Close()

	count := 0
	for entry, ok := iter.Next(); ok; entry, ok = iter.Next() {
		if entry.Message != "iterator test" {
			t.Errorf("unexpected message: %s", entry.Message)
		}
		count++
	}

	if iter.Error() != nil {
		t.Errorf("iterator error: %v", iter.Error())
	}

	if count != 12 {
		t.Errorf("expected 12 entries, got %d", count)
	}
}

func TestLogIterator_All(t *testing.T) {
	tmpDir := t.TempDir()

	handler, err := NewFileHandler(tmpDir)
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}

	logger := slog.New(handler)

	for i := 0; i < 5; i++ {
		logger.Info("range test", "i", i)
	}

	handler.Flush()
	handler.Close()

	sessions, _ := ListSessions(tmpDir)

	iter, err := NewLogIterator(sessions[0].Path)
	if err != nil {
		t.Fatalf("failed to create iterator: %v", err)
	}
	defer iter.Close()

	count := 0
	for entry := range iter.All() {
		if entry.Message != "range test" {
			t.Errorf("unexpected message: %s", entry.Message)
		}
		count++
	}

	if count != 5 {
		t.Errorf("expected 5 entries, got %d", count)
	}
}

func TestReadLatestSession(t *testing.T) {
	tmpDir := t.TempDir()

	handler1, _ := NewFileHandler(tmpDir)
	logger1 := slog.New(handler1)
	logger1.Info("first session")
	handler1.Flush()
	handler1.Close()

	time.Sleep(time.Second * 2)

	handler2, _ := NewFileHandler(tmpDir)
	logger2 := slog.New(handler2)
	logger2.Info("second session")
	handler2.Flush()
	handler2.Close()

	reader, err := ReadLatestSession(tmpDir, 10)
	if err != nil {
		t.Fatalf("failed to read latest session: %v", err)
	}

	entries, _ := reader.ReadAll()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	if entries[0].Message != "second session" {
		t.Errorf("expected message from second session, got: %s", entries[0].Message)
	}
}

func TestListSessions(t *testing.T) {
	tmpDir := t.TempDir()

	for i := 0; i < 3; i++ {
		handler, _ := NewFileHandler(tmpDir)
		logger := slog.New(handler)
		logger.Info("session", "num", i)
		handler.Flush()
		handler.Close()
		time.Sleep(time.Second * 2)
	}

	sessions, err := ListSessions(tmpDir)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}

	if len(sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(sessions))
	}

	for i := 0; i < len(sessions)-1; i++ {
		if sessions[i].Timestamp.Before(sessions[i+1].Timestamp) {
			t.Errorf("sessions not sorted newest first: %v before %v",
				sessions[i].Timestamp, sessions[i+1].Timestamp)
		}
	}
}

func TestCountLogs(t *testing.T) {
	tmpDir := t.TempDir()

	handler, err := NewFileHandler(tmpDir, WithMaxRecords(10))
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}

	logger := slog.New(handler)

	for i := 0; i < 25; i++ {
		logger.Info("count test", "i", i)
	}

	handler.Flush()
	handler.Close()

	sessions, _ := ListSessions(tmpDir)
	count, err := CountLogs(sessions[0].Path)
	if err != nil {
		t.Fatalf("failed to count logs: %v", err)
	}

	if count != 25 {
		t.Errorf("expected 25 logs, got %d", count)
	}
}

func TestGetSessionStats(t *testing.T) {
	tmpDir := t.TempDir()

	handler, err := NewFileHandler(tmpDir, WithMaxRecords(5))
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}

	logger := slog.New(handler)

	for i := 0; i < 12; i++ {
		logger.Info("stats test", "i", i)
	}

	handler.Flush()
	handler.Close()

	sessions, _ := ListSessions(tmpDir)
	stats, err := GetSessionStats(sessions[0].Path)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}

	if len(stats) != 3 {
		t.Fatalf("expected 3 files, got %d", len(stats))
	}

	totalLines := int64(0)
	for _, s := range stats {
		totalLines += s.LineCount
		if s.Size <= 0 {
			t.Errorf("expected positive file size, got %d", s.Size)
		}
	}

	if totalLines != 12 {
		t.Errorf("expected 12 total lines, got %d", totalLines)
	}
}

func TestExportSession(t *testing.T) {
	tmpDir := t.TempDir()

	handler, err := NewFileHandler(tmpDir, WithMaxRecords(5))
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}

	logger := slog.New(handler)

	for i := 0; i < 12; i++ {
		logger.Info("export test", "i", i)
	}

	handler.Flush()
	handler.Close()

	sessions, _ := ListSessions(tmpDir)

	exportPath := filepath.Join(tmpDir, "export.log")
	exportFile, err := os.Create(exportPath)
	if err != nil {
		t.Fatalf("failed to create export file: %v", err)
	}

	err = ExportSession(sessions[0].Path, exportFile)
	exportFile.Close()
	if err != nil {
		t.Fatalf("failed to export session: %v", err)
	}

	content, _ := os.ReadFile(exportPath)
	lines := 0
	for _, b := range content {
		if b == '\n' {
			lines++
		}
	}

	if lines != 12 {
		t.Errorf("expected 12 lines in export, got %d", lines)
	}
}

func TestFileHandler_WithFilterHandler(t *testing.T) {
	tmpDir := t.TempDir()

	fileHandler, err := NewFileHandler(tmpDir)
	if err != nil {
		t.Fatalf("failed to create file handler: %v", err)
	}
	defer fileHandler.Close()

	filterHandler := NewFilterHandler(fileHandler, WithDefaultLevel(slog.LevelWarn))

	logger := slog.New(filterHandler)

	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")
	logger.Error("error message")

	fileHandler.Flush()

	sessions, _ := ListSessions(tmpDir)
	reader, _ := NewLogReader(sessions[0].Path, 10)
	entries, _ := reader.ReadAll()

	if len(entries) != 2 {
		t.Errorf("expected 2 entries (filtered), got %d", len(entries))
	}
}

func TestParseFileIndex(t *testing.T) {
	tests := []struct {
		filename string
		expected int
		hasError bool
	}{
		{"00001.log", 1, false},
		{"00042.log", 42, false},
		{"99999.log", 99999, false},
		{"invalid.log", 0, true},
	}

	for _, tt := range tests {
		result, err := ParseFileIndex(tt.filename)
		if tt.hasError {
			if err == nil {
				t.Errorf("expected error for %s, got none", tt.filename)
			}
		} else {
			if err != nil {
				t.Errorf("unexpected error for %s: %v", tt.filename, err)
			}
			if result != tt.expected {
				t.Errorf("expected %d for %s, got %d", tt.expected, tt.filename, result)
			}
		}
	}
}
