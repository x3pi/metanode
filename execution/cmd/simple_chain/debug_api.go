package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gorilla/websocket"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/trace"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/error_tx_manager"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	pkg_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_state_db"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

type DebugApi struct {
	App *App // Export field Client
}

var (
	logStreamUpgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	maxLogChunkBytes int64 = 128 * 1024 // 128KB mỗi lần gửi
)

// LogListParams đại diện cho tham số yêu cầu liệt kê file log.
type LogListParams struct {
	Root  string `json:"root"`
	Epoch string `json:"epoch"`
}

// LogFileParams đại diện cho tham số yêu cầu đọc nội dung log.
type LogFileParams struct {
	Root     string `json:"root"`
	Epoch    string `json:"epoch"`
	FileName string `json:"fileName"`
	MaxBytes int64  `json:"maxBytes"`
	Format   string `json:"format"`
}

// ConnectionListParams xác định bộ lọc cho danh sách kết nối.
type ConnectionListParams struct {
	Type string `json:"type"` // nếu rỗng: liệt kê tất cả
}

// ConnectionInfo mô tả một kết nối đang được quản lý.
type ConnectionInfo struct {
	Type            string `json:"type"`
	Address         string `json:"address"`
	RemoteAddr      string `json:"remoteAddr"`
	Connected       bool   `json:"connected"`
	IsParent        bool   `json:"isParent"`
	TcpRemoteAddr   string `json:"tcpRemoteAddr"`
	TcpLocalAddr    string `json:"tcpLocalAddr"`
	ConnectionLabel string `json:"label"`
}

func (api *DebugApi) GetTransactionError(ctx context.Context, hash common.Hash) (types.Transaction, error) {
	dbSaveError := error_tx_manager.NewErrorTxManager(api.App.storageManager.GetStorageBackupDb())
	tx, err := dbSaveError.GetTransactionError(hash)
	if err != nil {
		return nil, fmt.Errorf("GetTransactionError: failed to get transaction error for hash %s: %w", hash.Hex(), err)
	}
	return tx, nil
}

func (api *DebugApi) GetRCPTransactionError(ctx context.Context, hash common.Hash) (types.Receipt, error) {
	dbSaveError := error_tx_manager.NewErrorTxManager(api.App.storageManager.GetStorageBackupDb())
	receipt, err := dbSaveError.GetRCPTransactionError(hash)
	if err != nil {
		return nil, fmt.Errorf("GetRCPTransactionError: failed to get receipt for hash %s: %w", hash.Hex(), err)
	}
	return receipt, nil
}

func (api *DebugApi) TraceTransaction(ctx context.Context, hashEth common.Hash) (types.ExecuteSCResult, error) {
	blockNumber, ok := blockchain.GetBlockChainInstance().GetBlockNumberByTxHash(hashEth)
	hashTx := hashEth

	if !ok {
		hash, ok := blockchain.GetBlockChainInstance().GetEthHashMapblsHash(hashEth)
		if !ok {
			return nil, fmt.Errorf("TraceTransaction: cannot map hash from file for hash: %s", hashEth.Hex())
		}

		blockNumber, ok = blockchain.GetBlockChainInstance().GetBlockNumberByTxHash(hash)
		if !ok {
			return nil, fmt.Errorf("TraceTransaction: transaction with hash %s not found in chain", hash.Hex())
		}
		hashTx = hash
	}

	hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
	if !ok {
		return nil, fmt.Errorf("TraceTransaction: cannot find block hash for block number %d", blockNumber)
	}

	blockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(hash)
	if err != nil {
		logger.Warn("TraceTransaction: error loading block from file:", err)
		return nil, fmt.Errorf("TraceTransaction: failed to load block with hash %s: %w", hash.Hex(), err)
	}

	oldBlockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber - 1)
	if !ok {
		return nil, fmt.Errorf("TraceBlock: cannot find previous block hash for block number %d", blockNumber-1)
	}

	oldBlockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(oldBlockHash)
	if err != nil {
		logger.Warn("TraceTransaction: error loading previous block from file:", err)
		return nil, fmt.Errorf("TraceTransaction: failed to load previous block with hash %s: %w", oldBlockHash.Hex(), err)
	}

	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), api.App.storageManager.GetStorageTransaction())
	if err != nil {
		return nil, fmt.Errorf("TraceTransaction: failed to create TransactionStateDB: %w", err)
	}

	tx, err := txDB.GetTransaction(hashTx)
	if err != nil {
		return nil, fmt.Errorf("TraceTransaction: cannot get transaction %s from state DB: %w", hashTx.Hex(), err)
	}

	rs, err := api.App.transactionProcessor.ProcessTransactionDebug(tx, oldBlockData)
	if err != nil {
		return nil, fmt.Errorf("TraceTransaction: error processing transaction debug: %w", err)
	}

	logger.Info(rs)
	return rs, nil
}

