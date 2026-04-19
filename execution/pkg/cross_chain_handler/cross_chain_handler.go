package cross_chain_handler

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	crosschainabi "github.com/meta-node-blockchain/meta-node/pkg/cross_chain_handler/abi"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/utils/receipt_helper"
	"github.com/meta-node-blockchain/meta-node/types"
)

// OffChainProcessor interface để gọi off-chain (tránh import cycle với tx_processor)
type OffChainProcessor interface {
	ProcessTransactionOffChain(tx types.Transaction) (types.ExecuteSCResult, error)
}

// CrossChainHandler xử lý các giao dịch cross-chain gửi đến CROSS_CHAIN_CONTRACT_ADDRESS (0x1002)
// Pattern giống ValidatorHandler: singleton, parse ABI 1 lần, dispatch theo method name
type CrossChainHandler struct {
	abi       abi.ABI // Gateway ABI (lockAndBridge, sendMessage, MessageSent event)
	configABI abi.ABI // Config Registry ABI (chainId, getRegisteredChainIds)

	// OffChainProcessor dùng để gọi contract off-chain (lưu lại sau lần đầu EnsureConfigLoaded)
	offChainProcessor OffChainProcessor

	// Cached config từ CrossChainConfigRegistry contract
	cachedChainId            *big.Int                // chainId của chain hiện tại (sourceId)
	cachedRegisteredChainIds []*big.Int              // danh sách nationId đã đăng ký
	cachedEmbassies          map[common.Address]bool // ETH address → isActive
	cachedEmbassyInfos       []EmbassyInfo           // danh sách embassy (đảm bảo chỉ lưu embassy active từ smart contract)
	cachedEmbassyCount       int                     // tổng số embassy active (dùng tính quorum)
	configLoaded             atomic.Bool             // đã load config chưa (atomic để tránh data race read-without-lock)
	configMu                 sync.Mutex              // chỉ dùng khi loadConfig

	// ─── Vote Tracking for batchSubmit ───────────────────────────────────────
	// Key: eventVoteKey (sha256 của event canonical data)
	// Value: *eventVoteState
	voteMap sync.Map
}

var (
	crossChainHandlerInstance *CrossChainHandler
	onceCrossChain            sync.Once
	// Package-level OffChainProcessor — set 1 lần khi khởi tạo node
	globalOffChainProcessor OffChainProcessor
)

// GetCrossChainHandler trả về instance duy nhất của CrossChainHandler
func GetCrossChainHandler() (*CrossChainHandler, error) {
	var err error
	onceCrossChain.Do(func() {
		var parsedABI abi.ABI
		parsedABI, err = abi.JSON(strings.NewReader(crosschainabi.CCGatewayABI))
		if err != nil {
			return
		}

		var parsedConfigABI abi.ABI
		parsedConfigABI, err = abi.JSON(strings.NewReader(crosschainabi.CrossChainConfigABI))
		if err != nil {
			return
		}

		crossChainHandlerInstance = &CrossChainHandler{
			abi:       parsedABI,
			configABI: parsedConfigABI,
		}
	})

	if err != nil {
		return nil, err
	}
	return crossChainHandlerInstance, nil
}

// SetOffChainProcessor lưu OffChainProcessor ở mức package.
// Gọi 1 lần khi khởi tạo node (trong NewTransactionProcessor).
func SetOffChainProcessor(tp OffChainProcessor) {
	globalOffChainProcessor = tp
}

// IsConfigLoaded kiểm tra config đã được load chưa
func (h *CrossChainHandler) IsConfigLoaded() bool {
	return h.configLoaded.Load()
}

// GetABI trả về parsed Gateway ABI.
// Virtual processor dùng để unpack batchSubmit args.
func (h *CrossChainHandler) GetABI() abi.ABI {
	return h.abi
}

// EmbassyCount trả về tổng số embassy active đang cached.
// Trả về 0 nếu config chưa load.
func (h *CrossChainHandler) EmbassyCount() int {
	if !h.configLoaded.Load() {
		return 0
	}
	return h.cachedEmbassyCount
}

// GetActiveEmbassyInfos trả về danh sách embassy đang active.
// Sub server dùng để verify chữ ký giao dịch batchSubmit độc lập với master.
// Trả về slice rỗng (không phải nil) nếu chưa load.
func (h *CrossChainHandler) GetActiveEmbassyInfos() []EmbassyInfo {
	if !h.configLoaded.Load() {
		return []EmbassyInfo{}
	}
	// Trả về copy để đảm bảo an toàn cho slice backing array
	result := make([]EmbassyInfo, len(h.cachedEmbassyInfos))
	copy(result, h.cachedEmbassyInfos)
	return result
}

