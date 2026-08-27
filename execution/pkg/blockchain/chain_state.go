package blockchain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blob_store"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract_db"
	"github.com/meta-node-blockchain/meta-node/pkg/state_changelog"
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
	// EPOCH VALIDATOR PERSISTENCE: Validator list per epoch, decoupled from NOMT.
	// Key = epoch number, Value = JSON-serialized pb.ValidatorInfoList.
	// This ensures validators survive snapshot restore even when NOMT knownKeys is empty.
	EpochValidators map[uint64]json.RawMessage `json:"epoch_validators"`
}

type EpochMapEntry struct {
	Key   uint64
	Value uint64
}

type EpochValidatorEntry struct {
	Key   uint64
	Value []byte
}

type EpochDataRLP struct {
	CurrentEpoch          uint64
	EpochStartTimestampMs uint64
	EpochStartTimestamps  []EpochMapEntry
	EpochBoundaryBlocks   []EpochMapEntry
	EpochBoundaryGeis     []EpochMapEntry
	EpochValidators       []EpochValidatorEntry
}

// ChainState quản lý trạng thái toàn cục của blockchain
type ChainState struct {
	config *config.SimpleChainConfig

	currentBlockHeader atomic.Pointer[types.BlockHeader]
	storageManager     *storage.StorageManager
	accountStateDB     atomic.Pointer[account_state_db.AccountStateDB]
	smartContractDB    atomic.Pointer[smart_contract_db.SmartContractDB]
	stakeStateDB       atomic.Pointer[stake_state_db.StakeStateDB]
	blockDatabase      *block.BlockDatabase
	freeFeeAddress     map[common.Address]struct{}
	changelogDB        *state_changelog.StateChangelogDB
	stakeChangelogDB   *state_changelog.StateChangelogDB
	blobStore          *blob_store.BlobStore

	// commitMutex serializes all block state mutations in CommitBlockState
	// to prevent concurrent corruption of the NOMT trie and DB batches.
	commitMutex sync.Mutex

	// Sui-style epoch tracking
	currentEpoch          uint64
	epochStartTimestampMs uint64
	epochStartTimestamps  map[uint64]uint64          // epoch -> timestamp_ms mapping
	epochBoundaryBlocks   map[uint64]uint64          // NEW: epoch -> boundary_block mapping (last block of prev epoch)
	epochBoundaryGeis     map[uint64]uint64          // NEW: epoch -> boundary_gei mapping
	epochValidatorsCache  map[uint64]json.RawMessage // EPOCH VALIDATOR PERSISTENCE: serialized ValidatorInfoList per epoch
	maxCachedEpochs       uint64                     // 0 = keep all, N = keep only N most recent epochs in cache

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

	// Unaligned future NOMT root detected on startup (NOMT ahead of LevelDB)
	futureNomtRoot common.Hash
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
	// initChangelog is a local helper to init a StateChangelogDB and attach it to a NomtStateTrie.
	// Each trie (account, stake) needs its own separate changelog DB with a different namespace
	// because they track completely different data:
	//   - account_state → user balances, nonces, BLS keys
	//   - stake_db      → validator stake amounts
	// They CANNOT be merged into one DB.
	initChangelog := func(nomtTrie *trie.NomtStateTrie, dirName, namespace string) *state_changelog.StateChangelogDB {
		isRPC := false
		if config != nil {
			if config.IsRPCNode || os.Getenv("NODE_TYPE") == "synconly" {
				isRPC = true
			}
		}
		if !isRPC || backupPath == "" || backupPath == "skip_epoch_data" {
			return nil
		}
		var baseDir string
		if config.Databases.RootPath != "" {
			baseDir = config.Databases.RootPath
		} else {
			baseDir = filepath.Dir(backupPath)
		}
		cPath := filepath.Join(baseDir, "history", dirName)
		cdb, cErr := state_changelog.NewStateChangelogDB(cPath, namespace)
		if cErr != nil {
			logger.Error("Failed to init StateChangelogDB for %s at %s: %v", namespace, cPath, cErr)
			return nil
		}
		nomtTrie.SetChangelogDB(cdb)
		logger.Info("✅ [STATE CHANGELOG] Enabled for %s", namespace)
		return cdb
	}

	useRegistry := true
	if backupPath == "skip_epoch_data" {
		useRegistry = false
	}

	accountStorage := sm.GetStorageAccount()
	accountStateTrie, err := trie.NewStateTrie(currentBlockHeader.AccountStatesRoot(), accountStorage, useRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to create account state trie: %v", err)
	}
	var changelogDB *state_changelog.StateChangelogDB
	if nomtTrie, ok := accountStateTrie.(*trie.NomtStateTrie); ok {
		changelogDB = initChangelog(nomtTrie, "changelog_db_account", "account_state")
	}

	stakeStorage := sm.GetStorageStake()
	stakeStateTrie, err := trie.NewStateTrie(common.Hash(currentBlockHeader.StakeStatesRoot()), stakeStorage, useRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to create stake state trie: %v", err)
	}
	var stakeChangelogDB *state_changelog.StateChangelogDB
	if nomtTrie, ok := stakeStateTrie.(*trie.NomtStateTrie); ok {
		stakeChangelogDB = initChangelog(nomtTrie, "changelog_db_stake", "stake_db")
	}

	// Blob sidecars are only ever seen by whichever node a wallet submits an
	// EIP-4844 tx to directly (consensus only carries the stripped tx, see
	// Transaction.Sidecar in transaction.proto) — unlike the state changelog,
	// every node exposes eth_sendRawTransaction, so this isn't gated behind
	// the isRPC check initChangelog uses.
	var blobStore *blob_store.BlobStore
	if backupPath != "" && backupPath != "skip_epoch_data" {
		var baseDir string
		if config != nil && config.Databases.RootPath != "" {
			baseDir = config.Databases.RootPath
		} else {
			baseDir = filepath.Dir(backupPath)
		}
		bPath := filepath.Join(baseDir, "history", "blob_store")
		bs, bErr := blob_store.NewBlobStore(bPath, "blob")
		if bErr != nil {
			logger.Error("Failed to init BlobStore at %s: %v", bPath, bErr)
		} else {
			blobStore = bs
		}
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
		config:                config,
		blockDatabase:         blockDatabase,
		freeFeeAddress:        freeFeeAddress,
		maxCachedEpochs:       maxCached,
		changelogDB:           changelogDB,
		stakeChangelogDB:      stakeChangelogDB,
		blobStore:             blobStore,
		currentEpoch:          0, // Start with epoch 0 (genesis)
		epochStartTimestampMs: 0, // Will be set on first epoch advance
		epochStartTimestamps:  make(map[uint64]uint64),
		epochBoundaryBlocks:   make(map[uint64]uint64),          // Track epoch boundary blocks
		epochBoundaryGeis:     make(map[uint64]uint64),          // Track epoch boundary GEIs
		epochValidatorsCache:  make(map[uint64]json.RawMessage), // Validator list per epoch
		backupPath:            backupPath,                       // CRITICAL: Set BEFORE LoadEpochData()
		attestationInterval:   10,                               // Default: attestation every 10 blocks
	}

	cs.accountStateDB.Store(asDB)
	cs.stakeStateDB.Store(stakeStateDB)
	cs.smartContractDB.Store(scDB)

	if backupPath != "skip_epoch_data" {
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
					deterministicTimestamp = currentBlockHeader.TimeStamp()
					logger.Info("🔧 [GENESIS EPOCH] Derived timestamp from currentBlockHeader: block=%d, timestamp_ms=%d",
						currentBlockHeader.BlockNumber(), currentBlockHeader.TimeStamp())
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
	} else {
		logger.Debug("🚀 [EPOCH PERSISTENCE] Skipping epoch data loading and initialization for virtual/temporary chain state")
	}

	headerCopy := currentBlockHeader
	cs.currentBlockHeader.Store(&headerCopy)

	// START SCRUBBER (Priority 3)
	// GIẢI THÍCH LÝ DO CẦN ĐIỀU KIỆN NÀY:
	// - "skip_epoch_data" là cờ được truyền vào khi tạo các ChainState ảo (ví dụ qua lệnh eth_call).
	// - Các State ảo này chỉ dùng tạm thời để đọc dữ liệu rồi bị thu hồi (Garbage Collected) ngay lập tức.
	// - Nếu không chặn lại, mỗi lệnh eth_call sẽ sinh ra 1 Goroutine Scrubber chạy ngầm trọn đời (leak).
	// - Câu lệnh `if` dưới đây đảm bảo chỉ có Node chính (lưu dữ liệu thật) mới được phép chạy Scrubber.
	if backupPath != "skip_epoch_data" {
		if accountDB := cs.GetAccountStateDB(); accountDB != nil {
			if trieDB := accountDB.Trie(); trieDB != nil {
				// Run a deep integrity check every 24 hours
				scrubber := NewScrubber(trieDB, 24*time.Hour)
				scrubber.Start()
			}
		}
	}

	return cs, nil // Trả về ChainState đã tạo và nil error
}

