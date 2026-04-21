package cross_chain_handler

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"
	"reflect"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	abihelper "github.com/meta-node-blockchain/meta-node/pkg/utils/abi_helper"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ─────────────────────────────────────────────────────────────────────────────
// BATCH SUBMIT — Embassy relay handler
// ─────────────────────────────────────────────────────────────────────────────

// eventVoteKey là khoá dùng để nhận diện duy nhất một event cross-chain.
// Key = sha256 của (eventKind || canonicalData) để tránh va chạm giữa INBOUND và CONFIRMATION.
// Với INBOUND:      canonicalData = sourceNationId(8) + destNationId(8) + blockNumber(8) + sender(20) + target(20) + value(32) + payload
// Với CONFIRMATION: canonicalData = messageId(32) + sourceBlockNumber(8) + isSuccess(1)
type eventVoteKey [32]byte

// eventVoteState theo dõi ai đã vote và data của event.
type eventVoteState struct {
	mu       sync.Mutex
	voters   map[common.Address]struct{} // embassy ETH address đã gửi vote này
	executed bool                        // đã execute chưa
	// INBOUND data
	packetData *inboundPacketData
	// CONFIRMATION data
	confirmData *confirmationData
	eventKind   uint8
}

type inboundPacketData struct {
	SourceNationId *big.Int
	DestNationId   *big.Int
	Timestamp      *big.Int
	Sender         common.Address
	Target         common.Address
	Value          *big.Int
	Payload        []byte
	BlockNumber    *big.Int
	MessageId      [32]byte // txHash gốc của user trên chain nguồn (từ MessageSent Topics[3])
}

type confirmationData struct {
	MessageId         [32]byte
	SourceBlockNumber *big.Int
	IsSuccess         bool
	ReturnData        []byte
	Sender            common.Address
	Value             *big.Int
}

// computeInboundKey tính khoá vote cho INBOUND event.
func computeInboundKey(p *inboundPacketData) eventVoteKey {
	var buf []byte
	buf = append(buf, 0x00) // eventKind = INBOUND

	bn8 := make([]byte, 8)
	if p.SourceNationId != nil {
		binary.BigEndian.PutUint64(bn8, p.SourceNationId.Uint64())
	}
	buf = append(buf, bn8...)

	if p.DestNationId != nil {
		binary.BigEndian.PutUint64(bn8, p.DestNationId.Uint64())
	}
	buf = append(buf, bn8...)

	if p.BlockNumber != nil {
		binary.BigEndian.PutUint64(bn8, p.BlockNumber.Uint64())
	}
	buf = append(buf, bn8...)

	buf = append(buf, p.Sender.Bytes()...)
	buf = append(buf, p.Target.Bytes()...)

	valBytes := make([]byte, 32)
	if p.Value != nil {
		vBytes := p.Value.Bytes()
		copy(valBytes[32-len(vBytes):], vBytes)
	}
	buf = append(buf, valBytes...)
	buf = append(buf, p.Payload...)
	return sha256.Sum256(buf)
}

// computeConfirmKey tính khoá vote cho CONFIRMATION event.
func computeConfirmKey(c *confirmationData) eventVoteKey {
	var buf []byte
	buf = append(buf, 0x01) // eventKind = CONFIRMATION
	buf = append(buf, c.MessageId[:]...)
	bn8 := make([]byte, 8)
	if c.SourceBlockNumber != nil {
		binary.BigEndian.PutUint64(bn8, c.SourceBlockNumber.Uint64())
	}
	buf = append(buf, bn8...)
	if c.IsSuccess {
		buf = append(buf, 0x01)
	} else {
		buf = append(buf, 0x00)
	}
	return sha256.Sum256(buf)
}

// isActiveEmbassy kiểm tra ETH address có phải embassy đang active trong cache không.
func (h *CrossChainHandler) isActiveEmbassyCached(addr common.Address) bool {
	return h.cachedEmbassies[addr]
}

