package account_handler

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/meta-node-blockchain/meta-node/pkg/file_handler/abi_file"
	"github.com/meta-node-blockchain/meta-node/types"
)

type AccountAbi struct {
	abi abi.ABI
}

var (
	accountAbi  *AccountAbi
	onceFileAbi sync.Once
)

func GetFileAbi() (*AccountAbi, error) {
	var err error
	onceFileAbi.Do(func() {
		var parsedABI abi.ABI
		parsedABI, err = abi.JSON(strings.NewReader(abi_file.FileABI))
		if err != nil {
			return
		}
		accountAbi = &AccountAbi{
			abi: parsedABI,
		}
	})
	if err != nil {
		return nil, err
	}
	return accountAbi, nil
}

func (h *AccountAbi) ParseMethodName(tx types.Transaction) (string, error) {
	inputData := tx.CallData().Input()
	if len(inputData) < 4 {
		err := fmt.Errorf("dữ liệu input không hợp lệ")
		return "", err
	}
	method, err := h.abi.MethodById(inputData[:4])
	if err != nil {
		return "", fmt.Errorf("không tìm thấy method trong ABI: %v", err)
	}
	return method.Name, nil
}
