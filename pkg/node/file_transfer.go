package node

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// FileChunkSize defines the buffer size for reading/writing file data.
const FileChunkSize = 1024 * 64 // 64KB

const (
	FileRequestProtocol  = "/file-request/1.0.0"  // Kept for reference
	FileTransferProtocol = "/file-transfer/1.0.0" // Kept for reference
)

// ---- State Management cho việc nhận Split Archive ----

// ArchivePartState lưu trữ trạng thái nhận các phần của một split archive.
type ArchivePartState struct {
	BaseArchiveName string
	TotalParts      int
	ReceivedParts   map[string]bool
	TempDir         string
	FirstPartPath   string
	LastUpdate      time.Time
}

var receiveStateMutex sync.Mutex
var receivingStates = make(map[string]*ArchivePartState)

const SplitInfoPrefix = "SPLIT_INFO:"

// ---- END State Management Split Archive ----

// ---- State Management cho File Synchronization ----

var syncStateMutex sync.Mutex
var expectedSyncItems = make(map[string]bool)
var receivedSyncItems = make(map[string]bool)
var isSyncActive bool = false

func ResetFileSyncState() {
	syncStateMutex.Lock()
	defer syncStateMutex.Unlock()
	expectedSyncItems = make(map[string]bool)
	receivedSyncItems = make(map[string]bool)
	isSyncActive = false
	logger.Info("File sync state reset.")
}

func markItemReceived(receivedItemKey string) bool {
	syncStateMutex.Lock()
	defer syncStateMutex.Unlock()

	if !isSyncActive {
		logger.Warn("Attempted to mark item received outside of active sync session:", receivedItemKey)
		return false
	}

	key := normalizeSyncItemKey(receivedItemKey)
	if _, expected := expectedSyncItems[key]; expected {
		if !receivedSyncItems[key] {
			receivedSyncItems[key] = true
			logger.Info(fmt.Sprintf("Marked sync item '%s' as received. Progress: %d/%d",
				key, len(receivedSyncItems), len(expectedSyncItems)))
			return true
		} else {
			logger.Debug("Sync item already marked as received:", key)
			return true
		}
	} else {
		logger.Warn("Received item '%s' (normalized key: '%s') was not in the expected sync list.", receivedItemKey, key)
		return false
	}
}

func checkSyncComplete() {
	syncStateMutex.Lock()
	defer syncStateMutex.Unlock()
	logger.Info("checkSyncComplete", isSyncActive)

	if !isSyncActive {
		return
	}
	logger.Info("checkSyncComplete 1")

	if len(expectedSyncItems) == 0 {
		logger.Debug("CheckSyncComplete called with empty expected items list.")
		return
	}

	if len(receivedSyncItems) >= len(expectedSyncItems) {
		allMatched := true
		for key := range expectedSyncItems {
			if !receivedSyncItems[key] {
				allMatched = false
				logger.Warn("Sync completion check failed: Missing expected item:", key)
				break
			}
		}

		if allMatched {
			logger.Info("✅ All expected sync items received. Updating state to 2.")
			expectedSyncItems = make(map[string]bool)
			receivedSyncItems = make(map[string]bool)
			isSyncActive = false
			logger.Info("File sync session completed and state reset.")
		} else {
			logger.Debug("Sync completion check: Received count matches expected, but some keys mismatch.")
		}
	} else {
		logger.Debug(fmt.Sprintf("Sync completion check: Still waiting for items (%d/%d received).", len(receivedSyncItems), len(expectedSyncItems)))
	}
}

func normalizeSyncItemKey(rawName string) string {
	name := filepath.Base(rawName)
	ext := filepath.Ext(name)
	if ext == ".7z" || ext == ".gz" {
		name = strings.TrimSuffix(name, ext)
		if filepath.Ext(name) == ".tar" {
			name = strings.TrimSuffix(name, ".tar")
		}
	}
	return name
}

// ---- END State Management File Synchronization ----

// HandleFileTransfer xử lý việc nhận file qua TCP route "FileTransfer".
// Request body chứa file data đã được encode dưới dạng JSON metadata + raw content.
func (node *HostNode) HandleFileTransfer(request network.Request) error {
	body := request.Message().Body()
	if len(body) == 0 {
		return fmt.Errorf("empty file transfer data")
	}

	// Parse the body as: JSON metadata line + file content
	reader := bufio.NewReader(strings.NewReader(string(body)))

	err := node.processIncomingData(reader)
	if err != nil {
		logger.Error(fmt.Sprintf("Error handling file transfer: %v", err))
		return err
	}

	logger.Info("Successfully handled file transfer via TCP")
	checkSyncComplete()
	return nil
}

