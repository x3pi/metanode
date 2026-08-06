package main

// eth_tx_converter.go — EthTx → MetaTx conversion embedded in the Master.
// Replaces the RPC client's processSendRawTransaction + BuildTransactionWithDeviceKeyFromEthTx
// by performing the conversion locally without any HTTP round-trips.

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"

	"google.golang.org/protobuf/proto"
)

// buildMetaTxFromEthTx converts an Ethereum-format signed transaction into a
// MetaNode TransactionWithDeviceKey protobuf. It does everything the old
// rpc-client's processSendRawTransaction + BuildTransactionWithDeviceKeyFromEthTx
// did, but without any network round-trip — account state is read directly from
// the in-process trie DB.
func buildMetaTxFromEthTx(
	ethTx *types.Transaction,
	chainID *big.Int,
	blsKeyPair *bls.KeyPair,
	stateRoot common.Hash,
	app *App,
) ([]byte, *mt_transaction.Transaction, error) {

	// 1. Derive sender from the Ethereum TX signature
	signer := types.LatestSignerForChainID(chainID)
	fromAddress, err := types.Sender(signer, ethTx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to derive sender: %w", err)
	}

	// 2. Get account state from local trie (using cache)
	accountStateTrie, err := app.GetAccountStateTrie(stateRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open account state trie: %w", err)
	}
	var bData []byte
	var errTrie error
	// Read directly from trie to avoid creating AccountStateDB and allocating 65k mutexes
	if lf, ok := accountStateTrie.(interface{ GetLockFree(key []byte) ([]byte, error) }); ok {
		bData, errTrie = lf.GetLockFree(fromAddress.Bytes())
	} else {
		bData, errTrie = accountStateTrie.Get(fromAddress.Bytes())
	}
	if errTrie != nil {
		return nil, nil, fmt.Errorf("failed to get account state from trie for %s: %w", fromAddress.Hex(), errTrie)
	}

	as := state.NewAccountState(fromAddress)
	if len(bData) > 0 {
		if err := as.Unmarshal(bData); err != nil {
			return nil, nil, fmt.Errorf("error unmarshalling %s from Trie: %w", fromAddress.Hex(), err)
		}
	}

	// 3. Verify BLS public key is registered on-chain (skip for account setting TX)
	if ethTx.To() == nil || *ethTx.To() != utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		if len(as.PublicKeyBls()) == 0 {
			return nil, nil, fmt.Errorf("account %s has no BLS public key registered on-chain", fromAddress.Hex())
		}
		// Use the pre-computed public key from blsKeyPair
		if !bytes.Equal(as.PublicKeyBls(), blsKeyPair.BytesPublicKey()) {
			return nil, nil, fmt.Errorf("registered BLS public key does not match the signing key for %s, expected: %s, got: %s", fromAddress.Hex(), hex.EncodeToString(as.PublicKeyBls()), hex.EncodeToString(blsKeyPair.BytesPublicKey()))
		}
	}

	// 4. Build device key
	deviceKey, err := app.stateProcessor.GetDeviceKey(as.LastHash())
	if err != nil {
		logger.Info("[ETH_TX_CONVERTER] device key lookup failed (non-fatal): %v", err)
	}

	rawNewDeviceKeyBytes := []byte(fmt.Sprintf("%s-%d",
		hex.EncodeToString(as.LastHash().Bytes()), time.Now().Unix()))
	rawNewDeviceKey := crypto.Keccak256(rawNewDeviceKeyBytes)
	newDeviceKey := crypto.Keccak256Hash(rawNewDeviceKey)

	// 5. Build MetaTx from EthTx
	metaTxIface, err := mt_transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build MetaTx from EthTx: %w", err)
	}
	metaTx, ok := metaTxIface.(*mt_transaction.Transaction)
	if !ok {
		return nil, nil, fmt.Errorf("unexpected transaction type from NewTransactionFromEth")
	}

	metaTx.UpdateDeriver(deviceKey, newDeviceKey)
	metaTx.SetSign(blsKeyPair.PrivateKey())

	// 6. Marshal as TransactionWithDeviceKey proto
	txWithDK := &mt_proto.TransactionWithDeviceKey{
		Transaction: metaTx.Proto().(*mt_proto.Transaction),
		DeviceKey:   rawNewDeviceKey,
	}

	data, err := proto.Marshal(txWithDK)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal TransactionWithDeviceKey: %w", err)
	}

	logger.Info("[ETH_TX_CONVERTER] Built MetaTx %s from EthTx, sender=%s",
		metaTx.Hash().Hex(), fromAddress.Hex())

	return data, metaTx, nil
}