// ─────────────────────────────────────────────────────────────────────────────
// POOL GATE: Verify batchSubmit sender trước khi nhận vào pool
// ─────────────────────────────────────────────────────────────────────────────

// batchSubmitSelector là 4-byte function selector của batchSubmit(EmbassyEvent[])
// keccak256("batchSubmit((uint8,uint256,(uint256,uint256,uint256,address,address,uint256,bytes),(bytes32,uint256,bool,bytes,address))[])")[:4]
// Giá trị được tính từ ABI khi init, cache lại để tránh tính lại mỗi lần.
var batchSubmitSelector [4]byte
var batchSubmitSelectorOnce sync.Once

// getBatchSubmitSelector trả về 4-byte selector của batchSubmit, tính lười từ ABI.
func (h *CrossChainHandler) getBatchSubmitSelector() [4]byte {
	batchSubmitSelectorOnce.Do(func() {
		method, ok := h.abi.Methods["batchSubmit"]
		if ok {
			copy(batchSubmitSelector[:], method.ID)
		}
	})
	return batchSubmitSelector
}

// IsBatchSubmitTx kiểm tra TX có phải là batchSubmit không (dựa vào 4-byte selector).
// Dùng để quyết định có cần verify embassy sender không.
func (h *CrossChainHandler) IsBatchSubmitTx(inputData []byte) bool {
	if len(inputData) < 4 {
		return false
	}
	sel := h.getBatchSubmitSelector()
	return sel != ([4]byte{}) &&
		inputData[0] == sel[0] && inputData[1] == sel[1] &&
		inputData[2] == sel[2] && inputData[3] == sel[3]
}

// dùng trong virtual
type CrossChainCallTarget struct {
	EventKind      uint8
	Target         common.Address
	Sender         common.Address
	SourceNationId *big.Int
	Payload        []byte
	Recipient      common.Address // rỗng nếu Target != address{} (sendMessage path)
	IsSuccess      bool
	Amount         *big.Int
}

//   - sendMessage path (Target != address{}): lấy Target + Payload để virtual processor
//     chạy fake EVM dry-run thu thập relatedAddresses chính xác.
//   - lockAndBridge path (Target == address{}): parse payload (abi.encode(recipient)) để
//     lấy Recipient và add vào relatedAddresses, đảm bảo chain xử lý tuần tự.
func (h *CrossChainHandler) ExtractCrossChainTargets(inputData []byte) []CrossChainCallTarget {
	if len(inputData) <= 4 {
		return nil
	}
	method, ok := h.abi.Methods["batchSubmit"]
	if !ok {
		return nil
	}
	args, err := method.Inputs.Unpack(inputData[4:])
	if err != nil || len(args) < 1 {
		return nil
	}
	eventsVal, eventsLen, err := abihelper.ReflectSlice(args[0])
	if err != nil {
		return nil
	}

	var targets []CrossChainCallTarget
	for i := 0; i < eventsLen; i++ {
		ev := abihelper.ReflectIndex(eventsVal, i)
		eventKind, err := abihelper.ReflectUint8(ev, "EventKind")
		if err != nil {
			continue
		}

		if eventKind == 0 { // INBOUND
			pkt, err := reflectInboundPacket(ev)
			if err != nil {
				continue
			}

			if pkt.Target != (common.Address{}) {
				// ── sendMessage path: dùng EVM dry-run với đúng payload ────────────
				targets = append(targets, CrossChainCallTarget{
					EventKind:      eventKind,
					Target:         pkt.Target,
					Sender:         pkt.Sender,
					SourceNationId: pkt.SourceNationId,
					Payload:        pkt.Payload,
					Amount:         pkt.Value,
				})
			} else {
				// ── lockAndBridge path: target rỗng, parse payload lấy recipient ──
				// Đảm bảo logic parse hoàn toàn khớp với executeMintForInbound:
				var recipient common.Address
				if len(pkt.Payload) >= 20 {
					if len(pkt.Payload) >= 32 {
						recipient = common.BytesToAddress(pkt.Payload[12:32])
					} else {
						recipient = common.BytesToAddress(pkt.Payload)
					}
				}

				// Luôn add vào targets để virtual processor thêm Sender + Recipient vào relatedAddresses
				targets = append(targets, CrossChainCallTarget{
					EventKind:      eventKind,
					Target:         common.Address{}, // zero = lockAndBridge, không chạy EVM dry-run
					Sender:         pkt.Sender,
					SourceNationId: pkt.SourceNationId,
					Payload:        pkt.Payload,
					Recipient:      recipient,
					Amount:         pkt.Value,
				})
			}
		} else if eventKind == 1 { // CONFIRMATION
			conf, err := reflectConfirmation(ev)
			if err != nil {
				continue
			}
			// Bổ sung target với eventKind == 1 để virtual processor xử lý
			targets = append(targets, CrossChainCallTarget{
				EventKind: eventKind,
				Target:    common.Address{}, // không chạy EVM dry-run
				Sender:    conf.Sender,
				Payload:   nil,
				Recipient: conf.Sender,
				IsSuccess: conf.IsSuccess,
				Amount:    conf.Value,
			})
		}
	}
	return targets
}

