package parser

import (
	"bufio"
	"strings"
	"testing"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

var now = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

func TestParseSyslog_RFC5424(t *testing.T) {
	// Examples from RFC 5424 section 6.5
	tests := []struct {
		raw     string
		host    string
		app     string
		procid  string
		msgid   string
		msg     string
		sev     int
		sdID    string
		sdKey   string
		sdValue string
	}{
		{
			raw:  "<34>1 2003-10-11T22:14:15.003Z mymachine.example.com su - ID47 - 'su root' failed for lonvick on /dev/pts/8",
			host: "mymachine.example.com", app: "su", msgid: "ID47",
			msg: "'su root' failed for lonvick on /dev/pts/8", sev: 2,
		},
		{
			raw:  "<165>1 2003-08-24T05:14:15.000003-07:00 192.0.2.1 myproc 8710 - - %% It's time to make the do-nuts.",
			host: "192.0.2.1", app: "myproc", procid: "8710",
			msg: "%% It's time to make the do-nuts.", sev: 5,
		},
		{
			raw:  `<165>1 2003-10-11T22:14:15.003Z mymachine.example.com evntslog - ID47 [exampleSDID@32473 iut="3" eventSource="Appli\"cation" eventID="1011"] An application event log entry...`,
			host: "mymachine.example.com", app: "evntslog", msgid: "ID47",
			msg: "An application event log entry...", sev: 5,
			sdID: "exampleSDID@32473", sdKey: "eventSource", sdValue: `Appli"cation`,
		},
		{
			raw:  `<165>1 2003-10-11T22:14:15.003Z mymachine.example.com evntslog - ID47 [exampleSDID@32473 iut="3"][examplePriority@32473 class="high"]`,
			host: "mymachine.example.com", app: "evntslog", msgid: "ID47",
			msg: "", sev: 5,
			sdID: "examplePriority@32473", sdKey: "class", sdValue: "high",
		},
	}
	for _, tt := range tests {
		t.Run(tt.app, func(t *testing.T) {
			s, err := ParseSyslog(tt.raw, now)
			if err != nil {
				t.Fatal(err)
			}
			if s.Version != 1 || s.Hostname != tt.host || s.AppName != tt.app ||
				s.ProcID != tt.procid || s.MsgID != tt.msgid || s.Message != tt.msg || s.Severity != tt.sev {
				t.Errorf("got %+v", s)
			}
			if s.Timestamp.IsZero() {
				t.Error("timestamp not parsed")
			}
			if tt.sdID != "" && s.StructuredData[tt.sdID][tt.sdKey] != tt.sdValue {
				t.Errorf("sd[%s][%s] = %q, want %q (%v)", tt.sdID, tt.sdKey,
					s.StructuredData[tt.sdID][tt.sdKey], tt.sdValue, s.StructuredData)
			}
		})
	}
}

func TestParseSyslog_RFC3164(t *testing.T) {
	tests := []struct {
		raw    string
		host   string
		app    string
		procid string
		msg    string
		ts     bool
	}{
		{"<34>Oct 11 22:14:15 mymachine su: 'su root' failed for lonvick on /dev/pts/8", "mymachine", "su", "", "'su root' failed for lonvick on /dev/pts/8", true},
		{"<13>Feb  5 17:32:18 10.0.0.99 sshd[1234]: Accepted password for user", "10.0.0.99", "sshd", "1234", "Accepted password for user", true},
		{"<13>Feb  5 17:32:18 cron[99]: job started", "", "cron", "99", "job started", true},
		{"<34>Error occurred in system", "", "", "", "Error occurred in system", false},
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			s, err := ParseSyslog(tt.raw, now)
			if err != nil {
				t.Fatal(err)
			}
			if s.Version != 0 || s.Hostname != tt.host || s.AppName != tt.app || s.ProcID != tt.procid || s.Message != tt.msg {
				t.Errorf("got %+v", s)
			}
			if tt.ts != !s.Timestamp.IsZero() {
				t.Errorf("timestamp parsed = %v, want %v", !s.Timestamp.IsZero(), tt.ts)
			}
			if tt.ts && s.Timestamp.Year() != 2026 {
				t.Errorf("year = %d, want 2026", s.Timestamp.Year())
			}
		})
	}
}

func TestParseSyslog_YearRollover(t *testing.T) {
	jan := time.Date(2027, 1, 1, 0, 5, 0, 0, time.UTC)
	s, err := ParseSyslog("<13>Dec 31 23:59:00 host app: late", jan)
	if err != nil {
		t.Fatal(err)
	}
	if s.Timestamp.Year() != 2026 {
		t.Errorf("year = %d, want 2026", s.Timestamp.Year())
	}
}

