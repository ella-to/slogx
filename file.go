package slogx

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FileHandler is a slog.Handler that writes logs to files with rotation support.
type FileHandler struct {
	mu sync.Mutex

	baseDir       string        // base directory for all logs
	sessionDir    string        // current session directory (timestamp-based)
	maxBytes      int64         // max bytes per log file (0 = no limit)
	maxRecords    int64         // max records per log file (0 = no limit)
	currentFile   *os.File      // current log file
	currentWriter *bufio.Writer // buffered writer for current file
	currentSize   int64         // current file size in bytes
	currentCount  int64         // current record count
	fileIndex     int           // current file index (1, 2, 3, ...)
	jsonHandler   *slog.JSONHandler
	closed        bool
}

// FileHandlerOpt is an option for configuring the FileHandler.
type FileHandlerOpt func(*FileHandler)

// WithMaxBytes sets the maximum size in bytes before rotating to a new file.
func WithMaxBytes(maxBytes int64) FileHandlerOpt {
	return func(h *FileHandler) {
		h.maxBytes = maxBytes
	}
}

// WithMaxRecords sets the maximum number of log records before rotating to a new file.
func WithMaxRecords(maxRecords int64) FileHandlerOpt {
	return func(h *FileHandler) {
		h.maxRecords = maxRecords
	}
}

// NewFileHandler creates a new FileHandler that writes logs to files.
// The directory structure will be: baseDir/<timestamp>/00001.log
// where timestamp is the current date and time when the handler is created.
func NewFileHandler(baseDir string, opts ...FileHandlerOpt) (*FileHandler, error) {
	// Create timestamp-based session directory
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	sessionDir := filepath.Join(baseDir, timestamp)

	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	h := &FileHandler{
		baseDir:    baseDir,
		sessionDir: sessionDir,
		fileIndex:  0,
	}

	for _, opt := range opts {
		opt(h)
	}

	if err := h.rotateFile(); err != nil {
		return nil, err
	}

	return h, nil
}

// rotateFile closes the current file (if any) and opens a new one.
func (h *FileHandler) rotateFile() error {
	// Close current file if open
	if h.currentWriter != nil {
		h.currentWriter.Flush()
	}
	if h.currentFile != nil {
		h.currentFile.Close()
	}

	// Increment file index
	h.fileIndex++

	// Create new file with padded index
	filename := fmt.Sprintf("%05d.log", h.fileIndex)
	filePath := filepath.Join(h.sessionDir, filename)

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("failed to create log file: %w", err)
	}

	h.currentFile = file
	h.currentWriter = bufio.NewWriter(file)
	h.currentSize = 0
	h.currentCount = 0

	// Create a new JSON handler that writes to our buffered writer
	h.jsonHandler = slog.NewJSONHandler(h.currentWriter, &slog.HandlerOptions{
		Level: slog.LevelDebug, // Accept all levels, filtering is done by FilterHandler
	})

	return nil
}

// needsRotation checks if the current file needs to be rotated.
func (h *FileHandler) needsRotation() bool {
	if h.maxBytes > 0 && h.currentSize >= h.maxBytes {
		return true
	}
	if h.maxRecords > 0 && h.currentCount >= h.maxRecords {
		return true
	}
	return false
}

// Enabled implements slog.Handler.
func (h *FileHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true // Accept all levels
}

// Handle implements slog.Handler.
func (h *FileHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return fmt.Errorf("file handler is closed")
	}

	// Check if rotation is needed before writing
	if h.needsRotation() {
		if err := h.rotateFile(); err != nil {
			return err
		}
	}

	// Get the size before writing
	sizeBefore := h.currentWriter.Buffered()

	// Write the log record
	if err := h.jsonHandler.Handle(ctx, r); err != nil {
		return err
	}

	// Estimate the size written
	sizeAfter := h.currentWriter.Buffered()
	written := int64(sizeAfter - sizeBefore)
	if written < 0 {
		// Buffer was flushed, estimate from the record
		written = 256 // reasonable estimate for a log line
	}

	h.currentSize += written
	h.currentCount++

	// Flush after every write for real-time log visibility
	h.currentWriter.Flush()

	return nil
}

// WithAttrs implements slog.Handler.
func (h *FileHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Create a new FileHandler that shares the same file but has additional attrs
	return &fileHandlerWithAttrs{
		parent: h,
		attrs:  attrs,
	}
}

