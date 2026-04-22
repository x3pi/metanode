package transfer

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	typesnetwork "github.com/meta-node-blockchain/meta-node/types/network"
)

// HandleDirectoryRequest xử lý yêu cầu lấy thư mục từ receiver.
func HandleDirectoryRequest(req typesnetwork.Request) error {
	msg := &pb.DirectoryRequest{}
	if err := req.Message().Unmarshal(msg); err != nil {
		return fmt.Errorf("lỗi unmarshal DirectoryRequest: %w", err)
	}

	logger.Info(fmt.Sprintf("Nhận được yêu cầu gửi thư mục '%s' từ %s", msg.DirectoryName, req.Connection().RemoteAddr()))
	logger.Info(fmt.Sprintf("Địa chỉ callback để gửi file là: %s", msg.CallbackAddress))

	if msg.CallbackAddress == "" {
		return fmt.Errorf("yêu cầu không chứa địa chỉ callback")
	}

	sourceDir, err := getSourcePath(msg.DirectoryName)
	if err != nil {
		logger.Error("Yêu cầu thư mục không hợp lệ:", err)
		return err
	}

	go func() {
		logger.Info("Sender đang kết nối lại đến Receiver tại", msg.CallbackAddress)
		receiverConn := network.NewConnection(common.Address{}, "SENDER_TO_RECEIVER", nil)
		receiverConn.SetRealConnAddr(msg.CallbackAddress)
		if err := receiverConn.Connect(); err != nil {
			logger.Error("Sender không thể kết nối đến callback address của receiver:", err)
			return
		}
		defer receiverConn.Disconnect()
		logger.Info("Sender đã kết nối lại đến Receiver thành công!")

		messageSender := network.NewMessageSender(req.Message().Version())
		archiveBaseName := fmt.Sprintf("%s_backup_%d.7z", msg.DirectoryName, time.Now().Unix())

		err := SendDirectory(
			context.Background(),
			messageSender,
			receiverConn,
			sourceDir,
			archiveBaseName,
			100,
		)
		if err != nil {
			logger.Error(fmt.Sprintf("Lỗi trong quá trình gửi thư mục '%s': %v", msg.DirectoryName, err))
		}
	}()

	return nil
}

func getSourcePath(requestedName string) (string, error) {
	if requestedName == "db_data" {
		sourcePath := "./db_data_to_send"
		return sourcePath, nil
	}
	return "", fmt.Errorf("không hỗ trợ yêu cầu cho thư mục '%s'", requestedName)
}
