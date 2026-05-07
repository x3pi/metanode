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
		return nil, fmt.Errorf("error unpacking input data: %v", err)
	}
	if len(args) != 13 {
		logger.Error("invalid number of arguments for registerValidator, expected 13, got %d", len(args))
		return nil, fmt.Errorf("invalid number of arguments for registerValidator: expected 13, got %d", len(args))
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
		return nil, fmt.Errorf("error checking validator existence: %v", err)
	}
	if validator != nil {
		return nil, fmt.Errorf("validator already exists")
	}
	if commissionRate > 10000 {
		return nil, fmt.Errorf("Commission rate too high")
	}

	// Kiểm tra trùng lặp các keys (Authority, Protocol, Network)
	allValidators, errGetAll := stakeStateDB.GetAllValidators()
	if errGetAll == nil {
		for _, v := range allValidators {
			if v.AuthorityKey() == authorityKey {
				return nil, fmt.Errorf("Authority key already in use")
			}
			if v.ProtocolKey() == protocolKey {
				return nil, fmt.Errorf("Protocol key already in use")
			}
			if v.NetworkKey() == networkKey {
				return nil, fmt.Errorf("Network key already in use")
			}
		}
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

// handleDeregisterValidator: Hủy đăng ký validator trong 1 transaction.
// Tự động:
//  1. Rút hết tiền đã delegate vào B, C, ... trả về ví validator.
//  2. Kiểm tra không còn external delegators (nếu còn → từ chối).
//  3. Hoàn trả self-stake về ví (bypass minSelfDelegation).
//  4. Xóa validator khỏi mạng + notify Rust consensus.
func (h *ValidatorHandler) handleDeregisterValidator(
	chainState *blockchain.ChainState, tx types.Transaction,
	blockTime uint64,
) ([]types.EventLog, error) {
	validatorAddr := tx.FromAddress()
	stakeStateDB := chainState.GetStakeStateDB()
	contractAcc, _ := chainState.GetAccountStateDB().AccountState(tx.ToAddress())
	validatorAcc, _ := chainState.GetAccountStateDB().AccountState(validatorAddr)

	validator, err := stakeStateDB.GetValidator(validatorAddr)
	if err != nil {
		return nil, fmt.Errorf("error checking validator existence: %v", err)
	}
	if validator == nil {
		return nil, fmt.Errorf("validator does not exist")
	}

	var eventLogs []types.EventLog

	// ── Bước 1: Tự động rút tiền đã delegate vào các validator khác (B, C, ...) ──
	// Tìm tất cả validator mà validatorAddr đã stake vào (trừ self)
	stakedInValidators := stakeStateDB.GetValidatorsStakedInByAddress(validatorAddr)
	for _, targetValAddr := range stakedInValidators {
		if targetValAddr == validatorAddr {
			continue // self-stake xử lý riêng ở bước 2
		}
		amount, _, errD := stakeStateDB.GetDelegation(targetValAddr, validatorAddr)
		if errD != nil || amount == nil || amount.Sign() == 0 {
			continue
		}
		if errU := stakeStateDB.Undelegate(validatorAddr, targetValAddr, amount); errU != nil {
			logger.Error("[deregisterValidator] error undelegating from %s: %v", targetValAddr.Hex(), errU)
			continue
		}
		if errT := chainState.TransferFrom(contractAcc, validatorAcc, amount); errT != nil {
			logger.Error("[deregisterValidator] error refunding from %s: %v", targetValAddr.Hex(), errT)
			continue
		}
		logger.Info("[deregisterValidator] withdrew %s wei from validator %s to wallet %s",
			amount.String(), targetValAddr.Hex(), validatorAddr.Hex())
	}

	// ── Bước 2: Tự động hoàn trả tiền cho external delegators (Bob, Carol...) ──
	// Duyệt qua danh sách người đã stake vào mình (vs.Delegators)
	externalDelegators := stakeStateDB.GetAllDelegatorsOfValidator(validatorAddr)
	for _, delegatorAddr := range externalDelegators {
		if delegatorAddr == validatorAddr {
			continue // self-stake xử lý riêng ở bước 3
		}
		amount, _, errD := stakeStateDB.GetDelegation(validatorAddr, delegatorAddr)
		if errD != nil || amount == nil || amount.Sign() == 0 {
			continue
		}
		// Undelegate and refund to delegator's wallet
		if errU := stakeStateDB.Undelegate(delegatorAddr, validatorAddr, amount); errU != nil {
			logger.Error("[deregisterValidator] error undelegating for delegator %s: %v", delegatorAddr.Hex(), errU)
			continue
		}
		delegatorAcc, _ := chainState.GetAccountStateDB().AccountState(delegatorAddr)
		if errT := chainState.TransferFrom(contractAcc, delegatorAcc, amount); errT != nil {
			logger.Error("[deregisterValidator] error refunding to delegator %s: %v", delegatorAddr.Hex(), errT)
			continue
		}
		logger.Info("[deregisterValidator] refunded %s wei to delegator %s", amount.String(), delegatorAddr.Hex())
	}

	// ── Bước 3: Hoàn trả self-stake về ví (bypass minSelfDelegation) ──
	selfDelegated, _, err := stakeStateDB.GetDelegation(validatorAddr, validatorAddr)
	if err != nil {
		return nil, fmt.Errorf("error getting self-delegation: %w", err)
	}
	if selfDelegated == nil {
		selfDelegated = big.NewInt(0)
	}

	if selfDelegated.Sign() > 0 {
		if errU := stakeStateDB.Undelegate(validatorAddr, validatorAddr, selfDelegated); errU != nil {
			return nil, fmt.Errorf("error undelegating self-stake: %w", errU)
		}
		if errT := chainState.TransferFrom(contractAcc, validatorAcc, selfDelegated); errT != nil {
			return nil, fmt.Errorf("error refunding self-stake to wallet: %w", errT)
		}
		logger.Info("[deregisterValidator] refunded %s wei self-stake to wallet %s",
			selfDelegated.String(), validatorAddr.Hex())
	}

	// ── Bước 4: Xóa validator khỏi danh sách ──
	err = stakeStateDB.DeleteValidator(validatorAddr)
	if err != nil {
		return nil, fmt.Errorf("error deleting validator: %v", err)
	}

	event, ok := h.abi.Events["ValidatorDeregistered"]
	if !ok {
		return nil, fmt.Errorf("event ValidatorDeregistered not found in ABI")
	}
	eventLog := smart_contract.NewEventLog(
		tx.Hash(),
		tx.ToAddress(),
		nil,
		[][]byte{event.ID.Bytes(), validatorAddr.Bytes()},
	)
	eventLogs = []types.EventLog{eventLog}

	// 📢 Notify Rust Metanode about committee change
	validatorCount, _ := stakeStateDB.GetValidatorCount()
	executor.NotifyValidatorDeregistered(0, validatorAddr.Hex(), uint64(validatorCount))
	logger.Info("[deregisterValidator] validator %s has left the network, %d validators remaining",
		validatorAddr.Hex(), validatorCount)

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
		return nil, fmt.Errorf("error checking validator: %w", err)
	}
	if validator == nil {
		return nil, fmt.Errorf("not a validator")
	}
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("error unpacking input: %w", err)
	}
	newRate, _ := args[0].(uint64)

	stakeStateDB.SetCommissionRate(validatorAddr, newRate)

	event, ok := h.abi.Events["CommissionRateUpdated"]
	if !ok {
		return nil, fmt.Errorf("event CommissionRateUpdated not found")
	}

	eventData, err := event.Inputs.NonIndexed().Pack(newRate)
	if err != nil {
		return nil, fmt.Errorf("error packing event: %w", err)
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
		return nil, fmt.Errorf("error checking validator: %w", err)
	}
	if validator == nil {
		return nil, fmt.Errorf("not a validator")
	}

	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, fmt.Errorf("error unpacking input: %w", err)
	}

	name, _ := args[0].(string)
	description, _ := args[1].(string)
	website, _ := args[2].(string)
	image, _ := args[3].(string)

	stakeStateDB.UpdateValidatorInfo(validatorAddr, name, description, website, image)

	event, ok := h.abi.Events["ValidatorInfoUpdated"]
	if !ok {
		return nil, fmt.Errorf("event ValidatorInfoUpdated not found")
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
		return nil, fmt.Errorf("error unpacking input: %w", err)
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
		return nil, fmt.Errorf("error checking validator: %w", err)
	}
	if validator == nil {
		return nil, fmt.Errorf("validator does not exist")
	}
	withdrawEvent, err := h._withdrawRewards(tx, chainState, validator, delegatorAddr, big.NewInt(0))
	if err != nil {
		return nil, fmt.Errorf("error automatically withdrawing rewards: %w", err)
	}
	err = chainState.TransferFrom(delegator, acc, tx.Amount())
	if err != nil {
		return nil, err
	}
	err = stakeStateDB.Delegate(_validatorAddr, delegatorAddr, tx.Amount())
	if err != nil {
		return nil, fmt.Errorf("error delegating: %w", err)
	}
	event, ok := h.abi.Events["Delegated"]
	if !ok {
		return nil, fmt.Errorf("event Delegated not found")
	}

	eventData, err := event.Inputs.NonIndexed().Pack(tx.Amount())
	if err != nil {
		return nil, fmt.Errorf("error packing Delegated event: %w", err)
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
		return nil, fmt.Errorf("error unpacking input: %w", err)
	}

	_validatorAddr, _ := args[0].(common.Address)
	amount, _ := args[1].(*big.Int)
	validator, err := stakeStateDB.GetValidator(_validatorAddr)
	if err != nil {
		return nil, fmt.Errorf("error getting validator: %w", err)
	}
	if validator == nil {
		return nil, fmt.Errorf("validator does not exist")
	}
	amountDelegated, _, err := stakeStateDB.GetDelegation(_validatorAddr, tx.FromAddress())
	if err != nil {
		return nil, fmt.Errorf("error fetching delegation: %w", err)
	}
	if amountDelegated.Cmp(amount) < 0 {
		return nil, fmt.Errorf("insufficient delegated amount %s < amount %s", amountDelegated.String(), amount.String())
	}
	amountDelegated.Sub(amountDelegated, amount)
	if tx.FromAddress() == _validatorAddr {
		// Self-stake sau khi rút phải >= minSelfDelegation
		// Tiền external delegators đã an toàn trong contract — không cần link ở đây
		if amountDelegated.Cmp(validator.MinSelfDelegation()) < 0 {
			return nil, fmt.Errorf(
				"self-delegation cannot be less than minimum self-delegation %s",
				validator.MinSelfDelegation().String(),
			)
		}
	}
	// RÚT THƯỞNG
	withdrawEvent, err := h._withdrawRewards(tx, chainState, validator, delegatorAddr, amount)
	if err != nil {
		return nil, fmt.Errorf("error automatically withdrawing rewards: %w", err)
	}
	err = stakeStateDB.Undelegate(delegatorAddr, _validatorAddr, amount)
	if err != nil {
		return nil, fmt.Errorf("error undelegating: %w", err)
	}
	// RÚT TIỀN CHO USER
	logger.Info("Amount to undelegate: %s", amount.String())
	acc, _ := chainState.GetAccountStateDB().AccountState(tx.ToAddress())
	delegator, _ := chainState.GetAccountStateDB().AccountState(delegatorAddr)
	err = chainState.TransferFrom(acc, delegator, amount)
	if err != nil {
		return nil, fmt.Errorf("error transferring funds: %w", err)
	}
	eventDef, ok := h.abi.Events["Undelegated"]
	if !ok {
		return nil, fmt.Errorf("event Undelegated not found")
	}

	eventData, err := eventDef.Inputs.NonIndexed().Pack(amount)
	if err != nil {
		return nil, fmt.Errorf("error packing event: %w", err)
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
		return nil, fmt.Errorf("event 'RewardWithdrawn' not found in ABI")
	}
	eventData, err := eventDef.Inputs.NonIndexed().Pack(rewardAmount)
	if err != nil {
		return nil, fmt.Errorf("error packing data for event RewardWithdrawn: %w", err)
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
