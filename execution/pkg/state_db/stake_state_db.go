package stake_state_db

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// StakeStateDB quản lý state staking của các validator sử dụng Merkle Patricia Trie.
type StakeStateDB struct {
	trie p_trie.StateTrie

	originRootHash common.Hash
	db             storage.Storage
	//cache tránh trie truy vấn nhiều lần
	dirtyValidators sync.Map

	lockedFlag atomic.Bool

	stakeBatch []byte

	muCommit sync.Mutex

	// FORK-SAFETY: persistReady is closed by PersistAsync after trie swap completes.
	// IntermediateRoot(true) waits on this channel before proceeding,
	// ensuring the trie reference reflects the previous block's committed state.
	persistReady chan struct{}
}

// NewStakeStateDB tạo một instance mới của StakeStateDB.
func NewStakeStateDB(
	trie p_trie.StateTrie,
	db storage.Storage,
) *StakeStateDB {
	if trie == nil || db == nil {
		logger.Error("NewStakeStateDB received a nil trie or db storage")
		return nil
	}
	// Initialize persistReady as pre-closed (first block won't wait)
	initReady := make(chan struct{})
	close(initReady)
	return &StakeStateDB{
		trie:            trie,
		db:              db,
		originRootHash:  trie.Hash(),
		dirtyValidators: sync.Map{},
		persistReady:    initReady,
	}
}

// Trie returns the underlying StateTrie instance.
func (db *StakeStateDB) Trie() p_trie.StateTrie {
	return db.trie
}

func (db *StakeStateDB) getOrCreateValidatorState(
	address common.Address,
) (state.ValidatorState, error) {
	value, ok := db.dirtyValidators.Load(address)
	if ok {
		if vs, valid := value.(state.ValidatorState); valid && vs != nil {
			return vs, nil
		}
	}

	trieToUse := db.trie
	if trieToUse == nil {
		return nil, errors.New("stake state DB has a nil trie")
	}
	bData, err := trieToUse.Get(address.Bytes())
	if err != nil {
		return nil, fmt.Errorf("error getting %s from Trie: %w", address.Hex(), err)
	}

	var stateToStore state.ValidatorState
	if len(bData) == 0 {
		stateToStore = state.NewValidatorState(address)
	} else {
		loadedVs := state.NewValidatorState(address)
		if err = loadedVs.Unmarshal(bData); err != nil {
			return nil, fmt.Errorf("error unmarshalling %s from Trie: %w", address.Hex(), err)
		}
		stateToStore = loadedVs
	}

	if db.lockedFlag.Load() {
		return stateToStore, nil
	}

	actualValue, _ := db.dirtyValidators.LoadOrStore(address, stateToStore)
	finalVs, _ := actualValue.(state.ValidatorState)
	return finalVs, nil
}

func (db *StakeStateDB) setDirtyValidatorState(vs state.ValidatorState) {
	if vs == nil {
		return
	}
	if db.lockedFlag.Load() {
		logger.Error("setDirtyValidatorState called while db is locked — skipping store")
		return
	}
	db.dirtyValidators.Store(vs.Address(), vs)
}

// --- Các hàm sửa đổi State ---
func (db *StakeStateDB) CreateRegister(
	address common.Address,
	name string,
	description string,
	website string,
	image string,
	commissionRate uint64,
	minSelfDelegation *big.Int,
	primaryAddress string,
	workerAddress string,
	p2pAddress string,
	pubKeyBls string,
	pubKeySecp string,
) error {
	// Backward compatible: use name as hostname, pubKeyBls as authorityKey
	return db.CreateRegisterWithKeys(address, name, description, website, image, commissionRate, minSelfDelegation,
		primaryAddress, workerAddress, p2pAddress, pubKeyBls, pubKeySecp, pubKeySecp, name, pubKeyBls)
}

