package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/fatihserhatturan/logflux/internal/app"
	"github.com/fatihserhatturan/logflux/internal/config"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("logflux", flag.ContinueOnError)
	fs.Usage = func() { printUsage(fs) }
	configPath := fs.String("config", "", "path to JSON config file")
	apiAddr := fs.String("api", "", "API listen address (overrides config, default :9090)")
	dataDir := fs.String("data", "", "data directory (overrides config, default ./data)")
	quiet := fs.Bool("quiet", false, "do not print entries to stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig(*configPath, fs.Args())
	if err != nil {
		printUsage(fs)
		return err
	}
	if *apiAddr != "" {
		cfg.API.Addr = *apiAddr
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}

	opts := app.Options{Stdout: os.Stdout}
	if *quiet {
		opts.Stdout = nil
	}
	a, err := app.New(cfg, opts)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(os.Stderr, "🌊 LogFlux starting: %d source(s), API on %s\n", len(cfg.Sources), cfg.API.Addr)
	if err := a.Run(ctx); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "👋 Stopped")
	return nil
}

// loadConfig reads the config file, or builds a single-source config from
// the legacy "file|syslog|http" subcommands
func loadConfig(path string, args []string) (*config.Config, error) {
	if path != "" {
		if len(args) > 0 {
			return nil, fmt.Errorf("use either -config or a subcommand, not both")
		}
		return config.Load(path)
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("no config or source given")
	}

	cfg := config.Default()
	var src config.SourceConfig
	switch args[0] {
	case "file":
		if len(args) < 2 {
			return nil, fmt.Errorf("file path required")
		}
		src = config.SourceConfig{Type: config.SourceFile, Name: "file", Path: args[1]}
	case "syslog":
		if len(args) < 3 {
			return nil, fmt.Errorf("protocol and address required")
		}
		src = config.SourceConfig{Type: config.SourceSyslog, Name: "syslog", Protocol: args[1], Addr: args[2]}
	case "http":
		if len(args) < 2 {
			return nil, fmt.Errorf("address required")
		}
		src = config.SourceConfig{Type: config.SourceHTTP, Name: "http", Addr: args[1]}
	default:
		return nil, fmt.Errorf("unknown mode: %s", args[0])
	}
	cfg.Sources = []config.SourceConfig{src}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func printUsage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  logflux [flags] -config <file.json>")
	fmt.Fprintln(out, "  logflux [flags] file <path>")
	fmt.Fprintln(out, "  logflux [flags] syslog <udp|tcp> <address>")
	fmt.Fprintln(out, "  logflux [flags] http <address>")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Flags:")
	fs.PrintDefaults()
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Examples:")
	fmt.Fprintln(out, "  logflux -config logflux.example.json")
	fmt.Fprintln(out, "  logflux file /var/log/app.log")
	fmt.Fprintln(out, "  logflux syslog udp :5140")
	fmt.Fprintln(out, "  logflux http :8080")
}