// TraceBlock executes the transactions within a specific block for debugging purposes.
// It takes a block number as input and returns a map of transaction hashes to their execution results.
func (api *DebugApi) TraceBlock(ctx context.Context, blockNumber uint64) ([]*trace.Span, error) {
	logger.Error("blockNumber:", blockNumber)
	if blockNumber == 0 {
		return nil, fmt.Errorf("TraceBlock: cannot trace genesis block (block number 0)")
	}

	// Get target block hash and data
	hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
	if !ok {
		return nil, fmt.Errorf("TraceBlock: cannot find block hash for block number %d", blockNumber)
	}

	blockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(hash)
	if err != nil {
		logger.Warn("TraceBlock: error loading block %d from file: %v", blockNumber, err) // Use Warnf for formatted warning
		return nil, fmt.Errorf("TraceBlock: failed to load block with hash %s: %w", hash.Hex(), err)
	}

	// Get previous block hash and data
	oldBlockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber - 1)
	if !ok {
		return nil, fmt.Errorf("TraceBlock: cannot find previous block hash for block number %d", blockNumber-1)
	}

	oldBlockData, err := api.App.chainState.GetBlockDatabase().GetBlockByHash(oldBlockHash)
	if err != nil {
		logger.Warn("TraceBlock: error loading previous block %d from file: %v", blockNumber-1, err) // Use Warnf
		return nil, fmt.Errorf("TraceBlock: failed to load previous block with hash %s: %w", oldBlockHash.Hex(), err)
	}

	// Create TransactionStateDB for the target block to fetch transactions
	txDB, err := transaction_state_db.NewTransactionStateDBFromRoot(blockData.Header().TransactionsRoot(), api.App.storageManager.GetStorageTransaction())
	if err != nil {
		return nil, fmt.Errorf("TraceBlock: failed to create TransactionStateDB for block %d: %w", blockNumber, err)
	}

	// --- Fetch all transactions into a slice first ---
	transactionHashes := blockData.Transactions() // Assuming this returns []common.Hash
	txs := make([]types.Transaction, 0, len(transactionHashes))
	logger.Info("Fetching %d transactions for block %d...", len(transactionHashes), blockNumber)

	for _, txHash := range transactionHashes {
		tx, err := txDB.GetTransaction(txHash)
		if err != nil {
			logger.Error("TraceBlock: failed to get transaction %s from state DB for block %d: %v", txHash.Hex(), blockNumber, err)
			// Return immediately if a transaction cannot be fetched
			return nil, fmt.Errorf("TraceBlock: cannot get transaction %s from state DB: %w", txHash.Hex(), err)
		}
		txs = append(txs, tx)
	}

	// Prepare items for grouping
	items := make([]grouptxns.Item, 0, len(txs)) // Use current tp.excludedItems
	for i, tx := range txs {
		items = append(items, grouptxns.Item{
			ID:        i, // Adjust ID based on current excludedItems length
			Array:     tx.RelatedAddresses(),
			GroupID:   0,
			Tx:        tx,
			TimeStart: time.Now(),
		})
	}

	// Group transactions
	groupedGroups, _, err := grouptxns.GroupAndLimitTransactionsOptimized(items, mt_common.MAX_GROUP_GAS, mt_common.MAX_TOTAL_GAS, mt_common.MAX_GROUP_TIME, mt_common.MAX_TOTAL_TIME)
	if err != nil {
		return nil, fmt.Errorf("TraceBlock: failed to create grouptxns for block %d: %w", blockNumber, err)
	}
	blockDatabase := block.NewBlockDatabase(api.App.storageManager.GetStorageBlock())
	myCollector := trace.NewSpanCollector()

	tracedCtx, rootSpan := trace.NewTrace(ctx, "ProcessBlockTransactions", map[string]interface{}{
		"blockNumber":  blockData.Header().BlockNumber(),
		"txGroupCount": len(groupedGroups),
	}, myCollector)
	// QUAN TRỌNG: Phải gọi End() trước khi cố gắng lấy span từ exporter
	defer rootSpan.End()

	chainState, err := blockchain.NewChainState(api.App.storageManager, blockDatabase, oldBlockData.Header(), api.App.config, FreeFeeAddresses, "") // Empty backupPath for temporary chain state
	if err != nil {
		return nil, fmt.Errorf("TraceBlock: failed to create chainState for block %d: %w", blockNumber, err)
	}
	processResult, err := tx_processor.ProcessTransactions(tracedCtx, chainState, groupedGroups, true, false, uint64(time.Now().Unix()))
	if err != nil {
		return nil, fmt.Errorf("TraceBlock: failed to create chainState for block %d: %w", blockNumber, err)
	}
	logger.Info(processResult)
	allSpans := myCollector.GetSpans()

	return allSpans, nil
}

