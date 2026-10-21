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

func TestNewLogEntry_ID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := NewLogEntry().ID
		if len(id) != 32 {
			t.Fatalf("Expected 32-char ID, got %q", id)
		}
		if seen[id] {
			t.Fatalf("Duplicate ID: %s", id)
		}
		seen[id] = true
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected LogLevel
	}{
		{"DEBUG", LevelDebug},
		{"debug", LevelDebug},
		{"INFO", LevelInfo},
		{"WARNING", LevelWarning},
		{"warn", LevelWarning},
		{"ERROR", LevelError},
		{"err", LevelError},
		{"CRITICAL", LevelCritical},
		{"crit", LevelCritical},
		{" Error ", LevelError},
		{"fatal", LevelCritical},
		{"trace", LevelDebug},
		{"notice", LevelInfo},
		{"", LevelInfo},
		{"unknown", LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := ParseLevel(tt.input); got != tt.expected {
				t.Errorf("ParseLevel(%q) = %s, want %s", tt.input, got, tt.expected)
			}
		})
	}
}

func TestLookupLevel_Unknown(t *testing.T) {
	if _, ok := LookupLevel("hello"); ok {
		t.Error("expected unknown level")
	}
}

func TestLogLevel_Severity(t *testing.T) {
	order := []LogLevel{LevelDebug, LevelInfo, LevelWarning, LevelError, LevelCritical}
	for i := 1; i < len(order); i++ {
		if order[i-1].Severity() >= order[i].Severity() {
			t.Errorf("%s should rank below %s", order[i-1], order[i])
		}
	}
}
