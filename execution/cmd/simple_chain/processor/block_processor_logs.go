// @title processor/block_processor_logs.go
// @markdown processor/block_processor_logs.go - Logs query functionality
package processor

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/processor/rpcquery"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	p_network "github.com/meta-node-blockchain/meta-node/pkg/network"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

const (
	maxTopics         = 4
	maxLogsPerRequest = 5000
	limitBlockRange   = 10000
)

var (
	errInvalidBlockRange = fmt.Errorf("invalid block range params")
	errExceedMaxTopics   = fmt.Errorf("exceed max topics")
)

// GetLogs handles GetLogs requests from network
// Đọc request ID từ Header.ID thay vì từ proto body
func (bp *BlockProcessor) GetLogs(request network.Request) error {
	id := request.Message().ID()

	req := &mt_proto.GetLogsRequest{}
	if err := proto.Unmarshal(request.Message().Body(), req); err != nil {
		logger.Error("GetLogs: Failed to unmarshal request: %v", err)
		return bp.sendLogsError(request, id, fmt.Sprintf("failed to unmarshal request: %v", err))
	}

	// logger.Info("GetLogs: Received request, header ID: %s", id)

	if len(req.Topics) > maxTopics {
		logger.Warn("GetLogs: Too many topics: %d", len(req.Topics))
		return bp.sendLogsError(request, id, "exceed max topics")
	}

	crit := bp.buildFilterCriteria(req)
	logs, err := bp.getLogs(crit)
	if err != nil {
		logger.Error("GetLogs: Failed to get logs: %v", err)
		return bp.sendLogsError(request, id, err.Error())
	}

	protoLogs := bp.convertLogsToProto(logs)
	response := &mt_proto.GetLogsResponse{
		Logs:  protoLogs,
		Error: "",
	}

	responseBytes, err := proto.Marshal(response)
	if err != nil {
		logger.Error("GetLogs: Failed to marshal response: %v", err)
		return bp.sendLogsError(request, id, fmt.Sprintf("failed to marshal response: %v", err))
	}

	respMsg := p_network.NewMessage(&mt_proto.Message{
		Header: &mt_proto.Header{
			Command: command.Logs,
			ID:      id,
		},
		Body: responseBytes,
	})
	return request.Connection().SendMessage(respMsg)
}

// buildFilterCriteria builds filter criteria from proto request
func (bp *BlockProcessor) buildFilterCriteria(req *mt_proto.GetLogsRequest) filters.FilterCriteria {
	crit := filters.FilterCriteria{}
	// Set block hash if provided
	if len(req.BlockHash) > 0 {
		hash := common.BytesToHash(req.BlockHash)
		crit.BlockHash = &hash
	}
	// Set from block if provided
	if len(req.FromBlock) > 0 {
		str := string(req.FromBlock)
		if str != "latest" {
			if fromBlock, ok := new(big.Int).SetString(str, 0); ok {
				crit.FromBlock = fromBlock
			}
		}
	}

	// Set to block if provided
	if len(req.ToBlock) > 0 {
		str := string(req.ToBlock)
		if str != "latest" {
			if toBlock, ok := new(big.Int).SetString(str, 0); ok {
				crit.ToBlock = toBlock
			}
		}
	}
	// Set addresses if provided
	if len(req.Addresses) > 0 {
		addresses := make([]common.Address, len(req.Addresses))
		for i, addr := range req.Addresses {
			addresses[i] = common.BytesToAddress(addr)
		}
		crit.Addresses = addresses
	}

	// Set topics if provided
	if len(req.Topics) > 0 {
		topics := make([][]common.Hash, len(req.Topics))
		for i, topicFilter := range req.Topics {
			if len(topicFilter.Hashes) > 0 {
				hashes := make([]common.Hash, len(topicFilter.Hashes))
				for j, hash := range topicFilter.Hashes {
					hashes[j] = common.BytesToHash(hash)
				}
				topics[i] = hashes
			}
		}
		crit.Topics = topics
	}

	return crit
}