// CreateRegisterWithKeys registers a validator with separate protocol_key and network_key
// This is the full version that supports committee.json compatibility
func (db *StakeStateDB) CreateRegisterWithKeys(
	address common.Address,
	name string,
	description string,
	website string,
	image string,
	commissionRate uint64,
	minSelfDelegation *big.Int,
	primaryAddress string,
	workerAddress string,
	p2pAddress string,
	pubKeyBls string,
	protocolKey string,
	networkKey string,
	hostname string,
	authorityKey string,
) error {
	if db.lockedFlag.Load() {
		return errors.New("CreateRegister: db is locked")
	}
	vs, err := db.getOrCreateValidatorState(address)
	if err != nil {
		return err
	}
	vs.SetName(name)
	vs.SetDescription(description)
	vs.SetWebsite(website)
	vs.SetImage(image)
	vs.SetCommissionRate(commissionRate)
	vs.SetMinSelfDelegation(minSelfDelegation)
	vs.SetPrimaryAddress(primaryAddress)
	vs.SetWorkerAddress(workerAddress)
	vs.SetP2PAddress(p2pAddress)
	vs.SetPubKeyBls(pubKeyBls)
	// CRITICAL: Set protocol_key and network_key separately for committee.json compatibility
	// SetProtocolKey() will automatically set PubkeySecp for backward compatibility if empty
	vs.SetProtocolKey(protocolKey)   // Set protocol_key (Ed25519) - tương thích với committee.json
	vs.SetNetworkKey(networkKey)     // Set network_key (Ed25519) - tương thích với committee.json
	vs.SetHostname(hostname)         // Set hostname - tương thích với committee.json
	vs.SetAuthorityKey(authorityKey) // Set authority_key (BLS) - tương thích với committee.json

	db.setDirtyValidatorState(vs)

	return nil
}
func (db *StakeStateDB) DeleteValidator(address common.Address) error {
	if db.lockedFlag.Load() {
		return errors.New("DeleteValidator: db is locked")
	}
	data, err := db.GetValidator(address)
	if err != nil {
		return fmt.Errorf("could not get validator state for deletion: %w", err)
	}
	if data != nil {
		db.dirtyValidators.Store(address, nil)
	}
	return nil

}
func (db *StakeStateDB) GetDelegation(validatorAddress, delegatorAddress common.Address) (*big.Int, *big.Int, error) {
	vs, err := db.GetValidator(validatorAddress)
	if err != nil {
		return big.NewInt(0), big.NewInt(0), fmt.Errorf("could not get validator state for deletion: %w", err)
	}
	if vs == nil {
		return big.NewInt(0), big.NewInt(0), nil
	}
	amount, rewardDebt := vs.GetDelegation(delegatorAddress)
	return amount, rewardDebt, nil
}

func (db *StakeStateDB) Delegate(validatorAddress, delegatorAddress common.Address, amount *big.Int) error {
	if db.lockedFlag.Load() {
		return errors.New("Delegate: db is locked")
	}
	vs, err := db.getOrCreateValidatorState(validatorAddress)
	if err != nil {
		return err
	}
	vs.SetDelegate(delegatorAddress, amount)
	db.setDirtyValidatorState(vs)

	return nil
}

func (db *StakeStateDB) Undelegate(delegatorAddress, validatorAddress common.Address, amount *big.Int) error {
	if db.lockedFlag.Load() {
		return errors.New("Undelegate: db is locked")
	}
	vs, err := db.GetValidator(validatorAddress)
	if err != nil {
		return err
	}
	if vs == nil {
		return fmt.Errorf("validator %s not found", validatorAddress.Hex())
	}
	if err := vs.SetUndelegate(delegatorAddress, amount); err != nil {
		return err
	}
	db.setDirtyValidatorState(vs)
	return nil
}

// --- Quản lý Phần thưởng ---

func (db *StakeStateDB) DistributeRewardsToValidator(validatorAddress common.Address, totalBlockReward *big.Int) (*big.Int, error) {
	if db.lockedFlag.Load() {
		return nil, errors.New("DistributeRewardsToValidator: db is locked")
	}
	vs, err := db.GetValidator(validatorAddress)
	if err != nil {
		return nil, fmt.Errorf("could not get validator state for reward distribution: %w", err)
	}
	rewardAmount := vs.DistributeRewards(totalBlockReward)
	db.setDirtyValidatorState(vs)
	return rewardAmount, nil
}

