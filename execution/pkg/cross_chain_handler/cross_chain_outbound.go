package cross_chain_handler

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/vm_processor"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ═══════════════════════════════════════════════════════════════════════
// OUTBOUND — lockAndBridge & sendMessage
// ═══════════════════════════════════════════════════════════════════════

// handleLockAndBridge xử lý bridge native coin sang chain đích
func (h *CrossChainHandler) handleLockAndBridge(
	ctx context.Context,
	chainState *blockchain.ChainState,
	tx types.Transaction,
	method *abi.Method,
	inputData []byte,
	mvmId common.Address,
	blockTime uint64,
) ([]types.EventLog, types.ExecuteSCResult, error) {
	if tx.Amount().Sign() <= 0 {
		return nil, nil, fmt.Errorf("lockAndBridge: amount must be greater than zero")
	}

	// Unpack: lockAndBridge(address recipient, uint256 destinationId)
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, nil, fmt.Errorf("lockAndBridge: unpack error: %v", err)
	}
	if len(args) != 2 {
		return nil, nil, fmt.Errorf("lockAndBridge: expected 2 args, got %d", len(args))
	}

	recipient, ok := args[0].(common.Address)
	if !ok {
		return nil, nil, fmt.Errorf("lockAndBridge: invalid recipient address type")
	}
	if recipient == (common.Address{}) {
		return nil, nil, fmt.Errorf("lockAndBridge: recipient cannot be zero address")
	}

	destinationId, ok := args[1].(*big.Int)
	if !ok {
		return nil, nil, fmt.Errorf("lockAndBridge: invalid destinationId type")
	}
	if destinationId.Sign() <= 0 {
		return nil, nil, fmt.Errorf("lockAndBridge: destinationId must be greater than zero")
	}

	// Kiểm tra destinationId có trong registeredChainIds
	// Nếu không có trong cache → tự động thử get lại từ chain 1 lần
	if !h.isDestinationRegistered(destinationId) {
		return nil, nil, fmt.Errorf("lockAndBridge: destinationId %s is not registered", destinationId.String())
	}

	sender := tx.FromAddress()
	amount := tx.Amount()

	// Kiểm tra balance
	senderAccount, err := chainState.GetAccountStateDB().AccountState(sender)
	if err != nil {
		return nil, nil, fmt.Errorf("lockAndBridge: get sender account error: %v", err)
	}
	if senderAccount.TotalBalance().Cmp(amount) < 0 {
		return nil, nil, fmt.Errorf("lockAndBridge: insufficient balance")
	}

	// BURN: processNativeMintBurn(operationType=1)
	var exRs types.ExecuteSCResult
	if ctx != nil {
		vmP := vm_processor.NewVmProcessor(chainState, mvmId, false, blockTime)

		exRs, err = vmP.ProcessNativeMintBurn(ctx, tx, 1)
		if err != nil {
			return nil, exRs, fmt.Errorf("lockAndBridge: burn failed: %v", err)
		}
		if exRs != nil && exRs.ReceiptStatus() != pb.RECEIPT_STATUS_RETURNED {
			return nil, exRs, fmt.Errorf("lockAndBridge: burn reverted")
		}
	}

	logger.Info("CrossChain lockAndBridge BURN success: sender=%s, amount=%s",
		sender.Hex(), amount.String())

	// EMIT EVENT: MessageSent
	eventDef, ok2 := h.abi.Events["MessageSent"]
	if !ok2 {
		return nil, exRs, fmt.Errorf("lockAndBridge: MessageSent event not found in ABI")
	}

	sourceId := h.cachedChainId

	payload, err := abi.Arguments{{Type: mustType("address")}}.Pack(recipient)
	if err != nil {
		return nil, exRs, fmt.Errorf("lockAndBridge: pack payload error: %v", err)
	}
	target := common.Address{}

	eventData, err := eventDef.Inputs.NonIndexed().Pack(
		false, // isEVM = false vì đang chạy bằng Go Interceptor
		sender,
		target,
		amount,
		payload,
		big.NewInt(int64(blockTime)),
	)
	if err != nil {
		return nil, exRs, fmt.Errorf("lockAndBridge: pack event data error: %v", err)
	}

	eventLog := smart_contract.NewEventLog(
		tx.Hash(),
		tx.ToAddress(),
		eventData,
		[][]byte{
			eventDef.ID.Bytes(),
			common.BigToHash(sourceId).Bytes(),
			common.BigToHash(destinationId).Bytes(),
			tx.Hash().Bytes(), // msgId = txHash gốc của user (indexed topic[3])
		},
	)

	logger.Info("[MSGID-TRACE] ⬆️  [1/4] OUTBOUND lockAndBridge: txHash=%s → EMITTING MessageSent\n"+
		"        sender=%s recipient=%s amount=%s srcId=%s destId=%s",
		tx.Hash().Hex(),
		sender.Hex(), recipient.Hex(), amount.String(),
		sourceId.String(), destinationId.String(),
	)

	return []types.EventLog{eventLog}, exRs, nil
}

