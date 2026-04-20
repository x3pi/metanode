package rpc_client

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	cfgCom "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/config"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/connection_manager/connection_client"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"google.golang.org/protobuf/proto"
)

const tcpTimeout = 60 * time.Second

// GetAccountStateTCP lấy AccountState qua TCP thay vì HTTP.
func (c *ClientRPC) GetAccountStateTCP(address common.Address, chainClient *connection_client.ConnectionClient) (mt_types.AccountState, error) {
	asBytes, err := chainClient.GetAccountState(address.Bytes(), tcpTimeout)
	if err != nil {
		return nil, fmt.Errorf("GetAccountStateTCP error: %w", err)
	}

	pbAS := &mt_proto.AccountState{}
	if err := proto.Unmarshal(asBytes, pbAS); err != nil {
		return nil, fmt.Errorf("GetAccountStateTCP unmarshal error: %w", err)
	}

	jsonAS := &state.JsonAccountState{
		Address:        common.BytesToAddress(pbAS.Address).Hex(),
		Balance:        new(big.Int).SetBytes(pbAS.Balance).String(),
		PendingBalance: new(big.Int).SetBytes(pbAS.PendingBalance).String(),
		LastHash:       common.BytesToHash(pbAS.LastHash).Hex(),
		DeviceKey:      common.BytesToHash(pbAS.DeviceKey).Hex(),
		Nonce:          binary.BigEndian.Uint64(pbAS.Nonce),
		PublicKeyBls:   hex.EncodeToString(pbAS.PublicKeyBls),
		AccountType:    int32(pbAS.AccountType),
	}
	return jsonAS.ToAccountState(), nil
}

// GetDeviceKeyTCP lấy DeviceKey qua TCP thay vì HTTP.
// Chain trả về hashBytes + deviceKeyBytes (concatenated).
// Logic parse giống handleDeviceKey trong handler.go:
//   - len == 64: data[:32] = transactionHash, data[32:] = deviceKey
//   - len == 32: data[:32] = transactionHash, deviceKey = zero hash
func (c *ClientRPC) GetDeviceKeyTCP(hash common.Hash, chainClient *connection_client.ConnectionClient) (common.Hash, error) {
	data, err := chainClient.GetDeviceKey(hash.Bytes(), tcpTimeout)
	if err != nil {
		return common.Hash{}, fmt.Errorf("GetDeviceKeyTCP error: %w", err)
	}

	if len(data) != 64 && len(data) != 32 {
		return common.Hash{}, fmt.Errorf("GetDeviceKeyTCP: unexpected data length: %d", len(data))
	}

	if len(data) == 32 {
		// Chỉ có transactionHash, không có deviceKey → trả về zero hash
		return common.Hash{}, nil
	}
	// len == 64: data[32:] = deviceKey
	return common.BytesToHash(data[32:]), nil
}

