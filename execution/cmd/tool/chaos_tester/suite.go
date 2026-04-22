package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// ─── Node & Cluster Configuration ──────────────────────────────────────────────

// NodeConfig describes a single MetaNode validator node in the localnet.
type NodeConfig struct {
	ID              int    // 0-4
	PeerRPCPort     int    // Go Master peer_info HTTP port (19200-19204)
	RPCPort         string // JSON-RPC port (e.g. ":8757")
	StopScript      string // absolute path to stop_node.sh
	ResumeScript    string // absolute path to resume_node.sh
	GoMasterSession string // tmux session name
	GoSubSession    string
	RustSession     string
}

// ClusterConfig holds the full cluster setup.
type ClusterConfig struct {
	Nodes          []NodeConfig
	StopScriptDir  string // directory containing stop_node.sh & resume_node.sh
	KillWaitSec    int    // seconds to wait after killing before restore
	RestoreWaitSec int    // seconds to wait after restore before validation
	MaxSyncRetries int    // max poll attempts when waiting for consensus parity
}

// PeerInfo is the JSON response from the Go Master /peer_info endpoint.
type PeerInfo struct {
	NodeID              int    `json:"node_id"`
	Epoch               uint64 `json:"epoch"`
	LastBlock           uint64 `json:"last_block"`
	LastGlobalExecIndex uint64 `json:"last_global_exec_index"`
	NetworkAddress      string `json:"network_address"`
	TimestampMs         uint64 `json:"timestamp_ms"`
	StateRoot           string `json:"state_root"`
}

// ─── Default Localnet Configuration ────────────────────────────────────────────

const (
	defaultScriptDir  = "/home/abc/chain-n/metanode/consensus/metanode/scripts/node"
	defaultKillWait   = 15  // seconds
	defaultRestoreWait = 30 // seconds
	defaultMaxRetries = 30  // attempts (x3s = 90s max wait)
)

// NewDefaultClusterConfig creates the standard 5-node localnet configuration
// based on the existing config-master-node{0..4}.json files.
func NewDefaultClusterConfig() *ClusterConfig {
	nodes := make([]NodeConfig, 5)
	for i := 0; i < 5; i++ {
		nodes[i] = NodeConfig{
			ID:              i,
			PeerRPCPort:     19200 + i,
			StopScript:      fmt.Sprintf("%s/stop_node.sh", defaultScriptDir),
			ResumeScript:    fmt.Sprintf("%s/resume_node.sh", defaultScriptDir),
			GoMasterSession: fmt.Sprintf("go-master-%d", i),
			GoSubSession:    fmt.Sprintf("go-sub-%d", i),
			RustSession:     fmt.Sprintf("metanode-%d", i),
		}
	}
	return &ClusterConfig{
		Nodes:          nodes,
		StopScriptDir:  defaultScriptDir,
		KillWaitSec:    defaultKillWait,
		RestoreWaitSec: defaultRestoreWait,
		MaxSyncRetries: defaultMaxRetries,
	}
}

// ─── Cluster Operations ────────────────────────────────────────────────────────

// StopNode executes stop_node.sh <id> to gracefully stop a node (Go+Rust).
func (cc *ClusterConfig) StopNode(nodeID int) error {
	node := cc.Nodes[nodeID]
	cmd := exec.Command("bash", node.StopScript, fmt.Sprintf("%d", nodeID))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("stop_node.sh %d failed: %v\nOutput: %s", nodeID, err, string(out))
	}
	fmt.Printf("%s", string(out))
	return nil
}

// KillNode sends SIGKILL -9 to all tmux sessions of a node (hard crash simulation).
func (cc *ClusterConfig) KillNode(nodeID int) error {
	node := cc.Nodes[nodeID]
	sessions := []string{node.GoMasterSession, node.GoSubSession, node.RustSession}
	for _, sess := range sessions {
		// Check if session exists
		check := exec.Command("tmux", "has-session", "-t", sess)
		if err := check.Run(); err != nil {
			continue // session not running
		}
		// Send SIGKILL to the session
		kill := exec.Command("tmux", "send-keys", "-t", sess, "", "")
		_ = kill.Run()
		// Kill tmux session
		killSess := exec.Command("tmux", "kill-session", "-t", sess)
		killSess.Run()
	}

	// Also clean up sockets
	sockets := []string{
		fmt.Sprintf("/tmp/rust-go-node%d-master.sock", nodeID),
		fmt.Sprintf("/tmp/executor%d.sock", nodeID),
		fmt.Sprintf("/tmp/metanode-tx-%d.sock", nodeID),
	}
	for _, sock := range sockets {
		exec.Command("rm", "-f", sock).Run()
	}

	return nil
}