// HandleFileRequest xử lý yêu cầu file/folder sync qua TCP route "FileRequest".
// Master nhận request và gửi lại folder data.
func (node *HostNode) HandleFileRequest(request network.Request) error {
	bodyStr := strings.TrimSpace(string(request.Message().Body()))
	if bodyStr == "" {
		return fmt.Errorf("empty file request")
	}

	logger.Info(fmt.Sprintf("📂 Received file request: %s", bodyStr))

	conn := request.Connection()
	if conn == nil || !conn.IsConnect() {
		return fmt.Errorf("connection not available for file request response")
	}

	// Gửi "OK" response
	if node.MessageSender != nil {
		err := node.MessageSender.SendBytes(conn, "FileRequestResponse", []byte("OK"))
		if err != nil {
			return fmt.Errorf("failed to send OK response: %w", err)
		}
	}

	// Gửi folder nếu request là "sys" (folder sync)
	if bodyStr == "sys" {
		logger.Info(fmt.Sprintf("Starting folder sync for root path: %s", node.rootPath))
		go func() {
			if err := node.SendFolderViaTCP(conn, node.rootPath, 1000); err != nil {
				logger.Error(fmt.Sprintf("Failed to send folder via TCP: %v", err))
			}
		}()
	}

	return nil
}

// processIncomingData xử lý dữ liệu nhận từ TCP (thay processIncomingStream).
func (node *HostNode) processIncomingData(reader *bufio.Reader) error {
	firstLine, err := reader.ReadString('\n')
	if err != nil {
		if err == io.EOF && firstLine == "" {
			return fmt.Errorf("empty data received")
		}
		return fmt.Errorf("lỗi khi đọc dòng đầu tiên: %w", err)
	}
	firstLine = strings.TrimSpace(firstLine)

	var partFileName string
	var fileSize int64
	var isSplitPart bool
	var totalParts int
	var baseArchiveName string

	if strings.HasPrefix(firstLine, SplitInfoPrefix) {
		isSplitPart = true
		partsInfo := strings.Split(strings.TrimPrefix(firstLine, SplitInfoPrefix), ":")
		if len(partsInfo) != 2 {
			return fmt.Errorf("định dạng split info không hợp lệ: '%s'", firstLine)
		}
		totalParts, err = strconv.Atoi(partsInfo[0])
		if err != nil || totalParts <= 0 {
			return fmt.Errorf("số lượng part không hợp lệ '%s': %w", partsInfo[0], err)
		}
		baseArchiveName = strings.TrimSpace(partsInfo[1])
		if baseArchiveName == "" {
			return fmt.Errorf("tên base archive không hợp lệ trong split info")
		}
		if !strings.HasSuffix(baseArchiveName, ".7z") {
			baseArchiveName += ".7z"
		}

		partFileNameLine, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("lỗi khi đọc tên file part sau split info: %w", err)
		}
		partFileName = strings.TrimSpace(partFileNameLine)

		fileSizeStr, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("lỗi khi đọc kích thước file part: %w", err)
		}
		parsedSize, err := strconv.ParseInt(strings.TrimSpace(fileSizeStr), 10, 64)
		if err != nil {
			return fmt.Errorf("lỗi chuyển đổi kích thước file part '%s': %w", fileSizeStr, err)
		}
		fileSize = parsedSize
		logger.Info(fmt.Sprintf("Receiving part '%s' for split archive '%s' (Total: %d parts, Size: %d bytes)", partFileName, baseArchiveName, totalParts, fileSize))
	} else {
		isSplitPart = false
		partFileName = firstLine
		fileSizeStr, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("lỗi khi đọc kích thước file (đơn): %w", err)
		}
		parsedSize, err := strconv.ParseInt(strings.TrimSpace(fileSizeStr), 10, 64)
		if err != nil {
			return fmt.Errorf("lỗi chuyển đổi kích thước file (đơn) '%s': %w", fileSizeStr, err)
		}
		fileSize = parsedSize
		logger.Info(fmt.Sprintf("Receiving single file '%s' (Size: %d bytes)", partFileName, fileSize))
	}

	if partFileName == "" {
		return fmt.Errorf("tên file nhận được rỗng")
	}
	if fileSize < 0 {
		return fmt.Errorf("kích thước file không hợp lệ: %d", fileSize)
	}
	parentDir := filepath.Dir(node.rootPath)

	finalOutputDirBase := filepath.Join(parentDir, "received_files")
	if err := os.MkdirAll(finalOutputDirBase, 0755); err != nil {
		return fmt.Errorf("lỗi tạo thư mục output chính '%s': %w", finalOutputDirBase, err)
	}
	tempReceiveDir := filepath.Join(filepath.Dir(node.rootPath), "temp_receive")
	err = os.MkdirAll(tempReceiveDir, 0755)
	if err != nil {
		return fmt.Errorf("lỗi tạo thư mục tạm '%s': %w", tempReceiveDir, err)
	}

	var targetFilePath string
	var partState *ArchivePartState
	var stateKey string
	var receivedItemKey string

	if isSplitPart {
		safeBaseName := strings.ReplaceAll(baseArchiveName, string(filepath.Separator), "_")
		safeBaseName = strings.ReplaceAll(safeBaseName, ":", "_")
		archiveTempDir := filepath.Join(tempReceiveDir, safeBaseName+"_parts")
		err = os.MkdirAll(archiveTempDir, 0755)
		if err != nil {
			return fmt.Errorf("lỗi tạo thư mục tạm cho archive '%s': %w", baseArchiveName, err)
		}
		safePartFileName := filepath.Base(partFileName)
		targetFilePath = filepath.Join(archiveTempDir, safePartFileName)
		stateKey = baseArchiveName
		receivedItemKey = normalizeSyncItemKey(baseArchiveName)

		receiveStateMutex.Lock()
		var exists bool
		partState, exists = receivingStates[stateKey]
		if !exists {
			partState = &ArchivePartState{BaseArchiveName: baseArchiveName, TotalParts: totalParts, ReceivedParts: make(map[string]bool), TempDir: archiveTempDir, LastUpdate: time.Now()}
			receivingStates[stateKey] = partState
		} else {
			partState.LastUpdate = time.Now()
			if partState.TotalParts != totalParts {
				partState.TotalParts = totalParts
			}
		}
		receiveStateMutex.Unlock()

		if strings.HasSuffix(safePartFileName, ".001") {
			receiveStateMutex.Lock()
			if partState.FirstPartPath == "" {
				partState.FirstPartPath = targetFilePath
			}
			receiveStateMutex.Unlock()
		}
	} else {
		safeFileName := filepath.Base(partFileName)
		targetFilePath = filepath.Join(tempReceiveDir, safeFileName)
		receivedItemKey = normalizeSyncItemKey(partFileName)
	}

	// --- Nhận dữ liệu file ---
	outFile, err := os.Create(targetFilePath)
	if err != nil {
		return fmt.Errorf("lỗi tạo file đích '%s': %w", targetFilePath, err)
	}
	bytesReceived, err := io.CopyN(outFile, reader, fileSize)
	closeErr := outFile.Close()
	if closeErr != nil {
		logger.Error("Error closing output file: %s: %v", targetFilePath, closeErr)
		return fmt.Errorf("error closing output file '%s': %w", targetFilePath, closeErr)
	}
	if err != nil {
		os.Remove(targetFilePath)
		return fmt.Errorf("lỗi khi nhận dữ liệu file '%s' (received %d/%d bytes): %w", partFileName, bytesReceived, fileSize, err)
	}
	if bytesReceived != fileSize {
		os.Remove(targetFilePath)
		return fmt.Errorf("nhận file '%s' không đủ: nhận %d, dự kiến %d", partFileName, bytesReceived, fileSize)
	}
	logger.Info(fmt.Sprintf("File/Part '%s' received successfully (%d bytes) and saved to '%s'.", partFileName, bytesReceived, targetFilePath))

	// --- Xử lý sau khi nhận ---
	processingSuccessful := false

	if isSplitPart {
		receiveStateMutex.Lock()
		partState.ReceivedParts[partFileName] = true
		partState.LastUpdate = time.Now()
		receivedCount := len(partState.ReceivedParts)
		totalExpected := partState.TotalParts
		firstPartPath := partState.FirstPartPath
		tempDir := partState.TempDir
		receiveStateMutex.Unlock()

		logger.Debug(fmt.Sprintf("Archive '%s': Received %d/%d parts.", stateKey, receivedCount, totalExpected))

		if receivedCount >= totalExpected {
			logger.Info(fmt.Sprintf("All %d parts received for archive '%s'. Attempting decompression.", totalExpected, stateKey))
			if firstPartPath == "" {
				logger.Error(fmt.Sprintf("All parts received for '%s', but the first part path was not recorded!", stateKey))
				receiveStateMutex.Lock()
				delete(receivingStates, stateKey)
				receiveStateMutex.Unlock()
				os.RemoveAll(tempDir)
				return fmt.Errorf("all parts received for '%s', but first part path was not recorded", stateKey)
			}
			finalExtractDir := filepath.Dir(node.rootPath)

			logger.Info(fmt.Sprintf("Decompressing '%s' from '%s' into '%s'", stateKey, firstPartPath, finalExtractDir))
			if err := os.MkdirAll(finalExtractDir, 0755); err != nil {
				return fmt.Errorf("lỗi tạo thư mục giải nén cuối cùng '%s': %w", finalExtractDir, err)
			}

			err = DecompressFolder(firstPartPath, finalExtractDir)
			if err != nil {
				return fmt.Errorf("lỗi giải nén archive '%s' từ part '%s': %w", stateKey, firstPartPath, err)
			}

			logger.Info(fmt.Sprintf("✅ Successfully decompressed split archive '%s' to '%s'.", stateKey, finalExtractDir))
			processingSuccessful = true
			storage.UpdateState(2)
			
			// CLEAR C++ CACHE: C++ cache must be cleared when a snapshot is loaded to prevent stale state reads
			logger.Info("🧹 [SNAPSHOT LOADED] Clearing C++ State Cache to prevent stale reads...")
			mvm.ClearAllStateInstances()

			logger.Debug(fmt.Sprintf("Cleaning up temporary parts directory: %s", tempDir))
			removeErr := os.RemoveAll(tempDir)
			if removeErr != nil {
				logger.Warn("Failed to remove temp parts directory:", tempDir, removeErr)
			}
			receiveStateMutex.Lock()
			delete(receivingStates, stateKey)
			receiveStateMutex.Unlock()
			logger.Debug(fmt.Sprintf("Removed state for completed archive '%s'.", stateKey))
		} else {
			logger.Debug(fmt.Sprintf("Waiting for more parts for archive '%s'...", stateKey))
		}
	} else {
		logger.Info("Processing received single file:", targetFilePath)
		isArchive := strings.HasSuffix(partFileName, ".7z")
		isCompressedFile := strings.HasSuffix(partFileName, ".gz")

		if isArchive || isCompressedFile {
			var baseName string
			if isArchive {
				baseName = strings.TrimSuffix(partFileName, ".7z")
			} else {
				baseName = strings.TrimSuffix(partFileName, ".gz")
			}
			finalExtractPathBase := filepath.Join(finalOutputDirBase, baseName)

			logger.Info(fmt.Sprintf("Decompressing '%s' to '%s'", targetFilePath, finalExtractPathBase))
			var decompErr error
			var finalPath string
			if isArchive {
				finalPath = finalExtractPathBase
				if err := os.MkdirAll(finalPath, 0755); err != nil {
					return fmt.Errorf("lỗi tạo thư mục giải nén '%s': %w", finalPath, err)
				}
				decompErr = DecompressFolder(targetFilePath, finalPath)
			} else {
				finalPath = finalExtractPathBase
				decompErr = DecompressFile(targetFilePath, finalOutputDirBase)
				if decompErr == nil {
					if _, statErr := os.Stat(finalPath); statErr != nil {
						logger.Warn("Decompressed file not found at expected location:", finalPath)
					}
				}
			}

			if decompErr != nil {
				logger.Error(fmt.Sprintf("Lỗi giải nén file '%s': %v", targetFilePath, decompErr))
				return fmt.Errorf("lỗi giải nén '%s': %w", targetFilePath, decompErr)
			}

			logger.Info(fmt.Sprintf("✅ Successfully decompressed '%s' to '%s'.", targetFilePath, finalPath))
			if isArchive {
				// Clear cache if this is a snapshot DB folder
				logger.Info("🧹 [SNAPSHOT LOADED] Clearing C++ State Cache to prevent stale reads...")
				mvm.ClearAllStateInstances()
			}
			processingSuccessful = true
			logger.Debug(fmt.Sprintf("Removing original compressed file: %s", targetFilePath))
			removeErr := os.Remove(targetFilePath)
			if removeErr != nil {
				logger.Warn("Failed to remove original compressed file:", targetFilePath, removeErr)
			}
		} else {
			finalPath := filepath.Join(finalOutputDirBase, partFileName)
			logger.Info(fmt.Sprintf("Received non-archive file. Moving from '%s' to '%s'", targetFilePath, finalPath))
			if _, err := os.Stat(finalPath); err == nil {
				logger.Warn("Destination file already exists, removing before move:", finalPath)
				if errRem := os.Remove(finalPath); errRem != nil {
					logger.Error("Failed to remove existing destination file:", finalPath, errRem)
				}
			}
			if err := os.Rename(targetFilePath, finalPath); err != nil {
				logger.Error(fmt.Sprintf("Lỗi di chuyển file '%s' đến đích cuối cùng '%s': %v", targetFilePath, finalPath, err))
				return fmt.Errorf("lỗi di chuyển file '%s': %w", targetFilePath, err)
			}
			logger.Info(fmt.Sprintf("File '%s' moved to final destination.", finalPath))
			processingSuccessful = true
		}
	}

	if processingSuccessful {
		logger.Debug("Processing successful for item, marking as received:", receivedItemKey)
		markItemReceived(receivedItemKey)
	} else {
		logger.Debug("Processing not marked as successful for item:", receivedItemKey)
	}

	return nil
}

