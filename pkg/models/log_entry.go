package models

import (
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
		Timestamp: time.Now(),
		Level:     LevelInfo,
		Fields:    make(map[string]interface{}),
	}
}
