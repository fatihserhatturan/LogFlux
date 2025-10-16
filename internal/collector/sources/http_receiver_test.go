package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

func TestHTTPReceiver_SingleLog(t *testing.T) {
	receiver := NewHTTPReceiver("127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan *models.LogEntry, 10)

	if err := receiver.Start(ctx, out); err != nil {
		t.Fatal(err)
	}
	defer receiver.Stop()

	// Get actual address
	addr := receiver.server.Addr
	time.Sleep(100 * time.Millisecond)

	// Send test log
	logData := map[string]interface{}{
		"level":   "ERROR",
		"message": "Test error message",
		"source":  "test-app",
		"fields": map[string]interface{}{
			"user_id": 123,
		},
	}

	body, _ := json.Marshal(logData)
	resp, err := http.Post("http://"+addr+"/logs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("Expected status 202, got %d", resp.StatusCode)
	}

	// Read entry
	select {
	case entry := <-out:
		if entry.Level != models.LevelError {
			t.Errorf("Expected ERROR level, got %s", entry.Level)
		}
		if entry.Message != "Test error message" {
			t.Errorf("Expected 'Test error message', got %q", entry.Message)
		}
		if entry.Source != "test-app" {
			t.Errorf("Expected source 'test-app', got %q", entry.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for log entry")
	}
}

func TestHTTPReceiver_Batch(t *testing.T) {
	receiver := NewHTTPReceiver("127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan *models.LogEntry, 20)

	if err := receiver.Start(ctx, out); err != nil {
		t.Fatal(err)
	}
	defer receiver.Stop()

	addr := receiver.server.Addr
	time.Sleep(100 * time.Millisecond)

	// Send batch
	logs := []map[string]interface{}{
		{"level": "INFO", "message": "Message 1"},
		{"level": "WARNING", "message": "Message 2"},
		{"level": "ERROR", "message": "Message 3"},
	}

	body, _ := json.Marshal(logs)
	resp, err := http.Post("http://"+addr+"/batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("Expected status 202, got %d", resp.StatusCode)
	}

	// Collect entries
	count := 0
	timeout := time.After(2 * time.Second)

	for count < 3 {
		select {
		case <-out:
			count++
		case <-timeout:
			t.Fatalf("Only received %d/3 entries", count)
		}
	}
}

func TestHTTPReceiver_Health(t *testing.T) {
	receiver := NewHTTPReceiver("127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan *models.LogEntry, 10)

	if err := receiver.Start(ctx, out); err != nil {
		t.Fatal(err)
	}
	defer receiver.Stop()

	addr := receiver.server.Addr
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var health map[string]string
	json.NewDecoder(resp.Body).Decode(&health)

	if health["status"] != "healthy" {
		t.Errorf("Expected healthy status, got %s", health["status"])
	}
}