func TestParseSyslog_Invalid(t *testing.T) {
	for _, raw := range []string{"no pri", "<>x", "<999>x", "<abc>x"} {
		if _, err := ParseSyslog(raw, now); err == nil {
			t.Errorf("expected error for %q", raw)
		}
	}
}

func TestSyslog_Level(t *testing.T) {
	want := map[int]models.LogLevel{
		0: models.LevelCritical, 2: models.LevelCritical, 3: models.LevelError,
		4: models.LevelWarning, 5: models.LevelInfo, 6: models.LevelInfo, 7: models.LevelDebug,
	}
	for sev, level := range want {
		if got := (&Syslog{Severity: sev}).Level(); got != level {
			t.Errorf("severity %d: got %s, want %s", sev, got, level)
		}
	}
}

func parseLine(t *testing.T, format, line string) *models.LogEntry {
	t.Helper()
	p, err := NewLineParser(format)
	if err != nil {
		t.Fatal(err)
	}
	e := models.NewLogEntry()
	e.Message = line
	e, ok := p.Process(e)
	if !ok {
		t.Fatal("parser dropped entry")
	}
	return e
}

func TestLineParser_JSON(t *testing.T) {
	e := parseLine(t, FormatAuto, `{"level":"error","msg":"db down","time":"2026-10-21T10:00:00Z","user":42}`)
	if e.Level != models.LevelError || e.Message != "db down" || e.Fields["user"] != float64(42) {
		t.Errorf("got %+v", e)
	}
	if !e.Timestamp.Equal(time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("timestamp = %v", e.Timestamp)
	}
	if _, ok := e.Fields["level"]; ok {
		t.Error("level key should be removed from fields")
	}

	e = parseLine(t, FormatAuto, `{"severity":"WARN","message":"x","ts":1792569600.5}`)
	if e.Level != models.LevelWarning || e.Timestamp.Unix() != 1792569600 {
		t.Errorf("got %+v", e)
	}
}

func TestLineParser_Logfmt(t *testing.T) {
	e := parseLine(t, FormatAuto, `time=2026-10-21T10:00:00Z level=warn msg="disk almost full" pct=91`)
	if e.Level != models.LevelWarning || e.Message != "disk almost full" || e.Fields["pct"] != "91" {
		t.Errorf("got %+v", e)
	}
}

func TestLineParser_Plain(t *testing.T) {
	tests := []struct {
		line  string
		level models.LogLevel
		ts    bool
	}{
		{"2026-10-21 10:00:00,123 ERROR [main] Connection refused", models.LevelError, true},
		{"[2026-10-21T10:00:00Z] [warn] slow request", models.LevelWarning, true},
		{"FATAL out of memory", models.LevelCritical, false},
		{"Error occurred in system", models.LevelError, false},
		{"No error found in config", models.LevelInfo, false},
		{"just a line", models.LevelInfo, false},
		{"a=b but not logfmt", models.LevelInfo, false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			e := parseLine(t, FormatAuto, tt.line)
			if e.Level != tt.level {
				t.Errorf("level = %s, want %s", e.Level, tt.level)
			}
			if e.Message != tt.line {
				t.Errorf("message = %q, want original line", e.Message)
			}
			if tt.ts && e.Timestamp.Year() != 2026 {
				t.Errorf("timestamp not parsed: %v", e.Timestamp)
			}
		})
	}
}

func TestLineParser_ForcedFormats(t *testing.T) {
	if e := parseLine(t, FormatNone, `{"level":"error"}`); e.Level != models.LevelInfo {
		t.Error("none format should not parse")
	}
	if e := parseLine(t, FormatJSON, "ERROR not json"); e.Level != models.LevelError {
		t.Error("json format should fall back to plain")
	}
	if e := parseLine(t, FormatLogfmt, "level=error"); e.Level != models.LevelError {
		t.Error("forced logfmt should accept a single pair")
	}
	if _, err := NewLineParser("xml"); err == nil {
		t.Error("expected error for unknown format")
	}
}

func TestSplitSyslog(t *testing.T) {
	stream := "25 <34>1 - host app - - - hi<13>plain line\n9 <13>no\nlf<13>last"
	sc := bufio.NewScanner(strings.NewReader(stream))
	sc.Split(SplitSyslog)
	var got []string
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	want := []string{"<34>1 - host app - - - hi", "<13>plain line", "<13>no\nlf", "<13>last"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIsContinuation(t *testing.T) {
	for _, l := range []string{"\tat com.Foo.bar(Foo.java:10)", "    File \"x.py\", line 1", "Caused by: java.io.IOException", "... 5 more"} {
		if !IsContinuation(l) {
			t.Errorf("%q should be a continuation", l)
		}
	}
	for _, l := range []string{"", "ERROR boom", "2026-10-21 next entry"} {
		if IsContinuation(l) {
			t.Errorf("%q should not be a continuation", l)
		}
	}
}
