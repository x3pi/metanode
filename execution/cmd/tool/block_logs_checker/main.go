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

// ===== JSON-RPC primitives =====

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

// ===== Log entry (eth_getLogs response item) =====

type logEntry struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockNumber      string   `json:"blockNumber"`
	TransactionHash  string   `json:"transactionHash"`
	TransactionIndex string   `json:"transactionIndex"`
	BlockHash        string   `json:"blockHash"`
	LogIndex         string   `json:"logIndex"`
	Removed          bool     `json:"removed"`
}

// fingerprint returns a unique key for a log entry used in comparison.
func (l logEntry) fingerprint() string {
	return strings.Join([]string{
		l.TransactionHash,
		l.LogIndex,
		l.Address,
		strings.Join(l.Topics, ";"),
		l.Data,
	}, "|")
}

// ===== Per-node logs result =====

type nodeLogsResult struct {
	Logs  []logEntry
	Error string
}

func (r nodeLogsResult) isError() bool { return r.Error != "" }

// ===== Node info =====

type nodeInfo struct {
	Name string
	URL  string
}

// ===== Mismatch record =====

type blockMismatch struct {
	BlockNumber uint64
	NodeResults map[string]nodeLogsResult
}

// ===== main =====

func main() {
	nodesFlag := flag.String("nodes", "", `Danh sách node, format: "name=url,name2=url2"`)
	fromBlock := flag.Uint64("from", 1, "Block bắt đầu kiểm tra")
	toBlock := flag.Uint64("to", 0, "Block kết thúc (0 = lấy block mới nhất từ node đầu tiên)")
	batchSize := flag.Int("batch", 20, "Số block kiểm tra song song mỗi lần")
	timeout := flag.Duration("timeout", 10*time.Second, "Timeout cho mỗi RPC call")
	watchMode := flag.Bool("watch", false, "Chế độ giám sát liên tục")
	watchInterval := flag.Duration("interval", 15*time.Second, "Khoảng thời gian giữa mỗi lần check (watch mode)")
	checkLast := flag.Int("check-last", 3, "Số block gần nhất cần check mỗi cycle (watch mode)")
	addrFilter := flag.String("address", "", "Lọc theo contract address (tùy chọn)")
	topicsStr := flag.String("topics", "", `Lọc theo topics, format: "0xabc,0xdef" (OR logic, tùy chọn)`)
	flag.Parse()

	if *nodesFlag == "" {
		printUsage()
		os.Exit(1)
	}

	nodes := parseNodes(*nodesFlag)
	if len(nodes) < 2 {
		fmt.Println("❌ Cần ít nhất 2 node để so sánh")
		os.Exit(1)
	}

	topicFilter := parseTopics(*topicsStr)
	client := &http.Client{Timeout: *timeout}

	printHeader(nodes, *addrFilter, topicFilter)

	if *watchMode {
		runWatch(client, nodes, *watchInterval, *checkLast, *addrFilter, topicFilter)
		return
	}

	// Resolve toBlock
	if *toBlock == 0 {
		latest, err := getLatestBlockNumber(client, nodes[0].URL)
		if err != nil {
			fmt.Printf("❌ Không thể lấy block mới nhất từ %s: %v\n", nodes[0].Name, err)
			os.Exit(1)
		}
		*toBlock = latest
		fmt.Printf("📊 Block mới nhất trên %s: %d\n", nodes[0].Name, *toBlock)
	}

	total := *toBlock - *fromBlock + 1
	fmt.Printf("📊 Kiểm tra block %d → %d (%d blocks)\n\n", *fromBlock, *toBlock, total)

	var allMismatches []blockMismatch
	var matchCount, errCount uint64
	start := time.Now()

	for bStart := *fromBlock; bStart <= *toBlock; bStart += uint64(*batchSize) {
		bEnd := bStart + uint64(*batchSize) - 1
		if bEnd > *toBlock {
			bEnd = *toBlock
		}
		mm, matched, errs := checkBatch(client, nodes, bStart, bEnd, *addrFilter, topicFilter)
		allMismatches = append(allMismatches, mm...)
		matchCount += matched
		errCount += errs

		checked := bEnd - *fromBlock + 1
		rate := float64(checked) / time.Since(start).Seconds()
		fmt.Printf("\r⏳ Đã kiểm tra %d/%d blocks (%.0f blk/s, %d lệch, %d lỗi)   ",
			checked, total, rate, len(allMismatches), errCount)
	}
	fmt.Println()
	fmt.Println()

	elapsed := time.Since(start)
	if len(allMismatches) == 0 {
		fmt.Printf("✅ KẾT QUẢ: Tất cả %d blocks có LOGS KHỚP giữa %d nodes (%.1fs)\n",
			matchCount, len(nodes), elapsed.Seconds())
		return
	}

	fmt.Printf("🚨 KẾT QUẢ: Phát hiện %d blocks LỆCH LOGS!\n", len(allMismatches))
	fmt.Printf("   ✅ Khớp: %d | 🚨 Lệch: %d | ❌ Lỗi: %d (%.1fs)\n\n",
		matchCount, len(allMismatches), errCount, elapsed.Seconds())

	maxShow := 50
	for i, m := range allMismatches {
		if i >= maxShow {
			fmt.Printf("   ... và %d blocks lệch khác (bỏ qua)\n", len(allMismatches)-maxShow)
			break
		}
		printMismatch(m, nodes)
	}

	csvFile := fmt.Sprintf("logs_mismatches_%d_%d.csv", *fromBlock, *toBlock)
	if err := writeMismatchCSV(csvFile, nodes, allMismatches); err != nil {
		fmt.Printf("⚠️  Không thể ghi CSV: %v\n", err)
	} else {
		fmt.Printf("📄 Chi tiết đã ghi vào: %s\n", csvFile)
	}
}

