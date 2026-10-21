package sources

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/internal/collector"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

var _ collector.Source = (*FileReader)(nil)

// FileReader reads logs from a file continuously
type FileReader struct {
	// SourceName overrides the entry source (defaults to the file path)
	SourceName string

	filepath   string
	offset     int64
	pollPeriod time.Duration

	mu      sync.Mutex
	file    *os.File
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewFileReader creates a new file reader
func NewFileReader(filepath string) *FileReader {
	return &FileReader{
		filepath:   filepath,
		offset:     0,
		pollPeriod: 100 * time.Millisecond,
	}
}

// Start begins reading the file
func (fr *FileReader) Start(ctx context.Context, out chan<- *models.LogEntry) error {
	fr.mu.Lock()
	if fr.running {
		fr.mu.Unlock()
		return fmt.Errorf("file reader already running")
	}
	fr.running = true
	fr.mu.Unlock()

	// Open file
	file, err := os.Open(fr.filepath)
	if err != nil {
		fr.setStopped()
		return fmt.Errorf("failed to open file: %w", err)
	}

	// Seek to offset
	if fr.offset > 0 {
		if _, err := file.Seek(fr.offset, 0); err != nil {
			_ = file.Close()
			fr.setStopped()
			return fmt.Errorf("failed to seek: %w", err)
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	fr.mu.Lock()
	fr.file = file
	fr.cancel = cancel
	fr.done = make(chan struct{})
	fr.mu.Unlock()

	go fr.readLoop(ctx, out)
	return nil
}

func (fr *FileReader) setStopped() {
	fr.mu.Lock()
	fr.running = false
	fr.mu.Unlock()
}

// readLoop continuously reads from file
func (fr *FileReader) readLoop(ctx context.Context, out chan<- *models.LogEntry) {
	defer close(fr.done)
	defer func() { _ = fr.file.Close() }()

	reader := bufio.NewReader(fr.file)
	ticker := time.NewTicker(fr.pollPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Try to read lines
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					if err == io.EOF {
						// No more data, wait for next tick
						break
					}
					// Log error but continue
					fmt.Printf("Error reading file: %v\n", err)
					return
				}

				// Update offset
				fr.mu.Lock()
				fr.offset += int64(len(line))
				fr.mu.Unlock()

				// Create log entry (simple parsing for now)
				entry := fr.parseSimpleLine(line)

				select {
				case out <- entry:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// parseSimpleLine does basic parsing (we'll improve this later)
func (fr *FileReader) parseSimpleLine(line string) *models.LogEntry {
	entry := models.NewLogEntry()
	entry.Source = fr.filepath
	if fr.SourceName != "" {
		entry.Source = fr.SourceName
		entry.Fields["path"] = fr.filepath
	}
	entry.Message = strings.TrimRight(line, "\r\n")
	return entry
}

// Stop stops the reader and waits for the read loop to exit
func (fr *FileReader) Stop() error {
	fr.mu.Lock()
	if !fr.running {
		fr.mu.Unlock()
		return nil
	}
	fr.running = false
	cancel, done := fr.cancel, fr.done
	fr.mu.Unlock()

	if cancel != nil {
		cancel()
		<-done
	}
	return nil
}

// Name returns the source name
func (fr *FileReader) Name() string {
	return fmt.Sprintf("file:%s", fr.filepath)
}

// GetOffset returns current offset
func (fr *FileReader) GetOffset() int64 {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return fr.offset
}
