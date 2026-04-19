package blockchain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract_db"
	stake_state_db "github.com/meta-node-blockchain/meta-node/pkg/state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/types"
)

// EpochData chứa thông tin epoch để persist vào database
type EpochData struct {
	CurrentEpoch          uint64            `json:"current_epoch"`
	EpochStartTimestampMs uint64            `json:"epoch_start_timestamp_ms"`
	EpochStartTimestamps  map[uint64]uint64 `json:"epoch_start_timestamps"`
	// NEW: Track boundary block for each epoch (last block of previous epoch)
	EpochBoundaryBlocks map[uint64]uint64 `json:"epoch_boundary_blocks"`
	// NEW: Track boundary GEI for each epoch
	EpochBoundaryGeis map[uint64]uint64 `json:"epoch_boundary_geis"`
}

// ChainState quản lý trạng thái toàn cục của blockchain
type ChainState struct {
	config *config.SimpleChainConfig

	currentBlockHeader atomic.Pointer[types.BlockHeader]
	storageManager     *storage.StorageManager
	accountStateDB     *account_state_db.AccountStateDB
	smartContractDB    *smart_contract_db.SmartContractDB
	blockDatabase      *block.BlockDatabase
	stakeStateDB       *stake_state_db.StakeStateDB
	freeFeeAddress     map[common.Address]struct{}

	// stateMutex protects accountStateDB, smartContractDB, stakeStateDB from
	// concurrent access during UpdateStateForNewHeader (writer) and Get*DB (readers).
	// Without this, virtual execution can race with block processing and read a
	// stale accountStateDB that doesn't contain newly deployed contract state.
	stateMutex sync.RWMutex

	// Sui-style epoch tracking
	currentEpoch          uint64
	epochStartTimestampMs uint64
	epochStartTimestamps  map[uint64]uint64 // epoch -> timestamp_ms mapping
	epochBoundaryBlocks   map[uint64]uint64 // NEW: epoch -> boundary_block mapping (last block of prev epoch)
	epochBoundaryGeis     map[uint64]uint64 // NEW: epoch -> boundary_gei mapping
	maxCachedEpochs       uint64            // 0 = keep all, N = keep only N most recent epochs in cache

	// RWMutex to prevent concurrent epoch map access
	// Writers: AdvanceEpochWithBoundary, CheckAndUpdateEpochFromBlock, PruneEpochCache, InitializeGenesisEpoch
	// Readers: GetCurrentEpoch, GetEpochBoundaryBlock, GetEpochStartTimestamp, GetCurrentEpochStartTimestampMs
	epochMutex sync.RWMutex

	// Node-specific backup path for epoch data persistence
	// This prevents epoch collision when multiple nodes run on the same machine
	backupPath string

	// Callback for epoch changes (to notify Rust)
	epochNotificationCallback func(uint64, uint64, uint64)

	// State attestation interval (in blocks) - from genesis config
	attestationInterval uint64
}

// NewChainState tạo một đối tượng ChainState mới.
// Nó cần một StorageManager và header của block cuối cùng (lastHeader) đã biết.
// CRITICAL: backupPath must be set to prevent epoch collision between nodes on same machine
func NewChainState(
	sm *storage.StorageManager,
	blockDatabase *block.BlockDatabase,
	currentBlockHeader types.BlockHeader,
	config *config.SimpleChainConfig,
	freeFeeAddress map[common.Address]struct{},
	backupPath string,
) (*ChainState, error) {
	return NewChainStateWithGenesis(sm, blockDatabase, currentBlockHeader, config, freeFeeAddress, nil, backupPath)
}

