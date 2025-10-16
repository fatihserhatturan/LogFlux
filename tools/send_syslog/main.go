package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Println("Usage: send_syslog <udp|tcp> <address> <message>")
		fmt.Println("Example: send_syslog udp localhost:5140 \"<34>Test message\"")
		os.Exit(1)
	}

	protocol := os.Args[1]
	address := os.Args[2]
	message := os.Args[3]

	switch protocol {
	case "udp":
		sendUDP(address, message)
	case "tcp":
		sendTCP(address, message)
	default:
		fmt.Printf("Unknown protocol: %s\n", protocol)
		os.Exit(1)
	}
}

func sendUDP(address, message string) {
	conn, err := net.Dial("udp", address)
	if err != nil {
		fmt.Printf("❌ Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	_, err = conn.Write([]byte(message))
	if err != nil {
		fmt.Printf("❌ Error sending: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ UDP message sent to %s\n", address)
}

func sendTCP(address, message string) {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		fmt.Printf("❌ Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Add newline for TCP
	if len(message) > 0 && message[len(message)-1] != '\n' {
		message += "\n"
	}

	_, err = conn.Write([]byte(message))
	if err != nil {
		fmt.Printf("❌ Error sending: %v\n", err)
		os.Exit(1)
	}

	time.Sleep(100 * time.Millisecond)
	fmt.Printf("✅ TCP message sent to %s\n", address)
}
