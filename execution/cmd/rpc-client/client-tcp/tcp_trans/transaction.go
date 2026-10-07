package tcp_trans

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	ethCom "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/abi_file"
	"github.com/meta-node-blockchain/meta-node/pkg/models/file_model"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils/file_handler_helper"
	"github.com/meta-node-blockchain/meta-node/types"
)

func GetFileInfoTransaction(c *client.Client, config *tcp_config.ClientConfig, fileKey [32]byte, originalTx types.Transaction) (*file_model.FileInfo, error) {
	maxGas := uint64(20000000) // Consider making these configurable
	maxGasPrice := uint64(10000000)
	parsedABI, err := abi.JSON(strings.NewReader(abi_file.FileABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %v", err)
	}
	inputData, err := parsedABI.Pack("getFileInfo", fileKey)
	callData := transaction.NewCallData(inputData)
	bData, _ := callData.Marshal()
	receipt, err := c.ReadTransaction(originalTx.FromAddress(), originalTx.ToAddress(), big.NewInt(0), bData, []ethCom.Address{}, maxGas, maxGasPrice, 60)
	if err != nil {
		return nil, fmt.Errorf("failed to get fileInfo: %v", err)
	}
	var returnData []byte = receipt.Return()
	if len(returnData) == 0 {
		return nil, fmt.Errorf("return data is empty")
	}
	fileInfo, err := file_handler_helper.ParseFileInfoFromResult(returnData)
	if err != nil {
		return nil, fmt.Errorf("lỗi parse fileInfo: %v", err)
	}
	return fileInfo, nil
}
func GetRustServerAddressesListTransaction(c *client.Client, config *tcp_config.ClientConfig, originalTx types.Transaction) ([]string, error) {
	maxGas := uint64(20000000) // Consider making these configurable
	maxGasPrice := uint64(10000000)
	parsedABI, err := abi.JSON(strings.NewReader(abi_file.FileABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %v", err)
	}
	inputData, err := parsedABI.Pack("getRustServerAddresses")
	if err != nil {
		return nil, fmt.Errorf("failed to pack getRustServerAddresses data: %v", err)
	}
	callData := transaction.NewCallData(inputData)
	bData, err := callData.Marshal()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal call data: %v", err)
	}
	receipt, err := c.ReadTransaction(originalTx.FromAddress(), originalTx.ToAddress(), big.NewInt(0), bData, []ethCom.Address{}, maxGas, maxGasPrice, 60)
	if err != nil {
		return nil, fmt.Errorf("failed to get Rust server addresses: %v", err)
	}
	var returnData []byte = receipt.Return()
	if len(returnData) == 0 {
		return nil, fmt.Errorf("return data is empty")
	}
	method, exists := parsedABI.Methods["getRustServerAddresses"]
	if !exists {
		return nil, fmt.Errorf("failed to unpack results: %v", err)
	}
	results, err := method.Outputs.Unpack(returnData)
	if len(results) == 0 {
		return nil, fmt.Errorf("no return values")
	}
	serverAddresses, ok := results[0].([]string)
	if !ok {
		return nil, fmt.Errorf("cannot cast result to []string, got: %T", results[0])
	}
	return serverAddresses, nil
}


