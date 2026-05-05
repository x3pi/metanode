package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ===== JSON-RPC types =====

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type blockResult struct {
	Hash             string `json:"hash"`
	Number           string `json:"number"`
	ParentHash       string `json:"parentHash"`
	StateRoot        string `json:"stateRoot"`
	StakeStatesRoot  string `json:"stakeStatesRoot"`
	TransactionsRoot string `json:"transactionsRoot"`
	ReceiptsRoot     string `json:"receiptsRoot"`
	GlobalExecIndex  string `json:"globalExecIndex"`
	Epoch            string `json:"epoch"`
	LeaderAddress    string `json:"leaderAddress"`
	Miner            string `json:"miner"`
	Timestamp        string `json:"timestamp"`
}

// ===== Block info (parsed from blockResult) =====

type blockInfo struct {
	Hash             string
	ParentHash       string
	StateRoot        string
	StakeStatesRoot  string
	TransactionsRoot string
	ReceiptsRoot     string
	GlobalExecIndex  string
	Epoch            string
	LeaderAddress    string
	Timestamp        string
	Error            string // non-empty if fetch failed
}

func (b blockInfo) IsError() bool {
	return b.Error != ""
}

// ===== Node info =====

type nodeInfo struct {
	Name string
	URL  string
}

// ===== Mismatch record =====

type mismatch struct {
	BlockNumber uint64
	Blocks      map[string]blockInfo // node name -> block info
}

func main() {
	nodesFlag := flag.String("nodes", "", `Danh sách node, format: "name=url,name2=url2"`)
	fromBlock := flag.Uint64("from", 1, "Block bắt đầu kiểm tra")
	toBlock := flag.Uint64("to", 0, "Block kết thúc (0 = lấy block mới nhất)")
	batchSize := flag.Int("batch", 50, "Số block kiểm tra song song mỗi lần")
	timeout := flag.Duration("timeout", 5*time.Second, "Timeout cho mỗi RPC call")
	watchMode := flag.Bool("watch", false, "Chế độ giám sát liên tục — kiểm tra block mới nhất định kỳ")
	watchInterval := flag.Duration("interval", 10*time.Second, "Khoảng thời gian giữa mỗi lần check (watch mode)")
	checkLast := flag.Int("check-last", 5, "Số block gần nhất cần check mỗi cycle (watch mode)")
	flag.Parse()

	if *nodesFlag == "" {
		fmt.Println("❌ Thiếu --nodes flag")
		fmt.Println()
		fmt.Println("Cách dùng:")
		fmt.Println(`  # Quét 1 lần:`)
		fmt.Println(`  ./block_hash_checker --nodes "master=http://localhost:8747,node4=http://localhost:10748" --from 1 --to 5000`)
		fmt.Println()
		fmt.Println(`  # Giám sát liên tục:`)
		fmt.Println(`  ./block_hash_checker --watch --nodes "master=http://localhost:8747,node4=http://localhost:10748" --interval 10s`)
		os.Exit(1)
	}

	// Parse nodes
	nodes := parseNodes(*nodesFlag)
	if len(nodes) < 2 {
		fmt.Println("❌ Cần ít nhất 2 node để so sánh")
		os.Exit(1)
	}

	fmt.Printf("🔍 Block Hash Checker — So sánh %d nodes\n", len(nodes))
	for _, n := range nodes {
		fmt.Printf("   📡 %s: %s\n", n.Name, n.URL)
	}
	fmt.Println()

	client := &http.Client{Timeout: *timeout}

	// ===== Watch mode =====
	if *watchMode {
		runWatch(client, nodes, *watchInterval, *checkLast)
		return
	}

	// Nếu --to=0, query block mới nhất từ node đầu tiên
	if *toBlock == 0 {
		latest, err := getLatestBlockNumber(client, nodes[0].URL)
		if err != nil {
			fmt.Printf("❌ Không thể lấy block mới nhất từ %s: %v\n", nodes[0].Name, err)
			os.Exit(1)
		}
		*toBlock = latest
		fmt.Printf("📊 Block mới nhất trên %s: %d\n", nodes[0].Name, *toBlock)
	}

	totalBlocks := *toBlock - *fromBlock + 1
	fmt.Printf("📊 Kiểm tra block %d → %d (%d blocks)\n\n", *fromBlock, *toBlock, totalBlocks)

	// ===== Quét block =====
	var allMismatches []mismatch
	var matchCount uint64
	var errorCount uint64
	var skipCount uint64
	startTime := time.Now()

	for batchStart := *fromBlock; batchStart <= *toBlock; batchStart += uint64(*batchSize) {
		batchEnd := batchStart + uint64(*batchSize) - 1
		if batchEnd > *toBlock {
			batchEnd = *toBlock
		}

		batchMismatches, batchMatches, batchErrors, batchSkips, _ := checkBatch(client, nodes, batchStart, batchEnd)
		allMismatches = append(allMismatches, batchMismatches...)
		matchCount += batchMatches
		errorCount += batchErrors
		skipCount += batchSkips

		// Progress
		checked := batchEnd - *fromBlock + 1
		elapsed := time.Since(startTime)
		rate := float64(checked) / elapsed.Seconds()
		fmt.Printf("\r⏳ Đã kiểm tra %d/%d blocks (%.0f blocks/s, %d lệch, %d lỗi, %d bỏ qua)   ",
			checked, totalBlocks, rate, len(allMismatches), errorCount, skipCount)
	}
	fmt.Println()
	fmt.Println()

	// ===== Báo cáo =====
	elapsed := time.Since(startTime)

	if len(allMismatches) == 0 {
		fmt.Printf("✅ KẾT QUẢ: Tất cả %d blocks KHỚP giữa %d nodes (%.1fs)\n",
			matchCount, len(nodes), elapsed.Seconds())
	} else {
		fmt.Printf("🚨 KẾT QUẢ: Phát hiện %d blocks LỆCH HASH!\n", len(allMismatches))
		fmt.Printf("   ✅ Khớp: %d | 🚨 Lệch: %d | ❌ Lỗi: %d (%.1fs)\n\n",
			matchCount, len(allMismatches), errorCount, elapsed.Seconds())

		// Chi tiết từng mismatch
		maxShow := 50
		for i, m := range allMismatches {
			if i >= maxShow {
				fmt.Printf("   ... và %d blocks lệch khác (bỏ qua)\n", len(allMismatches)-maxShow)
				break
			}
			printMismatchDetail(m, nodes)
		}

		// Xuất file CSV
		csvFile := fmt.Sprintf("mismatches_%d_%d.csv", *fromBlock, *toBlock)
		if err := writeMismatchCSV(csvFile, nodes, allMismatches); err != nil {
			fmt.Printf("⚠️  Không thể ghi file CSV: %v\n", err)
		} else {
			fmt.Printf("📄 Chi tiết đã ghi vào: %s\n", csvFile)
		}
	}
}

