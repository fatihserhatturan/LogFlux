package alert

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu     sync.Mutex
	alerts []Alert
}

func (r *recorder) Name() string { return "recorder" }

func (r *recorder) Notify(_ context.Context, a Alert) error {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
	return nil
}

func (r *recorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.alerts)
}

func TestManager_CooldownAndNotify(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	rec := &recorder{}
	m, err := newManager(Options{Cooldown: 10 * time.Minute, Notifiers: []Notifier{rec}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	if !m.Emit(Alert{Kind: KindSpike, Key: "spike:api"}) {
		t.Fatal("first alert should be emitted")
	}
	if m.Emit(Alert{Kind: KindSpike, Key: "spike:api"}) {
		t.Error("duplicate within cooldown should be suppressed")
	}
	if !m.Emit(Alert{Kind: KindSpike, Key: "spike:db"}) {
		t.Error("different key should be emitted")
	}
	now = now.Add(11 * time.Minute)
	if !m.Emit(Alert{Kind: KindSpike, Key: "spike:api"}) {
		t.Error("alert after cooldown should be emitted")
	}

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if rec.len() != 3 {
		t.Errorf("notifier got %d alerts, want 3", rec.len())
	}
	if m.Emit(Alert{Key: "x"}) {
		t.Error("emit after close must be a no-op")
	}
}

func TestManager_ListAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.jsonl")
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	m, err := newManager(Options{Path: path, Cooldown: time.Hour}, clock)
	if err != nil {
		t.Fatal(err)
	}
	m.Emit(Alert{Kind: KindSpike, Title: "one", Key: "a"})
	now = now.Add(time.Minute)
	m.Emit(Alert{Kind: KindRule, Title: "two", Key: "b"})
	_ = m.Close()

	// Reopen: alerts and cooldowns survive a restart
	m2, err := newManager(Options{Path: path, Cooldown: time.Hour}, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m2.Close() }()

	all := m2.List(Query{})
	if len(all) != 2 || all[0].Title != "two" {
		t.Fatalf("got %+v, want newest first", all)
	}
	if got := m2.List(Query{Kind: KindSpike}); len(got) != 1 || got[0].Title != "one" {
		t.Errorf("kind filter: %+v", got)
	}
	if got := m2.List(Query{Since: now}); len(got) != 1 {
		t.Errorf("since filter: %+v", got)
	}
	if m2.Emit(Alert{Kind: KindSpike, Key: "a"}) {
		t.Error("cooldown should survive restart")
	}
}
