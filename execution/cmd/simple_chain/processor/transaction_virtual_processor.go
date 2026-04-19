package processor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/types"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/vm_processor"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/proxy_tx"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
)

// ─────────────────────────────────────────────────────────────────────────────
// ProcessSingleTransactionVirtual
// ─────────────────────────────────────────────────────────────────────────────

func (v *TxVirtualExecutor) ProcessSingleTransactionVirtual(tx types.Transaction) (types.Transaction, error, []byte) {
	if tx == nil {
		return nil, fmt.Errorf("transaction cannot be nil"), nil
	}
	// tx.SetIsDebug(true)
	logger.Info("_virtual_ %v", tx)
	if tx.ToAddress() == utils.GetAddressSelector(mt_common.IDENTIFIER_STAKE) {
		updatedTx := tx
		updatedTx.AddRelatedAddress(tx.FromAddress())
		return updatedTx, nil, nil
	}

	// ─── CROSS_CHAIN_CONTRACT_ADDRESS (0x1002) ───────────────────────────────
	if tx.ToAddress() == mt_common.CROSS_CHAIN_CONTRACT_ADDRESS {
		updatedTx := tx
		updatedTx.AddRelatedAddress(tx.FromAddress())
		updatedTx.AddRelatedAddress(tx.ToAddress())

		inputData := tx.CallData().Input()

		// ── batchSubmit: vote accumulation ──────────────────────────────────
		// Dùng cross_chain_handler.IsBatchSubmitTx để check selector qua ABI.
		ccHandler, handlerErr := cross_chain_handler.GetCrossChainHandler()
		if handlerErr == nil && ccHandler != nil && ccHandler.IsBatchSubmitTx(inputData) {
			return v.processBatchSubmitVirtual(updatedTx, inputData)
		}

		// ── lockAndBridge / other: chỉ cần from+to ─────────────────────────
		return updatedTx, nil, nil
	}
	ctx := context.Background()
	blockTime := uint64(time.Now().Unix())

	combinedHash := sha256.Sum256([]byte(fmt.Sprintf("%x%d%d", tx.Hash(), rand.Int63(), time.Now().UnixNano())))
	mvmId := common.BytesToAddress(combinedHash[12:])

	var exRs types.ExecuteSCResult
	var statusUpdate bool

	// ─── OPTIMIZATION: Use shared state for quick account check ──────────
	// This avoids expensive NewChainState (trie creation) for simple TXs
	startAS := time.Now()
	as, err := v.chainState.GetAccountStateDB().AccountState(tx.FromAddress())
	asDuration := time.Since(startAS)
	logger.Info("[PERF-VIRTUAL] AccountState lookup: %v, hash: %v", asDuration, tx.Hash().Hex())
	if err != nil {
		return nil, fmt.Errorf("failed to get sender account state: %w", err), nil
	}

	// ─── 1. Nonce 0 Check ──────────────────────────────────────────────────
	// First transaction (nonce 0) MUST be to AccountSetting.
	// If tx.GetNonce() > 0, it may be a valid transaction with a lagging local AccountState (due to async state sync on Sub Nodes),
	// so we allow it to pass virtual validation and let the Master Node handle strict validation during execution.
	if as.Nonce() == 0 && tx.GetNonce() == 0 && tx.ToAddress() != utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT) {
		return nil, fmt.Errorf("tx0: invalid or missing contract address"), nil
	}

	// ─── 2. Determine if EVM execution is needed ──────────────────────────
	// We skip EVM for AccountSetting or non-contract calls
	isAccountSetting := tx.ToAddress() == utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	needsEVM := (tx.IsCallContract() || tx.IsDeployContract()) && !isAccountSetting
	logger.Info("[DEBUG VIRTUAL] hash=%s, call=%v, deploy=%v, accSetting=%v, needsEVM=%v",
		tx.Hash().Hex(), tx.IsCallContract(), tx.IsDeployContract(), isAccountSetting, needsEVM)

	if needsEVM {
		// Use the block processor's live chainState directly for EVM execution.
		// v.chainState is properly synchronized:
		//   1. applyBlockBatch() writes AccountBatch to PebbleDB
		//   2. UpdateStateForNewHeader() creates new trie at correct root from PebbleDB
		//   3. Receipt broadcast (async) happens AFTER state update
		// So by the time a client receives a receipt and sends the next TX,
		var blHeader types.BlockHeader
		if v.env != nil && v.env.GetLastBlock() != nil {
			blHeader = v.env.GetLastBlock().Header()
		}

		vmP := vm_processor.NewVmProcessor(v.chainState, mvmId, false, blockTime)
		if tx.IsCallContract() {
			// Validate smart contract call using live chainState (reliable after UpdateStateForNewHeader)
			/*toAccountState, getAccErr := v.chainState.GetAccountStateDB().AccountState(tx.ToAddress())
			if getAccErr != nil {
				logger.Error("[DEBUG VIRTUAL] AccountState lookup error for %s: %v", tx.ToAddress().Hex(), getAccErr)
				return nil, fmt.Errorf("failed to get 'to' account state: %w", getAccErr), nil
			}
			if !vmP.IsValidSmartContractCall(toAccountState, tx) {
				return nil, fmt.Errorf("invalid smart contract call"), nil
			}*/

			// Thực thi giao dịch hợp đồng call
			startEVM := time.Now()
			exRs, statusUpdate, err = vmP.ExecuteTransactionWithMvmIdSub(ctx, tx, true)
			evmDuration := time.Since(startEVM)
			logger.Info("[PERF-VIRTUAL] EVM execution (Call): %v, hash: %v, block#%d",
				evmDuration, tx.Hash().Hex(), blHeader.BlockNumber())
		} else if tx.IsDeployContract() {
			// Thực thi giao dịch hợp đồng deploy
			startEVM := time.Now()
			exRs, statusUpdate, err = vmP.ExecuteTransactionWithMvmIdSubDeploy(ctx, tx, mvmId, true)
			evmDuration := time.Since(startEVM)
			logger.Info("[PERF-VIRTUAL] EVM execution (Deploy): %v, hash: %v, block#%d", evmDuration, tx.Hash().Hex(), blHeader.BlockNumber())
		}

		// Kiểm tra trạng thái biên lai sau khi thực thi (chung cho cả call và deploy)
		if err == nil && exRs != nil && (exRs.ReceiptStatus() == pb.RECEIPT_STATUS_THREW || exRs.ReceiptStatus() == pb.RECEIPT_STATUS_HALTED) && exRs.Exception() != pb.EXCEPTION_ERR_WRITE_PROTECTION {
			logger.Error("[DEBUG VIRTUAL] Receipt error: status=%v, ex=%v, hash=%s", exRs.ReceiptStatus(), exRs.Exception(), tx.Hash().Hex())
			if len(exRs.Return()) >= 4 {
				// Cố gắng parse revert message từ Return data (đã được encode bởi prepareReturnDataWithExceptionMessage)
				msg, revertErr := RevertParser(hex.EncodeToString(exRs.Return()))
				if revertErr == nil && msg != "" {
					return nil, fmt.Errorf("%s", msg), exRs.Return()
				}
			}

			if exRs.Exception() == pb.EXCEPTION_ERR_EXECUTION_REVERTED {
				return nil, fmt.Errorf("transaction Revert"), exRs.Return()
			} else {
				txError := transaction.MapProtoExceptionToTransactionError(exRs.Exception())
				if txError != nil {
					if txError.Description == "none" {
						logger.Warn("[DEBUG VIRTUAL] Allowed unknown revert (EXCEPTION_NONE) to pass to consensus. hash=%s", tx.Hash().Hex())
						return tx, nil, exRs.Return()
					}
					return nil, fmt.Errorf("transaction error: %s", txError.Description), exRs.Return()
				} else {
					return nil, fmt.Errorf("transaction error Exception: %d", exRs.Exception()), exRs.Return()
				}
			}
		}

		if err != nil {
			logger.Error("[DEBUG VIRTUAL] Execute err: %v", err)
			return nil, err, nil
		}
		if exRs == nil {
			logger.Error("[DEBUG VIRTUAL] exRs IS NIL!")
			return nil, fmt.Errorf("executeTransactionOffChain returned nil"), nil
		}

		// Cập nhật RelatedAddresses
		listRelatedAddress := mvm.GetMVMApi(mvmId).GetCurrentRelatedAddresses()
		bRelatedAddresses := make([][]byte, len(listRelatedAddress))
		for j, addr := range listRelatedAddress {
			bRelatedAddresses[j] = addr.Bytes()
		}

		updatedTx := tx
		updatedTx.UpdateRelatedAddresses(bRelatedAddresses)
		updatedTx.AddRelatedAddress(tx.FromAddress())
		updatedTx.AddRelatedAddress(tx.ToAddress())
		updatedTx.SetReadOnly(!statusUpdate)

		mvm.ClearMVMApi(mvmId)
		return updatedTx, nil, exRs.Return()
	}

	// ─── QUICK PATH: No EVM needed ──────────────────────────────────────────
	tx.AddRelatedAddress(tx.FromAddress())
	tx.AddRelatedAddress(tx.ToAddress())
	return tx, nil, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// processBatchSubmitVirtual — Xác thực + vote accumulation cho batchSubmit
