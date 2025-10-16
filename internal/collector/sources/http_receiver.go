package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

// HTTPReceiver receives logs via HTTP POST
type HTTPReceiver struct {
	addr   string
	server *http.Server

	mu      sync.Mutex
	running bool
	out     chan<- *models.LogEntry
}

// NewHTTPReceiver creates a new HTTP receiver
func NewHTTPReceiver(addr string) *HTTPReceiver {
	return &HTTPReceiver{
		addr: addr,
	}
}

// Start begins listening for HTTP requests
func (hr *HTTPReceiver) Start(ctx context.Context, out chan<- *models.LogEntry) error {
	hr.mu.Lock()
	if hr.running {
		hr.mu.Unlock()
		return fmt.Errorf("HTTP receiver already running")
	}
	hr.running = true
	hr.out = out
	hr.mu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("/logs", hr.handleLogs)
	mux.HandleFunc("/batch", hr.handleBatch)
	mux.HandleFunc("/health", hr.handleHealth)

	hr.server = &http.Server{
		Addr:         hr.addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	fmt.Printf("📡 HTTP receiver listening on %s\n", hr.addr)
	fmt.Println("   POST /logs   - Single log entry")
	fmt.Println("   POST /batch  - Batch log entries")
	fmt.Println("   GET  /health - Health check")

	go func() {
		if err := hr.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("HTTP server error: %v\n", err)
		}
	}()

	// Wait for context cancellation
	go func() {
		<-ctx.Done()
		hr.Stop()
	}()

	return nil
}

// handleLogs handles single log entry
func (hr *HTTPReceiver) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Parse JSON
	var logData struct {
		Level   string                 `json:"level"`
		Message string                 `json:"message"`
		Source  string                 `json:"source"`
		Fields  map[string]interface{} `json:"fields"`
	}

	if err := json.Unmarshal(body, &logData); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Create log entry
	entry := models.NewLogEntry()
	entry.Message = logData.Message
	entry.Source = logData.Source
	if entry.Source == "" {
		entry.Source = "http"
	}

	// Parse level
	switch logData.Level {
	case "DEBUG":
		entry.Level = models.LevelDebug
	case "INFO":
		entry.Level = models.LevelInfo
	case "WARNING", "WARN":
		entry.Level = models.LevelWarning
	case "ERROR":
		entry.Level = models.LevelError
	case "CRITICAL", "CRIT":
		entry.Level = models.LevelCritical
	default:
		entry.Level = models.LevelInfo
	}

	// Add fields
	if logData.Fields != nil {
		entry.Fields = logData.Fields
	}

	// Send to channel
	select {
	case hr.out <- entry:
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "accepted",
			"id":     entry.ID,
		})
	default:
		http.Error(w, "Channel full", http.StatusServiceUnavailable)
	}
}

// handleBatch handles batch log entries
func (hr *HTTPReceiver) handleBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var logs []struct {
		Level   string                 `json:"level"`
		Message string                 `json:"message"`
		Source  string                 `json:"source"`
		Fields  map[string]interface{} `json:"fields"`
	}

	if err := json.Unmarshal(body, &logs); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	accepted := 0
	for _, logData := range logs {
		entry := models.NewLogEntry()
		entry.Message = logData.Message
		entry.Source = logData.Source
		if entry.Source == "" {
			entry.Source = "http"
		}

		switch logData.Level {
		case "DEBUG":
			entry.Level = models.LevelDebug
		case "INFO":
			entry.Level = models.LevelInfo
		case "WARNING", "WARN":
			entry.Level = models.LevelWarning
		case "ERROR":
			entry.Level = models.LevelError
		case "CRITICAL", "CRIT":
			entry.Level = models.LevelCritical
		default:
			entry.Level = models.LevelInfo
		}

		if logData.Fields != nil {
			entry.Fields = logData.Fields
		}

		select {
		case hr.out <- entry:
			accepted++
		default:
			// Channel full, skip
		}
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "accepted",
		"total":    len(logs),
		"accepted": accepted,
	})
}

// handleHealth handles health check
func (hr *HTTPReceiver) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// Stop stops the receiver
func (hr *HTTPReceiver) Stop() error {
	hr.mu.Lock()
	defer hr.mu.Unlock()

	if !hr.running {
		return nil
	}

	hr.running = false

	if hr.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return hr.server.Shutdown(ctx)
	}

	return nil
}

// Name returns the source name
func (hr *HTTPReceiver) Name() string {
	return fmt.Sprintf("http:%s", hr.addr)
}