func (db *StakeStateDB) WithdrawRewardFromValidator(validatorAddress, delegatorAddress common.Address) (*big.Int, error) {
	if db.lockedFlag.Load() {
		return nil, errors.New("WithdrawRewardFromValidator: db is locked")
	}
	vs, err := db.getOrCreateValidatorState(validatorAddress)
	if err != nil {
		return nil, err
	}
	rewardAmount := vs.WithdrawReward(delegatorAddress)
	return rewardAmount, nil
}
func (db *StakeStateDB) ResetRewardDebtForDelegator(validatorAddress, delegatorAddress common.Address) error {
	if db.lockedFlag.Load() {
		return errors.New("ResetRewardDebtForDelegator: db is locked")
	}
	vs, err := db.GetValidator(validatorAddress)
	if err != nil {
		return err
	}
	if vs == nil {
		return fmt.Errorf("validator %s not found", validatorAddress.Hex())
	}
	vs.ResetRewardDebt(delegatorAddress)
	db.setDirtyValidatorState(vs)
	return nil
}
func (db *StakeStateDB) GetPendingRewards(validatorAddress, delegatorAddress common.Address) (*big.Int, error) {
	if db.lockedFlag.Load() {
		return nil, errors.New("GetPendingRewards: db is locked")
	}
	vs, err := db.getOrCreateValidatorState(validatorAddress)
	if err != nil {
		return nil, err
	}
	rewardAmount := vs.WithdrawReward(delegatorAddress)
	return rewardAmount, nil
}

// --- Các hàm truy vấn ---

func (db *StakeStateDB) GetAllValidators() ([]state.ValidatorState, error) {
	trieToUse := db.trie
	if trieToUse == nil {
		return nil, errors.New("stake state DB has a nil trie")
	}
	allData, err := trieToUse.GetAll()


	if err != nil {
		return nil, fmt.Errorf("error getting all data from trie: %w", err)
	}
	allValidators := make([]state.ValidatorState, 0, len(allData))
	for addressStr, validatorStateBytes := range allData {
		address := common.HexToAddress(addressStr)
		validatorState := state.NewValidatorState(address)
		if err := validatorState.Unmarshal(validatorStateBytes); err != nil {
			logger.Warn("Failed to unmarshal validator state", "address", address.Hex(), "error", err)
			continue
		}
		allValidators = append(allValidators, validatorState)
	}

	// Sắp xếp các validator theo số lượng CỔ ĐÔNG (TotalStakedAmount) giảm dần
	sort.Slice(allValidators, func(i, j int) bool {
		cmp := allValidators[i].TotalStakedAmount().Cmp(allValidators[j].TotalStakedAmount())
		if cmp == 0 {
			// Đảm bảo tính nhất quán khi sort (nếu amount bằng nhau thì so sánh address)
			return bytes.Compare(allValidators[i].Address().Bytes(), allValidators[j].Address().Bytes()) < 0
		}
		return cmp > 0 // Giảm dần
	})

	// Tạm thời hardcode lấy top N validator (vd: 21)
	topN := 21
	if len(allValidators) > topN {
		allValidators = allValidators[:topN]
	}

	return allValidators, nil
}

func (db *StakeStateDB) GetValidatorCount() (int, error) {
	data, err := db.GetAllValidators()
	if err != nil {
		return 0, err
	}

	return len(data), nil
}
func (db *StakeStateDB) GetValidator(address common.Address) (state.ValidatorState, error) {
	// 1. Kiểm tra cache trước
	value, ok := db.dirtyValidators.Load(address)
	if ok {
		if vs, valid := value.(state.ValidatorState); valid || vs == nil {
			return vs, nil
		}
	}
	// 2. Truy vấn Trie
	trieToUse := db.trie
	if trieToUse == nil {
		return nil, errors.New("stake state DB has a nil trie")
	}
	bData, err := trieToUse.Get(address.Bytes())


	if err != nil {
		return nil, fmt.Errorf("error getting %s from Trie: %w", address.Hex(), err)
	}
	// 3. Nếu không có dữ liệu, validator không tồn tại
	if len(bData) == 0 {
		return nil, nil // Không tìm thấy
	}
	// 4. Giải mã dữ liệu và trả về
	// là chỉ đang lấy đối tượng vs để thực thi các hàm thôi
	vs := state.NewValidatorState(address)
	if err = vs.Unmarshal(bData); err != nil {
		return nil, fmt.Errorf("error unmarshalling %s from Trie: %w", address.Hex(), err)
	}

	return vs, nil
}

