package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fatihserhatturan/logflux/internal/alert"
)

var sample = alert.Alert{
	Kind:    alert.KindSpike,
	Title:   "error spike",
	Source:  "api",
	Message: "40 ERROR entries/min",
	Sample:  "timeout calling payments",
}

func TestText(t *testing.T) {
	got := Text(sample)
	want := `[SPIKE] error spike (source: api) — 40 ERROR entries/min | e.g. "timeout calling payments"`
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestWriter(t *testing.T) {
	var b strings.Builder
	if err := NewWriter(&b).Notify(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), "🚨 [SPIKE]") {
		t.Errorf("got %q", b.String())
	}
}

func TestWebhook_FormatsAndRetries(t *testing.T) {
	var calls atomic.Int32
	var lastBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lastBody.Store(string(body))
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable) // transient
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := NewWebhook(srv.URL, FormatSlack)
	wh.Backoff = time.Millisecond
	if err := wh.Notify(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (two retries)", calls.Load())
	}
	var slack map[string]string
	if err := json.Unmarshal([]byte(lastBody.Load().(string)), &slack); err != nil || !strings.Contains(slack["text"], "error spike") {
		t.Errorf("slack payload = %v (%v)", lastBody.Load(), err)
	}

	// Slack control characters are escaped
	calls.Store(10)
	withTemplate := sample
	withTemplate.Message = "disk <NUM> & more"
	if err := wh.Notify(context.Background(), withTemplate); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(lastBody.Load().(string)), &slack)
	if !strings.Contains(slack["text"], "disk &lt;NUM&gt; &amp; more") {
		t.Errorf("slack text not escaped: %q", slack["text"])
	}

	calls.Store(10) // succeed immediately
	wh = NewWebhook(srv.URL, FormatJSON)
	if err := wh.Notify(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	var a alert.Alert
	if err := json.Unmarshal([]byte(lastBody.Load().(string)), &a); err != nil || a.Title != "error spike" {
		t.Errorf("json payload = %v (%v)", lastBody.Load(), err)
	}
}

func TestWebhook_NoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	wh := NewWebhook(srv.URL, "")
	wh.Backoff = time.Millisecond
	if err := wh.Notify(context.Background(), sample); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestWebhook_GivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	wh := NewWebhook(srv.URL, "")
	wh.Backoff = time.Millisecond
	wh.Retries = 2
	if err := wh.Notify(context.Background(), sample); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("got %v, want 500 error", err)
	}
}
