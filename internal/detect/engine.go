package detect

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/internal/alert"
	"github.com/fatihserhatturan/logflux/internal/stats"
	"github.com/fatihserhatturan/logflux/internal/storage"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

// Rule raises an alert when at least Threshold entries matching Query arrive
// within Window, counted separately per GroupBy value
type Rule struct {
	Name      string
	Query     storage.Query
	Window    time.Duration
	Threshold int
	GroupBy   string // "", "source", "level" or a field name
	Severity  models.LogLevel
}

// Options configures the detection engine
type Options struct {
	Drain DrainOptions

	// New pattern: a template first seen after Warmup at NewPatternMinLevel or above
	NewPatternDisabled bool
	NewPatternMinLevel models.LogLevel // default WARNING
	Warmup             time.Duration   // default 1h

	// Spike: per-minute count far above the recent median (robust z-score)
	SpikeDisabled  bool
	SpikeLevels    []models.LogLevel // default ERROR, CRITICAL
	SpikeThreshold float64           // robust z-score, default 3.5
	SpikeMinCount  int64             // ignore spikes below this count, default 10
	History        int               // minutes of history kept, default 60
	MinHistory     int               // minutes needed before alerting, default 10
	EWMAAlpha      float64           // smoothing for the reported baseline, default 0.3

	// Silence: a source that normally sends logs stopped sending
	SilenceDisabled bool
	SilenceAfter    time.Duration // default 5m
	SilenceMinRate  float64       // entries/minute the source must normally send, default 1

	Rules []Rule

	// StatePath, if set, persists templates across restarts
	StatePath string
}

func (o *Options) applyDefaults() {
	if o.NewPatternMinLevel == "" {
		o.NewPatternMinLevel = models.LevelWarning
	}
	if o.Warmup <= 0 {
		o.Warmup = time.Hour
	}
	if len(o.SpikeLevels) == 0 {
		o.SpikeLevels = []models.LogLevel{models.LevelError, models.LevelCritical}
	}
	if o.SpikeThreshold <= 0 {
		o.SpikeThreshold = 3.5
	}
	if o.SpikeMinCount <= 0 {
		o.SpikeMinCount = 10
	}
	if o.History <= 0 {
		o.History = 60
	}
	if o.MinHistory <= 0 {
		o.MinHistory = 10
	}
	if o.MinHistory > o.History {
		o.MinHistory = o.History
	}
	if o.EWMAAlpha <= 0 || o.EWMAAlpha > 1 {
		o.EWMAAlpha = 0.3
	}
	if o.SilenceAfter < time.Minute {
		o.SilenceAfter = 5 * time.Minute
	}
	if o.SilenceMinRate <= 0 {
		o.SilenceMinRate = 1
	}
}

// series is the per-minute history of one counter
type series struct {
	history []float64
	ewma    float64
	started bool
	alerted bool // silence: already reported
}

func (s *series) push(x float64, max int, alpha float64) {
	s.history = append(s.history, x)
	if len(s.history) > max {
		s.history = s.history[len(s.history)-max:]
	}
	if !s.started {
		s.ewma, s.started = x, true
	} else {
		s.ewma = alpha*x + (1-alpha)*s.ewma
	}
}

type ruleState struct {
	rule   Rule
	groups map[string][]time.Time // recent match times (at most Threshold)
}

// Engine runs all detectors. It implements pipeline.Processor: Process tags
// entries with their template and evaluates per-entry detectors, while Run
// evaluates rate-based detectors once per minute.
type Engine struct {
	opts   Options
	drain  *Drain
	alerts *alert.Manager
	stats  *stats.Stats
	now    func() time.Time
	start  time.Time

	mu      sync.Mutex
	rate    map[stats.Key]*series
	silence map[string]*series
	rules   []*ruleState
}

// New creates an engine. st provides per-minute counts for spike and
// silence detection; alerts receives the alerts.
func New(opts Options, st *stats.Stats, alerts *alert.Manager) (*Engine, error) {
	return newEngine(opts, st, alerts, time.Now)
}

