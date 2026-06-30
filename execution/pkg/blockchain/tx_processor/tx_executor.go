package tx_processor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/trace"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/vm_processor"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

func processSingleGroup(
	ctx context.Context,
	chainState *blockchain.ChainState,
	localAccountDB types.AccountStateDB,
	localSmartContractDB types.SmartContractDB,
	groupItems []grouptxns.Item,
	mvmId common.Address,
	lastBlockHeader types.BlockHeader,
	enableTrace bool,
	isCache bool,
	blockTime uint64,
	leaderAddr common.Address,
	hasEvmTx bool,
	skipSignatureVerify bool,
) groupResultExt {
	// 🛑 TEE REVM FIX: Disable Cache manually per user instruction
	isCache = false
	
	// Acquire slices from memory pools to eliminate GC pressure
	txPtr := txSlicePool.Get().(*[]types.Transaction)
	txs := (*txPtr)[:0]

	rcpPtr := receiptSlicePool.Get().(*[]types.Receipt)
	rcps := (*rcpPtr)[:0]

	exPtr := scResultSlicePool.Get().(*[]types.ExecuteSCResult)
	exs := (*exPtr)[:0]

	mvmMapPtr := mvmIdMapPool.Get().(*map[common.Hash]common.Address)
	mvmMap := *mvmMapPtr
	clear(mvmMap) // Go 1.21+ fast clear

	gRs := grouptxns.GroupResult{
		Transactions:     txs,
		Receipts:         rcps,
		ExecuteSCResults: exs,
		Error:            nil,
		MvmIdMap:         mvmMap,
	}
	startGroup := time.Now()
	totalGasFee := big.NewInt(0)

	failedSendersPtr := failedSendersPool.Get().(*map[common.Address]bool)
	failedSenders := *failedSendersPtr
	clear(failedSenders) // Go 1.21+ fast clear
	defer failedSendersPool.Put(failedSendersPtr)

	// blockTime is now passed from Rust consensus for deterministic execution across all nodes
	for _, item := range groupItems {
		tx := item.Tx
		if enableTrace {
			GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTION_START", "Transaction retrieved from consensus block for execution")
		}
		var txCtx context.Context
		var txSpan *trace.Span
		if enableTrace {
			tracedTxCtx, actualTxSpan := trace.StartSpan(ctx, "TxProcessor.ProcessSingleTransaction", map[string]interface{}{
				"txHash":   tx.Hash().Hex(),
				"from":     tx.FromAddress().Hex(),
				"to":       tx.ToAddress().Hex(),
				"isCall":   tx.IsCallContract(),
				"isDeploy": tx.IsDeployContract(),
			})
			txCtx = tracedTxCtx
			txSpan = actualTxSpan
		} else {
			txCtx = ctx
			txSpan = nil
		}

		toAddress := tx.ToAddress()
		if tx.IsDeployContract() {
			toAddress = common.Address{}
		}
		// ❗ Nếu sender đã có lỗi trước đó, bài bỏ tx này mà không đưa vào block.
		// FORK-SAFETY & DATA INTEGRITY: Do NOT include skipped TXs in the block.
		// Including them without incrementing their nonce violates blockchain invariants.
		if failedSenders[tx.FromAddress()] {
			if enableTrace {
				GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTION_SKIPPED", "Skipped execution due to previous transaction failure from this sender")
			}
			if enableTrace && txSpan != nil {
				txSpan.End()
			}
			continue
		}

		// EARLY-ABORT SIGNAL CHECK
		// ---------------------------------------------------------------------
		// If the context is canceled (e.g., from Block-STM detecting a conflict
		// in a concurrent group), we abort processing this group immediately.
		select {
		case <-ctx.Done():
			logger.Debug("🛑 [EARLY-ABORT] Group aborted due to context cancellation")
			*txPtr = gRs.Transactions
			*rcpPtr = gRs.Receipts
			*exPtr = gRs.ExecuteSCResults
			*mvmMapPtr = gRs.MvmIdMap
			return groupResultExt{
				txPtr:         txPtr,
				rcpPtr:        rcpPtr,
				exPtr:         exPtr,
				mvmPtr:        mvmMapPtr,
				Error:         ctx.Err(),
			}
		default:
		}
		// ---------------------------------------------------------------------

		// NOTE: VerifyTransaction skipped here — TXs from Rust consensus were already
		// verified by go-sub (AddTransactionToPool) before entering the pool.
		// Skipping saves signature verification + nonce check per TX.

		// Phần xử lý bình thường
		as, _ := localAccountDB.AccountState(tx.FromAddress())
		var err error

		// CRITICAL SECURITY FIX: Validate nonce strictly before processing.
		// Although tx_pool verified the signature and nonce, multiple TXs
		// with the same nonce might have entered the pool concurrently before
		// the state was updated. We MUST reject duplicate nonces here to
		// prevent them from being executed by the C++ EVM in the same block.
		if as == nil {
			as = state.NewAccountState(tx.FromAddress())
		}

		// Bổ sung xác thực lại giao dịch (BLS, Amount, MaxFee,...)
		// để chặn các giao dịch không hợp lệ lọt vào block (đặc biệt từ Sync Data của Rust)
		var errVerify *transaction.TransactionError
		if skipSignatureVerify || LoadVerifiedSignature(tx.Hash()) {
			// PERF OPT (D): Fast path for already verified transactions (e.g., from local mempool injection).
			// Skips redundant AccountState fetches and amount/fee validations.
			// Nonce check is handled explicitly below.
			errVerify = nil
		} else {
			// Fallback: Full verification for transactions from P2P sync
			errVerify = VerifyTransaction(tx, chainState, as)
		}

		if errVerify != nil {
			if enableTrace {
				GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_VERIFY_REJECTED", errVerify.Description)
			}
			logger.Warn("❌ [VERIFY-REJECT] %v for tx %s (From: %s) -> GIAO DỊCH BỊ VỨT BỎ KHỎI BLOCK", errVerify.Description, tx.Hash().Hex(), tx.FromAddress().Hex())

			if errVerify.Code != transaction.InvalidNonce.Code {
				failedSenders[tx.FromAddress()] = true
			}

			if enableTrace && txSpan != nil {
				txSpan.End()
			}
			continue
		}

		if tx.GetNonce() != as.Nonce() {
			if !NomtAheadReplayMode.Load() {
				err = fmt.Errorf("nonce mismatch: tx.Nonce()=%d, state.Nonce()=%d", tx.GetNonce(), as.Nonce())
				if enableTrace {
					GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_NONCE_REJECTED", err.Error())
				}
				// CRITICAL FIX: Changed from Debug to Warn so you can see exactly when a duplicate is rejected
				logger.Warn("❌ [NONCE-REJECT] %v for tx %s (From: %s) -> GIAO DỊCH BỊ VỨT BỎ KHỎI BLOCK", err, tx.Hash().Hex(), tx.FromAddress().Hex())

				// FORK-SAFETY & DATA INTEGRITY: Do NOT include invalid nonce TXs in the block.
				// Including them causes duplicate TX hashes across multiple blocks when a client
				// resends a batch, inflating the block's TX count and bloating the ledger.
				if tx.GetNonce() > as.Nonce() {
					failedSenders[tx.FromAddress()] = true // Ngừng parse các TX tiếp theo của sender này (giữ đúng thứ tự nonce)
				}

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			} else {
				logger.Warn("🛡️ [NOMT-AHEAD-REPLAY-TX] Bypassing nonce mismatch check for tx %s (From: %s, tx.Nonce=%d, state.Nonce=%d)",
					tx.Hash().Hex()[:16]+"...", tx.FromAddress().Hex(), tx.GetNonce(), as.Nonce())
			}
		}
		var rcp types.Receipt
		if tx.ToAddress() == mt_common.VALIDATOR_CONTRACT_ADDRESS {
			validatorHandler, err := GetValidatorHandler()
			if err != nil {
				logger.Error("Lỗi khi tạo ValidatorHandler: %v", err)
				rcp = createErrorReceipt(tx, toAddress, err)
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				failedSenders[tx.FromAddress()] = true

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}
			rcp, exRs, txFailed := validatorHandler.HandleTransaction(txCtx, chainState, tx, mvmId, enableTrace, blockTime)
			gRs.Receipts = append(gRs.Receipts, rcp)
			gRs.Transactions = append(gRs.Transactions, tx)
			if exRs != nil { // exRs có thể nil trong một số trường hợp lỗi
				gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)
			}
			if txFailed {
				failedSenders[tx.FromAddress()] = true

			}

			if enableTrace && txSpan != nil {
				txSpan.End()
			}
			continue // Chuyển sang transaction tiếp theo
		}
		if tx.ToAddress() == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS {
			// 🔍 [CROSS-CHAIN DEBUG] Log TX được đưa vào block number nào
			// So sánh log này giữa các node → nếu block# khác nhau → TX đến muộn → hash lệch
			blockNum := lastBlockHeader.BlockNumber() + 1
			logger.Info("📦 [BLOCK-INCLUDE] Cross-chain TX included in block #%d: hash=%s from=%s nonce=%d readOnly=%v",
				blockNum, tx.Hash().Hex()[:16], tx.FromAddress().Hex()[:10], tx.GetNonce(), tx.GetReadOnly())
			// Master tạo receipt ngay (ExecuteNonceOnly), không thay đổi state.
			// TX vẫn vào block → embassy nhận receipt → biết vote đã được ghi nhận.
			// ReadOnly=false (mặc định) → gọi HandleTransaction để execute đầy đủ.
			if tx.GetReadOnly() {
				logger.Info("[CC SIG_ACK] TX %s readOnly=true → nonce-only", tx.Hash().Hex())
				// vm_processor handles VM interactions per transaction
				vmP := vm_processor.NewVmProcessor(chainState, mvmId, enableTrace, blockTime, leaderAddr)
				vmP.SetAccountStateDB(localAccountDB)
				vmP.SetSmartContractDB(localSmartContractDB)
				
				rcp = receipt.NewReceipt(
					tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
					pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
					mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
					[]types.EventLog{}, 0, common.Hash{}, 0,
				)

				var sigAckExRs types.ExecuteSCResult
				sigAckExRs, err = vmP.ExecuteNonceOnly(txCtx, tx, true)
				if err != nil {
					rcp = createErrorReceipt(tx, toAddress, err)
					if sigAckExRs != nil {
						rcp.UpdateExecuteResult(sigAckExRs.ReceiptStatus(), sigAckExRs.Return(), sigAckExRs.Exception(), sigAckExRs.GasUsed(), sigAckExRs.EventLogs())
					}
					gRs.Receipts = append(gRs.Receipts, rcp)
					gRs.Transactions = append(gRs.Transactions, tx)
					failedSenders[tx.FromAddress()] = true

					if enableTrace && txSpan != nil {
						txSpan.End()
					}
					logger.Error("❌ [CC SIG_ACK] TX %s type=100 → nonce-only failed: %v", tx.Hash().Hex(), err)
					continue
				}
				logger.Info("[CC SIG_ACK] TX %s nonce-only success: %v", tx.Hash().Hex(), tx.RelatedAddresses())
				rcp.UpdateExecuteResult(sigAckExRs.ReceiptStatus(), sigAckExRs.Return(), sigAckExRs.Exception(), sigAckExRs.GasUsed(), sigAckExRs.EventLogs())
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				if sigAckExRs != nil {
					gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, sigAckExRs)
				}
				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}
			// ── ReadOnly=false: virtual đã đủ 2/3 vote (EXECUTE) ────────────
			// Master gọi HandleTransaction → phân loại EventKind trong batchSubmit.
			// Các call khác (lockAndBridge, sendMessage) cũng đi vào đây (ReadOnly mặc định false).
			ccHandler, err := cross_chain_handler.GetCrossChainHandler()
			if err != nil {
				logger.Error("Lỗi khi tạo CrossChainHandler: %v", err)
				rcp = createErrorReceipt(tx, toAddress, err)
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				failedSenders[tx.FromAddress()] = true

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}
			rcp, exRs, txFailed := ccHandler.HandleTransaction(txCtx, chainState, tx, mvmId, enableTrace, blockTime)
			gRs.MvmIdMap[tx.Hash()] = mvmId
			gRs.Receipts = append(gRs.Receipts, rcp)
			gRs.Transactions = append(gRs.Transactions, tx)
			if exRs != nil {
				gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)
			}
			if txFailed {
				failedSenders[tx.FromAddress()] = true

			}
			if enableTrace && txSpan != nil {
				txSpan.End()
			}
			continue
		}
		if tx.ToAddress() == utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
			dataInput := tx.CallData().Input()
			if len(dataInput) < 4 {
				logger.Error("Invalid calldata: less than 4 bytes")
				err := errors.New("invalid calldata")
				rcp := createErrorReceipt(tx, toAddress, err)
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				failedSenders[tx.FromAddress()] = true

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}

			selector := dataInput[:4]
			fromAddr := tx.FromAddress()

			switch {
			case tx.GetNonce() == 0 && bytes.Equal(selector, utils.GetFunctionSelector("setBlsPublicKey(bytes)")):
				plk, err := UnpackSetBlsPublicKeyInput(dataInput)
				if err != nil {
					logger.Error("UnpackSetBlsPublicKeyInput failed for tx %s: %v", tx.Hash().Hex(), err)
					rcp := createErrorReceipt(tx, toAddress, err)
					gRs.Receipts = append(gRs.Receipts, rcp)
					gRs.Transactions = append(gRs.Transactions, tx)
					// NOTE: hasFailed NOT set — BLS operations are independent per-account
					if enableTrace && txSpan != nil {
						txSpan.End()
					}
					continue
				}
				if as != nil && len(as.PublicKeyBls()) != 0 {
					logger.Warn("PublicKeyBls already exists for %s, skipping tx %s", fromAddr.Hex(), tx.Hash().Hex())
					rcp := createErrorReceipt(tx, toAddress, fmt.Errorf("PublicKeyBls already exists"))
					gRs.Receipts = append(gRs.Receipts, rcp)
					gRs.Transactions = append(gRs.Transactions, tx)
					if enableTrace && txSpan != nil {
						txSpan.End()
					}
					continue
				}
				// ═══════════════════════════════════════════════════════════════
				// BATCH MUTATIONS: Mutate account state directly (no DB calls).
				// This bypasses accountLock + getOrCreateAccountState + setDirty
				// for each call (3 DB calls → 0). Dirty marking is deferred to
				// post-parallel phase via gRs.DirtyAccounts.
				//
				// FORK-SAFETY: `as` is the same object pointer from PreloadAccounts.
				// Each group has a unique address, so no data race between groups.
				// DirtyAccounts are applied in indexed order after wg.Wait().
				// ═══════════════════════════════════════════════════════════════
				if setErr := as.SetPublicKeyBls(plk); setErr != nil {
					rcp := createErrorReceipt(tx, toAddress, setErr)
					gRs.Receipts = append(gRs.Receipts, rcp)
					gRs.Transactions = append(gRs.Transactions, tx)
					logger.Error("SetPublicKeyBls failed for tx %s: %v", tx.Hash().Hex(), setErr)

					if enableTrace && txSpan != nil {
						txSpan.End()
					}
					continue
				}
				if tx.GetNonce() != as.Nonce() {
					logger.Error("[NONCE-TRACE] BLS-SetPublicKey MISMATCH: addr=%s, tx.nonce=%d, state.nonce=%d, txHash=%s", tx.FromAddress().Hex(), tx.GetNonce(), as.Nonce(), tx.Hash().Hex())
					// This is a critical error, but we can't return here.
					// For now, we'll log and proceed, but this indicates a deeper issue.
				}
				logger.Debug("[NONCE-TRACE] BLS-SetPublicKey OK: addr=%s, tx.nonce=%d, state.nonce=%d, txHash=%s", tx.FromAddress().Hex(), tx.GetNonce(), as.Nonce(), tx.Hash().Hex())
				as.SetNonce(as.Nonce() + 1)
				logger.Debug("[NONCE-TRACE] BLS-SetNonce: addr=%s, nonce after +1=%d, txHash=%s", tx.FromAddress().Hex(), as.Nonce(), tx.Hash().Hex())
				as.SetLastHash(tx.Hash())
				
				// Standardize Dirty Marking: Delegate to localAccountDB instead of manual gRs.DirtyAccounts slice
				localAccountDB.PublicSetDirtyAccountState(as)

				rcp := receipt.NewReceipt(
					tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
					pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
					mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
					[]types.EventLog{}, 0, common.Hash{}, 0,
				)
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			case tx.GetNonce() != 0 && bytes.Equal(selector, utils.GetFunctionSelector("setAccountType(uint8)")):
				acType, err := UnpackSetAccountTypeInput(dataInput)
				if err != nil {
					logger.Error("UnpackSetAccountTypeInput failed for tx %s: %v", tx.Hash().Hex(), err)
					rcp := createErrorReceipt(tx, toAddress, err)
					gRs.Receipts = append(gRs.Receipts, rcp)
					gRs.Transactions = append(gRs.Transactions, tx)
					if enableTrace && txSpan != nil {
						txSpan.End()
					}
					continue
				}
				err = localAccountDB.SetAccountType(fromAddr, acType)
				if err != nil {
					logger.Error("SetAccountType failed for tx %s: %v", tx.Hash().Hex(), err)
					rcp := createErrorReceipt(tx, toAddress, err)
					gRs.Receipts = append(gRs.Receipts, rcp)
					gRs.Transactions = append(gRs.Transactions, tx)

					if enableTrace && txSpan != nil {
						txSpan.End()
					}
					continue
				}
				// CRITICAL FORK-FIX: Create deterministic success receipt + nonce increment HERE.
				localAccountDB.SetNonce(fromAddr, as.Nonce()+1)
				logger.Debug("[NONCE-TRACE] setAccountType-SetNonce: addr=%s, nonce=%d, txHash=%s", fromAddr.Hex(), as.Nonce()+1, tx.Hash().Hex())
				localAccountDB.SetLastHash(fromAddr, tx.Hash())
				rcp := receipt.NewReceipt(
					tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
					pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
					mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
					[]types.EventLog{}, 0, common.Hash{}, 0,
				)
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			case bytes.Equal(selector, utils.GetFunctionSelector("getAllAccount(bytes,bytes,uint,uint,uint,bool)")):
				logger.Info("etch call account getAllAccount _ %v", tx)
			case bytes.Equal(selector, utils.GetFunctionSelector("confirmAccount(address,bytes)")):
				logger.Info("etch call account confirmAccount_ %v", tx)
			default:
				err := fmt.Errorf("invalid selector: nonce=%d, selector=0x%x, txHash=%s", tx.GetNonce(), selector, tx.Hash().Hex())
				rcp := createErrorReceipt(tx, toAddress, err)
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				logger.Error("Transaction failed for tx %s: %v", tx.Hash().Hex(), err)
				failedSenders[tx.FromAddress()] = true

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}

			rcp := receipt.NewReceipt(
				tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
				pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
				mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
				[]types.EventLog{}, 0, common.Hash{}, 0,
			)

			var exRs types.ExecuteSCResult
			vmP := vm_processor.NewVmProcessor(chainState, mvmId, enableTrace, blockTime, leaderAddr)
			vmP.SetAccountStateDB(localAccountDB)
			vmP.SetSmartContractDB(localSmartContractDB)

			exRs, err = vmP.ExecuteNonceOnly(txCtx, tx, true)
			if err != nil {
				rcp = createErrorReceipt(tx, toAddress, err)
				if exRs != nil {
					rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
					gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)
				}
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				logger.Error("executeTransactionWithMvmId failed for tx %s: %v", tx.Hash().Hex(), err)
				failedSenders[tx.FromAddress()] = true // ❗ Đánh dấu lỗi

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}
			rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
			localAccountDB.SetLastHash(tx.FromAddress(), tx.Hash())
			localAccountDB.SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())

			gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)
			gRs.Receipts = append(gRs.Receipts, rcp)
			gRs.Transactions = append(gRs.Transactions, tx)
			if enableTrace && txSpan != nil {
				txSpan.End()
			}
			continue
		}

		rcp = receipt.NewReceipt(
			tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
			pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
			mt_common.MINIMUM_BASE_FEE, mt_common.TRANSFER_GAS_COST,
			[]types.EventLog{}, 0, common.Hash{}, 0,
		)

		var exRs types.ExecuteSCResult
		vmP := vm_processor.NewVmProcessor(chainState, mvmId, enableTrace, blockTime, leaderAddr)
		vmP.SetAccountStateDB(localAccountDB)
		vmP.SetSmartContractDB(localSmartContractDB)
		usedMvmId := mvmId

		if tx.IsRegularTransaction() {
			if enableTrace {
				GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTE_START", "Executing native value transfer")
			}
			// ═══════════════════════════════════════════════════════════════
			// BATCH MUTATIONS for Native TX: Use DB locks to prevent data races
			// with concurrent EVM groups modifying the same account.
			// ═══════════════════════════════════════════════════════════════
			_, isFree := chainState.GetFreeFeeAddress()[tx.ToAddress()]
			gasLimit := uint64(mt_common.TRANSFER_GAS_COST)
			if isFree {
				gasLimit = uint64(mt_common.MAX_GASS_FEE)
			}
			gasFee := new(big.Int).SetUint64(gasLimit * tx.MaxGasPrice())
			totalCost := new(big.Int).Add(tx.Amount(), gasFee)

			// Mutate sender (thread-safe)
			err = localAccountDB.SubTotalBalance(tx.FromAddress(), totalCost)
			if err != nil {
				if enableTrace {
					GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTE_FAILED", "Insufficient balance for transfer")
				}
				// Thread-safe check: if err is returned, it means insufficient balance (or lock issue)
				rcp := createErrorReceipt(tx, toAddress, fmt.Errorf("insufficient balance for transfer"))
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				failedSenders[tx.FromAddress()] = true
				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}
			localAccountDB.PlusOneNonce(tx.FromAddress())
			localAccountDB.SetLastHash(tx.FromAddress(), tx.Hash())
			localAccountDB.SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())

			// Mutate receiver (thread-safe)
			localAccountDB.AddBalance(tx.ToAddress(), tx.Amount())

			// Accumulate gas fee for later batch update to coinbase
			totalGasFee.Add(totalGasFee, gasFee)

			// Generate fake MVM result
			exRs = smart_contract.NewExecuteSCResult(
				tx.Hash(), pb.RECEIPT_STATUS_RETURNED, pb.EXCEPTION_NONE, nil,
				mt_common.TRANSFER_GAS_COST, common.Hash{},
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			)

		} else {
			if enableTrace {
				GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTE_START", "Executing EVM smart contract call")
			}

			// CRITICAL FORK FIX: We removed `tx.ToAddress()` as the cache key and ALWAYS use `mvmId` (Group ID).
			// This prevents cross-block C++ EVM dirty state leaks and concurrency corruption.
			gRs.MvmIdMap[tx.Hash()] = usedMvmId
			// logger.Debug("1.ExecuteSmartContract MVMId:")
			startMVM := time.Now()
			exRs, err = vmP.ExecuteTransactionWithMvmId(txCtx, tx, false, isCache)
			mvmElapsed := time.Since(startMVM)
			if mvmElapsed.Milliseconds() > 50 {
				logger.Warn("🐌 [PERF-WARN] ExecuteTransactionWithMvmId (C++ EVM) took %v for tx %s (From: %s). EVM Execution is lagging!", mvmElapsed, tx.Hash().Hex(), tx.FromAddress().Hex())
			}
			if err != nil {
				if enableTrace {
					GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTE_FAILED", err.Error())
				}
				rcp = createErrorReceipt(tx, toAddress, err)
				if exRs != nil {
					rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
					gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)
				}
				gRs.Receipts = append(gRs.Receipts, rcp)
				gRs.Transactions = append(gRs.Transactions, tx)
				logger.Error("executeTransactionWithMvmId failed for tx %s: %v", tx.Hash().Hex(), err)
				failedSenders[tx.FromAddress()] = true // ❗ Đánh dấu lỗi

				if enableTrace && txSpan != nil {
					txSpan.End()
				}
				continue
			}
			if enableTrace {
				GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_EXECUTE_SUCCESS", fmt.Sprintf("EVM contract execution completed, status: %d, gasUsed: %d", exRs.ReceiptStatus(), exRs.GasUsed()))
			}
			logger.Debug("executeTransactionWithMvmId success for tx %s, exRs: %v", tx.Hash().Hex(), exRs)
			localAccountDB.SetLastHash(tx.FromAddress(), tx.Hash())
			localAccountDB.SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())
		}
		rcp.UpdateExecuteResult(exRs.ReceiptStatus(), exRs.Return(), exRs.Exception(), exRs.GasUsed(), exRs.EventLogs())
		localAccountDB.SetLastHash(tx.FromAddress(), tx.Hash())
		localAccountDB.SetNewDeviceKey(tx.FromAddress(), tx.NewDeviceKey())

		// 🔒 [STATE-LEAK-FIX / BLOCK-STM DESIGN FIX]
		// Apply EVM/Native state changes back to the Go state cache (localAccountDB).
		// This guarantees that the Block-STM conflict detector and subsequent TXs in the same block
		// see the correct updated balances and nonces, maintaining deterministic local write-sets.
		if exRs != nil {
			// Nonce must be updated unconditionally (even on revert) as sender still pays gas.
			if exRs.MapNonce() != nil {
				for addrHex, newNonceBytes := range exRs.MapNonce() {
					addr := common.HexToAddress(addrHex)
					newNonce := big.NewInt(0).SetBytes(newNonceBytes).Uint64()
					localAccountDB.SetNonce(addr, newNonce)
				}
			}
			// Balances are only updated if the execution didn't revert.
			if exRs.ReceiptStatus() == pb.RECEIPT_STATUS_RETURNED {
				if exRs.MapAddBalance() != nil {
					for addrHex, addAmtBytes := range exRs.MapAddBalance() {
						addr := common.HexToAddress(addrHex)
						addAmt := big.NewInt(0).SetBytes(addAmtBytes)
						localAccountDB.AddBalance(addr, addAmt)
					}
				}
				if exRs.MapSubBalance() != nil {
					for addrHex, subAmtBytes := range exRs.MapSubBalance() {
						addr := common.HexToAddress(addrHex)
						subAmt := big.NewInt(0).SetBytes(subAmtBytes)
						localAccountDB.SubTotalBalance(addr, subAmt)
					}
				}
				// 🔒 [STATE-LEAK-FIX / BLOCK-STM DESIGN FIX]
				// Apply storage changes to localSmartContractDB so subsequent TXs in the same sequential meta-group see the updated state!
				if exRs.MapStorageChange() != nil {
					for addrHex, changes := range exRs.MapStorageChange() {
						addr := common.HexToAddress(addrHex)
						var keys [][]byte
						var values [][]byte
						for keyHex, valueBytes := range changes {
							keys = append(keys, common.HexToHash(keyHex).Bytes())
							values = append(values, valueBytes)
						}
						// Tối ưu I/O và Mutex: Ghi nguyên một mẻ (batch) cho toàn bộ thay đổi của Contract này
						localSmartContractDB.BatchSetStorageValues(addr, keys, values)
					}
				}
			}
		}

		gRs.ExecuteSCResults = append(gRs.ExecuteSCResults, exRs)

		// ✅ Đảm bảo receipt luôn được đưa vào list, kể cả khi giao dịch bị revert (THREW/HALTED)
		// Giao dịch bị revert vẫn cần có receipt và đưa vào block để client biết được trạng thái
		if enableTrace && txSpan != nil {
			txSpan.End()
		}

		if enableTrace {
			GlobalTxTraceStore.UpdateTrace(tx.Hash(), "BLOCK_RECEIPT_CREATED", fmt.Sprintf("Receipt created and added to block. Status: %d", rcp.Status()))
		}

		// ─── Record Prometheus Metrics for Transaction Lifecycle ───
		if GlobalTxTraceStore.Enabled() {
			if traceObj, ok := GlobalTxTraceStore.GetTrace(tx.Hash()); ok {
				var tInjection, tForward, tConsensus, tReceipt int64
				for _, step := range traceObj.Steps {
					switch step.Step {
					case "INJECTION_RECEIVED":
						tInjection = step.Timestamp
					case "FORWARDED_TO_RUST":
						tForward = step.Timestamp
					case "CONSENSUS_COMMITTED":
						tConsensus = step.Timestamp
					case "BLOCK_RECEIPT_CREATED":
						tReceipt = step.Timestamp
					}
				}

				if tInjection > 0 && tForward >= tInjection {
					metrics.TxMempoolDuration.Observe(float64(tForward-tInjection) / 1000.0)
				}
				if tForward > 0 && tConsensus >= tForward {
					metrics.TxConsensusDuration.Observe(float64(tConsensus-tForward) / 1000.0)
				}
				if tConsensus > 0 && tReceipt >= tConsensus {
					metrics.TxExecutionDuration.Observe(float64(tReceipt-tConsensus) / 1000.0)
				}
				if tInjection > 0 && tReceipt >= tInjection {
					metrics.TxEndToEndDuration.Observe(float64(tReceipt-tInjection) / 1000.0)
				}
			}
		}
		// ──────────────────────────────────────────────────────────

		gRs.Receipts = append(gRs.Receipts, rcp)
		gRs.Transactions = append(gRs.Transactions, tx)

	}

	txCount := len(groupItems)
	elapsed := time.Since(startGroup)
	var avgPerTx time.Duration
	if txCount > 0 {
		avgPerTx = elapsed / time.Duration(txCount)
	}
	logger.Debug("⏱️ [PERF-GROUP] txCount=%d | groupTime=%v | avg=%v/tx",
		txCount, elapsed, avgPerTx)

	// Update slice headers in pools before returning
	*txPtr = gRs.Transactions
	*rcpPtr = gRs.Receipts
	*exPtr = gRs.ExecuteSCResults
	*mvmMapPtr = gRs.MvmIdMap

	// Standardize extraction of DirtyAccounts for Block-STM
	var dirtyAccounts []types.AccountState
	if adb, ok := localAccountDB.(interface{ GetDirtyAccounts() map[common.Address]types.AccountState }); ok {
		dirtyMap := adb.GetDirtyAccounts()
		for _, as := range dirtyMap {
			dirtyAccounts = append(dirtyAccounts, as)
		}
	} else {
		// Fallback
		dirtyAccounts = gRs.DirtyAccounts
	}

	return groupResultExt{
		txPtr:         txPtr,
		rcpPtr:        rcpPtr,
		exPtr:         exPtr,
		mvmPtr:        mvmMapPtr,
		Error:         gRs.Error,
		DirtyAccounts: dirtyAccounts,
		TotalGasFee:   totalGasFee,
	}
}

func createErrorReceipt(tx types.Transaction, toAddress common.Address, err error) types.Receipt {
	return receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), toAddress, tx.Amount(),
		pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(err.Error()), pb.EXCEPTION_NONE,
		mt_common.MINIMUM_BASE_FEE, 0, []types.EventLog{}, 0, common.Hash{}, 0,
	)
}
