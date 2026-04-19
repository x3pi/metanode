package executor

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// ============================================================================
//  Socket Abstraction Layer - Support both Unix and TCP sockets
// ============================================================================

// SocketConfig represents a socket address that can be either Unix or TCP
type SocketConfig struct {
	Address string // Can be "/tmp/socket.sock" or "tcp://192.168.1.100:9001" or "192.168.1.100:9001"
}

// NewSocketConfig creates a new SocketConfig from an address string
func NewSocketConfig(address string) *SocketConfig {
	return &SocketConfig{Address: address}
}

// IsUnix returns true if the socket is a Unix domain socket
// Unix sockets are identified by:
// - Starting with "/" (absolute path)
// - Starting with "unix://"
// - Not containing "tcp://" prefix
func (sc *SocketConfig) IsUnix() bool {
	addr := sc.Address
	if strings.HasPrefix(addr, "tcp://") {
		return false
	}
	if strings.HasPrefix(addr, "unix://") {
		return true
	}
	// If it starts with "/" it's a Unix socket path
	if strings.HasPrefix(addr, "/") {
		return true
	}
	// If it contains ":" and doesn't start with "/", assume it's TCP (host:port)
	if strings.Contains(addr, ":") {
		return false
	}
	// Default to Unix socket
	return true
}

// IsTCP returns true if the socket is a TCP socket
func (sc *SocketConfig) IsTCP() bool {
	return !sc.IsUnix()
}

// Network returns the network type ("unix" or "tcp")
func (sc *SocketConfig) Network() string {
	if sc.IsUnix() {
		return "unix"
	}
	return "tcp"
}

// ParsedAddress returns the address without protocol prefix
// Examples:
//   - "unix:///tmp/socket.sock" -> "/tmp/socket.sock"
//   - "tcp://192.168.1.100:9001" -> "192.168.1.100:9001"
//   - "/tmp/socket.sock" -> "/tmp/socket.sock"
//   - "192.168.1.100:9001" -> "192.168.1.100:9001"
func (sc *SocketConfig) ParsedAddress() string {
	addr := sc.Address

	// Strip "unix://" prefix
	if strings.HasPrefix(addr, "unix://") {
		return strings.TrimPrefix(addr, "unix://")
	}

	// Strip "tcp://" prefix
	if strings.HasPrefix(addr, "tcp://") {
		return strings.TrimPrefix(addr, "tcp://")
	}

	// Return as-is
	return addr
}

// Listen creates a network listener based on the socket type
// For Unix sockets: removes old socket file and creates a Unix listener
// For TCP sockets: creates a TCP listener
func (sc *SocketConfig) Listen() (net.Listener, error) {
	network := sc.Network()
	address := sc.ParsedAddress()

	if network == "unix" {
		// For Unix sockets, remove old socket file if it exists
		if _, err := os.Stat(address); err == nil {
			if err := os.Remove(address); err != nil {
				return nil, fmt.Errorf("cannot remove old Unix socket file %s: %w", address, err)
			}
		}
	}

	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, fmt.Errorf("cannot listen on %s socket %s: %w", network, address, err)
	}

	return listener, nil
}

// Dial creates a connection to the socket
// For Unix sockets: connects to Unix domain socket
// For TCP sockets: connects to TCP socket
func (sc *SocketConfig) Dial() (net.Conn, error) {
	network := sc.Network()
	address := sc.ParsedAddress()

	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to %s socket %s: %w", network, address, err)
	}

	return conn, nil
}

// String returns a readable representation of the socket config
func (sc *SocketConfig) String() string {
	if sc.IsUnix() {
		return fmt.Sprintf("unix://%s", sc.ParsedAddress())
	}
	return fmt.Sprintf("tcp://%s", sc.ParsedAddress())
}

// Cleanup removes the Unix socket file (only for Unix sockets)
// This is a no-op for TCP sockets
func (sc *SocketConfig) Cleanup() error {
	if sc.IsUnix() {
		address := sc.ParsedAddress()
		if err := os.Remove(address); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cannot remove Unix socket file %s: %w", address, err)
		}
	}
	return nil
}