func newEngine(opts Options, st *stats.Stats, alerts *alert.Manager, now func() time.Time) (*Engine, error) {
	opts.applyDefaults()
	e := &Engine{
		opts:    opts,
		drain:   NewDrain(opts.Drain),
		alerts:  alerts,
		stats:   st,
		now:     now,
		start:   now(),
		rate:    make(map[stats.Key]*series),
		silence: make(map[string]*series),
	}
	for _, r := range opts.Rules {
		if r.Threshold <= 0 || r.Window <= 0 {
			return nil, fmt.Errorf("rule %q: threshold and window must be positive", r.Name)
		}
		if r.Severity == "" {
			r.Severity = models.LevelWarning
		}
		e.rules = append(e.rules, &ruleState{rule: r, groups: make(map[string][]time.Time)})
	}
	if opts.StatePath != "" {
		if err := e.drain.Load(opts.StatePath); err != nil {
			return nil, fmt.Errorf("detect: loading templates: %w", err)
		}
		if e.drain.Len() > 0 {
			// Templates were learned before: no need to warm up again
			e.start = now().Add(-opts.Warmup)
		}
	}
	return e, nil
}

// Drain returns the template miner
func (e *Engine) Drain() *Drain {
	return e.drain
}

// Process assigns a template and runs per-entry detectors. It never drops entries.
func (e *Engine) Process(entry *models.LogEntry) (*models.LogEntry, bool) {
	now := e.now()
	c, created := e.drain.Add(entry, now)
	if c != nil {
		entry.Fields["template_id"] = c.ID
	}

	if created && !e.opts.NewPatternDisabled &&
		now.Sub(e.start) >= e.opts.Warmup &&
		entry.Level.Severity() >= e.opts.NewPatternMinLevel.Severity() {
		e.alerts.Emit(alert.Alert{
			Kind:       alert.KindNewPattern,
			Severity:   entry.Level,
			Source:     entry.Source,
			Title:      fmt.Sprintf("New %s pattern", strings.ToLower(string(entry.Level))),
			Message:    c.Template(),
			TemplateID: c.ID,
			Template:   c.Template(),
			Sample:     entry.Message,
			Key:        fmt.Sprintf("new_pattern:%d", c.ID),
		})
	}

	if len(e.rules) > 0 {
		e.mu.Lock()
		for _, rs := range e.rules {
			e.checkRule(rs, entry, now)
		}
		e.mu.Unlock()
	}
	return entry, true
}

func (e *Engine) checkRule(rs *ruleState, entry *models.LogEntry, now time.Time) {
	r := rs.rule
	if !r.Query.Match(entry) {
		return
	}
	group := groupValue(entry, r.GroupBy)
	times := rs.groups[group]
	cutoff := now.Add(-r.Window)
	i := 0
	for i < len(times) && times[i].Before(cutoff) {
		i++
	}
	times = append(times[i:], now)
	if len(times) < r.Threshold {
		rs.groups[group] = times
		return
	}

	// Threshold reached: alert and start counting again
	delete(rs.groups, group)
	title := fmt.Sprintf("Rule %q triggered", r.Name)
	msg := fmt.Sprintf("%d matching entries within %s", len(times), r.Window)
	key := "rule:" + r.Name
	if r.GroupBy != "" {
		msg += fmt.Sprintf(" (%s=%s)", r.GroupBy, group)
		key += ":" + group
	}
	e.alerts.Emit(alert.Alert{
		Kind:     alert.KindRule,
		Severity: r.Severity,
		Source:   entry.Source,
		Title:    title,
		Message:  msg,
		Rule:     r.Name,
		Value:    float64(len(times)),
		Sample:   entry.Message,
		Key:      key,
	})

	// Bound memory when a rule matches many distinct groups
	if len(rs.groups) > 10000 {
		for g, ts := range rs.groups {
			if len(ts) == 0 || ts[len(ts)-1].Before(cutoff) {
				delete(rs.groups, g)
			}
		}
	}
}

func groupValue(entry *models.LogEntry, by string) string {
	switch by {
	case "":
		return ""
	case "source":
		return entry.Source
	case "level":
		return string(entry.Level)
	default:
		if v, ok := entry.Fields[by]; ok {
			return fmt.Sprint(v)
		}
		return ""
	}
}

// Run evaluates rate-based detectors after each minute until ctx is done
func (e *Engine) Run(ctx context.Context) {
	saves := 0
	for {
		next := e.now().Truncate(time.Minute).Add(time.Minute + 2*time.Second)
		select {
		case <-ctx.Done():
			e.saveState()
			return
		case <-time.After(time.Until(next)):
		}
		e.Evaluate(next.Truncate(time.Minute).Add(-time.Minute))
		if saves++; saves%5 == 0 {
			e.saveState()
		}
	}
}