func (db *StakeStateDB) SetCommissionRate(address common.Address, newRate uint64) (bool, error) {
	if db.lockedFlag.Load() {
		return false, errors.New("GetPendingRewards: db is locked")
	}
	vs, err := db.GetValidator(address)
	if err != nil {
		return false, err
	}
	if vs == nil {
		return false, fmt.Errorf("validator %s not found", address.Hex())
	}
	vs.SetCommissionRate(newRate)
	db.setDirtyValidatorState(vs)
	return true, nil
}

func (db *StakeStateDB) UpdateValidatorInfo(address common.Address, name, description, website, image string) (bool, error) {
	if db.lockedFlag.Load() {
		return false, errors.New("GetPendingRewards: db is locked")
	}
	vs, err := db.GetValidator(address)
	if err != nil {
		return false, err
	}
	if vs == nil {
		return false, fmt.Errorf("validator %s not found", address.Hex())
	}
	vs.SetName(name)
	vs.SetDescription(description)
	vs.SetWebsite(website)
	vs.SetImage(image)

	db.setDirtyValidatorState(vs)
	return true, nil
}

func (db *StakeStateDB) GetAndSortValidators(descending bool) ([]state.ValidatorState, error) {
	validators, err := db.GetAllValidators()
	if err != nil {
		return nil, err
	}
	sort.Slice(validators, func(i, j int) bool {
		cmp := validators[i].TotalStakedAmount().Cmp(validators[j].TotalStakedAmount())
		if cmp == 0 {
			return bytes.Compare(validators[i].Address().Bytes(), validators[j].Address().Bytes()) < 0
		}
		if descending {
			return cmp > 0
		}
		return cmp < 0
	})
	return validators, nil
}
func (db *StakeStateDB) GetValidatorAddresses() ([]common.Address, error) {
	sortedValidators, err := db.GetAndSortValidators(true)
	if err != nil {
		return nil, err
	}
	addresses := make([]common.Address, len(sortedValidators))
	for i, v := range sortedValidators {
		addresses[i] = v.Address()
	}
	return addresses, nil
}
func (db *StakeStateDB) GetValidatorIndex(validatorAddress common.Address) (*big.Int, bool, error) {
	sortedValidators, err := db.GetAndSortValidators(true)
	if err != nil {
		return nil, false, err
	}
	for i, v := range sortedValidators {
		if v.Address() == validatorAddress {
			return big.NewInt(int64(i)), true, nil
		}
	}

	return nil, false, nil // Không tìm thấy
}

// --- Quản lý State: Commit, Discard, ... ---

// InvalidateAllCaches clears the in-memory dirtyValidators cache
// WITHOUT touching the trie itself.
//
// CRITICAL FOR SUB NODES: When a Sub node applies blocks received from Master via
// `applyBlockBatch()`, the data is written directly to NOMT/PebbleDB, bypassing
// StakeStateDB entirely. This means dirtyValidators may contain stale data from
// previous RPC queries. Without this call, subsequent validator queries would
// return old data instead of the freshly synced data.
func (db *StakeStateDB) InvalidateAllCaches() {
	db.dirtyValidators.Clear()
	logger.Debug("StakeStateDB.InvalidateAllCaches: Cleared dirtyValidators (Sub-node sync safe)")
}

// ⭐ HÀM MỚI: SetStakeBatch stores a serialized batch of stake data.
func (db *StakeStateDB) SetStakeBatch(batch []byte) {
	db.stakeBatch = batch
}

// ⭐ HÀM MỚI: GetStakeBatch retrieves and clears the stored stake batch.
func (db *StakeStateDB) GetStakeBatch() []byte {
	batch := db.stakeBatch
	db.stakeBatch = nil // Clear after retrieval
	return batch
}

func (db *StakeStateDB) GetStorage() storage.Storage {
	return db.db
}