// WithGroup implements slog.Handler.
func (h *FileHandler) WithGroup(name string) slog.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()
	return &fileHandlerWithGroup{
		parent: h,
		group:  name,
	}
}

// Close flushes and closes the current log file.
func (h *FileHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.closed = true

	if h.currentWriter != nil {
		h.currentWriter.Flush()
	}
	if h.currentFile != nil {
		return h.currentFile.Close()
	}
	return nil
}

// Flush forces a flush of the current buffer to disk.
func (h *FileHandler) Flush() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.currentWriter != nil {
		if err := h.currentWriter.Flush(); err != nil {
			return err
		}
	}
	if h.currentFile != nil {
		return h.currentFile.Sync()
	}
	return nil
}

// SessionDir returns the current session directory path.
func (h *FileHandler) SessionDir() string {
	return h.sessionDir
}

// fileHandlerWithAttrs wraps FileHandler with additional attributes.
type fileHandlerWithAttrs struct {
	parent *FileHandler
	attrs  []slog.Attr
}

func (h *fileHandlerWithAttrs) Enabled(ctx context.Context, level slog.Level) bool {
	return h.parent.Enabled(ctx, level)
}

func (h *fileHandlerWithAttrs) Handle(ctx context.Context, r slog.Record) error {
	r.AddAttrs(h.attrs...)
	return h.parent.Handle(ctx, r)
}

func (h *fileHandlerWithAttrs) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &fileHandlerWithAttrs{
		parent: h.parent,
		attrs:  append(h.attrs, attrs...),
	}
}

func (h *fileHandlerWithAttrs) WithGroup(name string) slog.Handler {
	return &fileHandlerWithGroup{
		parent: h.parent,
		group:  name,
		attrs:  h.attrs,
	}
}

// fileHandlerWithGroup wraps FileHandler with a group.
type fileHandlerWithGroup struct {
	parent *FileHandler
	group  string
	attrs  []slog.Attr
}

func (h *fileHandlerWithGroup) Enabled(ctx context.Context, level slog.Level) bool {
	return h.parent.Enabled(ctx, level)
}

func (h *fileHandlerWithGroup) Handle(ctx context.Context, r slog.Record) error {
	if len(h.attrs) > 0 {
		r.AddAttrs(h.attrs...)
	}
	// Note: Group handling would need more sophisticated implementation
	// This is a simplified version
	return h.parent.Handle(ctx, r)
}

func (h *fileHandlerWithGroup) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &fileHandlerWithGroup{
		parent: h.parent,
		group:  h.group,
		attrs:  append(h.attrs, attrs...),
	}
}

func (h *fileHandlerWithGroup) WithGroup(name string) slog.Handler {
	return &fileHandlerWithGroup{
		parent: h.parent,
		group:  h.group + "." + name,
		attrs:  h.attrs,
	}
}

// --- Log Reading Functions ---

// LogSession represents a logging session (a timestamp-based directory).
type LogSession struct {
	Name      string    // directory name (timestamp)
	Path      string    // full path to the session directory
	Timestamp time.Time // parsed timestamp
	FileCount int       // number of log files in the session
}

// LogEntry represents a single log entry read from a file.
type LogEntry struct {
	Time    time.Time              `json:"time"`
	Level   string                 `json:"level"`
	Message string                 `json:"msg"`
	Attrs   map[string]interface{} `json:"-"` // all other attributes
	Raw     json.RawMessage        `json:"-"` // the raw JSON for custom parsing
}

// LogPage represents a page of log entries with pagination info.
type LogPage struct {
	Entries    []LogEntry `json:"entries"`
	TotalCount int        `json:"total_count"` // total entries across all files (estimate)
	PageSize   int        `json:"page_size"`
	PageNumber int        `json:"page_number"`
	HasMore    bool       `json:"has_more"`
	FileIndex  int        `json:"file_index"`  // current file being read
	LineOffset int        `json:"line_offset"` // line offset within the file
}