// NewChainStateWithGenesis tạo một đối tượng ChainState mới với thông tin genesis.
// CRITICAL: backupPath must be set to prevent epoch collision between nodes on same machine
func NewChainStateWithGenesis(
	sm *storage.StorageManager,
	blockDatabase *block.BlockDatabase,
	currentBlockHeader types.BlockHeader,
	config *config.SimpleChainConfig,
	freeFeeAddress map[common.Address]struct{},
	genesisConfig *config.GenesisConfig,
	backupPath string,
) (*ChainState, error) {
	// Create account state trie from existing root
	// CRITICAL: Must use NewStateTrie() (factory) to match the backend used by AccountStateDB.Commit().
	// Using trie.New() (MPT hardcoded) would fail when backend=flat because flat entries (fs:*) are stored
	// instead of MPT trie nodes.
	accountStorage := sm.GetStorageAccount()
	accountStateTrie, err := trie.NewStateTrie(currentBlockHeader.AccountStatesRoot(), accountStorage, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create account state trie: %v", err)
	}
	stakeStorage := sm.GetStorageStake()

	stakeStateTrie, err := trie.NewStateTrie(common.Hash(currentBlockHeader.StakeStatesRoot()), stakeStorage, true)

	if err != nil {
		return nil, fmt.Errorf("failed to create stake state trie: %v", err)
	}

	asDB := account_state_db.NewAccountStateDB(accountStateTrie, accountStorage)

	stakeStateDB := stake_state_db.NewStakeStateDB(stakeStateTrie, stakeStorage)

	scDB := smart_contract_db.NewSmartContractDB(
		sm.GetStorageCode(),
		sm.GetStorageSmartContract(),
		asDB)

	// Determine maxCachedEpochs from config
	var maxCached uint64 = 10 // sensible default
	if config != nil && config.MaxCachedEpochs > 0 {
		maxCached = config.MaxCachedEpochs
	}

	cs := &ChainState{
		storageManager:        sm,
		accountStateDB:        asDB,
		stakeStateDB:          stakeStateDB,
		smartContractDB:       scDB,
		config:                config,
		blockDatabase:         blockDatabase,
		freeFeeAddress:        freeFeeAddress,
		maxCachedEpochs:       maxCached,
		currentEpoch:          0, // Start with epoch 0 (genesis)
		epochStartTimestampMs: 0, // Will be set on first epoch advance
		epochStartTimestamps:  make(map[uint64]uint64),
		epochBoundaryBlocks:   make(map[uint64]uint64), // Track epoch boundary blocks
		epochBoundaryGeis:     make(map[uint64]uint64), // Track epoch boundary GEIs
		backupPath:            backupPath,              // CRITICAL: Set BEFORE LoadEpochData()
		attestationInterval:   10,                      // Default: attestation every 10 blocks
	}

	// CRITICAL: Log backup path to verify node-specific path is used
	if backupPath == "" {
		logger.Warn("⚠️ [EPOCH PERSISTENCE] backupPath is empty - epoch data will use fallback /tmp path (NOT RECOMMENDED for multi-node setups)")
	} else {
		logger.Info("📁 [EPOCH PERSISTENCE] Using node-specific backup path: %s", backupPath)
	}

	// Try to load persisted epoch data first (NOW USING CORRECT NODE-SPECIFIC PATH)
	logger.Info("🔄 [EPOCH PERSISTENCE] Attempting to load epoch data from database...")
	if err := cs.LoadEpochData(); err != nil {
		logger.Warn("Failed to load epoch data from database, will use genesis config", "error", err)
	} else {
		logger.Info("✅ [EPOCH PERSISTENCE] Successfully loaded epoch data - current_epoch={}, epoch_timestamp_ms={}",
			cs.currentEpoch, cs.epochStartTimestampMs)
	}

	// Initialize genesis epoch only if no persisted data was loaded
	if cs.currentEpoch == 0 && cs.epochStartTimestampMs == 0 {
		// Set attestation interval from genesis config
		if genesisConfig != nil && genesisConfig.AttestationInterval > 0 {
			cs.attestationInterval = genesisConfig.AttestationInterval
			logger.Info("🔏 [ATTESTATION] Interval set from genesis: every %d blocks", cs.attestationInterval)
		}

		if genesisConfig != nil && genesisConfig.EpochTimestampMs > 0 {
			cs.InitializeGenesisEpoch(genesisConfig.EpochTimestampMs)
		} else {
			// CRITICAL FIX: Use DETERMINISTIC timestamp from the current block header
			// instead of time.Now() which causes epoch mismatch across different Go instances
			// The currentBlockHeader is passed during construction and should be consistent
			// across all nodes with the same blockchain state.
			var deterministicTimestamp uint64
			if currentBlockHeader != nil && currentBlockHeader.TimeStamp() > 0 {
				// Convert seconds to milliseconds
				deterministicTimestamp = currentBlockHeader.TimeStamp() * 1000
				logger.Info("🔧 [GENESIS EPOCH] Derived timestamp from currentBlockHeader: block=%d, timestamp_s=%d, timestamp_ms=%d",
					currentBlockHeader.BlockNumber(), currentBlockHeader.TimeStamp(), deterministicTimestamp)
			} else {
				// Absolute fallback: Use a fixed known timestamp (e.g., epoch 0 = 0)
				// This should rarely happen in production
				deterministicTimestamp = 0
				logger.Warn("⚠️ [GENESIS EPOCH] No valid block header available, using epoch 0 timestamp=0")
			}
			cs.InitializeGenesisEpoch(deterministicTimestamp)
		}

		// Save initial genesis epoch data
		if err := cs.SaveEpochData(); err != nil {
			logger.Warn("Failed to save initial genesis epoch data", "error", err)
		}
	}

	headerCopy := currentBlockHeader
	cs.currentBlockHeader.Store(&headerCopy)

	return cs, nil // Trả về ChainState đã tạo và nil error
}