// IntermediateRoot applies dirty validator states to the in-memory trie and returns the new root hash.
// This function is crucial for the commit process.
func (db *StakeStateDB) IntermediateRoot(isLockProcess ...bool) (common.Hash, error) {
	fileLogger, _ := loggerfile.NewFileLogger(fmt.Sprintf("IntermediateRoot_" + ".log"))

	var lockProcess bool
	if len(isLockProcess) > 0 {
		lockProcess = isLockProcess[0]
	} else {
		lockProcess = true // Default to true
	}

	if lockProcess {
		if db.lockedFlag.Load() {
			err := errors.New("IntermediateRoot (lockProcess=true): db.lockedFlag is already locked")
			logger.Error(err.Error())
			return common.Hash{}, err
		}
		db.lockedFlag.Store(true)

		// ═══════════════════════════════════════════════════════════════
		// DEFERRED PERSIST GATE (MOVED TO CommitPipeline):
		// For NOMT/FlatTrie, we no longer wait for persistReady here 
		// because we use BatchUpdateWithCachedOldValues which does not 
		// touch C++ or read from DB.
		// ═══════════════════════════════════════════════════════════════

	} else {
		if !db.lockedFlag.Load() {
			err := errors.New("IntermediateRoot (lockProcess=false): db.lockedFlag is not locked")
			logger.Error(err.Error())
			return common.Hash{}, err
		}
		defer func() {
			db.dirtyValidators.Clear()
			db.lockedFlag.Store(false)
		}()
	}

	if db.trie == nil {
		return common.Hash{}, errors.New("trie is nil")
	}

	var (
		updateErr   error
		hasChanges  bool = false
		batchKeys   [][]byte
		batchValues [][]byte
	)

	var dirtyAddresses []common.Address
	db.dirtyValidators.Range(func(key, _ interface{}) bool {
		if address, ok := key.(common.Address); ok {
			dirtyAddresses = append(dirtyAddresses, address)
		}
		return true
	})

	// CRITICAL FIX: Sort addresses to ensure deterministic trie updates
	slices.SortFunc(dirtyAddresses, func(a, b common.Address) int {
		return bytes.Compare(a[:], b[:])
	})

	for _, address := range dirtyAddresses {
		value, ok := db.dirtyValidators.Load(address)
		if !ok {
			continue
		}
		
		hasChanges = true
		var bytesToStore []byte
		if value != nil {
			vs, ok2 := value.(state.ValidatorState)
			fileLogger.Info("IntermediateRoot: vs", vs)
			if !ok2 {
				updateErr = fmt.Errorf("invalid value type for address %s", address.Hex())
				break 
			}
			var err error
			bytesToStore, err = vs.Marshal()
			if err != nil {
				updateErr = fmt.Errorf("marshal error for %s: %w", address.Hex(), err)
				break // stop iteration
			}
		}

		batchKeys = append(batchKeys, address.Bytes())
		batchValues = append(batchValues, bytesToStore)
	}

	if updateErr != nil {
		return common.Hash{}, updateErr
	}

	if len(batchKeys) > 0 {
		isNOMT := false
		if nomtTrie, ok := db.trie.(*p_trie.NomtStateTrie); ok {
			isNOMT = true
			<-db.persistReady // For NOMT, we MUST wait for the C++ CommitPayload to complete
			if err := nomtTrie.BatchUpdateWithCachedOldValues(batchKeys, batchValues, nil); err != nil {
				updateErr = fmt.Errorf("trie BatchUpdateWithCachedOldValues error: %w", err)
			}
		} 
		
		if !isNOMT {
			// For MPT, we MUST wait for the old trie pointer swap to complete
			<-db.persistReady
			for i, key := range batchKeys {
				if err := db.trie.Update(key, batchValues[i]); err != nil {
					updateErr = fmt.Errorf("trie update error for key %x: %w", key, err)
					break
				}
			}
		}
	}

	if updateErr != nil {
		return common.Hash{}, updateErr
	}

	if hasChanges {

		newHash := db.trie.Hash()
		logger.Debug("Calculated new intermediate hash for stake state", "newHash", newHash)
		fileLogger.Info("IntermediateRoot: hasChanges", newHash)

		return newHash, nil
	} else {
		nHash := db.trie.Hash()
		fileLogger.Info("IntermediateRoot: nHash", nHash)

		return nHash, nil // Return current hash if no changes
	}
}

