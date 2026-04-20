package cross_chain_handler

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// HandleOffChainQuery xử lý các lời gọi eth_call (read-only) đến CROSS_CHAIN_CONTRACT_ADDRESS.
// Pattern giống ValidatorHandler.HandleOffChainQuery trong validation_query.go
func (h *CrossChainHandler) HandleOffChainQuery(tx types.Transaction, chainState *blockchain.ChainState) (types.ExecuteSCResult, error) {
	inputData := tx.CallData().Input()
	if len(inputData) < 4 {
		return nil, errors.New("input data is too short")
	}
	blockTime := uint64(time.Now().Unix())
	methodID := inputData[:4]
	method, err := h.abi.MethodById(methodID)
	if err != nil {
		return nil, fmt.Errorf("method not found for ID %s in ABI: %w", hex.EncodeToString(methodID), err)
	}
	logger.Info("CrossChain offchain query: method=%s", method.Name)

	// Simulate write functions (chạy giả trên temporary chainState)
	var logicErr error
	switch method.Name {
	case "lockAndBridge":
		_, _, logicErr = h.handleLockAndBridge(nil, chainState, tx, method, inputData[4:], common.Address{}, blockTime)
	case "sendMessage":
		_, _, logicErr = h.handleSendMessage(nil, chainState, tx, method, inputData[4:], common.Address{}, blockTime)
	default:
		logicErr = fmt.Errorf("method '%s' không được hỗ trợ", method.Name)
		logger.Error(logicErr.Error())
	}

	if logicErr != nil {
		revertData := utils.EncodeRevertReason(logicErr.Error())
		return smart_contract.NewExecuteSCResult(
			tx.Hash(),
			pb.RECEIPT_STATUS_TRANSACTION_ERROR,
			pb.EXCEPTION_NONE,
			revertData,
			0,
			common.Hash{},
			make(map[string][]byte),
			make(map[string][]byte),
			make(map[string][]byte),
			make(map[string][]byte),
			make(map[string][]byte),
			make(map[string]common.Address),
			make(map[string][]byte),
			make(map[common.Address][]common.Address),
			make(map[common.Address][][2][]byte),
			[]types.EventLog{},
		), nil
	}
	return smart_contract.NewExecuteSCResult(
		tx.Hash(),
		pb.RECEIPT_STATUS_RETURNED,
		pb.EXCEPTION_NONE,
		nil,
		0,
		common.Hash{},
		make(map[string][]byte),
		make(map[string][]byte),
		make(map[string][]byte),
		make(map[string][]byte),
		make(map[string][]byte),
		make(map[string]common.Address),
		make(map[string][]byte),
		make(map[common.Address][]common.Address),
		make(map[common.Address][][2][]byte),
		[]types.EventLog{},
	), nil
}