// ===== Parse nodes flag =====

func parseNodes(s string) []nodeInfo {
	var nodes []nodeInfo
	parts := strings.Split(s, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		eqIdx := strings.Index(p, "=")
		if eqIdx < 0 {
			fmt.Printf("⚠️  Bỏ qua node không hợp lệ (thiếu '='): %s\n", p)
			continue
		}
		name := strings.TrimSpace(p[:eqIdx])
		url := strings.TrimSpace(p[eqIdx+1:])
		if name == "" || url == "" {
			continue
		}
		nodes = append(nodes, nodeInfo{Name: name, URL: url})
	}
	return nodes
}

// ===== Check a batch of blocks =====

func checkBatch(client *http.Client, nodes []nodeInfo, from, to uint64) (mismatches []mismatch, matchCount, errorCount, skipCount uint64, emptyBlocks []uint64) {
	type result struct {
		blockNum uint64
		blocks   map[string]blockInfo
		hasError bool
	}

	count := to - from + 1
	results := make([]result, count)

	var wg sync.WaitGroup
	sem := make(chan struct{}, 10) // max 10 concurrent

	for i := uint64(0); i < count; i++ {
		wg.Add(1)
		go func(idx uint64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			blockNum := from + idx
			blocks := make(map[string]blockInfo)
			hasErr := false

			for _, node := range nodes {
				bi, err := getBlockInfo(client, node.URL, blockNum)
				if err != nil {
					blocks[node.Name] = blockInfo{Error: fmt.Sprintf("ERROR: %v", err)}
					hasErr = true
				} else {
					blocks[node.Name] = bi
				}
			}

			results[idx] = result{blockNum: blockNum, blocks: blocks, hasError: hasErr}
		}(i)
	}
	wg.Wait()

	// Build a map of block hash by node name for chain integrity check (parentHash verification)
	// prevBlockHashes[nodeName] = hash of previous block on that node
	prevBlockHashes := make(map[string]string)

	for _, r := range results {
		if r.hasError {
			errorCount++
		}

		// === CHECK 1: Compare all fields across nodes ===
		var validBlocks []blockInfo
		var validNames []string
		missingResponseCount := 0

		for _, node := range nodes {
			bi := r.blocks[node.Name]
			if !bi.IsError() {
				validBlocks = append(validBlocks, bi)
				validNames = append(validNames, node.Name)
			} else if bi.Error == "(block không tồn tại)" {
				missingResponseCount++
			}
		}

		if len(validBlocks) < 2 {
			// Nếu tất cả các node phản hồi đều báo block không tồn tại, thì coi là ghost block và bỏ qua.
			// (Không cần đợi các node đang lỗi/sập phản hồi)
			if len(validBlocks) == 0 && missingResponseCount > 0 {
				emptyBlocks = append(emptyBlocks, r.blockNum)
				continue
			}

			skipCount++
			// Still update prevBlockHashes for chain integrity
			for _, node := range nodes {
				bi := r.blocks[node.Name]
				if !bi.IsError() {
					prevBlockHashes[node.Name] = bi.Hash
				}
			}
			continue
		}

		// Compare hash, parentHash, stateRoot, txRoot, receiptsRoot, etc. across all valid nodes
		hasMismatch := false
		ref := validBlocks[0]
		for i := 1; i < len(validBlocks); i++ {
			b := validBlocks[i]
			if b.Hash != ref.Hash || b.ParentHash != ref.ParentHash ||
				b.StateRoot != ref.StateRoot || b.TransactionsRoot != ref.TransactionsRoot ||
				b.ReceiptsRoot != ref.ReceiptsRoot || b.LeaderAddress != ref.LeaderAddress ||
				b.StakeStatesRoot != ref.StakeStatesRoot || b.Timestamp != ref.Timestamp ||
				b.GlobalExecIndex != ref.GlobalExecIndex || b.Epoch != ref.Epoch {
				hasMismatch = true
				break
			}
		}

		// === CHECK 2: Chain integrity — parentHash of block N == hash of block N-1 ===
		// CRITICAL FIX: Save original hashes BEFORE marking errors, to prevent cascade.
		// Otherwise, once a block is marked CHAIN BROKEN (Error set), prevBlockHashes
		// stops updating → every subsequent block is also falsely flagged CHAIN BROKEN.
		originalHashes := make(map[string]string)
		for _, node := range nodes {
			bi := r.blocks[node.Name]
			if !bi.IsError() {
				originalHashes[node.Name] = bi.Hash
			}
		}

		for _, node := range nodes {
			bi := r.blocks[node.Name]
			if bi.IsError() {
				continue
			}
			prevHash, hasPrev := prevBlockHashes[node.Name]
			if hasPrev && bi.ParentHash != prevHash {
				// Chain is broken on this node!
				hasMismatch = true
				// Mark the error in the block info for display
				brokenBi := r.blocks[node.Name]
				brokenBi.Error = fmt.Sprintf("CHAIN BROKEN: parentHash=%s but prev block hash=%s",
					bi.ParentHash[:18]+"...", prevHash[:18]+"...")
				r.blocks[node.Name] = brokenBi
			}
		}

		// Update prevBlockHashes using original (pre-error-marking) hashes
		// This prevents cascading false positives
		for nodeName, hash := range originalHashes {
			prevBlockHashes[nodeName] = hash
		}
		// If a node was missing or returned an error for this block, we MUST remove it from prevBlockHashes.
		// Otherwise, when the next block arrives, it will be incorrectly compared against a stale (N-2) hash
		// and falsely flagged as "CHAIN BROKEN" (e.g. a false positive chain break during heavy load async writes).
		for _, node := range nodes {
			if _, ok := originalHashes[node.Name]; !ok {
				delete(prevBlockHashes, node.Name)
			}
		}

		if hasMismatch {
			mismatches = append(mismatches, mismatch{BlockNumber: r.blockNum, Blocks: r.blocks})
		} else {
			matchCount++
		}
	}

	return
}