func (db *StakeStateDB) Commit() (common.Hash, error) {
	fileLogger, _ := loggerfile.NewFileLogger(fmt.Sprintf("Commit" + ".log"))

	db.muCommit.Lock()
	defer db.muCommit.Unlock()

	if !db.lockedFlag.Load() {
		return common.Hash{}, errors.New("Commit: db is not already locked")
	}

	// CRITICAL FIX: IntermediateRoot MUST be called BEFORE trie.Commit().
	// IntermediateRoot applies dirty validators to trie → computes hash.
	// trie.Commit() then commits the same dirty entries → must produce same hash.
	// Previous order (Commit first, IntermediateRoot second) caused FlatStateTrie hash mismatch
	// because Commit() cleared dirty map, then IntermediateRoot() re-applied dirty validators
	// creating a double-application.
	intermediateHash, err := db.IntermediateRoot(false)
	if err != nil {
		return common.Hash{}, fmt.Errorf("trie IntermediateRoot calculation failed: %w", err)
	}
	fileLogger.Info("IntermediateRoot: intermediateHash", intermediateHash)

	committedHash, nodeSet, _, err := db.trie.Commit(true)
	if err != nil {
		return common.Hash{}, fmt.Errorf("trie Commit calculation failed: %w", err)
	}
	fileLogger.Info("Commit: committedHash", committedHash)

	// Commit NOMT payload sequentially
	if nomtTrie, isNomt := db.trie.(*p_trie.NomtStateTrie); isNomt {
		if err := nomtTrie.CommitPayload(); err != nil {
			return common.Hash{}, fmt.Errorf("NOMT CommitPayload failed: %w", err)
		}
	}

	// NOTE: NomtStateTrie skips this check because its Commit() writes the registry
	// which deterministically changes the root hash.
	if _, isNomt := db.trie.(*p_trie.NomtStateTrie); !isNomt {
		if intermediateHash != committedHash {
			return common.Hash{}, fmt.Errorf("root hash mismatch after commit (intermediate: %s, commit: %s)", intermediateHash, committedHash)
		}
	}

	// Handle replication batch for Sub nodes
	if config.ConfigApp != nil && config.ConfigApp.ServiceType == p_common.ServiceTypeMaster {
		var stakeBatchData []byte
		if nodeSet != nil && len(nodeSet.Nodes) > 0 {
			// MPT path: use nodeSet for replication
			batch := make([][2][]byte, 0, len(nodeSet.Nodes))
			for _, node := range nodeSet.Nodes {
				batch = append(batch, [2][]byte{node.Hash.Bytes(), node.Blob})
			}
			data, err := storage.SerializeBatch(batch)
			if err != nil {
				logger.Error("Commit (StakeStateDB): Failed to serialize MPT batch: %v", err)
			} else {
				stakeBatchData = data
				logger.Debug("Commit (StakeStateDB): Serialized MPT stake batch for replication, size=%d", len(data))
			}
		} else {
			// FlatStateTrie / NOMT / Verkle path
			flatBatch := db.trie.GetCommitBatch()
			if len(flatBatch) > 0 {
				data, err := storage.SerializeBatch(flatBatch)
				if err != nil {
					logger.Error("Commit (StakeStateDB): Failed to serialize flat block batch: %v", err)
				} else {
					stakeBatchData = data
					logger.Debug("Commit (StakeStateDB): Serialized flat stake batch for replication, size=%d", len(data))
				}
			}
		}
		db.SetStakeBatch(stakeBatchData)
	}

	// Persist MPT nodes / FlatTrie to local DB
	// Only persist if NOT Nomt
	if _, isNomt := db.trie.(*p_trie.NomtStateTrie); !isNomt {
		if nodeSet != nil && len(nodeSet.Nodes) > 0 {
			batch := make([][2][]byte, 0, len(nodeSet.Nodes))
			for _, node := range nodeSet.Nodes {
				batch = append(batch, [2][]byte{node.Hash.Bytes(), node.Blob})
			}
			if err := db.db.BatchPut(batch); err != nil {
				return common.Hash{}, fmt.Errorf("DB BatchPut failed: %w", err)
			}
		}
	}

	newTrie, err := p_trie.NewStateTrie(committedHash, db.db, true)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to load trie for new root %s: %w", committedHash, err)
	}

	db.trie = newTrie
	db.originRootHash = committedHash

	return committedHash, nil
}

