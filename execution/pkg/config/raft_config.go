package config

// RaftPeer is one member of the Raft cluster.
type RaftPeer struct {
	ID      string `json:"id"`
	Address string `json:"address"` // Raft transport address host:port
	// ForwardAddress is the host:port of that node's internal follower->leader submit endpoint (SEQUENCER schema 1.5).
	ForwardAddress string `json:"forward_address"`
}

// RaftConfig configures consensus_mode="raft" as a replicated cluster (plan C2). Absent (nil) keeps the
// single-node feed of C1. Constraints are checked at startup by raftfeed.ValidateConfig; a bad value exits
// instead of guessing. Timeouts are used ONLY for leader election / heartbeats (the library's own, approved);
// they never decide whether a batch is dispatched.
type RaftConfig struct {
	NodeID           string     `json:"node_id"`
	BindAddress      string     `json:"bind_address"`
	AdvertiseAddress string     `json:"advertise_address,omitempty"` // defaults to BindAddress
	DataDir          string     `json:"data_dir"`
	Peers            []RaftPeer `json:"peers"`
	// Bootstrap must be true on exactly one node, once, when the cluster is first created; ignored when the
	// node already has Raft state.
	Bootstrap bool `json:"bootstrap,omitempty"`
	// JoinExistingChain lets a replica start with EMPTY Raft state although its block DB already holds blocks: a
	// replica added later, whose data directory was copied from a stopped peer (plan C4, `rollup-cluster
	// prepare-replica`). Log/snapshot replay then skips the blocks the DB already has. Never together with
	// Bootstrap. Without it such a start is refused (block numbers and log positions would not be aligned).
	JoinExistingChain bool `json:"join_existing_chain,omitempty"`
	// StateTransferAllowCopy lets this node serve state to a new replica on a filesystem WITHOUT reflink support
	// (ext4...). The atomic snapshot is then a full copy and execution stays paused for as long as it takes to
	// copy the database (seconds to minutes on a big one): off by default, the request is refused instead.
	StateTransferAllowCopy bool `json:"state_transfer_allow_copy,omitempty"`
	// ForwardPortOffset, when > 0, defines every member's internal endpoint as (raft host, raft port + offset), so
	// members added at runtime are reachable without editing the config of the others. Then peers[].forward_address
	// is optional and this node's forward_bind_address port must equal its advertise port + offset.
	ForwardPortOffset int `json:"forward_port_offset,omitempty"`

	HeartbeatTimeoutMs   int `json:"heartbeat_timeout_ms,omitempty"`    // default 1000
	ElectionTimeoutMs    int `json:"election_timeout_ms,omitempty"`     // default 1000
	LeaderLeaseTimeoutMs int `json:"leader_lease_timeout_ms,omitempty"` // default 500
	CommitTimeoutMs      int `json:"commit_timeout_ms,omitempty"`       // default 50
	MaxBatchBytes        int `json:"max_batch_bytes,omitempty"`         // default 4 MiB; Submit rejects bigger batches
	ProposeQueueSize     int `json:"propose_queue_size,omitempty"`      // default 1024 (bounded)
	SnapshotIntervalS    int `json:"snapshot_interval_s,omitempty"`     // default 120
	SnapshotThreshold    int `json:"snapshot_threshold,omitempty"`      // default 8192
	TrailingLogs         int `json:"trailing_logs,omitempty"`           // default 10240

	// ForwardBindAddress is where this node serves the internal submit endpoint (followers POST to the leader).
	ForwardBindAddress string `json:"forward_bind_address"`
	// ForwardSecretFile holds the shared HMAC secret (>= 32 bytes), read once.
	ForwardSecretFile string `json:"forward_secret_file"`
	// SequencerAddress is the fixed leader_address stamped in every block on every replica (so block hashes do not
	// depend on which node is the Raft leader). 20-byte hex.
	SequencerAddress string `json:"sequencer_address"`
}