// ===== Helpers =====

func printUsage() {
	fmt.Println("❌ Thiếu --nodes flag")
	fmt.Println()
	fmt.Println("Cách dùng:")
	fmt.Println(`  # Quét 1 lần:`)
	fmt.Println(`  ./block_logs_checker --nodes "master=http://localhost:8747,node4=http://localhost:10748" --from 1 --to 5000`)
	fmt.Println()
	fmt.Println(`  # Với filter address/topic:`)
	fmt.Println(`  ./block_logs_checker --nodes "..." --from 1 --to 100 \`)
	fmt.Println(`      --address 0x00000000000000000000000000000000B429C0B2 \`)
	fmt.Println(`      --topics "0xb528e3a3d4cbfd0b61a83cc28a004e801777b8ed6274adee62a727632fee66dd"`)
	fmt.Println()
	fmt.Println(`  # Watch mode:`)
	fmt.Println(`  ./block_logs_checker --watch --nodes "..." --interval 15s --check-last 3`)
}

func printHeader(nodes []nodeInfo, addr string, topics [][]string) {
	fmt.Printf("🔍 Block Logs Checker — So sánh eth_getLogs trên %d nodes\n", len(nodes))
	for _, n := range nodes {
		fmt.Printf("   📡 %s: %s\n", n.Name, n.URL)
	}
	if addr != "" {
		fmt.Printf("   🏷️  Address filter: %s\n", addr)
	}
	if len(topics) > 0 {
		fmt.Printf("   🏷️  Topics filter: %v\n", topics)
	}
	fmt.Println()
}

func parseNodes(s string) []nodeInfo {
	var nodes []nodeInfo
	for _, p := range strings.Split(s, ",") {
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

func parseTopics(s string) [][]string {
	if s == "" {
		return nil
	}
	var list []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			list = append(list, t)
		}
	}
	if len(list) == 0 {
		return nil
	}
	return [][]string{list}
}

// ===== Batch check =====

func checkBatch(
	client *http.Client,
	nodes []nodeInfo,
	from, to uint64,
	addr string,
	topicFilter [][]string,
) (mismatches []blockMismatch, matchCount, errCount uint64) {
	count := to - from + 1

	type result struct {
		blockNum    uint64
		nodeResults map[string]nodeLogsResult
		hasError    bool
	}
	results := make([]result, count)

	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)

	for i := uint64(0); i < count; i++ {
		wg.Add(1)
		go func(idx uint64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			blockNum := from + idx
			nodeResults := make(map[string]nodeLogsResult)
			hasErr := false

			for _, node := range nodes {
				logs, err := getLogsForBlock(client, node.URL, blockNum, addr, topicFilter)
				if err != nil {
					nodeResults[node.Name] = nodeLogsResult{Error: fmt.Sprintf("ERROR: %v", err)}
					hasErr = true
				} else {
					nodeResults[node.Name] = nodeLogsResult{Logs: logs}
				}
			}
			results[idx] = result{blockNum: blockNum, nodeResults: nodeResults, hasError: hasErr}
		}(i)
	}
	wg.Wait()

	for _, r := range results {
		if r.hasError {
			errCount++
		}

		// Collect valid (non-error) node results
		var validNames []string
		for _, node := range nodes {
			res := r.nodeResults[node.Name]
			if !res.isError() {
				validNames = append(validNames, node.Name)
			}
		}
		if len(validNames) < 2 {
			continue // not enough data to compare
		}

		refName := validNames[0]
		refRes := r.nodeResults[refName]
		refKeys := buildFingerprintSet(refRes.Logs)

		hasMismatch := false
		for _, name := range validNames[1:] {
			cmpRes := r.nodeResults[name]
			if len(cmpRes.Logs) != len(refRes.Logs) {
				hasMismatch = true
				break
			}
			if !fingerprintSetEqual(refKeys, buildFingerprintSet(cmpRes.Logs)) {
				hasMismatch = true
				break
			}
		}

		if hasMismatch {
			mismatches = append(mismatches, blockMismatch{BlockNumber: r.blockNum, NodeResults: r.nodeResults})
		} else {
			matchCount++
		}
	}
	return
}