//
// Sub server chạy hàm này độc lập với master (2 process riêng).
// Flow:
//  1. Load danh sách embassy active từ CrossChainHandler cache (sub tự load)
//  2. Verify nonce TX: tx.GetNonce() == accountState.Nonce()
//     → Nếu sai nonce → reject (không vào pool)
//  3. Verify BLS signature: bls.VerifySign(embassyBLSKey, tx.Sign(), tx.Hash())
//     → Tìm embassy có BLS public key match với signature
//     → Nếu không match bất kỳ embassy nào → reject
//  4. Vote accumulation theo sha256(events-only packed) làm key
//     → Chưa đủ 2/3 hoặc skip/duplicate: SetReadOnly(true)
//     → master tạo receipt ngay (nonce-only), không execute state
//     → Đủ 2/3 lần đầu: SetReadOnly(false)
//     → master gọi GetCrossChainHandler().HandleTransaction để execute
//
// NOTE: Dùng SetReadOnly thay SetType để không làm thay đổi tx hash.
// SetReadOnly(false) là mặc định cho giao dịch thường (read-write).
// ─────────────────────────────────────────────────────────────────────────────
func (v *TxVirtualExecutor) processBatchSubmitVirtual(
	updatedTx types.Transaction,
	inputData []byte,
) (types.Transaction, error, []byte) {
	sender := updatedTx.FromAddress()
	ccHandler, _ := cross_chain_handler.GetCrossChainHandler()
	acc := GetCCBatchVoteAccumulator()
	// chạy lần đầu
	// TryRecoverVotesOnce(v.chainState, v.storageManager, updatedTx)
	var embassies []cross_chain_handler.EmbassyInfo
	if ccHandler != nil {
		if !ccHandler.IsConfigLoaded() {
			err := ccHandler.EnsureConfigLoaded(v.chainState, updatedTx)
			if err != nil {
				logger.Warn("[VIRTUAL CC batchSubmit] ❌ Gặp lỗi khi load config: %v", err)
				return nil, fmt.Errorf("batchSubmit: failed to load config: %w", err), nil
			}
		}

		embassies = ccHandler.GetActiveEmbassyInfos()

		// Sync embassy count vào accumulator
		currentTotal := ccHandler.EmbassyCount()
		if acc.GetTotalEmbassies() != currentTotal {
			acc.SetTotalEmbassies(currentTotal)
		}
	} else {
		return nil, fmt.Errorf("batchSubmit: cross chain handler is nil"), nil
	}
	if len(embassies) == 0 {
		// Config đã load nhưng không có embassy nào active → reject
		logger.Warn("[VIRTUAL CC batchSubmit] ❌ No active embassies in config, reject TX from %s", sender.Hex())
		return nil, fmt.Errorf("batchSubmit: no active embassy registered"), nil
	}

	// ── 2. Verify nonce TX ────────────────────────────────────────────────
	// Nonce của TX phải đúng bằng nonce account tại thời điểm virtual.
	// Nếu sai → TX không hợp lệ, reject ngay (không vào block).
	txNonce := updatedTx.GetNonce()
	as, err := v.chainState.GetAccountStateDB().AccountState(sender)
	if err != nil {
		return nil, fmt.Errorf("batchSubmit: cannot get sender account state: %w", err), nil
	}
	if txNonce != as.Nonce() {
		logger.Warn("[VIRTUAL CC batchSubmit] ❌ Nonce mismatch: tx=%d, account=%d, sender=%s",
			txNonce, as.Nonce(), sender.Hex())
		return nil, fmt.Errorf("batchSubmit: invalid nonce tx=%d account=%d", txNonce, as.Nonce()), nil
	}

	// ── 3. Extract BLS pubkey từ ABI unpack → verify trực tiếp ────────
	// ABI: batchSubmit(EmbassyEvent[] events, bytes embassyPubKey)
	// Unpack 2 args: args[0] = events, args[1] = embassyPubKey (48 bytes)
	// Lookup pubkey trong embassy list → verify 1 lần duy nhất (O(1)).
	batchMethod, methodOk := ccHandler.GetABI().Methods["batchSubmit"]
	if !methodOk {
		return nil, fmt.Errorf("batchSubmit: method not found in ABI"), nil
	}
	batchArgs, unpackErr := batchMethod.Inputs.Unpack(inputData[4:])
	if unpackErr != nil || len(batchArgs) < 2 {
		return nil, fmt.Errorf("batchSubmit: ABI unpack failed (need 2 args): %v", unpackErr), nil
	}
	blsPubKeyBytes, pubKeyOk := batchArgs[1].([]byte)
	if !pubKeyOk || len(blsPubKeyBytes) == 0 {
		return nil, fmt.Errorf("batchSubmit: embassyPubKey arg invalid or empty"), nil
	}

	// Lookup pubkey trong danh sách embassy registered (so sánh bytes trực tiếp)
	var verifiedEmbassyAddr string
	var matchedPubKey mt_common.PublicKey
	for _, emb := range embassies {
		if len(emb.BlsPublicKey) == 0 {
			continue
		}
		if bytes.Equal(emb.BlsPublicKey, blsPubKeyBytes) {
			verifiedEmbassyAddr = emb.EthAddress.Hex()
			matchedPubKey = mt_common.PubkeyFromBytes(emb.BlsPublicKey)
			break
		}
	}
	if verifiedEmbassyAddr == "" {
		// Không tìm thấy trong cache → thử refresh từ on-chain 1 lần rồi lookup lại
		logger.Info("[VIRTUAL CC batchSubmit] BLS pubkey not in cached embassy list, refreshing from on-chain... sender=%s", sender.Hex())
		freshEmbassies := ccHandler.GetActiveEmbassyInfosWithRefresh(v.chainState, updatedTx)
		// Cập nhật lại count nếu embassy list đã thay đổi
		if newTotal := ccHandler.EmbassyCount(); acc.GetTotalEmbassies() != newTotal {
			acc.SetTotalEmbassies(newTotal)
		}
		for _, emb := range freshEmbassies {
			if len(emb.BlsPublicKey) == 0 {
				continue
			}
			if bytes.Equal(emb.BlsPublicKey, blsPubKeyBytes) {
				verifiedEmbassyAddr = emb.EthAddress.Hex()
				matchedPubKey = mt_common.PubkeyFromBytes(emb.BlsPublicKey)
				break
			}
		}
	}
	if verifiedEmbassyAddr == "" {
		logger.Warn("[VIRTUAL CC batchSubmit] ❌ BLS pubkey from calldata not found in embassy list (after on-chain refresh): sender=%s", sender.Hex())
		return nil, fmt.Errorf("batchSubmit: BLS pubkey does not match any active embassy"), nil
	}
	// Verify BLS signature với pubkey đã match (1 lần duy nhất thay vì loop tất cả)
	txHash := updatedTx.Hash().Bytes()
	txSign := updatedTx.Sign()
	if !bls.VerifySign(matchedPubKey, txSign, txHash) {
		logger.Warn("[VIRTUAL CC batchSubmit] ❌ BLS signature invalid for embassy=%s sender=%s",
			verifiedEmbassyAddr, sender.Hex())
		return nil, fmt.Errorf("batchSubmit: BLS signature verification failed for embassy %s", verifiedEmbassyAddr), nil
	}
	logger.Info("[VIRTUAL CC batchSubmit] ✅ BLS verified (direct lookup): embassy=%s sender=%s", verifiedEmbassyAddr, sender.Hex())

	// ── 4. Vote KEY = sha256(ABI(events only)) — loại bỏ pubkey ──────────
	// Re-pack chỉ arg events (args[0]) → tất cả embassy cùng hash vì events giống nhau.
	eventsOnlyPacked, packErr := batchMethod.Inputs[:1].Pack(batchArgs[0])
	var key [32]byte
	if packErr != nil {
		// Fallback: hash toàn bộ inputData nếu re-pack lỗi
		logger.Warn("[VIRTUAL CC batchSubmit] ⚠️ Failed to re-pack events for vote key: %v, using inputData fallback", packErr)
		return nil, fmt.Errorf("batchSubmit: failed to re-pack events for vote key: %v", packErr), nil
	} else {
		key = sha256.Sum256(eventsOnlyPacked)
	}
	// ── 5. Tích lũy vote ──────────────────────────────────────────────────
	voteCount, isFirstQuorum, voteErr := acc.AddVoteByKey(verifiedEmbassyAddr, key)
	if voteErr != nil {
		// Duplicate vote hoặc đã execute: ReadOnly=true → master tạo receipt ngay, không execute
		logger.Info("[VIRTUAL CC batchSubmit] ⏭ Skip vote: %v", voteErr)
		updatedTx.SetReadOnly(true)
		return updatedTx, nil, []byte(fmt.Sprintf("sig_ack:skip:%s", voteErr.Error()))
	}

	total := acc.GetTotalEmbassies()
	q := acc.quorum(total)

	if !isFirstQuorum {
		// Chưa đủ quorum: ReadOnly=true → master tạo receipt ngay (nonce-only), không execute state.
		logger.Info("[VIRTUAL CC batchSubmit] ⏳ SIG_ACK %d/%d (quorum=%d) embassy=%s key=%x",
			voteCount, total, q, verifiedEmbassyAddr, key[:8])
		updatedTx.SetReadOnly(true)
		return updatedTx, nil, []byte(fmt.Sprintf("sig_ack:%d/%d", voteCount, total))
	}

	// ── Đủ quorum lần đầu → EXECUTE ─────────────────────────────────────
	// ReadOnly=false (mặc định) → master gọi GetCrossChainHandler().HandleTransaction
	logger.Info("[VIRTUAL CC batchSubmit] 🚀 EXECUTE (quorum %d/%d) embassy=%s key=%x",
		voteCount, total, verifiedEmbassyAddr, key[:8])
	updatedTx.SetReadOnly(false)

	// ── Fake EVM dry-run để lấy relatedAddresses ─────────────────────────
	// Chỉ làm cho CC_EXECUTE (SIG_ACK không execute state nên không cần).
	// Với mỗi INBOUND packet có Target != address{} (sendMessage path),
	// ta gọi giả EVM với đúng payload của packet để EVM touch đúng storage slots,
	// từ đó GetCurrentRelatedAddresses() trả về danh sách chính xác.
	//
	// QUAN TRỌNG: Mỗi contract dry-run dùng mvmId RIÊNG để tránh:
	//   1. C++ state cache (State::instances) bị lẫn lộn giữa các contract — mỗi
	//      Execute ghi dirty state vào C++ cache theo key mvmId, nếu dùng chung thì
	//      lần sau đọc nhầm storage của contract trước.
	//   2. currentRelatedAddresses (sync.Map trên MVMApi) accumulate tất cả địa chỉ
	//      từ mọi lần Execute — dùng chung sẽ không biết địa chỉ nào thuộc target nào.
	ctx := context.Background()
	targets := ccHandler.ExtractInboundTargets(inputData)
	if len(targets) > 0 {
		blockTime := uint64(time.Now().Unix())
		for i, item := range targets {
			if item.Target == (common.Address{}) {
				// ── lockAndBridge path: không chạy EVM dry-run ──────────────────
				// Target rỗng → đây là mint native coin, chỉ cần thêm Recipient
				// vào relatedAddresses để chain xử lý sequential đúng cách.
				if item.Recipient != (common.Address{}) {
					updatedTx.AddRelatedAddress(item.Recipient)
				} else {
					// Không parse được recipient → log warn và skip (không reject cả batchSubmit).
					// Virtual processor chỉ collect relatedAddresses; nếu thiếu recipient
					// thì chain vẫn xử lý được, chỉ có thể không sequential-safe cho address đó.
					logger.Warn("[VIRTUAL CC batchSubmit] ⚠️ lockAndBridge: could not parse recipient from payload (sender=%s), skip",
						item.Sender.Hex())
				}
				continue
			}
			// ── sendMessage path: EVM dry-run với mvmId riêng cho từng target ──
			// Mỗi target có mvmId = hash(txHash + target + index) để C++ state cache
			// và relatedAddresses hoàn toàn độc lập nhau.
			itemHash := sha256.Sum256([]byte(fmt.Sprintf("batchsubmit-virtual-%x-%s-%d",
				updatedTx.Hash(), item.Target.Hex(), i)))
			itemMvmId := common.BytesToAddress(itemHash[12:])
			vmP := vm_processor.NewVmProcessor(v.chainState, itemMvmId, false, blockTime)

			// Validate: target phải là contract hợp lệ
			toAccountState, err := v.chainState.GetAccountStateDB().AccountState(item.Target)
			if err != nil || !vmP.IsValidSmartContractCall(toAccountState, updatedTx) {
				logger.Warn("[VIRTUAL CC batchSubmit] target=%s is not a valid contract, skip dry-run", item.Target.Hex())
				updatedTx.AddRelatedAddress(item.Target) // vẫn thêm địa chỉ để đảm bảo sequential
				mvm.ClearMVMApi(itemMvmId)
				continue
			}
			// Fake call với đúng payload của packet để EVM simulate đúng code path,
			// touch đúng storage slots → relatedAddresses sẽ khớp với lúc execute thật.
			fakeCallTx := proxy_tx.New(updatedTx, updatedTx.FromAddress(), item.Target,
				updatedTx.Amount(), uint64(mt_common.MAX_GASS_FEE), 0, item.Payload)
			_, _, _ = vmP.ExecuteTransactionWithMvmIdSub(ctx, fakeCallTx, true)
			mvmApi := mvm.GetMVMApi(itemMvmId)
			if mvmApi != nil {
				for _, addr := range mvmApi.GetCurrentRelatedAddresses() {
					updatedTx.AddRelatedAddress(addr)
				}
			}
			mvm.ClearMVMApi(itemMvmId)
			logger.Info("[VIRTUAL CC batchSubmit] 🔍 dry-run target=%s sender=%s payload=%dB → collected relatedAddresses: %v",
				item.Target.Hex(), item.Sender.Hex(), len(item.Payload), updatedTx.RelatedAddresses())
		}
	}

	return updatedTx, nil, []byte(fmt.Sprintf("execute:%d/%d", voteCount, total))
}
