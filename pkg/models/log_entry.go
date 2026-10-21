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
	if l, ok := LookupLevel(s); ok {
		return l
	}
	return LevelInfo
}

// LookupLevel converts a level name (case-insensitive) to a LogLevel and
// reports whether the name was recognised. Common aliases from other logging
// libraries (FATAL, TRACE, NOTICE, ...) are mapped to the closest level.
func LookupLevel(s string) (LogLevel, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "TRACE", "DEBUG", "DBG":
		return LevelDebug, true
	case "INFO", "INFORMATION", "NOTICE", "INF":
		return LevelInfo, true
	case "WARNING", "WARN", "WRN":
		return LevelWarning, true
	case "ERROR", "ERR", "SEVERE":
		return LevelError, true
	case "CRITICAL", "CRIT", "FATAL", "PANIC", "EMERG", "EMERGENCY", "ALERT":
		return LevelCritical, true
	default:
		return "", false
	}
}

// Severity returns a numeric rank for comparisons (DEBUG=0 ... CRITICAL=4)
func (l LogLevel) Severity() int {
	switch l {
	case LevelDebug:
		return 0
	case LevelWarning:
		return 2
	case LevelError:
		return 3
	case LevelCritical:
		return 4
	default:
		return 1
	}
}

// newID returns a random 128-bit hex identifier
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return hex.EncodeToString(b)
}
