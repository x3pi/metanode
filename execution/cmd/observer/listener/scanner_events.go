package listener

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/contracts/cross_chain_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// buildEmbassyEvents parse logs → []EmbassyEventInput (dùng type từ cross_chain_contract).
// MessageSent → INBOUND, MessageReceived → CONFIRMATION.
func (s *CrossChainScanner) buildEmbassyEvents(
	rc tcp_config.RemoteChain,
	logs []*pb.LogEntry,
	messageSentTopic common.Hash,
	messageReceivedTopic common.Hash,
) []cross_chain_contract.EmbassyEventInput {
	events := make([]cross_chain_contract.EmbassyEventInput, 0, len(logs))

	for _, log := range logs {
		if len(log.Topics) == 0 {
			continue
		}
		topic0 := common.BytesToHash(log.Topics[0])

		switch topic0 {
		case messageSentTopic:
			ev, err := s.buildInboundEvent(rc, log)
			if err != nil {
				logger.Warn("[Scanner][%s] buildInboundEvent failed (block=%d): %v", rc.Name, log.BlockNumber, err)
				continue
			}
			events = append(events, ev)

		case messageReceivedTopic:
			ev, err := s.buildConfirmationEvent(rc, log)
			if err != nil {
				logger.Warn("[Scanner][%s] buildConfirmationEvent failed (block=%d): %v", rc.Name, log.BlockNumber, err)
				continue
			}
			events = append(events, ev)
		}
	}
	return events
}

// buildInboundEvent chuyển MessageSent log → EmbassyEventInput{INBOUND}.
func (s *CrossChainScanner) buildInboundEvent(rc tcp_config.RemoteChain, log *pb.LogEntry) (cross_chain_contract.EmbassyEventInput, error) {
	msgSent, err := parseSentLogRaw(s.cfg, log.Data, log.Topics)
	if err != nil {
		return cross_chain_contract.EmbassyEventInput{}, fmt.Errorf("parseSentLog: %w", err)
	}

	// msgSent.MsgId đã được parseSentLogRaw parse từ Topics[3] (txHash gốc của user trên chain nguồn)
	// logger.Info("[MSGID-TRACE] 📵 [2/4] SCANNER[%s] READ MessageSent: msgId=0x%x src=%v→dest=%v block=%d sender=%s\n"+
	// 	"        ⇨ sẽ gửi INBOUND vào chain %s",
	// 	rc.Name,
	// 	msgSent.MsgId[:], // full 32 bytes
	// 	msgSent.SourceNationId, msgSent.DestNationId,
	// 	log.BlockNumber, msgSent.Sender.Hex(),
	// 	msgSent.DestNationId,
	// )

	// Đảm bảo Payload không nil để ABI pack không panic
	payload := msgSent.Payload
	if payload == nil {
		payload = []byte{}
	}

	packet := cross_chain_contract.CrossChainPacket{
		MessageId:      msgSent.MsgId,
		SourceNationId: msgSent.SourceNationId,
		DestNationId:   msgSent.DestNationId,
		Timestamp:      msgSent.Timestamp,
		Sender:         msgSent.Sender,
		Target:         msgSent.Target,
		Value:          msgSent.Value,
		Payload:        payload,
	}

	return cross_chain_contract.EmbassyEventInput{
		EventKind:   cross_chain_contract.EventKindInbound,
		Packet:      packet,
		BlockNumber: log.BlockNumber,
		// Dùng Confirmation.MessageId để carry msgId gốc → handler emit vào MessageReceived
		Confirmation: cross_chain_contract.ConfirmationParam{
			MessageId:  msgSent.MsgId, // ← từ struct, đã được parse từ Topics[3]
			ReturnData: []byte{},
		},
	}, nil
}

// buildConfirmationEvent chuyển MessageReceived log → EmbassyEventInput{CONFIRMATION}.
func (s *CrossChainScanner) buildConfirmationEvent(rc tcp_config.RemoteChain, log *pb.LogEntry) (cross_chain_contract.EmbassyEventInput, error) {
	msgReceived, err := parseReceivedLogRaw(s.cfg, log.Data, log.Topics)
	if err != nil {
		return cross_chain_contract.EmbassyEventInput{}, fmt.Errorf("parseReceivedLog: %w", err)
	}

	statusStr := "SUCCESS"
	if msgReceived.Status != cross_chain_contract.MessageStatusSuccess {
		statusStr = "FAILED"
	}
	msgTypeStr := "ASSET_TRANSFER"
	if msgReceived.MsgType == cross_chain_contract.MessageTypeContractCall {
		msgTypeStr = "CONTRACT_CALL"
	}

	// msgReceived.MsgId đã được parseReceivedLogRaw parse từ Topics[3]
	logger.Info("[MSGID-TRACE] 📵 [3b/4] SCANNER[%s] READ MessageReceived: msgId=0x%x src=%v→dest=%v block=%d status=%s type=%s\n"+
		"        ⇨ sẽ gửi CONFIRMATION về chain %s",
		rc.Name,
		msgReceived.MsgId[:], // full 32 bytes
		msgReceived.SourceNationId, msgReceived.DestNationId,
		log.BlockNumber, statusStr, msgTypeStr,
		msgReceived.SourceNationId,
	)

	// Đảm bảo ReturnData không nil để ABI pack không panic
	returnData := msgReceived.ReturnData
	if returnData == nil {
		returnData = []byte{}
	}

	confirmation := cross_chain_contract.ConfirmationParam{
		MessageId:         msgReceived.MsgId, // ← từ struct, đã parse từ Topics[3]
		SourceBlockNumber: new(big.Int).SetUint64(log.BlockNumber),
		IsSuccess:         msgReceived.Status == cross_chain_contract.MessageStatusSuccess,
		ReturnData:        returnData,
		Sender:            msgReceived.Sender,
		Value:             msgReceived.Amount,
	}

	if !confirmation.IsSuccess && msgReceived.Amount != nil && msgReceived.Amount.Sign() > 0 {
		logger.Info("💰 [Scanner][%s] Confirmation FAILED — refund amount: %s",
			rc.Name, msgReceived.Amount.String())
	}

	return cross_chain_contract.EmbassyEventInput{
		EventKind:    cross_chain_contract.EventKindConfirmation,
		Confirmation: confirmation,
		BlockNumber:  log.BlockNumber,
		Packet: cross_chain_contract.CrossChainPacket{
			Payload: []byte{},
		},
	}, nil
}
