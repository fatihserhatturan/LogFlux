package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "single":
		sendSingle()
	case "batch":
		sendBatch()
	case "health":
		checkHealth()
	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func sendSingle() {
	if len(os.Args) < 5 {
		fmt.Println("Usage: send_http single <address> <level> <message>")
		fmt.Println("Example: send_http single localhost:8080 ERROR 'Database connection failed'")
		os.Exit(1)
	}

	address := os.Args[2]
	level := os.Args[3]
	message := os.Args[4]

	logData := map[string]interface{}{
		"level":   level,
		"message": message,
		"source":  "http-test-tool",
		"fields": map[string]interface{}{
			"test": true,
		},
	}

	body, err := json.Marshal(logData)
	if err != nil {
		fmt.Printf("❌ Failed to marshal JSON: %v\n", err)
		os.Exit(1)
	}

	url := fmt.Sprintf("http://%s/logs", address)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Printf("❌ Failed to send request: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		var result map[string]string
		json.NewDecoder(resp.Body).Decode(&result)
		fmt.Printf("✅ Log sent successfully! ID: %s\n", result["id"])
	} else {
		fmt.Printf("❌ Server returned status: %d\n", resp.StatusCode)
	}
}

func sendBatch() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: send_http batch <address>")
		fmt.Println("Example: send_http batch localhost:8080")
		os.Exit(1)
	}

	address := os.Args[2]

	logs := []map[string]interface{}{
		{
			"level":   "INFO",
			"message": "Application started",
			"source":  "app",
		},
		{
			"level":   "DEBUG",
			"message": "Loading configuration",
			"source":  "app",
		},
		{
			"level":   "WARNING",
			"message": "High memory usage: 85%",
			"source":  "monitor",
		},
		{
			"level":   "ERROR",
			"message": "Database connection timeout",
			"source":  "database",
			"fields": map[string]interface{}{
				"host":    "db.example.com",
				"timeout": 30,
			},
		},
		{
			"level":   "CRITICAL",
			"message": "System crash imminent",
			"source":  "system",
		},
	}

	body, err := json.Marshal(logs)
	if err != nil {
		fmt.Printf("❌ Failed to marshal JSON: %v\n", err)
		os.Exit(1)
	}

	url := fmt.Sprintf("http://%s/batch", address)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Printf("❌ Failed to send request: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		var result map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&result)
		fmt.Printf("✅ Batch sent successfully!\n")
		fmt.Printf("   Total: %.0f, Accepted: %.0f\n", result["total"], result["accepted"])
	} else {
		fmt.Printf("❌ Server returned status: %d\n", resp.StatusCode)
	}
}

func checkHealth() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: send_http health <address>")
		fmt.Println("Example: send_http health localhost:8080")
		os.Exit(1)
	}

	address := os.Args[2]
	url := fmt.Sprintf("http://%s/health", address)

	resp, err := http.Get(url)
	if err != nil {
		fmt.Printf("❌ Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var health map[string]string
		json.NewDecoder(resp.Body).Decode(&health)
		fmt.Printf("✅ Server is healthy!\n")
		fmt.Printf("   Status: %s\n", health["status"])
		fmt.Printf("   Time: %s\n", health["time"])
	} else {
		fmt.Printf("❌ Server returned status: %d\n", resp.StatusCode)
	}
}

func printUsage() {
	fmt.Println("HTTP Test Tool - Send logs to LogFlux HTTP receiver")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  single  - Send a single log entry")
	fmt.Println("  batch   - Send multiple log entries")
	fmt.Println("  health  - Check server health")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  send_http single localhost:8080 ERROR 'Connection failed'")
	fmt.Println("  send_http single localhost:8080 INFO 'User logged in'")
	fmt.Println("  send_http batch localhost:8080")
	fmt.Println("  send_http health localhost:8080")
}
