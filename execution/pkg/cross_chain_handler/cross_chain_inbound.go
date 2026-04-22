package cross_chain_handler

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/proxy_tx"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/vm_processor"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ═══════════════════════════════════════════════════════════════════════
// INBOUND EXECUTION — executeMintForInbound & executeConfirmation
// ═══════════════════════════════════════════════════════════════════════

func (h *CrossChainHandler) executeMintForInbound(
	ctx context.Context,
	chainState *blockchain.ChainState,
	tx types.Transaction,
	pkt *inboundPacketData,
	mvmId common.Address,
	enableTrace bool,
	blockTime uint64,
) ([]types.EventLog, error) {
	if pkt == nil {
		return nil, fmt.Errorf("executeMintForInbound: nil packet data")
	}

	vmP := vm_processor.NewVmProcessor(chainState, mvmId, enableTrace, blockTime)
	mvmE := mvm.GetOrCreateMVMApi(mvmId, chainState.GetSmartContractDB(), chainState.GetAccountStateDB(), true)

	var allLogs []types.EventLog
	val := pkt.Value
	if val == nil {
		val = big.NewInt(0)
	}

	// Xác định recipient (mặc định bằng Target, có cơ chế extract từ payload với lockAndBridge)
	recipient := pkt.Target
	if recipient == (common.Address{}) && len(pkt.Payload) >= 20 {
		if len(pkt.Payload) >= 32 {
			recipient = common.BytesToAddress(pkt.Payload[12:32])
		} else {
			recipient = common.BytesToAddress(pkt.Payload)
		}
	}

	// mintStatus=0 → thành công, mintStatus=1 → thất bại
	// Dùng để emit MessageReceived với đúng status kể cả khi lỗi,
	// thay vì return error ngay (làm mất event và làm hỏng cả batch).
	var mintStatus uint8 = 0
	var failReason []byte

	if pkt.Target == (common.Address{}) {
		// --- 1. ASSET TRANSFER (lockAndBridge path) ---
		if recipient == (common.Address{}) {
			// Không có cách nào xác định recipient → không thể emit event có nghĩa → return luôn
			return nil, fmt.Errorf("executeMintForInbound: cannot determine recipient")
		}
		if val.Sign() > 0 {
			fakeTx := proxy_tx.New(tx, tx.FromAddress(), recipient, val, uint64(mt_common.MAX_GASS_FEE), 0 /* free gas */, nil)
			exRs, err := vmP.ProcessNativeMintBurn(ctx, fakeTx, mvmE, 0) // 0 = MINT
			if err != nil || (exRs != nil && exRs.ReceiptStatus() != pb.RECEIPT_STATUS_RETURNED) {
				logger.Error("[BatchSubmit] ❌ INBOUND MINT failed: recipient=%s, value=%s, err=%v", recipient.Hex(), val.String(), err)
				mintStatus = 1
				failReason = []byte(fmt.Sprintf("asset mint failed: %v", err))
			} else {
				logger.Info("[BatchSubmit] ✅ INBOUND MINT done: recipient=%s, value=%s, src=%s→dest=%s",
					recipient.Hex(), val.String(), pkt.SourceNationId, pkt.DestNationId)
			}
		}
	} else {
		// --- 2. CROSS-CHAIN CONTRACT CALL (sendMessage path) ---
		// Dùng tx.FromAddress() (embassy wallet) làm msg.sender khi gọi contract đích.
		//
		// Flow atomic (tuần tự, không chen được):
		//   1. Mint ETH cho tx.FromAddress() (nếu có value)
		//   2. Gọi contract từ tx.FromAddress() → msg.sender = embassy wallet
		//   3. Nếu revert → burn ETH từ tx.FromAddress()
		// 2.a. Nếu có value → mint tiền cho tx.FromAddress()
		if val.Sign() > 0 {
			fakeMintTx := proxy_tx.New(tx, tx.FromAddress(), tx.FromAddress(), val, uint64(mt_common.MAX_GASS_FEE), 0 /* free gas */, nil)
			mintRs, err := vmP.ProcessNativeMintBurn(ctx, fakeMintTx, mvmE, 0) // 0 = MINT
			if err != nil || (mintRs != nil && mintRs.ReceiptStatus() != pb.RECEIPT_STATUS_RETURNED) {
				logger.Error("[BatchSubmit] ❌ INBOUND pre-mint failed: from=%s, err=%v", tx.FromAddress().Hex(), err)
				mintStatus = 1
				failReason = []byte(fmt.Sprintf("pre-mint failed: %v", err))
			} else {
				logger.Info("[BatchSubmit] ✅ INBOUND pre-mint done: from=%s, value=%s", tx.FromAddress().Hex(), val.String())
			}
		}
		// 2.b. Chỉ call contract nếu pre-mint thành công (hoặc không cần mint)
		if mintStatus == 0 {
			gasFree := uint64(mt_common.MAX_GASS_FEE)
			// Set cross-chain context trên MVMApi để precompile address(263)
			// trả về đúng pkt.Sender và pkt.SourceNationId khi contract gọi
			// getOriginalSender() hoặc getSourceChainId().
			mvmE.SetCrossChainContext(pkt.Sender, pkt.SourceNationId.Uint64())
			defer mvmE.ClearCrossChainContext()
			fakeCallTx := proxy_tx.New(tx, tx.FromAddress(), pkt.Target, val, gasFree, 0 /* free gas */, pkt.Payload)
			exRs, err := vmP.ExecuteTransactionWithMvmId(ctx, fakeCallTx, true, true)

			// 2.c. Nếu revert mà có tiền, phải đốt lại lượng asset đã mint
			if err != nil || (exRs != nil && exRs.ReceiptStatus() != pb.RECEIPT_STATUS_RETURNED) {
				logger.Warn("[BatchSubmit] ⚠️ Contract execution reverted: target=%s, res %v", pkt.Target.Hex(), exRs)
				mintStatus = 1
				failReason = []byte(fmt.Sprintf("contract call reverted: %v + %s", err, string(exRs.Return())))
				if val.Sign() > 0 {
					fakeBurnTx := proxy_tx.New(tx, tx.FromAddress(), tx.FromAddress(), val, gasFree, 0 /* free gas */, nil)
					burnRs, errBurn := vmP.ProcessNativeMintBurn(ctx, fakeBurnTx, mvmE, 1) // 1 = BURN
					if errBurn != nil || (burnRs != nil && burnRs.ReceiptStatus() != pb.RECEIPT_STATUS_RETURNED) {
						logger.Error("[BatchSubmit] ❌ Failed to burn reverted minted money %v", errBurn)
					} else {
						logger.Info("[BatchSubmit] 🔥 BURNED reverted minted money from: %s, amount: %s", tx.FromAddress().Hex(), val.String())
					}
				}
			} else {
				logger.Info("[BatchSubmit] ✅ Contract execution success: target=%s", pkt.Target.Hex())
				if exRs != nil && len(exRs.EventLogs()) > 0 {
					allLogs = append(allLogs, exRs.EventLogs()...)
				}
			}
		}
	}

	// 3. Emit MessageReceived event chung cho cả 2 trường hợp (success và failed)
	// Luôn emit để client/observer có thể lắng nghe và xử lý cả khi thất bại.
	eventDef, ok := h.abi.Events["MessageReceived"]
	if !ok {
		logger.Warn("[BatchSubmit] MessageReceived event not found in ABI, skip event log")
		return allLogs, nil
	}

	eventData, err := eventDef.Inputs.NonIndexed().Pack(
		uint8(0),   // msgType = ASSET_TRANSFER
		mintStatus, // status: 0=SUCCESS, 1=FAILED
		failReason, // returnData: lý do thất bại (rỗng nếu thành công)
		pkt.Sender, // sender gốc từ chain nguồn
		val,        // amount: giá trị đã bridge
	)
	if err == nil {
		eventLog := smart_contract.NewEventLog(
			tx.Hash(),
			tx.ToAddress(),
			eventData,
			[][]byte{
				eventDef.ID.Bytes(),
				common.BigToHash(pkt.SourceNationId).Bytes(), // indexed: sourceNationId
				common.BigToHash(h.cachedChainId).Bytes(),    // indexed: destNationId
				pkt.MsgId[:], // indexed: msgId = txHash gốc user gửi trên chain nguồn
			},
		)
		allLogs = append(allLogs, eventLog)
		statusStr := "SUCCESS"
		if mintStatus != 0 {
			statusStr = "FAILED"
		}
		logger.Info("[MSGID-TRACE] ➡️  [3/4] INBOUND chain=%s EMITTING MessageReceived: msgId=%s (=%x) status=%s src=%s sender=%s val=%s",
			h.cachedChainId.String(),
			"0x"+fmt.Sprintf("%x", pkt.MsgId[:]),
			pkt.MsgId[:4], // short preview
			statusStr,
			pkt.SourceNationId.String(),
			pkt.Sender.Hex(),
			val.String(),
		)
	} else {
		logger.Error("[BatchSubmit] ❌ Failed to pack MessageReceived event: %v", err)
	}

	return allLogs, nil
}