// UpdateStateForNewHeader cập nhật trạng thái dựa trên header mới.
// Hàm này sẽ cập nhật con trỏ header và khởi tạo lại các DB trạng thái liên quan.
func (cs *ChainState) UpdateStateForNewHeader(newHeader types.BlockHeader) error {
	if newHeader == nil {
		return fmt.Errorf("cannot update state with a nil header")
	}
	// 1. Khởi tạo lại AccountStateDB với root mới
	accountStorage := cs.storageManager.GetStorageAccount()
	newAccountRoot := newHeader.AccountStatesRoot() // Lấy root từ header mới
	newAccountStateTrie, err := trie.NewStateTrie(newAccountRoot, accountStorage, true)
	if err != nil {
		logger.Error("Failed to create new account state trie during update", "error", err, "newRoot", newAccountRoot)
		return fmt.Errorf("failed to create new account state trie for update: %w", err)
	}
	newAsDB := account_state_db.NewAccountStateDB(newAccountStateTrie, accountStorage)

	// 2. Khởi tạo lại StakeStateDB với root mới
	stakeStorage := cs.storageManager.GetStorageStake()
	newStakeRoot := common.Hash(newHeader.StakeStatesRoot()) // Lấy root từ header mới
	newStakeStateTrie, err := trie.NewStateTrie(newStakeRoot, stakeStorage, true)
	if err != nil {
		logger.Error("Failed to create new stake state trie during update", "error", err, "newRoot", newStakeRoot)
		return fmt.Errorf("failed to create new stake state trie for update: %w", err)
	}
	newStakeStateDB := stake_state_db.NewStakeStateDB(newStakeStateTrie, stakeStorage)

	// 3. Khởi tạo lại SmartContractDB với AccountStateDB mới
	// (Giả sử config không thay đổi)
	newScDB := smart_contract_db.NewSmartContractDB(
		cs.storageManager.GetStorageCode(),
		cs.storageManager.GetStorageSmartContract(),
		newAsDB, // Sử dụng asDB mới tạo
	)

	// 4. Cập nhật các trường trong ChainState — WRITE LOCK to prevent virtual
	// execution from reading stale DB references during the swap.
	cs.stateMutex.Lock()
	if cs.accountStateDB != nil {
		if closer, ok := interface{}(cs.accountStateDB).(interface{ Close() }); ok {
			closer.Close()
		}
	}
	if cs.stakeStateDB != nil {
		if closer, ok := interface{}(cs.stakeStateDB.Trie()).(interface{ Close() }); ok {
			closer.Close()
		}
	}
	cs.accountStateDB = newAsDB
	cs.stakeStateDB = newStakeStateDB
	cs.smartContractDB = newScDB
	cs.stateMutex.Unlock()

	// 5. Cập nhật con trỏ header nguyên tử
	headerCopy := newHeader
	cs.currentBlockHeader.Store(&headerCopy)

	// logger.Info("ChainState updated for new header", "blockNumber", newHeader.BlockNumber(), "accountRoot", newAccountRoot, "stakeRoot", newStakeRoot)
	return nil
}

// NewChainState tạo một đối tượng ChainState mới.
// Nó cần một StorageManager và header của block cuối cùng (lastHeader) đã biết.
func NewChainStateRemote(
	currentBlockHeader types.BlockHeader,
	accountStorage,
	codeStorage, dbSmartContract storage.Storage,
	freeFeeAddress map[common.Address]struct{},
) (*ChainState, error) {

	// Create account state trie from existing root
	accountStateTrie, err := trie.NewStateTrie(currentBlockHeader.AccountStatesRoot(), accountStorage, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create account state trie: %v", err)
	}
	asDB := account_state_db.NewAccountStateDB(accountStateTrie, accountStorage)
	scDB := smart_contract_db.NewSmartContractDB(
		codeStorage,
		dbSmartContract,
		asDB)

	cs := &ChainState{
		accountStateDB:  asDB,
		smartContractDB: scDB,
		freeFeeAddress:  freeFeeAddress,
	}

	headerCopy := currentBlockHeader
	cs.currentBlockHeader.Store(&headerCopy)

	return cs, nil // Trả về ChainState đã tạo và nil error
}

// GetConfig trả về cấu hình của ChainState.
func (cs *ChainState) GetConfig() *config.SimpleChainConfig {
	return cs.config
}

// GetAttestationInterval returns the state attestation interval (in blocks).
// Returns 0 if attestation is disabled.
func (cs *ChainState) GetAttestationInterval() uint64 {
	return cs.attestationInterval
}
func (cs *ChainState) TransferFrom(from, to types.AccountState, amount *big.Int) error {
	if from == nil || to == nil {
		return errors.New("invalid account: from or to is nil")
	}
	if amount == nil {
		return errors.New("invalid amount: nil")
	}
	if amount.Cmp(big.NewInt(0)) < 0 {
		return errors.New("amount must be greater than zero")
	}
	// Trừ bên gửi
	err := from.SubTotalBalance(amount)
	if err != nil {
		return err
	}
	// Cộng bên nhận
	to.AddPendingBalance(amount)
	return nil
}