// ListLogFiles trả về danh sách file log theo epoch.
func (api *DebugApi) ListLogFiles(ctx context.Context, params LogListParams) ([]string, error) {
	files, err := loggerfile.ListLogFiles(params.Root, params.Epoch)
	if err != nil {
		return nil, fmt.Errorf("ListLogFiles: %w", err)
	}
	return files, nil
}

// GetLogFileContent đọc nội dung file log theo epoch, có thể giới hạn tối đa số byte trả về.
func (api *DebugApi) GetLogFileContent(ctx context.Context, params LogFileParams) (string, error) {
	content, err := loggerfile.ReadLogFile(params.Root, params.Epoch, params.FileName, params.MaxBytes)
	if err != nil {
		return "", fmt.Errorf("GetLogFileContent: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(params.Format)) {
	case "html":
		return renderColorLogHTML(content, params.FileName), nil
	case "text", "plain":
		fallthrough
	default:
		return content, nil
	}
}

// ServeLogPreview cung cấp endpoint HTTP GET sẵn có để xem log qua browser.
// Query params: root, epoch, file, maxBytes, format (html/plain).
func (api *DebugApi) ServeLogPreview(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	params := LogFileParams{
		Root:     strings.TrimSpace(query.Get("root")),
		Epoch:    strings.TrimSpace(query.Get("epoch")),
		FileName: strings.TrimSpace(query.Get("file")),
		Format:   strings.TrimSpace(query.Get("format")),
	}
	if params.FileName == "" {
		http.Error(w, "missing file parameter", http.StatusBadRequest)
		return
	}
	if maxBytes := strings.TrimSpace(query.Get("maxBytes")); maxBytes != "" {
		if parsed, err := strconv.ParseInt(maxBytes, 10, 64); err == nil {
			params.MaxBytes = parsed
		}
	}

	content, err := api.GetLogFileContent(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if strings.EqualFold(params.Format, "html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, content); err != nil {
		logger.Error("ServeLogPreview write error: %v", err)
	}
}

func renderColorLogHTML(content, fileName string) string {
	if fileName == "" {
		fileName = "Debug log"
	}
	var buf bytes.Buffer
	buf.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>")
	template.HTMLEscape(&buf, []byte(fileName))
	buf.WriteString("</title>")
	buf.WriteString("<style>")
	buf.WriteString("body{background:#0b0c10;color:#c5c6c7;font-family:Consolas,monospace;margin:0;padding:16px;}")
	buf.WriteString("pre{white-space:pre-wrap;word-break:break-word;line-height:1.4;}")
	buf.WriteString(".level-error{color:#ff6b6b;}")
	buf.WriteString(".level-warn{color:#ffd166;}")
	buf.WriteString(".level-info{color:#4ecdc4;}")
	buf.WriteString(".level-debug{color:#add8e6;}")
	buf.WriteString(".level-trace{color:#b084f9;}")
	buf.WriteString("</style></head><body><pre>")

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		class := classifyLogLine(line)
		if class != "" {
			buf.WriteString(`<span class="` + class + `">`)
		}
		template.HTMLEscape(&buf, []byte(line))
		if class != "" {
			buf.WriteString("</span>")
		}
		buf.WriteString("\n")
	}

	buf.WriteString("</pre></body></html>")
	return buf.String()
}

func classifyLogLine(line string) string {
	upper := strings.ToUpper(line)
	switch {
	case strings.Contains(upper, "[ERROR"):
		return "level-error"
	case strings.Contains(upper, "[WARN"):
		return "level-warn"
	case strings.Contains(upper, "[INFO"):
		return "level-info"
	case strings.Contains(upper, "[DEBUG"):
		return "level-debug"
	case strings.Contains(upper, "[TRACE"):
		return "level-trace"
	default:
		return ""
	}
}

// HandleLogStreamWS mở WebSocket để stream nội dung log theo thời gian thực.
// Query params:
//   - file:     tên file log (bắt buộc)
//   - epoch:    số epoch (mặc định epoch hiện tại)
//   - root:     thư mục gốc logs (mặc định app.config.LogPath hoặc loggerfile global dir)
//   - follow:   true/false, nếu true và epoch rỗng thì tự động chuyển sang epoch mới (mặc định true khi epoch rỗng)
func (api *DebugApi) HandleLogStreamWS(w http.ResponseWriter, r *http.Request) {
	conn, err := logStreamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	query := r.URL.Query()
	fileName := strings.TrimSpace(query.Get("file"))
	if fileName == "" {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("error: missing file parameter"))
		return
	}

	epochParam := strings.TrimSpace(query.Get("epoch"))
	root := strings.TrimSpace(query.Get("root"))
	if root == "" {
		if api.App != nil && api.App.config != nil && strings.TrimSpace(api.App.config.LogPath) != "" {
			root = api.App.config.LogPath
		} else {
			root = loggerfile.GetGlobalLogDir()
		}
	}

	followParam := strings.ToLower(strings.TrimSpace(query.Get("follow")))
	follow := false
	switch followParam {
	case "", "1", "true", "yes":
		follow = (epochParam == "") || followParam != ""
	case "0", "false", "no":
		follow = false
	default:
		if epochParam == "" {
			follow = true
		}
	}

	ctx := r.Context()
	api.streamLogLoop(ctx, conn, root, epochParam, fileName, follow)
}

