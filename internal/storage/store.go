// Package storage persists log entries in append-only JSONL segment files.
//
// Layout (inside Options.Dir):
//
//	segments/20261021T101500-000001.jsonl     active segment
//	segments/20261021T091500-000000.jsonl.gz  closed, compressed segment
//	segments/index.json                       per-segment summaries
//
// Like Loki, there is no full-text index: each segment keeps a small summary
// (time range, sources, level counts) and queries scan only the segments whose
// summary can match.
package storage

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

// ErrClosed is returned when writing to a closed store
var ErrClosed = errors.New("storage: store is closed")

const maxScanLine = 4 << 20 // 4 MiB per stored entry

// Options configures a Store
type Options struct {
	Dir              string
	SegmentMaxSize   int64         // rotate after this many bytes (default 64 MiB)
	SegmentMaxAge    time.Duration // rotate after this age (default 1h)
	RetentionMaxAge  time.Duration // delete segments older than this (0 = keep)
	RetentionMaxSize int64         // delete oldest segments above this total size (0 = no limit)
	FlushInterval    time.Duration // flush + fsync interval (default 1s)
	RecentSize       int           // entries kept in memory for live tail (default 1000)
}

// SegmentMeta summarises one segment file
type SegmentMeta struct {
	File       string                    `json:"file"`
	Created    time.Time                 `json:"created"`
	MinTime    time.Time                 `json:"min_time"`
	MaxTime    time.Time                 `json:"max_time"`
	Count      int64                     `json:"count"`
	Size       int64                     `json:"size"`      // uncompressed bytes
	DiskSize   int64                     `json:"disk_size"` // bytes on disk
	Sources    map[string]int64          `json:"sources"`
	Levels     map[models.LogLevel]int64 `json:"levels"`
	Closed     bool                      `json:"closed"`
	Compressed bool                      `json:"compressed"`

	busy bool // being compressed
}

func (m *SegmentMeta) clone() *SegmentMeta {
	c := *m
	c.Sources = make(map[string]int64, len(m.Sources))
	for k, v := range m.Sources {
		c.Sources[k] = v
	}
	c.Levels = make(map[models.LogLevel]int64, len(m.Levels))
	for k, v := range m.Levels {
		c.Levels[k] = v
	}
	return &c
}

func (m *SegmentMeta) add(e *models.LogEntry, size int) {
	if m.Count == 0 || e.Timestamp.Before(m.MinTime) {
		m.MinTime = e.Timestamp
	}
	if m.Count == 0 || e.Timestamp.After(m.MaxTime) {
		m.MaxTime = e.Timestamp
	}
	m.Count++
	m.Size += int64(size)
	m.Sources[e.Source]++
	m.Levels[e.Level]++
}

// Store is a segment-based log store. It implements pipeline.Sink.
type Store struct {
	opts Options
	dir  string // segments directory
	ring *Ring
	hub  *Hub
	now  func() time.Time

	mu       sync.Mutex
	segments []*SegmentMeta // oldest first; last may be active
	active   *SegmentMeta
	file     *os.File
	writer   *bufio.Writer
	dirty    bool
	seq      int
	closed   bool

	bgWG sync.WaitGroup
	stop chan struct{}
	done chan struct{}
}

// Open opens (or creates) a store in opts.Dir
func Open(opts Options) (*Store, error) {
	return open(opts, time.Now)
}

