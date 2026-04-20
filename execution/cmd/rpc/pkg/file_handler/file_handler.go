package file_handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/crypto"
	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/file_handler/abi_file"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	file_model "github.com/meta-node-blockchain/meta-node/pkg/models/file_model"
	"github.com/meta-node-blockchain/meta-node/pkg/quic_network"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/quic-go/quic-go"
	"github.com/shirou/gopsutil/mem"
)

const MAX_SIZE_CHUNK = 250 * 1024
const DEFAULT_MAX_MEMORY_USAGE_PERCENT = 99

const MAX_CHUNK = 8192 // 2GB
// Nó không còn chứa chainState nữa.
type FileHandlerNoReceipt struct {
	abi            abi.ABI
	comm           BlockchainCommunicator
	uploadProgress sync.Map
	fileInfoCache  sync.Map
	// mapMutex và cacheMutex đã bị XÓA BỎ
	confirmationChannel chan file_model.ConfirmationJob
	connPool1           []quic.Connection
	connPool2           []quic.Connection
	//
	cachedRustServers []string   // Cache cho địa chỉ 2 server
	initMutex         sync.Mutex // (Để bảo vệ việc khởi tạo)
	isInitialized     bool       // (Cờ báo đã khởi tạo thành công)
	//
	pool1Mutex sync.RWMutex // Mutex cho pool 1 (Giữ nguyên)
	pool2Mutex sync.RWMutex // Mutex cho pool 2 (Giữ nguyên)
	//
	chunkSemaphore        chan struct{} // Semaphore để giới hạn số luồng xử lý chunk đồng thời
	maxMemoryUsagePercent float64
	processingChunkCount  atomic.Int64
}

var (
	tcpHandlerInstance *FileHandlerNoReceipt
	tcpOnce            sync.Once

	inProcessHandlerInstance *FileHandlerNoReceipt
	inProcessOnce            sync.Once
)

const (
	CONNECTION_POOL_SIZE = 50
	MAX_SEND_RETRIES     = 3
)

func createAndStartFileHandler(comm BlockchainCommunicator) (*FileHandlerNoReceipt, error) {
	var err error

	var parsedABI abi.ABI
	parsedABI, err = abi.JSON(strings.NewReader(abi_file.FileABI))
	if err != nil {
		return nil, err // Trả về lỗi ngay
	}
	// Tạo semaphore với số lượng bằng số CPU cores để giới hạn số luồng xử lý
	maxConcurrentChunks := int(float64(runtime.NumCPU()))
	if maxConcurrentChunks < 1 {
		maxConcurrentChunks = 1 // Đảm bảo ít nhất 1 luồng
	}
	instance := &FileHandlerNoReceipt{
		abi:                   parsedABI,
		comm:                  comm,
		confirmationChannel:   make(chan file_model.ConfirmationJob, 1000),
		cachedRustServers:     make([]string, 2),
		pool1Mutex:            sync.RWMutex{},
		pool2Mutex:            sync.RWMutex{},
		chunkSemaphore:        make(chan struct{}, maxConcurrentChunks),
		maxMemoryUsagePercent: DEFAULT_MAX_MEMORY_USAGE_PERCENT,
	}
	go instance.startConfirmationWorker()
	go instance.waitForMemoryAvailability()
	go instance.monitorCacheHealth()

	if err != nil {
		return nil, err
	}
	return instance, nil
}

// sử lý trên tcp client
func GetFileHandlerTCP(c *client_tcp.Client, config *tcp_config.ClientConfig) (*FileHandlerNoReceipt, error) {
	var err error
	tcpOnce.Do(func() {
		comm := NewTCPCommunicator(c, config)
		// Gọi hàm tạo mới và gán vào instance TCP
		tcpHandlerInstance, err = createAndStartFileHandler(comm)
	})

	if err != nil {
		return nil, err // Trả về lỗi nếu khởi tạo thất bại
	}
	return tcpHandlerInstance, nil
}