// getLogs retrieves logs based on filter criteria
func (bp *BlockProcessor) getLogs(crit filters.FilterCriteria) ([]*types.Log, error) {
	var eventLogs []*types.Log
	var beginBlock, endBlock *big.Int

	// Determine block range
	if crit.BlockHash != nil {
		blockData, err := bp.chainState.GetBlockDatabase().GetBlockByHash(*crit.BlockHash)
		if err != nil {
			return nil, err
		}
		blockNumber := new(big.Int).SetUint64(blockData.Header().BlockNumber())
		beginBlock = blockNumber
		endBlock = blockNumber
	} else {
		lastBlockNum := bp.GetLastBlock().Header().BlockNumber()

		begin := int64(rpc.LatestBlockNumber) // Latest block
		if crit.FromBlock != nil {
			begin = crit.FromBlock.Int64()
		}
		if begin == int64(rpc.LatestBlockNumber) {
			beginBlock = new(big.Int).SetUint64(lastBlockNum)
		} else {
			beginBlock = new(big.Int).SetInt64(begin)
		}

		end := int64(rpc.LatestBlockNumber) // Latest block
		if crit.ToBlock != nil {
			end = crit.ToBlock.Int64()
		}
		if end == int64(rpc.LatestBlockNumber.Int64()) {
			endBlock = new(big.Int).SetUint64(lastBlockNum)
		} else {
			endBlock = new(big.Int).SetInt64(end)
		}
	}

	// Validate block range
	if beginBlock.Cmp(big.NewInt(0)) > 0 && endBlock.Cmp(big.NewInt(0)) > 0 && beginBlock.Cmp(endBlock) > 0 {
		return nil, errInvalidBlockRange
	}

	// Check maximum block range (10,000 blocks)
	blockDiff := new(big.Int).Sub(endBlock, beginBlock)
	if blockDiff.Cmp(big.NewInt(limitBlockRange)) > 0 {
		return nil, fmt.Errorf("block range too large: max %d blocks allowed", limitBlockRange)
	}

	// Iterate through blocks
	currentBlockNum := new(big.Int).Set(beginBlock)
	for currentBlockNum.Cmp(endBlock) <= 0 {
		hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(currentBlockNum.Uint64())
		if !ok {
			currentBlockNum.Add(currentBlockNum, big.NewInt(1))
			continue
		}

		blockData, err := bp.chainState.GetBlockDatabase().GetBlockByHash(hash)
		if err != nil {
			currentBlockNum.Add(currentBlockNum, big.NewInt(1))
			continue
		}

		rcpDb, err := receipt.NewReceiptsFromRoot(blockData.Header().ReceiptRoot(), bp.storageManager.GetStorageReceipt())
		if err != nil {
			return nil, err
		}

		for _, txsHash := range blockData.Transactions() {
			receipt, err := rcpDb.GetReceipt(txsHash)
			if err != nil {
				return nil, err
			}

			events := receipt.EventLogs()

			for _, eventLog := range events {
				blockNumberUint64 := currentBlockNum.Uint64()

				topics := make([]common.Hash, len(eventLog.Topics))
				for j, topicStr := range eventLog.Topics {
					topics[j] = common.BytesToHash(topicStr)
				}

				// Filter sớm theo address và topics ngay trong vòng lặp
				// để limit maxLogsPerRequest chỉ áp dụng lên matched logs
				logAddr := common.BytesToAddress(eventLog.Address)
				if !rpcquery.MatchAddress(crit.Addresses, logAddr) {
					continue
				}
				if !rpcquery.MatchTopics(crit.Topics, topics) {
					continue
				}

				evL := &types.Log{
					Address:     logAddr,
					BlockNumber: blockNumberUint64,
					Topics:      topics,
					Data:        eventLog.Data,
					TxHash:      common.BytesToHash(eventLog.TransactionHash),
					BlockHash:   hash,
				}
				eventLogs = append(eventLogs, evL)
				if len(eventLogs) > maxLogsPerRequest {
					return nil, fmt.Errorf("log result exceeds maximum of %d entries", maxLogsPerRequest)
				}
			}
		}

		currentBlockNum.Add(currentBlockNum, big.NewInt(1))
	}

	return eventLogs, nil
}

// convertLogsToProto converts Ethereum logs to proto format.
// Delegates to rpcquery.ConvertLogsToProto for the pure conversion logic.
func (bp *BlockProcessor) convertLogsToProto(logs []*types.Log) []*mt_proto.LogEntry {
	return rpcquery.ConvertLogsToProto(logs)
}

// sendLogsError sends error response with header ID
func (bp *BlockProcessor) sendLogsError(request network.Request, id string, errorMsg string) error {
	response := &mt_proto.GetLogsResponse{
		Logs:  nil,
		Error: errorMsg,
	}

	responseBytes, err := proto.Marshal(response)
	if err != nil {
		logger.Error("GetLogs: Failed to marshal error response: %v", err)
		return err
	}

	respMsg := p_network.NewMessage(&mt_proto.Message{
		Header: &mt_proto.Header{
			Command: command.Logs,
			ID:      id,
		},
		Body: responseBytes,
	})
	return request.Connection().SendMessage(respMsg)
}