func open(opts Options, now func() time.Time) (*Store, error) {
	if opts.Dir == "" {
		return nil, fmt.Errorf("storage: dir is required")
	}
	if opts.SegmentMaxSize <= 0 {
		opts.SegmentMaxSize = 64 << 20
	}
	if opts.SegmentMaxAge <= 0 {
		opts.SegmentMaxAge = time.Hour
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = time.Second
	}

	s := &Store{
		opts: opts,
		dir:  filepath.Join(opts.Dir, "segments"),
		ring: NewRing(opts.RecentSize),
		hub:  NewHub(),
		now:  now,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	if err := s.recover(); err != nil {
		return nil, err
	}

	// Compress segments left uncompressed by a previous run
	s.mu.Lock()
	for _, m := range s.segments {
		if m.Closed && !m.Compressed {
			s.compressAsync(m)
		}
	}
	s.mu.Unlock()

	go s.loop()
	return s, nil
}

// Ring returns the in-memory recent entries buffer
func (s *Store) Ring() *Ring { return s.ring }

// Hub returns the live entries hub
func (s *Store) Hub() *Hub { return s.hub }

// Write appends an entry to the active segment
func (s *Store) Write(e *models.LogEntry) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keeps "<" etc. literal so text prefilters work
	if err := enc.Encode(e); err != nil {
		return err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.active == nil {
		if err := s.newSegmentLocked(); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	if _, err := s.writer.Write(buf.Bytes()); err != nil {
		s.mu.Unlock()
		return err
	}
	s.active.add(e, buf.Len())
	s.dirty = true
	var err error
	if s.active.Size >= s.opts.SegmentMaxSize {
		err = s.rotateLocked()
	}
	s.mu.Unlock()

	s.ring.Add(e)
	s.hub.Publish(e)
	return err
}

// Close flushes data, closes the active segment and saves the index
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	close(s.stop)
	<-s.done

	s.mu.Lock()
	var err error
	if s.active != nil {
		err = s.closeActiveLocked(false)
	}
	s.mu.Unlock()
	s.bgWG.Wait()

	s.mu.Lock()
	if saveErr := s.saveIndexLocked(); err == nil {
		err = saveErr
	}
	s.mu.Unlock()
	return err
}

// Segments returns a snapshot of segment summaries, oldest first
func (s *Store) Segments() []SegmentMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SegmentMeta, len(s.segments))
	for i, m := range s.segments {
		out[i] = *m.clone()
	}
	return out
}

// Query returns matching entries, newest first, up to q.Limit (default 100)
func (s *Store) Query(q Query) ([]*models.LogEntry, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}

	// Snapshot segment metas; flush so the active file is readable
	s.mu.Lock()
	if s.writer != nil {
		_ = s.writer.Flush()
	}
	snap := make([]*SegmentMeta, len(s.segments))
	for i, m := range s.segments {
		snap[i] = m.clone()
	}
	s.mu.Unlock()

	prefilter := q.linePrefilter()
	var results []*models.LogEntry
	for i := len(snap) - 1; i >= 0 && len(results) < limit; i-- {
		m := snap[i]
		if !q.matchMeta(m) {
			continue
		}
		var matches []*models.LogEntry
		err := s.scanSegment(m, func(line []byte) error {
			if prefilter != nil && !prefilter(line) {
				return nil
			}
			var e models.LogEntry
			if err := json.Unmarshal(line, &e); err != nil {
				return nil // skip corrupt line
			}
			if q.Match(&e) {
				matches = append(matches, &e)
			}
			return nil
		})
		if err != nil {
			return results, err
		}
		sort.SliceStable(matches, func(a, b int) bool {
			return matches[a].Timestamp.After(matches[b].Timestamp)
		})
		for _, e := range matches {
			if len(results) >= limit {
				break
			}
			results = append(results, e)
		}
	}
	return results, nil
}