func (h *FileHandlerNoReceipt) HandleFileTransactionNoReceipt(
	ctx context.Context,
	tx types.Transaction,
) (bool, error) {
	blockTime := uint64(time.Now().Unix())
	inputData := tx.CallData().Input()
	if len(inputData) < 4 {
		err := fmt.Errorf("FileHandler: Dữ liệu input không hợp lệ")
		return false, err
	}
	logger.Info("____HandleFileTransactionNoReceipt: %v", inputData)
	method, err := h.abi.MethodById(inputData[:4])
	if err != nil {
		err := fmt.Errorf("FileHandler: Lỗi khi lấy method từ input data: %v", err)
		return true, err
	}
	var logicErr error
	var isCall bool = false
	switch method.Name {
	case "uploadChunk":
		isCall = true
		if !h.isInitialized {
			h.initMutex.Lock()
			defer h.initMutex.Unlock()
			if !h.isInitialized {
				err := h.initializeServerCacheAndPools(tx)
				if err != nil {
					logger.Error("Lỗi khi khởi tạo cache server: %v", err)
				}
				h.isInitialized = true
			}
		}
		_, logicErr = h.HandleUploadChunk(tx, method, inputData[4:], blockTime)
	default:
		return false, nil
	}
	if logicErr != nil {
		return true, logicErr
	}
	if isCall {
		return true, nil
	}
	return false, nil
}
func (h *FileHandlerNoReceipt) monitorCacheHealth() {
	fileLogger, _ := loggerfile.NewFileLogger("file_handler_debug.log")
	// Bạn có thể đổi lại 10s nếu muốn
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		uploadProgressCount := 0
		h.uploadProgress.Range(func(k, v interface{}) bool {
			uploadProgressCount++
			return true
		})

		fileInfoCount := 0
		h.fileInfoCache.Range(func(k, v interface{}) bool {
			fileInfoCount++
			return true
		})

		// <<< THÊM MỚI: Lấy số chunk đang xử lý đồng thời
		activeChunks := h.processingChunkCount.Load()

		fileLogger.Info("Tổng quan: %d files đang upload | %d chunks đang xử lý (đồng thời) | %d fileInfo cache",
			uploadProgressCount, activeChunks, fileInfoCount)

		if uploadProgressCount > 100 || fileInfoCount > 100 {
			logger.Error("⚠️  FileHandler cache leak! Progress: %d, Info: %d",
				uploadProgressCount, fileInfoCount)
		}

		// Log chunkSemaphore status
		fileLogger.Info("chunkSemaphore available: %d/%d",
			cap(h.chunkSemaphore)-len(h.chunkSemaphore), cap(h.chunkSemaphore))

		// --- Logger chi tiết từng file (nếu bạn vẫn muốn) ---
		// (Logger này không thay đổi, nó chỉ log progress đã hoàn thành)
		h.uploadProgress.Range(func(key, value interface{}) bool {
			fileKeyStr, ok := key.(string)
			if !ok {
				return true
			}
			progress, ok := value.(*file_model.FileUploadProgress) // <<< THAY ĐỔI
			if !ok {
				return true
			}

			// Đọc giá trị atomic một cách an toàn
			uploadedCount := progress.UploadedChunks.Load() // <<< THAY ĐỔI
			// Chuyển đổi để log
			uploaded := new(big.Int).SetUint64(uploadedCount) // <<< THAY ĐỔI
			total := progress.TotalChunks

			shortKey := fileKeyStr
			if len(shortKey) > 10 {
				shortKey = shortKey[:10]
			}

			fileLogger.Info("[ProgressMonitor] File: %s... | Uploaded: %s / %s",
				shortKey, uploaded.String(), total.String())
			return true
		})
	}
}

