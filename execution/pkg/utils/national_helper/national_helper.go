package national_helper

// import (
// 	"encoding/json"
// 	"fmt"

// 	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
// 	"github.com/meta-node-blockchain/meta-node/pkg/logger"
// 	pb_cross "github.com/meta-node-blockchain/meta-node/pkg/proto/cross_chain_proto"
// 	"github.com/meta-node-blockchain/meta-node/types/network"
// 	"google.golang.org/protobuf/proto"
// )

// SendNationalCheckpoint gửi national checkpoint tới global observer
// func SendNationalCheckpoint(
// 	messageSender network.MessageSender,
// 	conn network.Connection,
// 	checkpoint *pb_cross.NationCheckpoint,
// ) error {
// 	// Marshal checkpoint thành protobuf
// 	checkpointBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(checkpoint)
// 	if err != nil {
// 		return fmt.Errorf("failed to marshal national checkpoint: %w", err)
// 	}

// 	// Gửi qua connection
// 	err = messageSender.SendBytes(conn, command.SaveNationalRoot, checkpointBytes)
// 	if err != nil {
// 		return fmt.Errorf("failed to send national checkpoint: %w", err)
// 	}

// 	logger.Info("📤 Sent national checkpoint",
// 		"nation_id", checkpoint.NationId,
// 		"block_number", checkpoint.BlockNumber,
// 		"block_hash", fmt.Sprintf("0x%x", checkpoint.BlockHash))

// 	return nil
// }

// SendNationalCheckpointAck gửi ACK cho national checkpoint
// func SendNationalCheckpointAck(
// 	messageSender network.MessageSender,
// 	conn network.Connection,
// 	ack map[string]interface{},
// ) error {
// 	ackBytes, err := json.Marshal(ack)
// 	if err != nil {
// 		return fmt.Errorf("failed to marshal ACK: %w", err)
// 	}

// 	err = messageSender.SendBytes(conn, command.NationalCheckpointAck, ackBytes)
// 	if err != nil {
// 		return fmt.Errorf("failed to send ACK: %w", err)
// 	}

// 	logger.Info("📤 Sent national checkpoint ACK", "status", ack["status"])
// 	return nil
// }