// handleSendMessage xử lý cross-chain contract call
func (h *CrossChainHandler) handleSendMessage(
	ctx context.Context,
	chainState *blockchain.ChainState,
	tx types.Transaction,
	method *abi.Method,
	inputData []byte,
	mvmId common.Address,
	blockTime uint64,
) ([]types.EventLog, types.ExecuteSCResult, error) {
	// Unpack: sendMessage(address target, bytes payload, uint256 destinationId)
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, nil, fmt.Errorf("sendMessage: unpack error: %v", err)
	}
	if len(args) != 3 {
		return nil, nil, fmt.Errorf("sendMessage: expected 3 args, got %d", len(args))
	}

	target, ok := args[0].(common.Address)
	if !ok {
		return nil, nil, fmt.Errorf("sendMessage: invalid target address type")
	}
	if target == (common.Address{}) {
		return nil, nil, fmt.Errorf("sendMessage: target cannot be zero address (use lockAndBridge for asset transfer)")
	}

	payload, ok := args[1].([]byte)
	if !ok {
		return nil, nil, fmt.Errorf("sendMessage: invalid payload type")
	}

	destinationId, ok := args[2].(*big.Int)
	if !ok {
		return nil, nil, fmt.Errorf("sendMessage: invalid destinationId type")
	}
	if destinationId.Sign() <= 0 {
		return nil, nil, fmt.Errorf("sendMessage: destinationId must be greater than zero")
	}

	// Kiểm tra destinationId có trong registeredChainIds
	// Nếu không có trong cache → tự động thử get lại từ chain 1 lần
	if !h.isDestinationRegistered(destinationId) {
		return nil, nil, fmt.Errorf("sendMessage: destinationId %s is not registered", destinationId.String())
	}

	sender := tx.FromAddress()
	amount := tx.Amount()

	if amount.Sign() > 0 {
		senderAccount, err := chainState.GetAccountStateDB().AccountState(sender)
		if err != nil {
			return nil, nil, fmt.Errorf("sendMessage: get sender account error: %v", err)
		}
		if senderAccount.TotalBalance().Cmp(amount) < 0 {
			return nil, nil, fmt.Errorf("sendMessage: insufficient balance for msg.value")
		}
	}

	// BURN: Nếu có msg.value > 0 → burn coin từ sender
	var exRs types.ExecuteSCResult
	if amount.Sign() > 0 && ctx != nil {
		vmP := vm_processor.NewVmProcessor(chainState, mvmId, false, blockTime)

		exRs, err = vmP.ProcessNativeMintBurn(ctx, tx, 1)
		if err != nil {
			return nil, exRs, fmt.Errorf("sendMessage: burn msg.value failed: %v", err)
		}
		if exRs != nil && exRs.ReceiptStatus() != pb.RECEIPT_STATUS_RETURNED {
			return nil, exRs, fmt.Errorf("sendMessage: burn msg.value reverted")
		}
		logger.Info("CrossChain sendMessage BURN success: sender=%s, amount=%s",
			sender.Hex(), amount.String())
	}

	// EMIT EVENT
	eventDef, ok2 := h.abi.Events["MessageSent"]
	if !ok2 {
		return nil, exRs, fmt.Errorf("sendMessage: MessageSent event not found in ABI")
	}

	sourceId := h.cachedChainId

	eventData, err := eventDef.Inputs.NonIndexed().Pack(
		false, // isEVM = false
		sender,
		target,
		amount,
		payload,
		big.NewInt(int64(blockTime)),
	)
	if err != nil {
		return nil, exRs, fmt.Errorf("sendMessage: pack event data error: %v", err)
	}

	eventLog := smart_contract.NewEventLog(
		tx.Hash(),
		tx.ToAddress(),
		eventData,
		[][]byte{
			eventDef.ID.Bytes(),
			common.BigToHash(sourceId).Bytes(),
			common.BigToHash(destinationId).Bytes(),
			tx.Hash().Bytes(), // msgId = txHash gốc của user (indexed topic[3])
		},
	)

	logger.Info("[MSGID-TRACE] ⬆️  [1/4] OUTBOUND sendMessage: txHash=%s → EMITTING MessageSent\n"+
		"        sender=%s target=%s amount=%s srcId=%s destId=%s payloadLen=%d",
		tx.Hash().Hex(),
		sender.Hex(), target.Hex(), amount.String(),
		sourceId.String(), destinationId.String(), len(payload),
	)

	return []types.EventLog{eventLog}, exRs, nil
}
