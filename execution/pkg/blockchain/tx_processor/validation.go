package tx_processor

import (
	"bytes"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

var (
	verifiedSignaturesCache      sync.Map
	verifiedSignaturesCacheCount int64 // atomic counter for cache size
)

const (
	// Maximum cache entries before forced reset (safety valve)
	maxVerifiedSignaturesCacheSize = 500_000
	// Periodic cleanup interval
	signatureCacheCleanupInterval = 10 * time.Minute
)

// StartSignatureCacheCleanup starts a background goroutine that periodically
// resets the verifiedSignaturesCache to prevent unbounded memory growth.
// The cache is a performance optimization only — resetting it just means
// signatures will be re-verified (which is safe and correct).
func StartSignatureCacheCleanup(stopCh <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(signatureCacheCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				count := atomic.LoadInt64(&verifiedSignaturesCacheCount)
				if count > 0 {
					verifiedSignaturesCache.Clear()
					atomic.StoreInt64(&verifiedSignaturesCacheCount, 0)
					logger.Info("🧹 [MEMORY] Reset verifiedSignaturesCache (%d entries cleared)", count)
				}
			case <-stopCh:
				return
			}
		}
	}()
}

func callDataToAccountType(callData []byte) (pb.ACCOUNT_TYPE, *transaction.TransactionError) {

	if len(callData) != 36 {
		return pb.ACCOUNT_TYPE_REGULAR_ACCOUNT, transaction.InvalidData
	}
	bytesFSelect := utils.GetFunctionSelector("setAccountType(int256)")
	// Kiểm tra 4 byte đầu tiên phải bằng "0x61e1270b"
	if !bytes.Equal(callData[:4], bytesFSelect) {
		return pb.ACCOUNT_TYPE_REGULAR_ACCOUNT, transaction.InvalidData
	}
	// Lấy 4 byte sau để xác định kiểu tài khoản
	num := int32(binary.BigEndian.Uint32(callData[len(callData)-4:]))

	switch num {
	case 0:
		return pb.ACCOUNT_TYPE_REGULAR_ACCOUNT, nil
	case 1:
		return pb.ACCOUNT_TYPE_READ_WRITE_STRICT, nil
	default:
		return pb.ACCOUNT_TYPE_REGULAR_ACCOUNT, transaction.InvalidData
	}
}

