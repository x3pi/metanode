package explorer

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ExplorerTransaction hợp nhất một transaction, receipt và block header
// để hiển thị trên explorer.
type ExplorerTransaction struct {
	Tx     types.Transaction
	Rcpt   types.Receipt
	Header types.BlockHeader // THÊM: Trường chứa thông tin header của block
}

// NewExplorerTransaction tạo một đối tượng ExplorerTransaction mới.
// CẬP NHẬT: Thêm tham số `header`.
func NewExplorerTransaction(tx types.Transaction, rcpt types.Receipt, header types.BlockHeader) *ExplorerTransaction {
	return &ExplorerTransaction{
		Tx:     tx,
		Rcpt:   rcpt,
		Header: header,
	}
}

// MarshalJSON triển khai interface json.Marshaler để chuyển đổi đối tượng
// ExplorerTransaction thành JSON.
func (et *ExplorerTransaction) MarshalJSON() ([]byte, error) {
	if et.Tx == nil {
		return nil, fmt.Errorf("trường transaction (Tx) không được rỗng")
	}

	jsonData := make(map[string]interface{})

	if et.Header != nil {
		jsonData["blockNumber"] = et.Header.BlockNumber()
		jsonData["blockHash"] = et.Header.Hash().Hex()
		jsonData["timestamp"] = et.Header.TimeStamp()
	}

	// --- Dữ liệu từ Transaction ---
	jsonData["hash"] = et.Tx.Hash().Hex()
	jsonData["from"] = et.Tx.FromAddress().Hex()
	toAddress := et.Tx.ToAddress()
	if toAddress == (common.Address{}) {
		jsonData["to"] = nil
	} else {
		jsonData["to"] = toAddress.Hex()
	}
	jsonData["value"] = "0x" + et.Tx.Amount().Text(16)
	jsonData["nonce"] = et.Tx.GetNonce()
	jsonData["gas"] = et.Tx.MaxGas()
	jsonData["gasPrice"] = et.Tx.MaxGasPrice()
	jsonData["data"] = "0x" + hex.EncodeToString(et.Tx.Data())
	jsonData["chainId"] = et.Tx.GetChainID()

	v, r, s := et.Tx.RawSignatureValues()
	if v != nil {
		jsonData["v"] = "0x" + v.Text(16)
	}
	if r != nil {
		jsonData["r"] = "0x" + r.Text(16)
	}
	if s != nil {
		jsonData["s"] = "0x" + s.Text(16)
	}

	// Phân tích và thêm dữ liệu token vào JSON
	if et.Tx.IsCallContract() {
		if tokenData, ok := ParseERC20Transfer(et.Tx.FromAddress(), et.Tx.CallData().Input()); ok {
			jsonData["tokenTransfer"] = map[string]interface{}{
				"from":  tokenData.From.Hex(),
				"to":    tokenData.To.Hex(),
				"value": tokenData.Value.String(),
				"token": toAddress.Hex(),
			}
		}
	}

	// --- Dữ liệu từ Receipt (nếu có) ---
	if et.Rcpt != nil {
		jsonData["rHash"] = et.Rcpt.RHash().Hex()
		jsonData["transactionIndex"] = et.Rcpt.TransactionIndex()
		jsonData["gasUsed"] = et.Rcpt.GasUsed()
		jsonData["gasFee"] = et.Rcpt.GasFee()
		jsonData["status"] = et.Rcpt.Status().String()
		jsonData["returnValue"] = "0x" + hex.EncodeToString(et.Rcpt.Return())
		jsonData["exception"] = et.Rcpt.Exception().String()

		if toAddress == (common.Address{}) {
			jsonData["contractAddress"] = et.Rcpt.ToAddress().Hex()
		}

		// Định dạng Event Logs
		logs := []map[string]interface{}{}
		for _, logProto := range et.Rcpt.EventLogs() {
			var topics []string
			for _, topicBytes := range logProto.GetTopics() {
				topics = append(topics, "0x"+hex.EncodeToString(topicBytes))
			}
			logs = append(logs, map[string]interface{}{
				"address": "0x" + hex.EncodeToString(logProto.GetAddress()),
				"topics":  topics,
				"data":    "0x" + hex.EncodeToString(logProto.GetData()),
			})
		}
		jsonData["logs"] = logs
	}

	return json.Marshal(jsonData)
}

// ToJSONString chuyển đổi đối tượng ExplorerTransaction thành chuỗi JSON.
func (et *ExplorerTransaction) ToJSONString() (string, error) {
	b, err := et.MarshalJSON()
	if err != nil {
		return "", err
	}
	return string(b), nil
}
