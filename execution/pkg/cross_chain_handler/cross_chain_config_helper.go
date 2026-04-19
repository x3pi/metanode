package cross_chain_handler

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ═══════════════════════════════════════════════════════════════════════
// CROSS-CHAIN CONFIG HELPER
// Tách phần gọi contract CrossChainConfigRegistry ra file riêng.
// Pattern giống GetFileInfoTransaction trong file_handler_helper/file.go:
//   - Nhận originalTx → dùng FromAddress, GetChainID từ tx gốc
//   - Tạo read-only transaction gọi hàm view
//   - Unpack kết quả
// ═══════════════════════════════════════════════════════════════════════

// createReadOnlyTx tạo read-only transaction để gọi view function trên contract.
// Lấy FromAddress và ChainID từ originalTx (giống pattern GetFileInfoTransaction).
func createReadOnlyTx(originalTx types.Transaction, contractAddr common.Address, inputData []byte) types.Transaction {
	callData := transaction.NewCallData(inputData)
	bData, _ := callData.Marshal()

	tx := transaction.NewTransaction(
		originalTx.FromAddress(), // from = lấy từ tx gốc
		contractAddr,             // to = config contract
		big.NewInt(0),            // amount = 0 (view function)
		20000000,                 // max gas
		10000000,                 // max gas price
		60,                       // max time use
		bData,                    // encoded function call
		[][]byte{},               // related addresses
		common.Hash{},            // last device key
		common.Hash{},            // new device key
		0,                        // nonce
		originalTx.GetChainID(),  // chain ID = lấy từ tx gốc
	)
	tx.SetReadOnly(true)
	return tx
}

