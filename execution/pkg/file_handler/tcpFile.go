package file_handler

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/tcp_trans"
	"github.com/meta-node-blockchain/meta-node/pkg/file_handler/abi_file"
	"github.com/meta-node-blockchain/meta-node/pkg/models/file_model"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

type TCPCommunicator struct {
	client *client_tcp.Client
	config *tcp_config.ClientConfig
}

func NewTCPCommunicator(c *client_tcp.Client, cfg *tcp_config.ClientConfig) *TCPCommunicator {
	return &TCPCommunicator{client: c, config: cfg}
}

func (comm *TCPCommunicator) GetFileInfo(fileKey [32]byte, tx types.Transaction) (*file_model.FileInfo, error) {
	fileInfo, err := tcp_trans.GetFileInfoTransaction(comm.client, comm.config, fileKey, tx)
	if err != nil {
		return nil, err
	}
	// Thêm logic ký (sign) fileKey từ V2
	fileKeyStr := hex.EncodeToString(fileKey[:])
	merkleRootStr := hex.EncodeToString(fileInfo.MerkleRoot[:])

	// Combine fileKey + merkleRoot for signing
	messageToSign := fileKeyStr + merkleRootStr
	messageBytes := []byte(messageToSign)
	hash := crypto.Keccak256Hash(
		[]byte(fmt.Sprintf("0x00")),
		messageBytes,
	)
	privateKey, err := crypto.HexToECDSA(comm.config.PkAdminFileStorage)
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

func (comm *TCPCommunicator) GetRustServerAddresses(tx types.Transaction) ([]string, error) {
	return tcp_trans.GetRustServerAddressesListTransaction(comm.client, comm.config, tx)
}

func (comm *TCPCommunicator) SendConfirmation(fileKey [32]byte, tx types.Transaction) (types.Receipt, error) {
	abi, err := abi.JSON(strings.NewReader(abi_file.FileABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %v", err)
	}
	inputData, err := abi.Pack("confirmFileActive", fileKey)
	if err != nil {
		return nil, fmt.Errorf("failed to pack confirmFileActive data: %v", err)
	}
	callData := transaction.NewCallData(inputData)
	bData, _ := callData.Marshal()
	ownerFile := common.HexToAddress(comm.config.OwnerFileStorageAddress)

	return tcp_trans.SendTransactionWithDeviceKey(
		comm.client,
		ownerFile,
		tx.ToAddress(),
		big.NewInt(0),
		bData,
		[]common.Address{},
		20000000,
		10000000,
		60,
	)
}
