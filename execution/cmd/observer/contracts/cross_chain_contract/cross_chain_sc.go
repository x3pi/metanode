package cross_chain_contract

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	clientpkg "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/utils/tx_helper"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/pkg/models/tx_models"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// ─────────────────────────────────────────────────────────────────────────────
// EmbassyEvent — struct dùng cho batchSubmit
// ─────────────────────────────────────────────────────────────────────────────

// EventKindInbound / EventKindConfirmation mirrors Solidity enum EventKind.
const (
	EventKindInbound      uint8 = 0
	EventKindConfirmation uint8 = 1
)

// EmbassyEventSolidity là struct ABI-pack khớp với Solidity EmbassyEvent tuple.
// Đặt ở đây để tách khỏi scanner, dùng chung cho mọi caller.
type EmbassyEventSolidity struct {
	EventKind    uint8             `abi:"eventKind"`
	BlockNumber  *big.Int          `abi:"blockNumber"`
	Packet       CrossChainPacket  `abi:"packet"`
	Confirmation ConfirmationParam `abi:"confirmation"`
}

// EmbassyEventInput dữ liệu thô từ scanner trước khi convert sang Solidity format.
type EmbassyEventInput struct {
	EventKind    uint8
	BlockNumber  uint64
	Packet       CrossChainPacket
	Confirmation ConfirmationParam
}

// bigIntOrZero trả về b nếu không nil, ngược lại trả về new(big.Int).
func bigIntOrZero(b *big.Int) *big.Int {
	if b == nil {
		return new(big.Int)
	}
	return b
}

// bytesOrEmpty trả về b nếu không nil, ngược lại trả về []byte{}.
func bytesOrEmpty(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// ToSolEvent chuyển EmbassyEventInput → EmbassyEventSolidity để ABI pack.
// Normalize tất cả *big.Int nil → new(big.Int) và []byte nil → []byte{}
// để abi.Pack không panic với "zero Value".
func ToSolEvent(ev EmbassyEventInput) EmbassyEventSolidity {
	return EmbassyEventSolidity{
		EventKind:   ev.EventKind,
		BlockNumber: new(big.Int).SetUint64(ev.BlockNumber),
		Packet: CrossChainPacket{
			MessageId:      ev.Packet.MessageId,
			SourceNationId: bigIntOrZero(ev.Packet.SourceNationId),
			DestNationId:   bigIntOrZero(ev.Packet.DestNationId),
			Timestamp:      bigIntOrZero(ev.Packet.Timestamp),
			Sender:         ev.Packet.Sender,
			Target:         ev.Packet.Target,
			Value:          bigIntOrZero(ev.Packet.Value),
			Payload:        bytesOrEmpty(ev.Packet.Payload),
		},
		Confirmation: ConfirmationParam{
			MessageId:         ev.Confirmation.MessageId,
			SourceBlockNumber: bigIntOrZero(ev.Confirmation.SourceBlockNumber),
			IsSuccess:         ev.Confirmation.IsSuccess,
			ReturnData:        bytesOrEmpty(ev.Confirmation.ReturnData),
			Sender:            ev.Confirmation.Sender,
			Value:             bigIntOrZero(ev.Confirmation.Value),
		},
	}
}

// BatchSubmit ABI-encode batchSubmit(EmbassyEvent[], bytes embassyPubKey) và gửi TX qua WalletPool (fire-and-forget).
// from: địa chỉ ví trong WalletPool đã Acquire.
// embassyPubKey: BLS public key (48 bytes) của embassy gửi TX — chain dùng để verify O(1).
// Trả về txHash để caller lưu vào txHashToWallet map.
func BatchSubmit(
	cli *clientpkg.Client,
	cfg *c_config.ClientConfig,
	contract common.Address,
	from common.Address,
	events []EmbassyEventInput,
	embassyPubKey []byte,
	opts *tx_models.TxOptions,
	batchIdHex string,
) (common.Hash, error) {
	if len(events) == 0 {
		return common.Hash{}, fmt.Errorf("BatchSubmit: events list is empty")
	}

	// Chuyển sang Solidity tuple format
	solEvents := make([]EmbassyEventSolidity, len(events))
	for i, ev := range events {
		solEvents[i] = ToSolEvent(ev)
	}

	// ABI pack batchSubmit(EmbassyEvent[] events, bytes embassyPubKey)
	input, err := cfg.CrossChainAbi.Pack("batchSubmit", solEvents, embassyPubKey)
	if err != nil {
		return common.Hash{}, fmt.Errorf("BatchSubmit: pack failed: %w", err)
	}

	txHash, nonce, err := tx_helper.SendTransactionFromWallet("batchSubmit", cli, cfg, contract, from, input, opts)
	if err != nil {
		return txHash, fmt.Errorf("BatchSubmit: send failed: %w", err)
	}

	var firstMsgId, lastMsgId string
	if len(events) > 0 {
		if events[0].EventKind == EventKindInbound {
			firstMsgId = fmt.Sprintf("%x", events[0].Packet.MessageId[:])
			lastMsgId = fmt.Sprintf("%x", events[len(events)-1].Packet.MessageId[:])
		} else {
			firstMsgId = fmt.Sprintf("%x", events[0].Confirmation.MessageId[:])
			lastMsgId = fmt.Sprintf("%x", events[len(events)-1].Confirmation.MessageId[:])
		}
	}

	logger.Info("📤 BatchSubmit [Batch %s] sent to node %s: from=%s, nonce=%d, events=%d, txHash=%s, firstMsgId=%s, lastMsgId=%s", batchIdHex, cli.GetNodeAddr(), from.Hex(), nonce, len(events), txHash.Hex(), firstMsgId, lastMsgId)
	return txHash, nil
}

// CrossChainPacket matches the Solidity struct CrossChainGateway.CrossChainPacket
type CrossChainPacket struct {
	MessageId      [32]byte       `abi:"messageId"`
	SourceNationId *big.Int       `abi:"sourceNationId"`
	DestNationId   *big.Int       `abi:"destNationId"`
	Timestamp      *big.Int       `abi:"timestamp"`
	Sender         common.Address `abi:"sender"`
	Target         common.Address `abi:"target"`
	Value          *big.Int       `abi:"value"`
	Payload        []byte         `abi:"payload"`
}

// OutboundMessage struct to match Solidity
type OutboundMessage struct {
	MessageId   [32]byte
	Sender      common.Address
	Target      common.Address
	Amount      *big.Int
	MsgType     uint8 // 0=ASSET_TRANSFER, 1=CONTRACT_CALL
	IsConfirmed bool
	IsRefunded  bool
	Timestamp   *big.Int
}

// ConfirmationParam matches Solidity struct CrossChainGateway.ConfirmationParam
type ConfirmationParam struct {
	MessageId         [32]byte       `abi:"messageId"`
	SourceBlockNumber *big.Int       `abi:"sourceBlockNumber"`
	IsSuccess         bool           `abi:"isSuccess"`
	ReturnData        []byte         `abi:"returnData"`
	Sender            common.Address `abi:"sender"`
	Value             *big.Int       `abi:"value"` // Số tiền gốc từ outbound message để hoàn lại nếu thất bại
}
