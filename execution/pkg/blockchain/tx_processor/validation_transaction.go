package tx_processor

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/state"

	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types"
)

// Nó không còn chứa chainState nữa.
type ValidatorHandler struct {
	abi abi.ABI
}

var (
	validatorHandlerInstance *ValidatorHandler
	onceVal                  sync.Once
)

// GetValidatorHandler trả về instance duy nhất của ValidatorHandler.
// Nó sẽ được tạo ra trong lần gọi đầu tiên.
func GetValidatorHandler() (*ValidatorHandler, error) {
	var err error
	onceVal.Do(func() {
		var parsedABI abi.ABI
		parsedABI, err = abi.JSON(strings.NewReader(abi_contract.ValidationABI))
		if err != nil {
			return
		}
		validatorHandlerInstance = &ValidatorHandler{
			abi: parsedABI,
		}
	})

	if err != nil {
		return nil, err // Trả về lỗi nếu việc parse ABI thất bại
	}
	return validatorHandlerInstance, nil
}
func (h *ValidatorHandler) HandleTransaction(ctx context.Context, chainState *blockchain.ChainState, tx types.Transaction, enableTrace bool, blockTime uint64) (types.Receipt, types.ExecuteSCResult, bool) {
	toAddress := tx.ToAddress()
	inputData := tx.CallData().Input()
	if len(inputData) < 4 {
		err := fmt.Errorf("dữ liệu input không hợp lệ")
		rcp := createErrorReceipt(tx, toAddress, err)
		return rcp, nil, true // hasFailed = true
	}

	method, err := h.abi.MethodById(inputData[:4])
	if err != nil {
		logger.Error("Không tìm thấy method tương ứng với ID trong ABI: %v", err)
		rcp := createErrorReceipt(tx, toAddress, err)
		return rcp, nil, true
	}
	var eventLogs []types.EventLog
	var logicErr error
	var isCall bool = false
	// Hiện tại chỉ có 1 method, nhưng dùng switch sẽ dễ mở rộng sau này
	switch method.Name {
	case "registerValidator":
		isCall = true
		eventLogs, logicErr = h.handleRegisterValidator(chainState, tx, method, inputData[4:], blockTime)
	case "deregisterValidator":
		isCall = true
		eventLogs, logicErr = h.handleDeregisterValidator(chainState, tx, blockTime)
	case "setCommissionRate":
		isCall = true
		eventLogs, logicErr = h.handleSetCommissionRate(inputData[4:], method, chainState, tx, blockTime)
	case "updateValidatorInfo":
		isCall = true
		eventLogs, logicErr = h.handleUpdateValidatorInfo(inputData[4:], method, chainState, tx, blockTime)
	case "delegate":
		isCall = true
		eventLogs, logicErr = h.handleDelegate(inputData[4:], method, chainState, tx, blockTime)
	case "undelegate":
		isCall = true
		eventLogs, logicErr = h.handleUnDelegate(inputData[4:], method, chainState, tx, blockTime)
	case "withdrawReward":
		isCall = true
		eventLogs, logicErr = h.handleWithdrawRewards(inputData[4:], method, chainState, tx, blockTime)
	case "distributeRewards":
		isCall = true
		eventLogs, logicErr = h.handleDistributeRewards(inputData[4:], method, chainState, tx, blockTime)
	}
	if isCall {
		if logicErr != nil {
			logger.Error("Lỗi: %v", logicErr)
			return HandleRevertedTransaction(ctx, chainState, tx, toAddress, blockTime, enableTrace, logicErr.Error())
		}
		return HandleSuccessTransaction(ctx, chainState, tx, toAddress, blockTime, enableTrace, eventLogs, nil)
	}

	var returnData []byte
	var errCall error
	switch method.Name {
	case "getValidatorCount":
		returnData, errCall = h.handleGetValidatorCount(tx, chainState)
	case "validators":
		returnData, errCall = h.handleGetValidator(tx, chainState, method, inputData[4:])
	case "balanceOf":
		returnData, errCall = h.handleBalanceOf(tx, chainState, method, inputData[4:])
	case "validatorAddresses":
		returnData, errCall = h.handleGetValidatorAddresses(tx, chainState, method, inputData[4:])
	case "validatorIndexes":
		returnData, errCall = h.handleGetValidatorIndex(tx, chainState, method, inputData[4:])
	case "getPendingRewards":
		returnData, errCall = h.handleGetPendingRewards(tx, chainState, method, inputData[4:])
	}
	if errCall != nil {
		logger.Error("Lỗi: %v", errCall)
		return HandleRevertedTransaction(ctx, chainState, tx, toAddress, blockTime, enableTrace, errCall.Error())
	}
	return HandleSuccessTransaction(ctx, chainState, tx, toAddress, blockTime, enableTrace, eventLogs, returnData)

}

