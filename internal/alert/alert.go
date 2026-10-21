// Package alert stores alerts, suppresses duplicates and dispatches them to notifiers.
package alert

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/internal/metrics"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

// Alert kinds
const (
	KindNewPattern = "new_pattern"
	KindSpike      = "spike"
	KindSilence    = "silence"
	KindRule       = "rule"
)

// Alert is a detected anomaly
type Alert struct {
	ID         string          `json:"id"`
	Time       time.Time       `json:"time"`
	Kind       string          `json:"kind"`
	Severity   models.LogLevel `json:"severity"`
	Source     string          `json:"source,omitempty"`
	Title      string          `json:"title"`
	Message    string          `json:"message"`
	Rule       string          `json:"rule,omitempty"`
	TemplateID int             `json:"template_id,omitempty"`
	Template   string          `json:"template,omitempty"`
	Value      float64         `json:"value,omitempty"`
	Baseline   float64         `json:"baseline,omitempty"`
	Sample     string          `json:"sample,omitempty"` // example log message

	// Key identifies "the same problem" for duplicate suppression
	Key string `json:"key"`
}

// Notifier delivers alerts somewhere (stdout, webhook, ...)
type Notifier interface {
	Name() string
	Notify(ctx context.Context, a Alert) error
}

// Options configures a Manager
type Options struct {
	Path      string        // JSONL file alerts are appended to
	Cooldown  time.Duration // suppress alerts with the same key for this long (default 10m)
	Recent    int           // alerts kept in memory for the API (default 1000)
	Notifiers []Notifier
	Metrics   *metrics.Registry
}

// Manager records alerts and notifies asynchronously
type Manager struct {
	opts Options
	now  func() time.Time

	mu       sync.Mutex
	file     *os.File
	recent   []Alert
	lastSent map[string]time.Time
	seq      int64
	closed   bool

	queue      chan Alert
	wg         sync.WaitGroup
	emitted    *metrics.CounterVec
	suppressed *metrics.CounterVec
	notifyErrs *metrics.CounterVec
}

// NewManager opens the alert log and starts notifier workers
func NewManager(opts Options) (*Manager, error) {
	return newManager(opts, time.Now)
}

func newManager(opts Options, now func() time.Time) (*Manager, error) {
	if opts.Cooldown <= 0 {
		opts.Cooldown = 10 * time.Minute
	}
	if opts.Recent <= 0 {
		opts.Recent = 1000
	}
	if opts.Metrics == nil {
		opts.Metrics = metrics.NewRegistry()
	}
	m := &Manager{
		opts:       opts,
		now:        now,
		lastSent:   make(map[string]time.Time),
		queue:      make(chan Alert, 256),
		emitted:    opts.Metrics.NewCounterVec("logflux_alerts_total", "Alerts emitted by kind.", "kind"),
		suppressed: opts.Metrics.NewCounterVec("logflux_alerts_suppressed_total", "Alerts suppressed by cooldown or a full queue.", "reason"),
		notifyErrs: opts.Metrics.NewCounterVec("logflux_notify_errors_total", "Failed notification deliveries.", "notifier"),
	}

	if opts.Path != "" {
		if err := m.loadRecent(); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(opts.Path), 0o755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(opts.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("alert: %w", err)
		}
		m.file = f
	}

	m.wg.Add(1)
	go m.worker()
	return m, nil
}

// Emit records and dispatches an alert unless one with the same key was
// emitted within the cooldown. It reports whether the alert was emitted.
func (m *Manager) Emit(a Alert) bool {
	now := m.now()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	if last, ok := m.lastSent[a.Key]; ok && a.Key != "" && now.Sub(last) < m.opts.Cooldown {
		m.mu.Unlock()
		m.suppressed.Inc("cooldown")
		return false
	}
	m.lastSent[a.Key] = now
	m.seq++
	if a.Time.IsZero() {
		a.Time = now
	}
	a.ID = fmt.Sprintf("%d-%d", now.UnixNano(), m.seq)

	m.recent = append(m.recent, a)
	if len(m.recent) > m.opts.Recent {
		m.recent = m.recent[len(m.recent)-m.opts.Recent:]
	}
	if m.file != nil {
		if data, err := json.Marshal(a); err == nil {
			_, _ = m.file.Write(append(data, '\n'))
		}
	}
	// Forget old keys so the map does not grow forever
	if len(m.lastSent) > 10000 {
		for k, t := range m.lastSent {
			if now.Sub(t) >= m.opts.Cooldown {
				delete(m.lastSent, k)
			}
		}
	}

	// Enqueue while holding mu so Close cannot close the queue concurrently
	select {
	case m.queue <- a:
	default:
		m.suppressed.Inc("queue_full")
	}
	m.mu.Unlock()

	m.emitted.Inc(a.Kind)
	return true
}

// Query filters alerts in List
type Query struct {
	Since time.Time
	Kind  string
	Limit int
}

// List returns recent alerts, newest first
func (m *Manager) List(q Query) []Alert {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Alert, 0, limit)
	for i := len(m.recent) - 1; i >= 0 && len(out) < limit; i-- {
		a := m.recent[i]
		if !q.Since.IsZero() && a.Time.Before(q.Since) {
			continue
		}
		if q.Kind != "" && a.Kind != q.Kind {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Close stops workers after delivering queued alerts and closes the log
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.queue)
	m.mu.Unlock()

	m.wg.Wait()
	if m.file != nil {
		return m.file.Close()
	}
	return nil
}

func (m *Manager) worker() {
	defer m.wg.Done()
	for a := range m.queue {
		for _, n := range m.opts.Notifiers {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := n.Notify(ctx, a); err != nil {
				m.notifyErrs.Inc(n.Name())
				fmt.Fprintf(os.Stderr, "alert: %s notification failed: %v\n", n.Name(), err)
			}
			cancel()
		}
	}
}

// loadRecent reads the tail of the alert log into memory
func (m *Manager) loadRecent() error {
	f, err := os.Open(m.opts.Path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("alert: %w", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var a Alert
		if json.Unmarshal(sc.Bytes(), &a) != nil {
			continue
		}
		m.recent = append(m.recent, a)
		if len(m.recent) > 2*m.opts.Recent {
			m.recent = m.recent[len(m.recent)-m.opts.Recent:]
		}
	}
	if len(m.recent) > m.opts.Recent {
		m.recent = m.recent[len(m.recent)-m.opts.Recent:]
	}
	sort.SliceStable(m.recent, func(i, j int) bool { return m.recent[i].Time.Before(m.recent[j].Time) })
	// Restore cooldowns so a restart does not re-send the same alerts
	for _, a := range m.recent {
		if a.Key != "" && a.Time.After(m.lastSent[a.Key]) {
			m.lastSent[a.Key] = a.Time
		}
	}
	return sc.Err()
}
