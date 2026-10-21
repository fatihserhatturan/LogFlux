package parser

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// SplitSyslog is a bufio.SplitFunc for syslog over TCP (RFC 6587). It
// supports both octet counting ("27 <34>1 ...") and newline-delimited
// (non-transparent) framing, detected per message.
func SplitSyslog(data []byte, atEOF bool) (int, []byte, error) {
	if len(data) == 0 {
		return 0, nil, nil
	}
	if data[0] >= '1' && data[0] <= '9' {
		if sp := bytes.IndexByte(data, ' '); sp > 0 && sp <= 9 && isDigits(data[:sp]) {
			n, err := strconv.Atoi(string(data[:sp]))
			if err == nil {
				end := sp + 1 + n
				if len(data) >= end {
					return end, bytes.TrimRight(data[sp+1:end], "\r\n"), nil
				}
				if atEOF {
					return len(data), data[sp+1:], nil
				}
				return 0, nil, nil // need more data
			}
		}
	}
	return bufio.ScanLines(data, atEOF)
}

func isDigits(b []byte) bool {
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(b) > 0
}

// IsContinuation reports whether line continues the previous log entry,
// as in Java/Python/Go stack traces
func IsContinuation(line string) bool {
	if line == "" {
		return false
	}
	if line[0] == ' ' || line[0] == '\t' {
		return true
	}
	for _, prefix := range []string{"Caused by:", "Traceback (most recent call last)", "... ", "goroutine "} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}
