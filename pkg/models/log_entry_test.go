package models

import (
	"testing"
	"time"
)

func TestNewLogEntry(t *testing.T) {
	entry := NewLogEntry()

	if entry == nil {
		t.Fatal("NewLogEntry returned nil")
	}

	if entry.Level != LevelInfo {
		t.Errorf("Expected default level INFO, got %s", entry.Level)
	}

	if entry.Fields == nil {
		t.Error("Fields map should be initialized")
	}

	if entry.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}

	// Timestamp should be recent (within last second)
	if time.Since(entry.Timestamp) > time.Second {
		t.Error("Timestamp should be recent")
	}
}

func TestLogLevelConstants(t *testing.T) {
	tests := []struct {
		level    LogLevel
		expected string
	}{
		{LevelDebug, "DEBUG"},
		{LevelInfo, "INFO"},
		{LevelWarning, "WARNING"},
		{LevelError, "ERROR"},
		{LevelCritical, "CRITICAL"},
	}

	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			if string(tt.level) != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, tt.level)
			}
		})
	}
}