// handleUploadChunk: Chặn giao dịch, giải mã và gửi chunk đến Rust server.
func (h *FileHandlerNoReceipt) HandleUploadChunk(
	tx types.Transaction,
	method *abi.Method,
	inputData []byte,
	blockTime uint64,
) ([]types.EventLog, error) {
	h.processingChunkCount.Add(1)
	defer h.processingChunkCount.Add(-1)
	logger.Info("Bắt đầu xử lý uploadChunk cho tx %s", tx.Hash().Hex())
	// check
	start := time.Now()
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("Lỗi khi unpack input data: %v", err)
	}
	fileKey, _ := args[0].([32]byte)
	chunkData, _ := args[1].([]byte)
	chunkIndex, _ := args[2].(*big.Int)
	merkleProofHashes, _ := args[3].([][32]byte)

	fileKeyStr := hex.EncodeToString(fileKey[:])
	chunkIndexInt := int(chunkIndex.Int64())
	logPrefix := fmt.Sprintf("[Chunk %d -k %s]", chunkIndexInt, fileKeyStr)
	fileTimeLogger, _ := loggerfile.NewFileLogger("fileTimeLogger.log")
	fileTimeLogger.Info("%s Bắt đầu xử lý upload chunk", logPrefix, start.Format("2006-01-02 15:04:05.999"))
	defer func() {
		endTime := time.Now()
		duration := endTime.Sub(start) // Hoặc time.Since(start)
		formattedEndTime := endTime.Format("2006-01-02 15:04:05.999")
		fileTimeLogger.Info(
			"%s Tổng thời gian xử lý: %v. Kết thúc lúc: %s",
			logPrefix,
			duration,
			formattedEndTime,
		)
	}()
	var fileInfo *file_model.FileInfo
	val, found := h.fileInfoCache.Load(fileKeyStr)
	if !found {
		fileInfo, err = h.comm.GetFileInfo(fileKey, tx)
		if err != nil {
			return nil, fmt.Errorf("Lỗi khi tạo transaction getFileInfo: %v", err)
		}

		actualVal, loaded := h.fileInfoCache.LoadOrStore(fileKeyStr, fileInfo)
		if loaded {
			fileInfo = actualVal.(*file_model.FileInfo)
		}
	} else {
		fileInfo = val.(*file_model.FileInfo)
	}
	if fileInfo.TotalChunks.Cmp(big.NewInt(int64(MAX_CHUNK))) > 0 {
		return nil, fmt.Errorf("số chunk vượt quá giới hạn %d , yêu cầu < 2G", MAX_CHUNK)
	}
	if fileInfo.Status == 1 {
		h.uploadProgress.Delete(fileKeyStr)
		h.fileInfoCache.Delete(fileKeyStr)
		return nil, fmt.Errorf("file đã ở trạng thái Active, không thể upload thêm chunk %s filekey %s", chunkIndex, fileKeyStr)
	}
	if tx.FromAddress() != fileInfo.OwnerAddress {
		return nil, fmt.Errorf("chỉ chủ sở hữu file mới có thể upload chunk")
	}
	if len(chunkData) > MAX_SIZE_CHUNK {
		return nil, fmt.Errorf("kích thước chunk vượt quá giới hạn %d KB", MAX_SIZE_CHUNK/1024)
	}
	startVerifyMerkle := time.Now()
	merkleRoot := fileInfo.MerkleRoot
	leafHash := crypto.Keccak256Hash(chunkData)
	computedHash := leafHash[:]
	for level := 0; level < len(merkleProofHashes); level++ {
		siblingHash := merkleProofHashes[level]
		levelIndex := chunkIndex.Uint64() >> uint(level)
		var combined []byte
		if levelIndex%2 == 0 {
			combined = append(computedHash, siblingHash[:]...)
		} else {
			combined = append(siblingHash[:], computedHash...)
		}
		computedHash = crypto.Keccak256(combined)
	}
	if !bytes.Equal(computedHash, merkleRoot[:]) {
		logger.Error("INVALID Merkle Proof for file %s, chunk %d. Computed: %x, Expected: %x", fileKeyStr, chunkIndexInt, computedHash, merkleRoot)
		return nil, fmt.Errorf("merkle proof không hợp lệ cho chunk %d", chunkIndexInt)
	}
	fileTimeLogger.Info("%s Xác thực Merkle Proof (OK): %v", logPrefix, time.Since(startVerifyMerkle))
	startSendChunk := time.Now()
	// --- THAY ĐỔI: Lấy Progress từ sync.Map ---
	var progress *file_model.FileUploadProgress
	val, found = h.uploadProgress.Load(fileKeyStr)
	if !found {
		newProgress := &file_model.FileUploadProgress{
			TotalChunks: fileInfo.TotalChunks,
		}
		actualVal, loaded := h.uploadProgress.LoadOrStore(fileKeyStr, newProgress)
		if loaded {
			progress = actualVal.(*file_model.FileUploadProgress) // <<< THAY ĐỔI
		} else {
			progress = newProgress
		}
	} else {
		progress = val.(*file_model.FileUploadProgress)
	}

	var conn quic.Connection
	isServer1 := chunkIndexInt%2 == 0
	poolIndex := (chunkIndexInt / 2) % CONNECTION_POOL_SIZE
	conn, err = h.getAndRenewConn(isServer1, poolIndex, fileKeyStr, chunkIndexInt) // Truyền log ID
	if err != nil {
		return nil, fmt.Errorf("không thể lấy/tạo kết nối cho chunk %d: %v", chunkIndexInt, err)
	}
	err = h.sendChunk(conn, isServer1, poolIndex, fileKeyStr, chunkIndexInt, chunkData, fileInfo.Signature, merkleProofHashes, merkleRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to send chunk %d: %v", chunkIndexInt, err)
	}
	durationSendChunk := time.Since(startSendChunk)
	fileTimeLogger.Info("%s Gửi chunk (sendChunk): %v", logPrefix, durationSendChunk)
	startUpdateCounter := time.Now()
	// --- BƯỚC 2: Cập nhật bộ đếm ---
	var isComplete bool
	newUploadedCount := progress.UploadedChunks.Add(1)
	uploadedBigInt := new(big.Int).SetUint64(newUploadedCount)
	isComplete = (uploadedBigInt.Cmp(progress.TotalChunks) >= 0)
	if isComplete {
		job := file_model.ConfirmationJob{
			FileKey: fileKey,
			Tx:      tx,
		}
		h.confirmationChannel <- job

	}
	// logger.Info("✅ Hoàn thành upload chunk %d", chunkIndexInt)
	durationUpdateCounter := time.Since(startUpdateCounter)
	fileTimeLogger.Info("%s Cập nhật counter và kiểm tra hoàn thành: %v, count %d", logPrefix, durationUpdateCounter, uploadedBigInt)
	// logger.Warn("Uploaded chunk %d / %d for file %s", uploadedCount, progress.progress.TotalChunks, fileKeyStr)
	return nil, nil
}