func (api *DebugApi) streamLogLoop(ctx context.Context, conn *websocket.Conn, root, epoch, fileName string, follow bool) {
	defer conn.Close()

	currentEpoch := epoch
	if strings.TrimSpace(currentEpoch) == "" {
		currentEpoch = fmt.Sprintf("%d", loggerfile.GetGlobalEpoch())
	}

	var logFile *os.File
	var offset int64
	var currentPath string
	var lastErrorMessage string
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	sendMessage := func(message string) {
		if strings.TrimSpace(message) == "" {
			return
		}
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(message))
	}

	openLogFile := func(targetEpoch string) {
		if logFile != nil {
			_ = logFile.Close()
			logFile = nil
			offset = 0
		}

		fullPath, err := loggerfile.ResolveLogFilePath(root, targetEpoch, fileName)
		if err != nil {
			errMsg := fmt.Sprintf("error: %v", err)
			if errMsg != lastErrorMessage {
				sendMessage(errMsg)
				lastErrorMessage = errMsg
			}
			return
		}

		file, err := os.Open(fullPath)
		if err != nil {
			errMsg := fmt.Sprintf("error: %v", err)
			if errMsg != lastErrorMessage {
				sendMessage(errMsg)
				lastErrorMessage = errMsg
			}
			return
		}

		logFile = file
		currentPath = fullPath
		info, statErr := logFile.Stat()
		if statErr == nil {
			offset = info.Size()
		} else {
			offset = 0
		}
		lastErrorMessage = ""
		sendMessage(fmt.Sprintf("info: streaming %s (offset %d)", currentPath, offset))
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		targetEpoch := currentEpoch
		if follow && strings.TrimSpace(epoch) == "" {
			targetEpoch = fmt.Sprintf("%d", loggerfile.GetGlobalEpoch())
		}

		if logFile == nil || targetEpoch != currentEpoch {
			currentEpoch = targetEpoch
			openLogFile(currentEpoch)
		}

		if logFile != nil {
			info, err := logFile.Stat()
			if err != nil {
				sendMessage(fmt.Sprintf("error: stat %s: %v", currentPath, err))
				_ = logFile.Close()
				logFile = nil
				offset = 0
			} else {
				size := info.Size()
				if size < offset {
					// file truncated
					offset = 0
				}
				if size > offset {
					toRead := size - offset
					if toRead > maxLogChunkBytes {
						toRead = maxLogChunkBytes
					}
					buf := make([]byte, toRead)
					n, readErr := logFile.ReadAt(buf, offset)
					if n > 0 {
						offset += int64(n)
						chunk := buf[:n]
						if len(chunk) > 0 {
							sendMessage(string(chunk))
						}
					}
					if readErr != nil && !errors.Is(readErr, io.EOF) {
						sendMessage(fmt.Sprintf("error: read %s: %v", currentPath, readErr))
					}
				}
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ListManagedConnections trả về danh sách các kết nối đang được ConnectionsManager quản lý.
func (api *DebugApi) ListManagedConnections(ctx context.Context, params *ConnectionListParams) ([]ConnectionInfo, error) {
	if api.App == nil || api.App.connectionsManager == nil {
		return nil, errors.New("connections manager is not available")
	}
	if params == nil {
		params = &ConnectionListParams{}
	}

	filterType := strings.TrimSpace(strings.ToLower(params.Type))
	typeFilterIdx := -1
	if filterType != "" && filterType != mt_common.NONE_TYPE {
		typeFilterIdx = mt_common.MapConnectionTypeToIndex(filterType)
		if typeFilterIdx == mt_common.NONE_IDX {
			return nil, fmt.Errorf("unknown connection type: %s", filterType)
		}
	}

	parentConn := api.App.connectionsManager.ParentConnection()
	summaries := make([]ConnectionInfo, 0)

	addrToString := func(addr net.Addr) string {
		if addr == nil {
			return ""
		}
		return addr.String()
	}

	// Timeout tổng cho toàn bộ hàm
	const totalTimeout = 10 * time.Second

	// Tạo context với timeout tổng
	timeoutCtx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()

	for typeIdx := 0; typeIdx < pkg_network.MaxConnectionTypes; typeIdx++ {
		// Kiểm tra timeout trước mỗi iteration
		select {
		case <-timeoutCtx.Done():
			logger.Warn("ListManagedConnections: timeout trước khi xử lý xong tất cả connection types")
			return summaries, nil // Trả về kết quả đã có
		case <-ctx.Done():
			return summaries, ctx.Err()
		default:
		}

		if typeFilterIdx >= 0 && typeIdx != typeFilterIdx {
			continue
		}

		conns := api.App.connectionsManager.ConnectionsByType(typeIdx)
		if len(conns) == 0 {
			continue
		}

		typeName := mt_common.MapIndexToConnectionType(typeIdx)

		// Parallel processing cho nhiều connections
		// Batch processing để tránh tạo quá nhiều goroutines
		const batchSize = 50
		connList := make([]struct {
			addr common.Address
			conn network.Connection
		}, 0, len(conns))

		for addr, conn := range conns {
			if conn != nil {
				connList = append(connList, struct {
					addr common.Address
					conn network.Connection
				}{addr, conn})
			}
		}

		// Xử lý theo batch với worker pool
		for i := 0; i < len(connList); i += batchSize {
			select {
			case <-timeoutCtx.Done():
				logger.Warn("ListManagedConnections: timeout khi xử lý connections, trả về kết quả một phần")
				return summaries, nil
			case <-ctx.Done():
				return summaries, ctx.Err()
			default:
			}

			end := i + batchSize
			if end > len(connList) {
				end = len(connList)
			}

			batch := connList[i:end]
			var batchWg sync.WaitGroup
			batchResults := make([]ConnectionInfo, 0, len(batch))
			var batchMu sync.Mutex

			// Xử lý batch với goroutines
			for _, item := range batch {
				batchWg.Add(1)
				go func(addr common.Address, conn network.Connection) {
					defer batchWg.Done()

					// Kiểm tra timeout trong goroutine
					select {
					case <-timeoutCtx.Done():
						return // Timeout, bỏ qua connection này
					case <-ctx.Done():
						return
					default:
					}

					// Tất cả methods giờ non-blocking với cache (< 1ms)
					info := ConnectionInfo{
						Type:            conn.Type(), // Non-blocking với cache
						Address:         addr.Hex(),
						RemoteAddr:      conn.RemoteAddrSafe(), // Non-blocking với cache
						Connected:       conn.IsConnect(),      // Non-blocking với cache
						IsParent:        parentConn != nil && parentConn == conn,
						TcpRemoteAddr:   addrToString(conn.TcpRemoteAddr()), // Non-blocking với cache
						TcpLocalAddr:    addrToString(conn.TcpLocalAddr()),  // Non-blocking với cache
						ConnectionLabel: typeName,
					}

					batchMu.Lock()
					batchResults = append(batchResults, info)
					batchMu.Unlock()
				}(item.addr, item.conn)
			}

			// Wait với timeout để tránh block vô hạn
			done := make(chan struct{})
			go func() {
				batchWg.Wait()
				close(done)
			}()

			select {
			case <-done:
				// Tất cả goroutines đã hoàn thành
				summaries = append(summaries, batchResults...)
			case <-timeoutCtx.Done():
				logger.Warn("ListManagedConnections: timeout khi chờ batch hoàn thành, trả về kết quả một phần")
				// Vẫn append kết quả đã có (một số goroutines có thể đã hoàn thành)
				summaries = append(summaries, batchResults...)
				return summaries, nil
			case <-ctx.Done():
				return summaries, ctx.Err()
			}
		}
	}

	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Type == summaries[j].Type {
			return summaries[i].Address < summaries[j].Address
		}
		return summaries[i].Type < summaries[j].Type
	})

	return summaries, nil
}
