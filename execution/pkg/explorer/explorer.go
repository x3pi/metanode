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

type explorerTokenTransferJSON struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Token string `json:"token"`
	Value string `json:"value"`
}

type explorerLogJSON struct {
	Address string   `json:"address"`
	Data    string   `json:"data"`
	Topics  []string `json:"topics"`
}

// explorerTxFullJSON avoids map[string]interface{} allocations, interface boxing,
// and reflection map-key sorting on the common hot path where Header and Rcpt are non-nil.
type explorerTxFullJSON struct {
	BlockHash        string                     `json:"blockHash"`
	BlockNumber      uint64                     `json:"blockNumber"`
	ChainID          uint64                     `json:"chainId"`
	ContractAddress  string                     `json:"contractAddress,omitempty"`
	Data             string                     `json:"data"`
	Exception        string                     `json:"exception"`
	From             string                     `json:"from"`
	Gas              uint64                     `json:"gas"`
	GasFee           uint64                     `json:"gasFee"`
	GasPrice         uint64                     `json:"gasPrice"`
	GasUsed          uint64                     `json:"gasUsed"`
	Hash             string                     `json:"hash"`
	Logs             []explorerLogJSON          `json:"logs"`
	Nonce            uint64                     `json:"nonce"`
	R                string                     `json:"r,omitempty"`
	RHash            string                     `json:"rHash"`
	ReturnValue      string                     `json:"returnValue"`
	S                string                     `json:"s,omitempty"`
	Status           string                     `json:"status"`
	Timestamp        uint64                     `json:"timestamp"`
	To               *string                    `json:"to"`
	TokenTransfer    *explorerTokenTransferJSON `json:"tokenTransfer,omitempty"`
	TransactionIndex uint64                     `json:"transactionIndex"`
	V                string                     `json:"v,omitempty"`
	Value            string                     `json:"value"`
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

func marshalExplorerTxFull(
	tx types.Transaction,
	rcpt types.Receipt,
	header types.BlockHeader,
	blockHashHex string,
	txHashHex string,
	fromHex string,
	toAddr common.Address,
	toHex string,
	rHashHex string,
	tokenData *ERC20TransferData,
	tokenParsed bool,
) ([]byte, error) {
	if blockHashHex == "" {
		blockHashHex = header.Hash().Hex()
	}
	if txHashHex == "" {
		txHashHex = tx.Hash().Hex()
	}
	if fromHex == "" {
		fromHex = tx.FromAddress().Hex()
	}
	if rHashHex == "" {
		rHashHex = rcpt.RHash().Hex()
	}

	var toPtr *string
	var contractAddr string
	if toAddr == (common.Address{}) {
		contractAddr = rcpt.ToAddress().Hex()
	} else {
		if toHex == "" {
			toHex = toAddr.Hex()
		}
		toCopy := toHex
		toPtr = &toCopy
	}

	valHex := "0x0"
	if amt := tx.Amount(); amt != nil {
		valHex = "0x" + amt.Text(16)
	}

	var vStr, rStr, sStr string
	v, r, s := tx.RawSignatureValues()
	if v != nil {
		vStr = "0x" + v.Text(16)
	}
	if r != nil {
		rStr = "0x" + r.Text(16)
	}
	if s != nil {
		sStr = "0x" + s.Text(16)
	}

	var tokenTransfer *explorerTokenTransferJSON
	if !tokenParsed && tx.IsCallContract() && tx.CallData() != nil {
		if td, ok := ParseERC20Transfer(tx.FromAddress(), tx.CallData().Input()); ok {
			tokenData = td
		}
	}
	if tokenData != nil {
		if toHex == "" {
			toHex = toAddr.Hex()
		}
		tokenTransfer = &explorerTokenTransferJSON{
			From:  tokenData.From.Hex(),
			To:    tokenData.To.Hex(),
			Token: toHex,
			Value: tokenData.Value.String(),
		}
	}

	eventLogs := rcpt.EventLogs()
	logs := make([]explorerLogJSON, len(eventLogs))
	for i, logProto := range eventLogs {
		rawTopics := logProto.GetTopics()
		var topics []string
		if len(rawTopics) > 0 {
			topics = make([]string, len(rawTopics))
			for j, topicBytes := range rawTopics {
				topics[j] = "0x" + hex.EncodeToString(topicBytes)
			}
		}
		logs[i] = explorerLogJSON{
			Address: "0x" + hex.EncodeToString(logProto.GetAddress()),
			Data:    "0x" + hex.EncodeToString(logProto.GetData()),
			Topics:  topics,
		}
	}

	payload := explorerTxFullJSON{
		BlockHash:        blockHashHex,
		BlockNumber:      header.BlockNumber(),
		ChainID:          tx.GetChainID(),
		ContractAddress:  contractAddr,
		Data:             "0x" + hex.EncodeToString(tx.Data()),
		Exception:        rcpt.Exception().String(),
		From:             fromHex,
		Gas:              tx.MaxGas(),
		GasFee:           rcpt.GasFee(),
		GasPrice:         tx.MaxGasPrice(),
		GasUsed:          rcpt.GasUsed(),
		Hash:             txHashHex,
		Logs:             logs,
		Nonce:            tx.GetNonce(),
		R:                rStr,
		RHash:            rHashHex,
		ReturnValue:      "0x" + hex.EncodeToString(rcpt.Return()),
		S:                sStr,
		Status:           rcpt.Status().String(),
		Timestamp:        header.TimeStamp(),
		To:               toPtr,
		TokenTransfer:    tokenTransfer,
		TransactionIndex: rcpt.TransactionIndex(),
		V:                vStr,
		Value:            valHex,
	}
	return json.Marshal(&payload)
}

// MarshalJSON triển khai interface json.Marshaler để chuyển đổi đối tượng
// ExplorerTransaction thành JSON.
func (et *ExplorerTransaction) MarshalJSON() ([]byte, error) {
	if et.Tx == nil {
		return nil, fmt.Errorf("trường transaction (Tx) không được rỗng")
	}

	toAddress := et.Tx.ToAddress()
	if et.Header != nil && et.Rcpt != nil {
		return marshalExplorerTxFull(et.Tx, et.Rcpt, et.Header, "", "", "", toAddress, "", "", nil, false)
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
