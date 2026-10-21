package parser

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

// Line formats
const (
	FormatAuto   = "auto"
	FormatJSON   = "json"
	FormatLogfmt = "logfmt"
	FormatPlain  = "plain"
	FormatNone   = "none"
)

// Common key names used by logging libraries
var (
	levelKeys   = []string{"level", "lvl", "severity", "loglevel", "log.level"}
	messageKeys = []string{"msg", "message", "log", "text"}
	timeKeys    = []string{"time", "ts", "timestamp", "@timestamp", "t", "datetime"}
)

// ValidFormat reports whether f is a known line format
func ValidFormat(f string) bool {
	switch f {
	case FormatAuto, FormatJSON, FormatLogfmt, FormatPlain, FormatNone:
		return true
	}
	return false
}

// LineParser parses the entry message according to a format.
// It implements pipeline.Processor.
type LineParser struct {
	format string
}

// NewLineParser creates a parser for the given format
func NewLineParser(format string) (*LineParser, error) {
	if format == "" {
		format = FormatAuto
	}
	if !ValidFormat(format) {
		return nil, fmt.Errorf("unknown parser format %q", format)
	}
	return &LineParser{format: format}, nil
}

// Process parses entry.Message in place. It never drops entries.
func (p *LineParser) Process(entry *models.LogEntry) (*models.LogEntry, bool) {
	line := entry.Message
	switch p.format {
	case FormatNone:
	case FormatJSON:
		if !applyJSON(entry, line) {
			applyPlain(entry, line)
		}
	case FormatLogfmt:
		if !applyLogfmt(entry, line, false) {
			applyPlain(entry, line)
		}
	case FormatPlain:
		applyPlain(entry, line)
	default: // auto
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "{") && applyJSON(entry, trimmed):
		case applyLogfmt(entry, line, true):
		default:
			applyPlain(entry, line)
		}
	}
	return entry, true
}

// applyJSON parses a JSON object line
func applyJSON(entry *models.LogEntry, line string) bool {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return false
	}
	applyMap(entry, m)
	return true
}

// applyLogfmt parses key=value pairs. In strict mode (auto detection) the
// line must contain at least two pairs and a level or message key.
func applyLogfmt(entry *models.LogEntry, line string, strict bool) bool {
	pairs := parseLogfmt(line)
	if len(pairs) == 0 {
		return false
	}
	m := make(map[string]interface{}, len(pairs))
	for _, kv := range pairs {
		m[kv[0]] = kv[1]
	}
	if strict {
		_, _, hasLevel := firstString(m, levelKeys)
		_, _, hasMsg := firstString(m, messageKeys)
		if len(pairs) < 2 || (!hasLevel && !hasMsg) {
			return false
		}
	}
	applyMap(entry, m)
	return true
}

// applyMap moves well-known keys (level, message, time) into the entry and
// the remaining keys into entry.Fields
func applyMap(entry *models.LogEntry, m map[string]interface{}) {
	if k, v, ok := firstString(m, levelKeys); ok {
		if l, ok := models.LookupLevel(v); ok {
			entry.Level = l
			delete(m, k)
		}
	}
	if k, v, ok := firstString(m, messageKeys); ok {
		entry.Message = v
		delete(m, k)
	}
	for _, k := range timeKeys {
		if t, ok := parseTimeValue(m[k]); ok {
			entry.Timestamp = t
			delete(m, k)
			break
		}
	}
	for k, v := range m {
		entry.Fields[k] = v
	}
}

// parseLogfmt splits `a=1 b="two words"` into pairs. Any bare word that is
// not key=value makes the line invalid logfmt (returns nil).
func parseLogfmt(line string) [][2]string {
	var pairs [][2]string
	i := 0
	n := len(line)
	for i < n {
		for i < n && line[i] == ' ' {
			i++
		}
		if i >= n {
			break
		}
		start := i
		for i < n && line[i] != '=' && line[i] != ' ' {
			i++
		}
		if i >= n || line[i] != '=' || i == start {
			return nil // bare word: not logfmt
		}
		key := line[start:i]
		i++ // '='
		var val string
		if i < n && line[i] == '"' {
			i++
			var b strings.Builder
			for i < n && line[i] != '"' {
				if line[i] == '\\' && i+1 < n {
					i++
				}
				b.WriteByte(line[i])
				i++
			}
			i++ // closing quote
			val = b.String()
		} else {
			start = i
			for i < n && line[i] != ' ' {
				i++
			}
			val = line[start:i]
		}
		pairs = append(pairs, [2]string{key, val})
	}
	return pairs
}

// applyPlain extracts a leading timestamp and a level keyword from free text
func applyPlain(entry *models.LogEntry, line string) {
	rest := line
	if t, after, ok := leadingTimestamp(rest); ok {
		entry.Timestamp = t
		rest = after
	}
	if l, ok := DetectLevel(rest); ok {
		entry.Level = l
	}
	entry.Message = strings.TrimSpace(line)
}

// DetectLevel looks for a level name among the first few tokens of a line,
// e.g. "ERROR something", "[warn] something", "level=error ...". To avoid
// false positives in prose ("no error found"), a token only counts when it
// is the first token, upper case, bracketed or a level= pair.
func DetectLevel(line string) (models.LogLevel, bool) {
	tokens := strings.Fields(line)
	if len(tokens) > 5 {
		tokens = tokens[:5]
	}
	for i, raw := range tokens {
		tok := strings.Trim(raw, "[]():<>|,;")
		isPair := false
		if j := strings.IndexByte(tok, '='); j > 0 {
			tok = tok[j+1:]
			isPair = true
		}
		marked := i == 0 || isPair || tok == strings.ToUpper(tok) || strings.HasPrefix(raw, "[")
		if !marked {
			continue
		}
		if l, ok := models.LookupLevel(tok); ok {
			return l, true
		}
	}
	return "", false
}

var plainLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.000000",
	"2006-01-02 15:04:05.000",
	"2006-01-02 15:04:05,000",
	"2006-01-02 15:04:05",
	"2006/01/02 15:04:05",
}

// leadingTimestamp parses a timestamp made of the first one or two tokens
func leadingTimestamp(line string) (time.Time, string, bool) {
	tokens := strings.SplitN(strings.TrimLeft(line, "["), " ", 3)
	for n := 1; n <= 2 && n <= len(tokens); n++ {
		text := strings.TrimRight(strings.Join(tokens[:n], " "), "]")
		for _, layout := range plainLayouts {
			if t, err := time.ParseInLocation(layout, text, time.Local); err == nil {
				return t, strings.Join(tokens[n:], " "), true
			}
		}
	}
	return time.Time{}, line, false
}

func firstString(m map[string]interface{}, keys []string) (string, string, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return k, s, true
			}
		}
	}
	return "", "", false
}

// parseTimeValue accepts RFC 3339 strings and unix seconds/milliseconds
func parseTimeValue(v interface{}) (time.Time, bool) {
	switch x := v.(type) {
	case string:
		for _, layout := range plainLayouts {
			if t, err := time.Parse(layout, x); err == nil {
				return t, true
			}
		}
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return unixTime(f), true
		}
	case float64:
		return unixTime(x), true
	}
	return time.Time{}, false
}

func unixTime(f float64) time.Time {
	if f > 1e12 { // milliseconds
		return time.UnixMilli(int64(f))
	}
	sec := int64(f)
	return time.Unix(sec, int64((f-float64(sec))*1e9))
}
