// Package app builds a running LogFlux instance from a config.
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fatihserhatturan/logflux/internal/api"
	"github.com/fatihserhatturan/logflux/internal/collector"
	"github.com/fatihserhatturan/logflux/internal/collector/sources"
	"github.com/fatihserhatturan/logflux/internal/config"
	"github.com/fatihserhatturan/logflux/internal/metrics"
	"github.com/fatihserhatturan/logflux/internal/parser"
	"github.com/fatihserhatturan/logflux/internal/pipeline"
	"github.com/fatihserhatturan/logflux/internal/sinks"
)

// Options tweaks how the app is built
type Options struct {
	Stdout io.Writer // where the stdout sink writes; nil disables it
}

// App is a configured LogFlux instance
type App struct {
	cfg      *config.Config
	metrics  *metrics.Registry
	pipeline *pipeline.Pipeline
	api      *api.Server
}

// New builds an app from cfg
func New(cfg *config.Config, opts Options) (*App, error) {
	reg := metrics.NewRegistry()
	a := &App{
		cfg:     cfg,
		metrics: reg,
		pipeline: pipeline.New(pipeline.Options{
			BufferSize: cfg.BufferSize,
			Metrics:    reg,
		}),
		api: api.New(cfg.API.Addr, reg),
	}

	checkpoints, err := sources.LoadCheckpoints(filepath.Join(cfg.DataDir, "checkpoints.json"))
	if err != nil {
		return nil, err
	}

	for _, sc := range cfg.Sources {
		src, err := newSource(sc, checkpoints)
		if err != nil {
			return nil, err
		}
		var procs []pipeline.Processor
		if sc.Parser != parser.FormatNone {
			lp, err := parser.NewLineParser(sc.Parser)
			if err != nil {
				return nil, err
			}
			procs = append(procs, lp)
		}
		a.pipeline.AddInput(pipeline.Input{Name: sc.Name, Source: src, Processors: procs})
	}

	if opts.Stdout != nil {
		a.pipeline.AddSink("stdout", sinks.NewStdout(opts.Stdout))
	}
	return a, nil
}

// API returns the API server (useful in tests)
func (a *App) API() *api.Server {
	return a.api
}

// Run starts the API and pipeline and blocks until ctx is cancelled
func (a *App) Run(ctx context.Context) error {
	if err := os.MkdirAll(a.cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}
	if err := a.api.Start(); err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.api.Shutdown(shutdownCtx)
	}()

	return a.pipeline.Run(ctx)
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
