package sources

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

func TestFileReader_Basic(t *testing.T) {
	// Create temp file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.log")

	content := `line 1
line 2
line 3
`
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Create reader
	reader := NewFileReader(testFile)

	// Start reading
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out := make(chan *models.LogEntry, 10)

	if err := reader.Start(ctx, out); err != nil {
		t.Fatal(err)
	}

	// Collect entries
	var entries []*models.LogEntry
	timeout := time.After(1 * time.Second)

	for i := 0; i < 3; i++ {
		select {
		case entry := <-out:
			entries = append(entries, entry)
		case <-timeout:
			t.Fatal("timeout waiting for entries")
		}
	}

	// Verify
	if len(entries) != 3 {
		t.Errorf("Expected 3 entries, got %d", len(entries))
	}

	// Check offset
	if reader.GetOffset() == 0 {
		t.Error("Offset should have been updated")
	}
}

func TestFileReader_ContinuousReading(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.log")

	// Create initial file
	f, err := os.Create(testFile)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("line 1\n")
	f.Close()

	reader := NewFileReader(testFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan *models.LogEntry, 10)

	if err := reader.Start(ctx, out); err != nil {
		t.Fatal(err)
	}

	// Read first line
	select {
	case <-out:
	case <-time.After(1 * time.Second):
		t.Fatal("timeout reading first line")
	}

	// Append more lines
	f, _ = os.OpenFile(testFile, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString("line 2\n")
	f.Close()

	// Should read new line
	select {
	case entry := <-out:
		t.Logf("Read new line: %s", entry.Message)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout reading appended line")
	}
}