// SplitFileInfo chứa thông tin metadata cho một split archive part.
type SplitFileInfo struct {
	BaseArchiveName string
	TotalParts      int
}

// SendFileViaTCP gửi một file qua TCP connection.
func (node *HostNode) SendFileViaTCP(conn network.Connection, filePath string, splitInfo *SplitFileInfo) error {
	if node.MessageSender == nil {
		return fmt.Errorf("MessageSender not initialized")
	}
	if conn == nil || !conn.IsConnect() {
		return fmt.Errorf("connection not available")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("không thể mở file '%s': %w", filePath, err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("lỗi khi lấy thông tin file '%s': %w", filePath, err)
	}

	if fileInfo.IsDir() {
		return fmt.Errorf("path '%s' is a directory, use SendFolderViaTCP instead", filePath)
	}

	fileSize := fileInfo.Size()
	fileName := fileInfo.Name()

	// Build metadata + content
	var meta strings.Builder
	if splitInfo != nil {
		meta.WriteString(fmt.Sprintf("%s%d:%s\n", SplitInfoPrefix, splitInfo.TotalParts, splitInfo.BaseArchiveName))
	}
	meta.WriteString(fmt.Sprintf("%s\n%d\n", fileName, fileSize))

	// Read entire file content
	content, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("lỗi đọc file '%s': %w", filePath, err)
	}

	// Combine metadata + content
	payload := append([]byte(meta.String()), content...)

	// Send via TCP
	err = node.MessageSender.SendBytes(conn, "FileTransfer", payload)
	if err != nil {
		return fmt.Errorf("lỗi gửi file '%s' qua TCP: %w", fileName, err)
	}

	logger.Info(fmt.Sprintf("✅ Đã gửi file '%s' (%d bytes) qua TCP", fileName, fileSize))
	return nil
}

