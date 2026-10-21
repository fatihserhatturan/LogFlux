// Package config loads and validates the LogFlux configuration file.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fatihserhatturan/logflux/internal/parser"
)

// Source types
const (
	SourceFile   = "file"
	SourceSyslog = "syslog"
	SourceHTTP   = "http"
)

// Config is the top-level LogFlux configuration
type Config struct {
	DataDir    string         `json:"data_dir"`
	BufferSize int            `json:"buffer_size"`
	API        APIConfig      `json:"api"`
	Sources    []SourceConfig `json:"sources"`
}

// APIConfig configures the query/metrics HTTP server
type APIConfig struct {
	Addr string `json:"addr"`
}

// SourceConfig configures a single log source
type SourceConfig struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`     // file
	Protocol string `json:"protocol,omitempty"` // syslog: udp|tcp
	Addr     string `json:"addr,omitempty"`     // syslog, http

	// Parser is the line format: auto, json, logfmt, plain or none.
	// Defaults to auto for files and none for syslog/http (already structured).
	Parser string `json:"parser,omitempty"`
	// Multiline joins stack-trace continuation lines (file only)
	Multiline bool `json:"multiline,omitempty"`
}

// Duration is a time.Duration that unmarshals from strings like "5m"
type Duration struct {
	time.Duration
}

// UnmarshalJSON parses a duration string ("30s", "5m") or a number of seconds
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		d.Duration = v
		return nil
	}
	var secs float64
	if err := json.Unmarshal(b, &secs); err != nil {
		return fmt.Errorf("duration must be a string like \"5m\" or seconds")
	}
	d.Duration = time.Duration(secs * float64(time.Second))
	return nil
}

// MarshalJSON writes the duration as a string
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// Load reads, defaults and validates a config file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}
	return Parse(data)
}

// Parse decodes, defaults and validates config JSON
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Default returns a config with defaults and no sources
func Default() *Config {
	cfg := &Config{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults fills unset fields with default values
func (c *Config) ApplyDefaults() {
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.BufferSize <= 0 {
		c.BufferSize = 10000
	}
	if c.API.Addr == "" {
		c.API.Addr = ":9090"
	}
	for i := range c.Sources {
		s := &c.Sources[i]
		s.Type = strings.ToLower(s.Type)
		s.Protocol = strings.ToLower(s.Protocol)
		if s.Name == "" {
			s.Name = fmt.Sprintf("%s-%d", s.Type, i+1)
		}
		if s.Type == SourceSyslog && s.Protocol == "" {
			s.Protocol = "udp"
		}
		if s.Parser == "" {
			s.Parser = parser.FormatNone
			if s.Type == SourceFile {
				s.Parser = parser.FormatAuto
			}
		}
	}
}

// Validate checks the config for errors
func (c *Config) Validate() error {
	if len(c.Sources) == 0 {
		return fmt.Errorf("config: at least one source is required")
	}
	names := make(map[string]bool)
	for i, s := range c.Sources {
		where := fmt.Sprintf("config: sources[%d] (%s)", i, s.Name)
		if names[s.Name] {
			return fmt.Errorf("%s: duplicate source name", where)
		}
		names[s.Name] = true

		switch s.Type {
		case SourceFile:
			if s.Path == "" {
				return fmt.Errorf("%s: path is required", where)
			}
		case SourceSyslog:
			if s.Addr == "" {
				return fmt.Errorf("%s: addr is required", where)
			}
			if s.Protocol != "udp" && s.Protocol != "tcp" {
				return fmt.Errorf("%s: protocol must be udp or tcp", where)
			}
		case SourceHTTP:
			if s.Addr == "" {
				return fmt.Errorf("%s: addr is required", where)
			}
		default:
			return fmt.Errorf("%s: unknown type %q (want file, syslog or http)", where, s.Type)
		}
		if !parser.ValidFormat(s.Parser) {
			return fmt.Errorf("%s: unknown parser %q (want auto, json, logfmt, plain or none)", where, s.Parser)
		}
		if s.Multiline && s.Type != SourceFile {
			return fmt.Errorf("%s: multiline is only supported for file sources", where)
		}
	}
	return nil
}
