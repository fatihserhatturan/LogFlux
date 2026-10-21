// Package app builds a running LogFlux instance from a config.
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/internal/alert"
	"github.com/fatihserhatturan/logflux/internal/api"
	"github.com/fatihserhatturan/logflux/internal/collector"
	"github.com/fatihserhatturan/logflux/internal/collector/sources"
	"github.com/fatihserhatturan/logflux/internal/config"
	"github.com/fatihserhatturan/logflux/internal/detect"
	"github.com/fatihserhatturan/logflux/internal/metrics"
	"github.com/fatihserhatturan/logflux/internal/notify"
	"github.com/fatihserhatturan/logflux/internal/parser"
	"github.com/fatihserhatturan/logflux/internal/pipeline"
	"github.com/fatihserhatturan/logflux/internal/sinks"
	"github.com/fatihserhatturan/logflux/internal/stats"
	"github.com/fatihserhatturan/logflux/internal/storage"
	"github.com/fatihserhatturan/logflux/internal/ui"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

// Options tweaks how the app is built
type Options struct {
	Stdout io.Writer // where the stdout sink writes; nil disables it
	Alerts io.Writer // where the default stdout notifier writes (default os.Stderr)
}

// App is a configured LogFlux instance
type App struct {
	cfg      *config.Config
	metrics  *metrics.Registry
	pipeline *pipeline.Pipeline
	api      *api.Server
	store    *storage.Store
	stats    *stats.Stats
	alerts   *alert.Manager
	engine   *detect.Engine
}

// New builds an app from cfg
func New(cfg *config.Config, opts Options) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create data dir: %w", err)
	}

	reg := metrics.NewRegistry()
	a := &App{
		cfg:     cfg,
		metrics: reg,
		pipeline: pipeline.New(pipeline.Options{
			BufferSize: cfg.BufferSize,
			Metrics:    reg,
		}),
		api:   api.New(cfg.API.Addr, reg),
		stats: stats.New(24 * time.Hour),
	}
	ok := false
	defer func() {
		if !ok {
			a.closeResources()
		}
	}()

	if err := a.addInputs(); err != nil {
		return nil, err
	}

	if !cfg.Detect.Disabled {
		if err := a.addDetection(opts); err != nil {
			return nil, err
		}
	}

	a.pipeline.AddSink("stats", a.stats)
	a.api.EnableStats(a.stats)

	if !cfg.Storage.Disabled {
		sc := cfg.Storage
		store, err := storage.Open(storage.Options{
			Dir:              cfg.DataDir,
			SegmentMaxSize:   sc.SegmentMaxSizeMB << 20,
			SegmentMaxAge:    sc.SegmentMaxAge.Duration,
			RetentionMaxAge:  sc.RetentionMaxAge.Duration,
			RetentionMaxSize: sc.RetentionMaxSize << 20,
			FlushInterval:    sc.FlushInterval.Duration,
		})
		if err != nil {
			return nil, err
		}
		a.store = store
		a.pipeline.AddSink("storage", store)
		a.api.EnableStorage(store)
	}

	if opts.Stdout != nil {
		a.pipeline.AddSink("stdout", sinks.NewStdout(opts.Stdout))
	}
	a.api.Handle("/", ui.Handler())
	ok = true
	return a, nil
}

func (a *App) addInputs() error {
	checkpoints, err := sources.LoadCheckpoints(filepath.Join(a.cfg.DataDir, "checkpoints.json"))
	if err != nil {
		return err
	}
	for _, sc := range a.cfg.Sources {
		src, err := newSource(sc, checkpoints)
		if err != nil {
			return err
		}
		var procs []pipeline.Processor
		if sc.Parser != parser.FormatNone {
			lp, err := parser.NewLineParser(sc.Parser)
			if err != nil {
				return err
			}
			procs = append(procs, lp)
		}
		a.pipeline.AddInput(pipeline.Input{Name: sc.Name, Source: src, Processors: procs})
	}
	return nil
}