func buildFingerprintSet(logs []logEntry) map[string]int {
	m := make(map[string]int, len(logs))
	for _, l := range logs {
		m[l.fingerprint()]++
	}
	return m
}

func fingerprintSetEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// ===== RPC calls =====

func getLogsForBlock(
	client *http.Client,
	nodeURL string,
	blockNum uint64,
	addr string,
	topicFilter [][]string,
) ([]logEntry, error) {
	hexBlock := fmt.Sprintf("0x%x", blockNum)
	filter := map[string]interface{}{
		"fromBlock": hexBlock,
		"toBlock":   hexBlock,
	}
	if addr != "" {
		filter["address"] = addr
	}
	if len(topicFilter) > 0 {
		filter["topics"] = topicFilter
	}

	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  "eth_getLogs",
		Params:  []interface{}{filter},
		ID:      1,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	resp, err := client.Post(nodeURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	var logs []logEntry
	if err := json.Unmarshal(rpcResp.Result, &logs); err != nil {
		return nil, fmt.Errorf("cannot parse logs: %w", err)
	}
	return logs, nil
}

func getLatestBlockNumber(client *http.Client, nodeURL string) (uint64, error) {
	req := rpcRequest{JSONRPC: "2.0", Method: "eth_blockNumber", Params: []interface{}{}, ID: 1}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}

	resp, err := client.Post(nodeURL, "application/json", bytes.NewReader(body))
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
		return 0, err
	}
	if rpcResp.Error != nil {
		return 0, fmt.Errorf("RPC error: %s", rpcResp.Error.Message)
	}

	var hexStr string
	if err := json.Unmarshal(rpcResp.Result, &hexStr); err != nil {
		return 0, err
	}
	var num uint64
	fmt.Sscanf(hexStr, "0x%x", &num)
	return num, nil
}

// ===== Display =====