// GetcurrentBlock trả về header của block cuối cùng một cách an toàn.
// Trả về nil nếu chưa có header nào được đặt.
func (cs *ChainState) GetcurrentBlockHeader() *types.BlockHeader {
	return cs.currentBlockHeader.Load()
}

// SetcurrentBlock cập nhật header của block cuối cùng một cách an toàn.
func (cs *ChainState) SetcurrentBlockHeader(header *types.BlockHeader) {
	cs.currentBlockHeader.Store(header)
}

func (cs *ChainState) GetAccountStateDB() *account_state_db.AccountStateDB {
	cs.stateMutex.RLock()
	db := cs.accountStateDB
	cs.stateMutex.RUnlock()
	return db
}

// GetSmartContractDB trả về SmartContractDB.
func (cs *ChainState) GetSmartContractDB() *smart_contract_db.SmartContractDB {
	cs.stateMutex.RLock()
	db := cs.smartContractDB
	cs.stateMutex.RUnlock()
	return db
}

func (cs *ChainState) GetStakeStateDB() *stake_state_db.StakeStateDB {
	cs.stateMutex.RLock()
	db := cs.stakeStateDB
	cs.stateMutex.RUnlock()
	return db
}

// GetStorageManager trả về StorageManager.
func (cs *ChainState) GetStorageManager() *storage.StorageManager {
	return cs.storageManager
}

// SetAccountStateDB đặt AccountStateDB cho ChainState.
// Lưu ý: Việc thay đổi trực tiếp DB này có thể ảnh hưởng đến tính nhất quán
// của trạng thái nếu không được quản lý cẩn thận trong quy trình xử lý block.
func (cs *ChainState) SetAccountStateDB(asDB *account_state_db.AccountStateDB) {
	cs.accountStateDB = asDB
}

// GetBlockDatabase trả về BlockDatabase.
func (cs *ChainState) GetBlockDatabase() *block.BlockDatabase {
	return cs.blockDatabase
}

func (cs *ChainState) GetFreeFeeAddress() map[common.Address]struct{} {
	return cs.freeFeeAddress
}

// Sui-style epoch methods

// GetCurrentEpoch returns the current epoch number
func (cs *ChainState) GetCurrentEpoch() uint64 {
	cs.epochMutex.RLock()
	defer cs.epochMutex.RUnlock()
	return cs.currentEpoch
}

// GetEpochStartTimestamp returns the start timestamp for a given epoch
func (cs *ChainState) GetEpochStartTimestamp(epoch uint64) (uint64, error) {
	cs.epochMutex.RLock()
	defer cs.epochMutex.RUnlock()
	if timestamp, exists := cs.epochStartTimestamps[epoch]; exists {
		return timestamp, nil
	}
	return 0, fmt.Errorf("epoch %d not found", epoch)
}

// GetCurrentEpochStartTimestampMs returns the start timestamp of the current epoch
func (cs *ChainState) GetCurrentEpochStartTimestampMs() uint64 {
	cs.epochMutex.RLock()
	defer cs.epochMutex.RUnlock()
	return cs.epochStartTimestampMs
}

// GetEpochBoundaryBlock returns the boundary block (last block of prev epoch) for a given epoch
// For epoch 0 (genesis), returns 0 as there is no previous epoch
// CRITICAL: DO NOT fallback to current block number - this causes fork when late-joining nodes
// fetch committee at current block (with new validators) instead of epoch's actual boundary
func (cs *ChainState) GetEpochBoundaryBlock(epoch uint64) (uint64, bool) {
	cs.epochMutex.RLock()
	defer cs.epochMutex.RUnlock()
	if epoch == 0 {
		return 0, true // Genesis epoch has no boundary, use 0
	}
	if boundaryBlock, exists := cs.epochBoundaryBlocks[epoch]; exists {
		return boundaryBlock, true
	}
	// ❌ REMOVED: Fallback to storage.GetLastBlockNumber() causes fork!
	// When late-joining nodes query epoch boundary data, they would get committee at CURRENT block
	// (which may have new validators) instead of the epoch's ACTUAL boundary block.
	// Return 0, false to indicate "not found" - caller must handle this case properly.
	logger.Error("❌ [EPOCH BOUNDARY] No stored boundary block for epoch %d! "+
		"This node may not have witnessed the epoch transition.", epoch)
	return 0, false
}

// GetEpochBoundaryGei returns the boundary GEI for a given epoch
// For epoch 0 (genesis), returns 0
func (cs *ChainState) GetEpochBoundaryGei(epoch uint64) uint64 {
	cs.epochMutex.RLock()
	defer cs.epochMutex.RUnlock()
	if epoch == 0 {
		return 0
	}
	if gei, exists := cs.epochBoundaryGeis[epoch]; exists {
		return gei
	}
	logger.Error("❌ [EPOCH BOUNDARY] No stored boundary GEI for epoch %d! "+
		"This node may not have witnessed the epoch transition.", epoch)
	return 0
}

