package app

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fatihserhatturan/logflux/internal/config"
)

// syncBuffer is a goroutine-safe strings.Builder
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestApp_FileToStdoutAndMetrics(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "app.log")
	if err := os.WriteFile(logFile, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.API.Addr = "127.0.0.1:0"
	cfg.Sources = []config.SourceConfig{{Type: config.SourceFile, Name: "app", Path: logFile}}

	out := &syncBuffer{}
	a, err := New(cfg, Options{Stdout: out})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(out.String(), "app: world") {
		if time.Now().After(deadline) {
			t.Fatalf("entries not printed, got:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp, err := http.Get("http://" + a.API().Addr() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), `logflux_entries_received_total{input="app",level="INFO"} 2`) {
		t.Errorf("metrics missing received counter:\n%s", body)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.DataDir); err != nil {
		t.Errorf("data dir not created: %v", err)
	}
}
