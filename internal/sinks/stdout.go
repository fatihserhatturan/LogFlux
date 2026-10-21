// Package sinks contains simple pipeline outputs.
package sinks

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

// Stdout writes one human-readable line per entry
type Stdout struct {
	mu sync.Mutex
	w  *bufio.Writer
}

// NewStdout creates a sink writing to w
func NewStdout(w io.Writer) *Stdout {
	return &Stdout{w: bufio.NewWriter(w)}
}

// Write formats and writes an entry
func (s *Stdout) Write(entry *models.LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	msg := strings.TrimRight(entry.Message, "\r\n")
	if _, err := fmt.Fprintf(s.w, "%s [%s] %s: %s\n",
		entry.Timestamp.Format(time.RFC3339),
		entry.Level,
		entry.Source,
		msg,
	); err != nil {
		return err
	}
	// Flush per entry so output stays live; bufio still batches the syscall
	return s.w.Flush()
}

// Close flushes buffered output
func (s *Stdout) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Flush()
}