// ═══════════════════════════════════════════════════════════════════════════════
// PIPELINE COMMIT — Split persist out of critical path (same pattern as AccountStateDB)
// ═══════════════════════════════════════════════════════════════════════════════

// StakePipelineCommitResult holds data needed for async persistence after CommitPipeline.
// The caller should pass this to PersistAsync() in a background goroutine.
type StakePipelineCommitResult struct {
	FinalHash  common.Hash
	Batch      [][2][]byte // node hash → blob pairs for DB BatchPut
	StakeBatch []byte      // serialized batch for network transfer to sub-nodes
	Trie       p_trie.StateTrie // The trie instance after Commit, to be re-used
	PersistChannel chan struct{}  // Channel created for THIS block's persist async
}

// CommitPipeline performs the fast, synchronous phase of commit:
//  1. IntermediateRoot(false) → apply dirty validators, compute hash
//  2. trie.Commit(true) → generate nodeSet
//  3. Verify intermediate == committed hash
//  4. Serialize batch for network transfer
//
// FORK-SAFETY: stakeStatesRoot is computed from IntermediateRoot(true) in ProcessTransactions
// BEFORE this method is called. CommitPipeline only generates the data needed for persistence.
// The trie remains valid for reads after Commit() because it operates on an internal copy.
//
// The caller MUST call PersistAsync() with the returned result to eventually
// persist nodes to DB and swap the trie reference.
func (db *StakeStateDB) CommitPipeline() (*StakePipelineCommitResult, error) {
	db.muCommit.Lock()
	defer db.muCommit.Unlock()

	if !db.lockedFlag.Load() {
		return nil, errors.New("CommitPipeline: db is not already locked")
	}

	// ═══════════════════════════════════════════════════════════════
	// Phase 1: Apply dirty validators and compute hash
	// ═══════════════════════════════════════════════════════════════
	intermediateHash, err := db.IntermediateRoot(false)
	if err != nil {
		return nil, fmt.Errorf("CommitPipeline: IntermediateRoot failed: %w", err)
	}

	// ═══════════════════════════════════════════════════════════════
	// Phase 2: Generate nodeSet (trie.Commit creates copy internally)
	// ═══════════════════════════════════════════════════════════════
	committedHash, nodeSet, _, err := db.trie.Commit(true)
	if err != nil {
		logger.Error("CommitPipeline (StakeStateDB): trie.Commit failed: %v", err)
		return nil, fmt.Errorf("trie Commit failed: %w", err)
	}

	// Sanity check
	if intermediateHash != committedHash {
		logger.Error("CommitPipeline (StakeStateDB): root hash mismatch: intermediate=%s, committed=%s",
			intermediateHash, committedHash)
		return nil, fmt.Errorf("root hash mismatch (intermediate: %s, committed: %s)",
			intermediateHash, committedHash)
	}

	// ═══════════════════════════════════════════════════════════════
	// Phase 3: Prepare batch data for async persist + network transfer
	// ═══════════════════════════════════════════════════════════════
	var batch [][2][]byte
	var stakeBatchData []byte

	// Handle both MPT (nodeSet) and Flat/Verkle/NOMT (GetCommitBatch) backends
	if nodeSet != nil && len(nodeSet.Nodes) > 0 {
		batch = make([][2][]byte, 0, len(nodeSet.Nodes))
		for _, n := range nodeSet.Nodes {
			if n.Hash == (common.Hash{}) {
				continue
			}
			batch = append(batch, [2][]byte{n.Hash.Bytes(), n.Blob})
		}
	} else {
		batch = db.trie.GetCommitBatch()
	}

	// Serialize for network replication (Master only)
	if config.ConfigApp != nil && config.ConfigApp.ServiceType == p_common.ServiceTypeMaster {
		if len(batch) > 0 {
			data, serErr := storage.SerializeBatch(batch)
			if serErr != nil {
				logger.Error("CommitPipeline (StakeStateDB): Failed to serialize batch: %v", serErr)
			} else {
				stakeBatchData = data
			}
		}
	}

	// Store stakeBatch for network transfer
	// ALWAYS call SetStakeBatch (even if nil) to clear any leftover batch 
	// from the previous block, ensuring we don't leak stale data to Sub nodes.
	db.SetStakeBatch(stakeBatchData)

	logger.Debug("CommitPipeline (StakeStateDB): sync phase complete, hash=%s, batch_size=%d",
		committedHash.Hex(), len(batch))

	var persistBatch [][2][]byte
	if _, isNomt := db.trie.(*p_trie.NomtStateTrie); !isNomt {
		persistBatch = batch
	} else {
		logger.Debug("➖ [STAKE DB] CommitPipeline: Skipping PebbleDB persistBatch for NOMT (handled by CommitPayload)")
	}

	// FORK-SAFETY: Set new unclosed persistReady channel.
	// PersistAsync will close it after trie swap completes.
	newPersistReady := make(chan struct{})
	db.persistReady = newPersistReady

	return &StakePipelineCommitResult{
		FinalHash:  committedHash,
		Batch:      persistBatch,
		StakeBatch: stakeBatchData,
		Trie:       db.trie, // Pass the trie along
		PersistChannel: newPersistReady,
	}, nil
}

