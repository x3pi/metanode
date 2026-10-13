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

// Hard limits: the tracker lives in the ingress path of an unauthenticated TCP port, so it must be bounded no matter what
// clients send. (The relay itself separately caps the registrations it works on.)
const (
	maxTrackedRegistrations  = 8192 // distinct addresses with at least one waiting connection
	maxSubscribersPerAddress = 4    // connections that may wait for the same address
)

// RegistrationTracker manages client connections waiting for L1 account registration confirmation.
// When Parent Chain (L1) confirms, rejects, or fails a registration, it pushes the outcome directly
// back to the open TCP connection(s) of the client (similar to BroadCastReceipts).
//
// Entries are created ONLY after the relay accepted a signature-verified registration, never for arbitrary client input.
// Several connections may wait for one address, so one client can neither overwrite nor cancel another client's push.
type RegistrationTracker struct {
	mu      sync.Mutex
	entries map[common.Address][]RegConnEntry
	stopCh  chan struct{}
	once    sync.Once
}

// NewRegistrationTracker initializes a new tracker and starts background TTL cleaner.
func NewRegistrationTracker() *RegistrationTracker {
	rt := &RegistrationTracker{
		entries: make(map[common.Address][]RegConnEntry),
		stopCh:  make(chan struct{}),
	}
	go rt.cleanupLoop()
	return rt
}

// Store records a client connection waiting for registration of address. It returns false (and stores nothing) when the
// tracker or the per-address subscriber list is full. Storing the same connection again only refreshes its entry.
func (rt *RegistrationTracker) Store(address common.Address, entry RegConnEntry) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	list := rt.entries[address]
	for i := range list {
		if list[i].Conn == entry.Conn {
			list[i] = entry
			return true
		}
	}
	if len(list) == 0 && len(rt.entries) >= maxTrackedRegistrations {
		return false
	}
	// Drop subscribers whose connection is already gone before applying the per-address limit.
	live := list[:0]
	for _, e := range list {
		if e.Conn != nil && e.Conn.IsConnect() {
			live = append(live, e)
		}
	}
	if len(live) >= maxSubscribersPerAddress {
		rt.entries[address] = live
		return false
	}
	rt.entries[address] = append(live, entry)
	return true
}

// Get returns the oldest waiting connection entry for address.
func (rt *RegistrationTracker) Get(address common.Address) (RegConnEntry, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	list := rt.entries[address]
	if len(list) == 0 {
		return RegConnEntry{}, false
	}
	return list[0], true
}

// Len returns the number of addresses with at least one waiting connection.
func (rt *RegistrationTracker) Len() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.entries)
}

// Delete removes every waiting connection for address.
func (rt *RegistrationTracker) Delete(address common.Address) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.entries, address)
}

// Remove removes only the entry that belongs to conn, leaving other clients' entries for the same address untouched.
func (rt *RegistrationTracker) Remove(address common.Address, conn network.Connection) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	list := rt.entries[address]
	kept := list[:0]
	for _, e := range list {
		if e.Conn != conn {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		delete(rt.entries, address)
	} else {
		rt.entries[address] = kept
	}
}

// PushResult sends an asynchronous push notification back to the client over its open TCP socket.
func (rt *RegistrationTracker) PushResult(address common.Address, result rollup.RegistrationResult) {
	rt.mu.Lock()
	list := rt.entries[address]
	delete(rt.entries, address)
	rt.mu.Unlock()
	if len(list) == 0 {
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

	for _, entry := range list {
		if entry.Conn == nil || !entry.Conn.IsConnect() {
			continue
		}
		respMsg := p_network.NewMessage(&pb.Message{
			Header: &pb.Header{
				Command: command.RegistrationResult,
				ID:      entry.MsgID,
			},
			Body: bodyBytes,
		})
		// Non-blocking push in a goroutine; at most maxSubscribersPerAddress per address.
		go func(entry RegConnEntry, msg network.Message) {
			if err := entry.Conn.SendMessage(msg); err != nil {
				logger.Warn("RegistrationTracker: failed to push result to %s: %v", address.Hex(), err)
			} else {
				logger.Info("✅ [REGISTRATION PUSH] Pushed %s to %s (msgID=%s)", result.Status, address.Hex(), entry.MsgID)
			}
		}(entry, respMsg)
	}
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
			rt.mu.Lock()
			for addr, list := range rt.entries {
				kept := list[:0]
				for _, e := range list {
					if now.Sub(e.CreatedAt) <= 15*time.Minute {
						kept = append(kept, e)
					}
				}
				if len(kept) == 0 {
					delete(rt.entries, addr)
				} else {
					rt.entries[addr] = kept
				}
			}
			rt.mu.Unlock()
		}
	}
}

// Stop cleanly terminates the background cleanup routine.
func (rt *RegistrationTracker) Stop() {
	rt.once.Do(func() {
		close(rt.stopCh)
	})
}
