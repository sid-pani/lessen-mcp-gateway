package logbuf

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Sequence uint64         `json:"sequence"`
	Time     time.Time      `json:"time"`
	Level    string         `json:"level"`
	Message  string         `json:"message"`
	Attrs    map[string]any `json:"attrs,omitempty"`
	Size     int            `json:"-"`
}

type Ring struct {
	mu       sync.RWMutex
	entries  []Entry
	maxBytes int
	bytes    int
	nextSeq  uint64
}

func New(maxBytes int) *Ring {
	if maxBytes < 64*1024 {
		maxBytes = 64 * 1024
	}
	return &Ring{maxBytes: maxBytes, entries: make([]Entry, 0, 256)}
}

func (r *Ring) SetMaxBytes(maxBytes int) {
	if maxBytes < 64*1024 {
		maxBytes = 64 * 1024
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxBytes = maxBytes
	for len(r.entries) > 0 && r.bytes > r.maxBytes {
		r.bytes -= r.entries[0].Size
		copy(r.entries, r.entries[1:])
		last := len(r.entries) - 1
		r.entries[last] = Entry{}
		r.entries = r.entries[:last]
	}
}

func (r *Ring) Add(entry Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextSeq++
	entry.Sequence = r.nextSeq
	data, _ := json.Marshal(entry)
	entry.Size = len(data)
	if entry.Size > r.maxBytes/2 {
		entry.Message = truncate(entry.Message, r.maxBytes/4)
		entry.Attrs = map[string]any{"truncated": true}
		data, _ = json.Marshal(entry)
		entry.Size = len(data)
	}
	for len(r.entries) > 0 && r.bytes+entry.Size > r.maxBytes {
		r.bytes -= r.entries[0].Size
		copy(r.entries, r.entries[1:])
		last := len(r.entries) - 1
		r.entries[last] = Entry{} // release maps/strings held beyond the new length
		r.entries = r.entries[:last]
	}
	r.entries = append(r.entries, entry)
	r.bytes += entry.Size
}

func (r *Ring) List(after uint64, limit int) []Entry {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	start := 0
	if after > 0 {
		for start < len(r.entries) && r.entries[start].Sequence <= after {
			start++
		}
	}
	end := start + limit
	if end > len(r.entries) {
		end = len(r.entries)
	}
	out := make([]Entry, end-start)
	copy(out, r.entries[start:end])
	return out
}

func (r *Ring) Stats() (entries int, bytes int64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries), int64(r.bytes)
}

func truncate(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "…"
}

type ringHandler struct {
	ring   *Ring
	level  slog.Leveler
	attrs  []slog.Attr
	groups []string
}

func (h *ringHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *ringHandler) Handle(_ context.Context, rec slog.Record) error {
	entry := Entry{Time: rec.Time, Level: rec.Level.String(), Message: truncate(rec.Message, 4096), Attrs: map[string]any{}}
	for _, attr := range h.attrs {
		addAttr(entry.Attrs, h.groups, attr)
	}
	rec.Attrs(func(attr slog.Attr) bool { addAttr(entry.Attrs, h.groups, attr); return true })
	if len(entry.Attrs) == 0 {
		entry.Attrs = nil
	}
	h.ring.Add(entry)
	return nil
}
func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}
func (h *ringHandler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.groups = append(append([]string{}, h.groups...), name)
	return &clone
}

func addAttr(target map[string]any, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	key := attr.Key
	if len(groups) > 0 {
		key = strings.Join(append(append([]string{}, groups...), key), ".")
	}
	switch attr.Value.Kind() {
	case slog.KindString:
		target[key] = truncate(attr.Value.String(), 4096)
	case slog.KindInt64:
		target[key] = attr.Value.Int64()
	case slog.KindUint64:
		target[key] = attr.Value.Uint64()
	case slog.KindFloat64:
		target[key] = attr.Value.Float64()
	case slog.KindBool:
		target[key] = attr.Value.Bool()
	case slog.KindDuration:
		target[key] = attr.Value.Duration().String()
	case slog.KindTime:
		target[key] = attr.Value.Time()
	default:
		target[key] = truncate(attr.Value.String(), 4096)
	}
}

type MultiHandler struct{ handlers []slog.Handler }

func (m MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}
func (m MultiHandler) Handle(ctx context.Context, rec slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, rec.Level) {
			_ = h.Handle(ctx, rec.Clone())
		}
	}
	return nil
}
func (m MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	hs := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		hs[i] = h.WithAttrs(attrs)
	}
	return MultiHandler{handlers: hs}
}
func (m MultiHandler) WithGroup(name string) slog.Handler {
	hs := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		hs[i] = h.WithGroup(name)
	}
	return MultiHandler{handlers: hs}
}

type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	file     *os.File
	size     int64
}

func NewRotatingWriter(path string, maxBytes int64, keep int) (*RotatingWriter, error) {
	if path == "" {
		return nil, nil
	}
	if maxBytes <= 0 {
		maxBytes = 16 << 20
	}
	if keep < 1 {
		keep = 3
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && filepath.Dir(path) != "." {
		return nil, err
	}
	w := &RotatingWriter{path: path, maxBytes: maxBytes, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}
func (w *RotatingWriter) open() error {
	file, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	info, _ := file.Stat()
	w.file = file
	if info != nil {
		w.size = info.Size()
	}
	return nil
}
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, io.ErrClosedPipe
	}
	if w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}
func (w *RotatingWriter) rotate() error {
	_ = w.file.Close()
	for i := w.keep - 1; i >= 1; i-- {
		_ = os.Rename(w.path+"."+strconv.Itoa(i), w.path+"."+strconv.Itoa(i+1))
	}
	_ = os.Rename(w.path, w.path+".1")
	return w.open()
}
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func NewLogger(level slog.Level, format string, ring *Ring, filePath string, maxFileBytes int64, keep int) (*slog.Logger, io.Closer, error) {
	var handlers []slog.Handler
	options := &slog.HandlerOptions{Level: level}
	if format == "text" {
		handlers = append(handlers, slog.NewTextHandler(os.Stderr, options))
	} else {
		handlers = append(handlers, slog.NewJSONHandler(os.Stderr, options))
	}
	if ring != nil {
		handlers = append(handlers, &ringHandler{ring: ring, level: level})
	}
	writer, err := NewRotatingWriter(filePath, maxFileBytes, keep)
	if err != nil {
		return nil, nil, err
	}
	var closer io.Closer
	if writer != nil {
		closer = writer
		if format == "text" {
			handlers = append(handlers, slog.NewTextHandler(writer, options))
		} else {
			handlers = append(handlers, slog.NewJSONHandler(writer, options))
		}
	}
	return slog.New(MultiHandler{handlers: handlers}), closer, nil
}