// (hash comparison is now inline in checkBatch — compares all fields + chain integrity)

// ===== JSON-RPC calls =====

func getBlockInfo(client *http.Client, url string, blockNum uint64) (blockInfo, error) {
	hexBlock := fmt.Sprintf("0x%x", blockNum)
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  "eth_getBlockByNumber",
		Params:  []interface{}{hexBlock, false},
		ID:      1,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return blockInfo{}, err
	}

	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return blockInfo{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return blockInfo{}, err
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return blockInfo{}, fmt.Errorf("invalid JSON response: %v", err)
	}

	if rpcResp.Error != nil {
		return blockInfo{}, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	if string(rpcResp.Result) == "null" {
		return blockInfo{Error: "(block không tồn tại)"}, nil
	}

	var block blockResult
	if err := json.Unmarshal(rpcResp.Result, &block); err != nil {
		return blockInfo{}, fmt.Errorf("cannot parse block result: %v", err)
	}

	// Prefer leaderAddress (custom field), fall back to miner (ETH standard)
	leaderAddr := block.LeaderAddress
	if leaderAddr == "" {
		leaderAddr = block.Miner
	}

	return blockInfo{
		Hash:             block.Hash,
		ParentHash:       block.ParentHash,
		StateRoot:        block.StateRoot,
		StakeStatesRoot:  block.StakeStatesRoot,
		TransactionsRoot: block.TransactionsRoot,
		ReceiptsRoot:     block.ReceiptsRoot,
		GlobalExecIndex:  block.GlobalExecIndex,
		Epoch:            block.Epoch,
		LeaderAddress:    leaderAddr,
		Timestamp:        block.Timestamp,
	}, nil
}

