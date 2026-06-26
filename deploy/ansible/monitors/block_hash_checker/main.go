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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"block_hash_checker/logger"
)

// ===== JSON-RPC types =====

var ghostMutex sync.Mutex
var loggedBlocks = make(map[uint64]bool)

// noStopFlag disables writing /tmp/MTN_CHAIN_ERROR_STOP and os.Exit on mismatch.
// Enabled via --no-stop-flag CLI flag. Useful for passive observation without
// halting the CI pipeline or sending Telegram alerts.
var noStopFlag bool
var daemonMode bool

func clearLoggedBlocks() {
	ghostMutex.Lock()
	defer ghostMutex.Unlock()
	loggedBlocks = make(map[uint64]bool)
	os.Truncate("ghost_blocks.log", 0)
}

func loadEnvFallback() {
	paths := []string{
		"../../.env",
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					k := strings.TrimSpace(parts[0])
					v := strings.TrimSpace(parts[1])
					v = strings.Trim(v, `"'`)
					if os.Getenv(k) == "" {
						os.Setenv(k, v)
					}
				}
			}
		}
	}
}

func init() {
	loadEnvFallback()
	if os.Getenv("TELEGRAM_BOT_TOKEN") == "" {
		fmt.Println("❌ LỖI NGHIÊM TRỌNG: Không tìm thấy TELEGRAM_BOT_TOKEN! Vui lòng tạo file .env hoặc cấu hình biến môi trường trước khi chạy.")
		os.Exit(1)
	}
	loadLoggedBlocks()
}

func loadLoggedBlocks() {
	ghostMutex.Lock()
	defer ghostMutex.Unlock()

	data, err := os.ReadFile("ghost_blocks.log")
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.Contains(line, "blockNumber=") {
			parts := strings.Split(line, "blockNumber=")
			if len(parts) > 1 {
				numStr := strings.Split(parts[1], " ")[0]
				var num uint64
				fmt.Sscanf(numStr, "%d", &num)
				if num > 0 {
					loggedBlocks[num] = true
				}
			}
		} else if strings.Contains(line, "Ghost block detected: ") {
			parts := strings.Split(line, "Ghost block detected: ")
			if len(parts) > 1 {
				numStr := strings.TrimSpace(parts[1])
				var num uint64
				fmt.Sscanf(numStr, "%d", &num)
				if num > 0 {
					loggedBlocks[num] = true
				}
			}
		}
	}
}

func logBlockEvent(eventType string, blockNum uint64, gei string) {
	ghostMutex.Lock()
	defer ghostMutex.Unlock()

	if loggedBlocks[blockNum] {
		return // Avoid duplicates
	}
	loggedBlocks[blockNum] = true

	geiStr := "unknown"
	if gei != "" {
		geiStr = fmt.Sprintf("%d", parseHexStr(gei))
	}

	// Ghi vào file
	f, err := os.OpenFile("ghost_blocks.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	now := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf("[%s] %s DETECTED: blockNumber=%d gei=%s\n", now, eventType, blockNum, geiStr)
	f.WriteString(msg)

	// In ra terminal hiện tại chỉ khi là NIL_BLOCK
	if strings.Contains(eventType, "NIL_BLOCK") {
		logger.Info("\n🚨 " + msg)
		sendTelegramAlertDirect(fmt.Sprintf("🚨 *[CẢNH BÁO]* %s", msg), false)
	}
}

// logAnomaly logs chain health anomalies to chain_anomalies.log and prints to terminal.
func logAnomaly(anomalyType string, blockNum uint64, detail string) {
	now := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf("[%s] %s: blockNumber=%d %s\n", now, anomalyType, blockNum, detail)

	// Ghi vào file
	f, err := os.OpenFile("chain_anomalies.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.WriteString(msg)
		f.Close()
	}

	// In ra terminal
	logger.Info("\n🚨 " + msg)

	alertContent := fmt.Sprintf("🚨 *CẢNH BÁO ANOMALY: %s*\n• *Block:* #%d\n• *Chi tiết:* %s", anomalyType, blockNum, detail)
	if anomalyType == "NODE_LAGGING" {
		// Chỉ gửi cảnh báo tới Telegram, KHÔNG dừng test và polling
		sendTelegramAlertDirect(alertContent, false)
	} else {
		// Lỗi nghiêm trọng, dừng test và polling
		triggerStopFlag(alertContent)
	}
}

func triggerStopFlag(reason string) {
	if noStopFlag {
		logger.Info("\n🔕 [--no-stop-flag] Bỏ qua ghi cờ dừng (/tmp/MTN_CHAIN_ERROR_STOP)")
		return
	}
	err := os.WriteFile("/tmp/MTN_CHAIN_ERROR_STOP", []byte(reason), 0644)
	if err == nil {
		logger.Info("\n🛑 ĐÃ KÍCH HOẠT CỜ DỪNG AUTO_TEST (/tmp/MTN_CHAIN_ERROR_STOP)")
	}
	sendTelegramAlertDirect(reason, true)
}

func getSystemIPInfo() string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "Unknown"
	}

	var localIPs []string
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					localIPs = append(localIPs, ipnet.IP.String())
				}
			}
		}
	}
	localIPStr := "Unknown"
	if len(localIPs) > 0 {
		localIPStr = strings.Join(localIPs, ", ")
	}

	publicIP := "Unknown"
	client := http.Client{
		Timeout: 2 * time.Second,
	}
	resp, err := client.Get("https://api.ipify.org")
	if err == nil {
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err == nil {
			publicIP = strings.TrimSpace(string(body))
		}
	}

	if publicIP != "Unknown" {
		return fmt.Sprintf("%s (Static/Private IP: %s, Public IP: %s)", hostname, localIPStr, publicIP)
	}
	return fmt.Sprintf("%s (Static/Private IP: %s)", hostname, localIPStr)
}

func sendTelegramAlertDirect(message string, isCritical bool) {
	if noStopFlag {
		return
	}
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	if chatID == "" {
		chatID = "-1003867050625"
	}
	ipInfo := getSystemIPInfo()

	// Fix Telegram Markdown parsing error (status 400) when message contains unbalanced underscores like "NIL_BLOCK"
	safeMessage := strings.ReplaceAll(message, "_", "\\_")

	var header string
	if isCritical {
		header = "🔴 *[CRITICAL ERROR - DỪNG TEST & POLLING]*"
	} else {
		header = "⚠️ *[WARNING - KHÔNG DỪNG TEST & POLLING]*"
	}

	fullMessage := fmt.Sprintf("%s\n\n*Server:* `%s`\n\n%s", header, ipInfo, safeMessage)

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]string{
		"chat_id":    chatID,
		"text":       fullMessage,
		"parse_mode": "Markdown",
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		fmt.Printf("⚠️ Lỗi gửi Telegram alert: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Printf("⚠️ Lỗi gửi Telegram alert, status code: %d\n", resp.StatusCode)
		if resp.StatusCode == 400 {
			// Fallback: Gửi lại dạng raw text (bỏ parse_mode)
			fmt.Printf("🔄 Thử gửi lại bằng raw text...\n")
			delete(payload, "parse_mode")
			body, _ = json.Marshal(payload)
			fallbackResp, err := http.Post(url, "application/json", strings.NewReader(string(body)))
			if err == nil {
				defer fallbackResp.Body.Close()
				if fallbackResp.StatusCode == 200 {
					fmt.Printf("✅ Đã gửi thành công Telegram (fallback raw text)!\n")
				} else {
					fmt.Printf("⚠️ Vẫn lỗi khi gửi raw text, status code: %d\n", fallbackResp.StatusCode)
				}
			}
		}
	}
}

func logSystemError(nodeName string, blockNum uint64, errMsg string) {
	now := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf("[%s] 🚨 [SYSTEM ERROR] Lỗi hệ thống: Node %s getBlockByNumber(%d) trả về null hoặc lỗi: %s\n", now, nodeName, blockNum, errMsg)

	// Ghi vào file log
	f, err := os.OpenFile("system_errors.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.WriteString(msg)
		f.Close()
	}

	// In ra terminal hiện tại
	logger.Info("\n" + msg)

	// Nếu tool bị chạy ngầm và giấu log (như trong auto_test.sh dùng > log_file),
	// thì ép in thêm một bản thẳng ra màn hình để báo động!
	if stat, _ := os.Stdout.Stat(); (stat.Mode() & os.ModeCharDevice) == 0 {
		if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
			tty.WriteString("\n[TỪ BACKGROUND PROCESS] " + msg)
			tty.Close()
		}
	}
}

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
	Hash               string         `json:"hash"`
	Number             string         `json:"number"`
	ParentHash         string         `json:"parentHash"`
	StateRoot          string         `json:"stateRoot"`
	TransactionsRoot   string         `json:"transactionsRoot"`
	ReceiptsRoot       string         `json:"receiptsRoot"`
	Timestamp          string         `json:"timestamp"`
	Miner              string         `json:"miner"`
	GlobalExecIndex    string         `json:"globalExecIndex"`
	Epoch              string         `json:"epoch"`
	StakeStatesRoot    string         `json:"stakeStatesRoot"`
	AggregateSignature string         `json:"aggregateSignature"`
	CommitIndex        string         `json:"commitIndex"`
	Transactions       []txOrderEntry `json:"transactions"`
}

// ===== Block info (parsed from blockResult) =====

