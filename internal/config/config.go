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
	"github.com/fatihserhatturan/logflux/pkg/models"
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
	Storage    StorageConfig  `json:"storage"`
	Detect     DetectConfig   `json:"detect"`
	Notify     []NotifyConfig `json:"notify"`
	Sources    []SourceConfig `json:"sources"`
}

// DetectConfig configures anomaly detection
type DetectConfig struct {
	Disabled   bool             `json:"disabled,omitempty"`
	Warmup     Duration         `json:"warmup,omitempty"`   // default 1h
	Cooldown   Duration         `json:"cooldown,omitempty"` // default 10m
	NewPattern NewPatternConfig `json:"new_pattern"`
	Spike      SpikeConfig      `json:"spike"`
	Silence    SilenceConfig    `json:"silence"`
	Rules      []RuleConfig     `json:"rules,omitempty"`
}

// NewPatternConfig configures new-template alerts
type NewPatternConfig struct {
	Disabled bool   `json:"disabled,omitempty"`
	MinLevel string `json:"min_level,omitempty"` // default warning
}

// SpikeConfig configures error-rate spike alerts
type SpikeConfig struct {
	Disabled   bool     `json:"disabled,omitempty"`
	Levels     []string `json:"levels,omitempty"`      // default error, critical
	Threshold  float64  `json:"threshold,omitempty"`   // robust z-score, default 3.5
	MinCount   int64    `json:"min_count,omitempty"`   // default 10 per minute
	History    int      `json:"history,omitempty"`     // minutes, default 60
	MinHistory int      `json:"min_history,omitempty"` // minutes, default 10
}

// SilenceConfig configures silent-source alerts
type SilenceConfig struct {
	Disabled bool     `json:"disabled,omitempty"`
	After    Duration `json:"after,omitempty"`    // default 5m
	MinRate  float64  `json:"min_rate,omitempty"` // entries/minute, default 1
}

// RuleConfig is a threshold rule: alert when Threshold entries matching
// the filter arrive within Window
type RuleConfig struct {
	Name      string            `json:"name"`
	Q         string            `json:"q,omitempty"`
	Sources   []string          `json:"sources,omitempty"`
	Levels    []string          `json:"levels,omitempty"`
	MinLevel  string            `json:"min_level,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
	Window    Duration          `json:"window"`
	Threshold int               `json:"threshold"`
	GroupBy   string            `json:"group_by,omitempty"`
	Severity  string            `json:"severity,omitempty"` // default warning
}

// NotifyConfig configures an alert destination
type NotifyConfig struct {
	Type   string `json:"type"`             // stdout or webhook
	URL    string `json:"url,omitempty"`    // webhook
	Format string `json:"format,omitempty"` // webhook: json (default) or slack
}

// StorageConfig configures on-disk storage of entries
type StorageConfig struct {
	Disabled         bool     `json:"disabled,omitempty"`
	SegmentMaxSizeMB int64    `json:"segment_max_size_mb,omitempty"` // default 64
	SegmentMaxAge    Duration `json:"segment_max_age,omitempty"`     // default 1h
	RetentionMaxAge  Duration `json:"retention_max_age,omitempty"`   // default 168h unless retention_max_size_mb is set
	RetentionMaxSize int64    `json:"retention_max_size_mb,omitempty"`
	FlushInterval    Duration `json:"flush_interval,omitempty"` // default 1s
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
	if c.Storage.SegmentMaxSizeMB <= 0 {
		c.Storage.SegmentMaxSizeMB = 64
	}
	if c.Storage.SegmentMaxAge.Duration <= 0 {
		c.Storage.SegmentMaxAge.Duration = time.Hour
	}
	if c.Storage.RetentionMaxAge.Duration <= 0 && c.Storage.RetentionMaxSize <= 0 {
		c.Storage.RetentionMaxAge.Duration = 7 * 24 * time.Hour
	}
	if c.Storage.FlushInterval.Duration <= 0 {
		c.Storage.FlushInterval.Duration = time.Second
	}
	if c.Detect.Cooldown.Duration <= 0 {
		c.Detect.Cooldown.Duration = 10 * time.Minute
	}
	for i := range c.Notify {
		c.Notify[i].Type = strings.ToLower(c.Notify[i].Type)
		c.Notify[i].Format = strings.ToLower(c.Notify[i].Format)
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
	if err := c.validateDetect(); err != nil {
		return err
	}
	for i, n := range c.Notify {
		where := fmt.Sprintf("config: notify[%d]", i)
		switch n.Type {
		case "stdout":
		case "webhook":
			if !strings.HasPrefix(n.URL, "http://") && !strings.HasPrefix(n.URL, "https://") {
				return fmt.Errorf("%s: webhook url must start with http:// or https://", where)
			}
			if n.Format != "" && n.Format != "json" && n.Format != "slack" {
				return fmt.Errorf("%s: format must be json or slack", where)
			}
		default:
			return fmt.Errorf("%s: unknown type %q (want stdout or webhook)", where, n.Type)
		}
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

func (c *Config) validateDetect() error {
	d := c.Detect
	checkLevel := func(where, l string) error {
		if l == "" {
			return nil
		}
		if _, ok := models.LookupLevel(l); !ok {
			return fmt.Errorf("config: %s: unknown level %q", where, l)
		}
		return nil
	}
	if err := checkLevel("detect.new_pattern.min_level", d.NewPattern.MinLevel); err != nil {
		return err
	}
	for _, l := range d.Spike.Levels {
		if err := checkLevel("detect.spike.levels", l); err != nil {
			return err
		}
	}
	names := make(map[string]bool)
	for i, r := range d.Rules {
		where := fmt.Sprintf("detect.rules[%d]", i)
		if r.Name == "" {
			return fmt.Errorf("config: %s: name is required", where)
		}
		if names[r.Name] {
			return fmt.Errorf("config: %s: duplicate rule name %q", where, r.Name)
		}
		names[r.Name] = true
		if r.Window.Duration <= 0 || r.Threshold <= 0 {
			return fmt.Errorf("config: %s (%s): window and threshold must be positive", where, r.Name)
		}
		for _, l := range append(append([]string{}, r.Levels...), r.MinLevel, r.Severity) {
			if err := checkLevel(where, l); err != nil {
				return err
			}
		}
	}
	return nil
}
