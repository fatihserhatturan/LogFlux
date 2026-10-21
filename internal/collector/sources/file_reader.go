package sources

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fatihserhatturan/logflux/internal/collector"
	"github.com/fatihserhatturan/logflux/internal/parser"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

var _ collector.Source = (*FileReader)(nil)

const maxLineSize = 1 << 20 // lines longer than 1 MiB are split

// FileReader tails one or more files matching a path or glob pattern.
//
// It follows files across rotation: a rename (logrotate default) is detected
// by a change of file identity at the path, and copytruncate by the file
// shrinking below the read offset. Offsets can be persisted in Checkpoints so
// a restart resumes where it stopped.
type FileReader struct {
	// SourceName overrides the entry source (defaults to the file path)
	SourceName string
	// Multiline joins continuation lines (stack traces) into one entry
	Multiline bool
	// Checkpoints, if set, persists read offsets across restarts
	Checkpoints *Checkpoints

	pattern    string
	pollPeriod time.Duration
	scanPeriod time.Duration
	savePeriod time.Duration

	mu      sync.Mutex
	files   map[string]*tailedFile
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// tailedFile is the read state of one open file
type tailedFile struct {
	path    string
	f       *os.File
	info    os.FileInfo
	reader  *bufio.Reader
	offset  atomic.Int64 // bytes of complete lines consumed
	partial []byte       // trailing data without a newline yet
	pending *models.LogEntry
}

// NewFileReader creates a reader for a path or glob pattern
func NewFileReader(pattern string) *FileReader {
	return &FileReader{
		pattern:    pattern,
		pollPeriod: 100 * time.Millisecond,
		scanPeriod: time.Second,
		savePeriod: 5 * time.Second,
		files:      make(map[string]*tailedFile),
	}
}

// Start begins tailing matching files
func (fr *FileReader) Start(ctx context.Context, out chan<- *models.LogEntry) error {
	if _, err := filepath.Match(fr.pattern, ""); err != nil {
		return fmt.Errorf("invalid file pattern %q: %w", fr.pattern, err)
	}

	fr.mu.Lock()
	if fr.running {
		fr.mu.Unlock()
		return fmt.Errorf("file reader already running")
	}
	fr.running = true
	ctx, fr.cancel = context.WithCancel(ctx)
	fr.done = make(chan struct{})
	fr.mu.Unlock()

	fr.scan()
	if len(fr.files) == 0 {
		fmt.Printf("📂 No files match %s yet, waiting...\n", fr.pattern)
	}

	go fr.loop(ctx, out)
	return nil
}

// loop polls files for new data, rotation and new matches
func (fr *FileReader) loop(ctx context.Context, out chan<- *models.LogEntry) {
	defer close(fr.done)
	defer fr.shutdown(out)

	ticker := time.NewTicker(fr.pollPeriod)
	defer ticker.Stop()
	lastScan, lastSave := time.Now(), time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if time.Since(lastScan) >= fr.scanPeriod {
			fr.scan()
			lastScan = time.Now()
		}
		for _, tf := range fr.snapshot() {
			if !fr.read(ctx, tf, out) {
				return
			}
			fr.checkRotation(ctx, tf, out)
		}
		if time.Since(lastSave) >= fr.savePeriod {
			fr.saveCheckpoints()
			lastSave = time.Now()
		}
	}
}

// scan opens newly matching files and forgets deleted ones
func (fr *FileReader) scan() {
	matches, _ := filepath.Glob(fr.pattern) // pattern validated in Start
	seen := make(map[string]bool, len(matches))
	for _, path := range matches {
		seen[path] = true
		fr.mu.Lock()
		_, open := fr.files[path]
		fr.mu.Unlock()
		if open {
			continue
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			continue
		}
		if tf, err := fr.open(path, true); err == nil {
			fr.mu.Lock()
			fr.files[path] = tf
			fr.mu.Unlock()
		} else {
			fmt.Printf("Error opening %s: %v\n", path, err)
		}
	}

	// Files whose path disappeared and was not recreated
	for _, tf := range fr.snapshot() {
		if seen[tf.path] {
			continue
		}
		if _, err := os.Stat(tf.path); os.IsNotExist(err) {
			fr.mu.Lock()
			delete(fr.files, tf.path)
			fr.mu.Unlock()
			_ = tf.f.Close()
			if fr.Checkpoints != nil {
				fr.Checkpoints.Delete(tf.path)
			}
		}
	}
}

// open opens a file, resuming from its checkpoint when it is the same file
func (fr *FileReader) open(path string, resume bool) (*tailedFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	tf := &tailedFile{path: path, f: f, info: info}
	if resume && fr.Checkpoints != nil {
		if cp, ok := fr.Checkpoints.Get(path); ok && cp.FileID == fileID(info) && cp.Offset <= info.Size() {
			if _, err := f.Seek(cp.Offset, io.SeekStart); err == nil {
				tf.offset.Store(cp.Offset)
			}
		}
	}
	tf.reader = bufio.NewReaderSize(f, 64*1024)
	return tf, nil
}