// BuildTransactionWithDeviceKeyFromEthTxTCP giống BuildTransactionWithDeviceKeyFromEthTx
// nhưng sử dụng TCP (ConnectionClient) thay vì HTTP cho GetAccountState, GetDeviceKey,
// BuildTransferTransaction, SendRawTransactionBinary.
// topUpFunc (optional): được inject từ caller để đưa giao dịch chuyển tiền vào hàng chờ
// (tránh nonce conflict khi gửi trực tiếp). Nếu nil thì bỏ qua bước top-up.
func (c *ClientRPC) BuildTransactionWithDeviceKeyFromEthTxTCP(
	ethTx *types.Transaction,
	cfg *config.ClientConfig,
	cfgCom *cfgCom.Config,
	ldbContractFree *storage.ContractFreeGasStorage,
	isSetNonce bool,
	chainClient *connection_client.ConnectionClient,
	topUpFunc func(toAddress common.Address) error,
) ([]byte, mt_types.Transaction, func(), error) {
	sg := types.NewCancunSigner(ethTx.ChainId())
	fromAddress, err := sg.Sender(ethTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("lỗi khi get fromAddress: %w", err)
	}

	// Dùng TCP thay vì HTTP
	as, err := c.GetAccountStateTCP(fromAddress, chainClient)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("BuildTransactionWithDeviceKeyFromEthTxTCP lỗi khi get account state %v: %v", fromAddress, err)
	}

	if ethTx.To() == nil || *ethTx.To() != utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		if len(as.PublicKeyBls()) == 0 {
			return nil, nil, nil, fmt.Errorf("lỗi tài khoản chưa đăng ký public key bls trên chain")
		}
		if !bytes.Equal(as.PublicKeyBls(), c.KeyPair.BytesPublicKey()) {
			logger.Info("lỗi tài khoản chưa đăng ký private key bls với rpc: %x orgiginal %x", as.PublicKeyBls(), c.KeyPair.BytesPublicKey())
			return nil, nil, nil, fmt.Errorf("lỗi tài khoản chưa đăng ký private key bls với rpc")
		}
	}

	if cfg == nil {
		return nil, nil, nil, fmt.Errorf("cfg is nil")
	}
	// Chỉ check free gas khi tài khoản cần được top-up (balance thấp, đã có lịch sử giao dịch)
	if !cfgCom.DisableFreeGas && ethTx.To() != nil && as.Balance().Cmp(cfgCom.GetFreeGasMinBalance()) < 0 && as.Nonce() != 0 {
		exist, _ := ldbContractFree.HasContract(*ethTx.To())
		if exist && topUpFunc != nil {
			// Đưa vào hàng chờ owner để tránh nonce conflict
			if err := topUpFunc(fromAddress); err != nil {
				return nil, nil, nil, fmt.Errorf("topUpFunc failed: %v", err)
			}
		}
	}

	// Dùng TCP thay vì HTTP
	deviceKey, err := c.GetDeviceKeyTCP(as.LastHash(), chainClient)
	if err != nil {
		logger.Info("lỗi khi get deviceKey via TCP", err)
	}

	rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d", hex.EncodeToString(as.LastHash().Bytes()), time.Now().Unix()))
	rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)
	newDeviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

	bRelatedAddresses := make([][]byte, 0)
	transaction, err := mt_transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("error build NewTransactionFromEth: %w", err)
	}
	if isSetNonce {
		transaction.SetNonce(as.Nonce())
	}
	transaction.UpdateRelatedAddresses(bRelatedAddresses)
	transaction.UpdateDeriver(deviceKey, newDeviceKey)
	transaction.SetSign(c.KeyPair.PrivateKey())

	// Create TransactionWithDeviceKey
	transactionWithDeviceKey := &mt_proto.TransactionWithDeviceKey{
		Transaction: transaction.Proto().(*mt_proto.Transaction),
		DeviceKey:   rawNewDeviceKey,
	}

	data, release, err := marshalProtoMessage(transactionWithDeviceKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal TransactionWithDeviceKey: %w", err)
	}
	return data, transaction, release, err
}

// BuildTransferTransactionTCP tạo giao dịch chuyển tiền qua TCP.
func (c *ClientRPC) BuildTransferTransactionTCP(
	fromAddress common.Address,
	toAddress common.Address,
	amount *big.Int,
	chainClient *connection_client.ConnectionClient,
) ([]byte, mt_types.Transaction, func(), error) {
	// Lấy account state qua TCP
	as, err := c.GetAccountStateTCP(fromAddress, chainClient)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("BuildTransferTransactionTCP lỗi khi get account state: %v", err)
	}

	if as.Balance().Cmp(amount) < 0 {
		return nil, nil, nil, fmt.Errorf("số dư không đủ: có %s, cần %s", as.Balance().String(), amount.String())
	}
	if len(as.PublicKeyBls()) == 0 {
		return nil, nil, nil, fmt.Errorf("tài khoản chưa đăng ký public key BLS")
	}
	if !bytes.Equal(as.PublicKeyBls(), c.KeyPair.BytesPublicKey()) {
		return nil, nil, nil, fmt.Errorf("private key BLS không khớp với account")
	}

	// Lấy device key qua TCP
	deviceKey, err := c.GetDeviceKeyTCP(as.LastHash(), chainClient)
	if err != nil {
		logger.Info("lỗi khi get deviceKey via TCP", err)
	}

	rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d", hex.EncodeToString(as.LastHash().Bytes()), time.Now().Unix()))
	rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)
	newDeviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

	maxGas := uint64(21000)
	maxGasPrice := uint64(mt_common.MINIMUM_BASE_FEE)
	bRelatedAddresses := make([][]byte, 0)
	var bData []byte

	txx := mt_transaction.NewTransaction(
		fromAddress,
		toAddress,
		amount,
		maxGas,
		maxGasPrice,
		600,
		bData,
		bRelatedAddresses,
		deviceKey,
		newDeviceKey,
		as.Nonce(),
		c.ChainId.Uint64(),
	)

	txx.SetSign(c.KeyPair.PrivateKey())

	transactionWithDeviceKey := &mt_proto.TransactionWithDeviceKey{
		Transaction: txx.Proto().(*mt_proto.Transaction),
		DeviceKey:   rawNewDeviceKey,
	}

	data, release, err := marshalProtoMessage(transactionWithDeviceKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal TransactionWithDeviceKey: %w", err)
	}

	return data, txx, release, nil
}

