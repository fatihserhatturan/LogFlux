package storage

import (
	"fmt"
	"strings"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

// Query filters stored entries. Zero values match everything.
type Query struct {
	Since    time.Time
	Until    time.Time
	Sources  []string
	Levels   []models.LogLevel
	MinLevel models.LogLevel
	Text     string            // case-insensitive substring of the message
	Fields   map[string]string // exact match on entry.Fields[key] (stringified)
	Limit    int
}

// Match reports whether e satisfies the query
func (q *Query) Match(e *models.LogEntry) bool {
	if !q.Since.IsZero() && e.Timestamp.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && e.Timestamp.After(q.Until) {
		return false
	}
	if len(q.Sources) > 0 && !contains(q.Sources, e.Source) {
		return false
	}
	if len(q.Levels) > 0 && !containsLevel(q.Levels, e.Level) {
		return false
	}
	if q.MinLevel != "" && e.Level.Severity() < q.MinLevel.Severity() {
		return false
	}
	if q.Text != "" && !containsFold(e.Message, q.Text) {
		return false
	}
	for k, want := range q.Fields {
		v, ok := e.Fields[k]
		if !ok || fmt.Sprint(v) != want {
			return false
		}
	}
	return true
}

// matchMeta reports whether a segment can contain matching entries
func (q *Query) matchMeta(m *SegmentMeta) bool {
	if m.Count == 0 {
		return false
	}
	if !q.Since.IsZero() && m.MaxTime.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && m.MinTime.After(q.Until) {
		return false
	}
	if len(q.Sources) > 0 {
		found := false
		for _, s := range q.Sources {
			if m.Sources[s] > 0 {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(q.Levels) > 0 || q.MinLevel != "" {
		found := false
		for l, n := range m.Levels {
			if n == 0 {
				continue
			}
			if len(q.Levels) > 0 && !containsLevel(q.Levels, l) {
				continue
			}
			if q.MinLevel != "" && l.Severity() < q.MinLevel.Severity() {
				continue
			}
			found = true
			break
		}
		if !found {
			return false
		}
	}
	return true
}

// linePrefilter returns a cheap check on the raw JSON line, or nil when the
// query text could be escaped differently in JSON
func (q *Query) linePrefilter() func(line []byte) bool {
	if q.Text == "" || strings.ContainsAny(q.Text, "\"\\\n\r\t") || !isASCII(q.Text) {
		return nil
	}
	return func(line []byte) bool { return containsFold(string(line), q.Text) }
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsLevel(list []models.LogLevel, l models.LogLevel) bool {
	for _, v := range list {
		if v == l {
			return true
		}
	}
	return false
}

func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
