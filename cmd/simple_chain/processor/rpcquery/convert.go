// Package rpcquery provides pure conversion functions for RPC query responses.
//
// These functions convert internal types (receipts, logs, transactions) to
// their Protobuf representations for JSON-RPC responses. They are stateless
// and do not depend on BlockProcessor, making them independently testable.
package rpcquery

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// ConvertLogsToProto converts go-ethereum Log entries to Protobuf LogEntry
// messages suitable for JSON-RPC responses (eth_getLogs, eth_getTransactionReceipt).
func ConvertLogsToProto(logs []*ethtypes.Log) []*pb.LogEntry {
	result := make([]*pb.LogEntry, 0, len(logs))
	for _, log := range logs {
		topics := make([][]byte, len(log.Topics))
		for i, t := range log.Topics {
			topics[i] = t.Bytes()
		}
		entry := &pb.LogEntry{
			Address:          log.Address.Bytes(),
			Topics:           topics,
			Data:             log.Data,
			BlockNumber:      log.BlockNumber,
			TransactionHash:  log.TxHash.Bytes(),
			TransactionIndex: uint64(log.TxIndex),
			BlockHash:        log.BlockHash.Bytes(),
			LogIndex:         uint64(log.Index),
			Removed:          log.Removed,
		}
		result = append(result, entry)
	}
	return result
}

// FormatBigIntHex formats a *big.Int as a 0x-prefixed hex string.
// Returns "0x0" for nil or zero values.
func FormatBigIntHex(val *big.Int) string {
	if val == nil || val.Sign() == 0 {
		return "0x0"
	}
	return "0x" + val.Text(16)
}

// FormatUint64Hex formats a uint64 as a 0x-prefixed hex string.
func FormatUint64Hex(val uint64) string {
	if val == 0 {
		return "0x0"
	}
	b := new(big.Int).SetUint64(val)
	return "0x" + b.Text(16)
}

// FormatHashHex returns the hex representation of a hash.
// Returns the zero-hash hex for empty hashes.
func FormatHashHex(h common.Hash) string {
	return h.Hex()
}
