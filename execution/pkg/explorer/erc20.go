// explorer/erc20.go
package explorer

import (
	"bytes"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Method IDs for ERC20 transfer and transferFrom functions
var (
	// Method ID của hàm transfer(address,uint256)
	TransferMethodID = crypto.Keccak256([]byte("transfer(address,uint256)"))[:4]
	// Method ID của hàm transferFrom(address,address,uint256)
	TransferFromMethodID = crypto.Keccak256([]byte("transferFrom(address,address,uint256)"))[:4]
)

// ERC20TransferData chứa dữ liệu đã được phân tích từ một calldata của giao dịch token.
type ERC20TransferData struct {
	From  common.Address
	To    common.Address
	Value *big.Int
}

// ParseERC20Transfer cố gắng phân tích dữ liệu giao dịch ERC20 từ input.
// Hàm này hỗ trợ `transfer(address,uint256)` và `transferFrom(address,address,uint256)`.
// `txFrom` là địa chỉ người gửi của giao dịch gốc, được dùng làm người gửi token trong lệnh `transfer`.
func ParseERC20Transfer(txFrom common.Address, callData []byte) (*ERC20TransferData, bool) {
	if len(callData) < 4 {
		return nil, false
	}

	methodID := callData[:4]
	data := callData[4:]

	// Kiểm tra hàm `transfer(address,uint256)`
	if bytes.Equal(methodID, TransferMethodID) {
		if len(data) != 64 {
			return nil, false
		}
		to := common.BytesToAddress(data[12:32])
		value := new(big.Int).SetBytes(data[32:64])
		return &ERC20TransferData{
			From:  txFrom, // Trong lệnh transfer chuẩn, người gửi token chính là người tạo giao dịch
			To:    to,
			Value: value,
		}, true
	}

	// Kiểm tra hàm `transferFrom(address,address,uint256)`
	if bytes.Equal(methodID, TransferFromMethodID) {
		if len(data) != 96 {
			return nil, false
		}
		from := common.BytesToAddress(data[12:32])
		to := common.BytesToAddress(data[44:64]) // Địa chỉ `to` nằm sau địa chỉ `from` 32 bytes
		value := new(big.Int).SetBytes(data[64:96])
		return &ERC20TransferData{
			From:  from,
			To:    to,
			Value: value,
		}, true
	}

	return nil, false
}