// AdvanceEpoch advances the system to the next epoch (Sui-style)
// boundaryBlock: the last block of the previous epoch (epoch boundary for validators snapshot)
func (cs *ChainState) AdvanceEpoch(newEpoch uint64, epochStartTimestampMs uint64) error {
	// For legacy AdvanceEpoch, we don't know the boundary GEI
	return cs.AdvanceEpochWithBoundary(newEpoch, epochStartTimestampMs, storage.GetLastBlockNumber(), 0)
}

// AdvanceEpochWithBoundary advances the system to the next epoch with explicit boundary block
// SIMPLIFIED: Go just stores what Rust tells it. Rust is the single source of truth.
// No validation, no block checks - Rust controls epoch transitions.
func (cs *ChainState) AdvanceEpochWithBoundary(newEpoch uint64, epochStartTimestampMs uint64, boundaryBlock uint64, boundaryGei uint64) error {
	cs.epochMutex.Lock()
	defer cs.epochMutex.Unlock()

	logger.Info("🔄 [ADVANCE EPOCH] Rust says advance to epoch %d (timestamp=%d, boundary=%d, gei=%d)",
		newEpoch, epochStartTimestampMs, boundaryBlock, boundaryGei)

	// Only reject if going backwards (obvious bug)
	if newEpoch < cs.currentEpoch {
		return fmt.Errorf("cannot go backwards: new_epoch=%d < current_epoch=%d", newEpoch, cs.currentEpoch)
	}

	// Already at this epoch - just confirm
	if newEpoch == cs.currentEpoch {
		if _, exists := cs.epochBoundaryBlocks[newEpoch]; exists {
			logger.Info("✅ [ADVANCE EPOCH] Already at epoch %d", newEpoch)
			return nil
		}
	}

	// === SINGLE WRITE PATH: advanceEpochLocked ===
	cs.advanceEpochLocked(newEpoch, epochStartTimestampMs, boundaryBlock, boundaryGei)
	return nil
}

// InitializeGenesisEpoch initializes the genesis epoch with timestamp
func (cs *ChainState) InitializeGenesisEpoch(genesisTimestampMs uint64) {
	cs.epochMutex.Lock()
	defer cs.epochMutex.Unlock()

	// === SINGLE WRITE PATH: advanceEpochLocked ===
	cs.advanceEpochLocked(0, genesisTimestampMs, 0, 0)
	logger.Info("🌟 [GENESIS] Epoch 0 initialized", "timestamp_ms", genesisTimestampMs)
}

// advanceEpochLocked is the SINGLE place that writes to epoch state maps.
// ALL epoch state mutations MUST go through this method to ensure consistency.
// MUST be called with epochMutex already held (Lock, not RLock).
//
// Writes to: currentEpoch, epochStartTimestampMs, epochBoundaryBlocks, epochStartTimestamps, epochBoundaryGeis
func (cs *ChainState) advanceEpochLocked(newEpoch uint64, epochStartTimestampMs uint64, boundaryBlock uint64, boundaryGei uint64) {
	// Ensure maps are initialized (defensive against nil maps in tests or remote states)
	if cs.epochStartTimestamps == nil {
		cs.epochStartTimestamps = make(map[uint64]uint64)
	}
	if cs.epochBoundaryBlocks == nil {
		cs.epochBoundaryBlocks = make(map[uint64]uint64)
	}
	if cs.epochBoundaryGeis == nil {
		cs.epochBoundaryGeis = make(map[uint64]uint64)
	}

	// Store previous epoch timestamp before advancing
	if cs.currentEpoch > 0 && cs.epochStartTimestampMs > 0 {
		cs.epochStartTimestamps[cs.currentEpoch] = cs.epochStartTimestampMs
	}

	// === THE ONLY PLACE THAT WRITES EPOCH STATE ===
	oldEpoch := cs.currentEpoch
	cs.currentEpoch = newEpoch
	cs.epochStartTimestampMs = epochStartTimestampMs
	cs.epochBoundaryBlocks[newEpoch] = boundaryBlock
	cs.epochBoundaryGeis[newEpoch] = boundaryGei
	cs.epochStartTimestamps[newEpoch] = epochStartTimestampMs

	logger.Info("✅ [EPOCH STATE] epoch %d → %d, timestamp=%d, boundary=%d, gei=%d",
		oldEpoch, newEpoch, epochStartTimestampMs, boundaryBlock, boundaryGei)

	// Persist
	if cs.storageManager != nil {
		if err := cs.SaveEpochData(); err != nil {
			logger.Warn("⚠️ [EPOCH STATE] Failed to save: %v", err)
		}
	}
}