func (h *FileHandlerNoReceipt) startConfirmationWorker() {
	for job := range h.confirmationChannel {
		err := h.sendConfirmationTransaction(job)
		if err != nil {
			logger.Error("Worker: Failed to send confirmation for file %s: %v", hex.EncodeToString(job.FileKey[:]), err)
		} else {
			logger.Info("Worker: Successfully sent confirmation transaction for file %s.", hex.EncodeToString(job.FileKey[:]))
		}
		fileKeyStr := hex.EncodeToString(job.FileKey[:])
		h.uploadProgress.Delete(fileKeyStr)
		h.fileInfoCache.Delete(fileKeyStr)
	}
}

func (h *FileHandlerNoReceipt) sendChunk(
	initialConn quic.Connection,
	isServer1 bool,
	poolIndex int,
	fileKey string,
	chunkIndex int,
	chunkData []byte,
	signature string,
	merkleProofHashes [][32]byte,
	merkleRoot [32]byte,
) error {
	currentConn := initialConn
	var lastErr error
	for i := 0; i < MAX_SEND_RETRIES; i++ {
		lastErr = quic_network.SendChunkToRustServerQuic(currentConn, fileKey, chunkIndex, chunkData, signature, merkleProofHashes, merkleRoot)
		if lastErr == nil {
			return nil
		}
		if errors.Is(lastErr, context.DeadlineExceeded) || strings.Contains(lastErr.Error(), "deadline exceeded") {
			logger.Warn("[file: %s, chunk: %d] Stream timeout, sẽ thử stream mới...", fileKey, chunkIndex)
			continue // vòng lặp sẽ mở stream mới trên cùng connection
		}
		logger.Error("[file: %s, chunk: %d] Lỗi gửi chunk (lần thử %d/%d): %v. Đang lấy kết nối mới...", fileKey, chunkIndex, i+1, MAX_SEND_RETRIES, lastErr) // <<< LOG
		newConn, reconErr := h.getAndRenewConn(isServer1, poolIndex, fileKey, chunkIndex)                                                                     // <<< PASS LOG IDs
		if reconErr != nil {
			time.Sleep(100 * time.Millisecond)
		} else {
			currentConn = newConn
		}
	}
	h.isInitialized = false
	return fmt.Errorf("❌❌ [file: %s, chunk: %d] không thể gửi chunk sau %d lần thử: %v",
		fileKey, chunkIndex, MAX_SEND_RETRIES, lastErr)
}

func (h *FileHandlerNoReceipt) getAndRenewConn(isServer1 bool, poolIndex int, fileKeyStr string, chunkIndexInt int) (quic.Connection, error) {
	var pool []quic.Connection
	var addr string
	var serverName string
	var mtx *sync.RWMutex // Trỏ tới mutex đúng

	if isServer1 {
		pool = h.connPool1
		addr = h.cachedRustServers[0]
		serverName = "Server 1"
		mtx = &h.pool1Mutex // Dùng mutex 1
	} else {
		pool = h.connPool2
		addr = h.cachedRustServers[1]
		serverName = "Server 2"
		mtx = &h.pool2Mutex // Dùng mutex 2
	}

	// --- 1. FAST-PATH ---
	conn := pool[poolIndex]
	if conn != nil && conn.Context().Err() == nil {
		return conn, nil
	}

	// --- 2. SLOW-PATH ---
	mtx.Lock()
	defer mtx.Unlock()
	conn = pool[poolIndex]
	if conn != nil && conn.Context().Err() == nil {
		return conn, nil
	}

	newConn, err := quic_network.CreateQuicConnection(addr)
	if err != nil {
		logger.Error("[file: %s, chunk: %d] [ConnPool] Lỗi khi tái kết nối (%s, Index %d): %v", fileKeyStr, chunkIndexInt, serverName, poolIndex, err) // <<< LOG
		return nil, err
	}

	pool[poolIndex] = newConn
	return newConn, nil
}

