package storage

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"

	// Sửa đường dẫn import proto cho đúng với dự án của bạn
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// RemoteStorage implement Storage interface phía client
type RemoteStorage struct {
	messageSender network.MessageSender
	conn          network.Connection
	pending       sync.Map // An toàn cho goroutine
}

type pendingResponse struct {
	ch   chan *pb.DatabaseOperationResponse
	errc chan error
}

func NewRemoteStorage(messageSender network.MessageSender, conn network.Connection) *RemoteStorage {
	rs := &RemoteStorage{
		messageSender: messageSender,
		conn:          conn,
		pending:       sync.Map{},
	}
	go rs.handleResponses()
	return rs
}

// handleResponses lắng nghe response từ server
func (rs *RemoteStorage) handleResponses() {
	reqChan, errChan := rs.conn.RequestChan()
	for {
		select {
		case req, ok := <-reqChan:
			if !ok {
				logger.Warn("Request channel closed for remote storage client.")
				return
			}
			if req.Message().Command() == p_common.Response {
				respWrapper := &pb.DatabaseOperationResponse{}
				if err := req.Message().Unmarshal(respWrapper); err == nil {
					if val, ok := rs.pending.Load(respWrapper.RequestId); ok {
						pending := val.(*pendingResponse)
						pending.ch <- respWrapper
					}
				} else {
					logger.Error("Failed to unmarshal response wrapper: %v", err)
				}
			}
		case err, ok := <-errChan:
			if !ok {
				logger.Warn("Error channel closed for remote storage client.")
				return
			}
			logger.Error("Received connection error: %v. Notifying pending requests.", err)
			// Báo lỗi cho tất cả request đang chờ
			rs.pending.Range(func(key, value interface{}) bool {
				pending := value.(*pendingResponse)
				pending.errc <- err
				rs.pending.Delete(key)
				return true
			})
			return
		}
	}
}

// sendRequest gửi request và chờ response
func (rs *RemoteStorage) sendRequest(opType pb.OperationType, reqPayload protoreflect.ProtoMessage) (*pb.DatabaseOperationResponse, error) {
	reqID := uuid.New().String()
	reqWrapper := &pb.DatabaseOperationRequest{
		OperationType: opType,
		RequestId:     reqID,
	}

	switch p := reqPayload.(type) {
	case *pb.StorageGetRequest:
		reqWrapper.Payload = &pb.DatabaseOperationRequest_GetRequest{GetRequest: p}
	case *pb.StoragePutRequest:
		reqWrapper.Payload = &pb.DatabaseOperationRequest_PutRequest{PutRequest: p}
	case *pb.StorageDeleteRequest:
		reqWrapper.Payload = &pb.DatabaseOperationRequest_DeleteRequest{DeleteRequest: p}
	case *pb.StorageBatchPutRequest:
		reqWrapper.Payload = &pb.DatabaseOperationRequest_BatchPutRequest{BatchPutRequest: p}
	case *pb.StorageBatchDeleteRequest:
		reqWrapper.Payload = &pb.DatabaseOperationRequest_BatchDeleteRequest{BatchDeleteRequest: p}
	case *pb.StorageGetBackupPathRequest:
		reqWrapper.Payload = &pb.DatabaseOperationRequest_GetBackupPathRequest{GetBackupPathRequest: p}
	default:
		return nil, fmt.Errorf("unsupported request payload type")
	}

	pending := &pendingResponse{
		ch:   make(chan *pb.DatabaseOperationResponse, 1),
		errc: make(chan error, 1),
	}
	rs.pending.Store(reqID, pending)
	defer rs.pending.Delete(reqID)

	err := rs.messageSender.SendMessage(rs.conn, DatabaseOperationCommand, reqWrapper)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	select {
	case resp := <-pending.ch:
		if resp.Error != "" {
			return nil, errors.New(resp.Error)
		}
		return resp, nil
	case err := <-pending.errc:
		return nil, fmt.Errorf("connection error: %w", err)
	case <-time.After(15 * time.Second):
		return nil, errors.New("request timed out")
	}
}

func (rs *RemoteStorage) Get(key []byte) ([]byte, error) {
	req := &pb.StorageGetRequest{Key: key}
	respWrapper, err := rs.sendRequest(pb.OperationType_GET, req)
	if err != nil {
		return nil, err
	}
	resp := respWrapper.GetGetResponse()
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	return resp.Value, nil
}

func (rs *RemoteStorage) Put(key, value []byte) error {
	req := &pb.StoragePutRequest{Key: key, Value: value}
	respWrapper, err := rs.sendRequest(pb.OperationType_PUT, req)
	if err != nil {
		return err
	}
	resp := respWrapper.GetPutResponse()
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}

func (rs *RemoteStorage) Delete(key []byte) error {
	req := &pb.StorageDeleteRequest{Key: key}
	respWrapper, err := rs.sendRequest(pb.OperationType_DELETE, req)
	if err != nil {
		return err
	}
	resp := respWrapper.GetDeleteResponse()
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}

func (rs *RemoteStorage) BatchPut(kvs [][2][]byte) error {
	protoKVs := make([]*pb.KeyValuePair, len(kvs))
	for i, kv := range kvs {
		protoKVs[i] = &pb.KeyValuePair{Key: kv[0], Value: kv[1]}
	}
	req := &pb.StorageBatchPutRequest{Kvs: protoKVs}
	respWrapper, err := rs.sendRequest(pb.OperationType_BATCH_PUT, req)
	if err != nil {
		return err
	}
	resp := respWrapper.GetBatchPutResponse()
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}

func (rs *RemoteStorage) BatchDelete(keys [][]byte) error {
	req := &pb.StorageBatchDeleteRequest{Keys: keys}
	respWrapper, err := rs.sendRequest(pb.OperationType_BATCH_DELETE, req)
	if err != nil {
		return err
	}
	resp := respWrapper.GetBatchDeleteResponse()
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	return nil
}

func (rs *RemoteStorage) GetBackupPath() string {
	req := &pb.StorageGetBackupPathRequest{}
	respWrapper, err := rs.sendRequest(pb.OperationType_GET_BACKUP_PATH, req)
	if err != nil {
		logger.Error("Failed to get backup path: %v", err)
		return ""
	}
	resp := respWrapper.GetGetBackupPathResponse()
	if resp.Error != "" {
		logger.Error("Failed to get backup path from remote: %s", resp.Error)
		return ""
	}
	return resp.Path
}

func (rs *RemoteStorage) Open() error {
	logger.Info("Open called on RemoteStorage: No action needed, connection is managed externally.")
	return nil
}

func (rs *RemoteStorage) Close() error {
	logger.Info("Close called on RemoteStorage: No action needed, connection is managed externally.")
	return nil
}
