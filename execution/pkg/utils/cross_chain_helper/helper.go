package cross_chain_helper

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb_cross "github.com/meta-node-blockchain/meta-node/pkg/proto/cross_chain_proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

const (
	CROSS_CLUSTER_TRANSFER_TYPE = 100 // Cross-cluster transfer type
	MINT_TRANSACTION_TYPE       = 101 // Mint transaction type
)

// CrossClusterMetadata chứa thông tin metadata được decode
type CrossClusterMetadata struct {
	IsCrossCluster bool
	TargetNationId uint64
	OriginalInput  []byte // Có thể nil cho cross-cluster transfer
}

// SimpleCrossClusterMetadata struct đơn giản chỉ chứa TargetClusterId (format mới)
type SimpleCrossClusterMetadata struct {
	TargetNationId uint64 `json:"target_nation_id"`
}

// ParseCrossClusterMetadata parses cross-chain metadata from transaction input
// New format: 32-byte Simple Bytes encoding (Ethereum standard)
// Format: uint64 value left-padded to 32 bytes
// Example: 0x0000000000000000000000000000000000000000000000000000000000000002 (nation_id = 2)
func ParseCrossClusterMetadata(callDataInput []byte) (*CrossClusterMetadata, error) {
	// Check if input is exactly 32 bytes (cross-chain metadata format)
	var metadata pb_cross.CrossChainTransferMetadata
	if err := proto.Unmarshal(callDataInput, &metadata); err != nil {
		return nil, err
	}
	return &CrossClusterMetadata{
		IsCrossCluster: true,
		TargetNationId: metadata.TargetNationId,
		OriginalInput:  nil,
	}, nil

}

// SendCrossClusterTransferAck sends cross-cluster transfer ACK via network
func SendCrossClusterTransferAck(
	messageSender network.MessageSender,
	conn network.Connection,
	ack *pb_cross.CrossClusterTransferAck,
	command string,
) error {
	// Set timestamp if not set
	if ack.Timestamp == 0 {
		ack.Timestamp = time.Now().Unix()
	}
	// Marshal proto message directly
	ackBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(ack)
	if err != nil {
		logger.Error("Failed to marshal ACK", "error", err)
		return err
	}

	err = messageSender.SendBytes(conn, command, ackBytes)
	if err != nil {
		logger.Error("Failed to send ACK", "error", err, "txHash", common.BytesToHash(ack.TxHash).Hex())
	} else {
		logger.Info("ACK sent successfully", "txHash", common.BytesToHash(ack.TxHash).Hex(), "status", ack.Status)
	}

	return err
}
