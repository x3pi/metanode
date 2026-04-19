// Package revertparser cung cấp chức năng để phân tích (parse)
// chuỗi lý do revert được mã hóa theo chuẩn ABI Error(string) của Ethereum.
package processor

import (
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

func RevertParser(hexStr string) (string, error) {
	if len(hexStr) >= 2 && hexStr[:2] == "0x" {
		hexStr = hexStr[2:]
	}

	data, err := hex.DecodeString(hexStr)
	if err != nil {
		logger.Error(hexStr)
		return "", fmt.Errorf("decode hex lỗi: %v", err)
	}

	if len(data) < 4+32+32 {
		logger.Error(hexStr)
		return "", fmt.Errorf("dữ liệu không đủ dài")
	}

	data = data[4+32:] // Bỏ 4 bytes selector + 32 bytes offset

	if len(data) < 32 {
		logger.Error(hexStr)
		return "", fmt.Errorf("dữ liệu không đủ để đọc độ dài chuỗi")
	}

	strLen := new(big.Int).SetBytes(data[:32]).Int64()
	if strLen < 0 {
		logger.Error(hexStr)
		return "", fmt.Errorf("strLen âm, dữ liệu không hợp lệ")
	}
	if int64(len(data)) < 32+strLen {
		logger.Error(hexStr)
		return "", fmt.Errorf("dữ liệu không đủ dài để chứa thông điệp revert")
	}

	revertBytes := data[32 : 32+strLen]
	return string(revertBytes), nil
}