// executeConfirmation xử lý CONFIRMATION event khi đủ quorum.
// Ghi nhận kết quả (success/fail) cho outbound message.
func (h *CrossChainHandler) executeConfirmation(
	ctx context.Context,
	chainState *blockchain.ChainState,
	tx types.Transaction,
	conf *confirmationData,
	mvmId common.Address,
	enableTrace bool,
	blockTime uint64,
) ([]types.EventLog, error) {
	if conf == nil {
		return nil, fmt.Errorf("executeConfirmation: nil confirmation data")
	}

	logger.Info("[BatchSubmit] ✅ CONFIRMATION: messageId=%x, isSuccess=%v, sourceBlock=%s",
		conf.MessageId, conf.IsSuccess, conf.SourceBlockNumber)

	// Emit OutboundResult event để client theo dõi kết quả
	eventDef, ok := h.abi.Events["OutboundResult"]
	if !ok {
		logger.Warn("[BatchSubmit] OutboundResult event not found in ABI, skip event log")
		return nil, nil
	}

	// Lấy msgType và refundAmount từ confirmation data
	var msgType uint8 = 0
	refundAmount := conf.Value // amount đã được truyền từ observer qua ConfirmationParam.Value
	if refundAmount == nil {
		refundAmount = new(big.Int)
	}

	// refundFailed theo dõi riêng trạng thái hoàn tiền để đưa vào reason của event.
	// Dù refund có fail hay không, vẫn luôn emit OutboundResult để client biết.
	var refundFailed bool
	if !conf.IsSuccess {
		// Giao dịch thất bại bên kia → MINT lại (hoàn tiền) cho người gửi (Sender) bên này
		if refundAmount.Sign() > 0 {
			vmP := vm_processor.NewVmProcessor(chainState, mvmId, enableTrace, blockTime)
			mvmE := mvm.GetOrCreateMVMApi(mvmId, chainState.GetSmartContractDB(), chainState.GetAccountStateDB(), true)

			fakeRefundTx := proxy_tx.New(tx, tx.FromAddress(), conf.Sender, refundAmount, uint64(mt_common.MAX_GASS_FEE), 0 /* free gas */, nil)
			mintRs, errMint := vmP.ProcessNativeMintBurn(ctx, fakeRefundTx, mvmE, 0) // 0 = MINT
			if errMint != nil || (mintRs != nil && mintRs.ReceiptStatus() != pb.RECEIPT_STATUS_RETURNED) {
				logger.Error("[BatchSubmit] ❌ Failed to refund (mint) back to sender %s: %v", conf.Sender.Hex(), errMint)
				refundFailed = true
			} else {
				logger.Info("[BatchSubmit] 💵 REFUND (MINT) to sender=%s amount=%s", conf.Sender.Hex(), refundAmount.String())
			}
		} else {
			logger.Warn("[BatchSubmit] ⚠️ CONFIRMATION Failed but refundAmount=0, skip MINT refund")
		}
	}

	// Xây dựng reason phản ánh đúng trạng thái:
	// - remote OK → reason rỗng
	// - remote fail + refund OK → "confirmation failed on destination chain"
	// - remote fail + refund fail → "confirmation failed on destination chain; refund mint also failed"
	var reason []byte
	if !conf.IsSuccess {
		if refundFailed {
			reason = []byte("confirmation failed on destination chain; refund mint also failed")
		} else {
			reason = []byte("confirmation failed on destination chain")
		}
	}
	eventData, err := eventDef.Inputs.NonIndexed().Pack(
		msgType,
		conf.IsSuccess,
		refundAmount, // amount
		reason,       // reason: rỗng nếu thành công
	)
	if err != nil {
		logger.Warn("[BatchSubmit] pack OutboundResult event failed: %v", err)
		return nil, nil
	}

	// msgId = conf.MessageId = txHash gốc mà user đã gửi trên chain nguồn
	// → Client filter OutboundResult theo msgId để biết kết quả của TX cụ thể nào
	eventLog := smart_contract.NewEventLog(
		tx.Hash(),
		tx.ToAddress(),
		eventData,
		[][]byte{
			eventDef.ID.Bytes(),
			conf.MessageId[:],        // indexed[1]: msgId = txHash gốc của user
			conf.Sender.Bytes(),      // indexed[2]: sender (người gửi trên chain nguồn)
		},
	)
	logger.Info("[MSGID-TRACE] ⬇️  [4/4] CONFIRMATION chain=%s EMITTING OutboundResult: msgId=%s (=%x) isSuccess=%v sender=%s amount=%s",
		h.cachedChainId.String(),
		"0x"+fmt.Sprintf("%x", conf.MessageId[:]),
		conf.MessageId[:4], // short preview
		conf.IsSuccess,
		conf.Sender.Hex(),
		refundAmount.String(),
	)

	return []types.EventLog{eventLog}, nil
}
