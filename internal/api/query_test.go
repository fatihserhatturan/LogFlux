package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fatihserhatturan/logflux/internal/metrics"
	"github.com/fatihserhatturan/logflux/internal/stats"
	"github.com/fatihserhatturan/logflux/internal/storage"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

func newTestServer(t *testing.T) (*Server, *storage.Store, *stats.Stats) {
	t.Helper()
	store, err := storage.Open(storage.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	st := stats.New(time.Hour)
	s := New("127.0.0.1:0", metrics.NewRegistry())
	s.EnableStorage(store)
	s.EnableStats(st)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
		_ = store.Close()
	})
	return s, store, st
}

func write(t *testing.T, store *storage.Store, st *stats.Stats, source string, level models.LogLevel, msg string) {
	t.Helper()
	e := models.NewLogEntry()
	e.Source, e.Level, e.Message = source, level, msg
	if err := store.Write(e); err != nil {
		t.Fatal(err)
	}
	_ = st.Write(e)
}

func getJSON(t *testing.T, url string, v interface{}) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func TestParseQuery(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	v, _ := url.ParseQuery("since=15m&until=2026-10-21T11:59:00Z&source=a,b&level=error,warn&min_level=info&q=boom&field.template_id=3&limit=50000")
	q, err := parseQuery(v, now)
	if err != nil {
		t.Fatal(err)
	}
	if !q.Since.Equal(now.Add(-15*time.Minute)) || q.Until.Minute() != 59 {
		t.Errorf("times: %v %v", q.Since, q.Until)
	}
	if len(q.Sources) != 2 || len(q.Levels) != 2 || q.Levels[1] != models.LevelWarning || q.MinLevel != models.LevelInfo {
		t.Errorf("filters: %+v", q)
	}
	if q.Text != "boom" || q.Fields["template_id"] != "3" || q.Limit != maxLimit {
		t.Errorf("q/fields/limit: %+v", q)
	}

	for _, bad := range []string{"since=yesterday", "level=loud", "limit=-1", "min_level=x"} {
		v, _ := url.ParseQuery(bad)
		if _, err := parseQuery(v, now); err == nil {
			t.Errorf("expected error for %s", bad)
		}
	}
}

func TestAPI_Query(t *testing.T) {
	s, store, st := newTestServer(t)
	write(t, store, st, "api", models.LevelError, "db timeout")
	write(t, store, st, "api", models.LevelInfo, "ok")
	write(t, store, st, "web", models.LevelError, "404")

	var res struct {
		Count   int               `json:"count"`
		Entries []models.LogEntry `json:"entries"`
	}
	base := "http://" + s.Addr()
	if code := getJSON(t, base+"/api/query?source=api&min_level=error", &res); code != 200 {
		t.Fatalf("status %d", code)
	}
	if res.Count != 1 || res.Entries[0].Message != "db timeout" {
		t.Errorf("got %+v", res)
	}

	var errRes map[string]string
	if code := getJSON(t, base+"/api/query?level=nope", &errRes); code != 400 || errRes["error"] == "" {
		t.Errorf("bad request: %d %v", code, errRes)
	}

	resp, err := http.Post(base+"/api/query", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status %d", resp.StatusCode)
	}
}

func TestAPI_Stats(t *testing.T) {
	s, store, st := newTestServer(t)
	write(t, store, st, "api", models.LevelError, "x")
	write(t, store, st, "api", models.LevelError, "y")

	var res struct {
		Series []stats.Series `json:"series"`
	}
	if code := getJSON(t, "http://"+s.Addr()+"/api/stats?since=10m&step=5m", &res); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(res.Series) != 1 || res.Series[0].Total != 2 || res.Series[0].Source != "api" {
		t.Errorf("got %+v", res.Series)
	}

	var errRes map[string]string
	if code := getJSON(t, "http://"+s.Addr()+"/api/stats?step=1s", &errRes); code != 400 {
		t.Errorf("expected 400 for small step, got %d", code)
	}
}

func TestAPI_TailBacklogLiveAndShutdown(t *testing.T) {
	s, store, st := newTestServer(t)
	write(t, store, st, "api", models.LevelInfo, "old info")
	write(t, store, st, "api", models.LevelError, "old error")

	resp, err := http.Get("http://" + s.Addr() + "/api/tail?min_level=error&backlog=10")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	// Parse SSE strictly: an event is only delivered at the blank line
	lines := make(chan string, 10)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var data string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "" && data != "":
				var e models.LogEntry
				if json.Unmarshal([]byte(data), &e) == nil {
					lines <- e.Message
				}
				data = ""
			}
		}
		close(lines)
	}()

	next := func() string {
		select {
		case m, ok := <-lines:
			if !ok {
				return "<closed>"
			}
			return m
		case <-time.After(2 * time.Second):
			return "<timeout>"
		}
	}

	if m := next(); m != "old error" {
		t.Fatalf("backlog: got %q", m)
	}
	// Wait until the subscription is registered before publishing
	deadline := time.Now().Add(time.Second)
	for store.Hub().Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	write(t, store, st, "api", models.LevelInfo, "new info")
	write(t, store, st, "api", models.LevelCritical, "new critical")
	if m := next(); m != "new critical" {
		t.Fatalf("live: got %q", m)
	}

	// Shutdown must end the stream promptly
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("shutdown took %v", time.Since(start))
	}
	if m := next(); m != "<closed>" {
		t.Errorf("stream should be closed, got %q", m)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
}
