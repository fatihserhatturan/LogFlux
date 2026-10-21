package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fatihserhatturan/logflux/internal/metrics"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

// fakeSource emits n entries then waits for Stop
type fakeSource struct {
	n        int
	startErr error
	level    models.LogLevel

	wg      sync.WaitGroup
	cancel  context.CancelFunc
	stopped bool
}

func (f *fakeSource) Start(ctx context.Context, out chan<- *models.LogEntry) error {
	if f.startErr != nil {
		return f.startErr
	}
	ctx, f.cancel = context.WithCancel(ctx)
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for i := 0; i < f.n; i++ {
			e := models.NewLogEntry()
			e.Message = "msg"
			if f.level != "" {
				e.Level = f.level
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
		<-ctx.Done()
	}()
	return nil
}

func (f *fakeSource) Stop() error {
	if f.cancel != nil {
		f.cancel()
	}
	f.wg.Wait()
	f.stopped = true
	return nil
}

func (f *fakeSource) Name() string { return "fake" }

// memSink collects entries
type memSink struct {
	mu      sync.Mutex
	entries []*models.LogEntry
	closed  bool
	err     error
}

func (m *memSink) Write(e *models.LogEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	return m.err
}

func (m *memSink) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *memSink) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPipeline_FlowAndShutdown(t *testing.T) {
	reg := metrics.NewRegistry()
	p := New(Options{BufferSize: 4, InputBufferSize: 2, Metrics: reg})

	a := &fakeSource{n: 50}
	b := &fakeSource{n: 30, level: models.LevelError}
	p.AddInput(Input{Name: "a", Source: a})
	p.AddInput(Input{Name: "b", Source: b, Processors: []Processor{
		ProcessorFunc(func(e *models.LogEntry) (*models.LogEntry, bool) {
			e.Fields["tagged"] = true
			return e, true
		}),
	}})

	// Global processor drops every entry with message "drop"
	p.AddProcessor(ProcessorFunc(func(e *models.LogEntry) (*models.LogEntry, bool) {
		return e, e.Message != "drop"
	}))

	sink := &memSink{}
	p.AddSink("mem", sink)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- p.Run(ctx) }()

	waitFor(t, func() bool { return sink.len() == 80 })
	cancel()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}

	if !a.stopped || !b.stopped || !sink.closed {
		t.Error("sources should be stopped and sink closed")
	}
	tagged := 0
	for _, e := range sink.entries {
		if e.Fields["tagged"] == true {
			tagged++
			if e.Fields["input"] != "b" {
				t.Errorf("tagged entry has input %v", e.Fields["input"])
			}
		}
	}
	if tagged != 30 {
		t.Errorf("tagged = %d, want 30", tagged)
	}
	if got := p.received.Value("b", "ERROR"); got != 30 {
		t.Errorf("received{b,ERROR} = %d, want 30", got)
	}
}

func TestPipeline_InputProcessorDrops(t *testing.T) {
	p := New(Options{})
	p.AddInput(Input{Name: "a", Source: &fakeSource{n: 10}, Processors: []Processor{
		ProcessorFunc(func(e *models.LogEntry) (*models.LogEntry, bool) { return e, false }),
	}})
	sink := &memSink{}
	p.AddSink("mem", sink)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- p.Run(ctx) }()

	waitFor(t, func() bool { return p.dropped.Value("a") == 10 })
	cancel()
	<-errCh
	if sink.len() != 0 {
		t.Errorf("sink got %d entries, want 0", sink.len())
	}
}

func TestPipeline_StartErrorStopsOthers(t *testing.T) {
	p := New(Options{})
	ok := &fakeSource{n: 1}
	p.AddInput(Input{Name: "ok", Source: ok})
	p.AddInput(Input{Name: "bad", Source: &fakeSource{startErr: errors.New("boom")}})
	sink := &memSink{}
	p.AddSink("mem", sink)

	err := p.Run(context.Background())
	if err == nil {
		t.Fatal("expected start error")
	}
	if !ok.stopped || !sink.closed {
		t.Error("started sources should be stopped and sinks closed on start error")
	}
}

func TestPipeline_SinkErrorsCounted(t *testing.T) {
	p := New(Options{})
	p.AddInput(Input{Name: "a", Source: &fakeSource{n: 3}})
	p.AddSink("bad", &memSink{err: errors.New("disk full")})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- p.Run(ctx) }()

	waitFor(t, func() bool { return p.sinkErrs.Value("bad") == 3 })
	cancel()
	<-errCh
}