// BuildTransactionWithDeviceKeyFromEthTxAndBlsPrivateKeyTCP is the TCP variant
func (c *ClientRPC) BuildTransactionWithDeviceKeyFromEthTxAndBlsPrivateKeyTCP(
	ethTx *types.Transaction,
	cfg *config.ClientConfig,
	cfgCom *cfgCom.Config,
	ldbContractFree *storage.ContractFreeGasStorage,
	private mt_common.PrivateKey,
	chainClient *connection_client.ConnectionClient,
	topUpFunc func(toAddress common.Address) error,
) ([]byte, mt_types.Transaction, func(), error) {

	sg := types.NewCancunSigner(ethTx.ChainId())
	fromAddress, err := sg.Sender(ethTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("lỗi khi get fromAddress : %w", err)
	}
	as, err := c.GetAccountStateTCP(fromAddress, chainClient)

	if err != nil {
		return nil, nil, nil, fmt.Errorf("BuildTransactionWithDeviceKeyFromEthTxAndBlsPrivateKeyTCP lỗi khi get acccount state: %v", err)
	}
	if cfg == nil {
		return nil, nil, nil, fmt.Errorf("cfg is nil")
	}
	// Chỉ check free gas khi tài khoản cần được top-up (balance thấp, đã có lịch sử giao dịch)
	// if !cfgCom.DisableFreeGas && ethTx.To() != nil && as.Balance().Cmp(cfgCom.GetFreeGasMinBalance()) < 0 && as.Nonce() != 0 {
	// 	exist, err := ldbContractFree.HasContract(*ethTx.To())
	// 	if err != nil {
	// 		return nil, nil, nil, fmt.Errorf("lỗi khi kiểm tra contract free gas: %v", err)
	// 	}
	// 	if exist && topUpFunc != nil {
	// 		// Đưa vào hàng chờ owner để tránh nonce conflict
	// 		if err := topUpFunc(fromAddress); err != nil {
	// 			return nil, nil, nil, fmt.Errorf("topUpFunc failed: %v", err)
	// 		}
	// 	}
	// }
	deviceKey, err := c.GetDeviceKeyTCP(as.LastHash(), chainClient)
	if err != nil {
		logger.Info("lỗi khi get deviceKey via TCP", err)
	}

	rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d", hex.EncodeToString(as.LastHash().Bytes()), time.Now().Unix()))

	rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)

	newDeviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

	bRelatedAddresses := make([][]byte, 0)

	transaction, err := mt_transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("error buidl  NewTransactionFromEth: %w", err)
	}
	transaction.UpdateRelatedAddresses(bRelatedAddresses)
	transaction.UpdateDeriver(deviceKey, newDeviceKey)
	// Cập nhật nonce từ account state
	transaction.SetNonce(as.Nonce())
	transaction.SetSign(private)
	// Create TransactionWithDeviceKey
	transactionWithDeviceKey := &mt_proto.TransactionWithDeviceKey{
		Transaction: transaction.Proto().(*mt_proto.Transaction),
		DeviceKey:   rawNewDeviceKey,
	}

	data, release, err := marshalProtoMessage(transactionWithDeviceKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal TransactionWithDeviceKey: %w", err)
	}
	return data, transaction, release, nil
}