func parseHexStr(hexStr string) uint64 {
	if hexStr == "" {
		return 0
	}
	var num uint64
	fmt.Sscanf(hexStr, "0x%x", &num)
	return num
}

func getLatestBlockNumber(client *http.Client, url string) (uint64, error) {
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  "eth_blockNumber",
		Params:  []interface{}{},
		ID:      1,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}

	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return 0, fmt.Errorf("invalid JSON response: %v", err)
	}

	if rpcResp.Error != nil {
		return 0, fmt.Errorf("RPC error: %s", rpcResp.Error.Message)
	}

	var hexStr string
	if err := json.Unmarshal(rpcResp.Result, &hexStr); err != nil {
		return 0, fmt.Errorf("cannot parse block number: %v", err)
	}

	// Parse hex
	var num uint64
	fmt.Sscanf(hexStr, "0x%x", &num)
	return num, nil
}

type peerInfoResp struct {
	Epoch           uint64 `json:"epoch"`
	GlobalExecIndex uint64 `json:"global_exec_index"`
	LastBlockNumber uint64 `json:"last_block_number"`
}

func getPeerInfo(client *http.Client, rpcURL string) (uint64, uint64, error) {
	// rpcURL looks like http://127.0.0.1:8757
	peerURL := strings.TrimRight(rpcURL, "/") + "/peer_info"
	resp, err := client.Get(peerURL)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, 0, fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, err
	}

	var pInfo peerInfoResp
	if err := json.Unmarshal(data, &pInfo); err != nil {
		return 0, 0, err
	}

	return pInfo.GlobalExecIndex, pInfo.Epoch, nil
}

// ===== Print mismatch detail =====

func printMismatchDetail(m mismatch, nodes []nodeInfo) {
	fmt.Printf("\n⚠\ufe0f  Block %d:\n", m.BlockNumber)

	// Collect valid blocks to show which fields actually differ
	var validBlocks []blockInfo
	var validNames []string
	for _, n := range nodes {
		bi, ok := m.Blocks[n.Name]
		if ok && !bi.IsError() {
			validBlocks = append(validBlocks, bi)
			validNames = append(validNames, n.Name)
		}
	}

	// Determine which fields differ
	hashDiff, parentDiff, stateDiff, stakeDiff, txDiff, rcpDiff, leaderDiff, timeDiff := false, false, false, false, false, false, false, false
	if len(validBlocks) >= 2 {
		ref := validBlocks[0]
		for _, b := range validBlocks[1:] {
			if b.Hash != ref.Hash {
				hashDiff = true
			}
			if b.ParentHash != ref.ParentHash {
				parentDiff = true
			}
			if b.StateRoot != ref.StateRoot {
				stateDiff = true
			}
			if b.StakeStatesRoot != ref.StakeStatesRoot {
				stakeDiff = true
			}
			if b.TransactionsRoot != ref.TransactionsRoot {
				txDiff = true
			}
			if b.ReceiptsRoot != ref.ReceiptsRoot {
				rcpDiff = true
			}
			if b.LeaderAddress != ref.LeaderAddress {
				leaderDiff = true
			}
			if b.Timestamp != ref.Timestamp {
				timeDiff = true
			}
		}
	}

	// Print diff summary
	var diffs []string
	if hashDiff {
		diffs = append(diffs, "hash")
	}
	if parentDiff {
		diffs = append(diffs, "parentHash")
	}
	if stateDiff {
		diffs = append(diffs, "stateRoot")
	}
	if stakeDiff {
		diffs = append(diffs, "stakeStatesRoot")
	}
	if txDiff {
		diffs = append(diffs, "txRoot")
	}
	if rcpDiff {
		diffs = append(diffs, "receiptsRoot")
	}
	if leaderDiff {
		diffs = append(diffs, "leaderAddress")
	}
	if timeDiff {
		diffs = append(diffs, "timestamp")
	}
	if len(diffs) > 0 {
		fmt.Printf("   ⚠\ufe0f  Fields differ: %s\n", strings.Join(diffs, ", "))
	}

	for _, n := range nodes {
		bi, ok := m.Blocks[n.Name]
		if !ok {
			fmt.Printf("   %-12s (kh\u00f4ng c\u00f3 d\u1eef li\u1ec7u)\n", n.Name+":")
			continue
		}
		if bi.IsError() {
			fmt.Printf("   %-12s %s\n", n.Name+":", bi.Error)
			continue
		}
		fmt.Printf("   %-12s hash=%s leader=%s gei=%d epoch=%d\n", n.Name+":", bi.Hash, bi.LeaderAddress, parseHexStr(bi.GlobalExecIndex), parseHexStr(bi.Epoch))
		if parentDiff {
			fmt.Printf("   %-12s parentHash=%s\n", "", bi.ParentHash)
		}
		if stateDiff {
			fmt.Printf("   %-12s stateRoot=%s\n", "", bi.StateRoot)
		}
		if stakeDiff {
			fmt.Printf("   %-12s stakeStatesRoot=%s\n", "", bi.StakeStatesRoot)
		}
		if txDiff {
			fmt.Printf("   %-12s txRoot=%s\n", "", bi.TransactionsRoot)
		}
		if rcpDiff {
			fmt.Printf("   %-12s receiptsRoot=%s\n", "", bi.ReceiptsRoot)
		}
		if leaderDiff {
			fmt.Printf("   %-12s leaderAddress=%s\n", "", bi.LeaderAddress)
		}
		if timeDiff {
			fmt.Printf("   %-12s timestamp=%s\n", "", bi.Timestamp)
		}
	}
	fmt.Println()
}

