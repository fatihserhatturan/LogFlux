// Package stats keeps per-minute entry counts by source and level.
//
// Counts use arrival time, not the entry timestamp, so they reflect ingest
// rate (what rate-based anomaly detection needs) even when sources send
// delayed or out-of-order timestamps.
package stats

import (
	"sort"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

// Key identifies a series
type Key struct {
	Source string          `json:"source"`
	Level  models.LogLevel `json:"level"`
}

// Point is one bucket of a series
type Point struct {
	Time  time.Time `json:"t"`
	Count int64     `json:"count"`
}

// Series is a time series of counts for a key
type Series struct {
	Key
	Total  int64   `json:"total"`
	Points []Point `json:"points"`
}

// Stats counts entries per minute. It implements pipeline.Sink.
type Stats struct {
	retention time.Duration
	now       func() time.Time

	mu        sync.RWMutex
	counts    map[Key]map[int64]int64 // key -> unix minute -> count
	lastPrune int64
}

// New creates a Stats keeping retention worth of minutes (default 24h)
func New(retention time.Duration) *Stats {
	return newWithClock(retention, time.Now)
}

func newWithClock(retention time.Duration, now func() time.Time) *Stats {
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	return &Stats{retention: retention, now: now, counts: make(map[Key]map[int64]int64)}
}

// Write counts one entry in the current minute
func (s *Stats) Write(e *models.LogEntry) error {
	minute := s.now().Unix() / 60
	k := Key{Source: e.Source, Level: e.Level}

	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.counts[k]
	if !ok {
		m = make(map[int64]int64)
		s.counts[k] = m
	}
	m[minute]++

	if minute != s.lastPrune {
		s.pruneLocked(minute)
		s.lastPrune = minute
	}
	return nil
}

// Close is a no-op
func (s *Stats) Close() error { return nil }

func (s *Stats) pruneLocked(minute int64) {
	oldest := minute - int64(s.retention/time.Minute)
	for k, m := range s.counts {
		for t := range m {
			if t < oldest {
				delete(m, t)
			}
		}
		if len(m) == 0 {
			delete(s.counts, k)
		}
	}
}

// Count returns the count for a key in the minute containing t
func (s *Stats) Count(k Key, t time.Time) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.counts[k][t.Unix()/60]
}

// Keys returns all keys with data, sorted
func (s *Stats) Keys() []Key {
	s.mu.RLock()
	keys := make([]Key, 0, len(s.counts))
	for k := range s.counts {
		keys = append(keys, k)
	}
	s.mu.RUnlock()
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Source != keys[j].Source {
			return keys[i].Source < keys[j].Source
		}
		return keys[i].Level.Severity() < keys[j].Level.Severity()
	})
	return keys
}

// Range returns series from since to now in buckets of step (rounded to
// whole minutes). Missing buckets are zero. If sources is non-empty only
// those sources are returned.
func (s *Stats) Range(since time.Time, step time.Duration, sources []string) []Series {
	if step < time.Minute {
		step = time.Minute
	}
	stepMin := int64(step / time.Minute)
	nowMin := s.now().Unix() / 60
	startMin := since.Unix() / 60
	startMin -= startMin % stepMin
	if startMin > nowMin {
		return nil
	}
	buckets := int((nowMin-startMin)/stepMin) + 1

	want := make(map[string]bool, len(sources))
	for _, src := range sources {
		want[src] = true
	}

	var out []Series
	for _, k := range s.Keys() {
		if len(want) > 0 && !want[k.Source] {
			continue
		}
		series := Series{Key: k, Points: make([]Point, buckets)}
		for i := range series.Points {
			series.Points[i].Time = time.Unix((startMin+int64(i)*stepMin)*60, 0).UTC()
		}
		s.mu.RLock()
		for minute, n := range s.counts[k] {
			if minute < startMin || minute > nowMin {
				continue
			}
			series.Points[(minute-startMin)/stepMin].Count += n
			series.Total += n
		}
		s.mu.RUnlock()
		out = append(out, series)
	}
	return out
}
