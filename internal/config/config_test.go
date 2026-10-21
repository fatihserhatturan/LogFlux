package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParse_Defaults(t *testing.T) {
	cfg, err := Parse([]byte(`{"sources":[{"type":"SYSLOG","addr":":5140"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "./data" || cfg.BufferSize != 10000 || cfg.API.Addr != ":9090" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	s := cfg.Sources[0]
	if s.Type != "syslog" || s.Protocol != "udp" || s.Name != "syslog-1" || s.Parser != "none" {
		t.Errorf("unexpected source defaults: %+v", s)
	}
}

func TestParse_Errors(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"no sources", `{}`, "at least one source"},
		{"unknown type", `{"sources":[{"type":"kafka"}]}`, "unknown type"},
		{"file without path", `{"sources":[{"type":"file"}]}`, "path is required"},
		{"bad protocol", `{"sources":[{"type":"syslog","addr":":1","protocol":"sctp"}]}`, "udp or tcp"},
		{"http without addr", `{"sources":[{"type":"http"}]}`, "addr is required"},
		{"duplicate names", `{"sources":[{"type":"http","name":"a","addr":":1"},{"type":"http","name":"a","addr":":2"}]}`, "duplicate"},
		{"unknown field", `{"sorces":[]}`, "unknown field"},
		{"bad parser", `{"sources":[{"type":"file","path":"x","parser":"xml"}]}`, "unknown parser"},
		{"multiline on http", `{"sources":[{"type":"http","addr":":1","multiline":true}]}`, "only supported for file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.json))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestDuration_UnmarshalJSON(t *testing.T) {
	var v struct {
		A Duration `json:"a"`
		B Duration `json:"b"`
	}
	if err := json.Unmarshal([]byte(`{"a":"5m","b":30}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A.Duration != 5*time.Minute || v.B.Duration != 30*time.Second {
		t.Errorf("got %v, %v", v.A, v.B)
	}
	if err := json.Unmarshal([]byte(`{"a":"soon"}`), &v); err == nil {
		t.Error("expected error for invalid duration")
	}
}
