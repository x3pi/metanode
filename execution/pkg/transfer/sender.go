package transfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/node"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// === HÀM QUAN TRỌNG ĐÃ ĐƯỢC CẢI TIẾN ===
func SendDirectory(
	ctx context.Context,
	messageSender network.MessageSender,
	receiverConn network.Connection,
	sourceDir string,
	baseArchiveName string,
	splitSizeMB int,
) error {
	logger.Info("Bắt đầu quá trình gửi thư mục:", sourceDir)

	tempOutputDir := "./temp_compressed_output"
	if err := os.MkdirAll(tempOutputDir, 0755); err != nil {
		return fmt.Errorf("không thể tạo thư mục tạm: %w", err)
	}
	defer func() {
		logger.Info("Dọn dẹp thư mục nén tạm:", tempOutputDir)
		os.RemoveAll(tempOutputDir)
	}()

	pattern := filepath.Join(tempOutputDir, baseArchiveName+".*")
	chunkPaths, _ := filepath.Glob(pattern)

	if len(chunkPaths) == 0 {
		var err error
		chunkPaths, err = node.CompressFolderAndSplitWithOptionalSnapshot(
			ctx, sourceDir, tempOutputDir, baseArchiveName, splitSizeMB)
		if err != nil {
			return fmt.Errorf("lỗi khi nén: %w", err)
		}
	}

	if len(chunkPaths) == 0 {
		return fmt.Errorf("không có file chunk nào được tạo ra")
	}

	chunksToSend := chunkPaths
	logger.Info(fmt.Sprintf("Cần gửi %d chunk.", len(chunksToSend)))

	startReq := &pb.StartFileUploadRequest{
		BaseArchiveName: baseArchiveName,
		TotalChunks:     int32(len(chunkPaths)),
	}
	if err := messageSender.SendMessage(receiverConn, p_common.StartFileUpload, startReq); err != nil {
		return fmt.Errorf("lỗi gửi StartFileUpload: %w", err)
	}

	for i, chunkPath := range chunksToSend {
		logger.Info(fmt.Sprintf("Đang gửi chunk %d/%d: %s", i+1, len(chunksToSend), chunkPath))
		data, err := os.ReadFile(chunkPath)
		if err != nil {
			return fmt.Errorf("lỗi đọc chunk '%s': %w", chunkPath, err)
		}
		chunkMsg := &pb.FileChunk{
			PartName: filepath.Base(chunkPath),
			Data:     data,
		}
		if err := messageSender.SendMessage(receiverConn, p_common.FileChunkTransfer, chunkMsg); err != nil {
			return fmt.Errorf("lỗi gửi chunk '%s': %w", chunkPath, err)
		}
	}

	logger.Info("✅ === QUÁ TRÌNH GỬI THƯ MỤC HOÀN TẤT ===")

	// === THÊM DÒNG NÀY ĐỂ GIẢI QUYẾT LỖI "CONNECTION RESET" ===
	// Chờ một chút trước khi goroutine kết thúc và đóng kết nối.
	logger.Info("Đợi 2 giây để receiver xử lý trước khi đóng kết nối...")
	time.Sleep(2 * time.Second)

	return nil
}
