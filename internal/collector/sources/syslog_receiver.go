// internal/collector/sources/syslog_receiver.go
package sources

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/fatihserhatturan/logflux/pkg/models"
)

// SyslogReceiver receives syslog messages over UDP or TCP
type SyslogReceiver struct {
	addr     string
	protocol string // "udp" or "tcp"

	mu       sync.Mutex
	listener interface{} // net.PacketConn for UDP, net.Listener for TCP
	running  bool
	wg       sync.WaitGroup
}

// NewSyslogReceiver creates a new syslog receiver
func NewSyslogReceiver(addr string, protocol string) *SyslogReceiver {
	return &SyslogReceiver{
		addr:     addr,
		protocol: strings.ToLower(protocol),
	}
}

// Start begins listening for syslog messages
func (sr *SyslogReceiver) Start(ctx context.Context, out chan<- *models.LogEntry) error {
	sr.mu.Lock()
	if sr.running {
		sr.mu.Unlock()
		return fmt.Errorf("syslog receiver already running")
	}
	sr.running = true
	sr.mu.Unlock()

	switch sr.protocol {
	case "udp":
		return sr.startUDP(ctx, out)
	case "tcp":
		return sr.startTCP(ctx, out)
	default:
		return fmt.Errorf("unsupported protocol: %s", sr.protocol)
	}
}

// startUDP starts UDP listener
func (sr *SyslogReceiver) startUDP(ctx context.Context, out chan<- *models.LogEntry) error {
	addr, err := net.ResolveUDPAddr("udp", sr.addr)
	if err != nil {
		return fmt.Errorf("failed to resolve UDP address: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on UDP: %w", err)
	}

	sr.mu.Lock()
	sr.listener = conn
	sr.mu.Unlock()

	fmt.Printf("📡 Syslog receiver listening on UDP %s\n", sr.addr)

	sr.wg.Add(1)
	go sr.readUDP(ctx, conn, out)

	return nil
}

// readUDP reads from UDP connection
func (sr *SyslogReceiver) readUDP(ctx context.Context, conn *net.UDPConn, out chan<- *models.LogEntry) {
	defer sr.wg.Done()
	defer conn.Close()

	buffer := make([]byte, 4096)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Set read deadline to allow checking context
			conn.SetReadDeadline(time.Now().Add(1 * time.Second))

			n, _, err := conn.ReadFromUDP(buffer)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				// Log error but continue
				fmt.Printf("Error reading UDP: %v\n", err)
				continue
			}

			if n > 0 {
				message := string(buffer[:n])
				entry := sr.parseSyslogMessage(message)

				select {
				case out <- entry:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// startTCP starts TCP listener
func (sr *SyslogReceiver) startTCP(ctx context.Context, out chan<- *models.LogEntry) error {
	listener, err := net.Listen("tcp", sr.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on TCP: %w", err)
	}

	sr.mu.Lock()
	sr.listener = listener
	sr.mu.Unlock()

	fmt.Printf("📡 Syslog receiver listening on TCP %s\n", sr.addr)

	sr.wg.Add(1)
	go sr.acceptTCP(ctx, listener, out)

	return nil
}

// acceptTCP accepts TCP connections
func (sr *SyslogReceiver) acceptTCP(ctx context.Context, listener net.Listener, out chan<- *models.LogEntry) {
	defer sr.wg.Done()
	defer listener.Close()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Set accept deadline
			if tcpListener, ok := listener.(*net.TCPListener); ok {
				tcpListener.SetDeadline(time.Now().Add(1 * time.Second))
			}

			conn, err := listener.Accept()
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				// Log error but continue
				fmt.Printf("Error accepting connection: %v\n", err)
				continue
			}

			// Handle connection in separate goroutine
			sr.wg.Add(1)
			go sr.handleTCPConnection(ctx, conn, out)
		}
	}
}

// handleTCPConnection handles a single TCP connection
func (sr *SyslogReceiver) handleTCPConnection(ctx context.Context, conn net.Conn, out chan<- *models.LogEntry) {
	defer sr.wg.Done()
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 65536)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))

			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					fmt.Printf("Error scanning TCP: %v\n", err)
				}
				return
			}

			message := scanner.Text()
			if message == "" {
				continue
			}

			entry := sr.parseSyslogMessage(message)

			select {
			case out <- entry:
			case <-ctx.Done():
				return
			}
		}
	}
}

// parseSyslogMessage parses a basic syslog message
// Format: <priority>timestamp hostname tag: message
// For now, we'll do simple parsing. We'll improve this in the parser phase.
func (sr *SyslogReceiver) parseSyslogMessage(raw string) *models.LogEntry {
	entry := models.NewLogEntry()
	entry.Source = fmt.Sprintf("syslog:%s", sr.protocol)
	entry.Message = raw

	// Try to extract priority (RFC 3164)
	if strings.HasPrefix(raw, "<") {
		endIdx := strings.Index(raw, ">")
		if endIdx > 0 && endIdx < 10 {
			// Priority found, extract it
			entry.Fields["priority"] = raw[1:endIdx]
			raw = raw[endIdx+1:]
		}
	}

	// Store raw message for later parsing
	entry.Fields["raw"] = raw

	// Simple level detection based on keywords
	lowerMsg := strings.ToLower(raw)
	switch {
	case strings.Contains(lowerMsg, "crit") || strings.Contains(lowerMsg, "emerg") || strings.Contains(lowerMsg, "alert"):
		entry.Level = models.LevelCritical
	case strings.Contains(lowerMsg, "err") || strings.Contains(lowerMsg, "error"):
		entry.Level = models.LevelError
	case strings.Contains(lowerMsg, "warn"):
		entry.Level = models.LevelWarning
	case strings.Contains(lowerMsg, "debug"):
		entry.Level = models.LevelDebug
	default:
		entry.Level = models.LevelInfo
	}

	return entry
}

// Stop stops the receiver
func (sr *SyslogReceiver) Stop() error {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if !sr.running {
		return nil
	}

	sr.running = false

	// Close listener
	if sr.listener != nil {
		switch l := sr.listener.(type) {
		case *net.UDPConn:
			l.Close()
		case net.Listener:
			l.Close()
		}
	}

	// Wait for goroutines
	sr.wg.Wait()

	return nil
}

// Name returns the source name
func (sr *SyslogReceiver) Name() string {
	return fmt.Sprintf("syslog:%s@%s", sr.protocol, sr.addr)
}
