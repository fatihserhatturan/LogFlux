package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

var base = time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)

func entry(i int, source string, level models.LogLevel, msg string) *models.LogEntry {
	e := models.NewLogEntry()
	e.Timestamp = base.Add(time.Duration(i) * time.Second)
	e.Source = source
	e.Level = level
	e.Message = msg
	return e
}

func openStore(t *testing.T, opts Options) *Store {
	t.Helper()
	if opts.Dir == "" {
		opts.Dir = t.TempDir()
	}
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustQuery(t *testing.T, s *Store, q Query) []*models.LogEntry {
	t.Helper()
	res, err := s.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func fill(t *testing.T, s *Store) {
	t.Helper()
	for i := 0; i < 100; i++ {
		level := models.LevelInfo
		if i%10 == 0 {
			level = models.LevelError
		}
		src := "api"
		if i%2 == 1 {
			src = "db"
		}
		if err := s.Write(entry(i, src, level, fmt.Sprintf("request %d <done>", i))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStore_QueryFilters(t *testing.T) {
	s := openStore(t, Options{})
	fill(t, s)

	tests := []struct {
		name string
		q    Query
		want int
	}{
		{"all (default limit)", Query{}, 100},
		{"limit", Query{Limit: 5}, 5},
		{"source", Query{Sources: []string{"db"}, Limit: 1000}, 50},
		{"level", Query{Levels: []models.LogLevel{models.LevelError}, Limit: 1000}, 10},
		{"min level", Query{MinLevel: models.LevelWarning, Limit: 1000}, 10},
		{"text case-insensitive", Query{Text: "REQUEST 42 <DONE>", Limit: 1000}, 1},
		{"time range", Query{Since: base.Add(10 * time.Second), Until: base.Add(19 * time.Second), Limit: 1000}, 10},
		{"combined", Query{Sources: []string{"api"}, Levels: []models.LogLevel{models.LevelError}, Limit: 1000}, 10},
		{"no match", Query{Sources: []string{"nope"}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(mustQuery(t, s, tt.q)); got != tt.want {
				t.Errorf("got %d entries, want %d", got, tt.want)
			}
		})
	}

	res := mustQuery(t, s, Query{Limit: 3})
	if res[0].Message != "request 99 <done>" || !res[0].Timestamp.After(res[1].Timestamp) {
		t.Errorf("expected newest first, got %q then %q", res[0].Message, res[1].Message)
	}
}

func TestStore_FieldFilter(t *testing.T) {
	s := openStore(t, Options{})
	e := entry(0, "api", models.LevelError, "x")
	e.Fields["template_id"] = 7
	_ = s.Write(e)
	_ = s.Write(entry(1, "api", models.LevelError, "y"))

	res := mustQuery(t, s, Query{Fields: map[string]string{"template_id": "7"}})
	if len(res) != 1 || res[0].Message != "x" {
		t.Errorf("got %v", res)
	}
}

func TestStore_RotationCompressionAndReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, SegmentMaxSize: 2000})
	if err != nil {
		t.Fatal(err)
	}
	fill(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: closed segments are compressed in the background
	s2 := openStore(t, Options{Dir: dir, SegmentMaxSize: 2000})
	segs := s2.Segments()
	if len(segs) < 3 {
		t.Fatalf("expected several segments, got %d", len(segs))
	}
	var total int64
	for _, m := range segs {
		total += m.Count
	}
	if total != 100 {
		t.Errorf("segments hold %d entries, want 100", total)
	}

	// Wait for background compression
	deadline := time.Now().Add(3 * time.Second)
	for {
		all := true
		for _, m := range s2.Segments() {
			all = all && m.Compressed
		}
		if all {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("segments not compressed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := len(mustQuery(t, s2, Query{Limit: 1000})); got != 100 {
		t.Errorf("query after reopen returned %d, want 100", got)
	}
	// Segment pruning still filters correctly on compressed data
	if got := len(mustQuery(t, s2, Query{Since: base.Add(95 * time.Second), Limit: 1000})); got != 5 {
		t.Errorf("time-filtered query returned %d, want 5", got)
	}

	// New writes go to a new segment with a higher sequence number
	_ = s2.Write(entry(200, "api", models.LevelInfo, "after reopen"))
	segs = s2.Segments()
	last := segs[len(segs)-1]
	if last.Compressed || last.Count != 1 {
		t.Errorf("expected new active segment, got %+v", last)
	}
	for _, m := range segs[:len(segs)-1] {
		if m.File >= last.File {
			t.Errorf("segment order: %s should sort before %s", m.File, last.File)
		}
	}
}

func TestStore_RecoverWithoutIndex(t *testing.T) {
	dir := t.TempDir()
	segDir := filepath.Join(dir, "segments")
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: a segment file but no index
	line := `{"id":"1","timestamp":"2026-10-21T10:00:00Z","level":"ERROR","source":"api","message":"crashed"}` + "\n"
	if err := os.WriteFile(filepath.Join(segDir, "20261021T100000-000004.jsonl"), []byte(line+"not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := openStore(t, Options{Dir: dir})
	res := mustQuery(t, s, Query{})
	if len(res) != 1 || res[0].Message != "crashed" {
		t.Fatalf("got %v", res)
	}
	segs := s.Segments()
	if segs[0].Levels[models.LevelError] != 1 || segs[0].Sources["api"] != 1 {
		t.Errorf("summary not rebuilt: %+v", segs[0])
	}
	if s.seq != 5 {
		t.Errorf("seq = %d, want 5", s.seq)
	}
}

func TestStore_Retention(t *testing.T) {
	now := base.Add(2 * time.Hour)
	s, err := open(Options{Dir: t.TempDir(), SegmentMaxSize: 1500, RetentionMaxAge: time.Hour},
		func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Old entries (two hours before now) fill several segments...
	for i := 0; i < 30; i++ {
		_ = s.Write(entry(i, "api", models.LevelInfo, strings.Repeat("x", 50)))
	}
	before := len(s.Segments())
	// ...then fresh ones
	for i := 0; i < 5; i++ {
		e := entry(i, "api", models.LevelInfo, "fresh")
		e.Timestamp = now
		_ = s.Write(e)
	}

	s.bgWG.Wait() // segments being compressed are skipped by retention
	s.mu.Lock()
	s.applyRetentionLocked()
	s.mu.Unlock()

	segs := s.Segments()
	if len(segs) >= before {
		t.Errorf("expected old segments removed: %d before, %d after", before, len(segs))
	}
	for _, m := range segs {
		if m.Closed && now.Sub(m.MaxTime) > time.Hour {
			t.Errorf("segment %s (max %v) should have been removed", m.File, m.MaxTime)
		}
	}
	if len(mustQuery(t, s, Query{Text: "fresh"})) != 5 {
		t.Error("fresh entries must be kept")
	}
}

func TestStore_RetentionBySize(t *testing.T) {
	s := openStore(t, Options{SegmentMaxSize: 1000, RetentionMaxSize: 3000})
	for i := 0; i < 100; i++ {
		_ = s.Write(entry(i, "api", models.LevelInfo, strings.Repeat("y", 40)))
	}
	s.bgWG.Wait()
	s.mu.Lock()
	s.applyRetentionLocked()
	var total int64
	for _, m := range s.segments {
		total += m.DiskSize
	}
	s.mu.Unlock()
	// Active segment is never deleted, so allow one segment of slack
	if total > 3000+1000 {
		t.Errorf("total size %d exceeds limit", total)
	}
	res := mustQuery(t, s, Query{Limit: 1000})
	if len(res) == 0 || len(res) == 100 {
		t.Errorf("expected some but not all entries kept, got %d", len(res))
	}
	if res[0].Timestamp != base.Add(99*time.Second) {
		t.Error("newest entries must be kept")
	}
}

func TestStore_WriteAfterClose(t *testing.T) {
	s, err := Open(Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if err := s.Write(entry(0, "a", models.LevelInfo, "x")); err != ErrClosed {
		t.Errorf("got %v, want ErrClosed", err)
	}
}

func TestRing(t *testing.T) {
	r := NewRing(3)
	for i := 0; i < 5; i++ {
		r.Add(entry(i, "api", models.LevelInfo, fmt.Sprint(i)))
	}
	got := r.Last(10, nil)
	if len(got) != 3 || got[0].Message != "2" || got[2].Message != "4" {
		t.Errorf("got %v", got)
	}
	got = r.Last(1, &Query{Text: "3"})
	if len(got) != 1 || got[0].Message != "3" {
		t.Errorf("filtered got %v", got)
	}
}

func TestHub(t *testing.T) {
	h := NewHub()
	errs := h.Subscribe(&Query{MinLevel: models.LevelError}, 1)
	all := h.Subscribe(nil, 10)

	h.Publish(entry(0, "a", models.LevelInfo, "info"))
	h.Publish(entry(1, "a", models.LevelError, "err1"))
	h.Publish(entry(2, "a", models.LevelError, "err2")) // errs buffer full

	if e := <-errs.C; e.Message != "err1" {
		t.Errorf("got %q", e.Message)
	}
	if errs.Dropped() != 1 {
		t.Errorf("dropped = %d, want 1", errs.Dropped())
	}
	if len(all.C) != 3 {
		t.Errorf("all got %d entries, want 3", len(all.C))
	}

	h.Unsubscribe(errs)
	if _, ok := <-errs.C; ok {
		t.Error("channel should be closed after unsubscribe")
	}
	if h.Subscribers() != 1 {
		t.Errorf("subscribers = %d", h.Subscribers())
	}
}