// ═══════════════════════════════════════════════════════════════════════
// CONFIG LOADING
// Chỉ dùng mutex lúc loadConfig (double-checked locking).
// Sau khi configLoaded = true, read trực tiếp không cần lock (write-once).
// ═══════════════════════════════════════════════════════════════════════

// EnsureConfigLoaded load config từ contract nếu chưa có.
// Dùng globalOffChainProcessor (được set qua SetOffChainProcessor).
func (h *CrossChainHandler) EnsureConfigLoaded(chainState *blockchain.ChainState, originalTx types.Transaction) error {
	if h.configLoaded.Load() {
		return nil
	}
	h.configMu.Lock()
	defer h.configMu.Unlock()

	if h.configLoaded.Load() {
		return nil
	}
	if globalOffChainProcessor == nil {
		return fmt.Errorf("cross-chain: OffChainProcessor not set (call SetOffChainProcessor first)")
	}
	// Lưu vào struct để RefreshConfig dùng lại
	h.offChainProcessor = globalOffChainProcessor

	cfg := chainState.GetConfig()
	if cfg == nil {
		return fmt.Errorf("cross-chain: chainState config is nil")
	}
	configContractAddrHex := cfg.CrossChain.ConfigContract
	if configContractAddrHex == "" {
		return fmt.Errorf("cross-chain: config_contract not set in config")
	}
	configContractAddr := common.HexToAddress(configContractAddrHex)
	// 1. Fetch chainId (helper dùng originalTx.FromAddress() và originalTx.GetChainID())
	chainId, err := FetchChainId(h.configABI, globalOffChainProcessor, configContractAddr, originalTx)
	if err != nil {
		return fmt.Errorf("EnsureConfigLoaded: %v", err)
	}
	// 2. Fetch registeredChainIds
	registeredIds, err := FetchRegisteredChainIds(h.configABI, globalOffChainProcessor, configContractAddr, originalTx)
	if err != nil {
		return fmt.Errorf("EnsureConfigLoaded: %v", err)
	}
	h.cachedChainId = chainId
	h.cachedRegisteredChainIds = registeredIds
	// 3. Fetch embassy list (bao gồm ETH address để verify batchSubmit sender)
	embassyInfos, err := FetchAllEmbassies(h.configABI, globalOffChainProcessor, configContractAddr, originalTx)
	if err != nil {
		// Không fatal — embassy list trống → batchSubmit sẽ từ chối tất cả
		logger.Warn("EnsureConfigLoaded: FetchAllEmbassies warn: %v (batchSubmit sẽ bị từ chối)", err)
		h.cachedEmbassies = make(map[common.Address]bool)
		h.cachedEmbassyCount = 0
	} else {
		em := make(map[common.Address]bool, len(embassyInfos))
		active := 0
		var activeInfos []EmbassyInfo
		for _, info := range embassyInfos {
			if info.IsActive {
				em[info.EthAddress] = true
				activeInfos = append(activeInfos, info)
				active++
			}
		}
		h.cachedEmbassies = em
		h.cachedEmbassyInfos = activeInfos // Chỉ lưu những embassy active giống contract
		h.cachedEmbassyCount = active
		logger.Info("CrossChain embassy cache loaded: total=%d, active=%d", len(embassyInfos), active)
	}

	h.configLoaded.Store(true)

	logger.Info("CrossChain config loaded: chainId=%s, registeredChainIds=%v, embassies=%d",
		chainId.String(), registeredIds, h.cachedEmbassyCount)

	return nil
}

// isDestinationRegistered kiểm tra destinationId có nằm trong registeredChainIds không
// Không cần lock — cachedRegisteredChainIds chỉ write 1 lần trong ensureConfigLoaded
func (h *CrossChainHandler) isDestinationRegistered(destinationId *big.Int) bool {
	for _, id := range h.cachedRegisteredChainIds {
		if id.Cmp(destinationId) == 0 {
			return true
		}
	}
	return false
}

// isDestinationRegisteredWithRefresh kiểm tra destinationId trong cache;
// nếu không tìm thấy → thử RefreshConfig 1 lần rồi kiểm tra lại.
// Dùng khi chain có thể đã update registeredChainIds mà node chưa reload.
func (h *CrossChainHandler) isDestinationRegisteredWithRefresh(
	destinationId *big.Int,
	chainState *blockchain.ChainState,
	originalTx types.Transaction,
) bool {
	if h.isDestinationRegistered(destinationId) {
		return true
	}
	// Không tìm thấy trong cache → thử refresh và kiểm tra lại
	logger.Info("[CrossChain] destinationId=%s not in cache, refreshing config...", destinationId.String())
	if err := h.RefreshConfig(chainState, originalTx); err != nil {
		logger.Warn("[CrossChain] RefreshConfig failed: %v", err)
		return false
	}
	result := h.isDestinationRegistered(destinationId)
	if result {
		logger.Info("[CrossChain] destinationId=%s found after config refresh ✅", destinationId.String())
	} else {
		logger.Warn("[CrossChain] destinationId=%s still not found after config refresh ❌", destinationId.String())
	}
	return result
}