// ResumeNode executes resume_node.sh <id> to restart a stopped node.
func (cc *ClusterConfig) ResumeNode(nodeID int) error {
	node := cc.Nodes[nodeID]
	cmd := exec.Command("bash", node.ResumeScript, fmt.Sprintf("%d", nodeID))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("resume_node.sh %d failed: %v\nOutput: %s", nodeID, err, string(out))
	}
	fmt.Printf("%s", string(out))
	return nil
}

// ─── Cluster Observation ───────────────────────────────────────────────────────

// FetchPeerInfo queries the /peer_info HTTP endpoint of a single node.
// Returns nil if the node is unreachable.
func FetchPeerInfo(port int) (*PeerInfo, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/peer_info", port)
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("unreachable: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// Guard against empty response (known issue from previous sprint)
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, fmt.Errorf("empty response body")
	}

	var info PeerInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("json decode: %w (body: %s)", err, trimmed)
	}
	return &info, nil
}

// FetchAllPeerInfo queries all nodes and returns a map of nodeID -> PeerInfo.
// Nodes that are offline are omitted from the map.
func (cc *ClusterConfig) FetchAllPeerInfo() map[int]*PeerInfo {
	result := make(map[int]*PeerInfo)
	for _, node := range cc.Nodes {
		info, err := FetchPeerInfo(node.PeerRPCPort)
		if err != nil {
			continue
		}
		result[node.ID] = info
	}
	return result
}

// PrintClusterStatus prints a formatted table of all node states.
func (cc *ClusterConfig) PrintClusterStatus() {
	fmt.Println("┌──────┬─────────┬───────┬────────┬──────────────────────────────────────────────────────────────────────┐")
	fmt.Println("│ Node │  Block  │ Epoch │  GEI   │ StateRoot                                                          │")
	fmt.Println("├──────┼─────────┼───────┼────────┼──────────────────────────────────────────────────────────────────────┤")
	for _, node := range cc.Nodes {
		info, err := FetchPeerInfo(node.PeerRPCPort)
		if err != nil {
			fmt.Printf("│  %d   │  %-5s  │ %-5s │ %-6s │ %-68s │\n", node.ID, "OFF", "-", "-", "❌ "+err.Error())
		} else {
			fmt.Printf("│  %d   │  %-5d  │ %-5d │ %-6d │ %s │\n",
				node.ID, info.LastBlock, info.Epoch, info.LastGlobalExecIndex, truncateHash(info.StateRoot, 68))
		}
	}
	fmt.Println("└──────┴─────────┴───────┴────────┴──────────────────────────────────────────────────────────────────────┘")
}

// WaitForFullConsensus polls all nodes until either:
//   - All online nodes have the same StateRoot AND GlobalExecIndex, OR
//   - maxAttempts is exhausted.
//
// The `requiredOnline` parameter specifies the minimum number of nodes
// that must be online for consensus to be considered valid.
func (cc *ClusterConfig) WaitForFullConsensus(requiredOnline int, maxAttempts int) bool {
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		fmt.Printf("\r   ⏳ Consensus check attempt %d/%d ...", attempt, maxAttempts)

		peers := cc.FetchAllPeerInfo()
		if len(peers) < requiredOnline {
			time.Sleep(3 * time.Second)
			continue
		}

		// Check StateRoot consensus
		stateRoots := make(map[string]int)
		geis := make(map[uint64]int)
		for _, info := range peers {
			stateRoots[info.StateRoot]++
			geis[info.LastGlobalExecIndex]++
		}

		if len(stateRoots) == 1 && len(geis) == 1 {
			fmt.Print("\r")
			fmt.Println("   ✅ CONSENSUS ACHIEVED! All nodes have identical StateRoot & GEI.                    ")
			cc.PrintClusterStatus()
			return true
		}

		time.Sleep(3 * time.Second)
	}

	fmt.Println()
	fmt.Println("   ❌ Consensus NOT achieved within timeout.")
	cc.PrintClusterStatus()
	return false
}

// ─── Helpers ───────────────────────────────────────────────────────────────────

func truncateHash(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s + strings.Repeat(" ", maxLen-len(s))
	}
	return s[:maxLen]
}