func (h *CrossChainHandler) VerifyBatchSubmitSender(tx types.Transaction) error {
	if !h.configLoaded.Load() {
		logger.Warn("[CrossChain Gate] Config not loaded yet, skip embassy verify for %s", tx.FromAddress().Hex())
		return nil
	}
	sender := tx.FromAddress()
	if !h.isActiveEmbassyCached(sender) {
		return fmt.Errorf("cross-chain: sender %s is not a registered active embassy (batchSubmit rejected at pool gate)", sender.Hex())
	}

	logger.Info("[CrossChain Gate] ✅ Embassy verified: %s", sender.Hex())
	return nil
}

// quorum trả về số vote tối thiểu để thực thi (majority: > 50%).
// Với 1 embassy: cần 1; với 2: cần 2; với 3: cần 2; với 4: cần 3...
func quorum(total int) int {
	if total <= 0 {
		return 1
	}
	return (total/2 + 1)
}

// handleBatchSubmit xử lý TX batchSubmit từ embassy relay.
// 1. Verify sender là embassy active.
// 2. Unpack events từ calldata.
// 3. Tích lũy vote cho từng event (theo eventVoteKey).
// 4. Nếu đủ quorum → thực thi mint/unlock.
func (h *CrossChainHandler) handleBatchSubmit(
	ctx context.Context,
	chainState *blockchain.ChainState,
	tx types.Transaction,
	method *abi.Method,
	inputData []byte,
	mvmId common.Address,
	blockTime uint64,
) ([]types.EventLog, types.ExecuteSCResult, error) {

	// 2. Unpack events từ calldata
	// Method: batchSubmit(EmbassyEvent[] events, bytes embassyPubKey)
	// ABI unpack trả về 2 args: args[0] = events, args[1] = embassyPubKey (đã verify ở virtual processor)
	args, err := method.Inputs.Unpack(inputData)
	if err != nil {
		return nil, nil, fmt.Errorf("batchSubmit: unpack error: %v", err)
	}
	if len(args) < 1 {
		return nil, nil, fmt.Errorf("batchSubmit: expected at least 1 arg (events[]), got %d", len(args))
	}
	eventsVal, eventsLen, err := abihelper.ReflectSlice(args[0])
	if err != nil {
		return nil, nil, fmt.Errorf("batchSubmit: %v", err)
	}
	// Execute từng event trực tiếp — quorum đã được đảm bảo ở observer sub trước khi gửi TX
	var allEventLogs []types.EventLog
	var inboundCount, confirmationCount int // Thống kê số lượng event đã xử lý

	logger.Info("🔍 [FORK-DEBUG] handleBatchSubmit TX=%s from=%s nonce=%d events=%d",
		tx.Hash().Hex()[:16], tx.FromAddress().Hex()[:10], tx.GetNonce(), eventsLen)

	for i := 0; i < eventsLen; i++ {
		ev := abihelper.ReflectIndex(eventsVal, i)
		eventKind, err := abihelper.ReflectUint8(ev, "EventKind")
		if err != nil {
			logger.Error("[BatchSubmit] event[%d]: read EventKind failed: %v", i, err)
			continue
		}

		if eventKind == 0 { // INBOUND
			pkt, err := reflectInboundPacket(ev)
			if err != nil {
				logger.Error("[BatchSubmit] event[%d]: read packet failed: %v", i, err)
				continue
			}
			// 🔍 [FORK-DEBUG] Log chi tiết INBOUND event để so sánh node
			logger.Info("🔍 [FORK-DEBUG][%d] INBOUND: src=%s→dest=%s sender=%s value=%s blk=%s messageId=%x",
				i, pkt.SourceNationId, pkt.DestNationId,
				pkt.Sender.Hex()[:10], pkt.Value, pkt.BlockNumber, pkt.MessageId[:8])
			// Luôn collect logs kể cả khi exErr != nil:
			// executeMintForInbound emit MessageReceived(status=FAILED) ngị cả khi mint lỗi,
			// event đó vẫn cần được propagate để observer biết item nào thất bại.
			logs, exErr := h.executeMintForInbound(ctx, chainState, tx, pkt, blockTime)
			if exErr != nil {
				logger.Error("[BatchSubmit] executeMintForInbound hard-failed event[%d] (no event emitted): %v", i, exErr)
			} else {
				inboundCount++
			}
			allEventLogs = append(allEventLogs, logs...)
		} else if eventKind == 1 { // CONFIRMATION
			conf, err := reflectConfirmation(ev)
			if err != nil {
				logger.Error("[BatchSubmit] event[%d]: read confirmation failed: %v", i, err)
				continue
			}
			logger.Info("🔍 [FORK-DEBUG][%d] CONFIRMATION: msgId=%x isSuccess=%v srcBlk=%s sender=%s val=%s",
				i, conf.MessageId[:8], conf.IsSuccess, conf.SourceBlockNumber,
				conf.Sender.Hex()[:10], conf.Value)
			logs, exErr := h.executeConfirmation(ctx, chainState, tx, conf, blockTime)
			if exErr != nil {
				logger.Error("[BatchSubmit] executeConfirmation failed event[%d]: %v", i, exErr)
			} else {
				confirmationCount++
			}
			allEventLogs = append(allEventLogs, logs...)
		} else {
			logger.Warn("[BatchSubmit] Unknown eventKind=%d at index=%d, skipping", eventKind, i)
		}
	}

	logger.Info("📊 [BatchSubmit] Tx %s: Đã thống kê xử lý %d giao dịch (Đầu vào/INBOUND: %d | Đầu ra/CONFIRMATION: %d)", tx.Hash().Hex(), eventsLen, inboundCount, confirmationCount)
	return allEventLogs, nil, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Domain-specific unpack helpers — dùng pkg/utils/abi_reflect
// ─────────────────────────────────────────────────────────────────────────────

// reflectInboundPacket đọc Packet + BlockNumber từ EmbassyEvent reflect.Value.
func reflectInboundPacket(ev reflect.Value) (*inboundPacketData, error) {
	blockNumberField, err := abihelper.ReflectField(ev, "BlockNumber")
	if err != nil {
		return nil, err
	}
	var blockNumber *big.Int
	if bn, ok := blockNumberField.Interface().(*big.Int); ok && bn != nil {
		blockNumber = bn
	} else {
		blockNumber = new(big.Int)
	}

	pktField, err := abihelper.ReflectField(ev, "Packet")
	if err != nil {
		return nil, err
	}

	messageId, err := abihelper.ReflectBytes32(pktField, "MessageId")
	if err != nil {
		return nil, fmt.Errorf("packet.MessageId: %v", err)
	}
	sourceNationId, err := abihelper.ReflectBigInt(pktField, "SourceNationId")
	if err != nil {
		return nil, fmt.Errorf("packet.SourceNationId: %v", err)
	}
	destNationId, err := abihelper.ReflectBigInt(pktField, "DestNationId")
	if err != nil {
		return nil, fmt.Errorf("packet.DestNationId: %v", err)
	}
	timestamp, err := abihelper.ReflectBigInt(pktField, "Timestamp")
	if err != nil {
		return nil, fmt.Errorf("packet.Timestamp: %v", err)
	}
	sender, err := abihelper.ReflectAddress(pktField, "Sender")
	if err != nil {
		return nil, fmt.Errorf("packet.Sender: %v", err)
	}
	target, err := abihelper.ReflectAddress(pktField, "Target")
	if err != nil {
		return nil, fmt.Errorf("packet.Target: %v", err)
	}
	value, err := abihelper.ReflectBigInt(pktField, "Value")
	if err != nil {
		return nil, fmt.Errorf("packet.Value: %v", err)
	}
	payload, err := abihelper.ReflectBytes(pktField, "Payload")
	if err != nil {
		return nil, fmt.Errorf("packet.Payload: %v", err)
	}

	return &inboundPacketData{
		SourceNationId: sourceNationId,
		DestNationId:   destNationId,
		Timestamp:      timestamp,
		Sender:         sender,
		Target:         target,
		Value:          value,
		Payload:        payload,
		BlockNumber:    blockNumber,
		MessageId:      messageId,
	}, nil
}

// extractMsgIdFromInboundEvent đọc MsgId từ Confirmation.MessageId (được scanner carry theo).
// Trả về zero [32]byte nếu không tồn tại.
func extractMsgIdFromInboundEvent(ev reflect.Value) [32]byte {
	confField, err := abihelper.ReflectField(ev, "Confirmation")
	if err != nil {
		return [32]byte{}
	}
	msgId, err := abihelper.ReflectBytes32(confField, "MessageId")
	if err != nil {
		return [32]byte{}
	}
	return msgId
}

// reflectConfirmation đọc Confirmation từ EmbassyEvent reflect.Value.
func reflectConfirmation(ev reflect.Value) (*confirmationData, error) {
	confField, err := abihelper.ReflectField(ev, "Confirmation")
	if err != nil {
		return nil, err
	}

	messageId, err := abihelper.ReflectBytes32(confField, "MessageId")
	if err != nil {
		return nil, fmt.Errorf("confirmation.MessageId: %v", err)
	}
	sourceBlockNumber, err := abihelper.ReflectBigInt(confField, "SourceBlockNumber")
	if err != nil {
		return nil, fmt.Errorf("confirmation.SourceBlockNumber: %v", err)
	}
	isSuccess, err := abihelper.ReflectBool(confField, "IsSuccess")
	if err != nil {
		return nil, fmt.Errorf("confirmation.IsSuccess: %v", err)
	}
	returnData, err := abihelper.ReflectBytes(confField, "ReturnData")
	if err != nil {
		return nil, fmt.Errorf("confirmation.ReturnData: %v", err)
	}
	sender, err := abihelper.ReflectAddress(confField, "Sender")
	if err != nil {
		return nil, fmt.Errorf("confirmation.Sender: %v", err)
	}
	value, err := abihelper.ReflectBigInt(confField, "Value")
	if err != nil {
		return nil, fmt.Errorf("confirmation.Value: %v", err)
	}

	return &confirmationData{
		MessageId:         messageId,
		SourceBlockNumber: sourceBlockNumber,
		IsSuccess:         isSuccess,
		ReturnData:        returnData,
		Sender:            sender,
		Value:             value,
	}, nil
}
