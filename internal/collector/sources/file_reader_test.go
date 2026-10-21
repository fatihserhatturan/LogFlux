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

// fastReader returns a reader with short poll/scan periods for tests
func fastReader(pattern string) *FileReader {
	r := NewFileReader(pattern)
	r.pollPeriod = 10 * time.Millisecond
	r.scanPeriod = 20 * time.Millisecond
	return r
}

func appendFile(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// expectMessages reads len(want) entries and compares messages in order
func expectMessages(t *testing.T, out <-chan *models.LogEntry, want ...string) {
	t.Helper()
	for i, w := range want {
		select {
		case e := <-out:
			if e.Message != w {
				t.Fatalf("entry %d: got %q, want %q", i, e.Message, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for entry %d (%q)", i, w)
		}
	}
}

func expectNone(t *testing.T, out <-chan *models.LogEntry) {
	t.Helper()
	select {
	case e := <-out:
		t.Fatalf("unexpected entry %q", e.Message)
	case <-time.After(150 * time.Millisecond):
	}
}

func startReader(t *testing.T, r *FileReader) chan *models.LogEntry {
	t.Helper()
	out := make(chan *models.LogEntry, 100)
	if err := r.Start(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Stop() })
	return out
}

func TestFileReader_PartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.log")
	appendFile(t, path, "complete\npart")
	out := startReader(t, fastReader(path))

	expectMessages(t, out, "complete")
	expectNone(t, out)
	appendFile(t, path, "ial\n")
	expectMessages(t, out, "partial")
}

func TestFileReader_RenameRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	appendFile(t, path, "old 1\n")
	out := startReader(t, fastReader(path))
	expectMessages(t, out, "old 1")

	// Writer appends one more line, then logrotate renames and creates a new file
	appendFile(t, path, "old 2\n")
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, "new 1\n")
	expectMessages(t, out, "old 2", "new 1")
}

func TestFileReader_CopyTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	appendFile(t, path, "before truncate line\n")
	out := startReader(t, fastReader(path))
	expectMessages(t, out, "before truncate line")

	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	appendFile(t, path, "after\n")
	expectMessages(t, out, "after")
}

func TestFileReader_CheckpointResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	cpPath := filepath.Join(dir, "checkpoints.json")
	appendFile(t, path, "one\ntwo\n")

	cps, err := LoadCheckpoints(cpPath)
	if err != nil {
		t.Fatal(err)
	}
	r := fastReader(path)
	r.Checkpoints = cps
	out := make(chan *models.LogEntry, 10)
	if err := r.Start(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	expectMessages(t, out, "one", "two")
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}

	// Restart: only new lines are read
	appendFile(t, path, "three\n")
	cps, err = LoadCheckpoints(cpPath)
	if err != nil {
		t.Fatal(err)
	}
	r2 := fastReader(path)
	r2.Checkpoints = cps
	out2 := startReader(t, r2)
	expectMessages(t, out2, "three")
	expectNone(t, out2)
}

func TestFileReader_Multiline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	appendFile(t, path, "ERROR boom\n\tat a.b(C.java:1)\n\tat d.e(F.java:2)\nCaused by: x\nINFO next\n")
	r := fastReader(path)
	r.Multiline = true
	out := startReader(t, r)
	expectMessages(t, out,
		"ERROR boom\n\tat a.b(C.java:1)\n\tat d.e(F.java:2)\nCaused by: x",
		"INFO next", // flushed once the file is idle
	)
}

func TestFileReader_Glob(t *testing.T) {
	dir := t.TempDir()
	appendFile(t, filepath.Join(dir, "a.log"), "from a\n")
	r := fastReader(filepath.Join(dir, "*.log"))
	r.SourceName = "logs"
	out := startReader(t, r)

	select {
	case e := <-out:
		if e.Message != "from a" || e.Source != "logs" || e.Fields["path"] != filepath.Join(dir, "a.log") {
			t.Fatalf("got %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}

	// A file created later is picked up by the rescan
	appendFile(t, filepath.Join(dir, "b.log"), "from b\n")
	appendFile(t, filepath.Join(dir, "ignored.txt"), "nope\n")
	expectMessages(t, out, "from b")
	expectNone(t, out)
}

func TestFileReader_InvalidPattern(t *testing.T) {
	r := NewFileReader("[")
	if err := r.Start(context.Background(), make(chan *models.LogEntry)); err == nil {
		t.Fatal("expected error for invalid pattern")
	}
}