type blockInfo struct {
	Hash               string
	ParentHash         string
	StateRoot          string
	TransactionsRoot   string
	ReceiptsRoot       string
	Timestamp          string
	Miner              string
	GlobalExecIndex    string
	Epoch              string
	StakeStatesRoot    string
	AggregateSignature string
	CommitIndex        string
	TxCount            int
	SysTxCount         int
	Error              string // non-empty if fetch failed
	TxOrder            []txOrderEntry
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
	configFlag := flag.String("config", "config.json", "Đường dẫn file cấu hình JSON")
	fromBlock := flag.Uint64("from", 1, "Block bắt đầu kiểm tra")
	toBlock := flag.Uint64("to", 0, "Block kết thúc (0 = lấy block mới nhất)")
	batchSize := flag.Int("batch", 10, "Số block kiểm tra song song mỗi lần")
	timeout := flag.Duration("timeout", 5*time.Second, "Timeout cho mỗi RPC call")
	watchMode := flag.Bool("watch", true, "Chế độ giám sát liên tục — kiểm tra block mới nhất định kỳ")
	watchInterval := flag.Duration("interval", 2*time.Second, "Khoảng thời gian giữa mỗi lần check (watch mode)")
	lagThreshold := flag.Int("lag-threshold", 1000, "Ngưỡng chênh lệch block để kích hoạt cảnh báo NODE_LAGGING (0 = disable)")
	noStop := flag.Bool("no-stop-flag", false, "Không ghi /tmp/MTN_CHAIN_ERROR_STOP và không os.Exit khi phát hiện lệch hash (chỉ log, không báo Telegram/CI)")
	daemon := flag.Bool("daemon", false, "Chạy dưới dạng daemon (không tự động tắt khi tiến trình cha thoát)")
	flag.Parse()

	// Apply global flag
	noStopFlag = *noStop
	daemonMode = *daemon

	if *nodesFlag == "" {
		configData, err := os.ReadFile(*configFlag)
		if err == nil {
			var config struct {
				NodesRaw json.RawMessage `json:"nodes"`
			}
			if err := json.Unmarshal(configData, &config); err == nil && len(config.NodesRaw) > 0 {
				var nodesStr string
				if err := json.Unmarshal(config.NodesRaw, &nodesStr); err == nil {
					*nodesFlag = nodesStr
				} else {
					var nodesMap map[string]string
					if err := json.Unmarshal(config.NodesRaw, &nodesMap); err == nil {
						var keys []string
						for k := range nodesMap {
							keys = append(keys, k)
						}
						sort.Strings(keys)
						var parts []string
						for _, k := range keys {
							parts = append(parts, fmt.Sprintf("%s=%s", k, nodesMap[k]))
						}
						*nodesFlag = strings.Join(parts, ",")
					}
				}
				if *nodesFlag != "" {
					fmt.Printf("📂 Load nodes từ %s (%d nodes)\n", *configFlag, len(strings.Split(*nodesFlag, ",")))
				}
			}
		}

		if *nodesFlag == "" {
			fmt.Printf("❌ Thiếu --nodes flag và không tìm thấy cấu hình hợp lệ trong %s\n", *configFlag)
			fmt.Println()
			fmt.Println("Cách dùng:")
			fmt.Println(`  # Quét 1 lần:`)
			fmt.Println(`  ./block_hash_checker --nodes "master=http://localhost:8747,node4=http://localhost:10748" --from 1 --to 5000`)
			fmt.Println()
			fmt.Println(`  # Giám sát liên tục:`)
			fmt.Println(`  ./block_hash_checker --watch --nodes "master=http://localhost:8747,node4=http://localhost:10748" --interval 10s`)
			os.Exit(1)
		}
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
		runWatch(client, nodes, *watchInterval, *lagThreshold)
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

		batchMismatches, batchMatches, batchErrors, batchSkips, _, _ := checkBatch(client, nodes, batchStart, batchEnd)
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

		// Query backward via Binary Search to find the true first mismatch block
		firstMismatchInBatch := allMismatches[0].BlockNumber
		fmt.Printf("\n🔍 Phát hiện lệch hash tại block %d. Đang truy vấn lùi bằng Binary Search để tìm block lệch đầu tiên...\n", firstMismatchInBatch)

		realFirstMismatch := firstMismatchInBatch
		low := uint64(1)
		high := firstMismatchInBatch - 1

		for low <= high {
			mid := low + (high-low)/2
			m, _, _, _, _, _ := checkBatch(client, nodes, mid, mid)
			if len(m) > 0 {
				realFirstMismatch = mid
				high = mid - 1
			} else {
				low = mid + 1
			}
		}

		if realFirstMismatch < firstMismatchInBatch {
			fmt.Printf("🎯 Đã tìm thấy block lệch đầu tiên thực sự tại: %d\n", realFirstMismatch)
			m, _, _, _, _, _ := checkBatch(client, nodes, realFirstMismatch, realFirstMismatch)
			if len(m) > 0 {
				allMismatches = append(m, allMismatches...)
			}
		} else {
			fmt.Printf("🎯 Block %d chính là block lệch đầu tiên trên chuỗi.\n", realFirstMismatch)
		}

		// Trigger the verified first mismatch stop flag for Telegram!
		stopFlagMsg := triggerStopFlagForFirstMismatch(client, nodes, realFirstMismatch)
		fmt.Println(stopFlagMsg)

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

// prevBlockState tracks sequential state for anomaly detection across consecutive blocks.
type prevBlockState struct {
	Timestamp         uint64
	GEI               uint64
	Epoch             uint64
	StateRoot         string
	StateRootStreak   int // number of consecutive blocks with same stateRoot
	BlockNum          uint64
	IsNil             bool
	ConsecutiveErrors int // Track consecutive errors to detect node crashes
}

func checkBatch(client *http.Client, nodes []nodeInfo, from, to uint64) (mismatches []mismatch, matchCount, errorCount, skipCount uint64, nilBlocks []uint64, emptyBlocks []uint64) {
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
				if err == nil && bi.Error == "(block không tồn tại)" {
					latest, errLatest := getLatestBlockNumber(client, node.URL)
					if errLatest == nil && blockNum <= latest {
						logger.Info("⏳ Detected transient write lag on node %s for block %d (latest: %d). Retrying...", node.Name, blockNum, latest)
						for retries := 1; retries <= 10; retries++ {
							time.Sleep(500 * time.Millisecond)
							bi, err = getBlockInfo(client, node.URL, blockNum)
							if err == nil && !bi.IsError() {
								logger.Info("✅ Node %s caught up on block %d after %d retries", node.Name, blockNum, retries)
								break
							}
						}
					}
				}
				if err != nil || (bi.IsError() && bi.Error != "(block không tồn tại)") {
					isPruned := false
					if err != nil && strings.Contains(err.Error(), "pruned") {
						isPruned = true
					}
					if bi.IsError() && strings.Contains(bi.Error, "pruned") {
						isPruned = true
					}
					if !isPruned {
						// Retry logic: allow lagging nodes to catch up on async LevelDB writes
						for retries := 1; retries <= 3; retries++ {
							time.Sleep(500 * time.Millisecond)
							bi, err = getBlockInfo(client, node.URL, blockNum)
							if err == nil && (!bi.IsError() || bi.Error == "(block không tồn tại)") {
								break
							}
						}
					}
				}

				if err != nil {
					blocks[node.Name] = blockInfo{Error: fmt.Sprintf("ERROR: %v", err)}
					hasErr = true
				} else {
					blocks[node.Name] = bi
					if bi.IsError() {
						hasErr = true
					}
				}
			}

			results[idx] = result{blockNum: blockNum, blocks: blocks, hasError: hasErr}
		}(i)
	}
	wg.Wait()

	// Build a map of block hash by node name for chain integrity check (parentHash verification)
	// prevBlockHashes[nodeName] = hash of previous block on that node
	prevBlockHashes := make(map[string]string)

	// Track sequential state per node for anomaly detection
	prevState := make(map[string]*prevBlockState)

	for _, r := range results {
		if r.hasError {
			errorCount++
		}

		// === CHECK 1: Compare all fields across nodes ===
		var validBlocks []blockInfo
		var validNames []string
		missingResponseCount := 0
		prunedResponseCount := 0

		for _, node := range nodes {
			bi := r.blocks[node.Name]
			if !bi.IsError() {
				validBlocks = append(validBlocks, bi)
				validNames = append(validNames, node.Name)
			} else {
				if bi.Error == "(block không tồn tại)" {
					missingResponseCount++
				} else if strings.Contains(bi.Error, "pruned") {
					prunedResponseCount++
				}
			}
		}

		if len(validBlocks) < 2 {
			// Nếu tất cả các node phản hồi đều báo block không tồn tại, thì coi là ghost block và bỏ qua.
			// (Không cần đợi các node đang lỗi/sập phản hồi)
			if len(validBlocks) == 0 && missingResponseCount > 0 && prunedResponseCount == 0 {
				nilBlocks = append(nilBlocks, r.blockNum)
				logBlockEvent("NIL_BLOCK", r.blockNum, "")
				// Crucial: delete from prevBlockHashes for all nodes since this block is a gap/nil block
				for _, node := range nodes {
					delete(prevBlockHashes, node.Name)
				}
				continue
			}

			skipCount++
			// Still update prevBlockHashes for chain integrity
			for _, node := range nodes {
				bi := r.blocks[node.Name]
				if !bi.IsError() {
					prevBlockHashes[node.Name] = bi.Hash
				} else {
					delete(prevBlockHashes, node.Name)
				}
			}
			continue
		}

		// Compare hash, parentHash, stateRoot, txRoot, receiptsRoot across all valid nodes
		mismatchedFields := make(map[string]bool)
		ref := validBlocks[0]

		if ref.TxCount == 0 && ref.SysTxCount == 0 {
			emptyBlocks = append(emptyBlocks, r.blockNum)
			logBlockEvent("EMPTY_BLOCK", r.blockNum, ref.GlobalExecIndex)
		}

		for i := 1; i < len(validBlocks); i++ {
			b := validBlocks[i]
			if b.Hash != ref.Hash {
				mismatchedFields["hash"] = true
			}
			if b.ParentHash != ref.ParentHash {
				mismatchedFields["parentHash"] = true
			}
			if b.StateRoot != ref.StateRoot {
				mismatchedFields["stateRoot"] = true
			}
			if b.StakeStatesRoot != ref.StakeStatesRoot {
				mismatchedFields["stakeStatesRoot"] = true
			}
			if b.TransactionsRoot != ref.TransactionsRoot {
				mismatchedFields["transactionsRoot"] = true
			}
			if b.ReceiptsRoot != ref.ReceiptsRoot {
				mismatchedFields["receiptsRoot"] = true
			}
			if b.Timestamp != ref.Timestamp {
				mismatchedFields["timestamp"] = true
			}
			if b.Miner != ref.Miner {
				mismatchedFields["miner"] = true
			}
			if b.Epoch != ref.Epoch {
				mismatchedFields["epoch"] = true
			}
			if b.GlobalExecIndex != ref.GlobalExecIndex {
				mismatchedFields["globalExecIndex"] = true
			}
			if b.CommitIndex != ref.CommitIndex {
				mismatchedFields["commitIndex"] = true
			}

			if len(b.TxOrder) != len(ref.TxOrder) {
				mismatchedFields["txCount"] = true
			} else {
				for j := range ref.TxOrder {
					if b.TxOrder[j].Hash != ref.TxOrder[j].Hash ||
						b.TxOrder[j].GroupID != ref.TxOrder[j].GroupID ||
						b.TxOrder[j].TransactionIndex != ref.TxOrder[j].TransactionIndex {
						mismatchedFields["txOrder"] = true
						break
					}
				}
			}
			// if b.AggregateSignature != ref.AggregateSignature {
			// 	mismatchedFields["aggregateSignature"] = true
			// }
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
				mismatchedFields["chain_broken"] = true
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

		hasMismatch := len(mismatchedFields) > 0
		if hasMismatch {
			mismatches = append(mismatches, mismatch{BlockNumber: r.blockNum, Blocks: r.blocks})
			// Sort mismatched fields for deterministic output
			var fields []string
			for f := range mismatchedFields {
				fields = append(fields, f)
			}
			sort.Strings(fields)

			// Build a compact, beautifully formatted Telegram message for the stop flag
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("🚨 *LỆCH BLOCK #%d*\n", r.blockNum))
			sb.WriteString(fmt.Sprintf("• ⚠️ *Trường bị lệch:* `[%s]`\n", strings.Join(fields, ", ")))
			sb.WriteString("• *Chi tiết các node:*\n")

			for _, node := range nodes {
				bi := r.blocks[node.Name]
				if bi.IsError() {
					sb.WriteString(fmt.Sprintf("  - *%s*: `ERROR: %s`\n", node.Name, bi.Error))
				} else {
					var fieldVals []string
					for _, f := range fields {
						val := ""
						switch f {
						case "hash":
							val = bi.Hash
						case "parentHash":
							val = bi.ParentHash
						case "stateRoot":
							val = bi.StateRoot
						case "stakeStatesRoot":
							val = bi.StakeStatesRoot
						case "transactionsRoot":
							val = bi.TransactionsRoot
						case "receiptsRoot":
							val = bi.ReceiptsRoot
						case "timestamp":
							val = fmt.Sprintf("%s(%d)", bi.Timestamp, parseHexStr(bi.Timestamp))
						case "miner":
							val = bi.Miner
						case "epoch":
							val = fmt.Sprintf("%d", parseHexStr(bi.Epoch))
						case "globalExecIndex":
							val = fmt.Sprintf("%d", parseHexStr(bi.GlobalExecIndex))
						case "commitIndex":
							val = fmt.Sprintf("%d", parseHexStr(bi.CommitIndex))
						case "chain_broken":
							val = fmt.Sprintf("parentHash=%s", bi.ParentHash)
						}
						// Shorten long hashes for elegant display
						if len(val) > 16 && strings.HasPrefix(val, "0x") {
							val = val[:8] + "..." + val[len(val)-4:]
						}
						fieldVals = append(fieldVals, fmt.Sprintf("%s: `%s`", f, val))
					}

					// Add context fields
					geiVal := parseHexStr(bi.GlobalExecIndex)
					epochVal := parseHexStr(bi.Epoch)

					sb.WriteString(fmt.Sprintf("  - *%s*: %s (gei=%d, epoch=%d)\n",
						node.Name, strings.Join(fieldVals, ", "), geiVal, epochVal))
				}
			}

			// Check TX execution order across nodes — only when hash mismatch is detected.
			// This tells us whether the divergence is caused by different TX ordering.
			txOrderSummary := checkTxOrderMismatch(nodes, r.blockNum, r.blocks)
			sb.WriteString(txOrderSummary + "\n")

			// Deferred stop flag trigger to watchOnce/main after backward binary search resolves the true first mismatch block.
		} else {
			matchCount++
		}

		// === CHECK 3–7: Sequential anomaly detection (per node) ===
		for _, node := range nodes {
			bi := r.blocks[node.Name]
			if bi.IsError() {
				errCount := 1
				if prev, ok := prevState[node.Name]; ok {
					if bi.Error != "(block không tồn tại)" {
						errCount = prev.ConsecutiveErrors + 1
					} else {
						errCount = 0 // Reset error count if node is responding but lagging
					}
				} else if bi.Error == "(block không tồn tại)" {
					errCount = 0
				}

				// if errCount == 10 { // Alert after 10 consecutive real errors (e.g. connection refused)
				// 	logAnomaly("NODE_DOWN", r.blockNum,
				// 		fmt.Sprintf("node=%s KHÔNG PHẢN HỒI (Mất kết nối RPC hoặc Node đã sập) liên tục 10 blocks! Lỗi: %s",
				// 			node.Name, bi.Error))
				// }
				prevState[node.Name] = &prevBlockState{BlockNum: r.blockNum, IsNil: true, ConsecutiveErrors: errCount}
				continue
			}

			curTS := parseHexStr(bi.Timestamp)
			curGEI := parseHexStr(bi.GlobalExecIndex)
			curEpoch := parseHexStr(bi.Epoch)
			curStateRoot := bi.StateRoot
			curTxCount := bi.TxCount + bi.SysTxCount

			if prev, ok := prevState[node.Name]; ok && !prev.IsNil {
				// CHECK 1: Timestamp regression
				if curTS > 0 && prev.Timestamp > 0 && curTS < prev.Timestamp {
					regression := prev.Timestamp - curTS
					if regression > 30 {
						logAnomaly("TIMESTAMP_REGRESSION", r.blockNum,
							fmt.Sprintf("node=%s ts=%d < prev_ts=%d (-%ds — stale DAG commit!)",
								node.Name, curTS, prev.Timestamp, regression))
					}
				}

				// CHECK 2: GEI regression or duplicate
				if curGEI > 0 && prev.GEI > 0 && curGEI <= prev.GEI {
					logAnomaly("GEI_REGRESSION", r.blockNum,
						fmt.Sprintf("node=%s GEI=%d <= prev_GEI=%d (Ghost block or duplicate!)",
							node.Name, curGEI, prev.GEI))
				}

				// CHECK 3: Epoch inconsistency (epoch must never decrease and must not jump/skip)
				if curEpoch > 0 && prev.Epoch > 0 {
					if curEpoch < prev.Epoch {
						logAnomaly("EPOCH_REGRESSION", r.blockNum,
							fmt.Sprintf("node=%s epoch=%d < prev_epoch=%d (CRITICAL: epoch went backward!)",
								node.Name, curEpoch, prev.Epoch))
					} else if curEpoch > prev.Epoch+1 {
						logAnomaly("EPOCH_JUMP", r.blockNum,
							fmt.Sprintf("node=%s epoch=%d jumped from prev_epoch=%d (CRITICAL: epoch skipped!)",
								node.Name, curEpoch, prev.Epoch))
					}
				}

				// CHECK 5: StateRoot freeze (same stateRoot across 5+ consecutive non-epoch-boundary blocks with txs)
				if curStateRoot == prev.StateRoot && curEpoch == prev.Epoch {
					newStreak := prev.StateRootStreak + 1
					// LƯU Ý: Chỉ bắt lỗi nếu block thực sự có giao dịch người dùng (bi.TxCount > 0).
					// Bỏ qua giao dịch hệ thống vì nó có thể không làm thay đổi account state.
					if newStreak >= 5 && bi.TxCount > 0 {
						// Fetch receipt details for this frozen block to diagnose WHY stateRoot didn't change
						receiptSummary := fetchReceiptSummary(client, node.URL, r.blockNum)
						detailStr := fmt.Sprintf("\n"+
							"   [VẤN ĐỀ NGHIÊM TRỌNG TRÊN NODE %s]\n"+
							"   - Block này có tổng cộng %d giao dịch (Giao dịch thường: %d, Giao dịch hệ thống: %d).\n"+
							"   - Kết quả chạy: %s\n"+
							"   - TUY NHIÊN, StateRoot (%s...) KHÔNG HỀ THAY ĐỔI trong %d block liên tiếp.\n"+
							"   => KẾT LUẬN: Giao dịch báo thành công (tăng nonce, thu phí) nhưng node đã GẶP LỖI không cập nhật trạng thái (không commit NOMT Trie) vào Database!",
							node.Name, curTxCount, bi.TxCount, bi.SysTxCount, receiptSummary, safePrefix(curStateRoot, 10), newStreak)
						logAnomaly("STATEROOT_FREEZE_LỖI_KHÔNG_LƯU_STATE", r.blockNum, detailStr)
					}
					prevState[node.Name] = &prevBlockState{
						Timestamp: curTS, GEI: curGEI, Epoch: curEpoch,
						StateRoot: curStateRoot, StateRootStreak: newStreak,
						BlockNum: r.blockNum,
					}
				} else {
					prevState[node.Name] = &prevBlockState{
						Timestamp: curTS, GEI: curGEI, Epoch: curEpoch,
						StateRoot: curStateRoot, StateRootStreak: 0,
						BlockNum: r.blockNum,
					}
				}
			} else {
				prevState[node.Name] = &prevBlockState{
					Timestamp: curTS, GEI: curGEI, Epoch: curEpoch,
					StateRoot: curStateRoot, StateRootStreak: 0,
					BlockNum: r.blockNum,
				}
			}
		}
	}

	return
}

