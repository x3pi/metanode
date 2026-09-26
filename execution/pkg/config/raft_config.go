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
