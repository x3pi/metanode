package executor

import (
	"bufio"
	"net"
	"sync"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// CommitteeNotifier sends committee change notifications to Rust Metanode
type CommitteeNotifier struct {
	socketPath string
	conn       net.Conn
	writer     *bufio.Writer
	mu         sync.Mutex
	connected  bool
}

var (
	globalNotifier     *CommitteeNotifier
	globalNotifierOnce sync.Once
)

// GetCommitteeNotifier returns the global committee notifier instance
// Uses /tmp/committee-notify.sock by default for Rust to listen on
func GetCommitteeNotifier() *CommitteeNotifier {
	globalNotifierOnce.Do(func() {
		globalNotifier = &CommitteeNotifier{
			socketPath: "/tmp/committee-notify.sock",
			connected:  false,
		}
	})
	return globalNotifier
}

// SetSocketPath configures the socket path (call before first use)
func (cn *CommitteeNotifier) SetSocketPath(path string) {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	cn.socketPath = path
}

// connect attempts to connect to the Rust notification listener
func (cn *CommitteeNotifier) connect() error {
	cn.mu.Lock()
	defer cn.mu.Unlock()

	if cn.connected && cn.conn != nil {
		return nil
	}

	conn, err := net.Dial("unix", cn.socketPath)
	if err != nil {
		return err
	}

	cn.conn = conn
	cn.writer = bufio.NewWriter(conn)
	cn.connected = true
	logger.Info("[COMMITTEE NOTIFIER] Connected to Rust listener at %s", cn.socketPath)
	return nil
}

// Disconnect closes the connection to Rust
func (cn *CommitteeNotifier) Disconnect() {
	cn.mu.Lock()
	defer cn.mu.Unlock()

	if cn.conn != nil {
		cn.conn.Close()
		cn.conn = nil
		cn.writer = nil
		cn.connected = false
		logger.Info("[COMMITTEE NOTIFIER] Disconnected from Rust listener")
	}
}

// NotifyCommitteeChange sends a committee change notification to Rust
// This is called after registerValidator or deregisterValidator succeeds
func (cn *CommitteeNotifier) NotifyCommitteeChange(blockNumber uint64, changeType string, validatorAddress string, newValidatorCount uint64) error {
	// Try to connect if not already connected
	if err := cn.connect(); err != nil {
		// Log but don't fail - Rust may not be running yet
		logger.Debug("[COMMITTEE NOTIFIER] Cannot connect to Rust (may not be running): %v", err)
		return nil // Don't fail the transaction
	}

	cn.mu.Lock()
	defer cn.mu.Unlock()

	if cn.writer == nil {
		return nil
	}

	notification := &pb.CommitteeChangedNotification{
		BlockNumber:       blockNumber,
		ChangeType:        changeType,
		ValidatorAddress:  validatorAddress,
		NewValidatorCount: newValidatorCount,
	}

	// Serialize and send
	if err := WriteMessage(cn.writer, notification); err != nil {
		logger.Error("[COMMITTEE NOTIFIER] Failed to send notification: %v", err)
		cn.connected = false
		cn.conn.Close()
		cn.conn = nil
		cn.writer = nil
		return err
	}

	if err := cn.writer.Flush(); err != nil {
		logger.Error("[COMMITTEE NOTIFIER] Failed to flush notification: %v", err)
		cn.connected = false
		cn.conn.Close()
		cn.conn = nil
		cn.writer = nil
		return err
	}

	logger.Info("[COMMITTEE NOTIFIER] 📢 Sent committee change notification: type=%s, validator=%s, block=%d, total=%d",
		changeType, validatorAddress, blockNumber, newValidatorCount)
	return nil
}

// NotifyValidatorRegistered is a convenience wrapper for registration events
func NotifyValidatorRegistered(blockNumber uint64, validatorAddress string, newValidatorCount uint64) {
	GetCommitteeNotifier().NotifyCommitteeChange(blockNumber, "REGISTER", validatorAddress, newValidatorCount)
}

// NotifyValidatorDeregistered is a convenience wrapper for deregistration events
func NotifyValidatorDeregistered(blockNumber uint64, validatorAddress string, newValidatorCount uint64) {
	GetCommitteeNotifier().NotifyCommitteeChange(blockNumber, "DEREGISTER", validatorAddress, newValidatorCount)
}

// NotifyEpochChange sends an epoch change notification to Rust
// This is used for event-driven epoch transitions (Go -> Rust)
func (cn *CommitteeNotifier) NotifyEpochChange(newEpoch uint64, timestampMs uint64, boundaryBlock uint64) error {
	// Try to connect if not already connected
	if err := cn.connect(); err != nil {
		// Log but don't fail - Rust may not be running yet
		logger.Debug("[EPOCH NOTIFIER] Cannot connect to Rust (may not be running): %v", err)
		return nil // Don't fail the transaction/block processing
	}

	cn.mu.Lock()
	defer cn.mu.Unlock()

	if cn.writer == nil {
		return nil
	}

	// Create the request wrapper
	req := &pb.Request{
		Payload: &pb.Request_NotifyEpochChangeRequest{
			NotifyEpochChangeRequest: &pb.NotifyEpochChangeRequest{
				NewEpoch:         newEpoch,
				EpochTimestampMs: timestampMs,
				BoundaryBlock:    boundaryBlock,
			},
		},
	}

	// Serialize and send
	if err := WriteMessage(cn.writer, req); err != nil {
		logger.Error("[EPOCH NOTIFIER] Failed to send notification: %v", err)
		cn.connected = false
		cn.conn.Close()
		cn.conn = nil
		cn.writer = nil
		return err
	}

	if err := cn.writer.Flush(); err != nil {
		logger.Error("[EPOCH NOTIFIER] Failed to flush notification: %v", err)
		cn.connected = false
		cn.conn.Close()
		cn.conn = nil
		cn.writer = nil
		return err
	}

	logger.Info("[EPOCH NOTIFIER] 📣 Sent epoch change notification: epoch=%d, time=%d, boundary=%d",
		newEpoch, timestampMs, boundaryBlock)
	return nil
}