// scanSegment calls fn for each line of a segment (up to its known size)
func (s *Store) scanSegment(m *SegmentMeta, fn func(line []byte) error) error {
	path := filepath.Join(s.dir, m.File)
	f, err := os.Open(path)
	compressed := m.Compressed
	if os.IsNotExist(err) && !compressed {
		// compressed after our snapshot
		f, err = os.Open(path + ".gz")
		compressed = true
	}
	if os.IsNotExist(err) {
		return nil // deleted by retention after our snapshot
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var r io.Reader
	if compressed {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		r = gz
	} else {
		// Active segment may grow while we read: stop at the snapshot size
		r = io.LimitReader(f, m.Size)
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxScanLine)
	for sc.Scan() {
		if err := fn(sc.Bytes()); err != nil {
			return err
		}
	}
	return sc.Err()
}

// loop flushes periodically, rotates old segments and applies retention
func (s *Store) loop() {
	defer close(s.done)
	ticker := time.NewTicker(s.opts.FlushInterval)
	defer ticker.Stop()
	lastRetention := time.Time{}

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
		}

		s.mu.Lock()
		if s.dirty && s.writer != nil {
			if err := s.writer.Flush(); err == nil {
				_ = s.file.Sync()
				s.active.DiskSize = s.active.Size
				s.dirty = false
				_ = s.saveIndexLocked()
			}
		}
		if s.active != nil && s.active.Count > 0 && s.now().Sub(s.active.Created) >= s.opts.SegmentMaxAge {
			if err := s.rotateLocked(); err != nil {
				fmt.Printf("storage: rotate failed: %v\n", err)
			}
		}
		if s.now().Sub(lastRetention) >= time.Minute {
			s.applyRetentionLocked()
			lastRetention = s.now()
		}
		s.mu.Unlock()
	}
}

func (s *Store) newSegmentLocked() error {
	now := s.now()
	name := fmt.Sprintf("%s-%06d.jsonl", now.UTC().Format("20060102T150405"), s.seq)
	s.seq++
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	m := &SegmentMeta{
		File:    name,
		Created: now,
		Sources: make(map[string]int64),
		Levels:  make(map[models.LogLevel]int64),
	}
	s.active, s.file, s.writer = m, f, bufio.NewWriterSize(f, 256*1024)
	s.segments = append(s.segments, m)
	return nil
}

// rotateLocked closes the active segment and compresses it in the background
func (s *Store) rotateLocked() error {
	return s.closeActiveLocked(true)
}

func (s *Store) closeActiveLocked(compress bool) error {
	m := s.active
	err := s.writer.Flush()
	if syncErr := s.file.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := s.file.Close(); err == nil {
		err = closeErr
	}
	m.Closed = true
	m.DiskSize = m.Size
	s.active, s.file, s.writer, s.dirty = nil, nil, nil, false

	if m.Count == 0 {
		// Nothing written: drop the empty file
		_ = os.Remove(filepath.Join(s.dir, m.File))
		s.removeMetaLocked(m)
	} else if compress {
		s.compressAsync(m)
	}
	if saveErr := s.saveIndexLocked(); err == nil {
		err = saveErr
	}
	return err
}

// compressAsync gzips a closed segment in the background (s.mu must be held)
func (s *Store) compressAsync(m *SegmentMeta) {
	m.busy = true
	s.bgWG.Add(1)
	go func() {
		defer s.bgWG.Done()
		src := filepath.Join(s.dir, m.File)
		dst := src + ".gz"
		size, err := gzipFile(src, dst)

		s.mu.Lock()
		defer s.mu.Unlock()
		m.busy = false
		if err != nil {
			fmt.Printf("storage: compress %s failed: %v\n", m.File, err)
			_ = os.Remove(dst + ".tmp")
			return
		}
		m.File += ".gz"
		m.Compressed = true
		m.DiskSize = size
		_ = os.Remove(src)
		_ = s.saveIndexLocked()
	}()
}

func gzipFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()

	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		_ = out.Close()
		return 0, err
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		return 0, err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return 0, err
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	info, err := os.Stat(tmp)
	if err != nil {
		return 0, err
	}
	return info.Size(), os.Rename(tmp, dst)
}

// applyRetentionLocked deletes segments beyond the age and size limits
func (s *Store) applyRetentionLocked() {
	now := s.now()
	var remove []*SegmentMeta
	if s.opts.RetentionMaxAge > 0 {
		for _, m := range s.segments {
			if m.Closed && !m.busy && now.Sub(m.MaxTime) > s.opts.RetentionMaxAge {
				remove = append(remove, m)
			}
		}
	}
	if s.opts.RetentionMaxSize > 0 {
		var total int64
		for _, m := range s.segments {
			total += m.DiskSize
		}
		for _, m := range remove {
			total -= m.DiskSize
		}
		for _, m := range s.segments {
			if total <= s.opts.RetentionMaxSize {
				break
			}
			if !m.Closed || m.busy || containsMeta(remove, m) {
				continue
			}
			remove = append(remove, m)
			total -= m.DiskSize
		}
	}
	if len(remove) == 0 {
		return
	}
	for _, m := range remove {
		_ = os.Remove(filepath.Join(s.dir, m.File))
		s.removeMetaLocked(m)
	}
	_ = s.saveIndexLocked()
}

