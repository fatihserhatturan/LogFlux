package models

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// LogLevel represents the severity of a log entry
type LogLevel string

const (
	LevelDebug    LogLevel = "DEBUG"
	LevelInfo     LogLevel = "INFO"
	LevelWarning  LogLevel = "WARNING"
	LevelError    LogLevel = "ERROR"
	LevelCritical LogLevel = "CRITICAL"
)

// LogEntry represents a single log entry
type LogEntry struct {
	ID        string                 `json:"id"`
	Timestamp time.Time              `json:"timestamp"`
	Level     LogLevel               `json:"level"`
	Source    string                 `json:"source"`
	Message   string                 `json:"message"`
	Fields    map[string]interface{} `json:"fields,omitempty"`
}

// NewLogEntry creates a new log entry with defaults
func NewLogEntry() *LogEntry {
	return &LogEntry{
		ID:        newID(),
		Timestamp: time.Now(),
		Level:     LevelInfo,
		Fields:    make(map[string]interface{}),
	}
}

// ParseLevel converts a level name (case-insensitive) to a LogLevel.
// Unknown values default to INFO.
func ParseLevel(s string) LogLevel {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return LevelDebug
	case "WARNING", "WARN":
		return LevelWarning
	case "ERROR", "ERR":
		return LevelError
	case "CRITICAL", "CRIT":
		return LevelCritical
	default:
		return LevelInfo
	}
}

// newID returns a random 128-bit hex identifier
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return hex.EncodeToString(b)
}
