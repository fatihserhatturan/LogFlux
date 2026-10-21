// Package parser turns raw log lines into structured log entries.
package parser

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

// Syslog is a parsed syslog message (RFC 5424 or RFC 3164)
type Syslog struct {
	Priority       int
	Facility       int
	Severity       int
	Version        int // 1 for RFC 5424, 0 for RFC 3164
	Timestamp      time.Time
	Hostname       string
	AppName        string
	ProcID         string
	MsgID          string
	StructuredData map[string]map[string]string
	Message        string
}

// Level maps the syslog severity to a LogLevel
func (s *Syslog) Level() models.LogLevel {
	switch {
	case s.Severity <= 2: // emerg, alert, crit
		return models.LevelCritical
	case s.Severity == 3:
		return models.LevelError
	case s.Severity == 4:
		return models.LevelWarning
	case s.Severity == 7:
		return models.LevelDebug
	default: // notice, info
		return models.LevelInfo
	}
}

// Apply copies the parsed fields into entry
func (s *Syslog) Apply(entry *models.LogEntry) {
	entry.Message = s.Message
	entry.Level = s.Level()
	if !s.Timestamp.IsZero() {
		entry.Timestamp = s.Timestamp
	}
	entry.Fields["facility"] = s.Facility
	entry.Fields["severity"] = s.Severity
	setIf(entry.Fields, "host", s.Hostname)
	setIf(entry.Fields, "app", s.AppName)
	setIf(entry.Fields, "procid", s.ProcID)
	setIf(entry.Fields, "msgid", s.MsgID)
	if len(s.StructuredData) > 0 {
		entry.Fields["sd"] = s.StructuredData
	}
}

func setIf(m map[string]interface{}, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// ParseSyslog parses an RFC 5424 or RFC 3164 message. now is used to fill
// the missing year of RFC 3164 timestamps.
func ParseSyslog(raw string, now time.Time) (*Syslog, error) {
	raw = strings.TrimRight(raw, "\r\n\x00")
	if !strings.HasPrefix(raw, "<") {
		return nil, fmt.Errorf("syslog: missing PRI")
	}
	end := strings.IndexByte(raw, '>')
	if end < 2 || end > 4 {
		return nil, fmt.Errorf("syslog: invalid PRI")
	}
	pri, err := strconv.Atoi(raw[1:end])
	if err != nil || pri < 0 || pri > 191 {
		return nil, fmt.Errorf("syslog: invalid PRI %q", raw[1:end])
	}

	s := &Syslog{Priority: pri, Facility: pri / 8, Severity: pri % 8}
	rest := raw[end+1:]

	if strings.HasPrefix(rest, "1 ") {
		s.Version = 1
		parse5424(s, rest[2:])
		return s, nil
	}
	parse3164(s, rest, now)
	return s, nil
}

// parse5424: TIMESTAMP SP HOSTNAME SP APP-NAME SP PROCID SP MSGID SP SD [SP MSG]
func parse5424(s *Syslog, rest string) {
	fields := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		var tok string
		tok, rest = nextToken(rest)
		fields = append(fields, tok)
	}
	nil2empty := func(v string) string {
		if v == "-" {
			return ""
		}
		return v
	}

	if ts := fields[0]; ts != "-" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			s.Timestamp = t
		}
	}
	s.Hostname = nil2empty(fields[1])
	s.AppName = nil2empty(fields[2])
	s.ProcID = nil2empty(fields[3])
	s.MsgID = nil2empty(fields[4])

	if strings.HasPrefix(rest, "-") {
		rest = strings.TrimPrefix(rest[1:], " ")
	} else if strings.HasPrefix(rest, "[") {
		sd, remaining := parseStructuredData(rest)
		s.StructuredData = sd
		rest = strings.TrimPrefix(remaining, " ")
	}
	s.Message = strings.TrimPrefix(rest, "\xEF\xBB\xBF") // optional UTF-8 BOM
}

// parseStructuredData parses one or more [id key="value" ...] elements
func parseStructuredData(in string) (map[string]map[string]string, string) {
	sd := make(map[string]map[string]string)
	i := 0
	for i < len(in) && in[i] == '[' {
		i++
		// SD-ID
		start := i
		for i < len(in) && in[i] != ' ' && in[i] != ']' {
			i++
		}
		id := in[start:i]
		params := make(map[string]string)
		for i < len(in) && in[i] == ' ' {
			i++
			start = i
			for i < len(in) && in[i] != '=' && in[i] != ']' {
				i++
			}
			if i >= len(in) || in[i] != '=' {
				break
			}
			name := in[start:i]
			i++ // '='
			if i >= len(in) || in[i] != '"' {
				break
			}
			i++ // opening quote
			var v strings.Builder
			for i < len(in) && in[i] != '"' {
				if in[i] == '\\' && i+1 < len(in) && strings.IndexByte(`"\]`, in[i+1]) >= 0 {
					i++
				}
				v.WriteByte(in[i])
				i++
			}
			i++ // closing quote
			params[name] = v.String()
		}
		// skip to end of element
		for i < len(in) && in[i] != ']' {
			i++
		}
		if i < len(in) {
			i++
		}
		sd[id] = params
	}
	return sd, in[i:]
}

// parse3164: [TIMESTAMP SP HOSTNAME SP] TAG[PID]: MSG
func parse3164(s *Syslog, rest string, now time.Time) {
	// Timestamp "Mmm dd hh:mm:ss" (day may be space padded)
	if len(rest) >= 16 && rest[15] == ' ' {
		if t, err := time.ParseInLocation(time.Stamp, rest[:15], now.Location()); err == nil {
			t = t.AddDate(now.Year(), 0, 0)
			// Messages from late December received in January
			if t.After(now.Add(24 * time.Hour)) {
				t = t.AddDate(-1, 0, 0)
			}
			s.Timestamp = t
			rest = rest[16:]

			// Hostname, unless the next token is already the tag
			if tok, after := nextToken(rest); tok != "" && !isTag(tok) && after != "" {
				s.Hostname = tok
				rest = after
			}
		}
	}

	// TAG[PID]: MSG
	if tok, after := nextToken(rest); isTag(tok) {
		tag := strings.TrimSuffix(tok, ":")
		if open := strings.IndexByte(tag, '['); open > 0 && strings.HasSuffix(tag, "]") {
			s.ProcID = tag[open+1 : len(tag)-1]
			tag = tag[:open]
		}
		s.AppName = tag
		rest = after
	}
	s.Message = rest
}

// isTag reports whether tok looks like "app:" or "app[123]:"
func isTag(tok string) bool {
	if !strings.HasSuffix(tok, ":") || len(tok) < 2 || len(tok) > 49 {
		return false
	}
	for _, r := range strings.TrimSuffix(tok, ":") {
		if !isTagRune(r) {
			return false
		}
	}
	return true
}

func isTagRune(r rune) bool {
	switch {
	case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return true
	}
	return strings.ContainsRune("[]-_./", r)
}

// nextToken splits off the next space-separated token
func nextToken(s string) (string, string) {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}