// SendFolderViaTCP nén và gửi folder qua TCP connection.
func (node *HostNode) SendFolderViaTCP(conn network.Connection, folderPath string, maxPartSizeMB int) error {
	startTime := time.Now()
	logger.Info(fmt.Sprintf("Preparing to send folder '%s' via TCP (Max part size: %d MB)", folderPath, maxPartSizeMB))

	info, err := os.Stat(folderPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("thư mục nguồn '%s' không tồn tại", folderPath)
		}
		return fmt.Errorf("lỗi kiểm tra thư mục nguồn '%s': %w", folderPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("đường dẫn nguồn '%s' không phải là thư mục", folderPath)
	}

	parentDir := filepath.Dir(folderPath)
	tempDir, err := os.MkdirTemp(parentDir, "sendfolder-compress-")
	if err != nil {
		return fmt.Errorf("lỗi tạo thư mục tạm để nén: %w", err)
	}
	defer func() {
		logger.Debug("Removing compression temp directory:", tempDir)
		removeErr := os.RemoveAll(tempDir)
		if removeErr != nil {
			logger.Warn("Failed to remove compression temp directory:", tempDir, removeErr)
		}
	}()

	baseName := filepath.Base(folderPath)
	archiveBaseName := baseName

	logger.Info(fmt.Sprintf("Compressing folder '%s' into temp dir '%s'...", folderPath, tempDir))
	compressStart := time.Now()

	ctx := node.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	generatedParts, err := CompressFolderAndSplitWithOptionalSnapshot(ctx, folderPath, tempDir, archiveBaseName, maxPartSizeMB)
	if err != nil {
		return fmt.Errorf("lỗi khi nén thư mục '%s': %w", folderPath, err)
	}
	compressDuration := time.Since(compressStart)
	logger.Info(fmt.Sprintf("Compression successful (%s). Found %d file(s)/part(s).", compressDuration, len(generatedParts)))

	if len(generatedParts) == 0 {
		logger.Error("Compression reported success but no archive files found in temp dir:", tempDir)
		return fmt.Errorf("nén thành công nhưng không tìm thấy file archive nào trong %s", tempDir)
	}

	sort.Strings(generatedParts)

	totalParts := len(generatedParts)
	var splitInfo *SplitFileInfo = nil

	if totalParts > 1 || (totalParts == 1 && strings.Contains(filepath.Base(generatedParts[0]), ".7z.001")) {
		partBaseName := filepath.Base(generatedParts[0])
		ext := filepath.Ext(partBaseName)
		baseArchiveNameForInfo := strings.TrimSuffix(partBaseName, ext)

		splitInfo = &SplitFileInfo{
			BaseArchiveName: baseArchiveNameForInfo,
			TotalParts:      totalParts,
		}
		logger.Info(fmt.Sprintf("Detected split archive '%s' with %d parts.", splitInfo.BaseArchiveName, splitInfo.TotalParts))
	} else if totalParts == 1 {
		logger.Info("Detected single archive file:", generatedParts[0])
	}

	transferStart := time.Now()
	logger.Info(fmt.Sprintf("Starting transfer of %d file(s)/part(s) via TCP...", totalParts))

	var firstPartErr error
	for i, partPath := range generatedParts {
		partFileName := filepath.Base(partPath)
		logger.Info(fmt.Sprintf("Sending part %d/%d: '%s'", i+1, totalParts, partFileName))

		currentSplitInfo := splitInfo
		err = node.SendFileViaTCP(conn, partPath, currentSplitInfo)
		if err != nil {
			firstPartErr = fmt.Errorf("lỗi khi gửi part %d ('%s'): %w", i+1, partFileName, err)
			logger.Error(firstPartErr.Error())
			break
		}
		logger.Info(fmt.Sprintf("Successfully sent part %d/%d: '%s'", i+1, totalParts, partFileName))
	}

	transferDuration := time.Since(transferStart)

	if firstPartErr != nil {
		logger.Error(fmt.Sprintf("Transfer failed for folder '%s' due to error sending a part.", folderPath))
		return firstPartErr
	}

	logger.Info(fmt.Sprintf("✅ Finished sending all %d file(s)/part(s) for folder '%s' (%s total time).", totalParts, folderPath, time.Since(startTime)))
	logger.Info(fmt.Sprintf("   Compression: %s, Transfer: %s", compressDuration, transferDuration))
	return nil
}