// ListSessions returns all logging sessions in the base directory.
// Sessions are sorted by timestamp, newest first.
func ListSessions(baseDir string) ([]LogSession, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read log directory: %w", err)
	}

	sessions := make([]LogSession, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		sessionPath := filepath.Join(baseDir, name)

		// Try to parse the timestamp from the directory name
		timestamp, err := time.Parse("2006-01-02_15-04-05", name)
		if err != nil {
			// Skip directories that don't match our timestamp format
			continue
		}

		// Count log files in the session
		fileCount := 0
		logFiles, _ := filepath.Glob(filepath.Join(sessionPath, "*.log"))
		fileCount = len(logFiles)

		sessions = append(sessions, LogSession{
			Name:      name,
			Path:      sessionPath,
			Timestamp: timestamp,
			FileCount: fileCount,
		})
	}

	// Sort by timestamp, newest first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Timestamp.After(sessions[j].Timestamp)
	})

	return sessions, nil
}

// LogReader provides paginated access to log files in a session.
type LogReader struct {
	sessionPath string
	files       []string // sorted list of log files
	pageSize    int
}

// NewLogReader creates a new LogReader for the given session directory.
func NewLogReader(sessionPath string, pageSize int) (*LogReader, error) {
	if pageSize <= 0 {
		pageSize = 100 // default page size
	}

	// Find all log files in the session
	pattern := filepath.Join(sessionPath, "*.log")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to list log files: %w", err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no log files found in session: %s", sessionPath)
	}

	// Sort files by name (which sorts by index since they're zero-padded)
	sort.Strings(files)

	return &LogReader{
		sessionPath: sessionPath,
		files:       files,
		pageSize:    pageSize,
	}, nil
}

// FileCount returns the number of log files in the session.
func (r *LogReader) FileCount() int {
	return len(r.files)
}

// ReadPage reads a page of log entries starting from the given position.
// fileIndex is 1-based (matching the file naming convention).
// lineOffset is the line number within the file (0-based).
func (r *LogReader) ReadPage(fileIndex, lineOffset int) (*LogPage, error) {
	if fileIndex < 1 {
		fileIndex = 1
	}
	if fileIndex > len(r.files) {
		return &LogPage{
			Entries:    []LogEntry{},
			HasMore:    false,
			FileIndex:  fileIndex,
			LineOffset: lineOffset,
			PageSize:   r.pageSize,
			PageNumber: 0,
		}, nil
	}

	entries := make([]LogEntry, 0, r.pageSize)
	currentFileIdx := fileIndex - 1 // convert to 0-based
	currentLineOffset := lineOffset
	entriesRead := 0

	for entriesRead < r.pageSize && currentFileIdx < len(r.files) {
		file, err := os.Open(r.files[currentFileIdx])
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}

		scanner := bufio.NewScanner(file)
		lineNum := 0

		// Skip to the offset
		for lineNum < currentLineOffset && scanner.Scan() {
			lineNum++
		}

		// Read entries
		for scanner.Scan() && entriesRead < r.pageSize {
			line := scanner.Bytes()
			if len(line) == 0 {
				lineNum++
				continue
			}

			entry, err := parseLogEntry(line)
			if err != nil {
				// Skip malformed entries
				lineNum++
				continue
			}

			entries = append(entries, entry)
			entriesRead++
			lineNum++
		}

		file.Close()

		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("error reading log file: %w", err)
		}

		// If we've read all entries from this file, move to the next
		if entriesRead < r.pageSize {
			currentFileIdx++
			currentLineOffset = 0
		} else {
			currentLineOffset = lineNum
		}
	}

	hasMore := currentFileIdx < len(r.files)
	if currentFileIdx < len(r.files) {
		// Check if there are more lines in the current file
		file, err := os.Open(r.files[currentFileIdx])
		if err == nil {
			scanner := bufio.NewScanner(file)
			lineNum := 0
			for scanner.Scan() {
				lineNum++
				if lineNum > currentLineOffset {
					hasMore = true
					break
				}
			}
			file.Close()
		}
	}

	return &LogPage{
		Entries:    entries,
		PageSize:   r.pageSize,
		PageNumber: (fileIndex-1)*10000 + lineOffset/r.pageSize + 1, // approximate page number
		HasMore:    hasMore,
		FileIndex:  currentFileIdx + 1, // convert back to 1-based
		LineOffset: currentLineOffset,
	}, nil
}