// CheckAndUpdateEpochFromBlock checks if the incoming block has a higher epoch and auto-updates
// This is critical for late-joining nodes that receive blocks via network sync
// When a node joins after epoch has advanced, it needs to auto-detect the current epoch from blocks
// MULTI-EPOCH SUPPORT: For jumps > 1 epoch, directly advance to target epoch using current storage state.
// This handles restart catch-up scenarios where Go Sub receives blocks from a much later epoch.
func (cs *ChainState) CheckAndUpdateEpochFromBlock(blockEpoch uint64, blockTimestamp uint64) bool {
	cs.epochMutex.Lock()
	defer cs.epochMutex.Unlock()
	if blockEpoch > cs.currentEpoch {
		epochDiff := blockEpoch - cs.currentEpoch

		if epochDiff > 1 {
			logger.Info("🔄 [AUTO-EPOCH SYNC] Multi-epoch jump: current=%d → target=%d (diff=%d). "+
				"Advancing directly using current storage state.",
				cs.currentEpoch, blockEpoch, epochDiff)
		} else {
			logger.Info("🔄 [AUTO-EPOCH SYNC] Detected higher epoch from incoming block",
				"block_epoch", blockEpoch,
				"current_epoch", cs.currentEpoch,
				"block_timestamp", blockTimestamp)
		}

		// STEP 1: Calculate boundary block
		// The boundary block is the LAST block of the previous epoch
		// When syncing, this is storage.GetLastBlockNumber() - 1 (block before current)
		lastBlockNum := storage.GetLastBlockNumber()
		var boundaryBlock uint64
		if lastBlockNum > 0 {
			boundaryBlock = lastBlockNum - 1
		} else {
			boundaryBlock = 0
		}

		// STEP 2: MUST read boundary block - NO FALLBACK ALLOWED
		// If boundary block is not available, DEFER the epoch update
		var epochTimestampMs uint64
		var boundaryGei uint64
		if boundaryBlock > 0 {
			// Get boundary block's timestamp - REQUIRED, no fallback
			blockHash, ok := GetBlockChainInstance().GetBlockHashByNumber(boundaryBlock)
			if !ok {
				// CRITICAL: Boundary block not in chain yet - DEFER epoch update
				// This can happen if blocks arrive out-of-order
				logger.Error("❌ [AUTO-EPOCH SYNC] Boundary block %d hash not found in chain. "+
					"Block sync may be out-of-order. DEFERRING epoch update until boundary is available.",
					boundaryBlock)
				return false // ← DEFER epoch update
			}

			boundaryBlockData, err := cs.GetBlockDatabase().GetBlockByHash(blockHash)
			if err != nil {
				// CRITICAL: Cannot read boundary block data - DEFER epoch update
				logger.Error("❌ [AUTO-EPOCH SYNC] Cannot read boundary block %d data: %v. "+
					"DEFERRING epoch update.", boundaryBlock, err)
				return false // ← DEFER epoch update
			}

			epochTimestampMs = boundaryBlockData.Header().TimeStamp() * 1000
			boundaryGei = boundaryBlockData.Header().GlobalExecIndex()
			logger.Info("✅ [AUTO-EPOCH SYNC] Using BOUNDARY BLOCK timestamp and GEI (deterministic, no fallback)",
				"boundary_block", boundaryBlock,
				"epoch_timestamp_ms", epochTimestampMs,
				"boundary_gei", boundaryGei)
		} else {
			// Genesis case (epoch 1 from epoch 0): boundary = 0
			// Use genesis timestamp (should be set from genesis config)
			// For safety, use 0 and let Rust provide authoritative timestamp via AdvanceEpoch
			epochTimestampMs = 0
			boundaryGei = 0
			logger.Info("📝 [AUTO-EPOCH SYNC] Genesis epoch boundary (block=0), using placeholder timestamp=0. " +
				"Rust will provide authoritative timestamp via AdvanceEpoch RPC.")
		}

		// === SINGLE WRITE PATH: advanceEpochLocked ===
		cs.advanceEpochLocked(blockEpoch, epochTimestampMs, boundaryBlock, boundaryGei)

		// EVENT-DRIVEN NOTIFICATION: Notify Rust about the detected epoch change
		if cs.epochNotificationCallback != nil {
			logger.Info("📣 [AUTO-EPOCH SYNC] Triggering epoch notification callback for epoch %d", cs.currentEpoch)
			go cs.epochNotificationCallback(cs.currentEpoch, cs.epochStartTimestampMs, boundaryBlock)
		}

		return true // Epoch was updated
	}
	return false // No update needed
}

