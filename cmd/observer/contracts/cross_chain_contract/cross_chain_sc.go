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
		return common.Hash{}, fmt.Errorf("BatchSubmit: send failed: %w", err)
	}

	logger.Info("📤 BatchSubmit sent: from=%s, nonce=%d, events=%d, txHash=%s", from.Hex(), nonce, len(events), txHash.Hex())
	return txHash, nil
}

// CrossChainPacket matches the Solidity struct CrossChainGateway.CrossChainPacket
type CrossChainPacket struct {
	SourceNationId *big.Int       `abi:"sourceNationId"`
	DestNationId   *big.Int       `abi:"destNationId"`
	Timestamp      *big.Int       `abi:"timestamp"`
	Sender         common.Address `abi:"sender"`
	Target         common.Address `abi:"target"`
	Value          *big.Int       `abi:"value"`
	Payload        []byte         `abi:"payload"`
}

// ChannelInfo struct to match Solidity getChannelInfo return values
type ChannelInfo struct {
	SourceNationId                 *big.Int
	DestNationId                   *big.Int
	OutboundNonce                  *big.Int
	InboundNonce                   *big.Int
	InboundLastProcessedBlock      *big.Int
	ConfirmationNonce              *big.Int
	ConfirmationLastProcessedBlock *big.Int
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

// ReceiveMessage calls the multi-sig receiveMessage on contract.
// Each relayer calls this independently. Contract auto-executes when threshold is met.
// ethSignature is used by the contract to ecrecover and verify the signer matches msg.sender.
func ReceiveMessage(
	cli *clientpkg.Client,
	cfg *c_config.ClientConfig,
	from common.Address,
	contract common.Address,
	packet CrossChainPacket,
	sourceBlockNumber *big.Int,
	ethSignature []byte,
	opts *tx_models.TxOptions,
) error {
	input, err := cfg.CrossChainAbi.Pack(
		"receiveMessage",
		packet,
		sourceBlockNumber,
		ethSignature,
	)
	if err != nil {
		return fmt.Errorf("failed to pack receiveMessage params: %w", err)
	}
	_, err = tx_helper.SendTransaction("receiveMessage", cli, cfg, contract, from, input, opts)
	return err
}

func BatchReceiveMessage(
	cli *clientpkg.Client,
	cfg *c_config.ClientConfig,
	from common.Address,
	contract common.Address,
	packets []CrossChainPacket,
	sourceBlockNumbers []*big.Int,
	batchSignature []byte,
	opts *tx_models.TxOptions,
) error {
	input, err := cfg.CrossChainAbi.Pack(
		"batchReceiveMessage",
		packets,
		sourceBlockNumbers,
		batchSignature,
	)
	if err != nil {
		return fmt.Errorf("failed to pack batchReceiveMessage params: %w", err)
	}
	_, err = tx_helper.SendTransaction("batchReceiveMessage", cli, cfg, contract, from, input, opts)
	return err
}

func GetChannelInfo(
	cli *clientpkg.Client,
	cfg *c_config.ClientConfig,
	contract common.Address,
	from common.Address,
	opts *tx_models.TxOptions,
) (*ChannelInfo, error) {
	input, err := cfg.CrossChainAbi.Pack(
		"getChannelInfo",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to pack getChannelInfo params: %w", err)
	}
	receipt, err := tx_helper.SendReadTransaction("getChannelInfo", cli, cfg, contract, from, input, opts)
	if err != nil {
		return nil, err
	}

	returnData := receipt.Return()
	if len(returnData) == 0 {
		return nil, fmt.Errorf("contract returned empty data (check contract address or function existence)")
	}
	// returnData sẽ được parse và log chi tiết bên dưới

	// Parse using ABI method outputs
	method, exists := cfg.CrossChainAbi.Methods["getChannelInfo"]
	if !exists {
		return nil, fmt.Errorf("getChannelInfo method not found in ABI")
	}

	// Unpack results using method.Outputs
	results, err := method.Outputs.Unpack(returnData)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack getChannelInfo result: %w", err)
	}

	// Expecting 7 return values: sourceNationId, destNationId, outbound, inbound, inboundLastBlock, confirmNonce, confirmLastBlock
	if len(results) != 7 {
		return nil, fmt.Errorf("unexpected return length from getChannelInfo: got %d, want 7", len(results))
	}

	// Extract all values
	sourceNationId, ok1 := results[0].(*big.Int)
	if !ok1 {
		return nil, fmt.Errorf("failed to cast sourceNationId to *big.Int, got type: %T", results[0])
	}

	destNationId, ok2 := results[1].(*big.Int)
	if !ok2 {
		return nil, fmt.Errorf("failed to cast destNationId to *big.Int, got type: %T", results[1])
	}

	outboundNonce, ok3 := results[2].(*big.Int)
	if !ok3 {
		return nil, fmt.Errorf("failed to cast outboundNonce to *big.Int, got type: %T", results[2])
	}

	inboundNonce, ok4 := results[3].(*big.Int)
	if !ok4 {
		return nil, fmt.Errorf("failed to cast inboundNonce to *big.Int, got type: %T", results[3])
	}

	inboundLastBlock, ok5 := results[4].(*big.Int)
	if !ok5 {
		return nil, fmt.Errorf("failed to cast inboundLastBlock to *big.Int, got type: %T", results[4])
	}

	confirmNonce, ok6 := results[5].(*big.Int)
	if !ok6 {
		return nil, fmt.Errorf("failed to cast confirmNonce to *big.Int, got type: %T", results[5])
	}

	confirmLastBlock, ok7 := results[6].(*big.Int)
	if !ok7 {
		return nil, fmt.Errorf("failed to cast confirmLastBlock to *big.Int, got type: %T", results[6])
	}

	logger.Info("✅ Parsed ChannelInfo: sourceNationId=%v, destNationId=%v, outboundNonce=%v, inboundNonce=%v, inboundLastBlock=%v, confirmNonce=%v, confirmLastBlock=%v",
		sourceNationId, destNationId, outboundNonce, inboundNonce, inboundLastBlock, confirmNonce, confirmLastBlock)

	return &ChannelInfo{
		SourceNationId:                 sourceNationId,
		DestNationId:                   destNationId,
		OutboundNonce:                  outboundNonce,
		InboundNonce:                   inboundNonce,
		InboundLastProcessedBlock:      inboundLastBlock,
		ConfirmationNonce:              confirmNonce,
		ConfirmationLastProcessedBlock: confirmLastBlock,
	}, nil
}