func printMismatch(m blockMismatch, nodes []nodeInfo) {
	fmt.Printf("\n⚠️  Block %d:\n", m.BlockNumber)
	for _, n := range nodes {
		res, ok := m.NodeResults[n.Name]
		if !ok {
			fmt.Printf("   %-12s (không có dữ liệu)\n", n.Name+":")
			continue
		}
		if res.isError() {
			fmt.Printf("   %-12s %s\n", n.Name+":", res.Error)
			continue
		}
		fmt.Printf("   %-12s %d logs\n", n.Name+":", len(res.Logs))
		maxPrint := 3
		for i, l := range res.Logs {
			if i >= maxPrint {
				fmt.Printf("      ... và %d logs khác\n", len(res.Logs)-maxPrint)
				break
			}
			topicStr := strings.Join(l.Topics, ", ")
			fmt.Printf("      [%d] addr=%s topics=%s logIdx=%s\n",
				i, l.Address, truncate(topicStr, 66), l.LogIndex)
		}
	}
	fmt.Println()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ===== CSV export =====

func writeMismatchCSV(filename string, nodes []nodeInfo, mismatches []blockMismatch) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.Name
	}
	sort.Strings(names)

	header := "block_number"
	for _, name := range names {
		header += "," + name + "_log_count," + name + "_error"
	}
	fmt.Fprintln(f, header)

	for _, m := range mismatches {
		line := fmt.Sprintf("%d", m.BlockNumber)
		for _, name := range names {
			res, ok := m.NodeResults[name]
			if !ok {
				line += ",,"
			} else if res.isError() {
				line += ",," + res.Error
			} else {
				line += fmt.Sprintf(",%d,", len(res.Logs))
			}
		}
		fmt.Fprintln(f, line)
	}
	return nil
}

// ===== Watch mode =====

const alertFile = "logs_mismatch_alert.log"

func runWatch(
	client *http.Client,
	nodes []nodeInfo,
	interval time.Duration,
	checkLast int,
	addr string,
	topicFilter [][]string,
) {
	fmt.Printf("👁️  WATCH MODE — kiểm tra logs %d blocks gần nhất mỗi %v\n", checkLast, interval)
	fmt.Println("   Nhấn Ctrl+C để dừng")
	fmt.Printf("   🛑 Tự động DỪNG khi phát hiện lệch logs (ghi vào %s)\n", alertFile)
	fmt.Println()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	totalChecks := 0
	totalMismatches := 0

	if watchOnce(client, nodes, checkLast, addr, topicFilter, &totalChecks, &totalMismatches) {
		printWatchStop(totalChecks, totalMismatches)
		os.Exit(1)
	}

	for {
		select {
		case <-ticker.C:
			if watchOnce(client, nodes, checkLast, addr, topicFilter, &totalChecks, &totalMismatches) {
				printWatchStop(totalChecks, totalMismatches)
				os.Exit(1)
			}
		case sig := <-sigCh:
			fmt.Printf("\n\n🛑 Nhận signal %v — dừng watch mode\n", sig)
			fmt.Printf("📊 Tổng kết: %d lần check, %d lệch phát hiện\n", totalChecks, totalMismatches)
			return
		}
	}
}

func printWatchStop(checks, mismatches int) {
	fmt.Printf("\n🛑 DỪNG WATCH MODE: Phát hiện lệch logs! Chi tiết đã ghi vào %s\n", alertFile)
	fmt.Printf("📊 Tổng kết: %d lần check, %d lệch phát hiện\n", checks, mismatches)
}

