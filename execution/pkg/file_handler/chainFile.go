package file_handler

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	com "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/file_handler/abi_file"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	file_model "github.com/meta-node-blockchain/meta-node/pkg/models/file_model"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils/file_handler_helper"
	"github.com/meta-node-blockchain/meta-node/types"
)

type InProcessCommunicator struct {
	tp             tx_processor.OffChainProcessor
	storageManager *storage.StorageManager
	chainState     *blockchain.ChainState
	abi            abi.ABI
}

// NewInProcessCommunicator tạo một communicator mới chạy in-process.
func NewInProcessCommunicator(tp tx_processor.OffChainProcessor, sm *storage.StorageManager, cs *blockchain.ChainState, abi abi.ABI) *InProcessCommunicator {
	return &InProcessCommunicator{tp: tp, storageManager: sm, chainState: cs, abi: abi}
}

func (comm *InProcessCommunicator) GetFileInfo(fileKey [32]byte, tx types.Transaction) (*file_model.FileInfo, error) {
	fileInfoTx, err := file_handler_helper.GetFileInfoTransaction(fileKey, tx)
	if err != nil {
		return nil, fmt.Errorf("Lỗi khi tạo transaction getFileInfo: %v", err)
	}
	exeResult, err := comm.tp.ProcessTransactionOffChain(fileInfoTx)
	if err != nil {
		return nil, fmt.Errorf("Lỗi khi xử lý giao dịch off-chain: %v", err)
	}
	if exeResult == nil {
		return nil, fmt.Errorf("Kết quả thực thi là nil")
	}
	returnData := exeResult.Return()
	revertError, errUnpack := abi.UnpackRevert(returnData)
	if errUnpack == nil {
		return nil, fmt.Errorf("Giao dịch bị revert từ smart contract: %s", revertError)
	}
	fileInfo, err := file_handler_helper.ParseFileInfoFromResult(exeResult.Return())
	if err != nil {
		return nil, fmt.Errorf("Lỗi khi parse file info: %v", err)
	}
	fileKeyStr := hex.EncodeToString(fileKey[:])
	merkleRootStr := hex.EncodeToString(fileInfo.MerkleRoot[:])
	// Combine fileKey + merkleRoot for signing
	messageToSign := fileKeyStr + merkleRootStr
	messageBytes := []byte(messageToSign)
	hash := crypto.Keccak256Hash(
		[]byte(fmt.Sprintf("0x00")),
		messageBytes,
	)
	privateKey, err := crypto.HexToECDSA(comm.chainState.GetConfig().PkAdminFileStorage)
	if err != nil {
		return nil, fmt.Errorf("failed to sign fileKey: %v", err)
	}
	signatureBytes, err := crypto.Sign(hash.Bytes(), privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign fileKey: %v", err)
	}
	fileInfo.Signature = hex.EncodeToString(signatureBytes)
	return fileInfo, nil
}
func (comm *InProcessCommunicator) GetRustServerAddresses(tx types.Transaction) ([]string, error) {
	listTx, err := GetRustServerAddressesListTransaction(tx)
	if err != nil {
		return nil, fmt.Errorf("lỗi tạo tx getList: %v", err)
	}
	exeResultList, err := comm.tp.ProcessTransactionOffChain(listTx)
	if err != nil {
		return nil, fmt.Errorf("lỗi off-chain call getList: %v", err)
	}
	servers, err := ParseRustServerAddressesListResult(exeResultList.Return())
	if err != nil {
		return nil, fmt.Errorf("lỗi parse getList: %v", err)
	}
	if len(servers) < 2 {
		return nil, fmt.Errorf("lỗi: Contract trả về ít hơn 2 server (có %d)", len(servers))
	}
	return servers, nil
}

