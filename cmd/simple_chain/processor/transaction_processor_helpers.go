package processor

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// ════════════════════════════════════════════════════════════════════════
// Monitoring & cleanup helpers — extracted from transaction_processor.go
// ════════════════════════════════════════════════════════════════════════

func (tp *TransactionProcessor) MonitorCacheSize() {
	go func() {
		fileLogger, _ := loggerfile.NewFileLogger("cache_debug.log")
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			readTxCount := 0
			tp.readTxHashes.Range(func(k, v interface{}) bool {
				readTxCount++
				return true
			})

			fileLogger.Info("readTxHashes cache size: %d entries", readTxCount)

			if readTxCount > 10000 {
				logger.Error("⚠️  ALERT: readTxHashes cache > 10000 entries! (current: %d)", readTxCount)
			}
		}
	}()
}

// cleanupReadTxHashes là một worker chạy nền để xóa các hash cũ khỏi cache,
// ngăn chặn rò rỉ bộ nhớ.
func (v *TxVirtualExecutor) cleanupReadTxHashes() {
	// Ticker chạy định kỳ 10 giây một lần
	ticker := time.NewTicker(100 * time.Second)
	defer ticker.Stop()

	for {
		<-ticker.C
		// Xác định thời gian ngưỡng: các hash cũ hơn 5 giây sẽ bị xóa
		cutoff := time.Now().Add(-5 * time.Second)

		// Duyệt qua tất cả các entry trong cache
		v.readTxHashes.Range(func(key, value interface{}) bool {
			if timestamp, ok := value.(time.Time); ok {
				// Nếu timestamp của hash cũ hơn thời gian ngưỡng, xóa nó đi
				if timestamp.Before(cutoff) {
					v.readTxHashes.Delete(key)
				}
			}
			return true // Tiếp tục duyệt
		})
	}
}

// cleanupExecutedMvmIds dọn dẹp các MVM IDs cũ trong executedMvmIds để tránh rò rỉ bộ nhớ
// Xóa các entries cũ hơn 10 phút
func (tp *TransactionProcessor) cleanupExecutedMvmIds() {
	ticker := time.NewTicker(5 * time.Minute) // Chạy mỗi 5 phút
	defer ticker.Stop()

	for range ticker.C {
		cutoff := time.Now().Add(-10 * time.Minute) // Xóa entries cũ hơn 10 phút
		removedCount := 0
		totalCount := 0

		// Duyệt qua tất cả các entry trong executedMvmIds
		tp.executedMvmIds.Range(func(key, value interface{}) bool {
			totalCount++
			if timestamp, ok := value.(time.Time); ok {
				// Nếu timestamp của entry cũ hơn thời gian ngưỡng, xóa nó đi
				if timestamp.Before(cutoff) {
					tp.executedMvmIds.Delete(key)
					removedCount++
				}
			}
			return true // Tiếp tục duyệt
		})

		if removedCount > 0 {
			logger.Info("cleanupExecutedMvmIds: Đã xóa %d entries cũ khỏi executedMvmIds (tổng: %d)", removedCount, totalCount)
		}
		if totalCount > 10000 {
			logger.Warn("cleanupExecutedMvmIds: executedMvmIds size lớn (%d), có thể có vấn đề", totalCount)
		}
	}
}

// MonitorDeviceKeyGoroutines theo dõi số lượng goroutines và memory usage
// để phát hiện rò rỉ bộ nhớ sớm
func (tp *TransactionProcessor) MonitorDeviceKeyGoroutines() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Đọc metrics atomically
		goroutineCount := atomic.LoadInt64(&tp.deviceKeyGoroutineCount)
		completedCount := atomic.LoadInt64(&tp.deviceKeyGoroutineCompleted)
		totalDuration := atomic.LoadInt64(&tp.deviceKeyGoroutineDuration)

		// Đọc memory stats
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		// Tính toán average duration (nếu có goroutines đã hoàn thành)
		avgDuration := time.Duration(0)
		if completedCount > 0 {
			avgDuration = time.Duration(totalDuration) / time.Duration(completedCount)
		}

		// Log thông tin
		if goroutineCount > 0 || completedCount > 0 {
			logger.Info("📊 DeviceKey Goroutines: Active=%d, Completed=%d, AvgDuration=%v, PoolUsage=%d/%d",
				goroutineCount,
				completedCount,
				avgDuration,
				len(tp.deviceKeySendPool),
				cap(tp.deviceKeySendPool))
		}

		// Cảnh báo nếu có quá nhiều goroutines
		if goroutineCount > 50 {
			logger.Warn("⚠️  ALERT: DeviceKey goroutines > 50! (current: %d, pool: %d/%d)",
				goroutineCount, len(tp.deviceKeySendPool), cap(tp.deviceKeySendPool))
		}

		// Log memory stats nếu có vấn đề
		if m.Alloc > 1024*1024*1024 { // > 1GB
			logger.Info("💾 Memory: Alloc=%d MB, Sys=%d MB, NumGC=%d, Goroutines=%d",
				m.Alloc/(1024*1024),
				m.Sys/(1024*1024),
				m.NumGC,
				runtime.NumGoroutine())
		}

		// Cảnh báo nếu tổng số goroutines quá cao
		totalGoroutines := runtime.NumGoroutine()
		if totalGoroutines > 10000 {
			logger.Error("🚨 CRITICAL: Total goroutines > 10000! (current: %d)", totalGoroutines)
		}
	}
}

