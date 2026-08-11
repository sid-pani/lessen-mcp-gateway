package logbuf

import (
	"strings"
	"testing"
	"time"
)

func TestRingRemainsBoundedAndMonotonic(t *testing.T) {
	ring := New(64 << 10)
	for i := 0; i < 1000; i++ {
		ring.Add(Entry{Time: time.Now(), Level: "INFO", Message: strings.Repeat("x", 512), Attrs: map[string]any{"index": i}})
	}
	entries, bytes := ring.Stats()
	if entries <= 0 || entries >= 1000 {
		t.Fatalf("unexpected retained entry count: %d", entries)
	}
	if bytes > 64<<10 {
		t.Fatalf("ring retained %d bytes, limit is %d", bytes, 64<<10)
	}
	items := ring.List(0, 1000)
	for i := 1; i < len(items); i++ {
		if items[i].Sequence <= items[i-1].Sequence {
			t.Fatalf("sequence is not monotonic: %d then %d", items[i-1].Sequence, items[i].Sequence)
		}
	}
}

func TestNewLoggerWithoutFileReturnsNilCloser(t *testing.T) {
	logger, closer, err := NewLogger(0, "text", New(64<<10), "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if logger == nil {
		t.Fatal("logger is nil")
	}
	if closer != nil {
		t.Fatalf("closer = %#v, want nil", closer)
	}
}

func TestRingCanShrinkAtRuntime(t *testing.T) {
	ring := New(256 << 10)
	for i := 0; i < 400; i++ {
		ring.Add(Entry{Time: time.Now(), Level: "INFO", Message: strings.Repeat("x", 1024)})
	}
	_, before := ring.Stats()
	if before <= 64<<10 {
		t.Fatalf("test did not fill the original ring: %d bytes", before)
	}
	ring.SetMaxBytes(64 << 10)
	entries, after := ring.Stats()
	if entries == 0 {
		t.Fatal("shrink discarded every entry")
	}
	if after > 64<<10 {
		t.Fatalf("ring retained %d bytes after shrink, limit is %d", after, 64<<10)
	}
}