// UpdateStateForNewHeader cập nhật trạng thái dựa trên header mới.
// Hàm này sẽ cập nhật con trỏ header và khởi tạo lại các DB trạng thái liên quan.
func (cs *ChainState) updateStateForNewHeader(newHeader types.BlockHeader) error {
	if newHeader == nil {
		return fmt.Errorf("cannot update state with a nil header")
	}

	// ═══════════════════════════════════════════════════════════════════════
	// NOMT FAST-PATH: Lightweight root re-alignment (~1µs vs ~10ms full rebuild)
	//
	// For NOMT backend, the C++ engine already has the authoritative state.
	// We only need to update Go's in-memory root hash pointers to match.
	// This avoids:
	//   - loadRegistryFromFile() disk I/O per namespace
	//   - handle.Root() FFI calls
	//   - NewAccountStateDB/NewStakeStateDB constructor overhead
	//   - Atomic DB pointer swaps + GC pressure from discarded old DBs
	//
	// FORK-SAFETY: RealignRoot only updates readView.rootHash. It does NOT
	// modify the C++ NOMT state. Subsequent Commit() calls will compute
	// the correct root from dirty entries against the C++ engine.
	// ═══════════════════════════════════════════════════════════════════════
	if trie.GetStateBackend() == trie.BackendNOMT {
		newAccountRoot := newHeader.AccountStatesRoot()
		newStakeRoot := common.Hash(newHeader.StakeStatesRoot())

		// Realign/Rebuild account state trie
		if asDB := cs.GetAccountStateDB(); asDB != nil {
			if nomtTrie, ok := asDB.Trie().(*trie.NomtStateTrie); ok {
				if err := nomtTrie.AlignWithExpectedRoot(asDB.Storage(), newAccountRoot, newHeader.BlockNumber()); err != nil {
					logger.Error("❌ [NOMT-FAST-PATH] Failed to align account state NOMT: %v", err)
					return fmt.Errorf("failed to align account state NOMT: %w", err)
				}
				asDB.SetOriginRootHash(newAccountRoot)
			}
		}
		// Realign/Rebuild stake state trie
		if stakeDB := cs.GetStakeStateDB(); stakeDB != nil {
			if nomtTrie, ok := stakeDB.Trie().(*trie.NomtStateTrie); ok {
				if err := nomtTrie.AlignWithExpectedRoot(stakeDB.GetStorage(), newStakeRoot, newHeader.BlockNumber()); err != nil {
					logger.Error("❌ [NOMT-FAST-PATH] Failed to align stake NOMT: %v", err)
					return fmt.Errorf("failed to align stake NOMT: %w", err)
				}
				stakeDB.SetOriginRootHash(newStakeRoot)
			}
		}

		// Update header pointer atomically
		headerCopy := newHeader
		cs.currentBlockHeader.Store(&headerCopy)

		logger.Info("🔧 [NOMT-FAST-PATH] UpdateStateForNewHeader lightweight re-alignment for block #%d (accountRoot=%s, stakeRoot=%s)",
			newHeader.BlockNumber(), newAccountRoot.Hex()[:18], newStakeRoot.Hex()[:18])
		return nil
	}

	// ═══════════════════════════════════════════════════════════════════════
	// FULL REBUILD PATH: For MPT/Flat/Verkle backends
	// Creates new trie instances from header roots and swaps DB pointers.
	// ═══════════════════════════════════════════════════════════════════════

	// 1. Khởi tạo lại AccountStateDB với root mới
	accountStorage := cs.storageManager.GetStorageAccount()
	newAccountRoot := newHeader.AccountStatesRoot() // Lấy root từ header mới
	newAccountStateTrie, err := trie.NewStateTrie(newAccountRoot, accountStorage, true)
	if err != nil {
		logger.Error("Failed to create new account state trie during update", "error", err, "newRoot", newAccountRoot)
		return fmt.Errorf("failed to create new account state trie for update: %w", err)
	}
	if cs.changelogDB != nil {
		if nomtTrie, ok := newAccountStateTrie.(*trie.NomtStateTrie); ok {
			nomtTrie.SetChangelogDB(cs.changelogDB)
		}
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
	if cs.stakeChangelogDB != nil {
		if nomtTrie, ok := newStakeStateTrie.(*trie.NomtStateTrie); ok {
			nomtTrie.SetChangelogDB(cs.stakeChangelogDB)
		}
	}
	newStakeStateDB := stake_state_db.NewStakeStateDB(newStakeStateTrie, stakeStorage)

	// 3. Khởi tạo lại SmartContractDB với AccountStateDB mới
	// (Giả sử config không thay đổi)
	newScDB := smart_contract_db.NewSmartContractDB(
		cs.storageManager.GetStorageCode(),
		cs.storageManager.GetStorageSmartContract(),
		newAsDB, // Sử dụng asDB mới tạo
	)

	// 4. Atomic Lock-Free DB Swaps (no delayed Close)
	// Swap the pointers immediately so new EVM transactions use the new state.
	oldAsDB := cs.accountStateDB.Swap(newAsDB)
	oldStakeDB := cs.stakeStateDB.Swap(newStakeStateDB)
	oldScDB := cs.smartContractDB.Swap(newScDB)

	// OLD DB LIFECYCLE: Let Go's GC handle cleanup naturally.
	// CRITICAL FIX (May 2026 — Fork at Block 4690):
	// The previous pseudo-RCU goroutine called oldAsDB.Close() after 5 seconds.
	// NOMT C++ sessions share underlying mmap'd storage between old and new tries.
	// Closing the old session while the new session is actively reading causes
	// non-deterministic IntermediateRoot() across nodes → stateRoot divergence → FORK.
	// The old *AccountStateDB will be GC'd once all goroutine references are dropped.
	_ = oldAsDB
	_ = oldStakeDB
	_ = oldScDB

	// 5. Cập nhật con trỏ header nguyên tử
	headerCopy := newHeader
	cs.currentBlockHeader.Store(&headerCopy)

	// logger.Info("ChainState updated for new header", "blockNumber", newHeader.BlockNumber(), "accountRoot", newAccountRoot, "stakeRoot", newStakeRoot)
	return nil
}

// LockCommit locks the commit mutex to serialize external operations with block committing.
func (cs *ChainState) LockCommit() {
	cs.commitMutex.Lock()
}

// UnlockCommit unlocks the commit mutex.
func (cs *ChainState) UnlockCommit() {
	cs.commitMutex.Unlock()
}

// UpdateStateForNewHeader updates state using the new header under the commit lock.
func (cs *ChainState) UpdateStateForNewHeader(newHeader types.BlockHeader) error {
	cs.commitMutex.Lock()
	defer cs.commitMutex.Unlock()
	return cs.updateStateForNewHeader(newHeader)
}

// UpdateStateForNewHeaderUnlocked updates state using the new header without acquiring the commit lock.
// The caller MUST hold the commitMutex.
func (cs *ChainState) UpdateStateForNewHeaderUnlocked(newHeader types.BlockHeader) error {
	return cs.updateStateForNewHeader(newHeader)
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
		freeFeeAddress: freeFeeAddress,
	}
	cs.accountStateDB.Store(asDB)
	cs.smartContractDB.Store(scDB)

	headerCopy := currentBlockHeader
	cs.currentBlockHeader.Store(&headerCopy)

	return cs, nil // Trả về ChainState đã tạo và nil error
}