// safePrefix returns the first n chars of a string safely.
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// (hash comparison is now inline in checkBatch — compares all fields + chain integrity)

// ===== JSON-RPC calls =====

func getSystemTxsCount(client *http.Client, url string, hexBlock string) (int, error) {
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  "eth_getSystemTransactionsByBlockNumber",
		Params:  []interface{}{hexBlock},
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
		return 0, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	if string(rpcResp.Result) == "null" {
		return 0, nil
	}

	var txs []interface{}
	if err := json.Unmarshal(rpcResp.Result, &txs); err != nil {
		return 0, fmt.Errorf("cannot parse system txs: %v", err)
	}

	return len(txs), nil
}

// ===== TX Order Verification =====

// txOrderEntry captures the execution-order metadata for one transaction in a block.
type txOrderEntry struct {
	Hash             string // tx hash
	GroupID          string // groupId stamped by tx_processor (field "groupId" in RPC response)
	TransactionIndex string // transactionIndex in block (sequential across groups)
}

// checkTxOrderMismatch compares the tx execution order across all nodes for one block.
// Returns a human-readable summary: "✅ TX order identical" or a diff table.
func checkTxOrderMismatch(nodes []nodeInfo, blockNum uint64, blocks map[string]blockInfo) string {
	refIdx := -1
	for i, n := range nodes {
		if !blocks[n.Name].IsError() && len(blocks[n.Name].TxOrder) > 0 {
			refIdx = i
			break
		}
	}
	if refIdx < 0 {
		return "  🔍 *TX Order:* không thể fetch (các nodes error hoặc block rỗng)"
	}

	refName := nodes[refIdx].Name
	ref := blocks[refName]
	allMatch := true
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("  🔍 *TX Order Check* (block #%d, %d txs trên %s):\n", blockNum, len(ref.TxOrder), refName))

	for _, n := range nodes {
		b := blocks[n.Name]
		if b.IsError() {
			sb.WriteString(fmt.Sprintf("    ❌ %s: fetch error: %v\n", n.Name, b.Error))
			allMatch = false
			continue
		}
		if len(b.TxOrder) != len(ref.TxOrder) {
			sb.WriteString(fmt.Sprintf("    ❌ %s: %d txs (ref=%s: %d txs) — COUNT DIFF!\n",
				n.Name, len(b.TxOrder), refName, len(ref.TxOrder)))
			allMatch = false
			continue
		}
		// Compare entry by entry
		diffs := 0
		for i := range ref.TxOrder {
			refE := ref.TxOrder[i]
			curE := b.TxOrder[i]
			if refE.Hash != curE.Hash || refE.GroupID != curE.GroupID || refE.TransactionIndex != curE.TransactionIndex {
				if diffs < 5 { // show at most 5 diffs to keep log compact
					var reasons []string
					if curE.Hash != refE.Hash {
						reasons = append(reasons, fmt.Sprintf("Hash: %s != %s", shortHash(curE.Hash), shortHash(refE.Hash)))
					}
					if curE.GroupID != refE.GroupID {
						reasons = append(reasons, fmt.Sprintf("Grp: %s != %s", curE.GroupID, refE.GroupID))
					}
					if curE.TransactionIndex != refE.TransactionIndex {
						reasons = append(reasons, fmt.Sprintf("TxIdx: %s != %s", curE.TransactionIndex, refE.TransactionIndex))
					}

					sb.WriteString(fmt.Sprintf(
						"    ❌ %s pos[%d]: LỆCH so với %s [%s] (tx: %s, grp: %s)\n",
						n.Name, i, refName, strings.Join(reasons, " | "), shortHash(curE.Hash), curE.GroupID,
					))
				}
				diffs++
				allMatch = false
			}
		}
		if diffs == 0 {
			sb.WriteString(fmt.Sprintf("    ✅ %s: TX order identical (%d txs, groups match)\n", n.Name, len(ref.TxOrder)))
		} else if diffs > 5 {
			sb.WriteString(fmt.Sprintf("    ... và %d diff(s) khác (bỏ qua để tiết kiệm log)\n", diffs-5))
		}
	}

	if allMatch {
		return "  🔍 *TX Order:* ✅ Tất cả nodes cùng thứ tự giao dịch, group, và transactionIndex"
	}
	return sb.String()
}