func (h *FileHandlerNoReceipt) sendConfirmationTransaction(job file_model.ConfirmationJob) error {
	receipt, err := h.comm.SendConfirmation(job.FileKey, job.Tx)
	if err != nil {
		return fmt.Errorf("failed to create confirmation transaction: %v", err)
	}
	logger.Error("Receipt: %v", receipt)
	return nil
}

func (h *FileHandlerNoReceipt) waitForMemoryAvailability() error {
	if h.maxMemoryUsagePercent <= 0 {
		return nil
	}
	const (
		gcAttemptThreshold   = 20
		maxWaitAttempts      = 50
		waitIntervalDuration = 200 * time.Millisecond
	)
	logged := false
	attempts := 0
	for {
		if h.isMemoryUsageWithinLimit() {
			return nil
		}
		if !logged {
			logger.Warn("Đang tạm dừng upload chunk do RAM hệ thống đã vượt %.2f%%", h.maxMemoryUsagePercent)
			logged = true
		}
		attempts++
		if attempts == gcAttemptThreshold {
			logger.Warn("RAM vẫn cao sau %d lần kiểm tra, thực hiện runtime.GC() và debug.FreeOSMemory()", attempts)
			runtime.GC()
			debug.FreeOSMemory()
		}
		if attempts >= maxWaitAttempts {
			return fmt.Errorf("RAM vẫn vượt ngưỡng %.2f%% sau khi chờ %d lần", h.maxMemoryUsagePercent, attempts)
		}
		time.Sleep(waitIntervalDuration)
	}
}

func (h *FileHandlerNoReceipt) isMemoryUsageWithinLimit() bool {
	if h.maxMemoryUsagePercent <= 0 {
		return true
	}
	vmem, err := mem.VirtualMemory()
	if err != nil {
		logger.Error("Không thể đọc thông tin RAM hệ thống: %v", err)
		return true
	}
	return vmem.UsedPercent < h.maxMemoryUsagePercent
}

func (h *FileHandlerNoReceipt) initializeServerCacheAndPools(tx types.Transaction) error {
	servers, err := h.comm.GetRustServerAddresses(tx)
	if err != nil {
		return fmt.Errorf("lỗi tạo tx getList: %v", err)
	}
	if len(servers) < 2 {
		return fmt.Errorf("lỗi: Contract trả về ít hơn 2 server (có %d)", len(servers))
	}

	h.cachedRustServers[0] = servers[0]
	h.cachedRustServers[1] = servers[1]

	h.connPool1 = make([]quic.Connection, CONNECTION_POOL_SIZE)
	h.connPool2 = make([]quic.Connection, CONNECTION_POOL_SIZE)
	var wg sync.WaitGroup
	wg.Add(CONNECTION_POOL_SIZE * 2)
	var connErr error
	for i := 0; i < CONNECTION_POOL_SIZE; i++ {
		go func(idx int) { // Server 1
			defer wg.Done()
			conn, err := quic_network.CreateQuicConnection(h.cachedRustServers[0])
			if err != nil {
				logger.Error("[Init] Failed to create initial QUIC connection to server 1 (index %d): %v", idx, err) // <<< LOG
				if connErr == nil {
					connErr = err
				}
			}
			h.connPool1[idx] = conn
		}(i)

		go func(idx int) { // Server 2
			defer wg.Done()
			conn, err := quic_network.CreateQuicConnection(h.cachedRustServers[1])
			if err != nil {
				logger.Error("[Init] Failed to create initial QUIC connection to server 2 (index %d): %v", idx, err) // <<< LOG
				if connErr == nil {
					connErr = err
				}
			}
			h.connPool2[idx] = conn
		}(i)
	}
	wg.Wait()
	if connErr != nil {
		return fmt.Errorf("lỗi khi tạo connection pools: %v", connErr)
	}
	logger.Info("[Init] FileHandler: Khởi tạo server cache và connection pools thành công. %v", h.cachedRustServers) // <<< LOG
	return nil
}
