package transfer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	"github.com/meta-node-blockchain/meta-node/pkg/node"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	typesnetwork "github.com/meta-node-blockchain/meta-node/types/network"
)

// --- State Manager và các hằng số không thay đổi ---
type UploadState struct {
	BaseArchiveName string
	TotalChunks     int
	ReceivedChunks  int
}

type StateManager struct {
	mu       sync.RWMutex
	sessions map[string]*UploadState
}

func NewStateManager() *StateManager {
	return &StateManager{
		sessions: make(map[string]*UploadState),
	}
}

func (sm *StateManager) StartSession(baseArchiveName string, totalChunks int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.sessions[baseArchiveName] = &UploadState{
		BaseArchiveName: baseArchiveName,
		TotalChunks:     totalChunks,
		ReceivedChunks:  0,
	}
	logger.Info("Đã bắt đầu phiên tải lên cho '%s' với %d chunks.", baseArchiveName, totalChunks)
}

func (sm *StateManager) IncrementChunkCount(baseArchiveName string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	session, exists := sm.sessions[baseArchiveName]
	if !exists {
		return false
	}
	session.ReceivedChunks++
	logger.Info("Đã nhận chunk %d/%d cho '%s'.", session.ReceivedChunks, session.TotalChunks, baseArchiveName)
	return session.ReceivedChunks >= session.TotalChunks
}

func (sm *StateManager) EndSession(baseArchiveName string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, baseArchiveName)
	logger.Info("Đã kết thúc và xóa phiên tải lên cho '%s'.", baseArchiveName)
}

const (
	incomingDir = "./incoming_chunks"
)

var uploadStateManager = NewStateManager()

// HandleStartUpload giờ chỉ chịu trách nhiệm khởi tạo phiên.
func HandleStartUpload(req typesnetwork.Request) error {
	msg := &pb.StartFileUploadRequest{}
	if err := req.Message().Unmarshal(msg); err != nil {
		return fmt.Errorf("lỗi unmarshal StartFileUploadRequest: %w", err)
	}
	// Logic tạo thư mục đã được chuyển đi.
	uploadStateManager.StartSession(msg.BaseArchiveName, int(msg.TotalChunks))
	return nil
}

// === HÀM QUAN TRỌNG ĐÃ ĐƯỢC CẢI TIẾN ===
// HandleFileChunk giờ sẽ tự tạo thư mục nếu cần.
func HandleFileChunk(req typesnetwork.Request) error {
	msg := &pb.FileChunk{}
	if err := req.Message().Unmarshal(msg); err != nil {
		return fmt.Errorf("lỗi unmarshal FileChunk: %w", err)
	}
	if msg.PartName == "" {
		return fmt.Errorf("tên chunk không được rỗng")
	}

	// Đảm bảo thư mục `incoming_chunks` tồn tại.
	// os.MkdirAll an toàn để gọi nhiều lần.
	if err := os.MkdirAll(incomingDir, 0755); err != nil {
		return fmt.Errorf("không thể tạo thư mục incoming: %w", err)
	}

	cleanPartName := filepath.Base(msg.PartName)
	chunkPath := filepath.Join(incomingDir, cleanPartName)
	logger.Info(fmt.Sprintf("Đang nhận chunk '%s'...", cleanPartName))

	if err := os.WriteFile(chunkPath, msg.Data, 0644); err != nil {
		return fmt.Errorf("lỗi khi ghi chunk '%s' vào đĩa: %w", chunkPath, err)
	}

	baseArchiveName := strings.TrimSuffix(cleanPartName, filepath.Ext(cleanPartName))
	if uploadStateManager.IncrementChunkCount(baseArchiveName) {
		logger.Info("ĐÃ NHẬN ĐỦ TẤT CẢ CÁC CHUNK! Kích hoạt giải nén...")
		triggerDecompression(baseArchiveName)
		uploadStateManager.EndSession(baseArchiveName)
	}
	return nil
}

// triggerDecompression và các handler khác không thay đổi.
func triggerDecompression(baseArchiveName string) {
	firstChunkPath := filepath.Join(incomingDir, baseArchiveName+".001")
	outputDir := "./restored_data"

	go func() {
		const retries = 10
		const delay = 200 * time.Millisecond
		var fileFound bool

		for i := 0; i < retries; i++ {
			if _, err := os.Stat(firstChunkPath); err == nil {
				fileFound = true
				break
			}
			time.Sleep(delay)
		}

		if !fileFound {
			logger.Error("!!! LỖI GIẢI NÉN !!! Không tìm thấy file chunk nguồn '%s'.", firstChunkPath)
			return
		}

		if err := os.MkdirAll(outputDir, 0755); err != nil {
			logger.Error("!!! LỖI TẠO THƯ MỤC OUTPUT !!!", err)
			return
		}
		if err := node.DecompressFolder(firstChunkPath, outputDir); err != nil {
			logger.Error("!!! LỖI GIẢI NÉN !!!", err)
		} else {
			logger.Info("✅ Giải nén thành công thư mục vào:", outputDir)
			logger.Info("Đang dọn dẹp các file chunk...")
			pattern := filepath.Join(incomingDir, baseArchiveName+".*")
			files, _ := filepath.Glob(pattern)
			for _, f := range files {
				os.Remove(f)
			}
			logger.Info("Đã dọn dẹp các file chunk.")
		}
	}()
}

func HandleSyncRequest(req typesnetwork.Request) error {
	msg := &pb.SyncRequest{}
	if err := req.Message().Unmarshal(msg); err != nil {
		return fmt.Errorf("lỗi unmarshal SyncRequest: %w", err)
	}
	logger.Info(fmt.Sprintf("Nhận được yêu cầu đồng bộ cho '%s'", msg.BaseArchiveName))

	var receivedParts []string
	if _, err := os.Stat(incomingDir); !os.IsNotExist(err) {
		files, _ := os.ReadDir(incomingDir)
		for _, file := range files {
			if !file.IsDir() && strings.HasPrefix(file.Name(), msg.BaseArchiveName) {
				receivedParts = append(receivedParts, file.Name())
			}
		}
	}
	logger.Info(fmt.Sprintf("Đã tìm thấy %d chunk đã tồn tại. Gửi phản hồi...", len(receivedParts)))

	responseMsg := &pb.SyncResponse{ReceivedPartNames: receivedParts}
	messageSender := network.NewMessageSender(req.Message().Version())
	return messageSender.SendMessage(
		req.Connection(),
		p_common.SyncFileResponse,
		responseMsg,
	)
}