func (a *App) addDetection(opts Options) error {
	dc := a.cfg.Detect

	var notifiers []alert.Notifier
	for _, nc := range a.cfg.Notify {
		switch nc.Type {
		case "stdout":
			notifiers = append(notifiers, notify.NewWriter(alertWriter(opts)))
		case "webhook":
			notifiers = append(notifiers, notify.NewWebhook(nc.URL, nc.Format))
		}
	}
	if len(a.cfg.Notify) == 0 {
		notifiers = append(notifiers, notify.NewWriter(alertWriter(opts)))
	}

	mgr, err := alert.NewManager(alert.Options{
		Path:      filepath.Join(a.cfg.DataDir, "alerts.jsonl"),
		Cooldown:  dc.Cooldown.Duration,
		Notifiers: notifiers,
		Metrics:   a.metrics,
	})
	if err != nil {
		return err
	}
	a.alerts = mgr
	a.api.EnableAlerts(mgr)

	eopts := detect.Options{
		NewPatternDisabled: dc.NewPattern.Disabled,
		NewPatternMinLevel: lookupLevel(dc.NewPattern.MinLevel),
		Warmup:             dc.Warmup.Duration,
		SpikeDisabled:      dc.Spike.Disabled,
		SpikeThreshold:     dc.Spike.Threshold,
		SpikeMinCount:      dc.Spike.MinCount,
		History:            dc.Spike.History,
		MinHistory:         dc.Spike.MinHistory,
		SilenceDisabled:    dc.Silence.Disabled,
		SilenceAfter:       dc.Silence.After.Duration,
		SilenceMinRate:     dc.Silence.MinRate,
		StatePath:          filepath.Join(a.cfg.DataDir, "templates.json"),
	}
	for _, l := range dc.Spike.Levels {
		eopts.SpikeLevels = append(eopts.SpikeLevels, lookupLevel(l))
	}
	for _, rc := range dc.Rules {
		r := detect.Rule{
			Name:      rc.Name,
			Window:    rc.Window.Duration,
			Threshold: rc.Threshold,
			GroupBy:   rc.GroupBy,
			Severity:  lookupLevel(rc.Severity),
		}
		r.Query.Text = rc.Q
		r.Query.Sources = rc.Sources
		r.Query.Fields = rc.Fields
		r.Query.MinLevel = lookupLevel(rc.MinLevel)
		for _, l := range rc.Levels {
			r.Query.Levels = append(r.Query.Levels, lookupLevel(l))
		}
		eopts.Rules = append(eopts.Rules, r)
	}

	engine, err := detect.New(eopts, a.stats, mgr)
	if err != nil {
		return err
	}
	a.engine = engine
	a.pipeline.AddProcessor(engine)
	a.api.EnableTemplates(engine.Drain())
	return nil
}

func alertWriter(opts Options) io.Writer {
	if opts.Alerts != nil {
		return opts.Alerts
	}
	return os.Stderr
}

// lookupLevel converts a validated level name ("" stays "")
func lookupLevel(s string) models.LogLevel {
	if s == "" {
		return ""
	}
	l, _ := models.LookupLevel(s)
	return l
}

// API returns the API server (useful in tests)
func (a *App) API() *api.Server {
	return a.api
}

// Store returns the storage (nil when disabled)
func (a *App) Store() *storage.Store {
	return a.store
}

// Alerts returns the alert manager (nil when detection is disabled)
func (a *App) Alerts() *alert.Manager {
	return a.alerts
}

// Run starts the API, pipeline and detectors and blocks until ctx is cancelled
func (a *App) Run(ctx context.Context) error {
	if err := a.api.Start(); err != nil {
		a.closeResources()
		return err
	}

	// Detectors stop only after the pipeline has drained
	engineCtx, stopEngine := context.WithCancel(context.Background())
	var engineWG sync.WaitGroup
	if a.engine != nil {
		engineWG.Add(1)
		go func() {
			defer engineWG.Done()
			a.engine.Run(engineCtx)
		}()
	}

	err := a.pipeline.Run(ctx) // closes sinks (storage, stats, stdout)

	stopEngine()
	engineWG.Wait()
	if a.alerts != nil {
		_ = a.alerts.Close()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.api.Shutdown(shutdownCtx)
	return err
}

// closeResources releases resources when the app fails before or at start
func (a *App) closeResources() {
	if a.store != nil {
		_ = a.store.Close()
	}
	if a.alerts != nil {
		_ = a.alerts.Close()
	}
}

func newSource(sc config.SourceConfig, checkpoints *sources.Checkpoints) (collector.Source, error) {
	switch sc.Type {
	case config.SourceFile:
		r := sources.NewFileReader(sc.Path)
		r.SourceName = sc.Name
		r.Multiline = sc.Multiline
		r.Checkpoints = checkpoints
		return r, nil
	case config.SourceSyslog:
		r := sources.NewSyslogReceiver(sc.Addr, sc.Protocol)
		r.SourceName = sc.Name
		return r, nil
	case config.SourceHTTP:
		r := sources.NewHTTPReceiver(sc.Addr)
		r.SourceName = sc.Name
		return r, nil
	default:
		return nil, fmt.Errorf("unknown source type %q", sc.Type)
	}
}
