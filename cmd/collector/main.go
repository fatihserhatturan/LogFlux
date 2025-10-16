package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fatihserhatturan/logflux/internal/collector/sources"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

func main() {
	fmt.Println("🌊 LogFlux Collector - Starting...")

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	mode := os.Args[1]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	logChan := make(chan *models.LogEntry, 100)

	var err error
	switch mode {
	case "file":
		err = startFileMode(ctx, logChan)
	case "syslog":
		err = startSyslogMode(ctx, logChan)
	case "http":
		err = startHTTPMode(ctx, logChan)
	default:
		fmt.Printf("❌ Unknown mode: %s\n", mode)
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Printf("❌ Failed to start: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ Collector started, processing logs...")
	fmt.Println("Press Ctrl+C to stop")

	go processLogs(logChan)

	<-sigChan
	fmt.Println("\n🛑 Shutting down gracefully...")
	cancel()
	time.Sleep(500 * time.Millisecond)
	fmt.Println("👋 Goodbye!")
}

func startFileMode(ctx context.Context, out chan<- *models.LogEntry) error {
	if len(os.Args) < 3 {
		return fmt.Errorf("file path required")
	}

	logFile := os.Args[2]
	logFile = filepath.Clean(logFile)

	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		absPath, _ := filepath.Abs(logFile)
		return fmt.Errorf("file not found: %s (absolute: %s)", logFile, absPath)
	}

	fmt.Printf("📂 Reading from file: %s\n", logFile)

	reader := sources.NewFileReader(logFile)
	return reader.Start(ctx, out)
}

func startSyslogMode(ctx context.Context, out chan<- *models.LogEntry) error {
	if len(os.Args) < 4 {
		return fmt.Errorf("protocol and address required")
	}

	protocol := os.Args[2]
	addr := os.Args[3]

	fmt.Printf("📡 Starting syslog receiver: %s on %s\n", protocol, addr)

	receiver := sources.NewSyslogReceiver(addr, protocol)
	return receiver.Start(ctx, out)
}

func startHTTPMode(ctx context.Context, out chan<- *models.LogEntry) error {
	if len(os.Args) < 3 {
		return fmt.Errorf("address required")
	}

	addr := os.Args[2] // e.g., ":8080"

	fmt.Printf("📡 Starting HTTP receiver on %s\n", addr)

	receiver := sources.NewHTTPReceiver(addr)
	return receiver.Start(ctx, out)
}

func processLogs(logChan <-chan *models.LogEntry) {
	count := 0
	for entry := range logChan {
		count++
		fmt.Printf("[%d] %s [%s] %s: %s",
			count,
			entry.Timestamp.Format(time.RFC3339),
			entry.Level,
			entry.Source,
			entry.Message,
		)
		if len(entry.Message) > 0 && entry.Message[len(entry.Message)-1] != '\n' {
			fmt.Println()
		}
	}
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  File mode:   logflux file <path>")
	fmt.Println("  Syslog mode: logflux syslog <udp|tcp> <address>")
	fmt.Println("  HTTP mode:   logflux http <address>") // YENİ!
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  logflux file test/testdata/sample.log")
	fmt.Println("  logflux syslog udp :514")
	fmt.Println("  logflux syslog tcp :514")
	fmt.Println("  logflux http :8080")
}