// FetchChainId gọi CrossChainConfigRegistry.chainId() off-chain
// Truyền originalTx để lấy FromAddress và ChainID.
func FetchChainId(
	configABI abi.ABI,
	tp OffChainProcessor,
	configContractAddr common.Address,
	originalTx types.Transaction,
) (*big.Int, error) {
	// Pack function call: chainId()
	inputData, err := configABI.Pack("chainId")
	if err != nil {
		return nil, fmt.Errorf("pack chainId error: %v", err)
	}

	tx := createReadOnlyTx(originalTx, configContractAddr, inputData)

	exeResult, err := tp.ProcessTransactionOffChain(tx)
	if err != nil {
		return nil, fmt.Errorf("off-chain call chainId() error: %v", err)
	}
	if exeResult == nil {
		return nil, fmt.Errorf("off-chain call chainId() returned nil result")
	}

	returnData := exeResult.Return()

	// Check revert
	revertError, errUnpack := abi.UnpackRevert(returnData)
	if errUnpack == nil {
		return nil, fmt.Errorf("chainId() reverted: %s", revertError)
	}

	// Unpack: uint256
	method, exists := configABI.Methods["chainId"]
	if !exists {
		return nil, fmt.Errorf("chainId method not found in config ABI")
	}
	results, err := method.Outputs.Unpack(returnData)
	if err != nil {
		return nil, fmt.Errorf("unpack chainId result error: %v", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("chainId() returned no values")
	}

	chainId, ok := results[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("chainId() result is not *big.Int, got %T", results[0])
	}
	return chainId, nil
}

// FetchRegisteredChainIds gọi CrossChainConfigRegistry.getRegisteredChainIds() off-chain
// Truyền originalTx để lấy FromAddress và ChainID.
func FetchRegisteredChainIds(
	configABI abi.ABI,
	tp OffChainProcessor,
	configContractAddr common.Address,
	originalTx types.Transaction,
) ([]*big.Int, error) {
	// Pack function call: getRegisteredChainIds()
	inputData, err := configABI.Pack("getRegisteredChainIds")
	if err != nil {
		return nil, fmt.Errorf("pack getRegisteredChainIds error: %v", err)
	}

	tx := createReadOnlyTx(originalTx, configContractAddr, inputData)

	exeResult, err := tp.ProcessTransactionOffChain(tx)
	if err != nil {
		return nil, fmt.Errorf("off-chain call getRegisteredChainIds() error: %v", err)
	}
	if exeResult == nil {
		return nil, fmt.Errorf("off-chain call getRegisteredChainIds() returned nil result")
	}

	returnData := exeResult.Return()

	// Check revert
	revertError, errUnpack := abi.UnpackRevert(returnData)
	if errUnpack == nil {
		return nil, fmt.Errorf("getRegisteredChainIds() reverted: %s", revertError)
	}

	// Unpack: uint256[]
	method, exists := configABI.Methods["getRegisteredChainIds"]
	if !exists {
		return nil, fmt.Errorf("getRegisteredChainIds method not found in config ABI")
	}
	results, err := method.Outputs.Unpack(returnData)
	if err != nil {
		return nil, fmt.Errorf("unpack getRegisteredChainIds result error: %v", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("getRegisteredChainIds() returned no values")
	}

	chainIds, ok := results[0].([]*big.Int)
	if !ok {
		return nil, fmt.Errorf("getRegisteredChainIds() result is not []*big.Int, got %T", results[0])
	}
	return chainIds, nil
}

// EmbassyInfo chứa BLS public key và ETH address của một embassy.
type EmbassyInfo struct {
	BlsPublicKey []byte
	EthAddress   common.Address
	IsActive     bool
}

// FetchAllEmbassies gọi CrossChainConfigRegistry.getAllEmbassies() off-chain.
// Trả về danh sách EmbassyInfo (chỉ những embassy isActive=true) và tất cả count.
func FetchAllEmbassies(
	configABI abi.ABI,
	tp OffChainProcessor,
	configContractAddr common.Address,
	originalTx types.Transaction,
) ([]EmbassyInfo, error) {
	// Pack function call: getAllEmbassies()
	inputData, err := configABI.Pack("getAllEmbassies")
	if err != nil {
		return nil, fmt.Errorf("pack getAllEmbassies error: %v", err)
	}

	tx := createReadOnlyTx(originalTx, configContractAddr, inputData)

	exeResult, err := tp.ProcessTransactionOffChain(tx)
	if err != nil {
		return nil, fmt.Errorf("off-chain call getAllEmbassies() error: %v", err)
	}
	if exeResult == nil {
		return nil, fmt.Errorf("off-chain call getAllEmbassies() returned nil result")
	}

	returnData := exeResult.Return()

	// Check revert
	revertError, errUnpack := abi.UnpackRevert(returnData)
	if errUnpack == nil {
		return nil, fmt.Errorf("getAllEmbassies() reverted: %s", revertError)
	}

	// Unpack: Embassy[] (tuple[])
	method, exists := configABI.Methods["getAllEmbassies"]
	if !exists {
		return nil, fmt.Errorf("getAllEmbassies method not found in config ABI")
	}
	results, err := method.Outputs.Unpack(returnData)
	if err != nil {
		return nil, fmt.Errorf("unpack getAllEmbassies result error: %v", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("getAllEmbassies() returned no values")
	}

	// ABI decoder trả về []struct{ BlsPublicKey []byte; EthAddress common.Address; IsActive bool; RegisteredAt *big.Int; Index *big.Int }
	// Go ABI decoder map tuple[] → []interface{} hoặc []struct{...}
	type embassyTuple struct {
		BlsPublicKey []byte         `abi:"blsPublicKey"`
		EthAddress   common.Address `abi:"ethAddress"`
		IsActive     bool           `abi:"isActive"`
		RegisteredAt *big.Int       `abi:"registeredAt"`
		Index        *big.Int       `abi:"index"`
	}

	// results[0] sẽ là []embassyTuple sau khi ABI unpack tuple[]
	raw := results[0]
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("getAllEmbassies() marshal error: %v", err)
	}

	var embassiesRaw []struct {
		BlsPublicKey []byte         `json:"blsPublicKey"`
		EthAddress   common.Address `json:"ethAddress"`
		IsActive     bool           `json:"isActive"`
		RegisteredAt *big.Int       `json:"registeredAt"`
		Index        *big.Int       `json:"index"`
	}

	if err := json.Unmarshal(b, &embassiesRaw); err != nil {
		return nil, fmt.Errorf("getAllEmbassies() unmarshal error: %v", err)
	}

	var infos []EmbassyInfo
	for _, e := range embassiesRaw {
		infos = append(infos, EmbassyInfo{
			BlsPublicKey: e.BlsPublicKey,
			EthAddress:   e.EthAddress,
			IsActive:     e.IsActive,
		})
		if e.IsActive {
			logger.Info("[CrossChain] Embassy loaded: eth=%s, blsLen=%d",
				e.EthAddress.Hex(), len(e.BlsPublicKey))
		}
	}
	return infos, nil
}

// ═══════════════════════════════════════════════════════════════════════
// GENERIC CONFIG VIEW CALL
// Dùng bởi vote_recovery.go và các module khác cần gọi view function
// trên config contract mà không cần viết từng hàm Fetch riêng.
// ═══════════════════════════════════════════════════════════════════════

// CallConfigView gọi view function trên config contract off-chain.
// Trả về kết quả đã unpack ([]interface{}).
//
// Ví dụ:
//
//	result, err := CallConfigView(ccHandler, chainState, tx, "getLocalScanBlockRange")
//	minBlock := result[0].(*big.Int)
//	maxBlock := result[1].(*big.Int)
func CallConfigView(
	ccHandler *CrossChainHandler,
	chainState *blockchain.ChainState,
	originalTx types.Transaction,
	methodName string,
	args ...interface{},
) ([]interface{}, error) {
	cfg := chainState.GetConfig()
	if cfg == nil {
		return nil, fmt.Errorf("CallConfigView: chainState config is nil")
	}

	configContractHex := cfg.CrossChain.ConfigContract
	if configContractHex == "" {
		return nil, fmt.Errorf("CallConfigView: config_contract not set")
	}
	configContractAddr := common.HexToAddress(configContractHex)

	// Pack function call
	inputData, err := ccHandler.configABI.Pack(methodName, args...)
	if err != nil {
		return nil, fmt.Errorf("CallConfigView: pack %s error: %v", methodName, err)
	}

	tx := createReadOnlyTx(originalTx, configContractAddr, inputData)

	if globalOffChainProcessor == nil {
		return nil, fmt.Errorf("CallConfigView: OffChainProcessor not set")
	}

	exeResult, err := globalOffChainProcessor.ProcessTransactionOffChain(tx)
	if err != nil {
		return nil, fmt.Errorf("CallConfigView: off-chain call %s() error: %v", methodName, err)
	}
	if exeResult == nil {
		return nil, fmt.Errorf("CallConfigView: off-chain call %s() returned nil", methodName)
	}

	returnData := exeResult.Return()

	// Check revert
	revertError, errUnpack := abi.UnpackRevert(returnData)
	if errUnpack == nil {
		return nil, fmt.Errorf("CallConfigView: %s() reverted: %s", methodName, revertError)
	}

	// Unpack result
	method, exists := ccHandler.configABI.Methods[methodName]
	if !exists {
		return nil, fmt.Errorf("CallConfigView: method %s not found in config ABI", methodName)
	}
	results, err := method.Outputs.Unpack(returnData)
	if err != nil {
		return nil, fmt.Errorf("CallConfigView: unpack %s result error: %v", methodName, err)
	}

	return results, nil
}