// shortHash trims a 0x-prefixed hash to first 8 + last 4 chars for compact display.
func shortHash(h string) string {
	if len(h) > 14 && strings.HasPrefix(h, "0x") {
		return h[:8] + "..." + h[len(h)-4:]
	}
	return h
}

func getBlockInfo(client *http.Client, url string, blockNum uint64) (blockInfo, error) {
	hexBlock := fmt.Sprintf("0x%x", blockNum)
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  "eth_getBlockByNumber",
		Params:  []interface{}{hexBlock, true},
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

	txCount := len(block.Transactions)
	sysTxCount := 0

	if txCount == 0 {
		count, err := getSystemTxsCount(client, url, hexBlock)
		if err == nil {
			sysTxCount = count
		}
	}

	return blockInfo{
		Hash:               block.Hash,
		ParentHash:         block.ParentHash,
		StateRoot:          block.StateRoot,
		StakeStatesRoot:    block.StakeStatesRoot,
		TransactionsRoot:   block.TransactionsRoot,
		ReceiptsRoot:       block.ReceiptsRoot,
		Timestamp:          block.Timestamp,
		Miner:              block.Miner,
		GlobalExecIndex:    block.GlobalExecIndex,
		Epoch:              block.Epoch,
		AggregateSignature: block.AggregateSignature,
		CommitIndex:        block.CommitIndex,
		TxCount:            txCount,
		SysTxCount:         sysTxCount,
		TxOrder:            block.Transactions,
	}, nil
}

// ===== Receipt diagnostics for STATEROOT_FREEZE =====

// receiptResult represents a parsed transaction receipt from JSON-RPC
type receiptResult struct {
	TransactionHash string `json:"transactionHash"`
	Status          string `json:"status"` // "0x1" = success, "0x0" = failure
	From            string `json:"from"`
	To              string `json:"to"`
	GasUsed         string `json:"gasUsed"`
	BlockNumber     string `json:"blockNumber"`
}

// txObjectResult represents a full transaction object from eth_getBlockByNumber(true)
type txObjectResult struct {
	Hash  string `json:"hash"`
	From  string `json:"from"`
	To    string `json:"to"`
	Nonce string `json:"nonce"`
	Value string `json:"value"`
}

// fetchReceiptSummary fetches block with full TXs and their receipts,
// returning a diagnostic summary string like "[receipts: 5✅ / 0❌ / 0❓ | samples: 0x1234...→n=3:ok, ...]"
func fetchReceiptSummary(client *http.Client, nodeURL string, blockNum uint64) string {
	hexBlock := fmt.Sprintf("0x%x", blockNum)

	// Step 1: Fetch block with full transaction objects
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  "eth_getBlockByNumber",
		Params:  []interface{}{hexBlock, true}, // true = full tx objects
		ID:      1,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "[receipts: fetch_error]"
	}

	resp, err := client.Post(nodeURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return "[receipts: rpc_error]"
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "[receipts: read_error]"
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return "[receipts: parse_error]"
	}

	if string(rpcResp.Result) == "null" {
		return "[receipts: block_null]"
	}

	// Parse block with full tx objects
	var fullBlock struct {
		Transactions []txObjectResult `json:"transactions"`
	}
	if err := json.Unmarshal(rpcResp.Result, &fullBlock); err != nil {
		return "[receipts: tx_parse_error]"
	}

	txs := fullBlock.Transactions
	if len(txs) == 0 {
		return "[receipts: 0 txs in block body]"
	}

	// Step 2: Fetch receipt for each TX (parallel, max 10 concurrent)
	type receiptInfo struct {
		TxHash  string
		From    string
		Nonce   uint64
		Status  string // "ok", "fail", "missing"
		GasUsed uint64
	}

	results := make([]receiptInfo, len(txs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10)

	for i, tx := range txs {
		wg.Add(1)
		go func(idx int, txObj txObjectResult) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			nonce := parseHexStr(txObj.Nonce)
			ri := receiptInfo{
				TxHash: txObj.Hash,
				From:   txObj.From,
				Nonce:  nonce,
				Status: "missing",
			}

			rcp, err := getTransactionReceipt(client, nodeURL, txObj.Hash)
			if err == nil && rcp != nil {
				ri.GasUsed = parseHexStr(rcp.GasUsed)
				if rcp.Status == "0x1" {
					ri.Status = "ok"
				} else {
					ri.Status = "fail"
				}
			}

			results[idx] = ri
		}(i, tx)
	}
	wg.Wait()

	// Step 3: Summarize
	okCount := 0
	failCount := 0
	missingCount := 0
	for _, ri := range results {
		switch ri.Status {
		case "ok":
			okCount++
		case "fail":
			failCount++
		default:
			missingCount++
		}
	}

	// Build nonce detail for first 5 TXs
	nonceSamples := make([]string, 0, 5)
	for i, ri := range results {
		if i >= 5 {
			break
		}
		fromShort := ri.From
		if len(fromShort) > 10 {
			fromShort = fromShort[:10] + "..."
		}
		nonceSamples = append(nonceSamples, fmt.Sprintf("%s→n=%d:%s", fromShort, ri.Nonce, ri.Status))
	}

	return fmt.Sprintf("[receipts: %d✅ / %d❌ / %d❓ | samples: %s]",
		okCount, failCount, missingCount, strings.Join(nonceSamples, ", "))
}