func (h *ValidatorHandler) handleRegisterValidator(
	chainState *blockchain.ChainState, tx types.Transaction,
	method *abi.Method, inputData []byte,
	blockTime uint64,
) ([]types.EventLog, error) {
	stakeStateDB := chainState.GetStakeStateDB()
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("Lỗi khi unpack input data: %v", err)
	}
	if len(args) != 13 {
		logger.Error("số lượng tham số không đúng cho registerValidator, expected 13, got %d", len(args))
		return nil, fmt.Errorf("số lượng tham số không đúng cho registerValidator: expected 13, got %d", len(args))
	}
	primaryAddress, _ := args[0].(string)
	workerAddress, _ := args[1].(string)
	p2pAddress, _ := args[2].(string)
	name, _ := args[3].(string)
	description, _ := args[4].(string)
	website, _ := args[5].(string)
	image, _ := args[6].(string)
	commissionRate, _ := args[7].(uint64)
	minSelfDelegation, _ := args[8].(*big.Int)
	networkKey, _ := args[9].(string)
	hostname, _ := args[10].(string)
	authorityKey, _ := args[11].(string)
	protocolKey, _ := args[12].(string)

	validator, err := stakeStateDB.GetValidator(tx.FromAddress())
	if err != nil {
		return nil, fmt.Errorf("Lỗi khi kiểm tra validator tồn tại: %v", err)
	}
	if validator != nil {
		return nil, fmt.Errorf("Validator already exists")
	}
	if commissionRate > 10000 {
		return nil, fmt.Errorf("Commission rate too high")
	}
	// CRITICAL FIX: Use authorityKey (Base64 format) for both PubKeyBls and AuthorityKey
	// This ensures consistency when building committee for epoch transition.
	// Previously, pubKeyBls was retrieved from account state as hex, causing format mismatch.

	// Use CreateRegisterWithKeys with all new fields for committee.json compatibility
	stakeStateDB.CreateRegisterWithKeys(
		tx.FromAddress(),
		name,
		description,
		website,
		image,
		commissionRate,
		minSelfDelegation,
		primaryAddress,
		workerAddress,
		p2pAddress,
		authorityKey, // Use authorityKey (Base64) for pubKeyBls to ensure consistent format
		protocolKey,  // protocol_key (Ed25519) from input
		networkKey,   // network_key (Ed25519) from input
		hostname,     // hostname from input
		authorityKey, // authority_key (BLS) from input
	)
	// ---- Tạo Event Log ----
	event, ok := h.abi.Events["ValidatorRegistered"]
	if !ok {
		return nil, fmt.Errorf("không tìm thấy event 'ValidatorRegistered' trong ABI")
	}
	validatorAddr := tx.FromAddress()
	eventData, err := event.Inputs.NonIndexed().Pack(name)
	if err != nil {
		return nil, fmt.Errorf("Lỗi khi pack dữ liệu event: %v", err)
	}
	eventLog := smart_contract.NewEventLog(
		tx.Hash(),
		tx.ToAddress(),
		eventData,
		[][]byte{
			event.ID.Bytes(),
			validatorAddr.Bytes(),
		},
	)
	eventLogs := []types.EventLog{eventLog}

	// 📢 Notify Rust Metanode about committee change
	validatorCount, _ := stakeStateDB.GetValidatorCount()
	executor.NotifyValidatorRegistered(0, validatorAddr.Hex(), uint64(validatorCount))

	return eventLogs, nil
}