// ===== CSV export =====

func writeMismatchCSV(filename string, nodes []nodeInfo, mismatches []mismatch) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	// Header
	sortedNames := make([]string, len(nodes))
	for i, n := range nodes {
		sortedNames[i] = n.Name
	}
	sort.Strings(sortedNames)

	header := "block_number"
	for _, name := range sortedNames {
		header += "," + name + "_hash"
		header += "," + name + "_parentHash"
		header += "," + name + "_stateRoot"
		header += "," + name + "_stakeStatesRoot"
		header += "," + name + "_txRoot"
		header += "," + name + "_receiptsRoot"
		header += "," + name + "_leaderAddress"
		header += "," + name + "_timestamp"
		header += "," + name + "_gei"
		header += "," + name + "_epoch"
	}
	fmt.Fprintln(f, header)

	// Data
	for _, m := range mismatches {
		line := fmt.Sprintf("%d", m.BlockNumber)
		for _, name := range sortedNames {
			bi, ok := m.Blocks[name]
			if !ok || bi.IsError() {
				errMsg := ""
				if ok {
					errMsg = bi.Error
				}
				line += "," + errMsg + ",,,,,,,,,"
			} else {
				line += "," + bi.Hash
				line += "," + bi.ParentHash
				line += "," + bi.StateRoot
				line += "," + bi.StakeStatesRoot
				line += "," + bi.TransactionsRoot
				line += "," + bi.ReceiptsRoot
				line += "," + bi.LeaderAddress
				line += "," + bi.Timestamp
				line += fmt.Sprintf(",%d,%d", parseHexStr(bi.GlobalExecIndex), parseHexStr(bi.Epoch))
			}
		}
		fmt.Fprintln(f, line)
	}

	return nil
}

// ===== Watch mode =====

const mismatchAlertFile = "hash_mismatch_alert.log"

func runWatch(client *http.Client, nodes []nodeInfo, interval time.Duration, checkLast int) {
	fmt.Printf("👁️  WATCH MODE — kiểm tra %d blocks gần nhất mỗi %v\n", checkLast, interval)
	fmt.Println("   Nhấn Ctrl+C để dừng")
	fmt.Println("   🛑 Tự động DỪNG khi phát hiện lệch hash (ghi vào " + mismatchAlertFile + ")")
	fmt.Println()

	// Handle Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	totalChecks := 0
	totalMismatches := 0
	trackedGhosts := make(map[uint64]bool)

	// Run immediately on start
	if watchOnce(client, nodes, checkLast, &totalChecks, &totalMismatches, trackedGhosts) {
		fmt.Printf("\n🛑 DỪNG WATCH MODE: Phát hiện lệch hash! Chi tiết đã ghi vào %s\n", mismatchAlertFile)
		fmt.Printf("📊 Tổng kết: %d lần check, %d lệch phát hiện\n", totalChecks, totalMismatches)
		os.Exit(1)
	}

	for {
		select {
		case <-ticker.C:
			if watchOnce(client, nodes, checkLast, &totalChecks, &totalMismatches, trackedGhosts) {
				fmt.Printf("\n🛑 DỪNG WATCH MODE: Phát hiện lệch hash! Chi tiết đã ghi vào %s\n", mismatchAlertFile)
				fmt.Printf("📊 Tổng kết: %d lần check, %d lệch phát hiện\n", totalChecks, totalMismatches)
				os.Exit(1)
			}
		case sig := <-sigCh:
			fmt.Printf("\n\n🛑 Nhận signal %v — dừng watch mode\n", sig)
			fmt.Printf("📊 Tổng kết: %d lần check, %d lệch phát hiện\n", totalChecks, totalMismatches)
			return
		}
	}
}