// read consumes all available lines. It returns false if ctx was cancelled.
func (fr *FileReader) read(ctx context.Context, tf *tailedFile, out chan<- *models.LogEntry) bool {
	gotData := false
	for {
		line, err := tf.reader.ReadBytes('\n')
		if len(line) > 0 {
			gotData = true
		}
		if err != nil {
			// No newline yet: keep the partial line for the next poll
			tf.partial = append(tf.partial, line...)
			if len(tf.partial) >= maxLineSize {
				tf.offset.Add(int64(len(tf.partial)))
				text := string(tf.partial)
				tf.partial = nil
				if !fr.emit(ctx, tf, text, out) {
					return false
				}
			}
			if err != io.EOF {
				fmt.Printf("Error reading %s: %v\n", tf.path, err)
			}
			break
		}
		if len(tf.partial) > 0 {
			line = append(tf.partial, line...)
			tf.partial = nil
		}
		tf.offset.Add(int64(len(line)))
		if !fr.emit(ctx, tf, strings.TrimRight(string(line), "\r\n"), out) {
			return false
		}
	}

	// File is idle: flush a pending multiline entry
	if !gotData && tf.pending != nil {
		return fr.flush(ctx, tf, out)
	}
	return true
}

// emit turns a line into an entry, joining continuation lines if enabled
func (fr *FileReader) emit(ctx context.Context, tf *tailedFile, text string, out chan<- *models.LogEntry) bool {
	if strings.TrimSpace(text) == "" {
		return true
	}
	if !fr.Multiline {
		return send(ctx, out, fr.newEntry(tf.path, text))
	}
	if tf.pending != nil && parser.IsContinuation(text) && len(tf.pending.Message) < maxLineSize {
		tf.pending.Message += "\n" + text
		return true
	}
	if !fr.flush(ctx, tf, out) {
		return false
	}
	tf.pending = fr.newEntry(tf.path, text)
	return true
}

func (fr *FileReader) flush(ctx context.Context, tf *tailedFile, out chan<- *models.LogEntry) bool {
	if tf.pending == nil {
		return true
	}
	e := tf.pending
	tf.pending = nil
	return send(ctx, out, e)
}

// checkRotation detects rename and copytruncate rotation of tf
func (fr *FileReader) checkRotation(ctx context.Context, tf *tailedFile, out chan<- *models.LogEntry) {
	info, err := os.Stat(tf.path)
	if err != nil {
		return // renamed or deleted: keep reading the old handle until a new file appears
	}

	if !os.SameFile(tf.info, info) {
		// Rename rotation: drain the old file, then switch to the new one
		if !fr.read(ctx, tf, out) || !fr.flush(ctx, tf, out) {
			return
		}
		_ = tf.f.Close()
		nf, err := fr.open(tf.path, false)
		if err != nil {
			fmt.Printf("Error reopening %s: %v\n", tf.path, err)
			fr.mu.Lock()
			delete(fr.files, tf.path)
			fr.mu.Unlock()
			return
		}
		fr.mu.Lock()
		fr.files[tf.path] = nf
		fr.mu.Unlock()
		return
	}

	if info.Size() < tf.offset.Load()+int64(len(tf.partial)) {
		// copytruncate: start over from the beginning
		if _, err := tf.f.Seek(0, io.SeekStart); err == nil {
			tf.reader.Reset(tf.f)
			tf.offset.Store(0)
			tf.partial = nil
		}
	}
	tf.info = info
}

func (fr *FileReader) newEntry(path, text string) *models.LogEntry {
	entry := models.NewLogEntry()
	entry.Source = path
	if fr.SourceName != "" {
		entry.Source = fr.SourceName
		entry.Fields["path"] = path
	}
	entry.Message = text
	return entry
}

// shutdown flushes pending entries, saves checkpoints and closes files
func (fr *FileReader) shutdown(out chan<- *models.LogEntry) {
	for _, tf := range fr.snapshot() {
		if tf.pending != nil {
			select {
			case out <- tf.pending:
			case <-time.After(time.Second):
			}
			tf.pending = nil
		}
	}
	fr.saveCheckpoints()
	for _, tf := range fr.snapshot() {
		_ = tf.f.Close()
	}
}

func (fr *FileReader) saveCheckpoints() {
	if fr.Checkpoints == nil {
		return
	}
	for _, tf := range fr.snapshot() {
		fr.Checkpoints.Set(tf.path, Checkpoint{FileID: fileID(tf.info), Offset: tf.offset.Load()})
	}
	if err := fr.Checkpoints.Save(); err != nil {
		fmt.Printf("Error saving checkpoints: %v\n", err)
	}
}

func (fr *FileReader) snapshot() []*tailedFile {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	files := make([]*tailedFile, 0, len(fr.files))
	for _, tf := range fr.files {
		files = append(files, tf)
	}
	return files
}

func send(ctx context.Context, out chan<- *models.LogEntry, e *models.LogEntry) bool {
	select {
	case out <- e:
		return true
	case <-ctx.Done():
		return false
	}
}

// Stop stops the reader and waits for it to flush and close files
func (fr *FileReader) Stop() error {
	fr.mu.Lock()
	if !fr.running {
		fr.mu.Unlock()
		return nil
	}
	fr.running = false
	cancel, done := fr.cancel, fr.done
	fr.mu.Unlock()

	cancel()
	<-done
	return nil
}

// Name returns the source name
func (fr *FileReader) Name() string {
	return fmt.Sprintf("file:%s", fr.pattern)
}

// GetOffset returns the total bytes read across all files
func (fr *FileReader) GetOffset() int64 {
	var total int64
	for _, tf := range fr.snapshot() {
		total += tf.offset.Load()
	}
	return total
}