func (e *Engine) saveState() {
	if e.opts.StatePath == "" {
		return
	}
	if err := e.drain.Save(e.opts.StatePath); err != nil {
		fmt.Printf("detect: saving templates: %v\n", err)
	}
}

// Evaluate runs spike and silence detection for the minute starting at minute
func (e *Engine) Evaluate(minute time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()

	keys := e.stats.Keys()
	totals := make(map[string]float64)
	for _, k := range keys {
		totals[k.Source] += float64(e.stats.Count(k, minute))
	}

	if !e.opts.SpikeDisabled {
		known := make(map[stats.Key]bool)
		for _, k := range keys {
			if containsLevel(e.opts.SpikeLevels, k.Level) {
				known[k] = true
			}
		}
		for k := range e.rate {
			known[k] = true
		}
		sorted := make([]stats.Key, 0, len(known))
		for k := range known {
			sorted = append(sorted, k)
		}
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Source+string(sorted[i].Level) < sorted[j].Source+string(sorted[j].Level)
		})
		for _, k := range sorted {
			e.checkSpike(k, float64(e.stats.Count(k, minute)), minute)
		}
	}

	if !e.opts.SilenceDisabled {
		for src := range e.silence {
			if _, ok := totals[src]; !ok {
				totals[src] = 0
			}
		}
		for src, total := range totals {
			e.checkSilence(src, total, minute)
		}
	}
}

func (e *Engine) checkSpike(k stats.Key, x float64, minute time.Time) {
	s := e.rate[k]
	if s == nil {
		s = &series{}
		e.rate[k] = s
	}
	if len(s.history) >= e.opts.MinHistory && x >= float64(e.opts.SpikeMinCount) {
		med, mad := medianMAD(s.history)
		// Modified z-score (Iglewicz & Hoaglin). MAD is floored at 1 so a
		// flat history (often all zeros) still needs a real jump to alert.
		z := 0.6745 * (x - med) / math.Max(mad, 1)
		if z >= e.opts.SpikeThreshold {
			e.alerts.Emit(alert.Alert{
				Time:     minute.Add(time.Minute),
				Kind:     alert.KindSpike,
				Severity: k.Level,
				Source:   k.Source,
				Title:    fmt.Sprintf("%s spike", strings.ToLower(string(k.Level))),
				Message:  fmt.Sprintf("%.0f %s entries/min, typical %.1f (median %.0f, z=%.1f)", x, k.Level, s.ewma, med, z),
				Value:    x,
				Baseline: s.ewma,
				Key:      fmt.Sprintf("spike:%s:%s", k.Source, k.Level),
			})
		}
	}
	s.push(x, e.opts.History, e.opts.EWMAAlpha)
}

func (e *Engine) checkSilence(source string, total float64, minute time.Time) {
	s := e.silence[source]
	if s == nil {
		s = &series{}
		e.silence[source] = s
	}
	s.push(total, e.opts.History+int(e.opts.SilenceAfter/time.Minute), e.opts.EWMAAlpha)
	if total > 0 {
		s.alerted = false
		return
	}

	quiet := int(e.opts.SilenceAfter / time.Minute)
	h := s.history
	if s.alerted || len(h) < e.opts.MinHistory+quiet {
		return
	}
	for _, v := range h[len(h)-quiet:] {
		if v > 0 {
			return
		}
	}
	before := h[:len(h)-quiet]
	if mean(before) < e.opts.SilenceMinRate {
		return
	}
	s.alerted = true
	e.alerts.Emit(alert.Alert{
		Time:     minute.Add(time.Minute),
		Kind:     alert.KindSilence,
		Severity: models.LevelWarning,
		Source:   source,
		Title:    "Source went silent",
		Message:  fmt.Sprintf("no entries for %s (normally %.1f/min)", e.opts.SilenceAfter, mean(before)),
		Baseline: mean(before),
		Key:      "silence:" + source,
	})
}

func medianMAD(xs []float64) (float64, float64) {
	med := median(xs)
	dev := make([]float64, len(xs))
	for i, x := range xs {
		dev[i] = math.Abs(x - med)
	}
	return med, median(dev)
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func containsLevel(list []models.LogLevel, l models.LogLevel) bool {
	for _, v := range list {
		if v == l {
			return true
		}
	}
	return false
}
