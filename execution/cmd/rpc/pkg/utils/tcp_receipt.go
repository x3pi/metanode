package utils

import (
	"fmt"
	"time"

	ethCommon "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/connection_manager/connection_client"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

// WaitForReceiptTCP polls chain qua TCP cho transaction receipt.
// Dùng chung cho robot_handler, account_handler, hoặc bất kỳ nơi nào cần chờ receipt qua TCP.
func WaitForReceiptTCP(
	chainConn *connection_client.ConnectionClient,
	txHash string,
	timeout time.Duration,
) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	checkInterval := 50 * time.Millisecond

	txHashBytes := ethCommon.HexToHash(txHash)
	reqProto := &pb.GetTransactionReceiptRequest{
		TransactionHash: txHashBytes.Bytes(),
	}
	reqBytes, err := proto.Marshal(reqProto)
	if err != nil {
		return nil, fmt.Errorf("marshal receipt request: %w", err)
	}

	for time.Now().Before(deadline) {
		respBytes, err := chainConn.GetTransactionReceipt(reqBytes, 5*time.Second)
		if err != nil {
			time.Sleep(checkInterval)
			continue
		}
		respProto := &pb.GetTransactionReceiptResponse{}
		if err := proto.Unmarshal(respBytes, respProto); err != nil {
			time.Sleep(checkInterval)
			continue
		}
		if respProto.Error != "" || respProto.Receipt == nil {
			time.Sleep(checkInterval)
			continue
		}
		receiptBytes, _ := proto.Marshal(respProto.Receipt)
		return receiptBytes, nil
	}
	return nil, fmt.Errorf("timeout waiting for receipt: %s", txHash)
}