func containsMeta(list []*SegmentMeta, m *SegmentMeta) bool {
	for _, v := range list {
		if v == m {
			return true
		}
	}
	return false
}

func (s *Store) removeMetaLocked(m *SegmentMeta) {
	for i, v := range s.segments {
		if v == m {
			s.segments = append(s.segments[:i], s.segments[i+1:]...)
			return
		}
	}
}

func (s *Store) indexPath() string {
	return filepath.Join(s.dir, "index.json")
}

func (s *Store) saveIndexLocked() error {
	data, err := json.MarshalIndent(s.segments, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.indexPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.indexPath())
}

// recover loads the index and reconciles it with the files on disk. Segments
// that were active when the process stopped are rescanned and closed.
func (s *Store) recover() error {
	var metas []*SegmentMeta
	if data, err := os.ReadFile(s.indexPath()); err == nil {
		if err := json.Unmarshal(data, &metas); err != nil {
			fmt.Printf("storage: index corrupt, rebuilding: %v\n", err)
			metas = nil
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("storage: %w", err)
	}

	known := make(map[string]*SegmentMeta)
	for _, m := range metas {
		if m.Sources == nil {
			m.Sources = make(map[string]int64)
		}
		if m.Levels == nil {
			m.Levels = make(map[models.LogLevel]int64)
		}
		known[m.File] = m
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	var segments []*SegmentMeta
	for _, de := range entries {
		name := de.Name()
		isSegment := strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl.gz")
		if de.IsDir() || !isSegment {
			if strings.HasSuffix(name, ".tmp") {
				_ = os.Remove(filepath.Join(s.dir, name))
			}
			continue
		}
		m, ok := known[name]
		if !ok || !m.Closed {
			// Unknown or crashed while active: rebuild the summary from the file
			m, err = s.rebuildMeta(name)
			if err != nil {
				fmt.Printf("storage: skipping %s: %v\n", name, err)
				continue
			}
		}
		if info, err := de.Info(); err == nil {
			m.DiskSize = info.Size()
		}
		if m.Count == 0 {
			_ = os.Remove(filepath.Join(s.dir, name))
			continue
		}
		segments = append(segments, m)
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].File < segments[j].File })

	// Continue the sequence after the highest existing one
	for _, m := range segments {
		base := strings.SplitN(m.File, ".", 2)[0] // 20261021T101500-000001
		if i := strings.LastIndexByte(base, '-'); i >= 0 {
			if seq, err := strconv.Atoi(base[i+1:]); err == nil && seq >= s.seq {
				s.seq = seq + 1
			}
		}
	}
	s.segments = segments
	return s.saveIndexLocked()
}

func (s *Store) rebuildMeta(name string) (*SegmentMeta, error) {
	m := &SegmentMeta{
		File:       name,
		Sources:    make(map[string]int64),
		Levels:     make(map[models.LogLevel]int64),
		Closed:     true,
		Compressed: strings.HasSuffix(name, ".gz"),
	}
	info, err := os.Stat(filepath.Join(s.dir, name))
	if err != nil {
		return nil, err
	}
	m.Created = info.ModTime()

	// Scan the whole file (size unknown)
	probe := &SegmentMeta{File: name, Compressed: m.Compressed, Size: math.MaxInt64}
	err = s.scanSegment(probe, func(line []byte) error {
		var e models.LogEntry
		if json.Unmarshal(line, &e) == nil {
			m.add(&e, len(line)+1)
		}
		return nil
	})
	return m, err
}