// getTransactionReceipt fetches a single TX receipt via JSON-RPC
func getTransactionReceipt(client *http.Client, nodeURL string, txHash string) (*receiptResult, error) {
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  "eth_getTransactionReceipt",
		Params:  []interface{}{txHash},
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
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	if string(rpcResp.Result) == "null" {
		return nil, nil // TX exists in block but receipt not found
	}

	var rcp receiptResult
	if err := json.Unmarshal(rpcResp.Result, &rcp); err != nil {
		return nil, fmt.Errorf("cannot parse receipt: %v", err)
	}

	return &rcp, nil
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

// checkVerifyHistoricalRoot calls meta_verifyHistoricalRoot to diagnose state root mismatches
func checkVerifyHistoricalRoot(client *http.Client, nodeURL string, blockNum uint64) string {
	hexBlock := fmt.Sprintf("0x%x", blockNum)
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  "meta_verifyHistoricalRoot",
		Params:  []interface{}{hexBlock},
		ID:      1,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "[Verify: req_error]"
	}

	// Use a shorter timeout for this specific call so it doesn't block the alert too long
	// client.Timeout is applied by default, but verify might take a bit if it's full rebuild
	resp, err := client.Post(nodeURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return "[Verify: rpc_error]"
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "[Verify: read_error]"
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return "[Verify: parse_error]"
	}

	if rpcResp.Error != nil {
		return fmt.Sprintf("[Verify: ERR %s]", rpcResp.Error.Message)
	}

	if string(rpcResp.Result) == "null" {
		return "[Verify: null]"
	}

	var verifyRes map[string]interface{}
	if err := json.Unmarshal(rpcResp.Result, &verifyRes); err != nil {
		return "[Verify: result_parse_error]"
	}

	match, ok := verifyRes["match"].(bool)
	if !ok {
		return "[Verify: unknown]"
	}

	if match {
		if fastPath, ok := verifyRes["fast_path"].(bool); ok && fastPath {
			return "[Verify: MATCH (Fast Path)]"
		}
		return "[Verify: MATCH (Full)]"
	}

	return "[Verify: MISMATCH]"
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
	fmt.Printf("\n\033[1;31m╔══════════════════════════════════════════════════════════════════════════════╗\033[0m\n")
	fmt.Printf("\033[1;31m║  🚨  HASH MISMATCH DETECTED AT BLOCK %-46d ║\033[0m\n", m.BlockNumber)
	fmt.Printf("\033[1;31m╚══════════════════════════════════════════════════════════════════════════════╝\033[0m\n")

	// Collect valid blocks to show which fields actually differ
	var validBlocks []blockInfo
	for _, n := range nodes {
		bi, ok := m.Blocks[n.Name]
		if ok && !bi.IsError() {
			validBlocks = append(validBlocks, bi)
		}
	}

	// Determine which fields differ
	hashDiff, parentDiff, stateDiff, txDiff, rcpDiff, timeDiff, minerDiff, txOrderDiff := false, false, false, false, false, false, false, false
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
			if b.TransactionsRoot != ref.TransactionsRoot {
				txDiff = true
			}
			if b.ReceiptsRoot != ref.ReceiptsRoot {
				rcpDiff = true
			}
			if b.Timestamp != ref.Timestamp {
				timeDiff = true
			}
			if b.Miner != ref.Miner {
				minerDiff = true
			}
			if len(b.TxOrder) != len(ref.TxOrder) {
				txOrderDiff = true
			} else {
				for j := range ref.TxOrder {
					if b.TxOrder[j].Hash != ref.TxOrder[j].Hash || b.TxOrder[j].GroupID != ref.TxOrder[j].GroupID || b.TxOrder[j].TransactionIndex != ref.TxOrder[j].TransactionIndex {
						txOrderDiff = true
						break
					}
				}
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
	if txDiff {
		diffs = append(diffs, "txRoot")
	}
	if rcpDiff {
		diffs = append(diffs, "receiptsRoot")
	}
	if timeDiff {
		diffs = append(diffs, "timestamp")
	}
	if minerDiff {
		diffs = append(diffs, "miner")
	}
	if txOrderDiff {
		diffs = append(diffs, "txOrder")
	}
	if len(diffs) > 0 {
		fmt.Printf("   \033[1;33m⚠️  Mismatched fields: \033[1;31m%s\033[0m\n\n", strings.Join(diffs, ", "))
	}

	for _, n := range nodes {
		bi, ok := m.Blocks[n.Name]
		if !ok {
			fmt.Printf("   \033[1;30m%-12s (không có dữ liệu)\033[0m\n", n.Name+":")
			continue
		}
		if bi.IsError() {
			fmt.Printf("   \033[1;31m%-12s ERROR: %s\033[0m\n", n.Name+":", bi.Error)
			continue
		}

		fmt.Printf("   \033[1;36m• %s\033[0m\n", n.Name)

		// Helper to print a field with highlight if mismatched
		printField := func(label string, val string, isMismatched bool) {
			if isMismatched {
				fmt.Printf("       - \033[1;31m%-16s: %s [MISMATCH]\033[0m\n", label, val)
			} else {
				fmt.Printf("       - %-16s: %s\n", label, val)
			}
		}

		printField("hash", bi.Hash, hashDiff)
		printField("parentHash", bi.ParentHash, parentDiff)
		printField("stateRoot", bi.StateRoot, stateDiff)

		// if stateDiff {
		// 	verifyRes := checkVerifyHistoricalRoot(http.DefaultClient, n.URL, m.BlockNumber)
		// 	fmt.Printf("       - \033[1;36m%-16s: %s\033[0m\n", "verifyRoot", verifyRes)
		// }

		printField("txRoot", bi.TransactionsRoot, txDiff)
		printField("receiptsRoot", bi.ReceiptsRoot, rcpDiff)
		printField("timestamp", fmt.Sprintf("%s (%d)", bi.Timestamp, parseHexStr(bi.Timestamp)), timeDiff)
		printField("miner", bi.Miner, minerDiff)
		fmt.Printf("       - %-16s: %d\n", "globalExecIndex", parseHexStr(bi.GlobalExecIndex))
		fmt.Printf("       - %-16s: %d\n", "epoch", parseHexStr(bi.Epoch))
		fmt.Println()
	}
	fmt.Printf("\033[1;31m╚══════════════════════════════════════════════════════════════════════════════╝\033[0m\n\n")
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
		header += "," + name + "_txRoot"
		header += "," + name + "_receiptsRoot"
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
				line += "," + errMsg + ",,,,,,"
			} else {
				line += "," + bi.Hash
				line += "," + bi.ParentHash
				line += "," + bi.StateRoot
				line += "," + bi.TransactionsRoot
				line += "," + bi.ReceiptsRoot
				line += fmt.Sprintf(",%d,%d", parseHexStr(bi.GlobalExecIndex), parseHexStr(bi.Epoch))
			}
		}
		fmt.Fprintln(f, line)
	}

	return nil
}

// ===== Watch mode =====

const mismatchAlertFile = "hash_mismatch_alert.log"