func GetRustServerAddressesListTransaction(originalTx types.Transaction) (types.Transaction, error) {
	parsedABI, err := abi.JSON(strings.NewReader(abi_file.FileABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %v", err)
	}
	inputData, err := parsedABI.Pack("getRustServerAddresses")
	if err != nil {
		return nil, fmt.Errorf("failed to pack getRustServerAddresses data: %v", err)
	}
	callData := transaction.NewCallData(inputData)
	bData, _ := callData.Marshal()
	newTx := transaction.NewTransaction(
		common.Address{},
		originalTx.ToAddress(),
		big.NewInt(0),
		20000000,
		10000000,
		60,
		bData,
		[][]byte{},
		common.Hash{},
		common.Hash{},
		0,
		originalTx.GetChainID(),
	)
	newTx.SetReadOnly(true)
	return newTx, nil
}

// ParseRustServerAddressesListResult parse kết quả trả về từ getRustServerAddresses
func ParseRustServerAddressesListResult(returnData []byte) ([]string, error) {
	if len(returnData) == 0 {
		return nil, fmt.Errorf("return data is empty")
	}

	parsedABI, err := abi.JSON(strings.NewReader(abi_file.FileABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %v", err)
	}

	method, exists := parsedABI.Methods["getRustServerAddresses"]
	if !exists {
		return nil, fmt.Errorf("getRustServerAddresses method not found in ABI")
	}
	results, err := method.Outputs.Unpack(returnData)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack results: %v", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no return values")
	}

	serverAddresses, ok := results[0].([]string)
	if !ok {
		logger.Error("Không thể cast trực tiếp. Type của results[0]: %T", results[0])
		return nil, fmt.Errorf("cannot cast result to []string, got: %T", results[0])
	}
	return serverAddresses, nil
}

func (comm *InProcessCommunicator) SendConfirmation(fileKey [32]byte, tx types.Transaction) (types.Receipt, error) {
	inputData, err := comm.abi.Pack("confirmFileActive", fileKey)
	if err != nil {
		return nil, fmt.Errorf("failed to pack confirmFileActive data: %v", err)
	}
	callData := transaction.NewCallData(inputData)
	bData, _ := callData.Marshal()
	ownerFile := common.HexToAddress(comm.chainState.GetConfig().OwnerFileStorageAddress)
	lastHash, err := comm.chainState.GetAccountStateDB().GetLastHash(ownerFile)
	if err != nil {
		return nil, fmt.Errorf("failed to get last hash: %v", err)
	}
	acc, _ := comm.chainState.GetAccountStateDB().AccountState(ownerFile)
	deviceKey, err := comm.tp.GetDeviceKey(lastHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get device key: %v", err)
	}
	rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d", hex.EncodeToString(lastHash[:]), time.Now().Unix()))
	rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)
	newDeviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

	newTx := transaction.NewTransaction(
		ownerFile,
		tx.ToAddress(),
		big.NewInt(0),
		20000000,
		10000000,
		60,
		bData,
		[][]byte{},
		deviceKey,
		newDeviceKey,
		acc.Nonce(),
		tx.GetChainID(),
	)
	blsBytes, err := hex.DecodeString(comm.chainState.GetConfig().BlsAdminStorage)
	if err != nil {
		return nil, fmt.Errorf("failed to decode private key hex: %v", err)
	}
	var privateKey com.PrivateKey
	copy(privateKey[:], blsBytes)

	newTx.SetSign(privateKey)
	err = comm.tp.ProcessTransactionOnChainWithDeviceKey(newTx, rawNewDeviceKey)
	if err != nil {
		return nil, fmt.Errorf("failed to process transaction on chain: %v", err)
	}
	// chờ receipt
	startTime := time.Now()
	timeout := 30 * time.Second
	pollInterval := 80 * time.Millisecond
	for {
		time.Sleep(pollInterval)
		if time.Since(startTime) > timeout {
			return nil, fmt.Errorf("timeout: failed to get receipt for tx %s after 30 seconds", newTx.Hash().Hex())
		}
		blockNumber, ok := blockchain.GetBlockChainInstance().GetBlockNumberByTxHash(newTx.Hash())
		if !ok {
			continue
		}
		blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
		if !ok {
			continue
		}
		blockData, err := comm.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
		if err != nil {
			continue
		}
		rcpDb, err := receipt.NewReceiptsFromRoot(blockData.Header().ReceiptRoot(), comm.storageManager.GetStorageReceipt())
		if err != nil {
			continue
		}
		receipt, err := rcpDb.GetReceipt(newTx.Hash())
		if err != nil {
			return nil, fmt.Errorf("failed to get receipt: %v", err)
		}
		return receipt, nil
	}
}
