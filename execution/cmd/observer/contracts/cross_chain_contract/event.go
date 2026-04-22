package cross_chain_contract

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/utils/event_helper"
)

// MessageSent represents the MessageSent event (contract v3)
// Topics: [sig, sourceNationId(indexed), destNationId(indexed), msgId(indexed)]
// Data:   isEVM, sender, target, value, payload, timestamp
type MessageSent struct {
	SourceNationId *big.Int       // indexed uint256 (Topics[1])
	DestNationId   *big.Int       // indexed uint256 (Topics[2])
	MsgId          [32]byte       // indexed bytes32 (Topics[3]) = txHash gốc của user
	IsEVM          bool           // bool
	Sender         common.Address // address
	Target         common.Address // address
	Value          *big.Int       // uint256
	Payload        []byte         // bytes
	Timestamp      *big.Int       // uint256
}

// ParseMessageSent converts event data map to MessageSent struct
func ParseMessageSent(eventData map[string]interface{}) (*MessageSent, error) {
	messageSent := &MessageSent{}

	// sourceNationId và destNationId được inject từ Topics bởi parseSentLogRaw
	sourceNationId, err := event_helper.GetBigIntFromEventData(eventData, "sourceNationId")
	if err != nil {
		return nil, fmt.Errorf("sourceNationId: %w", err)
	}
	messageSent.SourceNationId = sourceNationId

	destNationId, err := event_helper.GetBigIntFromEventData(eventData, "destNationId")
	if err != nil {
		return nil, fmt.Errorf("destNationId: %w", err)
	}
	messageSent.DestNationId = destNationId

	if val, ok := eventData["isEVM"]; ok {
		if isEVM, ok := val.(bool); ok {
			messageSent.IsEVM = isEVM
		}
	}

	sender, err := event_helper.GetAddressFromEventData(eventData, "sender")
	if err != nil {
		return nil, fmt.Errorf("sender: %w", err)
	}
	messageSent.Sender = sender

	target, err := event_helper.GetAddressFromEventData(eventData, "target")
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	messageSent.Target = target

	value := event_helper.ExtractBigInt(eventData["value"])
	if value == nil {
		value = big.NewInt(0)
	}
	messageSent.Value = value

	payload := event_helper.ExtractBytesValue(eventData["payload"])
	if payload == nil {
		return nil, fmt.Errorf("payload invalid or missing")
	}
	messageSent.Payload = payload

	timestamp := event_helper.ExtractBigInt(eventData["timestamp"])
	if timestamp == nil {
		if _, ok := eventData["timestamp"]; !ok {
			return nil, fmt.Errorf("timestamp invalid or missing")
		}
		timestamp = big.NewInt(0)
	}
	messageSent.Timestamp = timestamp

	return messageSent, nil
}

// MessageType enum values
const (
	MessageTypeAssetTransfer uint8 = 0
	MessageTypeContractCall  uint8 = 1
)

// MessageStatus enum values
const (
	MessageStatusSuccess uint8 = 0
	MessageStatusFailed  uint8 = 1
)

// MessageReceived represents the MessageReceived event (contract v3)
// Topics: [sig, sourceNationId(indexed), destNationId(indexed), msgId(indexed)]
// Data:   msgType, status, returnData, sender, amount
type MessageReceived struct {
	SourceNationId *big.Int       // uint256 (indexed, from Topics[1])
	DestNationId   *big.Int       // uint256 (indexed, from Topics[2])
	MsgId          [32]byte       // bytes32 (indexed, from Topics[3]) = txHash gốc của user
	MsgType        uint8          // MessageType (0=ASSET_TRANSFER, 1=CONTRACT_CALL)
	Status         uint8          // MessageStatus (0=SUCCESS, 1=FAILED)
	ReturnData     []byte         // bytes
	Sender         common.Address // address
	Amount         *big.Int       // uint256 — giá trị giao dịch, dùng để hoàn tiền khi thất bại
}

// ParseMessageReceived converts event data map to MessageReceived struct
func ParseMessageReceived(eventData map[string]interface{}) (*MessageReceived, error) {
	messageReceived := &MessageReceived{}

	// sourceNationId và destNationId là from log_parsers.go from topics
	sourceNationId, err := event_helper.GetBigIntFromEventData(eventData, "sourceNationId")
	if err != nil {
		return nil, fmt.Errorf("sourceNationId: %w", err)
	}
	messageReceived.SourceNationId = sourceNationId

	destNationId, err := event_helper.GetBigIntFromEventData(eventData, "destNationId")
	if err != nil {
		return nil, fmt.Errorf("destNationId: %w", err)
	}
	messageReceived.DestNationId = destNationId

	// msgType (uint8) — decoded bởi UnpackIntoMap
	if val, ok := eventData["msgType"]; ok {
		if msgType, ok := val.(uint8); ok {
			messageReceived.MsgType = msgType
		} else if bigInt, ok := val.(*big.Int); ok {
			messageReceived.MsgType = uint8(bigInt.Uint64())
		}
	}

	// status (uint8) — decoded bởi UnpackIntoMap
	if val, ok := eventData["status"]; ok {
		if status, ok := val.(uint8); ok {
			messageReceived.Status = status
		} else if bigInt, ok := val.(*big.Int); ok {
			messageReceived.Status = uint8(bigInt.Uint64())
		}
	}

	// returnData (bytes) — decoded bởi UnpackIntoMap
	returnData := event_helper.ExtractBytesValue(eventData["returnData"])
	if returnData == nil {
		returnData = []byte{}
	}
	messageReceived.ReturnData = returnData

	// sender (address) — decoded bởi UnpackIntoMap
	if val, ok := eventData["sender"]; ok {
		if addr, ok := val.(common.Address); ok {
			messageReceived.Sender = addr
		}
	}

	// amount (uint256) — giá trị giao dịch gốc, dùng để hoàn tiền khi thất bại
	amount := event_helper.ExtractBigInt(eventData["amount"])
	if amount == nil {
		amount = big.NewInt(0)
	}
	messageReceived.Amount = amount

	return messageReceived, nil
}

// ChannelStateSet represents the ChannelStateSet event emitted by adminSetChannelState
type ChannelStateSet struct {
	OutboundNonce     *big.Int // new outboundNonce
	ConfirmationNonce *big.Int // new confirmationNonce
}

// ParseChannelStateSet converts event data map to ChannelStateSet struct
func ParseChannelStateSet(eventData map[string]interface{}) (*ChannelStateSet, error) {
	outboundNonce := event_helper.ExtractBigInt(eventData["outboundNonce"])
	if outboundNonce == nil {
		return nil, fmt.Errorf("outboundNonce invalid or missing")
	}

	confirmationNonce := event_helper.ExtractBigInt(eventData["confirmationNonce"])
	if confirmationNonce == nil {
		return nil, fmt.Errorf("confirmationNonce invalid or missing")
	}

	return &ChannelStateSet{
		OutboundNonce:     outboundNonce,
		ConfirmationNonce: confirmationNonce,
	}, nil
}