func (h *ValidatorHandler) handleDeregisterValidator(
	chainState *blockchain.ChainState, tx types.Transaction,
	blockTime uint64,
) ([]types.EventLog, error) {
	validatorAddr := tx.FromAddress()
	stakeStateDB := chainState.GetStakeStateDB()
	validator, err := stakeStateDB.GetValidator(tx.FromAddress())
	if err != nil {
		return nil, fmt.Errorf("Lỗi khi kiểm tra validator tồn tại: %v", err)
	}
	if validator == nil {
		return nil, fmt.Errorf("Validator không tồn tại")
	}
	if validator.TotalStakedAmount().Sign() > 0 {
		return nil, fmt.Errorf("Validator still has staked tokens")
	}
	err = stakeStateDB.DeleteValidator(validatorAddr)
	if err != nil {
		return nil, fmt.Errorf("Lỗi khi xóa validator: %v", err)
	}
	event, ok := h.abi.Events["ValidatorDeregistered"]
	if !ok {
		return nil, fmt.Errorf("không tìm thấy event 'ValidatorDeregistered' trong ABI")
	}
	eventLog := smart_contract.NewEventLog(
		tx.Hash(),
		tx.ToAddress(),
		nil,
		[][]byte{
			event.ID.Bytes(),
			validatorAddr.Bytes(),
		},
	)
	eventLogs := []types.EventLog{eventLog}

	// 📢 Notify Rust Metanode about committee change
	validatorCount, _ := stakeStateDB.GetValidatorCount()
	executor.NotifyValidatorDeregistered(0, validatorAddr.Hex(), uint64(validatorCount))

	return eventLogs, nil
}
func (h *ValidatorHandler) handleSetCommissionRate(
	inputData []byte, method *abi.Method, chainState *blockchain.ChainState,
	tx types.Transaction, blockTime uint64,
) ([]types.EventLog, error) {
	validatorAddr := tx.FromAddress()
	stakeStateDB := chainState.GetStakeStateDB()
	validator, err := stakeStateDB.GetValidator(validatorAddr)
	if err != nil {
		return nil, fmt.Errorf("lỗi khi kiểm tra validator: %w", err)
	}
	if validator == nil {
		return nil, fmt.Errorf("not a validator")
	}
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("lỗi unpack input: %w", err)
	}
	newRate, _ := args[0].(uint64)

	stakeStateDB.SetCommissionRate(validatorAddr, newRate)

	event, ok := h.abi.Events["CommissionRateUpdated"]
	if !ok {
		return nil, fmt.Errorf("không tìm thấy event CommissionRateUpdated")
	}

	eventData, err := event.Inputs.NonIndexed().Pack(newRate)
	if err != nil {
		return nil, fmt.Errorf("lỗi pack event: %w", err)
	}

	eventLog := smart_contract.NewEventLog(
		tx.Hash(), tx.ToAddress(), eventData,
		[][]byte{event.ID.Bytes(), validatorAddr.Bytes()},
	)

	return []types.EventLog{eventLog}, nil
}
func (h *ValidatorHandler) handleUpdateValidatorInfo(
	inputData []byte, method *abi.Method, chainState *blockchain.ChainState,
	tx types.Transaction, blockTime uint64,
) ([]types.EventLog, error) {
	validatorAddr := tx.FromAddress()
	stakeStateDB := chainState.GetStakeStateDB()
	validator, err := stakeStateDB.GetValidator(validatorAddr)
	if err != nil {
		return nil, fmt.Errorf("lỗi kiểm tra validator: %w", err)
	}
	if validator == nil {
		return nil, fmt.Errorf("not a validator")
	}

	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("lỗi unpack input: %w", err)
	}

	name, _ := args[0].(string)
	description, _ := args[1].(string)
	website, _ := args[2].(string)
	image, _ := args[3].(string)

	stakeStateDB.UpdateValidatorInfo(validatorAddr, name, description, website, image)

	event, ok := h.abi.Events["ValidatorInfoUpdated"]
	if !ok {
		return nil, fmt.Errorf("không tìm thấy event ValidatorInfoUpdated")
	}

	eventLog := smart_contract.NewEventLog(
		tx.Hash(), tx.ToAddress(), nil,
		[][]byte{event.ID.Bytes(), validatorAddr.Bytes()},
	)

	return []types.EventLog{eventLog}, nil
}
func (h *ValidatorHandler) handleDelegate(
	inputData []byte, method *abi.Method, chainState *blockchain.ChainState,
	tx types.Transaction, blockTime uint64,
) ([]types.EventLog, error) {
	stakeStateDB := chainState.GetStakeStateDB()
	delegatorAddr := tx.FromAddress()
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("lỗi unpack input: %w", err)
	}
	acc, _ := chainState.GetAccountStateDB().AccountState(tx.ToAddress())
	delegator, _ := chainState.GetAccountStateDB().AccountState(delegatorAddr)
	_validatorAddr, ok := args[0].(common.Address)
	if !ok {
		return nil, fmt.Errorf("invalid validator address type")
	}
	if tx.Amount().Cmp(big.NewInt(1000)) < 0 {
		return nil, fmt.Errorf("Minimum stake amount is 1000")
	}
	validator, err := stakeStateDB.GetValidator(_validatorAddr)
	if err != nil {
		return nil, fmt.Errorf("lỗi kiểm tra validator: %w", err)
	}
	if validator == nil {
		return nil, fmt.Errorf("validator does not exist")
	}
	withdrawEvent, err := h._withdrawRewards(tx, chainState, validator, delegatorAddr, big.NewInt(0))
	if err != nil {
		return nil, fmt.Errorf("lỗi khi rút thưởng tự động: %w", err)
	}
	err = chainState.TransferFrom(delegator, acc, tx.Amount())
	if err != nil {
		return nil, err
	}
	err = stakeStateDB.Delegate(_validatorAddr, delegatorAddr, tx.Amount())
	if err != nil {
		return nil, fmt.Errorf("lỗi khi delegate: %w", err)
	}
	event, ok := h.abi.Events["Delegated"]
	if !ok {
		return nil, fmt.Errorf("không tìm thấy event Delegated")
	}

	eventData, err := event.Inputs.NonIndexed().Pack(tx.Amount())
	if err != nil {
		return nil, fmt.Errorf("lỗi pack event Delegated: %w", err)
	}
	eventLog := smart_contract.NewEventLog(
		tx.Hash(), tx.ToAddress(), eventData,
		[][]byte{event.ID.Bytes(), delegatorAddr.Bytes()},
	)
	eventLogs := []types.EventLog{eventLog}
	if withdrawEvent != nil {
		eventLogs = append(eventLogs, withdrawEvent)
	}
	return eventLogs, nil
}