func runWatch(client *http.Client, nodes []nodeInfo, interval time.Duration, lagThreshold int) {
	fmt.Printf("👁️  WATCH MODE — khoảng thời gian %v\n", interval)
	fmt.Println("   Nhấn Ctrl+C để dừng")
	if noStopFlag {
		fmt.Println("   🔕 --no-stop-flag: KHÔNG ghi cờ dừng và KHÔNG dừng khi phát hiện lệch hash")
	} else {
		fmt.Println("   🛑 Tự động DỪNG khi phát hiện lệch hash (ghi vào " + mismatchAlertFile + ")")
	}
	fmt.Println()

	// Handle Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	totalChecks := 0
	totalMismatches := 0
	trackedGhosts := make(map[uint64]bool)
	lastVerifiedBlock := uint64(0)
	nodeWasDead := make(map[string]bool)

	// Run immediately on start
	if watchOnce(client, nodes, &totalChecks, &totalMismatches, trackedGhosts, &lastVerifiedBlock, nodeWasDead, lagThreshold) {
		fmt.Printf("\n🛑 DỪNG WATCH MODE: Phát hiện lệch hash! Chi tiết đã ghi vào %s\n", mismatchAlertFile)
		fmt.Printf("📊 Tổng kết: %d lần check, %d lệch phát hiện\n", totalChecks, totalMismatches)
		os.Exit(1)
	}

	for {
		select {
		case <-ticker.C:
			if !daemonMode && os.Getppid() == 1 {
				fmt.Println("ℹ️ Parent process has exited. Watcher exiting cleanly.")
				os.Exit(0)
			}
			if watchOnce(client, nodes, &totalChecks, &totalMismatches, trackedGhosts, &lastVerifiedBlock, nodeWasDead, lagThreshold) {
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
func watchOnce(client *http.Client, nodes []nodeInfo, totalChecks, totalMismatches *int, trackedGhosts map[uint64]bool, lastVerifiedBlock *uint64, nodeWasDead map[string]bool, lagThreshold int) bool {
	*totalChecks++
	now := time.Now().Format("15:04:05")

	// Query latest block number from each node
	type nodeBlock struct {
		name  string
		block uint64
		err   error
	}

	var results []nodeBlock
	var minBlock, maxBlock uint64
	minBlock = ^uint64(0)

	var hasNodeError bool
	for _, n := range nodes {
		num, err := getLatestBlockNumber(client, n.URL)

		if err == nil {
			if nodeWasDead[n.Name] {
				fmt.Printf("\n🔄 NODE %s ĐÃ SỐNG LẠI! Tiếp tục kiểm tra từ mốc đồng bộ hiện tại...\n", n.Name)
				nodeWasDead[n.Name] = false
			}
		} else {
			hasNodeError = true
			nodeWasDead[n.Name] = true
		}

		results = append(results, nodeBlock{name: n.Name, block: num, err: err})
		if err == nil {
			if num < minBlock {
				minBlock = num
			}
			if num > maxBlock {
				maxBlock = num
			}
		}
	}

	if hasNodeError {
		clearLoggedBlocks()
		for k := range trackedGhosts {
			delete(trackedGhosts, k)
		}
	}

	// Show block heights
	fmt.Printf("[%s] #%d ", now, *totalChecks)
	var heightParts []string
	for _, r := range results {
		if r.err != nil {
			heightParts = append(heightParts, fmt.Sprintf("%s=ERR", r.name))
		} else {
			heightParts = append(heightParts, fmt.Sprintf("%s=%d", r.name, r.block))
		}
	}
	fmt.Printf("Heights: %s", strings.Join(heightParts, "  "))

	// Block height difference warning
	if maxBlock > 0 && minBlock < ^uint64(0) {
		diff := maxBlock - minBlock
		if diff > 10 {
			fmt.Printf(" ⚠️ CHÊNH %d blocks!", diff)
			// Trigger Telegram alert if diff is huge (e.g. > lag-threshold blocks)
			if lagThreshold > 0 && diff > uint64(lagThreshold) {
				var laggingNodes []string
				for _, r := range results {
					if r.err == nil && maxBlock-r.block >= uint64(lagThreshold) {
						laggingNodes = append(laggingNodes, fmt.Sprintf("%s(%d)", r.name, r.block))
					}
				}
				logAnomaly("NODE_LAGGING", minBlock,
					fmt.Sprintf("Node %s đang bị tụt lại quá xa so với đỉnh (chênh lệch %d blocks giữa max và min). Chi tiết tất cả nodes: %s", strings.Join(laggingNodes, ", "), diff, strings.Join(heightParts, "  ")))
			}
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

	// Rewind cursor if a node comes back online with a smaller block
	if *lastVerifiedBlock > minBlock {
		fmt.Printf(" ⚠️ Phát hiện node tụt lùi hoặc khởi động lại (minBlock %d < lastVerified %d). Lùi mốc kiểm tra để quét lại...\n", minBlock, *lastVerifiedBlock)
		if minBlock > 0 {
			*lastVerifiedBlock = minBlock - 1
		} else {
			*lastVerifiedBlock = 0
		}
	}

	from := uint64(1)
	if *lastVerifiedBlock > 0 {
		from = *lastVerifiedBlock + 1
	}

	if from > maxBlock {
		fmt.Printf(" ✅ Không có block mới (đã check tới %d)\n", *lastVerifiedBlock)
		// Vẫn in hash của block mới nhất để user theo dõi
		fmt.Printf("   📦 Block %d hashes:\n", maxBlock)
		for _, n := range nodes {
			bi, err := getBlockInfo(client, n.URL, maxBlock)
			if err != nil {
				fmt.Printf("      %-12s ERR: %v\n", n.Name+":", err)
			} else if bi.IsError() {
				fmt.Printf("      %-12s %s\n", n.Name+":", bi.Error)
			} else {
				fmt.Printf("      %-12s hash=%s  stateRoot=%s  gei=%d  epoch=%d\n", n.Name+":", bi.Hash, bi.StateRoot, parseHexStr(bi.GlobalExecIndex), parseHexStr(bi.Epoch))
			}
		}
		return false
	}

	var mismatches []mismatch
	var matched, skipped uint64
	var nilBlocks, emptyBlocks []uint64

	// LỖI OOM FIX: Chunk checkBatch để tránh cấp phát mảng results khổng lồ (chứa full txOrderEntry) khi node bị tụt hậu xa
	batchSize := uint64(20)
	for batchStart := from; batchStart <= maxBlock; batchStart += batchSize {
		batchEnd := batchStart + batchSize - 1
		if batchEnd > maxBlock {
			batchEnd = maxBlock
		}
		
		bMismatches, bMatched, _, bSkipped, bNilBlocks, bEmptyBlocks := checkBatch(client, nodes, batchStart, batchEnd)
		mismatches = append(mismatches, bMismatches...)
		matched += bMatched
		skipped += bSkipped
		nilBlocks = append(nilBlocks, bNilBlocks...)
		emptyBlocks = append(emptyBlocks, bEmptyBlocks...)
		
		if len(mismatches) > 0 {
			break // Dừng ngay nếu phát hiện lệch hash trong chunk này
		}
	}

	if len(mismatches) == 0 {
		if skipped > 0 {
			fmt.Printf(" ✅ hash khớp %d blocks, ⚠️ %d blocks không đủ node (block %d→%d)\n", matched, skipped, from, maxBlock)
		} else {
			fmt.Printf(" ✅ hash khớp %d blocks (block %d→%d)\n", matched, from, maxBlock)
		}

		if len(nilBlocks) > 0 || len(emptyBlocks) > 0 {
			showN := len(nilBlocks)
			if showN > 5 {
				showN = 5
			}
			showE := len(emptyBlocks)
			if showE > 5 {
				showE = 5
			}

			var parts []string
			if len(nilBlocks) > 0 {
				s := fmt.Sprintf("⚠️ Get bị nil: %d blocks %v", len(nilBlocks), nilBlocks[:showN])
				if len(nilBlocks) > 5 {
					s += "..."
				}
				parts = append(parts, s)
			}
			if len(emptyBlocks) > 0 {
				s := fmt.Sprintf("👻 Rỗng (tx=0, sys_txs=0): %d blocks %v", len(emptyBlocks), emptyBlocks[:showE])
				if len(emptyBlocks) > 5 {
					s += "..."
				}
				parts = append(parts, s)
			}
			fmt.Printf("   %s\n", strings.Join(parts, "  |  "))
		}

		// In hash của block mới nhất (maxBlock) từ mỗi node
		fmt.Printf("   📦 Block %d hashes:\n", maxBlock)
		for _, n := range nodes {
			bi, err := getBlockInfo(client, n.URL, maxBlock)
			if err != nil {
				fmt.Printf("      %-12s ERR: %v\n", n.Name+":", err)
			} else if bi.IsError() {
				fmt.Printf("      %-12s %s\n", n.Name+":", bi.Error)
			} else {
				fmt.Printf("      %-12s hash=%s  stateRoot=%s  gei=%d  epoch=%d\n", n.Name+":", bi.Hash, bi.StateRoot, parseHexStr(bi.GlobalExecIndex), parseHexStr(bi.Epoch))
			}
		}

		*lastVerifiedBlock = minBlock
		return false
	}

	// ═══════════════════════════════════════════════════════════════════
	// MISMATCH DETECTED — write to file + print to console + signal stop
	// ═══════════════════════════════════════════════════════════════════

	// --- TRUY VẤN LÙI ĐỂ TÌM BLOCK LỆCH ĐẦU TIÊN ---
	firstMismatchInBatch := mismatches[0].BlockNumber
	fmt.Printf("\n🔍 Phát hiện lệch hash tại block %d. Đang truy vấn lùi bằng Binary Search để tìm block lệch đầu tiên...\n", firstMismatchInBatch)

	realFirstMismatch := firstMismatchInBatch
	low := uint64(1)
	high := firstMismatchInBatch - 1

	for low <= high {
		mid := low + (high-low)/2
		// Check just this single block
		m, _, _, _, _, _ := checkBatch(client, nodes, mid, mid)
		if len(m) > 0 {
			realFirstMismatch = mid
			high = mid - 1 // Tiếp tục tìm về trước
		} else {
			low = mid + 1 // Mismatch nằm ở phía sau
		}
	}

	if realFirstMismatch < firstMismatchInBatch {
		fmt.Printf("🎯 Đã tìm thấy block lệch đầu tiên thực sự tại: %d\n", realFirstMismatch)
		// Fetch lại chi tiết block này
		m, _, _, _, _, _ := checkBatch(client, nodes, realFirstMismatch, realFirstMismatch)
		if len(m) > 0 {
			// Push vào đầu slice để in ra báo cáo
			mismatches = append(m, mismatches...)
		}
	} else {
		fmt.Printf("🎯 Block %d chính là block lệch đầu tiên trên chuỗi.\n", realFirstMismatch)
	}

	// Trigger the verified first mismatch stop flag for Telegram!
	stopFlagMsg := triggerStopFlagForFirstMismatch(client, nodes, realFirstMismatch)

	*totalMismatches += len(mismatches)

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

	for _, m := range mismatches {
		alertBuf.WriteString(fmt.Sprintf("\n⚠️  Block %d:\n", m.BlockNumber))

		// Find which fields differ
		var diffParent, diffState, diffTx, diffRcp, diffTime, diffMiner, diffGei, diffEpoch bool
		var ref blockInfo
		var refSet bool
		for _, n := range nodes {
			bi, ok := m.Blocks[n.Name]
			if !ok || bi.IsError() || bi.Error != "" {
				continue
			}
			if !refSet {
				ref = bi
				refSet = true
				continue
			}
			if bi.ParentHash != ref.ParentHash {
				diffParent = true
			}
			if bi.StateRoot != ref.StateRoot {
				diffState = true
			}
			if bi.TransactionsRoot != ref.TransactionsRoot {
				diffTx = true
			}
			if bi.ReceiptsRoot != ref.ReceiptsRoot {
				diffRcp = true
			}
			if bi.Timestamp != ref.Timestamp {
				diffTime = true
			}
			if bi.Miner != ref.Miner {
				diffMiner = true
			}
			if bi.GlobalExecIndex != ref.GlobalExecIndex {
				diffGei = true
			}
			if bi.Epoch != ref.Epoch {
				diffEpoch = true
			}
		}

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

			// Always print hash
			line := fmt.Sprintf("   %-12s hash=%s", n.Name+":", bi.Hash)

			// Only print differing fields
			if diffParent {
				line += fmt.Sprintf("  parent=%s", bi.ParentHash)
			}
			if diffState {
				line += fmt.Sprintf("  stateRoot=%s", bi.StateRoot)
			}
			if diffTx {
				line += fmt.Sprintf("  txRoot=%s", bi.TransactionsRoot)
			}
			if diffRcp {
				line += fmt.Sprintf("  rcpRoot=%s", bi.ReceiptsRoot)
			}
			if diffTime {
				line += fmt.Sprintf("  time=%s", bi.Timestamp)
			}
			if diffMiner {
				line += fmt.Sprintf("  miner=%s", bi.Miner)
			}
			if diffGei {
				line += fmt.Sprintf("  gei=%d", parseHexStr(bi.GlobalExecIndex))
			}
			if diffEpoch {
				line += fmt.Sprintf("  epoch=%d", parseHexStr(bi.Epoch))
			}

			alertBuf.WriteString(line + "\n")
		}
	}

	// Build surrounding details for the first mismatched block
	var surroundingDetails strings.Builder
	surroundingDetails.WriteString("\n================================================================================\n")
	surroundingDetails.WriteString("🔍 CHI TIẾT BLOCK LỆCH ĐẦU TIÊN VÀ CÁC BLOCK LÂN CẬN\n")
	surroundingDetails.WriteString("================================================================================\n")

	if realFirstMismatch > 1 {
		surroundingDetails.WriteString(formatFullBlockDetails(client, nodes, realFirstMismatch-1, "⏮️  BLOCK TRƯỚC ĐÓ"))
	}
	surroundingDetails.WriteString(formatFullBlockDetails(client, nodes, realFirstMismatch, "🚨 BLOCK LỆCH ĐẦU TIÊN"))

	surroundingDetails.WriteString(formatFullBlockDetails(client, nodes, realFirstMismatch+1, "⏭️  BLOCK SAU ĐÓ"))
	surroundingDetails.WriteString("================================================================================\n")

	alertBuf.WriteString(surroundingDetails.String())

	alertBuf.WriteString("\n" + stopFlagMsg + "\n")

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

func formatFullBlockDetails(client *http.Client, nodes []nodeInfo, blockNum uint64, title string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n%s (BLOCK #%d):\n", title, blockNum))

	for _, node := range nodes {
		bi, err := getBlockInfo(client, node.URL, blockNum)
		if err != nil {
			sb.WriteString(fmt.Sprintf("   %-12s ERROR: %v\n", node.Name+":", err))
			continue
		}
		if bi.IsError() {
			sb.WriteString(fmt.Sprintf("   %-12s ERROR: %s\n", node.Name+":", bi.Error))
			continue
		}

		sb.WriteString(fmt.Sprintf("   %-12s\n", "• "+node.Name+":"))
		sb.WriteString(fmt.Sprintf("       - hash:               %s\n", bi.Hash))
		sb.WriteString(fmt.Sprintf("       - parentHash:         %s\n", bi.ParentHash))
		sb.WriteString(fmt.Sprintf("       - stateRoot:          %s\n", bi.StateRoot))
		sb.WriteString(fmt.Sprintf("       - stakeStatesRoot:    %s\n", bi.StakeStatesRoot))
		sb.WriteString(fmt.Sprintf("       - transactionsRoot:   %s\n", bi.TransactionsRoot))
		sb.WriteString(fmt.Sprintf("       - receiptsRoot:       %s\n", bi.ReceiptsRoot))
		sb.WriteString(fmt.Sprintf("       - timestamp:          %s (%d)\n", bi.Timestamp, parseHexStr(bi.Timestamp)))
		sb.WriteString(fmt.Sprintf("       - miner:              %s\n", bi.Miner))
		sb.WriteString(fmt.Sprintf("       - globalExecIndex:    %s (%d)\n", bi.GlobalExecIndex, parseHexStr(bi.GlobalExecIndex)))
		sb.WriteString(fmt.Sprintf("       - epoch:              %s (%d)\n", bi.Epoch, parseHexStr(bi.Epoch)))
		sb.WriteString(fmt.Sprintf("       - commitIndex:        %s (%d)\n", bi.CommitIndex, parseHexStr(bi.CommitIndex)))
		sb.WriteString(fmt.Sprintf("       - aggregateSignature: %s\n", bi.AggregateSignature))
	}
	return sb.String()
}

func triggerStopFlagForFirstMismatch(client *http.Client, nodes []nodeInfo, blockNum uint64) string {
	// Fetch block info across all nodes
	blocks := make(map[string]blockInfo)
	var validBlocks []blockInfo

	for _, node := range nodes {
		bi, err := getBlockInfo(client, node.URL, blockNum)
		if err == nil && !bi.IsError() {
			blocks[node.Name] = bi
			validBlocks = append(validBlocks, bi)
		} else {
			if err != nil {
				blocks[node.Name] = blockInfo{Error: fmt.Sprintf("ERROR: %v", err)}
			} else {
				blocks[node.Name] = bi
			}
		}
	}

	if len(validBlocks) < 2 {
		return ""
	}

	mismatchedFields := make(map[string]bool)
	ref := validBlocks[0]

	for i := 1; i < len(validBlocks); i++ {
		b := validBlocks[i]
		if b.Hash != ref.Hash {
			mismatchedFields["hash"] = true
		}
		if b.ParentHash != ref.ParentHash {
			mismatchedFields["parentHash"] = true
		}
		if b.StateRoot != ref.StateRoot {
			mismatchedFields["stateRoot"] = true
		}
		if b.StakeStatesRoot != ref.StakeStatesRoot {
			mismatchedFields["stakeStatesRoot"] = true
		}
		if b.TransactionsRoot != ref.TransactionsRoot {
			mismatchedFields["transactionsRoot"] = true
		}
		if b.ReceiptsRoot != ref.ReceiptsRoot {
			mismatchedFields["receiptsRoot"] = true
		}
		if b.Timestamp != ref.Timestamp {
			mismatchedFields["timestamp"] = true
		}
		if b.Miner != ref.Miner {
			mismatchedFields["miner"] = true
		}
		if b.Epoch != ref.Epoch {
			mismatchedFields["epoch"] = true
		}
		if b.GlobalExecIndex != ref.GlobalExecIndex {
			mismatchedFields["globalExecIndex"] = true
		}
		if b.CommitIndex != ref.CommitIndex {
			mismatchedFields["commitIndex"] = true
		}
	}

	var fields []string
	for f := range mismatchedFields {
		fields = append(fields, f)
	}
	sort.Strings(fields)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🚨 *LỆCH BLOCK #%d*\n", blockNum))
	if len(fields) > 0 {
		sb.WriteString(fmt.Sprintf("• ⚠️ *Trường bị lệch:* `[%s]`\n", strings.Join(fields, ", ")))
	} else {
		sb.WriteString("• ⚠️ *Trường bị lệch:* `[không xác định - độ trễ/lỗi node]`\n")
	}
	sb.WriteString("• *Chi tiết các node:*\n")

	for _, node := range nodes {
		bi := blocks[node.Name]
		if bi.IsError() {
			sb.WriteString(fmt.Sprintf("  - *%s*: `ERROR: %s`\n", node.Name, bi.Error))
		} else {
			var fieldVals []string
			// Show actual values for mismatched fields
			showFields := fields
			if len(showFields) == 0 {
				showFields = []string{"hash", "stateRoot"}
			}
			for _, f := range showFields {
				val := ""
				switch f {
				case "hash":
					val = bi.Hash
				case "parentHash":
					val = bi.ParentHash
				case "stateRoot":
					val = bi.StateRoot
				case "stakeStatesRoot":
					val = bi.StakeStatesRoot
				case "transactionsRoot":
					val = bi.TransactionsRoot
				case "receiptsRoot":
					val = bi.ReceiptsRoot
				case "timestamp":
					val = fmt.Sprintf("%s(%d)", bi.Timestamp, parseHexStr(bi.Timestamp))
				case "miner":
					val = bi.Miner
				case "epoch":
					val = fmt.Sprintf("%d", parseHexStr(bi.Epoch))
				case "globalExecIndex":
					val = fmt.Sprintf("%d", parseHexStr(bi.GlobalExecIndex))
				case "commitIndex":
					val = fmt.Sprintf("%d", parseHexStr(bi.CommitIndex))
				}
				if len(val) > 16 && strings.HasPrefix(val, "0x") {
					val = val[:8] + "..." + val[len(val)-4:]
				}
				fieldVals = append(fieldVals, fmt.Sprintf("%s: `%s`", f, val))
			}
			geiVal := parseHexStr(bi.GlobalExecIndex)
			epochVal := parseHexStr(bi.Epoch)
			sb.WriteString(fmt.Sprintf("  - *%s*: %s (gei=%d, epoch=%d)\n",
				node.Name, strings.Join(fieldVals, ", "), geiVal, epochVal))

			if mismatchedFields["stateRoot"] {
				// verifyRes := checkVerifyHistoricalRoot(client, node.URL, blockNum)
				// sb.WriteString(fmt.Sprintf("    ↳ %s\n", verifyRes))
			}
		}
	}

	sb.WriteString("\n")
	sb.WriteString(checkTxOrderMismatch(nodes, blockNum, blocks) + "\n")

	if mismatchedFields["stateRoot"] {
		// 1. Show curl commands
		sb.WriteString("\n• 🔍 *Để kiểm tra thay đổi tài khoản chi tiết bằng curl:*\n")
		for _, node := range nodes {
			bi := blocks[node.Name]
			if !bi.IsError() {
				sb.WriteString(fmt.Sprintf("  - *%s*: `curl -s -X POST -H \"Content-Type: application/json\" --data '{\"jsonrpc\":\"2.0\",\"method\":\"debug_getBlockStateDiff\",\"params\":[%d],\"id\":1}' %s`\n",
					node.Name, blockNum, node.URL))
			}
		}

		// 2. Select two nodes with different stateRoots for comparison
		var diffNode1, diffNode2 *nodeInfo
		for i, nodeA := range nodes {
			biA := blocks[nodeA.Name]
			if biA.IsError() {
				continue
			}
			for j, nodeB := range nodes {
				if i == j {
					continue
				}
				biB := blocks[nodeB.Name]
				if biB.IsError() {
					continue
				}
				if biA.StateRoot != biB.StateRoot {
					nA := nodeA
					nB := nodeB
					diffNode1 = &nA
					diffNode2 = &nB
					break
				}
			}
			if diffNode1 != nil {
				break
			}
		}

		if diffNode1 != nil && diffNode2 != nil {
			comparison := compareNodeStateDiffs(client, *diffNode1, *diffNode2, blockNum)
			sb.WriteString(comparison)
		}
	}

	triggerStopFlag(sb.String())
	return sb.String()
}

// ===== Types and helper functions for RPC Block State Diffing =====

type ModifiedAccountRPC struct {
	Address         string `json:"address"`
	PreBalance      string `json:"preBalance"`
	PostBalance     string `json:"postBalance"`
	PreNonce        uint64 `json:"preNonce"`
	PostNonce       uint64 `json:"postNonce"`
	PreCodeHash     string `json:"preCodeHash"`
	PostCodeHash    string `json:"postCodeHash"`
	PreStorageRoot  string `json:"preStorageRoot"`
	PostStorageRoot string `json:"postStorageRoot"`
	PreDataHash     string `json:"preDataHash"`
	PostDataHash    string `json:"postDataHash"`
	IsNew           bool   `json:"isNew"`
}

type BlockStateDiffRPC struct {
	BlockNumber      uint64                        `json:"blockNumber"`
	CalculatedRoot   string                        `json:"calculatedRoot"`
	ModifiedAccounts map[string]ModifiedAccountRPC `json:"modifiedAccounts"`
}

type blockStateDiffResponse struct {
	JsonRPC string             `json:"jsonrpc"`
	Result  *BlockStateDiffRPC `json:"result"`
	Error   interface{}        `json:"error"`
}

func queryBlockStateDiff(client *http.Client, nodeURL string, blockNum uint64) (*BlockStateDiffRPC, error) {
	reqBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "debug_getBlockStateDiff",
		"params":  []interface{}{blockNum},
		"id":      1,
	})
	if err != nil {
		return nil, err
	}

	resp, err := client.Post(nodeURL, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var rpcResp blockStateDiffResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, err
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error: %v", rpcResp.Error)
	}

	return rpcResp.Result, nil
}

func compareNodeStateDiffs(client *http.Client, node1 nodeInfo, node2 nodeInfo, blockNum uint64) string {
	diff1, err1 := queryBlockStateDiff(client, node1.URL, blockNum)
	diff2, err2 := queryBlockStateDiff(client, node2.URL, blockNum)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n• 🔍 *ĐỐI CHIẾU CHI TIẾT TÀI KHOẢN GIỮA %s VÀ %s TẠI BLOCK #%d:*\n", node1.Name, node2.Name, blockNum))

	if err1 != nil {
		sb.WriteString(fmt.Sprintf("  - [%s] ❌ Lỗi lấy thông tin: `%v`\n", node1.Name, err1))
	}
	if err2 != nil {
		sb.WriteString(fmt.Sprintf("  - [%s] ❌ Lỗi lấy thông tin: `%v`\n", node2.Name, err2))
	}
	if err1 != nil || err2 != nil {
		return sb.String()
	}

	if diff1 == nil || diff2 == nil {
		sb.WriteString("  - ⚠️ Dữ liệu trả về từ RPC rỗng.\n")
		return sb.String()
	}

	allAddrs := make(map[string]bool)
	for addr := range diff1.ModifiedAccounts {
		allAddrs[addr] = true
	}
	for addr := range diff2.ModifiedAccounts {
		allAddrs[addr] = true
	}

	var sortedAddrs []string
	for addr := range allAddrs {
		sortedAddrs = append(sortedAddrs, addr)
	}
	sort.Strings(sortedAddrs)

	hasDiffs := false
	for _, addr := range sortedAddrs {
		acc1, ok1 := diff1.ModifiedAccounts[addr]
		acc2, ok2 := diff2.ModifiedAccounts[addr]

		if !ok1 {
			hasDiffs = true
			sb.WriteString(fmt.Sprintf("  - *Tài khoản %s*: Chỉ thay đổi ở `%s` (không đổi ở `%s`)\n", addr, node2.Name, node1.Name))
			sb.WriteString(fmt.Sprintf("    * [%s]: %s (bal=%s, nonce=%d)\n", node2.Name, formatAccountDiffCompact(acc2), acc2.PostBalance, acc2.PostNonce))
			continue
		}
		if !ok2 {
			hasDiffs = true
			sb.WriteString(fmt.Sprintf("  - *Tài khoản %s*: Chỉ thay đổi ở `%s` (không đổi ở `%s`)\n", addr, node1.Name, node2.Name))
			sb.WriteString(fmt.Sprintf("    * [%s]: %s (bal=%s, nonce=%d)\n", node1.Name, formatAccountDiffCompact(acc1), acc1.PostBalance, acc1.PostNonce))
			continue
		}

		var fieldDiffs []string
		if acc1.PostBalance != acc2.PostBalance {
			fieldDiffs = append(fieldDiffs, fmt.Sprintf("balance: `%s` vs `%s`", acc1.PostBalance, acc2.PostBalance))
		}
		if acc1.PostNonce != acc2.PostNonce {
			fieldDiffs = append(fieldDiffs, fmt.Sprintf("nonce: `%d` vs `%d`", acc1.PostNonce, acc2.PostNonce))
		}
		if acc1.PostCodeHash != acc2.PostCodeHash {
			fieldDiffs = append(fieldDiffs, fmt.Sprintf("codeHash: `%s` vs `%s`", acc1.PostCodeHash, acc2.PostCodeHash))
		}
		if acc1.PostStorageRoot != acc2.PostStorageRoot {
			fieldDiffs = append(fieldDiffs, fmt.Sprintf("storageRoot: `%s` vs `%s`", acc1.PostStorageRoot, acc2.PostStorageRoot))
		}
		if acc1.PostDataHash != acc2.PostDataHash {
			fieldDiffs = append(fieldDiffs, fmt.Sprintf("dataHash: `%s` vs `%s`", acc1.PostDataHash, acc2.PostDataHash))
		}

		if len(fieldDiffs) > 0 {
			hasDiffs = true
			sb.WriteString(fmt.Sprintf("  - *Tài khoản %s*:\n", addr))
			for _, fd := range fieldDiffs {
				sb.WriteString(fmt.Sprintf("    * ⚠️ *Khác biệt %s*\n", fd))
			}
		}
	}

	if !hasDiffs {
		sb.WriteString("  - ✅ Không phát hiện khác biệt nào trong dữ liệu các tài khoản bị thay đổi!\n")
	}

	return sb.String()
}

func formatAccountDiffCompact(acc ModifiedAccountRPC) string {
	if acc.IsNew {
		return "TẠO MỚI"
	}
	return "CẬP NHẬT"
}
