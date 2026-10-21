// Package metrics provides minimal counters exposed in Prometheus text format.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// CounterVec is a set of counters partitioned by label values
type CounterVec struct {
	name   string
	help   string
	labels []string

	mu       sync.RWMutex
	counters map[string]*atomic.Int64
	values   map[string][]string
}

// GaugeFunc is a gauge whose value is read at scrape time
type GaugeFunc struct {
	name string
	help string
	fn   func() float64
}

// FuncVec is a labelled metric whose values are read from functions at scrape time
type FuncVec struct {
	name  string
	help  string
	kind  string // "counter" or "gauge"
	label string

	mu  sync.RWMutex
	fns map[string]func() float64
}

// Set registers the function for a label value
func (f *FuncVec) Set(value string, fn func() float64) {
	f.mu.Lock()
	f.fns[value] = fn
	f.mu.Unlock()
}

// Registry holds all metrics
type Registry struct {
	mu       sync.Mutex
	counters []*CounterVec
	gauges   []*GaugeFunc
	funcs    []*FuncVec
}

// NewRegistry creates an empty registry
func NewRegistry() *Registry {
	return &Registry{}
}

// NewCounterVec registers a counter with the given label names
func (r *Registry) NewCounterVec(name, help string, labels ...string) *CounterVec {
	c := &CounterVec{
		name:     name,
		help:     help,
		labels:   labels,
		counters: make(map[string]*atomic.Int64),
		values:   make(map[string][]string),
	}
	r.mu.Lock()
	r.counters = append(r.counters, c)
	r.mu.Unlock()
	return c
}

// NewGaugeFunc registers a gauge computed by fn
func (r *Registry) NewGaugeFunc(name, help string, fn func() float64) {
	r.mu.Lock()
	r.gauges = append(r.gauges, &GaugeFunc{name: name, help: help, fn: fn})
	r.mu.Unlock()
}

// NewFuncVec registers a labelled metric of the given kind ("counter" or "gauge")
func (r *Registry) NewFuncVec(name, help, kind, label string) *FuncVec {
	f := &FuncVec{name: name, help: help, kind: kind, label: label, fns: make(map[string]func() float64)}
	r.mu.Lock()
	r.funcs = append(r.funcs, f)
	r.mu.Unlock()
	return f
}

// Add increments the counter for the given label values
func (c *CounterVec) Add(n int64, values ...string) {
	c.get(values).Add(n)
}

// Inc increments the counter for the given label values by one
func (c *CounterVec) Inc(values ...string) {
	c.get(values).Add(1)
}

// Value returns the current value for the given label values
func (c *CounterVec) Value(values ...string) int64 {
	key := strings.Join(values, "\xff")
	c.mu.RLock()
	defer c.mu.RUnlock()
	if v, ok := c.counters[key]; ok {
		return v.Load()
	}
	return 0
}

func (c *CounterVec) get(values []string) *atomic.Int64 {
	key := strings.Join(values, "\xff")

	c.mu.RLock()
	v, ok := c.counters[key]
	c.mu.RUnlock()
	if ok {
		return v
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.counters[key]; ok {
		return v
	}
	v = &atomic.Int64{}
	c.counters[key] = v
	c.values[key] = append([]string(nil), values...)
	return v
}

// WriteTo writes all metrics in Prometheus text exposition format
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	r.mu.Lock()
	counters := append([]*CounterVec(nil), r.counters...)
	gauges := append([]*GaugeFunc(nil), r.gauges...)
	funcs := append([]*FuncVec(nil), r.funcs...)
	r.mu.Unlock()

	var b strings.Builder
	for _, c := range counters {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name)
		c.mu.RLock()
		keys := make([]string, 0, len(c.counters))
		for k := range c.counters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s%s %d\n", c.name, formatLabels(c.labels, c.values[k]), c.counters[k].Load())
		}
		c.mu.RUnlock()
	}
	for _, g := range gauges {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", g.name, g.help, g.name, g.name, g.fn())
	}

	for _, f := range funcs {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind)
		f.mu.RLock()
		keys := make([]string, 0, len(f.fns))
		for k := range f.fns {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s%s %g\n", f.name, formatLabels([]string{f.label}, []string{k}), f.fns[k]())
		}
		f.mu.RUnlock()
	}

	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

func formatLabels(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	parts := make([]string, len(names))
	for i, name := range names {
		v := ""
		if i < len(values) {
			v = values[i]
		}
		v = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
		parts[i] = fmt.Sprintf(`%s="%s"`, name, v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}
