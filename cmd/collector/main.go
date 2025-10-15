package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fatihserhatturan/logflux/internal/collector/sources"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

func main() {
	fmt.Println("🌊 LogFlux Collector - Starting...")

	// Check if file argument provided
	if len(os.Args) < 2 {
		fmt.Println("Usage: logflux <logfile>")
		fmt.Println("Example: logflux test/testdata/sample.log")
		os.Exit(1)
	}

	logFile := os.Args[1]
	fmt.Printf("📂 Reading from: %s\n", logFile)

	// Create file reader
	reader := sources.NewFileReader(logFile)

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Channel for log entries
	logChan := make(chan *models.LogEntry, 100)

	// Start reader
	if err := reader.Start(ctx, logChan); err != nil {
		fmt.Printf("❌ Failed to start reader: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ Reader started, processing logs...")
	fmt.Println("Press Ctrl+C to stop")

	// Process logs
	go func() {
		count := 0
		for entry := range logChan {
			count++
			fmt.Printf("[%d] %s: %s",
				count,
				entry.Timestamp.Format(time.RFC3339),
				entry.Message,
			)
		}
	}()

	// Wait for interrupt
	<-sigChan
	fmt.Println("\n🛑 Shutting down gracefully...")
	cancel()
	time.Sleep(500 * time.Millisecond)
	fmt.Println("👋 Goodbye!")
}