// ReadAll reads all log entries from the session.
// Use with caution for large log files - prefer ReadPage for pagination.
func (r *LogReader) ReadAll() ([]LogEntry, error) {
	var entries []LogEntry

	for _, filePath := range r.files {
		file, err := os.Open(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			entry, err := parseLogEntry(line)
			if err != nil {
				continue // skip malformed entries
			}
			entries = append(entries, entry)
		}

		file.Close()

		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("error reading log file: %w", err)
		}
	}

	return entries, nil
}

// StreamLogs streams all log entries through a channel.
// The channel is closed when all entries have been read or an error occurs.
func (r *LogReader) StreamLogs(ctx context.Context) (<-chan LogEntry, <-chan error) {
	entries := make(chan LogEntry, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(entries)
		defer close(errs)

		for _, filePath := range r.files {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			default:
			}

			file, err := os.Open(filePath)
			if err != nil {
				errs <- fmt.Errorf("failed to open log file: %w", err)
				return
			}

			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				select {
				case <-ctx.Done():
					file.Close()
					errs <- ctx.Err()
					return
				default:
				}

				line := scanner.Bytes()
				if len(line) == 0 {
					continue
				}

				entry, err := parseLogEntry(line)
				if err != nil {
					continue
				}

				select {
				case entries <- entry:
				case <-ctx.Done():
					file.Close()
					errs <- ctx.Err()
					return
				}
			}

			file.Close()

			if err := scanner.Err(); err != nil {
				errs <- fmt.Errorf("error reading log file: %w", err)
				return
			}
		}
	}()

	return entries, errs
}

// parseLogEntry parses a JSON log line into a LogEntry.
func parseLogEntry(line []byte) (LogEntry, error) {
	var entry LogEntry
	entry.Raw = make(json.RawMessage, len(line))
	copy(entry.Raw, line)

	// Parse into a map to extract known and unknown fields
	var data map[string]interface{}
	if err := json.Unmarshal(line, &data); err != nil {
		return entry, err
	}

	// Extract known fields
	if t, ok := data["time"].(string); ok {
		entry.Time, _ = time.Parse(time.RFC3339Nano, t)
		delete(data, "time")
	}
	if level, ok := data["level"].(string); ok {
		entry.Level = level
		delete(data, "level")
	}
	if msg, ok := data["msg"].(string); ok {
		entry.Message = msg
		delete(data, "msg")
	}

	// Store remaining fields as attrs
	entry.Attrs = data

	return entry, nil
}

// --- Utility Functions ---

// ReadSessionByName reads logs from a session by its name (timestamp string).
func ReadSessionByName(baseDir, sessionName string, pageSize int) (*LogReader, error) {
	sessionPath := filepath.Join(baseDir, sessionName)
	return NewLogReader(sessionPath, pageSize)
}

// ReadLatestSession reads logs from the most recent session.
func ReadLatestSession(baseDir string, pageSize int) (*LogReader, error) {
	sessions, err := ListSessions(baseDir)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("no sessions found in: %s", baseDir)
	}
	return NewLogReader(sessions[0].Path, pageSize)
}

// CountLogs counts the total number of log entries in a session.
// This scans all files and may be slow for large sessions.
func CountLogs(sessionPath string) (int64, error) {
	pattern := filepath.Join(sessionPath, "*.log")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return 0, err
	}

	var total int64
	for _, filePath := range files {
		file, err := os.Open(filePath)
		if err != nil {
			return 0, err
		}

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			if len(scanner.Bytes()) > 0 {
				total++
			}
		}
		file.Close()

		if err := scanner.Err(); err != nil {
			return 0, err
		}
	}

	return total, nil
}

// TailLogs reads the last n log entries from a session.
func TailLogs(sessionPath string, n int) ([]LogEntry, error) {
	pattern := filepath.Join(sessionPath, "*.log")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	// Read from the end
	entries := make([]LogEntry, 0, n)

	for i := len(files) - 1; i >= 0 && len(entries) < n; i-- {
		fileEntries, err := readFileEntries(files[i])
		if err != nil {
			return nil, err
		}

		// Prepend entries (we're reading backwards)
		needed := n - len(entries)
		if len(fileEntries) <= needed {
			entries = append(fileEntries, entries...)
		} else {
			// Take only the last 'needed' entries from this file
			entries = append(fileEntries[len(fileEntries)-needed:], entries...)
		}
	}

	return entries, nil
}