// HandleFreeFeeResponse xử lý response fee addresses nhận từ Master qua TCP.
func (node *HostNode) HandleFreeFeeResponse(request network.Request) error {
	body := request.Message().Body()
	if len(body) == 0 {
		return fmt.Errorf("empty FreeFeeResponse")
	}

	var addresses []string
	if err := json.Unmarshal(body, &addresses); err != nil {
		return fmt.Errorf("failed to unmarshal fee addresses: %w", err)
	}

	node.SetFeeAddresses(addresses)
	logger.Info(fmt.Sprintf("✅ Set %d fee addresses from master response", len(addresses)))
	return nil
}

// --- Cleanup function for stale receiving states ---

func CleanupOldStates(maxIdleTime time.Duration) {
	receiveStateMutex.Lock()
	defer receiveStateMutex.Unlock()

	now := time.Now()
	cleanedCount := 0
	for key, state := range receivingStates {
		if now.Sub(state.LastUpdate) > maxIdleTime {
			logger.Warn(fmt.Sprintf("Cleaning up stale receiving state for '%s' (idle for %v)", key, now.Sub(state.LastUpdate)))
			if state.TempDir != "" {
				logger.Debug("Removing stale temp directory:", state.TempDir)
				removeErr := os.RemoveAll(state.TempDir)
				if removeErr != nil {
					logger.Error("Failed to remove stale temp directory:", state.TempDir, removeErr)
				}
			}
			delete(receivingStates, key)
			cleanedCount++
		}
	}
	if cleanedCount > 0 {
		logger.Info("Finished cleaning up stale receiving states. Removed:", cleanedCount)
	}
}
