package processor

import (
	"encoding/hex"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

// RegConnEntry holds connection and client request ID for asynchronous push notifications.
type RegConnEntry struct {
	Conn      network.Connection
	MsgID     string
	CreatedAt time.Time
}

// RegistrationResultPayload is the JSON payload pushed to clients via TCP socket.
type RegistrationResultPayload struct {
	Status      string `json:"status"`
	Address     string `json:"address,omitempty"`
	HomeCluster string `json:"homeCluster,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// RegistrationTracker manages client connections waiting for L1 account registration confirmation.
// When Parent Chain (L1) confirms, rejects, or fails a registration, it pushes the outcome directly
// back to the open TCP connection of the client (similar to BroadCastReceipts).
type RegistrationTracker struct {
	entries sync.Map // map[common.Address]RegConnEntry
	stopCh  chan struct{}
	once    sync.Once
}

// NewRegistrationTracker initializes a new tracker and starts background TTL cleaner.
func NewRegistrationTracker() *RegistrationTracker {
	rt := &RegistrationTracker{
		stopCh: make(chan struct{}),
	}
	go rt.cleanupLoop()
	return rt
}

// Store records a client connection waiting for registration of address.
func (rt *RegistrationTracker) Store(address common.Address, entry RegConnEntry) {
	rt.entries.Store(address, entry)
}

// Get retrieves a client connection entry for address.
func (rt *RegistrationTracker) Get(address common.Address) (RegConnEntry, bool) {
	val, ok := rt.entries.Load(address)
	if !ok {
		return RegConnEntry{}, false
	}
	return val.(RegConnEntry), true
}

// Delete removes a client connection entry for address.
func (rt *RegistrationTracker) Delete(address common.Address) {
	rt.entries.Delete(address)
}

// PushResult sends an asynchronous push notification back to the client over its open TCP socket.
func (rt *RegistrationTracker) PushResult(address common.Address, result rollup.RegistrationResult) {
	val, ok := rt.entries.LoadAndDelete(address)
	if !ok {
		return
	}
	entry := val.(RegConnEntry)
	if entry.Conn == nil || !entry.Conn.IsConnect() {
		return
	}

	protoResult := &pb.RegistrationResult{
		Status:  string(result.Status),
		Address: address.Hex(),
		Reason:  result.Reason,
	}
	if result.HomeCluster != nil {
		protoResult.HomeCluster = "0x" + hex.EncodeToString(result.HomeCluster[:])
	}

	bodyBytes, err := proto.Marshal(protoResult)
	if err != nil {
		logger.Error("RegistrationTracker: failed to marshal proto result for %s: %v", address.Hex(), err)
		return
	}

	respMsg := p_network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: command.RegistrationResult,
			ID:      entry.MsgID,
		},
		Body: bodyBytes,
	})

	// Non-blocking push in a goroutine
	go func() {
		if err := entry.Conn.SendMessage(respMsg); err != nil {
			logger.Warn("RegistrationTracker: failed to push result to %s: %v", address.Hex(), err)
		} else {
			logger.Info("✅ [REGISTRATION PUSH] Pushed %s to %s (msgID=%s)", result.Status, address.Hex(), entry.MsgID)
		}
	}()
}

// cleanupLoop periodically evicts expired entries (TTL 15 minutes) to ensure bounded memory.
func (rt *RegistrationTracker) cleanupLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rt.stopCh:
			return
		case now := <-ticker.C:
			rt.entries.Range(func(key, value any) bool {
				entry, ok := value.(RegConnEntry)
				if ok && now.Sub(entry.CreatedAt) > 15*time.Minute {
					rt.entries.Delete(key)
				}
				return true
			})
		}
	}
}

// Stop cleanly terminates the background cleanup routine.
func (rt *RegistrationTracker) Stop() {
	rt.once.Do(func() {
		close(rt.stopCh)
	})
}