// SetBackupPath sets the node-specific backup path for epoch data persistence
// This should be called with the node's data directory path to prevent epoch collision
// CRITICAL: This also reloads epoch data from the correct node-specific path
func (cs *ChainState) SetBackupPath(path string) {
	cs.backupPath = path
	logger.Info("📁 [EPOCH PERSISTENCE] Set node-specific backup path: %s", path)

	// CRITICAL: Reload epoch data from the correct node-specific path
	// This is necessary because NewChainState() calls LoadEpochData() before SetBackupPath()
	// which may load stale data from /tmp/epoch_data_backup.json (shared fallback)
	logger.Info("🔄 [EPOCH PERSISTENCE] Reloading epoch data from node-specific backup path...")
	if err := cs.LoadEpochData(); err != nil {
		logger.Warn("⚠️ [EPOCH PERSISTENCE] Failed to reload epoch data from node-specific path, keeping current values", "error", err)
	} else {
		logger.Info("✅ [EPOCH PERSISTENCE] Successfully reloaded epoch data from node-specific path - current_epoch=%d, epoch_timestamp_ms=%d",
			cs.currentEpoch, cs.epochStartTimestampMs)
	}
}

// getEpochBackupPath returns the node-specific epoch backup file path
// Falls back to /tmp/epoch_data_backup.json if no backup path is set (legacy behavior)
func (cs *ChainState) getEpochBackupPath() string {
	if cs.backupPath != "" {
		return cs.backupPath + "/epoch_data_backup.json"
	}
	// Fallback to legacy shared path (not recommended for multi-node setups)
	logger.Warn("⚠️ [EPOCH PERSISTENCE] Using shared /tmp backup path - this may cause epoch collision on multi-node setups")
	return "/tmp/epoch_data_backup.json"
}

// PruneEpochCache removes epoch boundary data older than maxCachedEpochs.
// Always preserves epoch 0 (genesis). If maxCachedEpochs is 0, keeps all.
func (cs *ChainState) PruneEpochCache() {
	cs.epochMutex.Lock()
	defer cs.epochMutex.Unlock()
	cs.pruneEpochCacheLocked()
}

// pruneEpochCacheLocked is the internal implementation of PruneEpochCache.
// MUST be called with epochMutex already held (Lock, not RLock).
func (cs *ChainState) pruneEpochCacheLocked() {
	if cs.maxCachedEpochs == 0 || cs.currentEpoch <= cs.maxCachedEpochs {
		return // unlimited or not enough epochs to prune
	}
	cutoff := cs.currentEpoch - cs.maxCachedEpochs
	pruned := 0
	for epoch := range cs.epochBoundaryBlocks {
		if epoch > 0 && epoch < cutoff {
			delete(cs.epochBoundaryBlocks, epoch)
			delete(cs.epochBoundaryGeis, epoch)
			delete(cs.epochStartTimestamps, epoch)
			pruned++
		}
	}
	if pruned > 0 {
		logger.Info("🗑️ [EPOCH CACHE] Pruned %d old epochs (cutoff=%d, max_cached=%d, remaining=%d)",
			pruned, cutoff, cs.maxCachedEpochs, len(cs.epochBoundaryBlocks))
	}
}

// Epoch data persistence keys
var (
	epochDataKey = common.BytesToHash(crypto.Keccak256([]byte("epochData")))
)

// SaveEpochData lưu thông tin epoch vào database (với backup file system)
func (cs *ChainState) SaveEpochData() error {
	logger.Info("💾 [EPOCH PERSISTENCE] Starting to save epoch data to database - current_epoch={}, epoch_timestamp_ms={}",
		cs.currentEpoch, cs.epochStartTimestampMs)

	// Prune old epochs before saving (configurable retention)
	// NOTE: Uses lockless version because SaveEpochData is always called from within epochMutex.Lock()
	cs.pruneEpochCacheLocked()

	epochData := EpochData{
		CurrentEpoch:          cs.currentEpoch,
		EpochStartTimestampMs: cs.epochStartTimestampMs,
		EpochStartTimestamps:  cs.epochStartTimestamps,
		EpochBoundaryBlocks:   cs.epochBoundaryBlocks, // NEW: Include epoch boundary blocks
		EpochBoundaryGeis:     cs.epochBoundaryGeis,   // NEW: Include epoch boundary GEIs
	}

	data, err := json.Marshal(epochData)
	if err != nil {
		logger.Error("❌ [EPOCH PERSISTENCE] Failed to marshal epoch data", "error", err)
		return fmt.Errorf("failed to marshal epoch data: %w", err)
	}

	logger.Info("📦 [EPOCH PERSISTENCE] Marshaled epoch data", "data_size", len(data), "key", epochDataKey.Hex())

	// Thử lưu vào database trước
	if cs.storageManager != nil {
		blockStorage := cs.storageManager.GetStorageBlock()
		if blockStorage != nil {
			if err := blockStorage.Put(epochDataKey.Bytes(), data); err != nil {
				logger.Warn("⚠️ [EPOCH PERSISTENCE] Failed to save epoch data to database, will try backup", "error", err)
			} else {
				logger.Info("✅ [EPOCH PERSISTENCE] Epoch data saved to database")
			}
		} else {
			logger.Warn("⚠️ [EPOCH PERSISTENCE] Block storage is nil, will use backup")
		}
	} else {
		logger.Warn("⚠️ [EPOCH PERSISTENCE] Storage manager is nil, will use backup")
	}

	// Backup: LUÔN LUÔN lưu vào file system kể cả khi rớt xuống database thành công.
	// Lý do: LevelDB có thể không flush memtable xuống ổ cứng kịp khi tạo thư mục snapshot (khác với PebbleDB có Flush()).
	// Việc lưu backup file đảm bảo file copy snapshot luôn luôn chứa phiên bản epoch đúng.
	backupFile := cs.getEpochBackupPath()
	if err := os.WriteFile(backupFile, data, 0644); err != nil {
		logger.Error("❌ [EPOCH PERSISTENCE] Failed to save epoch data to backup file", "error", err, "file", backupFile)
		return fmt.Errorf("failed to save epoch data to backup file: %w", err)
	}
	logger.Info("✅ [EPOCH PERSISTENCE] Epoch data saved to backup file", "file", backupFile)

	logger.Info("✅ [EPOCH PERSISTENCE] Epoch data persistence completed",
		"current_epoch", cs.currentEpoch,
		"epoch_timestamp_ms", cs.epochStartTimestampMs)

	return nil
}