// ════════════════════════════════════════════════════════════════════════
// Device key & message send helpers
// ════════════════════════════════════════════════════════════════════════

func (v *TxVirtualExecutor) sendTransactionError(conn network.Connection, txHash common.Hash, code int64, message string, output []byte, msgID string) {
	if v.messageSender != nil {
		logger.Error("output %v", common.Bytes2Hex(output))
		logger.Error("txHash %v", txHash)

		body, err := proto.Marshal(
			transaction.NewTransactionHashWithError(
				txHash,
				code,
				message,
				output,
			).Proto(),
		)
		if err != nil {
			logger.Error("sendTransactionError: marshal error: %v", err)
			return
		}
		respMsg := p_network.NewMessage(&pb.Message{
			Header: &pb.Header{
				Command: command.TransactionError,
				ID:      msgID,
			},
			Body: body,
		})
		conn.SendMessage(respMsg)
	}
}

// sendTransactionResult gửi phản hồi thành công qua command TransactionError với txHash và msgID.
// Body chỉ chứa txHash bytes (không wrap trong TransactionHashWithError).
// msgID được đặt trong header để client có thể match response với request.
func (v *TxVirtualExecutor) sendTransactionResult(conn network.Connection, txHash common.Hash, msgID string) {
	if v.messageSender == nil {
		return
	}

	respMsg := p_network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command: command.TransactionSuccess,
			ID:      msgID,
		},
		Body: txHash.Bytes(),
	})
	conn.SendMessage(respMsg)
}

func (tp *TransactionProcessor) backupDeviceKey(s storage.Storage, t types.Transaction, newDeviceKey []byte) error {
	_, ok := blockchain.GetBlockChainInstance().GetBlockNumberByTxHash(t.Hash())
	if ok {
		return fmt.Errorf("transaction already exists")
	}

	err := s.Put(t.Hash().Bytes(), newDeviceKey)
	if err != nil {
		return fmt.Errorf("backupDeviceKey failed: %v", err)
	}

	putRequest := &pb.StoragePutRequest{
		Key:   t.Hash().Bytes(),
		Value: newDeviceKey,
	}

	dbOperationRequest := &pb.DatabaseOperationRequest{
		OperationType: pb.OperationType_PUT,
		RequestId:     uuid.NewString(),
		Payload: &pb.DatabaseOperationRequest_PutRequest{
			PutRequest: putRequest,
		},
	}

	// Sử dụng worker pool và timeout để tránh rò rỉ bộ nhớ
	// Tạo context với timeout 5 giây cho mỗi goroutine
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Gửi đến master connections với worker pool và timeout
	tp.sendDeviceKeyWithPool(ctx, mt_common.MASTER_CONNECTION_TYPE, command.RemoteDeviceKeyDB, dbOperationRequest)

	// Gửi đến child node connections với worker pool và timeout
	tp.sendDeviceKeyWithPool(ctx, mt_common.CHILD_NODE_CONNECTION_TYPE, command.RemoteDeviceKeyDB, dbOperationRequest)

	return nil
}

// sendDeviceKeyWithPool gửi device key với worker pool và timeout để tránh rò rỉ bộ nhớ
func (tp *TransactionProcessor) sendDeviceKeyWithPool(
	ctx context.Context,
	connectionTypeName string,
	command string,
	pbMessage proto.Message,
) {
	// Cố gắng lấy slot từ worker pool
	select {
	case tp.deviceKeySendPool <- struct{}{}:
		// Có slot, tạo goroutine với timeout
		atomic.AddInt64(&tp.deviceKeyGoroutineCount, 1)
		go func() {
			defer func() {
				<-tp.deviceKeySendPool // Trả lại slot
				atomic.AddInt64(&tp.deviceKeyGoroutineCount, -1)
			}()

			start := time.Now()
			defer func() {
				duration := time.Since(start)
				atomic.AddInt64(&tp.deviceKeyGoroutineDuration, int64(duration))
				atomic.AddInt64(&tp.deviceKeyGoroutineCompleted, 1)
			}()

			// Gửi với context timeout
			_ = sendToAllConnectionsOfTypeWithContext(
				ctx,
				tp.env,
				tp.messageSender,
				connectionTypeName,
				command,
				pbMessage,
			)
		}()
	case <-ctx.Done():
		// Timeout hoặc context bị cancel trước khi có slot
		logger.Warn("Device key send pool full or timeout, dropping request for %s", connectionTypeName)
	case <-time.After(100 * time.Millisecond):
		// Không có slot sau 100ms, log warning và bỏ qua
		logger.Warn("Device key send pool full (timeout 100ms), dropping request for %s", connectionTypeName)
	}
}