// GetActiveEmbassyInfosWithRefresh trả về active embassy list;
// nếu rỗng (chưa có embassy nào) → thử RefreshConfig 1 lần.
func (h *CrossChainHandler) GetActiveEmbassyInfosWithRefresh(
	chainState *blockchain.ChainState,
	originalTx types.Transaction,
) []EmbassyInfo {
	infos := h.GetActiveEmbassyInfos()
	if len(infos) > 0 {
		return infos
	}
	logger.Info("[CrossChain] No active embassies in cache, refreshing config...")
	if err := h.RefreshConfig(chainState, originalTx); err != nil {
		logger.Warn("[CrossChain] RefreshConfig for embassies failed: %v", err)
		return infos
	}
	infos = h.GetActiveEmbassyInfos()
	if len(infos) > 0 {
		logger.Info("[CrossChain] Found %d active embassies after config refresh ✅", len(infos))
	} else {
		logger.Warn("[CrossChain] Still no active embassies after config refresh ❌")
	}
	return infos
}

// RefreshConfig force reload config (useful khi registeredChainIds thay đổi)
func (h *CrossChainHandler) RefreshConfig(chainState *blockchain.ChainState, originalTx types.Transaction) error {
	h.configMu.Lock()
	h.configLoaded.Store(false)
	h.configMu.Unlock()
	return h.EnsureConfigLoaded(chainState, originalTx)
}

// ═══════════════════════════════════════════════════════════════════════
// TRANSACTION HANDLING — Dispatch
// ═══════════════════════════════════════════════════════════════════════

// HandleTransaction xử lý transaction gửi đến CROSS_CHAIN_CONTRACT_ADDRESS
func (h *CrossChainHandler) HandleTransaction(
	ctx context.Context,
	chainState *blockchain.ChainState,
	tx types.Transaction,
	mvmId common.Address,
	enableTrace bool,
	blockTime uint64,
) (types.Receipt, types.ExecuteSCResult, bool) {
	toAddress := tx.ToAddress()
	inputData := tx.CallData().Input()
	if len(inputData) < 4 {
		err := fmt.Errorf("cross-chain: invalid input data (less than 4 bytes)")
		rcp := receipt_helper.CreateErrorReceipt(tx, toAddress, err)
		return rcp, nil, true
	}

	method, err := h.abi.MethodById(inputData[:4])
	if err != nil {
		logger.Error("CrossChain: method not found for selector 0x%x: %v", inputData[:4], err)
		rcp := receipt_helper.CreateErrorReceipt(tx, toAddress, err)
		return rcp, nil, true
	}

	// Tự động load config nếu chưa có (dùng globalOffChainProcessor + nil check explorer)
	if err := h.EnsureConfigLoaded(chainState, tx); err != nil {
		logger.Error("CrossChain %s: EnsureConfigLoaded error: %v", method.Name, err)
		return receipt_helper.HandleRevertedTx(ctx, chainState, tx, toAddress, blockTime, enableTrace, err.Error())
	}

	var eventLogs []types.EventLog
	var exRs types.ExecuteSCResult
	var logicErr error

	switch method.Name {
	case "lockAndBridge":
		eventLogs, exRs, logicErr = h.handleLockAndBridge(ctx, chainState, tx, method, inputData[4:], mvmId, enableTrace, blockTime)
	case "sendMessage":
		eventLogs, exRs, logicErr = h.handleSendMessage(ctx, chainState, tx, method, inputData[4:], mvmId, enableTrace, blockTime)
	case "batchSubmit":
		eventLogs, exRs, logicErr = h.handleBatchSubmit(ctx, chainState, tx, method, inputData[4:], mvmId, enableTrace, blockTime)
	default:
		logicErr = fmt.Errorf("cross-chain: unsupported method '%s'", method.Name)
	}

	if logicErr != nil {
		logger.Error("CrossChain %s error: %v", method.Name, logicErr)
		return receipt_helper.HandleRevertedTx(ctx, chainState, tx, toAddress, blockTime, enableTrace, logicErr.Error())
	}
	if exRs != nil {
		logger.Info("cc_exRs: %v", exRs)
		return receipt_helper.HandleSuccessTxWithExRs(chainState, tx, toAddress, eventLogs, exRs)
	}
	return receipt_helper.HandleSuccessTx(ctx, chainState, tx, toAddress, blockTime, enableTrace, eventLogs, nil)
}

// mustType parse ABI type, panic nếu lỗi
func mustType(t string) abi.Type {
	typ, err := abi.NewType(t, "", nil)
	if err != nil {
		panic(fmt.Sprintf("invalid abi type: %s", t))
	}
	return typ
}