// watchOnce returns true if a mismatch is detected (caller should stop).
func watchOnce(
	client *http.Client,
	nodes []nodeInfo,
	checkLast int,
	addr string,
	topicFilter [][]string,
	totalChecks, totalMismatches *int,
) bool {
	*totalChecks++
	now := time.Now().Format("15:04:05")

	type nodeBlock struct {
		name  string
		block uint64
		err   error
	}
	var nodeBlocks []nodeBlock
	var minBlock, maxBlock uint64
	minBlock = ^uint64(0)

	for _, n := range nodes {
		num, err := getLatestBlockNumber(client, n.URL)
		nodeBlocks = append(nodeBlocks, nodeBlock{name: n.Name, block: num, err: err})
		if err == nil {
			if num < minBlock {
				minBlock = num
			}
			if num > maxBlock {
				maxBlock = num
			}
		}
	}

	// Print heights
	fmt.Printf("[%s] #%d ", now, *totalChecks)
	var parts []string
	for _, r := range nodeBlocks {
		if r.err != nil {
			parts = append(parts, fmt.Sprintf("%s=ERR", r.name))
		} else {
			parts = append(parts, fmt.Sprintf("%s=%d", r.name, r.block))
		}
	}
	fmt.Printf("Heights: %s", strings.Join(parts, "  "))

	if maxBlock > 0 && minBlock < ^uint64(0) && maxBlock-minBlock > 10 {
		fmt.Printf(" ⚠️ CHÊNH %d blocks!", maxBlock-minBlock)
	}

	// Count responding nodes
	responding := 0
	for _, r := range nodeBlocks {
		if r.err == nil {
			responding++
		}
	}

	if minBlock == ^uint64(0) {
		fmt.Println(" ❌ tất cả node lỗi")
		return false
	}
	if responding < 2 {
		fmt.Printf(" ⚠️ chỉ %d/%d node phản hồi — KHÔNG THỂ SO SÁNH\n", responding, len(nodes))
		return false
	}

	from := uint64(1)
	if minBlock > uint64(checkLast) {
		from = minBlock - uint64(checkLast) + 1
	}

	mismatches, matched, _ := checkBatch(client, nodes, from, minBlock, addr, topicFilter)
	if len(mismatches) == 0 {
		fmt.Printf(" ✅ logs khớp %d blocks (block %d→%d)\n", matched, from, minBlock)
		return false
	}

	// --- Mismatch: build alert ---
	*totalMismatches += len(mismatches)

	var buf strings.Builder
	buf.WriteString("╔══════════════════════════════════════════════════════════════════╗\n")
	buf.WriteString(fmt.Sprintf("║  🚨 LOGS MISMATCH DETECTED — %s                     ║\n",
		time.Now().Format("2006-01-02 15:04:05")))
	buf.WriteString("╚══════════════════════════════════════════════════════════════════╝\n")
	buf.WriteString(fmt.Sprintf("\nCheck #%d | Blocks checked: %d→%d | Mismatches: %d\n",
		*totalChecks, from, minBlock, len(mismatches)))
	buf.WriteString("\nNode Heights:\n")
	for _, r := range nodeBlocks {
		if r.err != nil {
			buf.WriteString(fmt.Sprintf("  %-12s ERR: %v\n", r.name+":", r.err))
		} else {
			buf.WriteString(fmt.Sprintf("  %-12s block=%d\n", r.name+":", r.block))
		}
	}
	buf.WriteString("\n─── Mismatch Details ───\n")

	for _, m := range mismatches {
		buf.WriteString(fmt.Sprintf("\n⚠️  Block %d:\n", m.BlockNumber))
		for _, n := range nodes {
			res, ok := m.NodeResults[n.Name]
			if !ok {
				buf.WriteString(fmt.Sprintf("   %-12s (no data)\n", n.Name+":"))
				continue
			}
			if res.isError() {
				buf.WriteString(fmt.Sprintf("   %-12s %s\n", n.Name+":", res.Error))
				continue
			}
			buf.WriteString(fmt.Sprintf("   %-12s %d logs\n", n.Name+":", len(res.Logs)))
			for i, l := range res.Logs {
				if i >= 5 {
					buf.WriteString(fmt.Sprintf("      ... và %d logs khác\n", len(res.Logs)-5))
					break
				}
				buf.WriteString(fmt.Sprintf("      [%d] txHash=%s logIdx=%s addr=%s\n",
					i, l.TransactionHash, l.LogIndex, l.Address))
			}
		}
	}

	buf.WriteString("\n─── Summary ───\n")
	buf.WriteString(fmt.Sprintf("Total mismatches: %d\n", *totalMismatches))
	buf.WriteString(fmt.Sprintf("Detected at: %s\n", time.Now().Format("2006-01-02 15:04:05.000")))

	content := buf.String()
	fmt.Printf("\n")
	fmt.Print(content)

	if err := os.WriteFile(alertFile, []byte(content), 0644); err != nil {
		fmt.Printf("⚠️  Không thể ghi %s: %v\n", alertFile, err)
	} else {
		fmt.Printf("\n📄 Chi tiết đã ghi vào: %s\n", alertFile)
	}

	csvFile := fmt.Sprintf("logs_mismatches_%d_%d.csv", from, minBlock)
	if err := writeMismatchCSV(csvFile, nodes, mismatches); err != nil {
		fmt.Printf("⚠️  Không thể ghi CSV: %v\n", err)
	} else {
		fmt.Printf("📄 CSV chi tiết: %s\n", csvFile)
	}

	return true
}
