package stats

import (
	"testing"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

func e(source string, level models.LogLevel) *models.LogEntry {
	x := models.NewLogEntry()
	x.Source = source
	x.Level = level
	return x
}

func TestStats_CountAndRange(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 30, 0, time.UTC)
	s := NewWithClock(time.Hour, func() time.Time { return now })

	_ = s.Write(e("api", models.LevelError))
	_ = s.Write(e("api", models.LevelError))
	now = now.Add(time.Minute)
	_ = s.Write(e("api", models.LevelError))
	_ = s.Write(e("db", models.LevelInfo))

	if got := s.Count(Key{"api", models.LevelError}, now.Add(-time.Minute)); got != 2 {
		t.Errorf("count previous minute = %d, want 2", got)
	}
	if got := s.Count(Key{"api", models.LevelError}, now); got != 1 {
		t.Errorf("count current minute = %d, want 1", got)
	}

	series := s.Range(now.Add(-5*time.Minute), time.Minute, nil)
	if len(series) != 2 {
		t.Fatalf("got %d series, want 2", len(series))
	}
	api := series[0]
	if api.Source != "api" || api.Total != 3 || len(api.Points) != 6 {
		t.Errorf("api series = %+v", api)
	}
	if last := api.Points[len(api.Points)-1]; last.Count != 1 {
		t.Errorf("last bucket = %d, want 1", last.Count)
	}

	// Step aggregation and source filter
	series = s.Range(now.Add(-5*time.Minute), 5*time.Minute, []string{"api"})
	if len(series) != 1 || series[0].Total != 3 {
		t.Errorf("filtered = %+v", series)
	}
	var sum int64
	for _, p := range series[0].Points {
		sum += p.Count
	}
	if sum != 3 {
		t.Errorf("bucket sum = %d, want 3", sum)
	}
}

func TestStats_Prune(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	s := NewWithClock(10*time.Minute, func() time.Time { return now })
	_ = s.Write(e("old", models.LevelInfo))
	now = now.Add(time.Hour)
	_ = s.Write(e("new", models.LevelInfo))
	keys := s.Keys()
	if len(keys) != 1 || keys[0].Source != "new" {
		t.Errorf("keys = %v, want only new", keys)
	}
}