// PersistAsync performs the slow, background phase of commit:
//  1. BatchPut nodeSet to DB (disk I/O)
//  2. Create new trie from committed hash
//  3. Swap trie reference and update originRootHash
//
// This method is designed to be called from a background goroutine.
func (db *StakeStateDB) PersistAsync(result *StakePipelineCommitResult) error {
	if result == nil {
		return nil
	}

	// ═══════════════════════════════════════════════════════════════
	// Step 1: Persist to DB (slow, disk I/O)
	// ═══════════════════════════════════════════════════════════════
	if len(result.Batch) > 0 {
		if err := db.db.BatchPut(result.Batch); err != nil {
			logger.Error("PersistAsync (StakeStateDB): BatchPut failed: %v", err)
			return fmt.Errorf("PersistAsync BatchPut failed: %w", err)
		}
	}

	// ═══════════════════════════════════════════════════════════════
	// Step 2: Create new trie and swap reference
	// ═══════════════════════════════════════════════════════════════

	if nomtTrie, isNomt := result.Trie.(*p_trie.NomtStateTrie); isNomt {
		if err := nomtTrie.CommitPayload(); err != nil {
			logger.Error("PersistAsync: NOMT CommitPayload failed", "error", err)
			return fmt.Errorf("PersistAsync (StakeStateDB): NOMT CommitPayload failed: %w", err)
		}
	}

	db.muCommit.Lock()
	if result.Trie != nil {
		db.trie = result.Trie
	} else {
		newTrie, err := p_trie.NewStateTrie(result.FinalHash, db.db, true)
		if err != nil {
			logger.Error("PersistAsync (StakeStateDB): Failed to create new trie: hash=%s, error=%v", result.FinalHash, err)
			db.muCommit.Unlock()
			return fmt.Errorf("PersistAsync: failed to load trie for root %s: %w", result.FinalHash, err)
		}
		db.trie = newTrie
	}
	db.originRootHash = result.FinalHash
	db.muCommit.Unlock()

	// FORK-SAFETY: Signal that trie swap is complete.
	if result.PersistChannel != nil {
		close(result.PersistChannel)
	} else {
		close(db.persistReady)
	}

	logger.Debug("PersistAsync (StakeStateDB): trie swapped to new root %s, persistReady signaled", result.FinalHash)
	return nil
}

func (db *StakeStateDB) Discard() error {
	if db.lockedFlag.Load() {
		return errors.New("Discard: db is locked")
	}
	db.dirtyValidators.Clear()
	newTrie, err := p_trie.NewStateTrie(db.originRootHash, db.db, true)
	if err != nil {
		return fmt.Errorf("failed to reload trie to %s: %w", db.originRootHash, err)
	}
	db.trie = newTrie
	return nil
}