// GetConfig trả về cấu hình của ChainState.
func (cs *ChainState) GetConfig() *config.SimpleChainConfig {
	return cs.config
}

// SetConfig sets the configuration for the ChainState
func (cs *ChainState) SetConfig(cfg *config.SimpleChainConfig) {
	cs.config = cfg
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

// GetChangelogDB returns the state changelog database instance for historical state tracking.
func (cs *ChainState) GetChangelogDB() *state_changelog.StateChangelogDB {
	return cs.changelogDB
}

// GetStakeChangelogDB returns the state changelog database instance for historical stake tracking.
func (cs *ChainState) GetStakeChangelogDB() *state_changelog.StateChangelogDB {
	return cs.stakeChangelogDB
}

// GetBlobStore returns the EIP-4844 blob sidecar store, or nil if this node
// wasn't set up with a backup path (e.g. in-memory/test chains).
func (cs *ChainState) GetBlobStore() *blob_store.BlobStore {
	return cs.blobStore
}

func (cs *ChainState) GetAccountStateDB() *account_state_db.AccountStateDB {
	return cs.accountStateDB.Load()
}

// HasCode reports whether addr currently has code — a real deployed contract,
// or an EIP-7702-delegated EOA. Matches grouptxns.HasCodeFunc's signature;
// pass this method value directly to grouptxns.GroupTransactionsDeterministic
// so a plain value-transfer to such an address is routed through the EVM
// instead of the native fast-path (see HasCodeFunc's doc for the one case
// this intentionally doesn't cover).
func (cs *ChainState) HasCode(addr common.Address) bool {
	asDB := cs.GetAccountStateDB()
	if asDB == nil {
		return false
	}
	as, err := asDB.AccountState(addr)
	if err != nil || as == nil {
		return false
	}
	return as.SmartContractState() != nil
}

// GetSmartContractDB trả về SmartContractDB.
func (cs *ChainState) GetSmartContractDB() *smart_contract_db.SmartContractDB {
	return cs.smartContractDB.Load()
}

func (cs *ChainState) GetStakeStateDB() *stake_state_db.StakeStateDB {
	return cs.stakeStateDB.Load()
}

// GetStorageManager trả về StorageManager.
func (cs *ChainState) GetStorageManager() *storage.StorageManager {
	return cs.storageManager
}

func (cs *ChainState) SetAccountStateDB(asDB *account_state_db.AccountStateDB) {
	cs.accountStateDB.Store(asDB)
}

func (cs *ChainState) SetSmartContractDB(scDB *smart_contract_db.SmartContractDB) {
	cs.smartContractDB.Store(scDB)
}

func (cs *ChainState) SetStakeStateDB(stakeDB *stake_state_db.StakeStateDB) {
	cs.stakeStateDB.Store(stakeDB)
}

// CloneSpeculative creates a speculative, thread-safe copy of ChainState
// sharing the same databases configuration but with cloned/copied trie structures.
func (cs *ChainState) CloneSpeculative(header types.BlockHeader) (*ChainState, error) {
	// 1. Copy AccountStateDB
	accDB := cs.GetAccountStateDB()
	clonedAccTrie := accDB.Trie().Copy()
	clonedAccDB := account_state_db.NewAccountStateDB(clonedAccTrie, cs.storageManager.GetStorageAccount())

	// 2. Copy StakeStateDB
	stakeDB := cs.GetStakeStateDB()
	clonedStakeTrie := stakeDB.Trie().Copy()
	clonedStakeDB := stake_state_db.NewStakeStateDB(clonedStakeTrie, cs.storageManager.GetStorageStake())

	// 3. Copy SmartContractDB
	clonedScDB := smart_contract_db.NewSmartContractDB(
		cs.storageManager.GetStorageCode(),
		cs.storageManager.GetStorageSmartContract(),
		clonedAccDB,
	)

	// 4. Construct cloned ChainState
	clonedCS := &ChainState{
		config:                cs.config,
		storageManager:        cs.storageManager,
		blockDatabase:         cs.blockDatabase,
		freeFeeAddress:        cs.freeFeeAddress,
		changelogDB:           cs.changelogDB,
		stakeChangelogDB:      cs.stakeChangelogDB,
		blobStore:             cs.blobStore,
		currentEpoch:          cs.currentEpoch,
		epochStartTimestampMs: cs.epochStartTimestampMs,
		backupPath:            cs.backupPath,
		attestationInterval:   cs.attestationInterval,
	}
	clonedCS.currentBlockHeader.Store(&header)
	clonedCS.accountStateDB.Store(clonedAccDB)
	clonedCS.smartContractDB.Store(clonedScDB)
	clonedCS.stakeStateDB.Store(clonedStakeDB)

	cs.epochMutex.RLock()
	clonedCS.epochStartTimestamps = cs.epochStartTimestamps
	clonedCS.epochBoundaryBlocks = cs.epochBoundaryBlocks
	clonedCS.epochBoundaryGeis = cs.epochBoundaryGeis
	cs.epochMutex.RUnlock()

	return clonedCS, nil
}


// InvalidateAllState clears all in-memory caches across all state databases.
// This ensures that subsequent reads will fetch fresh data from the underlying PebbleDB/NOMT storage.
func (cs *ChainState) InvalidateAllState() {
	if db := cs.accountStateDB.Load(); db != nil {
		db.InvalidateAllCaches()
	}
	if db := cs.stakeStateDB.Load(); db != nil {
		db.InvalidateAllCaches()
	}
	if db := cs.smartContractDB.Load(); db != nil {
		db.InvalidateAllCaches()
	}
}

// SetFutureNomtRoot sets the unaligned future NOMT root detected on startup.
func (cs *ChainState) SetFutureNomtRoot(root common.Hash) {
	cs.futureNomtRoot = root
}

// GetFutureNomtRoot returns the unaligned future NOMT root detected on startup.
func (cs *ChainState) GetFutureNomtRoot() common.Hash {
	return cs.futureNomtRoot
}

// ClearFutureNomtRoot clears the unaligned future NOMT root.
func (cs *ChainState) ClearFutureNomtRoot() {
	cs.futureNomtRoot = common.Hash{}
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

// SetEpochValidators stores the validator list for a given epoch in the cache.
// This is called during epoch transitions to snapshot the validator set,
// decoupled from NOMT's transient state.
// The data is a JSON-serialized pb.ValidatorInfoList.
func (cs *ChainState) SetEpochValidators(epoch uint64, serializedValidators json.RawMessage) {
	cs.epochMutex.Lock()
	defer cs.epochMutex.Unlock()
	cs.setEpochValidatorsLocked(epoch, serializedValidators)
}

// setEpochValidatorsLocked is the unlocked version of SetEpochValidators.
// Caller MUST hold cs.epochMutex.Lock().
func (cs *ChainState) setEpochValidatorsLocked(epoch uint64, serializedValidators json.RawMessage) {
	if cs.epochValidatorsCache == nil {
		cs.epochValidatorsCache = make(map[uint64]json.RawMessage)
	}
	cs.epochValidatorsCache[epoch] = serializedValidators
	logger.Info("💾 [EPOCH VALIDATORS] Cached validators for epoch %d (%d bytes)", epoch, len(serializedValidators))
}

// GetEpochValidators returns the cached validator list for a given epoch.
// Returns nil if no cached validators exist for this epoch.
func (cs *ChainState) GetEpochValidators(epoch uint64) json.RawMessage {
	cs.epochMutex.RLock()
	defer cs.epochMutex.RUnlock()
	if cs.epochValidatorsCache == nil {
		return nil
	}
	if data, exists := cs.epochValidatorsCache[epoch]; exists {
		return data
	}
	return nil
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
	lockStart := time.Now()
	cs.epochMutex.Lock()
	if d := time.Since(lockStart); d > 50*time.Millisecond {
		logger.Warn("🚨 [LOCK CONTENTION] epochMutex Lock in AdvanceEpochWithBoundary took %v", d)
	}
	defer cs.epochMutex.Unlock()

	logger.Info("🔄 [ADVANCE EPOCH] Rust says advance to epoch %d (timestamp=%d, boundary=%d, gei=%d)",
		newEpoch, epochStartTimestampMs, boundaryBlock, boundaryGei)

	// Only reject if going backwards (obvious bug)
	if newEpoch < cs.currentEpoch {
		logger.Warn("🛡️ [EPOCH GUARD] Backwards AdvanceEpoch request ignored! Target Epoch %d, but Go is already at Epoch %d. (Likely a recovery catch-up).", newEpoch, cs.currentEpoch)
		return nil
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

// ForceAlignEpochFromBlockHeader ensures that ChainState.currentEpoch matches
// the epoch recorded in the last block header. This is the DEFINITIVE fix for
// snapshot-restore epoch desync:
//
// FAILURE SCENARIO: Snapshot was taken at epoch N, but after restore,
// LoadEpochData() returns epoch N-1 (stale data in block storage or backup file).
// Rust then asks Go for current epoch → gets N-1 → uses wrong committee/leader.
// Result: Fork at the next block because leader schedule is wrong.
//
// FIX: Block headers contain authoritative epoch information. If the last block
// header has epoch > ChainState.currentEpoch, force-advance to match.
//
// ARCHITECTURAL INVARIANT: This function is called BEFORE InitBlockChain(),
// so GetBlockChainInstance() will return nil. We MUST NOT depend on the
// BlockChain singleton. Instead, use:
//  1. Already-loaded epochBoundaryBlocks map (from LoadEpochData)
//  2. BlockDatabase parent-chain traversal (always available)
func (cs *ChainState) ForceAlignEpochFromBlockHeader(blockEpoch uint64, blockTimestamp uint64, blockNumber uint64) {
	lockStart := time.Now()
	cs.epochMutex.Lock()
	if d := time.Since(lockStart); d > 50*time.Millisecond {
		logger.Warn("🚨 [LOCK CONTENTION] epochMutex Lock in ForceAlignEpochFromBlockHeader took %v", d)
	}
	defer cs.epochMutex.Unlock()

	if blockEpoch != cs.currentEpoch {
		logger.Warn("🚨 [SNAPSHOT FIX] Epoch alignment: ChainState.currentEpoch=%d but lastBlock.epoch=%d. "+
			"Forcing epoch to %d to prevent fork. (block=%d, ts=%d)",
			cs.currentEpoch, blockEpoch, blockEpoch, blockNumber, blockTimestamp)

		// ═══════════════════════════════════════════════════════════════════
		// STRATEGY 1: Use already-loaded epoch boundary data.
		// LoadEpochData() ran before us and populated epochBoundaryBlocks.
		// If the target epoch's boundary was previously recorded, use it
		// directly — no scanning needed. This is the common case for both
		// epoch upgrade (stale backup) and epoch downgrade (snapshot from
		// a node that was ahead).
		// ═══════════════════════════════════════════════════════════════════
		trueBoundaryBlock := blockNumber
		var alignTimestampMs uint64

		if cachedBoundary, ok := cs.epochBoundaryBlocks[blockEpoch]; ok && cachedBoundary > 0 {
			trueBoundaryBlock = cachedBoundary
			logger.Info("✅ [SNAPSHOT FIX] Using cached boundary block #%d for epoch %d (from LoadEpochData)",
				trueBoundaryBlock, blockEpoch)

			// Also use the cached timestamp if available
			if cachedTs, ok := cs.epochStartTimestamps[blockEpoch]; ok && cachedTs > 0 {
				alignTimestampMs = cachedTs
				logger.Info("✅ [SNAPSHOT FIX] Using cached timestamp %d for epoch %d", alignTimestampMs, blockEpoch)
			} else {
				alignTimestampMs = blockTimestamp
			}
		} else {
			// ═══════════════════════════════════════════════════════════════
			// STRATEGY 2: Walk parent chain via BlockDatabase.
			// No cached boundary exists. Walk backwards from the last block
			// using the parent hash chain (Header.LastBlockHash) instead of
			// the blockNumber→hash index (which requires BlockChain singleton).
			// BlockDatabase is always available at this point.
			// ═══════════════════════════════════════════════════════════════
			logger.Info("🔍 [SNAPSHOT FIX] No cached boundary for epoch %d. Walking parent chain from block #%d to find true boundary.",
				blockEpoch, blockNumber)

			blockDB := cs.GetBlockDatabase()
			if blockDB != nil {
				lastBlk, err := blockDB.GetLastBlock()
				if err == nil && lastBlk != nil {
					walkBlk := lastBlk
					scanned := 0
					for walkBlk != nil && scanned < 1000 {
						if walkBlk.Header().Epoch() < blockEpoch {
							trueBoundaryBlock = walkBlk.Header().BlockNumber()
							alignTimestampMs = walkBlk.Header().TimeStamp()
							logger.Info("✅ [SNAPSHOT FIX] Found true boundary block #%d (epoch %d) via parent chain walk",
								trueBoundaryBlock, walkBlk.Header().Epoch())
							break
						}
						parentHash := walkBlk.Header().LastBlockHash()
						if parentHash == (common.Hash{}) {
							break // reached genesis
						}
						walkBlk, err = blockDB.GetBlockByHash(parentHash)
						if err != nil {
							break
						}
						scanned++
					}
				}
			}

			// If we still don't have a timestamp, derive from block header
			if alignTimestampMs == 0 {
				alignTimestampMs = blockTimestamp
			}
		}

		// Genesis special case
		if blockEpoch == 0 {
			if genesisTs, ok := cs.epochStartTimestamps[0]; ok && genesisTs > 0 {
				logger.Info("✅ [SNAPSHOT FIX] Using cached genesis timestamp %d for epoch 0 instead of block timestamp %d", genesisTs, alignTimestampMs)
				alignTimestampMs = genesisTs
			}
		}

		cs.advanceEpochLocked(blockEpoch, alignTimestampMs, trueBoundaryBlock, 0)
	} else {
		logger.Info("✅ [SNAPSHOT FIX] Epoch aligned: ChainState.currentEpoch=%d matches lastBlock.epoch=%d",
			cs.currentEpoch, blockEpoch)
	}
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

	// --- NOMT CHANGELOG PRUNING ---
	// If newEpoch >= 2, we prune changelog data from (newEpoch - 2).
	// This prevents the disk from filling up on validators.
	if newEpoch >= 2 {
		epochToPrune := newEpoch - 2
		boundaryBlockToPrune, ok := cs.epochBoundaryBlocks[epochToPrune]
		if ok && boundaryBlockToPrune > 0 {
			go func(epoch, block uint64) {
				logger.Info("🧹 [CHANGELOG PRUNER] Starting background prune for epoch %d (before block %d)", epoch, block)
				
				if cs.changelogDB != nil {
					if err := cs.changelogDB.PruneBeforeBlock(block); err != nil {
						logger.Error("❌ [CHANGELOG PRUNER] Failed to prune account changelog: %v", err)
					}
				}
				if cs.stakeChangelogDB != nil {
					if err := cs.stakeChangelogDB.PruneBeforeBlock(block); err != nil {
						logger.Error("❌ [CHANGELOG PRUNER] Failed to prune stake changelog: %v", err)
					}
				}
				if cs.blobStore != nil {
					if err := cs.blobStore.PruneBeforeBlock(block); err != nil {
						logger.Error("❌ [CHANGELOG PRUNER] Failed to prune blob store: %v", err)
					}
				}
				logger.Info("✅ [CHANGELOG PRUNER] Finished pruning for epoch %d", epoch)
			}(epochToPrune, boundaryBlockToPrune)
		}
	}
}

// CheckAndUpdateEpochFromBlock checks if the incoming block has a higher epoch and auto-updates
// This is critical for late-joining nodes that receive blocks via network sync
// When a node joins after epoch has advanced, it needs to auto-detect the current epoch from blocks
// MULTI-EPOCH SUPPORT: For jumps > 1 epoch, directly advance to target epoch using current storage state.
// This handles restart catch-up scenarios where Go Sub receives blocks from a much later epoch.
func (cs *ChainState) CheckAndUpdateEpochFromBlock(blockEpoch uint64, blockTimestamp uint64) bool {
	// ═══════════════════════════════════════════════════════════════════════════
	// FAST PATH (RLock): Check if epoch update is needed before doing any heavy I/O
	// ═══════════════════════════════════════════════════════════════════════════
	cs.epochMutex.RLock()
	currentEpoch := cs.currentEpoch
	needsUpdate := blockEpoch > currentEpoch
	cs.epochMutex.RUnlock()

	if !needsUpdate {
		return false // No update needed
	}

	epochDiff := blockEpoch - currentEpoch
	if epochDiff > 1 {
		logger.Info("🔄 [AUTO-EPOCH SYNC] Multi-epoch jump: current=%d → target=%d (diff=%d). "+
			"Advancing directly using current storage state.",
			currentEpoch, blockEpoch, epochDiff)
	} else {
		logger.Info("🔄 [AUTO-EPOCH SYNC] Detected higher epoch from incoming block",
			"block_epoch", blockEpoch,
			"current_epoch", currentEpoch,
			"block_timestamp", blockTimestamp)
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// I/O PHASE (Lockless): Do heavy LevelDB backwards search WITHOUT epochMutex
	// ═══════════════════════════════════════════════════════════════════════════
	// STEP 1: Calculate boundary block
	// CRITICAL FIX (April 2026): Find the exact boundary block deterministically.
	// The boundary block is exactly the HIGHEST block with epoch < blockEpoch.
	searchBlock := storage.GetLastBlockNumber()
	var boundaryBlock uint64 = 0

	for searchBlock > 0 {
		hash, ok := GetBlockChainInstance().GetBlockHashByNumber(searchBlock)
		if !ok {
			searchBlock--
			continue
		}
		blockData, err := cs.GetBlockDatabase().GetBlockByHash(hash)
		if err != nil {
			searchBlock--
			continue
		}
		if blockData.Header().Epoch() < blockEpoch {
			boundaryBlock = searchBlock
			break
		}
		searchBlock--
	}

	// STEP 2: MUST read boundary block - NO FALLBACK ALLOWED
	// If boundary block is not available, DEFER the epoch update
	// ═══════════════════════════════════════════════════════════════════════════
	// WRITE PHASE (Locked): Apply updates and cache safely
	// ═══════════════════════════════════════════════════════════════════════════
	lockStart := time.Now()
	cs.epochMutex.Lock()
	if d := time.Since(lockStart); d > 50*time.Millisecond {
		logger.Warn("🚨 [LOCK CONTENTION] epochMutex Lock in CheckAndUpdateEpochFromBlock took %v", d)
	}
	defer cs.epochMutex.Unlock()

	// DOUBLE-CHECK: Ensure no other goroutine advanced the epoch while we were doing I/O
	if blockEpoch <= cs.currentEpoch {
		return false
	}

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

		epochTimestampMs = boundaryBlockData.Header().TimeStamp()
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

	// AGGRESSIVE CACHING: Proactively cache validators for the new epoch
	// RUN SYNCHRONOUSLY: We must NOT run this in a goroutine because it calls
	// stakeDB.GetAllValidators() -> Nomt.GetAll() -> handle.Read() which would
	// RACE with the background PersistAsync() -> CommitPayload() of the boundary block!
	if boundaryBlock > 0 {
		targetEpoch := blockEpoch
		logger.Info("🔄 [AUTO-EPOCH SYNC] Proactively caching validators for epoch %d at boundary block %d", targetEpoch, boundaryBlock)
		stakeDB := cs.GetStakeStateDB()
		if stakeDB != nil {
			validators, err := stakeDB.GetAllValidators()
			if err != nil || len(validators) == 0 {
				logger.Warn("⚠️ [AUTO-EPOCH SYNC] Failed to actively cache validators: %v (len=%d) - NOMT knownKeys amnesia likely.", err, len(validators))
			} else {
				validatorInfoList := &pb.ValidatorInfoList{
					EpochTimestampMs:    epochTimestampMs,
					LastGlobalExecIndex: boundaryGei,
				}

				for _, v := range validators {
					stakeNormalized := big.NewInt(1000000)
					if totalStake := v.TotalStakedAmount(); totalStake != nil && totalStake.Sign() > 0 {
						stakeNormalized = new(big.Int).Div(totalStake, big.NewInt(1_000_000_000_000_000_000))
						if stakeNormalized.Sign() <= 0 {
							stakeNormalized = big.NewInt(1)
						}
					}

					val := &pb.ValidatorInfo{
						Address:                    v.Address().Hex(),
						Stake:                      stakeNormalized.String(),
						AuthorityKey:               v.AuthorityKey(),
						ProtocolKey:                v.ProtocolKey(),
						NetworkKey:                 v.NetworkKey(),
						Name:                       v.Name(),
						Description:                v.Description(),
						Website:                    v.Website(),
						Image:                      v.Image(),
						CommissionRate:             v.CommissionRate(),
						MinSelfDelegation:          v.MinSelfDelegation().String(),
						AccumulatedRewardsPerShare: v.AccumulatedRewardsPerShare().String(),
						P2PAddress:                 v.P2PAddress(),
					}
					validatorInfoList.Validators = append(validatorInfoList.Validators, val)
				}

				if serializedData, err := json.Marshal(validatorInfoList); err == nil {
					// CRITICAL FIX: Use unlocked version to prevent deadlock (we already hold epochMutex.Lock())
					cs.setEpochValidatorsLocked(targetEpoch, serializedData)
					logger.Info("✅ [AUTO-EPOCH SYNC] Successfully cached %d validators for epoch %d", len(validatorInfoList.Validators), targetEpoch)
				} else {
					logger.Warn("⚠️ [AUTO-EPOCH SYNC] Failed to serialize validators for cache: %v", err)
				}
			}
		}
	}

	return true // Epoch was updated
}

// SetBackupPath sets the node-specific backup path for epoch data persistence
// This should be called with the node's data directory path to prevent epoch collision
// CRITICAL: This also reloads epoch data from the correct node-specific path
func (cs *ChainState) SetBackupPath(path string) {
	cs.backupPath = path
	logger.Info("📁 [EPOCH PERSISTENCE] Set node-specific backup path: %s", path)

	// CRITICAL FIX: Reset the epoch state BEFORE reloading to prevent pollution
	// from the shared /tmp/epoch_data_backup.json loaded during NewChainState().
	// If the node-specific file doesn't exist, we must start from a clean state (epoch 0).
	cs.epochMutex.Lock()
	cs.currentEpoch = 0
	cs.epochStartTimestampMs = 0
	cs.epochStartTimestamps = make(map[uint64]uint64)
	cs.epochBoundaryBlocks = make(map[uint64]uint64)
	cs.epochBoundaryGeis = make(map[uint64]uint64)
	cs.epochValidatorsCache = make(map[uint64]json.RawMessage)
	cs.epochMutex.Unlock()

	// CRITICAL: Reload epoch data from the correct node-specific path
	// This is necessary because NewChainState() calls LoadEpochData() before SetBackupPath()
	// which may load stale data from /tmp/epoch_data_backup.json (shared fallback)
	logger.Info("🔄 [EPOCH PERSISTENCE] Reloading epoch data from node-specific backup path...")
	if err := cs.LoadEpochData(); err != nil {
		logger.Warn("⚠️ [EPOCH PERSISTENCE] Failed to reload epoch data from node-specific path, keeping clean state", "error", err)
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
			delete(cs.epochValidatorsCache, epoch)
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

// SaveEpochDataSafe saves epoch data while acquiring the epochMutex.
// Use this from external callers (e.g., HandleAdvanceEpochRequest) that need
// to persist after modifying the validator cache.
// DO NOT call from within advanceEpochLocked or other methods that already hold the lock.
func (cs *ChainState) SaveEpochDataSafe() error {
	cs.epochMutex.Lock()
	defer cs.epochMutex.Unlock()
	return cs.SaveEpochData()
}

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
		EpochBoundaryBlocks:   cs.epochBoundaryBlocks,  // Include epoch boundary blocks
		EpochBoundaryGeis:     cs.epochBoundaryGeis,    // Include epoch boundary GEIs
		EpochValidators:       cs.epochValidatorsCache, // NOMT-independent validator persistence
	}

	epochDataRLP := EpochDataRLP{
		CurrentEpoch:          epochData.CurrentEpoch,
		EpochStartTimestampMs: epochData.EpochStartTimestampMs,
		EpochStartTimestamps:  make([]EpochMapEntry, 0, len(epochData.EpochStartTimestamps)),
		EpochBoundaryBlocks:   make([]EpochMapEntry, 0, len(epochData.EpochBoundaryBlocks)),
		EpochBoundaryGeis:     make([]EpochMapEntry, 0, len(epochData.EpochBoundaryGeis)),
		EpochValidators:       make([]EpochValidatorEntry, 0, len(epochData.EpochValidators)),
	}

	for k, v := range epochData.EpochStartTimestamps {
		epochDataRLP.EpochStartTimestamps = append(epochDataRLP.EpochStartTimestamps, EpochMapEntry{k, v})
	}
	for k, v := range epochData.EpochBoundaryBlocks {
		epochDataRLP.EpochBoundaryBlocks = append(epochDataRLP.EpochBoundaryBlocks, EpochMapEntry{k, v})
	}
	for k, v := range epochData.EpochBoundaryGeis {
		epochDataRLP.EpochBoundaryGeis = append(epochDataRLP.EpochBoundaryGeis, EpochMapEntry{k, v})
	}
	for k, v := range epochData.EpochValidators {
		epochDataRLP.EpochValidators = append(epochDataRLP.EpochValidators, EpochValidatorEntry{k, []byte(v)})
	}

	data, err := rlp.EncodeToBytes(epochDataRLP)
	if err != nil {
		logger.Error("❌ [EPOCH PERSISTENCE] Failed to rlp encode epoch data", "error", err)
		return fmt.Errorf("failed to rlp encode epoch data: %w", err)
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

	if len(data) == 0 {
		return fmt.Errorf("empty epoch data")
	}

	var epochData EpochData

	// Fallback logic for backward compatibility
	if data[0] == '{' { // JSON format starts with {
		if err := json.Unmarshal(data, &epochData); err != nil {
			logger.Error("❌ [EPOCH PERSISTENCE] Failed to unmarshal JSON epoch data", "error", err)
			return err
		}
	} else {
		// RLP format
		var epochDataRLP EpochDataRLP
		if err := rlp.DecodeBytes(data, &epochDataRLP); err != nil {
			logger.Error("❌ [EPOCH PERSISTENCE] Failed to decode RLP epoch data", "error", err)
			return err
		}

		epochData = EpochData{
			CurrentEpoch:          epochDataRLP.CurrentEpoch,
			EpochStartTimestampMs: epochDataRLP.EpochStartTimestampMs,
			EpochStartTimestamps:  make(map[uint64]uint64),
			EpochBoundaryBlocks:   make(map[uint64]uint64),
			EpochBoundaryGeis:     make(map[uint64]uint64),
			EpochValidators:       make(map[uint64]json.RawMessage),
		}

		for _, entry := range epochDataRLP.EpochStartTimestamps {
			epochData.EpochStartTimestamps[entry.Key] = entry.Value
		}
		for _, entry := range epochDataRLP.EpochBoundaryBlocks {
			epochData.EpochBoundaryBlocks[entry.Key] = entry.Value
		}
		for _, entry := range epochDataRLP.EpochBoundaryGeis {
			epochData.EpochBoundaryGeis[entry.Key] = entry.Value
		}
		for _, entry := range epochDataRLP.EpochValidators {
			epochData.EpochValidators[entry.Key] = json.RawMessage(entry.Value)
		}
	}

	cs.epochMutex.Lock()
	defer cs.epochMutex.Unlock()

	// Restore epoch state - preserve exact millisecond precision without rounding
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
	if epochData.EpochValidators != nil {
		cs.epochValidatorsCache = epochData.EpochValidators
		logger.Info("✅ [EPOCH VALIDATORS] Loaded cached validators for %d epochs", len(epochData.EpochValidators))
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

// CheckpointChangelogs creates atomic PebbleDB checkpoints for changelog databases if enabled
func (cs *ChainState) CheckpointChangelogs(destBaseDir string) error {
	if cs.changelogDB != nil {
		cPath := filepath.Join(destBaseDir, "history", "changelog_db_account")
		if err := cs.changelogDB.Checkpoint(cPath); err != nil {
			return fmt.Errorf("failed to checkpoint changelog_db_account: %w", err)
		}
		logger.Info("✅ [STATE CHANGELOG] Checkpointed changelog_db_account to %s", cPath)
	}
	if cs.stakeChangelogDB != nil {
		cPath := filepath.Join(destBaseDir, "history", "changelog_db_stake")
		if err := cs.stakeChangelogDB.Checkpoint(cPath); err != nil {
			return fmt.Errorf("failed to checkpoint changelog_db_stake: %w", err)
		}
		logger.Info("✅ [STATE CHANGELOG] Checkpointed changelog_db_stake to %s", cPath)
	}
	return nil
}

// Close releases any resources held by the chain state databases and tries.
func (cs *ChainState) Close() {
	if asDB := cs.GetAccountStateDB(); asDB != nil {
		asDB.Close()
	}
	if stakeDB := cs.GetStakeStateDB(); stakeDB != nil {
		if closer, ok := stakeDB.Trie().(interface{ Close() }); ok {
			closer.Close()
		}
	}
	if scDB := cs.GetSmartContractDB(); scDB != nil {
		scDB.Discard()
	}
	if cs.changelogDB != nil {
		cs.changelogDB.Close()
	}
	if cs.stakeChangelogDB != nil {
		cs.stakeChangelogDB.Close()
	}
	if cs.blobStore != nil {
		cs.blobStore.Close()
	}
}

// CloseSpeculative releases in-memory trie sessions of a cloned ChainState
// WITHOUT closing the shared changelog databases.
func (cs *ChainState) CloseSpeculative() {
	if asDB := cs.GetAccountStateDB(); asDB != nil {
		asDB.Close()
	}
	if stakeDB := cs.GetStakeStateDB(); stakeDB != nil {
		if closer, ok := stakeDB.Trie().(interface{ Close() }); ok {
			closer.Close()
		}
	}
	if scDB := cs.GetSmartContractDB(); scDB != nil {
		scDB.Discard()
	}
}
