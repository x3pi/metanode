package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorPurple = "\033[35m"
	colorCyan   = "\033[36m"
	colorWhite  = "\033[37m"
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"
)

// Config represents cluster nodes configuration
type Config struct {
	Nodes    map[string]string `json:"nodes"`
	Roles    map[string]string `json:"roles"`
	TCPNodes map[string]string `json:"tcp_nodes"`
}

// JSON-RPC structs
type rpcRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcResponse struct {
	Jsonrpc string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
	ID      int             `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type blockHeaderResult struct {
	Number          string `json:"number"`
	Hash            string `json:"hash"`
	LeaderAddress   string `json:"leaderAddress"`
	Miner           string `json:"miner"`
	Epoch           string `json:"epoch"`
	CommitIndex     string `json:"commitIndex"`
	GlobalExecIndex string `json:"globalExecIndex"`
	Timestamp       string `json:"timestamp"`
	StateRoot       string `json:"stateRoot"`
}

// NodeState tracks current status of each node
type NodeState struct {
	Name            string
	URL             string
	Role            string // "validator" only (synconly filtered out)
	Address         string // ETH / Validator address (e.g. 0x87ba...)
	CurrentHeight   uint64
	CurrentCommit   uint32
	QuorumCommit    uint32
	ConsensusEpoch  uint64
	HasConsensusVotes bool
	CurrentHash     string
	LastResponded   time.Time
	LastVotedTime   time.Time
	LastVotedHeight uint64
	Latency         time.Duration
	IsAlive         bool
	IsIgnored       bool
	ErrorMessage    string
	ConsecutiveMiss int
	TotalMissed         uint64
	MissAlertSent       bool
	LastMissLoggedBlock uint64
}

// ConsensusVoteSnapshot models Rust CommitVoteMonitor vote snapshot directly from BFT consensus
type ConsensusVoteSnapshot struct {
	Epoch             uint64              `json:"epoch"`
	QuorumCommitIndex uint32              `json:"quorum_commit_index"`
	HighestSeenEpoch  uint64              `json:"highest_seen_epoch"`
	Authorities       []AuthorityVoteInfo `json:"authorities"`
	RecentCommits     []CommitVoteDetails `json:"recent_commits"`
}

// AuthorityVoteInfo models per-validator highest voted commit in consensus
type AuthorityVoteInfo struct {
	AuthorityIndex     int    `json:"authority_index"`
	HighestVotedCommit uint32 `json:"highest_voted_commit"`
	VoteCount          int    `json:"vote_count"`
}

// CommitVoteDetails models commit-level voting breakdown with exact voters and missing nodes
type CommitVoteDetails struct {
	CommitIndex     uint32   `json:"commit_index"`
	QuorumDigest    string   `json:"quorum_digest"`
	QuorumReached   bool     `json:"quorum_reached"`
	TotalStake      uint64   `json:"total_stake"`
	QuorumThreshold uint64   `json:"quorum_threshold"`
	Voters          []string `json:"voters"`
	MissingVoters   []string `json:"missing_voters"`
}

// BlockVoteRecord tracks voting status of each individual block sequentially
type BlockVoteRecord struct {
	BlockNumber    uint64
	FirstSeen      time.Time
	Leader         string
	LeaderAddr     string
	QuorumVoters   []string
	InitialMissing []string
	CatchUpElapsed map[string]time.Duration
	NodeVoted      map[string]bool
	NodeVotedTime  map[string]time.Time
	NodeAlertSent  map[string]bool
	IsCompleted    bool
}

// AnomalyTracker tracks cluster-wide anomalies to avoid alert spam
type AnomalyTracker struct {
	ChainStalledAlertSent bool
	LastChainAdvance      time.Time
	QuorumAlertSent       bool
	ForkAlertSent         map[uint64]bool
	InForkState           bool
	LastForkAlertTime     time.Time
	LastForkNodes         string
	mu                    sync.Mutex
}

var (
	configFile        string
	pollInterval      time.Duration
	maxMisses         int
	stallTimeout      time.Duration
	chainStallTimeout time.Duration
	ignoreNodesFlag   string
	daemonMode        bool
	watchMode         bool
	noAlert           bool
	logFilePath       string
	botTokenOverride  string
	chatIDOverride    string
	noStopFlag        bool

	telegramBotToken string
	telegramChatID   string

	logFile *os.File
	logMu   sync.Mutex

	// Validator Address to Node Name map (e.g. "0x87ba..." -> "m0")
	valAddrToName = make(map[string]string)
	valNameToAddr = make(map[string]string)

	tracker = &AnomalyTracker{
		LastChainAdvance: time.Now(),
		ForkAlertSent:    make(map[uint64]bool),
	}

	// Sequential block tracking state
	lastEnqueuedBlock uint64
	seqStartHeight    uint64
	isInitialStartup  = true
	trackedBlocks     []*BlockVoteRecord
)

func init() {
	flag.StringVar(&configFile, "config", "", "Path to config file (prioritizes /tmp/rpc_nodes.json)")
	flag.DurationVar(&pollInterval, "interval", 2*time.Second, "Polling interval (e.g. 1s, 2s, 5s)")
	flag.IntVar(&maxMisses, "max-misses", 5, "Consecutive missed blocks to trigger alert")
	flag.DurationVar(&stallTimeout, "stall-timeout", 10*time.Minute, "Duration without voting a block to trigger alert (e.g. 10m)")
	flag.DurationVar(&chainStallTimeout, "chain-stall-timeout", 10*time.Minute, "Duration without new block to trigger chain stall alert (e.g. 10m)")
	flag.StringVar(&ignoreNodesFlag, "ignore-nodes", "", "Nodes to temporarily ignore (e.g. 'm3' or '3' or 'm2,m3')")
	flag.StringVar(&ignoreNodesFlag, "stopped-nodes", "", "Alias for --ignore-nodes")
	flag.BoolVar(&daemonMode, "daemon", false, "Run in daemon mode (append to log, no interactive dashboard)")
	flag.BoolVar(&watchMode, "watch", true, "Interactive live dashboard mode")
	flag.BoolVar(&noAlert, "no-alert", false, "Disable Telegram alerts")
	flag.BoolVar(&noStopFlag, "no-stop-flag", false, "Disable writing /tmp/MTN_CHAIN_ERROR_STOP and os.Exit on fork")
	flag.StringVar(&logFilePath, "log", "vote_monitor.log", "Log file path")
	flag.StringVar(&botTokenOverride, "bot-token", "", "Telegram bot token override")
	flag.StringVar(&chatIDOverride, "chat-id", "", "Telegram chat ID override")
}

func logMessage(format string, args ...interface{}) {
	now := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s\n", now, msg)

	logMu.Lock()
	defer logMu.Unlock()

	if logFile != nil {
		logFile.WriteString(line)
		logFile.Sync()
	}
	if daemonMode || !watchMode {
		fmt.Print(line)
	}
}

// loadTelegramConfig detects Telegram bot token and chat ID from multiple sources
func loadTelegramConfig() {
	if botTokenOverride != "" {
		telegramBotToken = botTokenOverride
	}
	if chatIDOverride != "" {
		telegramChatID = chatIDOverride
	}
	if telegramBotToken != "" && telegramChatID != "" {
		return
	}

	// 1. Env vars
	if telegramBotToken == "" {
		telegramBotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	if telegramChatID == "" {
		telegramChatID = os.Getenv("TELEGRAM_CHAT_ID")
	}
	if telegramBotToken != "" && telegramChatID != "" {
		return
	}

	// 2. .env files
	envCandidates := []string{
		".env",
		"../.env",
		"../../.env",
		"../../../.env",
		"/opt/metanode/monitors/.env",
		"/opt/metanode/.env",
	}
	for _, p := range envCandidates {
		data, err := os.ReadFile(p)
		if err == nil {
			lines := strings.Split(string(data), "\n")
			for _, l := range lines {
				l = strings.TrimSpace(l)
				if l == "" || strings.HasPrefix(l, "#") {
					continue
				}
				parts := strings.SplitN(l, "=", 2)
				if len(parts) == 2 {
					k := strings.TrimSpace(parts[0])
					v := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
					if k == "TELEGRAM_BOT_TOKEN" && telegramBotToken == "" {
						telegramBotToken = v
					}
					if k == "TELEGRAM_CHAT_ID" && telegramChatID == "" {
						telegramChatID = v
					}
				}
			}
		}
		if telegramBotToken != "" && telegramChatID != "" {
			return
		}
	}

	// 3. YAML configs (inventory.yml, ci_config.yaml)
	yamlCandidates := []string{
		"../../inventory.yml",
		"../inventory.yml",
		"inventory.yml",
		"/opt/metanode/deploy/ansible/inventory.yml",
		"../../ci/ci_config.yaml",
		"../../../deploy/ci/ci_config.yaml",
		"/opt/metanode/deploy/ci/ci_config.yaml",
	}
	for _, yp := range yamlCandidates {
		data, err := os.ReadFile(yp)
		if err == nil {
			lines := strings.Split(string(data), "\n")
			for _, l := range lines {
				l = strings.TrimSpace(l)
				if strings.HasPrefix(l, "bot_token:") && telegramBotToken == "" {
					parts := strings.SplitN(l, ":", 2)
					if len(parts) == 2 {
						telegramBotToken = strings.Trim(strings.TrimSpace(parts[1]), `"' `)
					}
				}
				if strings.HasPrefix(l, "chat_id:") && telegramChatID == "" {
					parts := strings.SplitN(l, ":", 2)
					if len(parts) == 2 {
						telegramChatID = strings.Trim(strings.TrimSpace(parts[1]), `"' `)
					}
				}
			}
		}
		if telegramBotToken != "" && telegramChatID != "" {
			return
		}
	}
}

