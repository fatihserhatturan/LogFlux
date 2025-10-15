package sources

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

// FileReader reads logs from a file continuously
type FileReader struct {
	filepath   string
	offset     int64
	pollPeriod time.Duration

	mu      sync.Mutex
	file    *os.File
	running bool
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
		return fmt.Errorf("failed to open file: %w", err)
	}
	fr.file = file

	// Seek to offset
	if fr.offset > 0 {
		if _, err := fr.file.Seek(fr.offset, 0); err != nil {
			return fmt.Errorf("failed to seek: %w", err)
		}
	}

	go fr.readLoop(ctx, out)
	return nil
}

// readLoop continuously reads from file
func (fr *FileReader) readLoop(ctx context.Context, out chan<- *models.LogEntry) {
	defer fr.Stop()

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
	entry.Message = line
	return entry
}

// Stop stops the reader
func (fr *FileReader) Stop() error {
	fr.mu.Lock()
	defer fr.mu.Unlock()

	if !fr.running {
		return nil
	}

	fr.running = false
	if fr.file != nil {
		return fr.file.Close()
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