// readFileEntries reads all entries from a single log file.
func readFileEntries(filePath string) ([]LogEntry, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []LogEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		entry, err := parseLogEntry(line)
		if err != nil {
			continue
		}
		entries = append(entries, entry)
	}

	return entries, scanner.Err()
}

// SearchLogs searches for log entries matching the given criteria.
type SearchCriteria struct {
	Level     string     // filter by log level (e.g., "ERROR", "WARN")
	Message   string     // substring match in message
	StartTime *time.Time // filter entries after this time
	EndTime   *time.Time // filter entries before this time
	AttrKey   string     // filter by attribute key presence
	AttrValue string     // filter by attribute value (requires AttrKey)
}

// SearchSession searches a session for log entries matching the criteria.
func SearchSession(sessionPath string, criteria SearchCriteria, limit int) ([]LogEntry, error) {
	reader, err := NewLogReader(sessionPath, 1000)
	if err != nil {
		return nil, err
	}

	var results []LogEntry
	ctx := context.Background()
	entries, errs := reader.StreamLogs(ctx)

	for entry := range entries {
		if matchesCriteria(entry, criteria) {
			results = append(results, entry)
			if limit > 0 && len(results) >= limit {
				break
			}
		}
	}

	// Check for errors
	select {
	case err := <-errs:
		if err != nil {
			return results, err
		}
	default:
	}

	return results, nil
}

// matchesCriteria checks if a log entry matches the search criteria.
func matchesCriteria(entry LogEntry, criteria SearchCriteria) bool {
	if criteria.Level != "" && !strings.EqualFold(entry.Level, criteria.Level) {
		return false
	}
	if criteria.Message != "" && !strings.Contains(entry.Message, criteria.Message) {
		return false
	}
	if criteria.StartTime != nil && entry.Time.Before(*criteria.StartTime) {
		return false
	}
	if criteria.EndTime != nil && entry.Time.After(*criteria.EndTime) {
		return false
	}
	if criteria.AttrKey != "" {
		val, ok := entry.Attrs[criteria.AttrKey]
		if !ok {
			return false
		}
		if criteria.AttrValue != "" {
			if strVal, ok := val.(string); !ok || strVal != criteria.AttrValue {
				return false
			}
		}
	}
	return true
}

// MultiHandler combines multiple slog.Handlers, writing to all of them.
type MultiHandler struct {
	handlers []slog.Handler
}

// NewMultiHandler creates a handler that writes to multiple handlers.
// This is useful for writing logs to both console and file simultaneously.
func NewMultiHandler(handlers ...slog.Handler) *MultiHandler {
	return &MultiHandler{handlers: handlers}
}

func (h *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *MultiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, r.Level) {
			if err := handler.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return &MultiHandler{handlers: handlers}
}

func (h *MultiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return &MultiHandler{handlers: handlers}
}

// Closer interface for handlers that need cleanup.
type Closer interface {
	Close() error
}

// CloseHandler closes a handler if it implements Closer.
func CloseHandler(h slog.Handler) error {
	if closer, ok := h.(Closer); ok {
		return closer.Close()
	}
	return nil
}

// Flusher interface for handlers that need flushing.
type Flusher interface {
	Flush() error
}

// FlushHandler flushes a handler if it implements Flusher.
func FlushHandler(h slog.Handler) error {
	if flusher, ok := h.(Flusher); ok {
		return flusher.Flush()
	}
	return nil
}

// Close closes all handlers in a MultiHandler that implement Closer.
func (h *MultiHandler) Close() error {
	var errs []error
	for _, handler := range h.handlers {
		if err := CloseHandler(handler); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors closing handlers: %v", errs)
	}
	return nil
}

// Flush flushes all handlers in a MultiHandler that implement Flusher.
func (h *MultiHandler) Flush() error {
	var errs []error
	for _, handler := range h.handlers {
		if err := FlushHandler(handler); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors flushing handlers: %v", errs)
	}
	return nil
}

// --- Iterator-based Reading (Go 1.23+) ---

// LogIterator provides an iterator interface for reading logs.
type LogIterator struct {
	reader      *LogReader
	fileIndex   int
	file        *os.File
	scanner     *bufio.Scanner
	currentFile int
	err         error
}

// NewLogIterator creates a new iterator for reading logs sequentially.
func NewLogIterator(sessionPath string) (*LogIterator, error) {
	reader, err := NewLogReader(sessionPath, 0)
	if err != nil {
		return nil, err
	}

	return &LogIterator{
		reader:      reader,
		fileIndex:   0,
		currentFile: -1,
	}, nil
}