func (tp *TransactionProcessor) HandleDeviceKeyRequest(r network.Request) error {
	dbOpReq := &pb.DatabaseOperationRequest{}
	err := r.Message().Unmarshal(dbOpReq)
	if err != nil {
		return fmt.Errorf("không thể unmarshal DatabaseOperationRequest: %w", err)
	}
	if dbOpReq.OperationType != pb.OperationType_PUT {
		return fmt.Errorf("loại hoạt động cơ sở dữ liệu không được hỗ trợ: %s", dbOpReq.OperationType.String())
	}

	putReq := dbOpReq.GetPutRequest()
	if putReq == nil {
		return errors.New("payload yêu cầu put là nil")
	}

	err = tp.storageManager.GetStorageBackupDeviceKey().Put(putReq.Key, putReq.Value)
	if err != nil {
		return errors.New("lưu device key không thành công")
	}

	return nil
}

// GetDeviceKey implements OffChainProcessor interface
// Retrieves device key from storage by hash
func (tp *TransactionProcessor) GetDeviceKey(hash common.Hash) (common.Hash, error) {
	deviceStorage := tp.storageManager.GetStorageBackupDeviceKey()
	data, err := deviceStorage.Get(hash.Bytes())
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to get device key: %w", err)
	}
	return common.BytesToHash(data), nil
}

func sendToAllConnectionsOfType(
	connManager IConnectionManager,
	msgSender network.MessageSender,
	connectionTypeName string,
	command string,
	pbMessage proto.Message,
) error {
	return sendToAllConnectionsOfTypeWithContext(
		context.Background(),
		connManager,
		msgSender,
		connectionTypeName,
		command,
		pbMessage,
	)
}

// sendToAllConnectionsOfTypeWithContext gửi message với context để hỗ trợ timeout và cancellation
// Giới hạn số lượng errors để tránh memory growth
func sendToAllConnectionsOfTypeWithContext(
	ctx context.Context,
	connManager IConnectionManager,
	msgSender network.MessageSender,
	connectionTypeName string,
	command string,
	pbMessage proto.Message,
) error {
	cTypeIndex := mt_common.MapConnectionTypeToIndex(connectionTypeName)
	if cTypeIndex < 0 {
		return fmt.Errorf("loại kết nối không xác định: %s", connectionTypeName)
	}

	// Kiểm tra context đã bị cancel chưa
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	connectionsMap := connManager.ConnectionsByType(cTypeIndex)
	if len(connectionsMap) == 0 {
		return nil
	}

	// Giới hạn số lượng errors để tránh memory growth không kiểm soát
	const maxErrors = 10
	allErrors := make([]error, 0, maxErrors)
	errorCount := 0
	totalConnections := len(connectionsMap)
	successCount := 0

	for address, conn := range connectionsMap {
		// Kiểm tra context trước mỗi iteration
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if conn != nil && conn.IsConnect() {
			err := msgSender.SendMessage(conn, command, pbMessage)
			if err != nil {
				if errorCount < maxErrors {
					errMsg := fmt.Errorf("lỗi khi gửi tin nhắn '%s' đến %s: %w", command, address.Hex(), err)
					logger.Error(errMsg.Error())
					allErrors = append(allErrors, errMsg)
					errorCount++
				} else if errorCount == maxErrors {
					// Log một lần khi đạt giới hạn
					logger.Warn("Đã đạt giới hạn %d errors, không thu thập thêm errors cho %s", maxErrors, connectionTypeName)
					errorCount++
				}
			} else {
				successCount++
			}
		} else {
			// Chỉ log warning khi có nhiều connection không hoạt động
			if totalConnections > 5 && errorCount < maxErrors {
				warnMsg := fmt.Errorf("kết nối đến %s không hoạt động", address.Hex())
				logger.Warn(warnMsg.Error())
				allErrors = append(allErrors, warnMsg)
				errorCount++
			}
		}
	}

	// Log thống kê
	if errorCount > 0 {
		logger.Warn("Gửi device key: %d thành công, %d lỗi (trong %d connections) cho %s",
			successCount, errorCount, totalConnections, connectionTypeName)
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("có lỗi khi gửi tin nhắn đến một hoặc nhiều kết nối của loại '%s': %d lỗi (hiển thị %d đầu tiên): %v",
			connectionTypeName, errorCount, len(allErrors), allErrors)
	}
	return nil
}
