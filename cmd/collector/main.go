package main

import (
	"fmt"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

func main() {
	fmt.Println("🌊 LogFlux - Starting...")

	// Test our first model
	entry := models.NewLogEntry()
	entry.Source = "test"
	entry.Message = "Hello, LogFlux!"

	fmt.Printf("Created log entry: %+v\n", entry)
	fmt.Println("✅ LogFlux foundation ready!")
}