func (h *ValidatorHandler) handleUnDelegate(
	inputData []byte, method *abi.Method, chainState *blockchain.ChainState,
	tx types.Transaction, blockTime uint64,
) ([]types.EventLog, error) {
	stakeStateDB := chainState.GetStakeStateDB()
	delegatorAddr := tx.FromAddress()

	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("lỗi unpack input: %w", err)
	}

	_validatorAddr, _ := args[0].(common.Address)
	amount, _ := args[1].(*big.Int)
	validator, err := stakeStateDB.GetValidator(_validatorAddr)
	if err != nil {
		return nil, fmt.Errorf("lỗi lấy validator: %w", err)
	}
	if validator == nil {
		return nil, fmt.Errorf("validator does not exist")
	}
	amountDelegated, _, err := stakeStateDB.GetDelegation(_validatorAddr, tx.FromAddress())
	if err != nil {
		return nil, fmt.Errorf("error fetching delegation: %w", err)
	}
	if amountDelegated.Cmp(amount) < 0 {
		return nil, fmt.Errorf("insufficient delegated amount")
	}
	amountDelegated.Sub(amountDelegated, amount)
	if tx.FromAddress() == _validatorAddr {
		if amountDelegated.Cmp(validator.MinSelfDelegation()) < 0 {
			return nil, fmt.Errorf("self-delegation cannot be less than minimum self-delegation")
		}
	}
	// RÚT THƯỞNG
	withdrawEvent, err := h._withdrawRewards(tx, chainState, validator, delegatorAddr, amount)
	if err != nil {
		return nil, fmt.Errorf("lỗi khi rút thưởng tự động: %w", err)
	}
	err = stakeStateDB.Undelegate(delegatorAddr, _validatorAddr, amount)
	if err != nil {
		return nil, fmt.Errorf("lỗi khi undelegate: %w", err)
	}
	// RÚT TIỀN CHO USER
	logger.Info("Amount to undelegate: %s", amount.String())
	acc, _ := chainState.GetAccountStateDB().AccountState(tx.ToAddress())
	delegator, _ := chainState.GetAccountStateDB().AccountState(delegatorAddr)
	err = chainState.TransferFrom(acc, delegator, amount)
	if err != nil {
		return nil, fmt.Errorf("Lỗi chuyển tiền: %w", err)
	}
	eventDef, ok := h.abi.Events["Undelegated"]
	if !ok {
		return nil, fmt.Errorf("không tìm thấy event Undelegated")
	}

	eventData, err := eventDef.Inputs.NonIndexed().Pack(amount)
	if err != nil {
		return nil, fmt.Errorf("lỗi pack event: %w", err)
	}
	eventLog := smart_contract.NewEventLog(
		tx.Hash(), tx.ToAddress(), eventData,
		[][]byte{
			eventDef.ID.Bytes(),
			delegatorAddr.Bytes(),
			_validatorAddr.Bytes(),
		},
	)

	eventLogs := []types.EventLog{eventLog}
	if withdrawEvent != nil {
		eventLogs = append(eventLogs, withdrawEvent)
	}

	return eventLogs, nil
}