// Next returns the next log entry and a boolean indicating if there are more entries.
func (it *LogIterator) Next() (LogEntry, bool) {
	for {
		// If we need to open a new file
		if it.scanner == nil || !it.scanner.Scan() {
			// Check for scanner error
			if it.scanner != nil {
				if err := it.scanner.Err(); err != nil {
					it.err = err
					_ = it.closeFile()
					return LogEntry{}, false
				}
			}

			// Close current file
			_ = it.closeFile()

			// Move to next file
			it.currentFile++
			if it.currentFile >= len(it.reader.files) {
				return LogEntry{}, false
			}

			// Open next file
			file, err := os.Open(it.reader.files[it.currentFile])
			if err != nil {
				it.err = err
				return LogEntry{}, false
			}
			it.file = file
			it.scanner = bufio.NewScanner(file)

			// Try scanning again
			if !it.scanner.Scan() {
				continue // empty file, try next
			}
		}

		line := it.scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		entry, err := parseLogEntry(line)
		if err != nil {
			continue // skip malformed entries
		}

		return entry, true
	}
}

// Error returns any error that occurred during iteration.
func (it *LogIterator) Error() error {
	return it.err
}

// Close closes the iterator and releases resources.
func (it *LogIterator) Close() error {
	return it.closeFile()
}

func (it *LogIterator) closeFile() error {
	if it.file != nil {
		err := it.file.Close()
		it.file = nil
		it.scanner = nil
		return err
	}
	return nil
}

// All returns an iterator function compatible with range loops.
// Usage: for entry := range iterator.All() { ... }
func (it *LogIterator) All() func(yield func(LogEntry) bool) {
	return func(yield func(LogEntry) bool) {
		for {
			entry, ok := it.Next()
			if !ok {
				return
			}
			if !yield(entry) {
				return
			}
		}
	}
}

// FileStats returns statistics about log files in a session.
type FileStats struct {
	FileName  string    `json:"file_name"`
	FilePath  string    `json:"file_path"`
	Size      int64     `json:"size"`
	LineCount int64     `json:"line_count"`
	ModTime   time.Time `json:"mod_time"`
}

// GetSessionStats returns statistics about all log files in a session.
func GetSessionStats(sessionPath string) ([]FileStats, error) {
	pattern := filepath.Join(sessionPath, "*.log")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	stats := make([]FileStats, 0, len(files))
	for _, filePath := range files {
		info, err := os.Stat(filePath)
		if err != nil {
			return nil, err
		}

		lineCount := int64(0)
		file, err := os.Open(filePath)
		if err == nil {
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				lineCount++
			}
			file.Close()
		}

		stats = append(stats, FileStats{
			FileName:  filepath.Base(filePath),
			FilePath:  filePath,
			Size:      info.Size(),
			LineCount: lineCount,
			ModTime:   info.ModTime(),
		})
	}

	return stats, nil
}

// CompressOldSessions compresses sessions older than the given duration.
// Returns the paths of compressed sessions.
func CompressOldSessions(baseDir string, olderThan time.Duration) ([]string, error) {
	sessions, err := ListSessions(baseDir)
	if err != nil {
		return nil, err
	}

	cutoff := time.Now().Add(-olderThan)
	var compressed []string

	for _, session := range sessions {
		if session.Timestamp.Before(cutoff) {
			// Here you would implement compression logic
			// For now, just return the paths that would be compressed
			compressed = append(compressed, session.Path)
		}
	}

	return compressed, nil
}

// ExportSession exports a session to a single file or writer.
func ExportSession(sessionPath string, w io.Writer) error {
	reader, err := NewLogReader(sessionPath, 0)
	if err != nil {
		return err
	}

	for _, filePath := range reader.files {
		file, err := os.Open(filePath)
		if err != nil {
			return err
		}

		_, err = io.Copy(w, file)
		file.Close()
		if err != nil {
			return err
		}
	}

	return nil
}

// ParseFileIndex extracts the numeric index from a log filename.
func ParseFileIndex(filename string) (int, error) {
	// Remove .log extension
	name := strings.TrimSuffix(filename, ".log")
	return strconv.Atoi(name)
}
