package network

import (
	"net"
	"testing"
)

// sendChanCap reads the capacity of the connection's outbound channel once the accept handshake has finished.
func sendChanCap(t *testing.T, cfg *Config) int {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	conn, err := ConnectionFromTcpConnection(serverConn, cfg)
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}
	defer conn.Disconnect()

	c := conn.(*Connection)
	c.sendChanMu.RLock()
	defer c.sendChanMu.RUnlock()
	if c.sendChan == nil {
		t.Fatal("sendChan was not created for an accepted connection")
	}
	return cap(c.sendChan)
}

// An accepted (client) connection must use ConnSendChanSize, not the 500K-slot default that costs ~7.6 MiB each.
func TestAcceptedConnectionUsesConnSendChanSize(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConnSendChanSize = 1234
	if got := sendChanCap(t, cfg); got != 1234 {
		t.Fatalf("accepted connection sendChan cap = %d, want 1234", got)
	}
}

// With ConnSendChanSize unset every other binary keeps the previous behaviour.
func TestAcceptedConnectionFallsBackToSendChanSize(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SendChanSize = 4321
	cfg.ConnSendChanSize = 0
	if got := sendChanCap(t, cfg); got != 4321 {
		t.Fatalf("accepted connection sendChan cap = %d, want fallback 4321", got)
	}
}
