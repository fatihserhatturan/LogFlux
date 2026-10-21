package detect

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fatihserhatturan/logflux/internal/alert"
	"github.com/fatihserhatturan/logflux/internal/stats"
	"github.com/fatihserhatturan/logflux/internal/storage"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

// clock is a mutable fake time source
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type harness struct {
	clock  *clock
	stats  *stats.Stats
	alerts *alert.Manager
	engine *Engine
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	c := &clock{t: t0}
	st := stats.NewWithClock(24*time.Hour, c.now)
	mgr, err := alert.NewManager(alert.Options{Cooldown: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	e, err := newEngine(opts, st, mgr, c.now)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{clock: c, stats: st, alerts: mgr, engine: e}
}

// feed sends n entries through the engine and stats, like the pipeline does
func (h *harness) feed(n int, source string, level models.LogLevel, text string) {
	for i := 0; i < n; i++ {
		e := msg(level, text)
		e.Source = source
		h.engine.Process(e)
		_ = h.stats.Write(e)
	}
}

// minute ends the current minute and evaluates it
func (h *harness) minute() {
	m := h.clock.now().Truncate(time.Minute)
	h.clock.add(time.Minute)
	h.engine.Evaluate(m)
}

func (h *harness) listAlerts(kind string) []alert.Alert {
	return h.alerts.List(alert.Query{Kind: kind})
}

func TestEngine_TagsTemplates(t *testing.T) {
	h := newHarness(t, Options{})
	a := msg(models.LevelInfo, "user 1 logged in")
	b := msg(models.LevelInfo, "user 2 logged in")
	h.engine.Process(a)
	h.engine.Process(b)
	if a.Fields["template_id"] == nil || a.Fields["template_id"] != b.Fields["template_id"] {
		t.Errorf("template ids: %v %v", a.Fields["template_id"], b.Fields["template_id"])
	}
}

func TestEngine_NewPatternAfterWarmup(t *testing.T) {
	h := newHarness(t, Options{Warmup: 10 * time.Minute})

	h.feed(1, "api", models.LevelError, "database connection refused")
	if n := len(h.listAlerts(alert.KindNewPattern)); n != 0 {
		t.Fatalf("no alert expected during warmup, got %d", n)
	}

	h.clock.add(11 * time.Minute)
	h.feed(1, "api", models.LevelError, "database connection refused") // known
	h.feed(1, "api", models.LevelInfo, "cache warmed in 3 seconds")    // new but INFO
	h.feed(1, "api", models.LevelError, "disk quota exceeded on volume 7")

	got := h.listAlerts(alert.KindNewPattern)
	if len(got) != 1 {
		t.Fatalf("got %d new_pattern alerts, want 1: %+v", len(got), got)
	}
	if got[0].Template != "disk quota exceeded on volume <NUM>" || got[0].Source != "api" {
		t.Errorf("alert = %+v", got[0])
	}
}

func TestEngine_LoadedStateSkipsWarmup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drain.json")
	h := newHarness(t, Options{StatePath: path})
	h.feed(1, "api", models.LevelInfo, "service started")
	h.engine.saveState()

	h2 := newHarness(t, Options{StatePath: path})
	h2.feed(1, "api", models.LevelError, "unexpected EOF from upstream")
	if len(h2.listAlerts(alert.KindNewPattern)) != 1 {
		t.Error("with learned templates, new patterns should alert immediately")
	}
}

func TestEngine_Spike(t *testing.T) {
	h := newHarness(t, Options{MinHistory: 10, SpikeMinCount: 10})

	// Normal: 0-3 errors per minute for 30 minutes
	for i := 0; i < 30; i++ {
		h.feed(i%4, "api", models.LevelError, "timeout calling payments")
		h.feed(50, "api", models.LevelInfo, "request ok")
		h.minute()
	}
	if n := len(h.listAlerts(alert.KindSpike)); n != 0 {
		t.Fatalf("no spike expected in normal traffic, got %d", n)
	}

	// A burst of 40 errors
	h.feed(40, "api", models.LevelError, "timeout calling payments")
	h.minute()
	got := h.listAlerts(alert.KindSpike)
	if len(got) != 1 {
		t.Fatalf("got %d spike alerts, want 1", len(got))
	}
	if got[0].Value != 40 || got[0].Severity != models.LevelError || got[0].Source != "api" {
		t.Errorf("alert = %+v", got[0])
	}
	if got[0].Baseline <= 0 || got[0].Baseline > 5 {
		t.Errorf("baseline = %v, want small positive EWMA", got[0].Baseline)
	}

	// INFO traffic surges never count as spikes by default
	h.feed(5000, "api", models.LevelInfo, "request ok")
	h.minute()
	if n := len(h.listAlerts(alert.KindSpike)); n != 1 {
		t.Errorf("INFO surge should not alert, total spikes %d", n)
	}
}

func TestEngine_SpikeNeedsHistoryAndMinCount(t *testing.T) {
	h := newHarness(t, Options{MinHistory: 10, SpikeMinCount: 10})
	// Burst on the very first minute: not enough history
	h.feed(100, "api", models.LevelError, "boom")
	h.minute()
	for i := 0; i < 15; i++ {
		h.minute()
	}
	// Small jump from zero: below the minimum count
	h.feed(6, "api", models.LevelError, "boom")
	h.minute()
	if n := len(h.listAlerts(alert.KindSpike)); n != 0 {
		t.Errorf("got %d spike alerts, want 0", n)
	}
}

func TestEngine_Silence(t *testing.T) {
	h := newHarness(t, Options{MinHistory: 10, SilenceAfter: 3 * time.Minute, SilenceMinRate: 1})

	for i := 0; i < 15; i++ {
		h.feed(5, "worker", models.LevelInfo, "job done")
		h.feed(1, "rare", models.LevelInfo, "tick") // exactly the minimum rate
		h.minute()
	}
	// worker goes silent, rare keeps going
	for i := 0; i < 3; i++ {
		h.feed(1, "rare", models.LevelInfo, "tick")
		h.minute()
	}
	got := h.listAlerts(alert.KindSilence)
	if len(got) != 1 || got[0].Source != "worker" {
		t.Fatalf("got %+v, want one silence alert for worker", got)
	}

	// Still silent: no duplicate alert
	h.minute()
	h.minute()
	if n := len(h.listAlerts(alert.KindSilence)); n != 1 {
		t.Errorf("duplicate silence alerts: %d", n)
	}
}

func TestEngine_Rules(t *testing.T) {
	rule := Rule{
		Name:      "login-fail",
		Query:     storage.Query{Text: "failed password"},
		Window:    5 * time.Minute,
		Threshold: 3,
		GroupBy:   "host",
	}
	h := newHarness(t, Options{Rules: []Rule{rule}})

	send := func(host string) {
		e := msg(models.LevelWarning, "Failed password for root")
		e.Fields["host"] = host
		h.engine.Process(e)
	}

	send("a")
	send("a")
	send("b")
	h.clock.add(6 * time.Minute) // first two "a" fall out of the window
	send("a")
	send("a")
	if n := len(h.listAlerts(alert.KindRule)); n != 0 {
		t.Fatalf("expected no alert yet, got %d", n)
	}
	send("a")
	got := h.listAlerts(alert.KindRule)
	if len(got) != 1 || got[0].Rule != "login-fail" || got[0].Value != 3 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Key != "rule:login-fail:a" {
		t.Errorf("key = %q", got[0].Key)
	}

	if _, err := newEngine(Options{Rules: []Rule{{Name: "bad"}}}, nil, h.alerts, h.clock.now); err == nil {
		t.Error("expected error for rule without threshold/window")
	}
}

func TestMedianMAD(t *testing.T) {
	med, mad := medianMAD([]float64{1, 1, 2, 2, 4, 6, 9})
	if med != 2 || mad != 1 {
		t.Errorf("median=%v mad=%v, want 2 and 1", med, mad)
	}
	if median(nil) != 0 || median([]float64{1, 3}) != 2 {
		t.Error("median edge cases")
	}
}