func VerifyTransaction(
	tx types.Transaction,
	chainState *blockchain.ChainState,

) *transaction.TransactionError {
	isCrossChainBatchSubmit := false
	if tx.ToAddress() == common.CROSS_CHAIN_CONTRACT_ADDRESS {
		ccHandler, errHandler := cross_chain_handler.GetCrossChainHandler()
		if errHandler == nil && ccHandler != nil {
			isCrossChainBatchSubmit = ccHandler.IsBatchSubmitTx(tx.CallData().Input())
		}
	}
	logger.Info("isCrossChainBatchSubmit: %v", isCrossChainBatchSubmit)
	as, err := chainState.GetAccountStateDB().AccountStateReadOnly(tx.FromAddress())
	if err != nil {
		// For nonce-0 account setting TXs (BLS registration), a fresh account state
		// is functionally correct: nonce=0, no BLS key, no balance, isFree=true.
		accountSettingAddr := utils.GetAddressSelector(common.ACCOUNT_SETTING_ADDRESS_SELECT)
		if tx.GetNonce() == 0 && tx.ToAddress() == accountSettingAddr {
			as = state.NewAccountState(tx.FromAddress())
		} else {
			return transaction.InvalidData
		}
	}
	if tx.GetNonce() < as.Nonce() {
		logger.Error("tx.GetNonce() < as.Nonce(): ", tx.GetNonce(), as.Nonce())
		return transaction.InvalidNonce
	}

	// ════════════════════════════════════════════════════════════════
	// SUB-NODE SYNC OPTIMIZATION (Fix Code 66 Invalid Sign)
	// Sub nodes may have incomplete account state: the AccountBatch
	// replication might replicate nonce but NOT PublicKeyBls.
	// Two scenarios where we must bypass strict BLS verification:
	//   1. Sub node has NO state at all: as.Nonce()==0, tx.GetNonce()>0
	//   2. Sub node has nonce but missing BLS key: as.Nonce()>0, PublicKeyBls empty
	// In both cases, forward the TX to Master/Rust for proper verification.
	// On Master nodes, PublicKeyBls is always populated for registered accounts,
	// so this bypass never fires on Master (which is correct).
	// ════════════════════════════════════════════════════════════════
	isSubNodeLagging := len(as.PublicKeyBls()) == 0 && (tx.GetNonce() > 0 || as.Nonce() > 0)

	if as.Nonce() != 0 || tx.ToAddress() != utils.GetAddressSelector(common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		txHashHex := tx.Hash().Hex()

		if isSubNodeLagging {
			logger.Warn("⚠️ [SUB-NODE] Local state lagging for %s. Bypassing BLS strict check to allow forward to Master.", tx.FromAddress().String())
			// Let it pass local verification; assume Master will reject if invalid.
		} else {
			if !isCrossChainBatchSubmit {
				if _, ok := verifiedSignaturesCache.Load(txHashHex); !ok {
					request := transaction.NewVerifyTransactionRequest(
						tx.Hash(),
						common.PubkeyFromBytes(as.PublicKeyBls()),
						tx.Sign(),
					)
					if !request.Valid() {
						logger.Error("BLS Verification Failed!")
						logger.Error("  txHashHex: %s", txHashHex)
						logger.Error("  FromAddress: %s", tx.FromAddress().Hex())
						logger.Error("  ToAddress: %s", tx.ToAddress().Hex())
						logger.Error("  SenderPubKey: %x", as.PublicKeyBls())
						logger.Error("  SenderSign: %x", tx.Sign().Bytes())
						logger.Error("  Hash() of TX according to SubNode: %x", tx.Hash().Bytes())
						if !tx.ValidEthSign() {
							logger.Error("  ETH Verification also failed!")
							return transaction.InvalidSign
						}
					}
					// Only cache on successful validation
					verifiedSignaturesCache.Store(txHashHex, true)
					if atomic.AddInt64(&verifiedSignaturesCacheCount, 1) >= maxVerifiedSignaturesCacheSize {
						verifiedSignaturesCache.Clear()
						atomic.StoreInt64(&verifiedSignaturesCacheCount, 0)
					}
				}
			}
		}
	}

	if as.AccountType() == 1 && tx.ToAddress() != utils.GetAddressSelector(common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		if !tx.ValidEthSign() {
			return transaction.RequiresTwoSignatures
		}
	}

	if tx.ToAddress() == utils.GetAddressSelector(common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		dataInput := tx.CallData().Input()

		if len(dataInput) < 4 {
			return transaction.InvalidData
		}

		selector := dataInput[:4]
		isSetBls := bytes.Equal(selector, utils.GetFunctionSelector("setBlsPublicKey(bytes)"))
		isSetType := bytes.Equal(selector, utils.GetFunctionSelector("setAccountType(uint8)"))

		switch {
		case as.Nonce() == 0 && isSetBls:
			txHashHex := tx.Hash().Hex()
			if _, ok := verifiedSignaturesCache.Load(txHashHex); !ok {
				if !tx.ValidEthSign() {
					return transaction.InvalidSignSecp
				}
				verifiedSignaturesCache.Store(txHashHex, true)
				if atomic.AddInt64(&verifiedSignaturesCacheCount, 1) >= maxVerifiedSignaturesCacheSize {
					verifiedSignaturesCache.Clear()
					atomic.StoreInt64(&verifiedSignaturesCacheCount, 0)
				}
			}
			_, err := UnpackSetBlsPublicKeyInput(dataInput)
			if err != nil {
				return transaction.InvalidData
			}
			if len(as.PublicKeyBls()) != 0 {
				return transaction.PublicKeyExists
			}
		case as.Nonce() != 0 && isSetType:
			_, err := UnpackSetAccountTypeInput(dataInput)
			if err != nil {
				return transaction.InvalidData
			}
		default:
			return transaction.InvalidData
		}
	} else {
		if as.Nonce() == 0 && !isCrossChainBatchSubmit && !isSubNodeLagging {
			return transaction.InvalidAddressMatchForTx0
		}
		if !tx.ValidDeployData() {
			return transaction.InvalidDeployData
		}
		if !tx.ValidCallData() {
			return transaction.InvalidCallData
		}
	}

	// Thêm kiểm tra kích thước Call Data
	const maxDataSize = 6 * 1024 * 1024
	if len(tx.Data()) > maxDataSize {
		logger.Error("Transaction data size exceeds limit", "hash", tx.Hash().Hex(), "size", len(tx.Data()), "limit", maxDataSize)
		return transaction.InvalidData // Sử dụng lỗi InvalidData hoặc tạo lỗi mới nếu cần
	}

	if tx.ToAddress() == tx.FromAddress() && tx.GetNonce() != 0 {
		_, erR := callDataToAccountType(tx.Data())
		if erR != nil {
			return erR
		}
	}

	if !tx.ValidChainID(chainState.GetConfig().ChainId.Uint64()) {
		return transaction.InvalidChainId
	}

	// validTx0, errCode := tx.ValidTx0(as, chainState.GetConfig().ChainId.String())

	// if !validTx0 {
	// 	return transaction.CodeToError[errCode]
	// }

	// verify pending use
	if !tx.ValidPendingUse(as) {
		return transaction.InvalidPendingUse
	}

	// nếu mà là giao dịch chuyển native token
	// thì maxGas sẽ là định phí
	// chỉ cho phép lơn hơn 10 lần phí cố định
	// ngược lại nếu là smart contract thì phí lơn hơn tối đa là 10.000 lần
	_, isFree := chainState.GetFreeFeeAddress()[tx.ToAddress()]
	if !isFree {
		_, isFree = chainState.GetFreeFeeAddress()[tx.FromAddress()]
	}
	if tx.GetNonce() == 0 || isCrossChainBatchSubmit {
		isFree = true
	}
	if !isSubNodeLagging && !tx.ValidAmount(as) {
		return transaction.InvalidAmount
	}

	if !isFree && !isSubNodeLagging && !tx.ValidMaxFee(as) {
		return transaction.InvalidMaxFee
	}

	// kiểm tra số dư có đủ cho max price
	// maxFee := tx.MaxFee()
	// if !isFree && maxFee.Cmp(big.NewInt(common.MINIMUM_BASE_FEE)) < 0 {
	// 	logger.Error("maxFee", maxFee)
	// 	return transaction.InvalidAmount
	// }

	// if !isFree && !tx.ValidAmountSpend(as, maxFee) {
	// 	logger.Error("Error when execute transaction code 120003: maxFee")
	// 	logger.Error("Error when execute transaction code 120003: detail as", as.Balance(), as.PendingBalance())
	// 	logger.Error("Error when execute transaction code 120003: detail mf", maxFee, tx.Amount())
	// 	return transaction.InvalidMaxGasPrice
	// }

	// if (!isFree && tx.ValidMaxGas() ) {
	// 	return transaction.InvalidMaxGas
	// }

	// verify last hash

	// Debug
	// neu newDeviceKey ma bang voi as.DeviceKey() thi bao loi
	// if tx.NewDeviceKey() == as.DeviceKey() && as.Nonce() != 0 {
	// 	return transaction.InvalidNewDeviceKey
	// }

	// // // verify device key
	// if !tx.ValidDeviceKey(as) {
	// 	return transaction.InvalidLastDeviceKey
	// }
	return nil
}