// watchOnce returns true if mismatch detected (caller should stop)
func watchOnce(client *http.Client, nodes []nodeInfo, checkLast int, totalChecks, totalMismatches *int, trackedGhosts map[uint64]bool) bool {
	*totalChecks++
	now := time.Now().Format("15:04:05")

	// Query latest block number from each node
	type nodeBlock struct {
		name  string
		block uint64
		gei   uint64
		epoch uint64
		err   error
	}

	var results []nodeBlock
	var minBlock, maxBlock uint64
	minBlock = ^uint64(0)

	for _, n := range nodes {
		num, err := getLatestBlockNumber(client, n.URL)
		
		var gei, epoch uint64
		if err == nil {
			// Lấy gei và epoch từ chính block mới nhất thông qua eth_getBlockByNumber
			bi, errBi := getBlockInfo(client, n.URL, num)
			if errBi == nil {
				gei = parseHexStr(bi.GlobalExecIndex)
				epoch = parseHexStr(bi.Epoch)
			}
		}

		results = append(results, nodeBlock{name: n.Name, block: num, gei: gei, epoch: epoch, err: err})
		if err == nil {
			if num < minBlock {
				minBlock = num
			}
			if num > maxBlock {
				maxBlock = num
			}
		}
	}

	// Show block heights
	fmt.Printf("[%s] #%d ", now, *totalChecks)
	var heightParts []string
	for _, r := range results {
		if r.err != nil {
			heightParts = append(heightParts, fmt.Sprintf("%s=ERR", r.name))
		} else {
			heightParts = append(heightParts, fmt.Sprintf("%s=%d (gei:%d e:%d)", r.name, r.block, r.gei, r.epoch))
		}
	}
	fmt.Printf("Heights: %s", strings.Join(heightParts, "  "))

	// Block height difference warning
	if maxBlock > 0 && minBlock < ^uint64(0) {
		diff := maxBlock - minBlock
		if diff > 10 {
			fmt.Printf(" ⚠️ CHÊNH %d blocks!", diff)
		}
	}

	// Count how many nodes are actually responding
	respondingNodes := 0
	for _, r := range results {
		if r.err == nil {
			respondingNodes++
		}
	}

	// Check last N blocks — use minBlock as reference (all nodes should have these)
	if minBlock == ^uint64(0) {
		fmt.Println(" ❌ không thể check hash — tất cả node lỗi")
		return false
	}

	if respondingNodes < 2 {
		fmt.Printf(" ⚠️ chỉ %d/%d node phản hồi — KHÔNG THỂ SO SÁNH hash\n", respondingNodes, len(nodes))
		return false
	}

	from := minBlock
	if from > uint64(checkLast) {
		from = minBlock - uint64(checkLast) + 1
	} else {
		from = 1
	}

	mismatches, matched, _, skipped, emptyBlocks := checkBatch(client, nodes, from, minBlock)

	if len(mismatches) == 0 {
		if skipped > 0 {
			fmt.Printf(" ✅ hash khớp %d blocks, ⚠️ %d blocks không đủ node (block %d→%d)\n", matched, skipped, from, minBlock)
		} else {
			fmt.Printf(" ✅ hash khớp %d blocks (block %d→%d)\n", matched, from, minBlock)
		}
		
		if len(emptyBlocks) > 0 {
			show := len(emptyBlocks)
			if show > 10 { show = 10 }
			fmt.Printf("   👻 Có %d block rỗng/nhảy cóc: %v", len(emptyBlocks), emptyBlocks[:show])
			if len(emptyBlocks) > 10 { fmt.Printf("...") }
			fmt.Println()
			
			// Lưu vào file (tránh trùng lặp)
			if trackedGhosts != nil {
				f, err := os.OpenFile("ghost_blocks.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
				if err == nil {
					nowStr := time.Now().Format("2006-01-02 15:04:05")
					for _, b := range emptyBlocks {
						if !trackedGhosts[b] {
							fmt.Fprintf(f, "[%s] Ghost block detected: %d\n", nowStr, b)
							trackedGhosts[b] = true
						}
					}
					f.Close()
				}
			}
		}
		
		// In hash của block mới nhất (minBlock) từ mỗi node
		fmt.Printf("   📦 Block %d hashes:\n", minBlock)
		for _, n := range nodes {
			bi, err := getBlockInfo(client, n.URL, minBlock)
			if err != nil {
				fmt.Printf("      %-12s ERR: %v\n", n.Name+":", err)
			} else if bi.IsError() {
				fmt.Printf("      %-12s %s\n", n.Name+":", bi.Error)
			} else {
				fmt.Printf("      %-12s hash=%s  stateRoot=%s  leader=%s  gei=%d  epoch=%d\n", n.Name+":", bi.Hash, bi.StateRoot, bi.LeaderAddress, parseHexStr(bi.GlobalExecIndex), parseHexStr(bi.Epoch))
			}
		}
		return false
	}

	// ═══════════════════════════════════════════════════════════════════
	// MISMATCH DETECTED — write to file + print to console + signal stop
	// ═══════════════════════════════════════════════════════════════════
	*totalMismatches += len(mismatches)

	// --- AUTO BACKTRACK TO FIND EXACT FORK POINT ---
	earliestMismatch := mismatches[0].BlockNumber
	for _, m := range mismatches {
		if m.BlockNumber < earliestMismatch {
			earliestMismatch = m.BlockNumber
		}
	}

	fmt.Printf("\n🔍 Tự động dò ngược (backtrack) từ block %d để tìm điểm chia nhánh (fork point)...\n", earliestMismatch)
	
	// Try up to 500 blocks backwards to find the exact fork point
	forkPoint := earliestMismatch
	for b := earliestMismatch - 1; b > 0 && b >= earliestMismatch-500; b-- {
		bMismatches, _, bErrors, _, _ := checkBatch(client, nodes, b, b)
		if len(bMismatches) > 0 {
			forkPoint = b
			// Prepend to our mismatch list for the report
			mismatches = append([]mismatch{bMismatches[0]}, mismatches...)
		} else if bErrors == 0 {
			// Found a block that MATCHES and has NO errors! This means 'b+1' is the fork point.
			fmt.Printf("   ✅ Block %d khớp hash.\n", b)
			fmt.Printf("   🛑 => ĐIỂM CHIA NHÁNH (FORK POINT) CHÍNH XÁC LÀ BLOCK: %d\n", forkPoint)
			break
		} else {
			// error fetching block, we just stop backtracking
			fmt.Printf("   ⚠️ Lỗi khi lấy block %d, dừng dò ngược.\n", b)
			break
		}
	}
	// ------------------------------------------------

	// Build alert content for both console and file
	var alertBuf strings.Builder
	alertBuf.WriteString("╔══════════════════════════════════════════════════════════════════╗\n")
	alertBuf.WriteString(fmt.Sprintf("║  🚨 HASH MISMATCH DETECTED — %s                      ║\n", time.Now().Format("2006-01-02 15:04:05")))
	alertBuf.WriteString("╚══════════════════════════════════════════════════════════════════╝\n")
	alertBuf.WriteString(fmt.Sprintf("\nCheck #%d | Blocks checked: %d→%d | Mismatches: %d\n", *totalChecks, from, minBlock, len(mismatches)))
	alertBuf.WriteString("\nNode Heights:\n")
	for _, r := range results {
		if r.err != nil {
			alertBuf.WriteString(fmt.Sprintf("  %-12s ERR: %v\n", r.name+":", r.err))
		} else {
			alertBuf.WriteString(fmt.Sprintf("  %-12s block=%d\n", r.name+":", r.block))
		}
	}
	alertBuf.WriteString("\n─── Mismatch Details ───\n")

	// Sort mismatches by BlockNumber ascending
	sort.Slice(mismatches, func(i, j int) bool {
		return mismatches[i].BlockNumber < mismatches[j].BlockNumber
	})

	if len(mismatches) > 0 {
		firstMismatch := mismatches[0]
		alertBuf.WriteString(fmt.Sprintf("\n🛑 ĐIỂM CHIA NHÁNH (FORK POINT) - Block %d:\n", firstMismatch.BlockNumber))
		
		// Find mismatched fields
		var validBlocks []blockInfo
		for _, n := range nodes {
			bi, ok := firstMismatch.Blocks[n.Name]
			if ok && !bi.IsError() {
				validBlocks = append(validBlocks, bi)
			}
		}

		hashDiff, parentDiff, stateDiff, stakeDiff, txDiff, rcpDiff, leaderDiff, timeDiff, geiDiff, epochDiff := false, false, false, false, false, false, false, false, false, false
		if len(validBlocks) >= 2 {
			ref := validBlocks[0]
			for _, b := range validBlocks[1:] {
				if b.Hash != ref.Hash { hashDiff = true }
				if b.ParentHash != ref.ParentHash { parentDiff = true }
				if b.StateRoot != ref.StateRoot { stateDiff = true }
				if b.StakeStatesRoot != ref.StakeStatesRoot { stakeDiff = true }
				if b.TransactionsRoot != ref.TransactionsRoot { txDiff = true }
				if b.ReceiptsRoot != ref.ReceiptsRoot { rcpDiff = true }
				if b.LeaderAddress != ref.LeaderAddress { leaderDiff = true }
				if b.Timestamp != ref.Timestamp { timeDiff = true }
				if b.GlobalExecIndex != ref.GlobalExecIndex { geiDiff = true }
				if b.Epoch != ref.Epoch { epochDiff = true }
			}
		}

		var diffs []string
		if hashDiff { diffs = append(diffs, "hash") }
		if parentDiff { diffs = append(diffs, "parentHash") }
		if stateDiff { diffs = append(diffs, "stateRoot") }
		if stakeDiff { diffs = append(diffs, "stakeStatesRoot") }
		if txDiff { diffs = append(diffs, "txRoot") }
		if rcpDiff { diffs = append(diffs, "receiptsRoot") }
		if leaderDiff { diffs = append(diffs, "leaderAddress") }
		if timeDiff { diffs = append(diffs, "timestamp") }
		if geiDiff { diffs = append(diffs, "gei") }
		if epochDiff { diffs = append(diffs, "epoch") }
		
		if len(diffs) > 0 {
			alertBuf.WriteString(fmt.Sprintf("   🔍 NGUYÊN NHÂN: Sai lệch ở các trường -> %s\n", strings.Join(diffs, ", ")))
		}

		for _, n := range nodes {
			bi, ok := firstMismatch.Blocks[n.Name]
			if !ok {
				alertBuf.WriteString(fmt.Sprintf("   %-12s (no data)\n", n.Name+":"))
				continue
			}
			if bi.IsError() {
				alertBuf.WriteString(fmt.Sprintf("   %-12s %s\n", n.Name+":", bi.Error))
				continue
			}
			alertBuf.WriteString(fmt.Sprintf("   %-12s hash=%s  parentHash=%s  stateRoot=%s  stakeRoot=%s  txRoot=%s  receiptsRoot=%s  leader=%s  time=%s  gei=%d  epoch=%d\n",
				n.Name+":", bi.Hash, bi.ParentHash, bi.StateRoot, bi.StakeStatesRoot, bi.TransactionsRoot, bi.ReceiptsRoot, bi.LeaderAddress, bi.Timestamp, parseHexStr(bi.GlobalExecIndex), parseHexStr(bi.Epoch)))
		}

		// Only show last 3 blocks if there are many
		showLast := 3
		startIdx := len(mismatches) - showLast
		if startIdx <= 0 {
			startIdx = 1
		}

		if startIdx > 1 {
			alertBuf.WriteString(fmt.Sprintf("\n   ... (bỏ qua %d block sai lệch ở giữa) ...\n", startIdx-1))
		}

		for i := startIdx; i < len(mismatches); i++ {
			m := mismatches[i]
			alertBuf.WriteString(fmt.Sprintf("\n⚠️  Block %d:\n", m.BlockNumber))
			for _, n := range nodes {
				bi, ok := m.Blocks[n.Name]
				if !ok {
					alertBuf.WriteString(fmt.Sprintf("   %-12s (no data)\n", n.Name+":"))
					continue
				}
				if bi.IsError() {
					alertBuf.WriteString(fmt.Sprintf("   %-12s %s\n", n.Name+":", bi.Error))
					continue
				}
				alertBuf.WriteString(fmt.Sprintf("   %-12s hash=%s  parentHash=%s  stateRoot=%s  stakeRoot=%s  txRoot=%s  receiptsRoot=%s  leader=%s  time=%s  gei=%d  epoch=%d\n",
					n.Name+":", bi.Hash, bi.ParentHash, bi.StateRoot, bi.StakeStatesRoot, bi.TransactionsRoot, bi.ReceiptsRoot, bi.LeaderAddress, bi.Timestamp, parseHexStr(bi.GlobalExecIndex), parseHexStr(bi.Epoch)))
			}
		}
	}

	alertBuf.WriteString("\n─── Summary ───\n")
	alertBuf.WriteString(fmt.Sprintf("Total mismatches: %d\n", *totalMismatches))
	alertBuf.WriteString(fmt.Sprintf("Detected at: %s\n", time.Now().Format("2006-01-02 15:04:05.000")))

	alertContent := alertBuf.String()

	// Print to console
	fmt.Printf("\n")
	fmt.Print(alertContent)

	// Write to file (overwrite)
	if err := os.WriteFile(mismatchAlertFile, []byte(alertContent), 0644); err != nil {
		fmt.Printf("⚠️  Không thể ghi file %s: %v\n", mismatchAlertFile, err)
	} else {
		fmt.Printf("\n📄 Chi tiết đã ghi vào: %s\n", mismatchAlertFile)
	}

	// Also write CSV for detailed analysis
	csvFile := fmt.Sprintf("mismatches_%d_%d.csv", from, minBlock)
	if err := writeMismatchCSV(csvFile, nodes, mismatches); err != nil {
		fmt.Printf("⚠️  Không thể ghi file CSV: %v\n", err)
	} else {
		fmt.Printf("📄 CSV chi tiết: %s\n", csvFile)
	}

	return true // Signal caller to STOP
}
