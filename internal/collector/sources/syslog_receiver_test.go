// internal/collector/sources/syslog_receiver_test.go
package sources

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

func TestSyslogReceiver_UDP(t *testing.T) {
	// Create receiver on random port
	receiver := NewSyslogReceiver("127.0.0.1:0", "udp")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan *models.LogEntry, 10)

	// Start receiver
	if err := receiver.Start(ctx, out); err != nil {
		t.Fatal(err)
	}
	defer receiver.Stop()

	// Get actual address (since we used port 0)
	receiver.mu.Lock()
	actualAddr := receiver.listener.(*net.UDPConn).LocalAddr().String()
	receiver.mu.Unlock()

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	// Send test message
	conn, err := net.Dial("udp", actualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	testMsg := "<34>Oct 11 22:14:15 mymachine su: 'su root' failed for user on /dev/pts/8"
	if _, err := conn.Write([]byte(testMsg)); err != nil {
		t.Fatal(err)
	}

	// Read entry
	select {
	case entry := <-out:
		if want := "'su root' failed for user on /dev/pts/8"; entry.Message != want {
			t.Errorf("Expected message %q, got %q", want, entry.Message)
		}
		if entry.Fields["raw"] != testMsg {
			t.Errorf("Expected raw %q, got %q", testMsg, entry.Fields["raw"])
		}
		if entry.Fields["host"] != "mymachine" || entry.Fields["app"] != "su" {
			t.Errorf("Expected host/app parsed, got %v", entry.Fields)
		}
		if entry.Level != models.LevelCritical {
			t.Errorf("Expected CRITICAL from PRI 34 (severity 2), got %s", entry.Level)
		}
		if entry.Source != "syslog:udp" {
			t.Errorf("Expected source 'syslog:udp', got %q", entry.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for log entry")
	}
}

func TestSyslogReceiver_TCP(t *testing.T) {
	receiver := NewSyslogReceiver("127.0.0.1:0", "tcp")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan *models.LogEntry, 10)

	if err := receiver.Start(ctx, out); err != nil {
		t.Fatal(err)
	}
	defer receiver.Stop()

	// Get actual address
	receiver.mu.Lock()
	actualAddr := receiver.listener.(net.Listener).Addr().String()
	receiver.mu.Unlock()

	time.Sleep(100 * time.Millisecond)

	// Connect and send message
	conn, err := net.Dial("tcp", actualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	testMsg := "<34>Oct 11 22:14:15 mymachine su: 'su root' failed for user\n"
	if _, err := conn.Write([]byte(testMsg)); err != nil {
		t.Fatal(err)
	}

	// Read entry
	select {
	case entry := <-out:
		if entry.Source != "syslog:tcp" {
			t.Errorf("Expected source 'syslog:tcp', got %q", entry.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for log entry")
	}
}

func TestSyslogReceiver_MultipleMessages(t *testing.T) {
	receiver := NewSyslogReceiver("127.0.0.1:0", "udp")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan *models.LogEntry, 20)

	if err := receiver.Start(ctx, out); err != nil {
		t.Fatal(err)
	}
	defer receiver.Stop()

	receiver.mu.Lock()
	actualAddr := receiver.listener.(*net.UDPConn).LocalAddr().String()
	receiver.mu.Unlock()

	time.Sleep(100 * time.Millisecond)

	conn, err := net.Dial("udp", actualAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Send multiple messages
	numMessages := 10
	for i := 0; i < numMessages; i++ {
		msg := fmt.Sprintf("<34>Test message %d", i)
		conn.Write([]byte(msg))
		time.Sleep(10 * time.Millisecond)
	}

	// Collect entries
	var count int
	timeout := time.After(2 * time.Second)

	for count < numMessages {
		select {
		case <-out:
			count++
		case <-timeout:
			t.Fatalf("Only received %d/%d messages", count, numMessages)
		}
	}

	if count != numMessages {
		t.Errorf("Expected %d messages, got %d", numMessages, count)
	}
}

func TestSyslogReceiver_LevelDetection(t *testing.T) {
	tests := []struct {
		message       string
		expectedLevel models.LogLevel
	}{
		// Severity from PRI wins (PRI = facility*8 + severity)
		{"<11>Something happened", models.LevelError},    // severity 3
		{"<12>Something happened", models.LevelWarning},  // severity 4
		{"<10>Something happened", models.LevelCritical}, // severity 2
		{"<15>Something happened", models.LevelDebug},    // severity 7
		{"<14>Something happened", models.LevelInfo},     // severity 6
		// Without PRI, fall back to keyword detection
		{"Error occurred in system", models.LevelError},
		{"Warning: disk space low", models.LevelWarning},
		{"Critical system failure", models.LevelCritical},
		{"Debug information", models.LevelDebug},
		{"Normal operation", models.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			receiver := NewSyslogReceiver("127.0.0.1:0", "udp")
			entry := receiver.parseSyslogMessage(tt.message)

			if entry.Level != tt.expectedLevel {
				t.Errorf("Expected level %s, got %s", tt.expectedLevel, entry.Level)
			}
		})
	}
}

func TestSyslogReceiver_GracefulShutdown(t *testing.T) {
	receiver := NewSyslogReceiver("127.0.0.1:0", "tcp")

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan *models.LogEntry, 10)

	if err := receiver.Start(ctx, out); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	// Signal shutdown
	cancel()
	time.Sleep(100 * time.Millisecond)

	// Stop should complete quickly
	done := make(chan error, 1)
	go func() {
		done <- receiver.Stop()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not complete in time")
	}
}

func BenchmarkSyslogReceiver_UDP(b *testing.B) {
	receiver := NewSyslogReceiver("127.0.0.1:0", "udp")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan *models.LogEntry, 1000)

	if err := receiver.Start(ctx, out); err != nil {
		b.Fatal(err)
	}
	defer receiver.Stop()

	receiver.mu.Lock()
	actualAddr := receiver.listener.(*net.UDPConn).LocalAddr().String()
	receiver.mu.Unlock()

	time.Sleep(100 * time.Millisecond)

	conn, err := net.Dial("udp", actualAddr)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()

	// Drain channel
	go func() {
		for range out {
		}
	}()

	testMsg := []byte("<34>Oct 11 22:14:15 mymachine test message")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn.Write(testMsg)
		}
	})
}

func TestSyslogReceiver_TCPOctetCounting(t *testing.T) {
	receiver := NewSyslogReceiver("127.0.0.1:0", "tcp")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan *models.LogEntry, 10)
	if err := receiver.Start(ctx, out); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receiver.Stop() }()

	conn, err := net.Dial("tcp", receiver.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Two octet-counted messages without newlines, one containing a newline
	msg1 := "<11>1 2026-10-21T10:00:00Z host app 1 - - first"
	msg2 := "<14>1 2026-10-21T10:00:01Z host app 1 - - line1\nline2"
	if _, err := fmt.Fprintf(conn, "%d %s%d %s", len(msg1), msg1, len(msg2), msg2); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"first", "line1\nline2"} {
		select {
		case e := <-out:
			if e.Message != want {
				t.Errorf("got %q, want %q", e.Message, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout")
		}
	}
}