func findConfigFile() string {
	if configFile != "" {
		return configFile
	}

	candidates := []string{
		"/tmp/rpc_nodes.json",
		"config-m-nodes.json",
		"../block_hash_checker/config-m-nodes.json",
		"/opt/metanode/monitors/block_hash_checker/config-m-nodes.json",
		"../../block_hash_checker/config-m-nodes.json",
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "/tmp/rpc_nodes.json"
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file %s error: %w", path, err)
	}

	var raw struct {
		Nodes    map[string]string `json:"nodes"`
		Roles    map[string]string `json:"roles"`
		TCPNodes map[string]string `json:"tcp_nodes"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse json error: %w", err)
	}

	if len(raw.Nodes) == 0 {
		return nil, fmt.Errorf("no nodes defined in config")
	}

	cfg := &Config{
		Nodes:    make(map[string]string),
		Roles:    make(map[string]string),
		TCPNodes: make(map[string]string),
	}

	// Filter: ONLY include validators, completely exclude synconly nodes
	for name, url := range raw.Nodes {
		role := strings.ToLower(strings.TrimSpace(raw.Roles[name]))
		if role == "synconly" || role == "sync_only" || role == "sync" {
			// Synconly nodes do not vote in BFT consensus — exclude
			continue
		}
		cfg.Nodes[name] = url
		cfg.Roles[name] = "validator"
	}

	for name, tcp := range raw.TCPNodes {
		cfg.TCPNodes[name] = tcp
	}

	if len(cfg.Nodes) == 0 {
		return nil, fmt.Errorf("no validator nodes found in config (all were synconly)")
	}

	return cfg, nil
}

// loadValidatorAddresses parses genesis.json to map addresses to node names (m0, m1, ...)
func loadValidatorAddresses(cfg *Config) {
	candidates := []string{
		"/opt/metanode/node-0/config/genesis.json",
		"/opt/metanode/node-1/config/genesis.json",
		"/opt/metanode/chain-101/node-0/config/genesis.json",
	}

	// Find readable genesis.json
	var genesisData []byte
	for _, p := range candidates {
		d, err := os.ReadFile(p)
		if err == nil {
			genesisData = d
			break
		}
	}

	if len(genesisData) == 0 {
		// Fallback to searching matching glob
		matches, _ := filepath.Glob("/opt/metanode/*/config/genesis.json")
		for _, m := range matches {
			d, err := os.ReadFile(m)
			if err == nil {
				genesisData = d
				break
			}
		}
	}

	if len(genesisData) > 0 {
		var gen struct {
			Validators []struct {
				Address        string `json:"address"`
				Hostname       string `json:"hostname"`
				PrimaryAddress string `json:"primary_address"`
			} `json:"validators"`
		}

		if err := json.Unmarshal(genesisData, &gen); err == nil {
			for _, v := range gen.Validators {
				addr := strings.ToLower(strings.TrimSpace(v.Address))
				if addr == "" {
					continue
				}

				// Try matching by hostname ("node-0" -> "m0")
				matchedName := ""
				if strings.HasPrefix(v.Hostname, "node-") {
					idx := strings.TrimPrefix(v.Hostname, "node-")
					matchedName = "m" + idx
				}

				// If not matched, try matching by TCP primary port
				if matchedName == "" && v.PrimaryAddress != "" {
					for nodeName, tcp := range cfg.TCPNodes {
						if strings.HasSuffix(tcp, v.PrimaryAddress) || strings.HasSuffix(v.PrimaryAddress, tcp) {
							matchedName = nodeName
							break
						}
					}
				}

				if matchedName != "" {
					valAddrToName[addr] = matchedName
					valNameToAddr[matchedName] = addr
				}
			}
		}
	}

	// Hardcoded fallback for default 4-node cluster on 192.168.1.234 if genesis was unreadable
	if len(valAddrToName) == 0 {
		valAddrToName["0x87ba736b46bd8f56e6bffb79ac15f4a82752db69"] = "m0"
		valAddrToName["0xd368bab8690eaeb2659b660b9553560a4076272e"] = "m1"
		valAddrToName["0x47647d410714e52e503394729f13257775c3b378"] = "m2"
		valAddrToName["0x3367670f06000c388589575c04a15f76b20bd1e1"] = "m3"

		for addr, name := range valAddrToName {
			valNameToAddr[name] = addr
		}
	}
}

// resolveLeader resolves leader node name (e.g. "m0") from hex address
func resolveLeader(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return "Unknown"
	}
	if name := valAddrToName[addr]; name != "" {
		return name
	}
	if len(addr) > 10 {
		return addr[:8] + "..."
	}
	return addr
}

// getIgnoredNodes dynamically merges CLI flag and test trigger files (/tmp/monitors_ignore_nodes)
func getIgnoredNodes() map[string]bool {
	ignored := make(map[string]bool)

	// 1. From CLI flag (--ignore-nodes m3 or --ignore-nodes 3,m2)
	if ignoreNodesFlag != "" {
		for _, part := range strings.Split(ignoreNodesFlag, ",") {
			p := strings.TrimSpace(strings.ToLower(part))
			if p != "" {
				clean := strings.TrimPrefix(strings.TrimPrefix(p, "node"), "m")
				ignored[p] = true
				ignored["m"+clean] = true
				ignored[clean] = true
			}
		}
	}

	// 2. From /tmp/monitors_ignore_nodes (used by run_restart_test.sh & run_snapshot_test.sh)
	checkFiles := []string{
		"/tmp/monitors_ignore_nodes",
		"/tmp/metanode_stopped_nodes",
	}

	for _, f := range checkFiles {
		data, err := os.ReadFile(f)
		if err == nil {
			fields := strings.Fields(string(data))
			for _, fld := range fields {
				p := strings.TrimSpace(strings.ToLower(fld))
				if p != "" {
					clean := strings.TrimPrefix(strings.TrimPrefix(p, "node"), "m")
					ignored[p] = true
					ignored["m"+clean] = true
					ignored[clean] = true
				}
			}
		}
	}

	return ignored
}

func sendTelegramAlert(title string, message string, isRecovery bool) {
	if noAlert || telegramBotToken == "" || telegramChatID == "" {
		return
	}

	var header string
	if isRecovery {
		header = fmt.Sprintf("🟢 *[%s - RECOVERED]*", title)
	} else {
		header = fmt.Sprintf("🚨 *[%s]*", title)
	}

	fullMsg := fmt.Sprintf("%s\n\n%s\n\n🕒 _Time: %s_", header, message, time.Now().Format("2006-01-02 15:04:05"))
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", telegramBotToken)
	payload := map[string]string{
		"chat_id":    telegramChatID,
		"text":       fullMsg,
		"parse_mode": "Markdown",
	}

	body, _ := json.Marshal(payload)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(apiURL, "application/json", bytes.NewReader(body))
	if err != nil {
		logMessage("⚠️ Failed to send Telegram alert: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logMessage("⚠️ Telegram alert returned non-200 code: %d", resp.StatusCode)
	}
}

// queryRPC executes a JSON-RPC call
func queryRPC(client *http.Client, rpcURL string, method string, params []interface{}) (json.RawMessage, error) {
	reqBody := rpcRequest{
		Jsonrpc: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", rpcURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal error: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

// getConsensusVotes queries real-time BFT consensus votes directly from the Rust consensus engine.
func getConsensusVotes(client *http.Client, rpcURL string) (*ConsensusVoteSnapshot, time.Duration, error) {
	start := time.Now()
	res, err := queryRPC(client, rpcURL, "mtn_getConsensusVotes", []interface{}{})
	latency := time.Since(start)
	if err != nil {
		return nil, latency, err
	}
	if string(res) == "null" || len(res) == 0 {
		return nil, latency, fmt.Errorf("empty consensus votes response")
	}

	var snap ConsensusVoteSnapshot
	if err := json.Unmarshal(res, &snap); err != nil {
		return nil, latency, fmt.Errorf("failed to parse consensus vote snapshot: %w", err)
	}
	return &snap, latency, nil
}

// getBlockNumber returns current block number from RPC
func getBlockNumber(client *http.Client, rpcURL string) (uint64, time.Duration, error) {
	start := time.Now()
	res, err := queryRPC(client, rpcURL, "eth_blockNumber", []interface{}{})
	latency := time.Since(start)
	if err != nil {
		return 0, latency, err
	}

	var hexStr string
	if err := json.Unmarshal(res, &hexStr); err != nil {
		return 0, latency, err
	}

	cleanHex := strings.TrimPrefix(hexStr, "0x")
	num, err := strconv.ParseUint(cleanHex, 16, 64)
	if err != nil {
		return 0, latency, fmt.Errorf("parse uint error: %w", err)
	}

	return num, latency, nil
}

// getBlockHeader fetches block header details
func getBlockHeader(client *http.Client, rpcURL string, blockNum uint64) (*blockHeaderResult, error) {
	hexNum := fmt.Sprintf("0x%x", blockNum)
	res, err := queryRPC(client, rpcURL, "eth_getBlockByNumber", []interface{}{hexNum, false})
	if err != nil {
		return nil, err
	}

	if string(res) == "null" || len(res) == 0 {
		return nil, fmt.Errorf("block %d returned null", blockNum)
	}

	var hdr blockHeaderResult
	if err := json.Unmarshal(res, &hdr); err != nil {
		return nil, err
	}

	return &hdr, nil
}

func extractNodeIndex(name string) int {
	digits := ""
	for _, ch := range name {
		if ch >= '0' && ch <= '9' {
			digits += string(ch)
		}
	}
	if digits == "" {
		return 0
	}
	id, _ := strconv.Atoi(digits)
	return id
}

// visualWidth calculates visible terminal columns of a string (ignoring ANSI escape sequences)
func visualWidth(s string) int {
	w := 0
	inEscape := false
	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		// Wide characters / emojis take 2 terminal columns
		if r > 0x1F000 || (r >= 0x2600 && r <= 0x27BF) {
			w += 2
		} else {
			w += 1
		}
	}
	return w
}

func padRight(s string, targetWidth int) string {
	vw := visualWidth(s)
	if vw < targetWidth {
		return s + strings.Repeat(" ", targetWidth-vw)
	}
	return s
}

// snapshotTracker tracks cluster snapshot state to avoid logging identical tables repeatedly
type snapshotTracker struct {
	netHeight      uint64
	nodeHeights    map[string]uint64
	nodeAlive      map[string]bool
	nodeIgnored    map[string]bool
	pendingCount   int
	completedCount int
	anomalyCount   int
	lastLogged     time.Time
}

var lastSnapshotState *snapshotTracker

func shouldLogSnapshot(currentNetHeight uint64, states []*NodeState, pendingCount int, completedCount int, anomalyCount int) bool {
	if lastSnapshotState == nil {
		return true
	}
	if currentNetHeight != lastSnapshotState.netHeight {
		return true
	}
	if pendingCount != lastSnapshotState.pendingCount {
		return true
	}
	if completedCount != lastSnapshotState.completedCount {
		return true
	}
	if anomalyCount != lastSnapshotState.anomalyCount {
		return true
	}
	for _, s := range states {
		if s.CurrentHeight != lastSnapshotState.nodeHeights[s.Name] {
			return true
		}
		if s.IsAlive != lastSnapshotState.nodeAlive[s.Name] {
			return true
		}
		if s.IsIgnored != lastSnapshotState.nodeIgnored[s.Name] {
			return true
		}
	}
	// Heartbeat: log snapshot at least once every 30s during idle/stalled periods
	if time.Since(lastSnapshotState.lastLogged) >= 30*time.Second {
		return true
	}
	return false
}

func recordSnapshotState(currentNetHeight uint64, states []*NodeState, pendingCount int, completedCount int, anomalyCount int) {
	nh := make(map[string]uint64, len(states))
	na := make(map[string]bool, len(states))
	ni := make(map[string]bool, len(states))
	for _, s := range states {
		nh[s.Name] = s.CurrentHeight
		na[s.Name] = s.IsAlive
		ni[s.Name] = s.IsIgnored
	}
	lastSnapshotState = &snapshotTracker{
		netHeight:      currentNetHeight,
		nodeHeights:    nh,
		nodeAlive:      na,
		nodeIgnored:    ni,
		pendingCount:   pendingCount,
		completedCount: completedCount,
		anomalyCount:   anomalyCount,
		lastLogged:     time.Now(),
	}
}

// formatShortDuration formats duration cleanly for compact tables (e.g. 12s, 5m9s)
func formatShortDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm%ds", m, s)
}

// getRecentBlocks returns the last N blocks in newest-first order
func getRecentBlocks(tracked []*BlockVoteRecord, count int) []*BlockVoteRecord {
	if len(tracked) == 0 {
		return nil
	}
	var res []*BlockVoteRecord
	start := len(tracked) - count
	if start < 0 {
		start = 0
	}
	for i := len(tracked) - 1; i >= start; i-- {
		res = append(res, tracked[i])
	}
	return res
}

// formatConsensusAuditTable formats real BFT consensus vote audit from Rust CommitVoteMonitor
func formatConsensusAuditTable(recentCommits []CommitVoteDetails, states []*NodeState, useColor bool) string {
	if len(recentCommits) == 0 {
		return ""
	}

	var cReset, cBold, cGreen, cYellow, cCyan, cRed, cDim string
	if useColor {
		cReset = colorReset
		cBold = colorBold
		cGreen = colorGreen
		cYellow = colorYellow
		cCyan = colorCyan
		cRed = colorRed
		cDim = colorDim
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s%s┌─────────────────────────────────────────────────────────────────────────────┐%s\n", cBold, cCyan, cReset))
	sb.WriteString(fmt.Sprintf("%s%s│                 REAL BFT CONSENSUS COMMIT VOTE AUDIT (RUST)                 │%s\n", cBold, cCyan, cReset))
	sb.WriteString(fmt.Sprintf("%s%s└─────────────────────────────────────────────────────────────────────────────┘%s\n", cBold, cCyan, cReset))

	// Column widths: COMMIT(8) QUORUM(14) VOTED VALIDATORS(22) MISSING(14) DIGEST(11) -> 73
	sb.WriteString(fmt.Sprintf("%s%s %s %s %s %s%s\n",
		cBold,
		padRight("COMMIT", 8),
		padRight("QUORUM", 14),
		padRight("VOTED VALIDATORS", 22),
		padRight("MISSING", 14),
		padRight("DIGEST", 11),
		cReset))
	sb.WriteString(strings.Repeat("─", 73) + "\n")

	authToNode := make(map[int]string)
	for _, s := range states {
		authToNode[extractNodeIndex(s.Name)] = s.Name
	}

	// Show commits in newest-first order
	for i := len(recentCommits) - 1; i >= 0; i-- {
		c := recentCommits[i]
		commitStr := fmt.Sprintf("#%d", c.CommitIndex)

		var quorumStr string
		if c.QuorumReached {
			quorumStr = fmt.Sprintf("%s✅ REACHED (%d)%s", cGreen, len(c.Voters), cReset)
		} else {
			quorumStr = fmt.Sprintf("%s⏳ PENDING (%d)%s", cYellow, len(c.Voters), cReset)
		}

		votedNodes := make([]string, 0, len(c.Voters))
		for _, v := range c.Voters {
			idx := extractNodeIndex(v)
			if name, ok := authToNode[idx]; ok {
				votedNodes = append(votedNodes, name)
			} else {
				votedNodes = append(votedNodes, v)
			}
		}
		votedStr := strings.Join(votedNodes, ",")
		if len(votedStr) > 20 {
			votedStr = votedStr[:18] + ".."
		}

		missingNodes := make([]string, 0, len(c.MissingVoters))
		for _, v := range c.MissingVoters {
			idx := extractNodeIndex(v)
			if name, ok := authToNode[idx]; ok {
				missingNodes = append(missingNodes, name)
			} else {
				missingNodes = append(missingNodes, v)
			}
		}
		var missingStr string
		if len(missingNodes) == 0 {
			missingStr = fmt.Sprintf("%snone%s", cDim, cReset)
		} else {
			missingStr = fmt.Sprintf("%s[%s]%s", cRed, strings.Join(missingNodes, ","), cReset)
		}

		digestStr := c.QuorumDigest
		if len(digestStr) > 10 {
			digestStr = digestStr[:8] + ".."
		}
		if digestStr == "" {
			digestStr = "-"
		}

		sb.WriteString(fmt.Sprintf("%s %s %s %s %s\n",
			padRight(commitStr, 8),
			padRight(quorumStr, 14),
			padRight(fmt.Sprintf("[%s]", votedStr), 22),
			padRight(missingStr, 14),
			padRight(digestStr, 11)))
	}

	sb.WriteString(strings.Repeat("─", 73) + "\n")
	return sb.String()
}

// formatRecentBlockHistory formats a compact audit table of recent block voting & quorums
func formatRecentBlockHistory(recentBlocks []*BlockVoteRecord, useColor bool) string {
	if len(recentBlocks) == 0 {
		return ""
	}

	var cReset, cBold, cGreen, cYellow, cCyan string
	if useColor {
		cReset = colorReset
		cBold = colorBold
		cGreen = colorGreen
		cYellow = colorYellow
		cCyan = colorCyan
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s%s┌─────────────────────────────────────────────────────────────────────────────┐%s\n", cBold, cCyan, cReset))
	sb.WriteString(fmt.Sprintf("%s%s│                   RECENT BLOCK SYNC & EXECUTION STATUS (GO)                 │%s\n", cBold, cCyan, cReset))
	sb.WriteString(fmt.Sprintf("%s%s└─────────────────────────────────────────────────────────────────────────────┘%s\n", cBold, cCyan, cReset))

	// Column widths: BLOCK(6) LEADER(7) SYNCED NODES(20) SYNCING/LAG(23) STATUS(13) -> 73
	sb.WriteString(fmt.Sprintf("%s%s %s %s %s %s%s\n",
		cBold,
		padRight("BLOCK", 6),
		padRight("LEADER", 7),
		padRight("SYNCED NODES", 20),
		padRight("SYNCING / LAG", 23),
		padRight("STATUS", 13),
		cReset))
	sb.WriteString(strings.Repeat("─", 73) + "\n")

	for _, b := range recentBlocks {
		blockStr := fmt.Sprintf("#%d", b.BlockNumber)
		leaderStr := b.Leader
		if leaderStr == "" {
			leaderStr = "?"
		}

		// Format Synced nodes string
		var quorumStr string
		if len(b.QuorumVoters) > 0 {
			qVoters := strings.Join(b.QuorumVoters, ",")
			if len(qVoters) > 13 {
				qVoters = qVoters[:11] + ".."
			}
			quorumStr = fmt.Sprintf("[%s] (%d)", qVoters, len(b.QuorumVoters))
		} else {
			quorumStr = "-"
		}

		// Format Delayed / Syncing string
		var delayedParts []string
		for _, m := range b.InitialMissing {
			if dur, ok := b.CatchUpElapsed[m]; ok {
				delayedParts = append(delayedParts, fmt.Sprintf("%s(+%s)", m, formatShortDuration(dur)))
			} else if b.NodeVoted[m] {
				delayedParts = append(delayedParts, fmt.Sprintf("%s(synced)", m))
			} else {
				pendingDur := time.Since(b.FirstSeen)
				delayedParts = append(delayedParts, fmt.Sprintf("⏳%s(%s)", m, formatShortDuration(pendingDur)))
			}
		}

		delayedStr := "none"
		if len(delayedParts) > 0 {
			delayedStr = strings.Join(delayedParts, ",")
			if visualWidth(delayedStr) > 23 {
				delayedStr = delayedStr[:20] + "..."
			}
		}

		// Format Status string
		var statusStr string
		if b.IsCompleted {
			statusStr = fmt.Sprintf("%s✅ ALL SYNCED%s", cGreen, cReset)
		} else {
			statusStr = fmt.Sprintf("%s⏳ SYNCING%s", cBold+cYellow, cReset)
		}

		sb.WriteString(fmt.Sprintf("%s %s %s %s %s\n",
			padRight(blockStr, 6),
			padRight(leaderStr, 7),
			padRight(quorumStr, 20),
			padRight(delayedStr, 23),
			padRight(statusStr, 13)))
	}

	sb.WriteString(strings.Repeat("─", 73) + "\n")
	return sb.String()
}

// formatDashboard builds the dashboard table string. If useColor is false, no ANSI escape codes are emitted.
func formatDashboard(
	states []*NodeState,
	netHeight uint64,
	leaderInfo string,
	epochInfo string,
	activeAnomalies []string,
	ignoredList []string,
	seqStart uint64,
	trackedCount int,
	pendingCount int,
	oldestPending uint64,
	oldestWaiting []string,
	oldestElapsed time.Duration,
	recentBlocks []*BlockVoteRecord,
	recentCommits []CommitVoteDetails,
	useColor bool,
) string {
	var cReset, cBold, cRed, cGreen, cYellow, cCyan, cDim string
	if useColor {
		cReset = colorReset
		cBold = colorBold
		cRed = colorRed
		cGreen = colorGreen
		cYellow = colorYellow
		cCyan = colorCyan
		cDim = colorDim
	}

	var sb strings.Builder
	now := time.Now().Format("2006-01-02 15:04:05")
	sb.WriteString(fmt.Sprintf("%s%s┌─────────────────────────────────────────────────────────────────────────────┐%s\n", cBold, cCyan, cReset))
	sb.WriteString(fmt.Sprintf("%s%s│                METANODE BFT CONSENSUS VOTE & ADVANCE MONITOR                │%s\n", cBold, cCyan, cReset))
	sb.WriteString(fmt.Sprintf("%s%s└─────────────────────────────────────────────────────────────────────────────┘%s\n", cBold, cCyan, cReset))
	var maxQuorumCommit uint32
	for _, s := range states {
		if s.QuorumCommit > maxQuorumCommit {
			maxQuorumCommit = s.QuorumCommit
		}
		if s.CurrentCommit > maxQuorumCommit {
			maxQuorumCommit = s.CurrentCommit
		}
	}
	commitHeader := "-"
	if maxQuorumCommit > 0 {
		commitHeader = fmt.Sprintf("#%d", maxQuorumCommit)
	}

	sb.WriteString(fmt.Sprintf(" 🕒 %s%s%s | ⛓️ Block: %s#%d%s | 🗳️ Commit: %s%s%s | %s | %s\n",
		cBold, now, cReset, cBold+cGreen, netHeight, cReset, cBold+cCyan, commitHeader, cReset, epochInfo, leaderInfo))
	sb.WriteString(fmt.Sprintf(" ⚙️ Interval: %v | Miss Alert: %d blk | Stall: %v | Quorum: %d/%d\n",
		pollInterval, maxMisses, stallTimeout, (len(states)*2)/3+1, len(states)))

	// Sequential tracking status bar
	if seqStart > 0 {
		if pendingCount == 0 {
			sb.WriteString(fmt.Sprintf(" 🔍 Sequential: #%d → #%d | Status: %sALL VOTED (0 pending)%s\n\n",
				seqStart, netHeight, cGreen, cReset))
		} else {
			waitStr := strings.Join(oldestWaiting, ",")
			if len(waitStr) > 15 {
				waitStr = waitStr[:12] + "..."
			}
			sb.WriteString(fmt.Sprintf(" 🔍 Sequential: #%d → #%d | %s⚠️ Block #%d pending [%s] (%s)%s\n\n",
				seqStart, netHeight, cYellow, oldestPending, waitStr, oldestElapsed.Round(time.Second), cReset))
		}
	} else {
		sb.WriteString("\n")
	}

	// Table Header (Width: 77 chars)
	sb.WriteString(fmt.Sprintf("%s%s %s %s %s %s %s %s %s%s\n",
		cBold,
		padRight("NODE", 4),
		padRight("ROLE", 4),
		padRight("ENDPOINT", 20),
		padRight("BLOCK", 7),
		padRight("COMMIT", 8),
		padRight("LAG", 4),
		padRight("VOTE STATUS", 18),
		padRight("LATENCY", 7),
		cReset))
	sb.WriteString(strings.Repeat("─", 77) + "\n")

	aliveCount := 0
	for _, s := range states {
		var voteStatus string
		var lagStr string
		var blockStr string
		var commitStr string

		if s.IsIgnored {
			blockStr = fmt.Sprintf("%s-%s", cDim, cReset)
			commitStr = fmt.Sprintf("%s-%s", cDim, cReset)
			lagStr = fmt.Sprintf("%s-%s", cDim, cReset)
			voteStatus = fmt.Sprintf("%s⚪ STOPPED (TEST)%s", cDim, cReset)
		} else if !s.IsAlive {
			blockStr = fmt.Sprintf("%sERR%s", cRed, cReset)
			commitStr = fmt.Sprintf("%sERR%s", cRed, cReset)
			lagStr = fmt.Sprintf("%s-%s", cRed, cReset)
			voteStatus = fmt.Sprintf("%s❌ UNREACHABLE%s", cBold+cRed, cReset)
		} else {
			aliveCount++
			blockStr = fmt.Sprintf("#%d", s.CurrentHeight)
			if s.CurrentCommit > 0 {
				commitStr = fmt.Sprintf("#%d", s.CurrentCommit)
			} else {
				commitStr = "-"
			}

			lag := int64(netHeight) - int64(s.CurrentHeight)
			if lag < 0 {
				lag = 0
			}

			// Check if this node is the actual block leader
			isLeader := strings.Contains(leaderInfo, fmt.Sprintf("(%s)", s.Name)) ||
				strings.HasPrefix(leaderInfo, fmt.Sprintf("Leader: %s ", s.Name))

			if lag == 0 {
				lagStr = fmt.Sprintf("%s0%s", cGreen, cReset)
				if isLeader {
					voteStatus = fmt.Sprintf("%s👑 LEADER (VOTED)%s", cBold+cGreen, cReset)
				} else {
					voteStatus = fmt.Sprintf("%s✅ VOTED%s", cGreen, cReset)
				}
			} else if lag == 1 {
				lagStr = fmt.Sprintf("%s1%s", cYellow, cReset)
				voteStatus = fmt.Sprintf("%s🟡 VOTING (Lag 1)%s", cYellow, cReset)
			} else {
				lagStr = fmt.Sprintf("%s%s%d%s", cBold, cRed, lag, cReset)
				if s.ConsecutiveMiss >= maxMisses {
					voteStatus = fmt.Sprintf("%s🚨 MISSED (%d)%s", cBold+cRed, s.ConsecutiveMiss, cReset)
				} else {
					voteStatus = fmt.Sprintf("%s⚠️ MISSED (%d)%s", cYellow, s.ConsecutiveMiss, cReset)
				}
			}
		}

		latencyStr := fmt.Sprintf("%dms", s.Latency.Milliseconds())
		if s.Latency > 1*time.Second {
			latencyStr = fmt.Sprintf("%s%s%s", cYellow, latencyStr, cReset)
		}
		if !s.IsAlive || s.IsIgnored {
			latencyStr = "-"
		}

		cleanURL := strings.TrimPrefix(s.URL, "http://")
		if len(cleanURL) > 19 {
			cleanURL = cleanURL[:17] + ".."
		}

		sb.WriteString(fmt.Sprintf("%s %s %s %s %s %s %s %s\n",
			padRight(s.Name, 4),
			padRight("val", 4),
			padRight(cleanURL, 20),
			padRight(blockStr, 7),
			padRight(commitStr, 8),
			padRight(lagStr, 4),
			padRight(voteStatus, 18),
			padRight(latencyStr, 7)))
	}

	sb.WriteString(strings.Repeat("─", 77) + "\n")

	// Status line
	statusTag := fmt.Sprintf("%s🟢 HEALTHY%s", cBold+cGreen, cReset)
	if len(activeAnomalies) > 0 {
		statusTag = fmt.Sprintf("%s🚨 %d ANOMALIES%s", cBold+cRed, len(activeAnomalies), cReset)
	}
	testExemptStr := "-"
	if len(ignoredList) > 0 {
		testExemptStr = fmt.Sprintf("[%s]", strings.Join(ignoredList, ", "))
	}
	sb.WriteString(fmt.Sprintf(" Status: %s | Online: %d/%d | Tracked: %d | Test Exempt: %s\n",
		statusTag, aliveCount, len(states), trackedCount, testExemptStr))

	if len(activeAnomalies) > 0 {
		for _, a := range activeAnomalies {
			sb.WriteString(fmt.Sprintf(" %s⚠️ [ANOMALY]%s %s\n", cBold+cYellow, cReset, a))
		}
	}

	// Append Real BFT Consensus Commit Vote Audit AND Recent Block Voting History
	if len(recentCommits) > 0 {
		sb.WriteString("\n")
		sb.WriteString(formatConsensusAuditTable(recentCommits, states, useColor))
	}
	if len(recentBlocks) > 0 {
		sb.WriteString("\n")
		sb.WriteString(formatRecentBlockHistory(recentBlocks, useColor))
	}

	return sb.String()
}

// renderDashboard displays the live interactive dashboard in terminal
func renderDashboard(
	states []*NodeState,
	netHeight uint64,
	leaderInfo string,
	epochInfo string,
	activeAnomalies []string,
	ignoredList []string,
	seqStart uint64,
	trackedCount int,
	pendingCount int,
	oldestPending uint64,
	oldestWaiting []string,
	oldestElapsed time.Duration,
	recentBlocks []*BlockVoteRecord,
	recentCommits []CommitVoteDetails,
) {
	fmt.Print("\033[H\033[J")
	fmt.Print(formatDashboard(states, netHeight, leaderInfo, epochInfo, activeAnomalies, ignoredList, seqStart, trackedCount, pendingCount, oldestPending, oldestWaiting, oldestElapsed, recentBlocks, recentCommits, true))
	fmt.Printf("%s[Tip]%s Ctrl+C to exit. Logging to '%s'\n", colorCyan, colorReset, logFilePath)
}

// logDashboardSnapshot writes the clean plain-text dashboard table directly to vote_monitor.log
func logDashboardSnapshot(
	states []*NodeState,
	netHeight uint64,
	leaderInfo string,
	epochInfo string,
	activeAnomalies []string,
	ignoredList []string,
	seqStart uint64,
	trackedCount int,
	pendingCount int,
	oldestPending uint64,
	oldestWaiting []string,
	oldestElapsed time.Duration,
	recentBlocks []*BlockVoteRecord,
	recentCommits []CommitVoteDetails,
) {
	tableText := formatDashboard(states, netHeight, leaderInfo, epochInfo, activeAnomalies, ignoredList, seqStart, trackedCount, pendingCount, oldestPending, oldestWaiting, oldestElapsed, recentBlocks, recentCommits, false)

	logMu.Lock()
	defer logMu.Unlock()

	if logFile != nil {
		logFile.WriteString(tableText + "\n")
		logFile.Sync()
	}
}

func main() {
	flag.Parse()

	// Load Telegram config
	loadTelegramConfig()

	// Open log file
	var err error
	logFile, err = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Printf("⚠️ Warning: Cannot open log file %s: %v\n", logFilePath, err)
	} else {
		defer logFile.Close()
	}

	// Locate and read config
	resolvedConfig := findConfigFile()
	cfg, err := loadConfig(resolvedConfig)
	if err != nil {
		fmt.Printf("❌ Fatal: Failed to load config from %s: %v\n", resolvedConfig, err)
		os.Exit(1)
	}

	// Load Genesis validator mapping
	loadValidatorAddresses(cfg)

	logMessage("🚀 Starting MetaNode Validator Vote Monitor...")
	logMessage("📁 Loaded config: %s (%d validator nodes)", resolvedConfig, len(cfg.Nodes))
	if telegramBotToken != "" && telegramChatID != "" {
		logMessage("📱 Telegram Alerts: ENABLED (Chat ID: %s)", telegramChatID)
	} else {
		logMessage("⚠️ Telegram Alerts: DISABLED (Missing BOT_TOKEN or CHAT_ID)")
	}

	// Sort node names
	nodeNames := make([]string, 0, len(cfg.Nodes))
	for name := range cfg.Nodes {
		nodeNames = append(nodeNames, name)
	}
	sort.Slice(nodeNames, func(i, j int) bool {
		return extractNodeIndex(nodeNames[i]) < extractNodeIndex(nodeNames[j])
	})

	// Initialize states
	states := make([]*NodeState, 0, len(nodeNames))
	stateMap := make(map[string]*NodeState)
	now := time.Now()
	for _, name := range nodeNames {
		s := &NodeState{
			Name:          name,
			URL:           cfg.Nodes[name],
			Role:          cfg.Roles[name],
			Address:       valNameToAddr[name],
			LastVotedTime: now,
		}
		states = append(states, s)
		stateMap[name] = s
	}

	// Shared HTTP client with optimized connection pooling
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   1500 * time.Millisecond,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   2 * time.Second,
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		logMessage("🛑 Shutting down Vote Monitor...")
		if logFile != nil {
			logFile.Sync()
		}
		os.Exit(0)
	}()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		var wg sync.WaitGroup
		var mu sync.Mutex
		var currentMaxHeight uint64
		var bestNodeURL string
		var latestConsensusSnapshot *ConsensusVoteSnapshot

		// Refresh dynamically ignored nodes
		ignoredMap := getIgnoredNodes()
		var ignoredList []string
		for _, s := range states {
			cleanID := strconv.Itoa(extractNodeIndex(s.Name))
			isIgnored := ignoredMap[s.Name] || ignoredMap[cleanID]
			s.IsIgnored = isIgnored
			if isIgnored {
				ignoredList = append(ignoredList, s.Name)
				// Reset miss counter when ignored for tests
				s.ConsecutiveMiss = 0
				s.MissAlertSent = false
			}
		}

		// Poll all active nodes concurrently
		for _, s := range states {
			if s.IsIgnored {
				// Don't hammer node if intentionally stopped in test
				s.Latency = 0
				continue
			}

			wg.Add(1)
			go func(state *NodeState) {
				defer wg.Done()
				// 1. Query BFT consensus votes directly from Rust
				snap, lat, err := getConsensusVotes(httpClient, state.URL)
				// 2. Query execution block height from Go
				h, lat2, err2 := getBlockNumber(httpClient, state.URL)

				mu.Lock()
				defer mu.Unlock()

				if lat > 0 {
					state.Latency = lat
				} else {
					state.Latency = lat2
				}
				state.LastResponded = time.Now()

				if err == nil && snap != nil {
					state.IsAlive = true
					state.ErrorMessage = ""
					state.HasConsensusVotes = true
					state.ConsensusEpoch = snap.Epoch
					state.QuorumCommit = snap.QuorumCommitIndex

					// Extract this validator node's highest voted commit
					nodeIdx := extractNodeIndex(state.Name)
					votedCommit := snap.QuorumCommitIndex
					for _, auth := range snap.Authorities {
						if auth.AuthorityIndex == nodeIdx {
							votedCommit = auth.HighestVotedCommit
							break
						}
					}
					state.CurrentCommit = votedCommit

					if latestConsensusSnapshot == nil || snap.QuorumCommitIndex >= latestConsensusSnapshot.QuorumCommitIndex {
						latestConsensusSnapshot = snap
					}
				}

				if err2 == nil {
					state.IsAlive = true
					state.ErrorMessage = ""
					state.CurrentHeight = h

					if h > currentMaxHeight {
						currentMaxHeight = h
						bestNodeURL = state.URL
					}
				} else if err != nil {
					state.IsAlive = false
					state.ErrorMessage = err.Error()
				}
			}(s)
		}

		wg.Wait()

		// If no unignored node responded, try fallback to any responding node
		if currentMaxHeight == 0 {
			for _, s := range states {
				if s.IsAlive && s.CurrentHeight > currentMaxHeight {
					currentMaxHeight = s.CurrentHeight
					bestNodeURL = s.URL
				}
			}
		}

		// Fetch block metadata and leader resolution
		leaderInfo := "Leader: Unknown"
		epochInfo := "Epoch: -"
		var currentBlockHeader *blockHeaderResult

		if currentMaxHeight > 0 && bestNodeURL != "" {
			hdr, err := getBlockHeader(httpClient, bestNodeURL, currentMaxHeight)
			if err == nil && hdr != nil {
				currentBlockHeader = hdr
				leadAddr := strings.ToLower(hdr.LeaderAddress)
				if leadAddr == "" {
					leadAddr = strings.ToLower(hdr.Miner)
				}

				if leadAddr != "" {
					shortAddr := leadAddr
					if len(shortAddr) > 10 {
						shortAddr = shortAddr[:8] + "..."
					}

					// Resolve actual leader node name from address
					leaderNodeName := valAddrToName[leadAddr]
					if leaderNodeName != "" {
						leaderInfo = fmt.Sprintf("Leader: %s (%s)", leaderNodeName, shortAddr)
					} else {
						leaderInfo = fmt.Sprintf("Leader: %s", shortAddr)
					}
				}

				if hdr.Epoch != "" {
					if ep, err := strconv.ParseUint(strings.TrimPrefix(hdr.Epoch, "0x"), 16, 64); err == nil {
						epochInfo = fmt.Sprintf("Epoch: %d", ep)
					}
				}
			}
		}

		var activeAnomalies []string

		// -------------------------------------------------------------
		// 1. SEQUENTIAL PER-BLOCK TRACKING ENGINE (NO SKIPPING)
		// -------------------------------------------------------------
		if currentMaxHeight > 0 {
			if isInitialStartup {
				seqStartHeight = currentMaxHeight
				isInitialStartup = false
				tracker.LastChainAdvance = time.Now()
				logMessage("🎯 Initialized monitor at latest block #%d", currentMaxHeight)

				for _, s := range states {
					s.LastVotedHeight = s.CurrentHeight
					s.LastVotedTime = time.Now()
				}

				// Seed up to the last 5 blocks into trackedBlocks so history table is immediately populated
				seedStart := uint64(1)
				if currentMaxHeight > 5 {
					seedStart = currentMaxHeight - 4
				}
				for b := seedStart; b <= currentMaxHeight; b++ {
					rec := &BlockVoteRecord{
						BlockNumber:    b,
						FirstSeen:      time.Now(),
						CatchUpElapsed: make(map[string]time.Duration),
						NodeVoted:      make(map[string]bool),
						NodeVotedTime:  make(map[string]time.Time),
						NodeAlertSent:  make(map[string]bool),
					}
					var hdr *blockHeaderResult
					if b == currentMaxHeight && currentBlockHeader != nil {
						hdr = currentBlockHeader
					} else if bestNodeURL != "" {
						hdr, _ = getBlockHeader(httpClient, bestNodeURL, b)
					}
					if hdr != nil {
						leadAddr := hdr.LeaderAddress
						if leadAddr == "" {
							leadAddr = hdr.Miner
						}
						rec.LeaderAddr = strings.ToLower(leadAddr)
						rec.Leader = resolveLeader(leadAddr)
					}
					if rec.Leader == "" {
						rec.Leader = "?"
					}

					for _, s := range states {
						if s.IsIgnored {
							continue
						}
						if s.IsAlive && s.CurrentHeight >= b {
							rec.NodeVoted[s.Name] = true
							rec.NodeVotedTime[s.Name] = time.Now()
							rec.QuorumVoters = append(rec.QuorumVoters, s.Name)
						} else {
							rec.InitialMissing = append(rec.InitialMissing, s.Name)
						}
					}
					if len(rec.InitialMissing) == 0 {
						rec.IsCompleted = true
					}
					trackedBlocks = append(trackedBlocks, rec)
				}
				lastEnqueuedBlock = currentMaxHeight
			} else if currentMaxHeight > lastEnqueuedBlock {
				// ADVANCEMENT: Enqueue EVERY block from lastEnqueuedBlock+1 to currentMaxHeight
				newCount := currentMaxHeight - lastEnqueuedBlock
				if newCount > 1 {
					logMessage("📦 Network jumped from #%d to #%d (%d blocks) — enqueuing all blocks sequentially!",
						lastEnqueuedBlock, currentMaxHeight, newCount)
				} else {
					logMessage("📦 Network advanced to #%d", currentMaxHeight)
				}
				tracker.LastChainAdvance = time.Now()

				// Chain Stall Recovery alert if previously alerted
				if tracker.ChainStalledAlertSent {
					recMsg := fmt.Sprintf("Mạng blockchain đã tiếp tục sinh block trở lại tại Block #%d sau thời gian bị kẹt!", currentMaxHeight)
					logMessage("🟢 %s", recMsg)
					sendTelegramAlert("METANODE CHAIN ADVANCING", recMsg, true)
					tracker.ChainStalledAlertSent = false
				}

				activeValCount := 0
				for _, s := range states {
					if !s.IsIgnored {
						activeValCount++
					}
				}

				// Strictly sequential: enqueue each intermediate block
				for b := lastEnqueuedBlock + 1; b <= currentMaxHeight; b++ {
					rec := &BlockVoteRecord{
						BlockNumber:    b,
						FirstSeen:      time.Now(),
						CatchUpElapsed: make(map[string]time.Duration),
						NodeVoted:      make(map[string]bool),
						NodeVotedTime:  make(map[string]time.Time),
						NodeAlertSent:  make(map[string]bool),
					}

					var hdr *blockHeaderResult
					if b == currentMaxHeight && currentBlockHeader != nil {
						hdr = currentBlockHeader
					} else if bestNodeURL != "" {
						hdr, _ = getBlockHeader(httpClient, bestNodeURL, b)
					}
					if hdr != nil {
						leadAddr := hdr.LeaderAddress
						if leadAddr == "" {
							leadAddr = hdr.Miner
						}
						rec.LeaderAddr = strings.ToLower(leadAddr)
						rec.Leader = resolveLeader(leadAddr)
					}
					if rec.Leader == "" {
						rec.Leader = "?"
					}

					// Pre-mark nodes that already have reached block b
					for _, s := range states {
						if s.IsIgnored {
							continue
						}
						if s.IsAlive && s.CurrentHeight >= b {
							rec.NodeVoted[s.Name] = true
							rec.NodeVotedTime[s.Name] = time.Now()
							rec.QuorumVoters = append(rec.QuorumVoters, s.Name)
							s.LastVotedTime = time.Now()
							s.LastVotedHeight = s.CurrentHeight
						} else {
							rec.InitialMissing = append(rec.InitialMissing, s.Name)
						}
					}
					if len(rec.InitialMissing) == 0 {
						rec.IsCompleted = true
					}
					trackedBlocks = append(trackedBlocks, rec)

					// Log block creation event with synced status
					qStr := strings.Join(rec.QuorumVoters, ",")
					if len(rec.InitialMissing) > 0 {
						mStr := strings.Join(rec.InitialMissing, ",")
						logMessage("📦 Block #%d [Leader: %s] | Synced (%d/%d): [%s] | Syncing: [%s]",
							b, rec.Leader, len(rec.QuorumVoters), activeValCount, qStr, mStr)
					} else {
						logMessage("📦 Block #%d [Leader: %s] | Synced (%d/%d): [%s] (All synced)",
							b, rec.Leader, len(rec.QuorumVoters), activeValCount, qStr)
					}
				}
				lastEnqueuedBlock = currentMaxHeight
			}
		}

		// -------------------------------------------------------------
		// 2. CHECK VOTE COMPLETION & PER-BLOCK 10-MINUTE TIMEOUT
		// -------------------------------------------------------------
		var pendingCount int
		var oldestPending uint64
		var oldestWaiting []string
		var oldestElapsed time.Duration

		for _, b := range trackedBlocks {
			if b.IsCompleted {
				continue
			}

			allVoted := true
			elapsed := time.Since(b.FirstSeen)
			var waitingNodes []string

			for _, s := range states {
				if s.IsIgnored {
					continue
				}

				if b.NodeVoted[s.Name] {
					continue
				}

				// Check if node has reached block b now
				if s.IsAlive && s.CurrentHeight >= b.BlockNumber {
					b.NodeVoted[s.Name] = true
					b.NodeVotedTime[s.Name] = time.Now()
					s.LastVotedTime = time.Now()
					s.LastVotedHeight = s.CurrentHeight
					if b.CatchUpElapsed == nil {
						b.CatchUpElapsed = make(map[string]time.Duration)
					}
					b.CatchUpElapsed[s.Name] = elapsed

					logMessage("🟢 [CATCH-UP] Node %s caught up vote for Block #%d after %s (Quorum was: [%s])",
						s.Name, b.BlockNumber, formatShortDuration(elapsed), strings.Join(b.QuorumVoters, ","))

					// If an alert was sent for this block, send recovery!
					if b.NodeAlertSent[s.Name] {
						recMsg := fmt.Sprintf("Node *%s* (%s) đã VOTE thành công cho Block `#%d` (sau %s trễ)!",
							s.Name, s.URL, b.BlockNumber, elapsed.Round(time.Second))
						logMessage("🟢 %s", recMsg)
						sendTelegramAlert(fmt.Sprintf("METANODE BLOCK #%d VOTE RECOVERED", b.BlockNumber), recMsg, true)
						b.NodeAlertSent[s.Name] = false
					}
					continue
				}

				// Node still has not voted / reached block b
				allVoted = false
				waitingNodes = append(waitingNodes, s.Name)

				// Check 10-minute timeout for this specific block:
				if elapsed >= stallTimeout {
					activeAnomalies = append(activeAnomalies,
						fmt.Sprintf("Node %s chưa vote Block #%d (%s)", s.Name, b.BlockNumber, elapsed.Round(time.Second)))

					if !b.NodeAlertSent[s.Name] {
						alertMsg := fmt.Sprintf("Node *%s* (%s) KHÔNG THAM GIA VOTE cho Block `#%d` trong suốt *%s* (vượt ngưỡng %s)!\n• Block chưa vote: `#%d`\n• Block mạng hiện tại: `#%d`\n• Block hiện tại của node: `#%d` (Lag %d blocks)\n• Block sinh ra lúc: %s\n• Trạng thái: Node có thể đã bị treo consensus, desync hoặc network partition.",
							s.Name, s.URL, b.BlockNumber, elapsed.Round(time.Second), stallTimeout,
							b.BlockNumber, currentMaxHeight, s.CurrentHeight, int64(currentMaxHeight)-int64(s.CurrentHeight),
							b.FirstSeen.Format("15:04:05"))
						logMessage("🚨 ANOMALY: %s", alertMsg)
						sendTelegramAlert(fmt.Sprintf("METANODE ANOMALY: MISSED BLOCK #%d (>10m)", b.BlockNumber), alertMsg, false)
						b.NodeAlertSent[s.Name] = true
					}
				}
			}

			if allVoted {
				b.IsCompleted = true
			} else {
				pendingCount++
				if oldestPending == 0 {
					oldestPending = b.BlockNumber
					oldestWaiting = waitingNodes
					oldestElapsed = elapsed
				}
			}
		}

		// Prune completed blocks older than 100 entries to prevent memory growth
		if len(trackedBlocks) > 150 {
			var pruned []*BlockVoteRecord
			completedKept := 0
			for i := len(trackedBlocks) - 1; i >= 0; i-- {
				rec := trackedBlocks[i]
				if !rec.IsCompleted {
					pruned = append(pruned, rec)
				} else if completedKept < 50 {
					pruned = append(pruned, rec)
					completedKept++
				}
			}
			for i, j := 0, len(pruned)-1; i < j; i, j = i+1, j-1 {
				pruned[i], pruned[j] = pruned[j], pruned[i]
			}
			trackedBlocks = pruned
		}

		// -------------------------------------------------------------
		// 3. CONSECUTIVE MISSED BLOCKS EVALUATION
		// -------------------------------------------------------------
		if currentMaxHeight > 0 {
			for _, s := range states {
				if s.IsIgnored {
					s.ConsecutiveMiss = 0
					s.MissAlertSent = false
					continue
				}

				if !s.IsAlive {
					s.ConsecutiveMiss++
					s.TotalMissed++
				} else {
					lag := int64(currentMaxHeight) - int64(s.CurrentHeight)
					if lag <= 1 {
						if s.MissAlertSent {
							recoveryMsg := fmt.Sprintf("Node *%s* (%s) đã vote trở lại tại block #%d (sau %d block lỡ).",
								s.Name, s.URL, s.CurrentHeight, s.ConsecutiveMiss)
							logMessage("🟢 %s", recoveryMsg)
							sendTelegramAlert("METANODE VOTE RECOVERED", recoveryMsg, true)
							s.MissAlertSent = false
						}
						s.ConsecutiveMiss = 0
					} else {
						s.ConsecutiveMiss = int(lag)
						if s.LastMissLoggedBlock != currentMaxHeight {
							s.TotalMissed++
							s.LastMissLoggedBlock = currentMaxHeight
							logMessage("⚠️ Node %s MISSED vote for block #%d (Current: #%d, Lag: %d blocks, Miss streak: %d)",
								s.Name, currentMaxHeight, s.CurrentHeight, lag, s.ConsecutiveMiss)
						}
					}
				}

				if s.ConsecutiveMiss >= maxMisses && !s.MissAlertSent {
					alertMsg := fmt.Sprintf("Node *%s* (%s) KHÔNG VOTE liên tiếp *%d* blocks!\n• Block mạng: `#%d`\n• Block của node: `#%d`\n• Trạng thái: Có thể bị desync, crash hoặc network partition.",
						s.Name, s.URL, s.ConsecutiveMiss, currentMaxHeight, s.CurrentHeight)
					logMessage("🚨 ALERT TRIGGERED: %s", alertMsg)
					sendTelegramAlert("METANODE MISSED BLOCKS ALERT", alertMsg, false)
					s.MissAlertSent = true
				}
			}
		}

		// -------------------------------------------------------------
		// 4. ANOMALY DETECTION: TIME-BASED CHAIN STALL (10m)
		// -------------------------------------------------------------
		timeSinceChainAdvance := time.Since(tracker.LastChainAdvance)
		if currentMaxHeight > 0 && timeSinceChainAdvance >= chainStallTimeout {
			activeAnomalies = append(activeAnomalies,
				fmt.Sprintf("Toàn mạng kẹt block #%d suốt %s", currentMaxHeight, timeSinceChainAdvance.Round(time.Second)))

			if !tracker.ChainStalledAlertSent {
				chainMsg := fmt.Sprintf("TOÀN MẠNG KHÔNG RA BLOCK MỚI suốt *%s* (vượt ngưỡng %s)!\n• Block dừng tại: `#%d`\n• Trạng thái: Chuỗi bị kẹt consensus, không thể commit block mới.",
					timeSinceChainAdvance.Round(time.Second), chainStallTimeout, currentMaxHeight)
				logMessage("🚨 ANOMALY: %s", chainMsg)
				sendTelegramAlert("METANODE ANOMALY: CHAIN STALLED", chainMsg, false)
				tracker.ChainStalledAlertSent = true
			}
		}

		// -------------------------------------------------------------
		// 5. ANOMALY DETECTION: QUORUM INTEGRITY (< 2f+1)
		// -------------------------------------------------------------
		totalValidators := len(states)
		quorumThreshold := (totalValidators*2)/3 + 1
		onlineValidators := 0
		var offlineNodes []string

		for _, s := range states {
			if s.IsAlive && !s.IsIgnored {
				onlineValidators++
			} else if !s.IsIgnored {
				offlineNodes = append(offlineNodes, s.Name)
			}
		}

		if onlineValidators < quorumThreshold && len(offlineNodes) > 0 {
			activeAnomalies = append(activeAnomalies,
				fmt.Sprintf("Quorum risk: %d/%d nodes online (Cần tối thiểu %d)", onlineValidators, totalValidators, quorumThreshold))

			if !tracker.QuorumAlertSent {
				quorumMsg := fmt.Sprintf("NGUY CƠ MẤT ĐỒNG THUẬN QUORUM BFT!\n• Số Validator Online: *%d/%d* (Cần tối thiểu: *%d* để đạt 2f+1)\n• Các node bị ngắt kết nối: `%s`\n• Cảnh báo: Mạng sẽ không thể đồng thuận sinh block mới!",
					onlineValidators, totalValidators, quorumThreshold, strings.Join(offlineNodes, ", "))
				logMessage("🚨 ANOMALY: %s", quorumMsg)
				sendTelegramAlert("METANODE ANOMALY: QUORUM LOSS RISK", quorumMsg, false)
				tracker.QuorumAlertSent = true
			}
		} else if tracker.QuorumAlertSent && onlineValidators >= quorumThreshold {
			recMsg := fmt.Sprintf("Cụm Validator đã phục hồi đủ Quorum BFT (%d/%d nodes online)!", onlineValidators, totalValidators)
			logMessage("🟢 %s", recMsg)
			sendTelegramAlert("METANODE QUORUM RECOVERED", recMsg, true)
			tracker.QuorumAlertSent = false
		}

		// -------------------------------------------------------------
		// 6. ANOMALY DETECTION: ZERO-FORK VERIFICATION
		// -------------------------------------------------------------
		if currentMaxHeight > 0 && currentBlockHeader != nil && len(states) >= 2 {
			refHash := currentBlockHeader.Hash
			hasForkAtMaxHeight := false
			var forkedNodeNames []string
			var lastForkMsg string

			for _, s := range states {
				if !s.IsAlive || s.IsIgnored || s.CurrentHeight != currentMaxHeight {
					continue
				}
				// Fetch header from other node at currentMaxHeight to cross-verify
				hHdr, err := getBlockHeader(httpClient, s.URL, currentMaxHeight)
				if err == nil && hHdr != nil && hHdr.Hash != "" {
					if hHdr.Hash != refHash {
						hasForkAtMaxHeight = true
						forkedNodeNames = append(forkedNodeNames, s.Name)
						lastForkMsg = fmt.Sprintf("CẢNH BÁO VI PHẠM ZERO-FORK TẠI BLOCK #%d!\n• Node chuẩn (%s): `%s`\n• Node %s (%s): `%s`\n• Mạng đang bị chia nhánh (Fork)!",
							currentMaxHeight, bestNodeURL, refHash, s.Name, s.URL, hHdr.Hash)
						activeAnomalies = append(activeAnomalies, fmt.Sprintf("FORK DETECTED at #%d (%s vs %s)", currentMaxHeight, s.Name, bestNodeURL))
					}
				}
			}

			if hasForkAtMaxHeight {
				// Build detailed fork report
				var forkReport strings.Builder
				forkReport.WriteString("================================================================================\n")
				forkReport.WriteString(fmt.Sprintf("🚨 ZERO-FORK VIOLATION DETECTED AT BLOCK #%d\n", currentMaxHeight))
				forkReport.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format("2006-01-02 15:04:05.000")))
				forkReport.WriteString(fmt.Sprintf("Reference Node: %s (Hash: %s)\n", bestNodeURL, refHash))
				forkReport.WriteString(fmt.Sprintf("Divergent Nodes: %s\n", strings.Join(forkedNodeNames, ", ")))
				forkReport.WriteString("--------------------------------------------------------------------------------\n")
				forkReport.WriteString(lastForkMsg + "\n")
				forkReport.WriteString("--------------------------------------------------------------------------------\n")
				forkReport.WriteString("👉 HƯỚNG DẪN XỬ LÝ (RUNBOOK CHO DEV):\n")
				forkReport.WriteString("• Nếu chỉ 1 node bị lệch hash do hỏng DB hoặc out-of-sync > 5 epochs, khôi phục node đó từ Snapshot:\n")
				forkReport.WriteString("  `./ansible_deploy.sh --reset-all --only-node <ID> --restore-node <ID> --snapshot-url <URL>`\n")
				forkReport.WriteString("• Tuyệt đối KHÔNG chạy --reset-all toàn cụm để tránh làm mất dữ liệu cả mạng.\n")
				forkReport.WriteString("================================================================================\n")

				forkLogPath := "fork_mismatch_alert.log"
				_ = os.WriteFile(forkLogPath, []byte(forkReport.String()), 0644)
				logMessage("📄 Chi tiết vi phạm Zero-Fork đã được ghi vào: %s", forkLogPath)

				// Dừng vote_monitor và nhường quyền gửi cảnh báo Telegram chi tiết cho block_hash_checker
				if !noStopFlag {
					_ = os.WriteFile("/tmp/MTN_CHAIN_ERROR_STOP", []byte(forkReport.String()), 0644)
					logMessage("🛑 ĐÃ KÍCH HOẠT CỜ DỪNG AUTO_TEST (/tmp/MTN_CHAIN_ERROR_STOP)")
					fmt.Printf("\n🛑 DỪNG VOTE_MONITOR: Phát hiện vi phạm Zero-Fork tại block #%d! Dừng để block_hash_checker báo cáo Telegram.\n", currentMaxHeight)
					os.Exit(1)
				} else {
					logMessage("🔕 [--no-stop-flag] Phát hiện vi phạm Zero-Fork tại block #%d nhưng tiếp tục chạy quan sát.", currentMaxHeight)
				}
			} else {
				// No fork at currentMaxHeight
				tracker.mu.Lock()
				wasInFork := tracker.InForkState
				if wasInFork {
					tracker.InForkState = false
					recMsg := fmt.Sprintf("Tất cả các node đã đồng nhất block hash tại Block #%d (`%s`)!", currentMaxHeight, refHash)
					logMessage("🟢 FORK RECOVERED: %s", recMsg)
					sendTelegramAlert("METANODE ZERO-FORK RECOVERED", recMsg, true)
				}
				tracker.mu.Unlock()
			}
		}

		// Calculate completed blocks count and extract recent blocks
		completedCount := 0
		for _, b := range trackedBlocks {
			if b.IsCompleted {
				completedCount++
			}
		}
		recentBlocks := getRecentBlocks(trackedBlocks, 5)
		var recentCommits []CommitVoteDetails
		if latestConsensusSnapshot != nil {
			recentCommits = latestConsensusSnapshot.RecentCommits
		}

		// Render live dashboard in terminal if in watch mode
		if watchMode && !daemonMode {
			renderDashboard(
				states,
				currentMaxHeight,
				leaderInfo,
				epochInfo,
				activeAnomalies,
				ignoredList,
				seqStartHeight,
				len(trackedBlocks),
				pendingCount,
				oldestPending,
				oldestWaiting,
				oldestElapsed,
				recentBlocks,
				recentCommits,
			)
		}

		// Log clean table snapshot to vote_monitor.log whenever state advances or periodic heartbeat
		if shouldLogSnapshot(currentMaxHeight, states, pendingCount, completedCount, len(activeAnomalies)) {
			logDashboardSnapshot(
				states,
				currentMaxHeight,
				leaderInfo,
				epochInfo,
				activeAnomalies,
				ignoredList,
				seqStartHeight,
				len(trackedBlocks),
				pendingCount,
				oldestPending,
				oldestWaiting,
				oldestElapsed,
				recentBlocks,
				recentCommits,
			)
			recordSnapshotState(currentMaxHeight, states, pendingCount, completedCount, len(activeAnomalies))
		}

		select {
		case <-sigChan:
			return
		case <-ticker.C:
		}
	}
}
