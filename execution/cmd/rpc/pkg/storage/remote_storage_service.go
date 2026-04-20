package storage

import (
	"fmt"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"

	// Sửa đường dẫn import proto cho đúng với dự án của bạn
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// Hằng số cho command chung
const (
	DatabaseOperationCommand = "DATABASE_OPERATION"
)

// RemoteStorageService xử lý các yêu cầu storage từ network
type RemoteStorageService struct {
	actualStorage Storage
	messageSender network.MessageSender
}

func NewRemoteStorageService(storageImpl Storage, messageSender network.MessageSender) *RemoteStorageService {
	return &RemoteStorageService{
		actualStorage: storageImpl,
		messageSender: messageSender,
	}
}

// GetRoutes trả về map các route để đăng ký với network handler
func (rss *RemoteStorageService) GetRoutes() map[string]func(network.Request) error {
	return map[string]func(network.Request) error{
		DatabaseOperationCommand: rss.handleDatabaseOperation,
	}
}

// handleDatabaseOperation là handler duy nhất, định tuyến dựa vào operation_type
func (rss *RemoteStorageService) handleDatabaseOperation(req network.Request) error {
	reqWrapper := &pb.DatabaseOperationRequest{}
	if err := req.Message().Unmarshal(reqWrapper); err != nil {
		return fmt.Errorf("failed to unmarshal request wrapper: %w", err)
	}

	// logger.Info("Handling database operation: %v (ID: %s)", reqWrapper.OperationType, reqWrapper.RequestId)

	var respWrapper *pb.DatabaseOperationResponse
	var opErr error

	switch reqWrapper.OperationType {
	case pb.OperationType_GET:
		specificReq := reqWrapper.GetGetRequest()
		value, err := rss.actualStorage.Get(specificReq.Key)
		opErr = err
		respWrapper = rss.createResponseWrapper(reqWrapper, &pb.StorageGetResponse{Value: value}, err)

	case pb.OperationType_PUT:
		specificReq := reqWrapper.GetPutRequest()
		opErr = rss.actualStorage.Put(specificReq.Key, specificReq.Value)
		respWrapper = rss.createResponseWrapper(reqWrapper, &pb.StoragePutResponse{}, opErr)

	case pb.OperationType_DELETE:
		specificReq := reqWrapper.GetDeleteRequest()
		opErr = rss.actualStorage.Delete(specificReq.Key)
		respWrapper = rss.createResponseWrapper(reqWrapper, &pb.StorageDeleteResponse{}, opErr)

	case pb.OperationType_BATCH_PUT:
		specificReq := reqWrapper.GetBatchPutRequest()
		kvs := make([][2][]byte, len(specificReq.Kvs))
		for i, pair := range specificReq.Kvs {
			kvs[i] = [2][]byte{pair.Key, pair.Value}
		}
		opErr = rss.actualStorage.BatchPut(kvs)
		respWrapper = rss.createResponseWrapper(reqWrapper, &pb.StorageBatchPutResponse{}, opErr)

	case pb.OperationType_BATCH_DELETE:
		specificReq := reqWrapper.GetBatchDeleteRequest()
		opErr = rss.actualStorage.BatchDelete(specificReq.Keys)
		respWrapper = rss.createResponseWrapper(reqWrapper, &pb.StorageBatchDeleteResponse{}, opErr)

	case pb.OperationType_GET_BACKUP_PATH:
		path := rss.actualStorage.GetBackupPath()
		respWrapper = rss.createResponseWrapper(reqWrapper, &pb.StorageGetBackupPathResponse{Path: path}, nil)

	default:
		opErr = fmt.Errorf("unsupported operation type: %v", reqWrapper.OperationType)
		respWrapper = rss.createResponseWrapper(reqWrapper, nil, opErr)
	}

	if opErr != nil {
		logger.Error("Error processing operation %v (ID: %s): %v", reqWrapper.OperationType, reqWrapper.RequestId, opErr)
	}

	// Gửi response wrapper về cho client
	return rss.messageSender.SendMessage(req.Connection(), p_common.Response, respWrapper)
}

// createResponseWrapper tạo một response wrapper chung
func (rss *RemoteStorageService) createResponseWrapper(reqWrapper *pb.DatabaseOperationRequest, payload interface{}, err error) *pb.DatabaseOperationResponse {
	resp := &pb.DatabaseOperationResponse{
		OriginalOperationType: reqWrapper.OperationType,
		RequestId:             reqWrapper.RequestId,
	}

	if err != nil {
		resp.Error = err.Error()
	}

	switch p := payload.(type) {
	case *pb.StorageGetResponse:
		if err != nil {
			p.Error = err.Error()
		}
		resp.Payload = &pb.DatabaseOperationResponse_GetResponse{GetResponse: p}
	case *pb.StoragePutResponse:
		if err != nil {
			p.Error = err.Error()
		}
		resp.Payload = &pb.DatabaseOperationResponse_PutResponse{PutResponse: p}
	case *pb.StorageDeleteResponse:
		if err != nil {
			p.Error = err.Error()
		}
		resp.Payload = &pb.DatabaseOperationResponse_DeleteResponse{DeleteResponse: p}
	case *pb.StorageBatchPutResponse:
		if err != nil {
			p.Error = err.Error()
		}
		resp.Payload = &pb.DatabaseOperationResponse_BatchPutResponse{BatchPutResponse: p}
	case *pb.StorageBatchDeleteResponse:
		if err != nil {
			p.Error = err.Error()
		}
		resp.Payload = &pb.DatabaseOperationResponse_BatchDeleteResponse{BatchDeleteResponse: p}
	case *pb.StorageGetBackupPathResponse:
		if err != nil {
			p.Error = err.Error()
		}
		resp.Payload = &pb.DatabaseOperationResponse_GetBackupPathResponse{GetBackupPathResponse: p}
	}

	return resp
}