func (h *ValidatorHandler) _withdrawRewards(tx types.Transaction, chainState *blockchain.ChainState, validator state.ValidatorState, delegatorAddr common.Address, amount *big.Int) (types.EventLog, error) {
	acc, _ := chainState.GetAccountStateDB().AccountState(tx.ToAddress())
	delegator, _ := chainState.GetAccountStateDB().AccountState(delegatorAddr)
	// check tiền rút có lớn hơn tiền SM
	rewardAmount, err := chainState.GetStakeStateDB().WithdrawRewardFromValidator(validator.Address(), delegatorAddr)
	if err != nil {
		return nil, err
	}
	logger.Info("Withdraw rewards called: totalbalance %s,  rewardAmount %s , amount %d", acc.TotalBalance(), rewardAmount.String(), amount)
	if acc.TotalBalance().Cmp(new(big.Int).Add(rewardAmount, amount)) < 0 {
		return nil, fmt.Errorf("Insufficient validator balance.")
	}
	err = chainState.TransferFrom(acc, delegator, rewardAmount)
	if err != nil {
		return nil, err
	}
	eventDef, ok := h.abi.Events["RewardWithdrawn"]
	if !ok {
		return nil, fmt.Errorf("không tìm thấy event 'RewardWithdrawn' trong ABI")
	}
	eventData, err := eventDef.Inputs.NonIndexed().Pack(rewardAmount)
	if err != nil {
		return nil, fmt.Errorf("lỗi khi pack dữ liệu event RewardWithdrawn: %w", err)
	}
	eventLog := smart_contract.NewEventLog(tx.Hash(), tx.ToAddress(), eventData, [][]byte{
		eventDef.ID.Bytes(),         // topic0: signature
		delegatorAddr.Bytes(),       // topic1: delegator (indexed)
		validator.Address().Bytes(), // topic2: validator (indexed)
	})
	return eventLog, nil
}
func (h *ValidatorHandler) handleWithdrawRewards(
	inputData []byte,
	method *abi.Method,
	chainState *blockchain.ChainState,
	tx types.Transaction,
	blockTime uint64,
) ([]types.EventLog, error) {
	stakeStateDB := chainState.GetStakeStateDB()
	delegatorAddr := tx.FromAddress()

	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("unpack input data for withdrawReward: %w", err)
	}

	validatorAddr, _ := args[0].(common.Address)
	validator, err := stakeStateDB.GetValidator(validatorAddr)
	if err != nil {
		return nil, fmt.Errorf("error getting validator state: %w", err)
	}
	if validator == nil {
		return nil, fmt.Errorf("validator does not exist")
	}

	eventLog, err := h._withdrawRewards(tx, chainState, validator, delegatorAddr, big.NewInt(0))
	if err != nil {
		return nil, fmt.Errorf("error withdrawing rewards: %w", err)
	}
	stakeStateDB.ResetRewardDebtForDelegator(validatorAddr, tx.FromAddress())
	var eventLogs []types.EventLog
	if eventLog != nil {
		eventLogs = append(eventLogs, eventLog)
	}

	return eventLogs, nil
}
func (h *ValidatorHandler) handleDistributeRewards(

	inputData []byte,
	method *abi.Method,
	chainState *blockchain.ChainState,
	tx types.Transaction,
	blockTime uint64,
) ([]types.EventLog, error) {

	toAddress := tx.ToAddress()
	accountStateDB := chainState.GetAccountStateDB()
	stakeStateDB := chainState.GetStakeStateDB()

	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("unpack distributeRewards params: %w", err)
	}

	validatorAddr, _ := args[0].(common.Address)
	rewardAmount, _ := args[1].(*big.Int)

	if rewardAmount.Sign() <= 0 {
		return nil, fmt.Errorf("reward must be positive")
	}

	validator, err := stakeStateDB.GetValidator(validatorAddr)
	if err != nil {
		return nil, fmt.Errorf("get validator state: %w", err)
	}
	if validator == nil {
		return nil, fmt.Errorf("validator does not exist")
	}

	contractAccount, err := accountStateDB.AccountState(toAddress)
	if err != nil {
		return nil, fmt.Errorf("get contract account state: %w", err)
	}
	if contractAccount.TotalBalance().Cmp(rewardAmount) < 0 {
		return nil, fmt.Errorf("insufficient contract balance to distribute rewards")
	}
	if validator.TotalStakedAmount().Sign() <= 0 {
		return nil, fmt.Errorf("no delegators staking with this validator")
	}
	reward, _ := stakeStateDB.DistributeRewardsToValidator(validatorAddr, rewardAmount)
	if rewardAmount.Sign() > 0 {
		ownerAddr := validator.Address()
		ownerAccount, _ := accountStateDB.AccountState(ownerAddr)

		err = chainState.TransferFrom(contractAccount, ownerAccount, big.NewInt(0).Sub(rewardAmount, reward))
		if err != nil {
			return nil, fmt.Errorf("failed to transfer commission: %w", err)
		}
	}
	eventDef, ok := h.abi.Events["RewardsDistributed"]
	if !ok {
		return nil, fmt.Errorf("event 'RewardsDistributed' not found in ABI")
	}

	eventData, err := eventDef.Inputs.NonIndexed().Pack(rewardAmount)
	if err != nil {
		return nil, fmt.Errorf("pack event data 'RewardsDistributed': %w", err)
	}

	eventLog := smart_contract.NewEventLog(
		tx.Hash(),
		toAddress,
		eventData,
		[][]byte{
			eventDef.ID.Bytes(),
			validatorAddr.Bytes(),
		},
	)
	return []types.EventLog{eventLog}, nil
}
