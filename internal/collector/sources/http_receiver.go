package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fatihserhatturan/logflux/internal/collector"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

var _ collector.Source = (*HTTPReceiver)(nil)

// httpLog is the JSON payload accepted by /logs and /batch
type httpLog struct {
	Level   string                 `json:"level"`
	Message string                 `json:"message"`
	Source  string                 `json:"source"`
	Fields  map[string]interface{} `json:"fields"`
}

// toEntry converts an HTTP payload into a log entry
func (l httpLog) toEntry(defaultSource string) *models.LogEntry {
	entry := models.NewLogEntry()
	entry.Message = l.Message
	entry.Source = l.Source
	if entry.Source == "" {
		entry.Source = defaultSource
	}
	entry.Level = models.ParseLevel(l.Level)
	if l.Fields != nil {
		entry.Fields = l.Fields
	}
	return entry
}

// HTTPReceiver receives logs via HTTP POST
type HTTPReceiver struct {
	// SourceName is the entry source used when the payload has none (defaults to "http")
	SourceName string

	addr   string
	server *http.Server

	mu      sync.Mutex
	running bool
	out     chan<- *models.LogEntry
	dropped atomic.Int64
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

	// Bind first so the actual address is known (e.g. when using port 0)
	listener, err := net.Listen("tcp", hr.addr)
	if err != nil {
		hr.mu.Lock()
		hr.running = false
		hr.mu.Unlock()
		return fmt.Errorf("failed to listen on HTTP: %w", err)
	}

	hr.server = &http.Server{
		Addr:         listener.Addr().String(),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	fmt.Printf("📡 HTTP receiver listening on %s\n", hr.server.Addr)
	fmt.Println("   POST /logs   - Single log entry")
	fmt.Println("   POST /batch  - Batch log entries")
	fmt.Println("   GET  /health - Health check")

	go func() {
		if err := hr.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Printf("HTTP server error: %v\n", err)
		}
	}()

	// Wait for context cancellation
	go func() {
		<-ctx.Done()
		_ = hr.Stop()
	}()

	return nil
}

// handleLogs handles single log entry
func (hr *HTTPReceiver) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	var logData httpLog
	if err := json.Unmarshal(body, &logData); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	entry := logData.toEntry(hr.defaultSource())

	// Send to channel
	select {
	case hr.out <- entry:
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "accepted",
			"id":     entry.ID,
		})
	default:
		hr.dropped.Add(1)
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

	var logs []httpLog
	if err := json.Unmarshal(body, &logs); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	accepted := 0
	for _, logData := range logs {
		entry := logData.toEntry(hr.defaultSource())

		select {
		case hr.out <- entry:
			accepted++
		default:
			// Channel full, skip
			hr.dropped.Add(1)
		}
	}

	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "accepted",
		"total":    len(logs),
		"accepted": accepted,
	})
}

// handleHealth handles health check
func (hr *HTTPReceiver) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
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

func (hr *HTTPReceiver) defaultSource() string {
	if hr.SourceName != "" {
		return hr.SourceName
	}
	return "http"
}

// Dropped returns the number of entries rejected because the queue was full
func (hr *HTTPReceiver) Dropped() int64 {
	return hr.dropped.Load()
}

// Addr returns the actual listening address
func (hr *HTTPReceiver) Addr() string {
	hr.mu.Lock()
	defer hr.mu.Unlock()
	if hr.server == nil {
		return hr.addr
	}
	return hr.server.Addr
}

// Name returns the source name
func (hr *HTTPReceiver) Name() string {
	return fmt.Sprintf("http:%s", hr.addr)
}