// GetOutboundMessage gets outbound message info
func GetOutboundMessage(
	cli *clientpkg.Client,
	cfg *c_config.ClientConfig,
	contract common.Address,
	from common.Address,
	messageId [32]byte,
	opts *tx_models.TxOptions,
) (*OutboundMessage, error) {
	input, err := cfg.CrossChainAbi.Pack(
		"getOutboundMessage",
		messageId,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to pack getOutboundMessage params: %w", err)
	}

	receipt, err := tx_helper.SendReadTransaction("getOutboundMessage", cli, cfg, contract, from, input, opts)
	if err != nil {
		return nil, err
	}

	returnData := receipt.Return()
	if len(returnData) == 0 {
		return nil, fmt.Errorf("contract returned empty data")
	}

	method, exists := cfg.CrossChainAbi.Methods["getOutboundMessage"]
	if !exists {
		return nil, fmt.Errorf("getOutboundMessage method not found in ABI")
	}

	results, err := method.Outputs.Unpack(returnData)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack getOutboundMessage result: %w", err)
	}

	if len(results) != 1 {
		return nil, fmt.Errorf("unexpected return length: got %d, want 1", len(results))
	}

	// The result should be a struct
	outboundMsg := &OutboundMessage{}

	// Type assertion for the struct - the ABI unpacker returns the struct
	// We need to extract fields manually or use reflection
	// For now, return empty struct - TODO: implement proper parsing
	logger.Info("✅ GetOutboundMessage called for messageId=%x", messageId)

	return outboundMsg, nil
}
