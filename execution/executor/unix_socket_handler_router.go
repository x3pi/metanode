package executor

import (
    "fmt"

    "github.com/meta-node-blockchain/meta-node/pkg/logger"
    pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// ProcessProtobufRequest handles a proto request directly, independent of socket logic.
// This is used by both the FFI Bridge and the legacy UnixSocket handler.
func (se *RequestHandler) ProcessProtobufRequest(wrappedRequest *pb.Request) *pb.Response {
	var wrappedResponse *pb.Response
	switch req := wrappedRequest.GetPayload().(type) {
		case *pb.Request_BlockRequest:
			logger.Info("[Go Server] Received BlockRequest for block: %d", req.BlockRequest.GetBlockNumber())
			res, err := se.HandleBlockRequest(req.BlockRequest)
			if err != nil {
				logger.Error("[Go Server] Error handling block request: %v", err)
				// Send error response instead of continue
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				logger.Info("[Go Server] BlockRequest handled, sending ValidatorList with %d validators", len(res.GetValidators()))
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_ValidatorList{
						ValidatorList: res,
					},
				}
			}
		case *pb.Request_StatusRequest:
			logger.Info("[Go Server] Received StatusRequest")
			status := &pb.ServerStatus{
				StatusMessage: "Server is running smoothly",
				UptimeSeconds: 9001,
			}
			wrappedResponse = &pb.Response{
				Payload: &pb.Response_ServerStatus{
					ServerStatus: status,
				},
			}
		case *pb.Request_GetActiveValidatorsRequest:
			logger.Info("[Go Server] Received GetActiveValidatorsRequest for epoch transition")
			res, err := se.HandleGetActiveValidatorsRequest(req.GetActiveValidatorsRequest)
			if err != nil {
				logger.Error("[Go Server] Error handling GetActiveValidatorsRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
				return wrappedResponse
			}
			logger.Info("[Go Server] GetActiveValidatorsRequest handled, sending ValidatorInfoList with %d active validators", len(res.GetValidators()))
			wrappedResponse = &pb.Response{
				Payload: &pb.Response_ValidatorInfoList{
					ValidatorInfoList: res,
				},
			}
		case *pb.Request_GetValidatorsAtBlockRequest:
			logger.Info("[Go Server] Received GetValidatorsAtBlockRequest for block %d", req.GetValidatorsAtBlockRequest.GetBlockNumber())
			res, err := se.HandleGetValidatorsAtBlockRequest(req.GetValidatorsAtBlockRequest)
			if err != nil {
				logger.Error("[Go Server] Error handling GetValidatorsAtBlockRequest: %v", err)
				// Send error response so Rust client knows there was an error
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				logger.Info("[Go Server] GetValidatorsAtBlockRequest handled, sending ValidatorInfoList with %d validators, epoch_timestamp_ms=%d, last_global_exec_index=%d",
					len(res.GetValidators()), res.GetEpochTimestampMs(), res.GetLastGlobalExecIndex())
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_ValidatorInfoList{
						ValidatorInfoList: res,
					},
				}
			}
		case *pb.Request_GetLastBlockNumberRequest:
			res, err := se.HandleGetLastBlockNumberRequest(req.GetLastBlockNumberRequest)
			if err != nil {
				logger.Error("[Go Server] Error handling GetLastBlockNumberRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_LastBlockNumberResponse{
						LastBlockNumberResponse: res,
					},
				}
			}
		case *pb.Request_GetCurrentEpochRequest:
			res, err := se.HandleGetCurrentEpochRequest(req.GetCurrentEpochRequest)
			if err != nil {
				logger.Error("[Go Server] Error handling GetCurrentEpochRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_GetCurrentEpochResponse{
						GetCurrentEpochResponse: res,
					},
				}
			}
		case *pb.Request_GetEpochStartTimestampRequest:
			res, err := se.HandleGetEpochStartTimestampRequest(req.GetEpochStartTimestampRequest)
			if err != nil {
				logger.Error("[Go Server] Error handling GetEpochStartTimestampRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				logger.Info("[Go Server] GetEpochStartTimestampRequest handled, sending response with timestamp_ms=%d", res.GetTimestampMs())
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_GetEpochStartTimestampResponse{
						GetEpochStartTimestampResponse: res,
					},
				}
			}
		case *pb.Request_AdvanceEpochRequest:
			logger.Info("[Go Server] Received AdvanceEpochRequest (epoch transition completion) epoch %d, timestamp %d",
				req.AdvanceEpochRequest.GetNewEpoch(), req.AdvanceEpochRequest.GetEpochStartTimestampMs())
			res, err := se.HandleAdvanceEpochRequest(req.AdvanceEpochRequest)
			if err != nil {
				logger.Error("[Go Server] Error handling AdvanceEpochRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				logger.Info("[Go Server] AdvanceEpochRequest handled, sending response with epoch=%d, timestamp_ms=%d",
					res.GetNewEpoch(), res.GetEpochStartTimestampMs())
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_AdvanceEpochResponse{
						AdvanceEpochResponse: res,
					},
				}
			}
		case *pb.Request_ForceCommitRequest:
			res, err := se.HandleForceCommitRequest(req.ForceCommitRequest)
			if err != nil {
				logger.Error("[Go Server] Error handling ForceCommitRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_ForceCommitResponse{
						ForceCommitResponse: res,
					},
				}
			}
		case *pb.Request_GetEpochBoundaryDataRequest:
			logger.Info("[Go Server] Received GetEpochBoundaryDataRequest (unified epoch boundary) for epoch %d", req.GetEpochBoundaryDataRequest.GetEpoch())
			res, err := se.HandleGetEpochBoundaryDataRequest(req.GetEpochBoundaryDataRequest)
			if err != nil {
				logger.Error("[Go Server] Error handling GetEpochBoundaryDataRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				logger.Info("[Go Server] GetEpochBoundaryDataRequest handled, sending EpochBoundaryData with epoch=%d, timestamp_ms=%d, boundary_block=%d, validator_count=%d",
					res.GetEpoch(), res.GetEpochStartTimestampMs(), res.GetBoundaryBlock(), len(res.GetValidators()))
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_EpochBoundaryData{
						EpochBoundaryData: res,
					},
				}
			}
		case *pb.Request_SetConsensusStartBlockRequest:
			blockNumber := req.SetConsensusStartBlockRequest.GetBlockNumber()
			logger.Info("[Go Server] 📥 Received SetConsensusStartBlockRequest: consensus will start at block %d", blockNumber)
			res, err := se.HandleSetConsensusStartBlockRequest(req.SetConsensusStartBlockRequest)
			if err != nil {
				logger.Error("[Go Server] ❌ Error handling SetConsensusStartBlockRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				logger.Info("[Go Server] ✅ SetConsensusStartBlockRequest processed: last_sync_block=%d", res.GetLastSyncBlock())
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_SetConsensusStartBlockResponse{
						SetConsensusStartBlockResponse: res,
					},
				}
			}
		case *pb.Request_SetSyncStartBlockRequest:
			lastConsensusBlock := req.SetSyncStartBlockRequest.GetLastConsensusBlock()
			logger.Info("[Go Server] 📥 Received SetSyncStartBlockRequest: consensus ended at block %d", lastConsensusBlock)
			res, err := se.HandleSetSyncStartBlockRequest(req.SetSyncStartBlockRequest)
			if err != nil {
				logger.Error("[Go Server] ❌ Error handling SetSyncStartBlockRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				logger.Info("[Go Server] ✅ SetSyncStartBlockRequest processed: sync_start_block=%d", res.GetSyncStartBlock())
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_SetSyncStartBlockResponse{
						SetSyncStartBlockResponse: res,
					},
				}
			}
		case *pb.Request_WaitForSyncToBlockRequest:
			targetBlock := req.WaitForSyncToBlockRequest.GetTargetBlock()
			timeoutSecs := req.WaitForSyncToBlockRequest.GetTimeoutSeconds()
			logger.Info("[Go Server] 📥 Received WaitForSyncToBlockRequest: target=%d, timeout=%ds", targetBlock, timeoutSecs)
			res, err := se.HandleWaitForSyncToBlockRequest(req.WaitForSyncToBlockRequest)
			if err != nil {
				logger.Error("[Go Server] ❌ Error handling WaitForSyncToBlockRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				logger.Info("[Go Server] ✅ WaitForSyncToBlockRequest processed: reached=%v, current_block=%d", res.GetReached(), res.GetCurrentBlock())
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_WaitForSyncToBlockResponse{
						WaitForSyncToBlockResponse: res,
					},
				}
			}
		case *pb.Request_GetBlocksRangeRequest:
			fromBlock := req.GetBlocksRangeRequest.GetFromBlock()
			toBlock := req.GetBlocksRangeRequest.GetToBlock()
			logger.Debug("[Go Server] 📥 Received GetBlocksRangeRequest: from=%d, to=%d", fromBlock, toBlock)
			res, err := se.HandleGetBlocksRangeRequest(req.GetBlocksRangeRequest)
			if err != nil {
				logger.Error("[Go Server] ❌ Error handling GetBlocksRangeRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				logger.Debug("[Go Server] ✅ GetBlocksRangeRequest processed: count=%d", res.GetCount())
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_GetBlocksRangeResponse{
						GetBlocksRangeResponse: res,
					},
				}
			}
		case *pb.Request_SyncBlocksRequest:
			blockCount := len(req.SyncBlocksRequest.GetBlocks())
			logger.Info("[Go Server] 📥 Received SyncBlocksRequest: block_count=%d", blockCount)
			res, err := se.HandleSyncBlocksRequest(req.SyncBlocksRequest)
			if err != nil {
				logger.Error("[Go Server] ❌ Error handling SyncBlocksRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				logger.Info("[Go Server] ✅ SyncBlocksRequest processed: synced_count=%d, last_synced_block=%d", res.GetSyncedCount(), res.GetLastSyncedBlock())
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_SyncBlocksResponse{
						SyncBlocksResponse: res,
					},
				}
			}
		case *pb.Request_GetLastHandledCommitIndexRequest:
			logger.Info("[Go Server] 📥 Received GetLastHandledCommitIndexRequest (GO-AUTH GEI recovery)")
			res, err := se.HandleGetLastHandledCommitIndexRequest(req.GetLastHandledCommitIndexRequest)
			if err != nil {
				logger.Error("[Go Server] ❌ Error handling GetLastHandledCommitIndexRequest: %v", err)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: err.Error(),
					},
				}
			} else {
				logger.Info("[Go Server] ✅ GetLastHandledCommitIndexRequest: last_commit=%d, last_gei=%d, block=%d, epoch=%d",
					res.GetLastCommitIndex(), res.GetLastGei(), res.GetLastBlockNumber(), res.GetEpoch())
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_GetLastHandledCommitIndexResponse{
						GetLastHandledCommitIndexResponse: res,
					},
				}
			}
		default:
			logger.Error("[Go Server] Unknown request type: %T", req)
			// Send error response instead of continue
			wrappedResponse = &pb.Response{
				Payload: &pb.Response_Error{
					Error: fmt.Sprintf("Unknown request type: %T", req),
				},
			}
		}

	if wrappedResponse == nil {
		logger.Error("[RequestHandler] wrappedResponse is nil - this is a bug!")
		wrappedResponse = &pb.Response{
			Payload: &pb.Response_Error{
				Error: "Internal server error: response is nil",
			},
		}
	}
	return wrappedResponse
}
