// Package pipeline wires sources, processors and sinks together.
//
// Data flow:
//
//	source ─▶ input queue ─▶ input processors ─┐
//	source ─▶ input queue ─▶ input processors ─┼─▶ main queue ─▶ processors ─▶ sinks
//	source ─▶ input queue ─▶ input processors ─┘
//
// Queues are bounded. Sources that can wait (files) block when a queue is full;
// sources that cannot (UDP, HTTP) drop and count the entry.
package pipeline

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/internal/collector"
	"github.com/fatihserhatturan/logflux/internal/metrics"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

// Processor transforms an entry. Returning false drops the entry.
type Processor interface {
	Process(entry *models.LogEntry) (*models.LogEntry, bool)
}

// ProcessorFunc adapts a function to the Processor interface
type ProcessorFunc func(entry *models.LogEntry) (*models.LogEntry, bool)

// Process calls f(entry)
func (f ProcessorFunc) Process(entry *models.LogEntry) (*models.LogEntry, bool) {
	return f(entry)
}

// Sink receives processed entries
type Sink interface {
	Write(entry *models.LogEntry) error
	Close() error
}

// Dropper is implemented by sources that drop entries when the queue is full
type Dropper interface {
	Dropped() int64
}

// Input is a named source with its own processors (e.g. a parser)
type Input struct {
	Name       string
	Source     collector.Source
	Processors []Processor
}

// Options configures a pipeline
type Options struct {
	BufferSize      int           // main queue size
	InputBufferSize int           // per-input queue size
	ShutdownTimeout time.Duration // max time to drain on shutdown
	Metrics         *metrics.Registry
}

// Pipeline moves entries from inputs through processors to sinks
type Pipeline struct {
	opts       Options
	inputs     []Input
	processors []Processor
	sinks      []namedSink

	in       chan *models.LogEntry
	received *metrics.CounterVec
	dropped  *metrics.CounterVec
	sinkErrs *metrics.CounterVec
	inDrops  *metrics.FuncVec
}

type namedSink struct {
	name string
	sink Sink
}

// New creates a pipeline
func New(opts Options) *Pipeline {
	if opts.BufferSize <= 0 {
		opts.BufferSize = 10000
	}
	if opts.InputBufferSize <= 0 {
		opts.InputBufferSize = 1000
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 10 * time.Second
	}
	if opts.Metrics == nil {
		opts.Metrics = metrics.NewRegistry()
	}

	p := &Pipeline{
		opts: opts,
		in:   make(chan *models.LogEntry, opts.BufferSize),
	}
	reg := opts.Metrics
	p.received = reg.NewCounterVec("logflux_entries_received_total", "Entries received per input and level.", "input", "level")
	p.dropped = reg.NewCounterVec("logflux_entries_filtered_total", "Entries dropped by processors per input.", "input")
	p.sinkErrs = reg.NewCounterVec("logflux_sink_errors_total", "Sink write errors.", "sink")
	p.inDrops = reg.NewFuncVec("logflux_input_dropped_total", "Entries a source dropped because its queue was full.", "counter", "input")
	reg.NewGaugeFunc("logflux_queue_length", "Entries waiting in the main queue.", func() float64 {
		return float64(len(p.in))
	})
	return p
}

// AddInput registers a source
func (p *Pipeline) AddInput(in Input) {
	p.inputs = append(p.inputs, in)
	if d, ok := in.Source.(Dropper); ok {
		p.inDrops.Set(in.Name, func() float64 { return float64(d.Dropped()) })
	}
}

// AddProcessor registers a processor applied to every entry
func (p *Pipeline) AddProcessor(proc Processor) {
	p.processors = append(p.processors, proc)
}

// AddSink registers an output
func (p *Pipeline) AddSink(name string, sink Sink) {
	p.sinks = append(p.sinks, namedSink{name: name, sink: sink})
}

// Run starts all inputs and blocks until ctx is cancelled and the
// pipeline has drained. Sinks are closed before Run returns.
func (p *Pipeline) Run(ctx context.Context) error {
	// Main loop
	mainDone := make(chan struct{})
	go func() {
		defer close(mainDone)
		for entry := range p.in {
			p.handle(entry)
		}
	}()

	// Start inputs
	var inputsWG sync.WaitGroup
	type started struct {
		input Input
		queue chan *models.LogEntry
	}
	var running []started

	srcCtx, cancelSources := context.WithCancel(context.Background())
	defer cancelSources()

	for _, in := range p.inputs {
		queue := make(chan *models.LogEntry, p.opts.InputBufferSize)
		if err := in.Source.Start(srcCtx, queue); err != nil {
			cancelSources()
			for _, r := range running {
				_ = r.input.Source.Stop()
				close(r.queue)
			}
			inputsWG.Wait()
			close(p.in)
			<-mainDone
			p.closeSinks()
			return fmt.Errorf("input %s: %w", in.Name, err)
		}
		running = append(running, started{input: in, queue: queue})

		inputsWG.Add(1)
		go func(in Input, queue <-chan *models.LogEntry) {
			defer inputsWG.Done()
			p.forward(in, queue)
		}(in, queue)
	}

	<-ctx.Done()

	// Shutdown: stop sources, drain queues, close sinks
	done := make(chan struct{})
	go func() {
		cancelSources()
		for _, r := range running {
			_ = r.input.Source.Stop()
			close(r.queue)
		}
		inputsWG.Wait()
		close(p.in)
		<-mainDone
		close(done)
	}()

	var err error
	select {
	case <-done:
	case <-time.After(p.opts.ShutdownTimeout):
		err = fmt.Errorf("shutdown timed out after %s", p.opts.ShutdownTimeout)
	}
	p.closeSinks()
	return err
}

// forward applies input processors and moves entries to the main queue
func (p *Pipeline) forward(in Input, queue <-chan *models.LogEntry) {
	for entry := range queue {
		if entry.Fields == nil {
			entry.Fields = make(map[string]interface{})
		}
		entry.Fields["input"] = in.Name
		ok := true
		for _, proc := range in.Processors {
			if entry, ok = proc.Process(entry); !ok {
				break
			}
		}
		if !ok {
			p.dropped.Inc(in.Name)
			continue
		}
		p.received.Inc(in.Name, string(entry.Level))
		p.in <- entry
	}
}

// handle applies global processors and writes to every sink
func (p *Pipeline) handle(entry *models.LogEntry) {
	var ok bool
	for _, proc := range p.processors {
		if entry, ok = proc.Process(entry); !ok {
			return
		}
	}
	for _, s := range p.sinks {
		if err := s.sink.Write(entry); err != nil {
			p.sinkErrs.Inc(s.name)
		}
	}
}

func (p *Pipeline) closeSinks() {
	for _, s := range p.sinks {
		_ = s.sink.Close()
	}
}