// LoadEpochData tải thông tin epoch từ database (với backup file system)
func (cs *ChainState) LoadEpochData() error {
	logger.Info("📖 [EPOCH PERSISTENCE] Starting to load epoch data...")

	var data []byte
	var source string

	// Thử load từ database trước
	if cs.storageManager != nil {
		blockStorage := cs.storageManager.GetStorageBlock()
		if blockStorage != nil {
			logger.Debug("[EPOCH PERSISTENCE] Looking for epoch data in database with key", "key", epochDataKey.Hex())

			if dbData, err := blockStorage.Get(epochDataKey.Bytes()); err == nil {
				data = dbData
				source = "database"
				logger.Debug("[EPOCH PERSISTENCE] Found epoch data in database", "data_size", len(data))
			} else {
				logger.Debug("[EPOCH PERSISTENCE] No epoch data found in database", "error", err)
			}
		} else {
			logger.Warn("⚠️ [EPOCH PERSISTENCE] Block storage is nil")
		}
	} else {
		logger.Warn("⚠️ [EPOCH PERSISTENCE] Storage manager is nil")
	}

	// Nếu không có data từ database, thử load từ backup file
	// CRITICAL: Use node-specific backup path to prevent epoch collision between nodes
	if data == nil {
		backupFile := cs.getEpochBackupPath()
		if fileData, err := os.ReadFile(backupFile); err == nil {
			data = fileData
			source = "backup file"
			logger.Info("📦 [EPOCH PERSISTENCE] Found epoch data in backup file", "file", backupFile, "data_size", len(data))
		} else {
			logger.Info("📖 [EPOCH PERSISTENCE] No epoch data found in backup file either (first time initialization)", "file", backupFile, "error", err)
			return nil // Không coi là error
		}
	}

	var epochData EpochData
	if err := json.Unmarshal(data, &epochData); err != nil {
		logger.Error("❌ [EPOCH PERSISTENCE] Failed to unmarshal epoch data", "error", err, "source", source, "raw_data", string(data))
		return fmt.Errorf("failed to unmarshal epoch data from %s: %w", source, err)
	}

	// Restore epoch state
	cs.currentEpoch = epochData.CurrentEpoch
	cs.epochStartTimestampMs = epochData.EpochStartTimestampMs
	cs.epochStartTimestamps = epochData.EpochStartTimestamps
	// NEW: Restore epoch boundary blocks (may be nil for older data)
	if epochData.EpochBoundaryBlocks != nil {
		cs.epochBoundaryBlocks = epochData.EpochBoundaryBlocks
	}
	if epochData.EpochBoundaryGeis != nil {
		cs.epochBoundaryGeis = epochData.EpochBoundaryGeis
	}

	logger.Info("✅ [EPOCH PERSISTENCE] Epoch data successfully loaded and restored",
		"source", source,
		"current_epoch", cs.currentEpoch,
		"epoch_timestamp_ms", cs.epochStartTimestampMs,
		"num_historical_epochs", len(cs.epochStartTimestamps),
		"num_boundary_blocks", len(cs.epochBoundaryBlocks))

	return nil
}

// SetEpochNotificationCallback sets the callback function to be called when epoch changes
func (cs *ChainState) SetEpochNotificationCallback(cb func(uint64, uint64, uint64)) {
	cs.epochNotificationCallback = cb
}
