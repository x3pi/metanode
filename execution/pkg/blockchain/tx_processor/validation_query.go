package tx_processor

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

func (h *ValidatorHandler) HandleOffChainQuery(tx types.Transaction, chainState *blockchain.ChainState) (types.ExecuteSCResult, error) {
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
	logger.Error("Invoking method: %s", method.Name)
	var isCall bool = false
	var returnData []byte
	var errCall error
	switch method.Name {
	case "getValidatorCount":
		isCall = true
		returnData, errCall = h.handleGetValidatorCount(tx, chainState)
	case "validators":
		isCall = true
		returnData, errCall = h.handleGetValidator(tx, chainState, method, inputData[4:])
	case "balanceOf":
		isCall = true
		returnData, errCall = h.handleBalanceOf(tx, chainState, method, inputData[4:])
	case "delegations":
		isCall = true
		returnData, errCall = h.handleGetDelegations(tx, chainState, method, inputData[4:])
	case "validatorAddresses":
		logger.Error("Go emthoad: %s", method.Name)
		isCall = true
		returnData, errCall = h.handleGetValidatorAddresses(tx, chainState, method, inputData[4:])
	case "validatorIndexes":
		isCall = true
		returnData, errCall = h.handleGetValidatorIndex(tx, chainState, method, inputData[4:])
	case "getPendingRewards":
		isCall = true
		returnData, errCall = h.handleGetPendingRewards(tx, chainState, method, inputData[4:])
	}
	if isCall {
		return smart_contract.NewExecuteSCResult(
			tx.Hash(),
			pb.RECEIPT_STATUS_TRANSACTION_ERROR,
			pb.EXCEPTION_NONE,
			returnData,
			0, // GasUsed là 0 vì là hàm view
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
		), errCall
	}
	var logicErr error
	switch method.Name {
	case "registerValidator":
		_, logicErr = h.handleRegisterValidator(chainState, tx, method, inputData[4:], blockTime)
	case "deregisterValidator":
		_, logicErr = h.handleDeregisterValidator(chainState, tx, blockTime)
	case "setCommissionRate":
		_, logicErr = h.handleSetCommissionRate(inputData[4:], method, chainState, tx, blockTime)
	case "updateValidatorInfo":
		_, logicErr = h.handleUpdateValidatorInfo(inputData[4:], method, chainState, tx, blockTime)
	case "delegate":
		_, logicErr = h.handleDelegate(inputData[4:], method, chainState, tx, blockTime)
	case "undelegate":
		_, logicErr = h.handleUnDelegate(inputData[4:], method, chainState, tx, blockTime)
	case "withdrawReward":
		_, logicErr = h.handleWithdrawRewards(inputData[4:], method, chainState, tx, blockTime)
	case "distributeRewards":
		_, logicErr = h.handleDistributeRewards(inputData[4:], method, chainState, tx, blockTime)
	default:
		logicErr = fmt.Errorf("phương thức '%s' không được hỗ trợ", method.Name)
		logger.Error(logicErr.Error())
	}
	if logicErr != nil {
		revertData := utils.EncodeRevertReason(logicErr.Error())
		return smart_contract.NewExecuteSCResult(
			tx.Hash(),
			pb.RECEIPT_STATUS_TRANSACTION_ERROR,
			pb.EXCEPTION_NONE,
			revertData,
			0, // GasUsed là 0 vì là hàm view
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
		0, // GasUsed là 0 vì là hàm view
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

func (h *ValidatorHandler) handleGetValidatorCount(tx types.Transaction, chainState *blockchain.ChainState) ([]byte, error) {
	stakeStateDB := chainState.GetStakeStateDB()
	count, err := stakeStateDB.GetValidatorCount()
	if err != nil {
		return nil, fmt.Errorf("lỗi khi lấy số lượng validator: %w", err)
	}
	logger.Info("Validator count query returned: %d", count)
	returnData, err := utils.EncodeReturnData("uint256", count)
	if err != nil {
		return nil, err
	}
	return returnData, nil
}

func (h *ValidatorHandler) handleGetValidator(
	tx types.Transaction, chainState *blockchain.ChainState,
	method *abi.Method, inputData []byte,
) ([]byte, error) {
	if len(inputData) < 32 {
		return nil, fmt.Errorf("input data for validator query is too short, expected 32 bytes, got %d", len(inputData))
	}
	stakeStateDB := chainState.GetStakeStateDB()
	// common.BytesToAddress sẽ lấy 20 byte cuối từ slice 32 byte
	validatorAddress := common.BytesToAddress(inputData[0:32])
	validatorState, err := stakeStateDB.GetValidator(validatorAddress)
	if err != nil {
		return nil, fmt.Errorf("error fetching validator state for address %s: %w", validatorAddress.Hex(), err)
	}
	logger.Info("Fetched validator state for address %s: %+v", validatorAddress.Hex(), validatorState)
	// 3. Chuẩn bị dữ liệu để trả về theo đúng cấu trúc của struct Validator
	var returnValues []interface{}
	if validatorState == nil {
		returnValues = []interface{}{
			common.Address{}, // owner
			"",               // name
			"",               // primaryAddress
			"",               // workerAddress
			"",               // p2pAddress
			"",               // description
			"",               // website
			"",               // image
			new(big.Int),     // commissionRate
			new(big.Int),     // minSelfDelegation
			new(big.Int),     // totalStakedAmount
			new(big.Int),     // accumulatedRewardsPerShare
			"",               // hostname
			"",               // authority_key
			"",               // protocol_key
			"",               // network_key
		}
	} else {
		// Nếu tìm thấy, điền các giá trị từ validatorState
		returnValues = []interface{}{
			validatorState.Address(),                                // owner
			validatorState.PrimaryAddress(),                         // primaryAddress
			validatorState.WorkerAddress(),                          // workerAddress
			validatorState.P2PAddress(),                             // p2pAddress
			validatorState.Name(),                                   // name
			validatorState.Description(),                            // description
			validatorState.Website(),                                // website
			validatorState.Image(),                                  // image
			new(big.Int).SetUint64(validatorState.CommissionRate()), // commissionRate
			validatorState.MinSelfDelegation(),                      // minSelfDelegation
			validatorState.TotalStakedAmount(),                      // totalStakedAmount
			validatorState.AccumulatedRewardsPerShare(),             // accumulatedRewardsPerShare
			validatorState.Hostname(),                               // hostname
			validatorState.AuthorityKey(),                           // authority_key (use AuthorityKey() getter)
			validatorState.ProtocolKey(),                            // protocol_key
			validatorState.NetworkKey(),                             // network_key
		}
	}
	logger.Info("Validator query for address %s returned values: %v", validatorAddress.Hex(), returnValues)
	returnData, err := method.Outputs.Pack(returnValues...)
	if err != nil {
		return nil, fmt.Errorf("failed to pack return data for validator query: %w", err)
	}
	// 5. Tạo và trả về ExecuteSCResult
	return returnData, nil
}

// handleBalanceOf triển khai logic cho hàm balanceOf.
func (h *ValidatorHandler) handleBalanceOf(
	tx types.Transaction, chainState *blockchain.ChainState, method *abi.Method, inputData []byte,
) ([]byte, error) {
	// 1. Unpack địa chỉ từ input data
	if len(inputData) < 32 {
		return nil, fmt.Errorf("input data for balanceOf is too short")
	}
	accountAddress := common.BytesToAddress(inputData[0:32])
	// 2. Lấy AccountState từ AccountStateDB
	accountStateDB := chainState.GetAccountStateDB()
	account, err := accountStateDB.AccountState(accountAddress)
	if err != nil {
		return nil, fmt.Errorf("error fetching account state for %s: %w", accountAddress.Hex(), err)
	}
	balance := account.TotalBalance()

	// 4. ABI-encode dữ liệu trả về
	returnData, err := method.Outputs.Pack(balance)
	if err != nil {
		return nil, fmt.Errorf("failed to pack return data for balanceOf: %w", err)
	}

	// 5. Tạo và trả về ExecuteSCResult
	return returnData, nil
}
func (h *ValidatorHandler) handleGetDelegations(tx types.Transaction, chainState *blockchain.ChainState, method *abi.Method, inputData []byte) ([]byte, error) {
	// Hàm này nhận 2 tham số address, mỗi cái 32 byte.
	if len(inputData) < 64 {
		return nil, fmt.Errorf("input data for delegations query is too short, expected 64 bytes, got %d", len(inputData))
	}
	validatorAddress := common.BytesToAddress(inputData[0:32])
	delegatorAddress := common.BytesToAddress(inputData[32:64])
	stakeStateDB := chainState.GetStakeStateDB()
	amount, rewardDebt, err := stakeStateDB.GetDelegation(validatorAddress, delegatorAddress)
	if err != nil {
		return nil, fmt.Errorf("error fetching delegation: %w", err)
	}
	var returnValues = []interface{}{
		amount,
		rewardDebt,
	}
	returnData, err := method.Outputs.Pack(returnValues...)
	if err != nil {
		return nil, fmt.Errorf("failed to pack return data for delegations query: %w", err)
	}
	return returnData, nil
}
func (h *ValidatorHandler) handleGetValidatorAddresses(tx types.Transaction, chainState *blockchain.ChainState, method *abi.Method, inputData []byte) ([]byte, error) {
	if len(inputData) < 32 {
		return nil, fmt.Errorf("input data for validatorAddresses query is too short")
	}
	index := new(big.Int).SetBytes(inputData[0:32])
	logger.Info("Handling validatorAddresses query", "index", index.String())
	stakeStateDB := chainState.GetStakeStateDB()

	// Lấy toàn bộ mảng địa chỉ đã được sắp xếp
	addresses, err := stakeStateDB.GetValidatorAddresses()
	if err != nil {
		return nil, fmt.Errorf("error fetching validator addresses: %w", err)
	}

	idx := int(index.Int64())
	// Kiểm tra chỉ số có hợp lệ không
	if idx < 0 || idx >= len(addresses) {
		return nil, fmt.Errorf("index out of bounds")
	}
	addressAtIndex := addresses[idx]
	returnData, err := method.Outputs.Pack(addressAtIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to pack return data for validatorAddresses query: %w", err)
	}
	return returnData, nil
}

// handleGetValidatorIndex triển khai logic cho hàm getValidatorIndex(address)
func (h *ValidatorHandler) handleGetValidatorIndex(tx types.Transaction, chainState *blockchain.ChainState, method *abi.Method, inputData []byte) ([]byte, error) {
	if len(inputData) < 32 {
		return nil, fmt.Errorf("input data for getValidatorIndex query is too short")
	}
	validatorAddress := common.BytesToAddress(inputData[0:32])
	logger.Info("Handling getValidatorIndex query", "validator", validatorAddress.Hex())

	stakeStateDB := chainState.GetStakeStateDB()

	index, found, err := stakeStateDB.GetValidatorIndex(validatorAddress)
	if err != nil {
		return nil, fmt.Errorf("error fetching validator index: %w", err)
	}

	var returnIndex *big.Int
	if !found {
		returnIndex = big.NewInt(0)
	} else {
		returnIndex = index
	}
	returnData, err := method.Outputs.Pack(returnIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to pack return data for getValidatorIndex query: %w", err)
	}
	return returnData, nil
}
func (h *ValidatorHandler) handleGetPendingRewards(tx types.Transaction, chainState *blockchain.ChainState, method *abi.Method, inputData []byte) ([]byte, error) {
	if len(inputData) < 64 {
		return nil, fmt.Errorf("input data for delegations query is too short, expected 64 bytes, got %d", len(inputData))
	}
	delegatorAddress := common.BytesToAddress(inputData[0:32])
	validatorAddress := common.BytesToAddress(inputData[32:64])
	logger.Info("Handling getPendingRewards query", "validator", validatorAddress.Hex())

	stakeStateDB := chainState.GetStakeStateDB()

	reward, err := stakeStateDB.GetPendingRewards(validatorAddress, delegatorAddress)
	if err != nil {
		return nil, fmt.Errorf("error fetching validator rewards: %w", err)
	}
	returnData, err := method.Outputs.Pack(reward)
	if err != nil {
		return nil, fmt.Errorf("failed to pack return data for getValidatorIndex query: %w", err)
	}
	return returnData, nil
}
