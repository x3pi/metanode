package raftfeed

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
)

const (
	minForwardSecretBytes = 32
	defaultMaxBatchBytes  = 4 << 20
)

// effectiveRaftConfig copies rc, fills defaults and rejects anything unusable (SEQUENCER_SCHEMAS §1.1).
// A bad value is an error, never a guess.
func effectiveRaftConfig(rc *config.RaftConfig) (config.RaftConfig, error) {
	if rc == nil {
		return config.RaftConfig{}, errors.New("raft config is nil")
	}
	c := *rc
	c.Peers = append([]config.RaftPeer(nil), rc.Peers...)
	def := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	def(&c.HeartbeatTimeoutMs, 1000)
	def(&c.ElectionTimeoutMs, 1000)
	def(&c.LeaderLeaseTimeoutMs, 500)
	def(&c.CommitTimeoutMs, 50)
	def(&c.MaxBatchBytes, defaultMaxBatchBytes)
	def(&c.ProposeQueueSize, 1024)
	def(&c.SnapshotIntervalS, 120)
	def(&c.SnapshotThreshold, 8192)
	def(&c.TrailingLogs, 10240)
	if c.AdvertiseAddress == "" {
		c.AdvertiseAddress = c.BindAddress
	}

	if c.NodeID == "" || c.BindAddress == "" || c.DataDir == "" || c.ForwardBindAddress == "" || c.ForwardSecretFile == "" {
		return c, errors.New("raft: node_id, bind_address, data_dir, forward_bind_address and forward_secret_file are required")
	}
	if len(c.Peers) < 1 {
		return c, errors.New("raft: peers must list at least this node")
	}
	ids, addrs, self := map[string]bool{}, map[string]bool{}, false
	for _, p := range c.Peers {
		if p.ID == "" || p.Address == "" || (p.ForwardAddress == "" && c.ForwardPortOffset <= 0) {
			return c, fmt.Errorf("raft: peer %+v needs id, address and forward_address (or forward_port_offset)", p)
		}
		if ids[p.ID] || addrs[p.Address] {
			return c, fmt.Errorf("raft: duplicate peer id or address (%s / %s)", p.ID, p.Address)
		}
		ids[p.ID], addrs[p.Address] = true, true
		self = self || p.ID == c.NodeID
	}
	if !self {
		return c, fmt.Errorf("raft: node_id %q is not in peers", c.NodeID)
	}
	if c.Bootstrap && c.JoinExistingChain {
		return c, errors.New("raft: bootstrap and join_existing_chain are mutually exclusive")
	}
	if c.ForwardPortOffset < 0 {
		return c, errors.New("raft: forward_port_offset must be >= 0")
	}
	if c.ForwardPortOffset > 0 {
		adv, err := portOf(c.AdvertiseAddress)
		if err != nil {
			return c, fmt.Errorf("raft: advertise_address: %w", err)
		}
		fwd, err := portOf(c.ForwardBindAddress)
		if err != nil {
			return c, fmt.Errorf("raft: forward_bind_address: %w", err)
		}
		if fwd != adv+c.ForwardPortOffset {
			return c, fmt.Errorf("raft: forward_bind_address port %d must equal advertise port %d + forward_port_offset %d", fwd, adv, c.ForwardPortOffset)
		}
	}
	if c.ElectionTimeoutMs < c.HeartbeatTimeoutMs {
		return c, errors.New("raft: election_timeout_ms must be >= heartbeat_timeout_ms")
	}
	if c.LeaderLeaseTimeoutMs > c.HeartbeatTimeoutMs {
		return c, errors.New("raft: leader_lease_timeout_ms must be <= heartbeat_timeout_ms")
	}
	for name, v := range map[string]int{
		"heartbeat_timeout_ms": c.HeartbeatTimeoutMs, "leader_lease_timeout_ms": c.LeaderLeaseTimeoutMs,
		"commit_timeout_ms": c.CommitTimeoutMs, "max_batch_bytes": c.MaxBatchBytes, "propose_queue_size": c.ProposeQueueSize,
		"snapshot_interval_s": c.SnapshotIntervalS, "snapshot_threshold": c.SnapshotThreshold, "trailing_logs": c.TrailingLogs,
	} {
		if v <= 0 {
			return c, fmt.Errorf("raft: %s must be > 0", name)
		}
	}
	if !common.IsHexAddress(c.SequencerAddress) {
		return c, errors.New("raft: sequencer_address must be a 20-byte hex address")
	}
	return c, nil
}

// portOf returns the numeric port of a host:port string.
func portOf(hostport string) (int, error) {
	_, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// derivedForwardAddr is the internal endpoint of the member whose raft address is raftAddr when
// forward_port_offset is used.
func derivedForwardAddr(raftAddr string, offset int) (string, bool) {
	host, p, err := net.SplitHostPort(raftAddr)
	if err != nil {
		return "", false
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return "", false
	}
	return net.JoinHostPort(host, strconv.Itoa(n+offset)), true
}

// readForwardSecret reads the shared HMAC secret once (>= 32 bytes).
func readForwardSecret(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("raft: read forward_secret_file: %w", err)
	}
	if len(b) < minForwardSecretBytes {
		return nil, fmt.Errorf("raft: forward secret must be at least %d bytes, got %d", minForwardSecretBytes, len(b))
	}
	return b, nil
}
