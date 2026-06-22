package account_state_db

import (
	"math/big"
	"github.com/ethereum/go-ethereum/common"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	p_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ValidationStateCache is used during Phase 2 (Sequential Validation)
// It overlays the accepted speculative writes on top of the global DB.
// It implements types.AccountStateDB and types.SmartContractDB.
type ValidationStateCache struct {
	parentAccountDB types.AccountStateDB
	parentSmartContractDB types.SmartContractDB

	acceptedAccounts map[common.Address]types.AccountState
	acceptedStorage  map[common.Address]map[string][]byte
}

func NewValidationStateCache(accountDB types.AccountStateDB, scDB types.SmartContractDB) *ValidationStateCache {
	return &ValidationStateCache{
		parentAccountDB:       accountDB,
		parentSmartContractDB: scDB,
		acceptedAccounts:      make(map[common.Address]types.AccountState),
		acceptedStorage:       make(map[common.Address]map[string][]byte),
	}
}

// Trie returns the underlying Trie from the parent account DB
func (v *ValidationStateCache) Trie() p_trie.StateTrie {
	if t, ok := v.parentAccountDB.(interface{ Trie() p_trie.StateTrie }); ok {
		return t.Trie()
	}
	return nil
}

// -----------------------------------------------------------------------------
// Read Methods (Overlay Cache -> Global DB)
// -----------------------------------------------------------------------------

func (v *ValidationStateCache) AccountState(address common.Address) (types.AccountState, error) {
	if state, ok := v.acceptedAccounts[address]; ok {
		if state != nil {
			return state.Copy(), nil
		}
		return nil, nil
	}
	return v.parentAccountDB.AccountState(address)
}

func (v *ValidationStateCache) StorageValue(address common.Address, key []byte, customRoot ...*common.Hash) ([]byte, bool) {
	if keysMap, ok := v.acceptedStorage[address]; ok {
		if val, exists := keysMap[string(key)]; exists {
			return val, true
		}
	}
	return v.parentSmartContractDB.StorageValue(address, key)
}

func (v *ValidationStateCache) Code(address common.Address) []byte {
	return v.parentSmartContractDB.Code(address)
}

// -----------------------------------------------------------------------------
// Accumulate Methods (Used to merge accepted speculative results)
// -----------------------------------------------------------------------------

func (v *ValidationStateCache) ApplyWrites(dirtyAccounts map[common.Address]types.AccountState, storageChange map[string]map[string][]byte) {
	for addr, acc := range dirtyAccounts {
		v.acceptedAccounts[addr] = acc.Copy()
	}
	for addrStr, kvs := range storageChange {
		addr := common.HexToAddress(addrStr)
		if v.acceptedStorage[addr] == nil {
			v.acceptedStorage[addr] = make(map[string][]byte)
		}
		for k, val := range kvs {
			v.acceptedStorage[addr][k] = val
		}
	}
}

func (v *ValidationStateCache) CheckConflict(readAccounts map[common.Address]types.AccountState, readStorage map[common.Address][]string) bool {
	conflictFreeAddr := utils.GetAddressSelector(p_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	for addr := range readAccounts {
		if addr == conflictFreeAddr {
			continue // Skip conflict check for the special contract
		}
		if _, ok := v.acceptedAccounts[addr]; ok {
			return true // Conflict: Another accepted TX wrote to an account we read
		}
	}
	for addr, keys := range readStorage {
		if addr == conflictFreeAddr {
			continue // Skip conflict check for the special contract
		}
		if keysMap, ok := v.acceptedStorage[addr]; ok {
			for _, k := range keys {
				if _, exists := keysMap[k]; exists {
					return true // Conflict: Another accepted TX wrote to a storage slot we read
				}
			}
		}
	}
	return false
}

func (v *ValidationStateCache) FlushToGlobal() error {
	accs := make([]types.AccountState, 0, len(v.acceptedAccounts))
	for _, acc := range v.acceptedAccounts {
		accs = append(accs, acc)
	}
	
	// Atomic batch memory write if the implementation supports it
	if accountDB, ok := v.parentAccountDB.(interface{
		PublicSetDirtyAccountStateBatch([]types.AccountState)
	}); ok {
		accountDB.PublicSetDirtyAccountStateBatch(accs)
	} else {
		for _, acc := range accs {
			v.parentAccountDB.SetState(acc)
		}
	}

	// Flush storage
	for addr, keysMap := range v.acceptedStorage {
		for kStr, val := range keysMap {
			v.parentSmartContractDB.SetStorageValue(addr, []byte(kStr), val)
		}
	}
	return nil
}

// ApplyAcceptedWritesTo copies all accepted overlay writes to another DB instance.
func (v *ValidationStateCache) ApplyAcceptedWritesTo(targetAccountDB types.AccountStateDB, targetSCDB types.SmartContractDB) {
	for _, acc := range v.acceptedAccounts {
		targetAccountDB.InjectLoadedAccount(acc.Copy())
	}
	for addr, keysMap := range v.acceptedStorage {
		for kStr, val := range keysMap {
			targetSCDB.SetStorageValue(addr, []byte(kStr), val)
		}
	}
}

// InjectTargetedAcceptedWrites injects only the specific accounts and storage slots into the target DBs
func (v *ValidationStateCache) InjectTargetedAcceptedWrites(targetAccountDB types.AccountStateDB, targetSCDB types.SmartContractDB, readAccounts map[common.Address]types.AccountState, readStorage map[common.Address][]string) {
	for addr := range readAccounts {
		if acc, ok := v.acceptedAccounts[addr]; ok {
			targetAccountDB.InjectLoadedAccount(acc.Copy())
		}
	}
	for addr, keys := range readStorage {
		if keysMap, ok := v.acceptedStorage[addr]; ok {
			for _, kStr := range keys {
				if val, exists := keysMap[kStr]; exists {
					targetSCDB.SetStorageValue(addr, []byte(kStr), val)
				}
			}
		}
	}
}


// -----------------------------------------------------------------------------
// Write Methods (Mutate the overlay directly during fallback re-execution)
// -----------------------------------------------------------------------------

func (v *ValidationStateCache) getOrCreateWriteState(address common.Address) (types.AccountState, error) {
	if state, ok := v.acceptedAccounts[address]; ok {
		return state, nil
	}
	state, err := v.parentAccountDB.AccountState(address)
	if err != nil {
		return nil, err
	}
	if state != nil {
		cloned := state.Copy()
		v.acceptedAccounts[address] = cloned
		return cloned, nil
	}
	return nil, nil
}

func (v *ValidationStateCache) SetState(as types.AccountState) {
	v.acceptedAccounts[as.Address()] = as
}

func (v *ValidationStateCache) SubTotalBalance(address common.Address, amount *big.Int) error {
	state, err := v.getOrCreateWriteState(address)
	if err != nil { return err }
	if state != nil {
		return state.SubTotalBalance(amount)
	}
	return nil
}

func (v *ValidationStateCache) AddPendingBalance(address common.Address, amount *big.Int) error {
	state, err := v.getOrCreateWriteState(address)
	if err != nil { return err }
	if state != nil {
		state.AddPendingBalance(amount)
	}
	return nil
}

func (v *ValidationStateCache) SubPendingBalance(address common.Address, amount *big.Int) error {
	state, err := v.getOrCreateWriteState(address)
	if err != nil { return err }
	if state != nil {
		state.SubPendingBalance(amount)
	}
	return nil
}

func (v *ValidationStateCache) AddBalance(address common.Address, amount *big.Int) error {
	state, err := v.getOrCreateWriteState(address)
	if err != nil { return err }
	if state != nil {
		state.AddBalance(amount)
	}
	return nil
}

func (v *ValidationStateCache) SubBalance(address common.Address, amount *big.Int) error {
	state, err := v.getOrCreateWriteState(address)
	if err != nil { return err }
	if state != nil {
		return state.SubBalance(amount)
	}
	return nil
}

func (v *ValidationStateCache) SetLastHash(address common.Address, h common.Hash) error {
	state, err := v.getOrCreateWriteState(address)
	if err != nil { return err }
	if state != nil {
		state.SetLastHash(h)
	}
	return nil
}

func (v *ValidationStateCache) SetNewDeviceKey(address common.Address, h common.Hash) error {
	state, err := v.getOrCreateWriteState(address)
	if err != nil { return err }
	if state != nil {
		state.SetNewDeviceKey(h)
	}
	return nil
}

// Implement remaining unused types.AccountStateDB methods as no-ops or panics
func (v *ValidationStateCache) IntermediateRoot(isLockProcess ...bool) (common.Hash, error) { return common.Hash{}, nil }
func (v *ValidationStateCache) Commit() (common.Hash, error) { return common.Hash{}, nil }
func (v *ValidationStateCache) Discard() error { return nil }
func (v *ValidationStateCache) SetCreatorPublicKey(address common.Address, creatorPublicKey p_common.PublicKey) error { return nil }
func (v *ValidationStateCache) SetCodeHash(address common.Address, codeHash common.Hash) error { return nil }
func (v *ValidationStateCache) SetStorageRoot(address common.Address, storageRoot common.Hash) error { return nil }
func (v *ValidationStateCache) SetStorageAddress(address common.Address, storageAddress common.Address) error { return nil }
func (v *ValidationStateCache) AddLogHash(address common.Address, logsHash common.Hash) error { return nil }
func (v *ValidationStateCache) CopyFrom(as types.AccountStateDB) error { return nil }

// Dummy SmartContractDB methods
func (v *ValidationStateCache) SetAccountStateDB(asdb types.AccountStateDB) {}
func (v *ValidationStateCache) SetBlockNumber(blockNumber uint64) {}
func (v *ValidationStateCache) SetCode(address common.Address, codeHash common.Hash, code []byte) {}
func (v *ValidationStateCache) SetStorageValue(address common.Address, key []byte, value []byte) error { return nil }
func (v *ValidationStateCache) AddEventLogs(eventLogs []types.EventLog) {}
func (v *ValidationStateCache) StorageRoot(address common.Address) common.Hash { return common.Hash{} }
func (v *ValidationStateCache) NewTrieStorage(address common.Address) common.Hash { return common.Hash{} }
func (v *ValidationStateCache) DeleteAddress(address common.Address) {}
func (v *ValidationStateCache) GetSmartContractUpdateDatas() map[common.Address]types.SmartContractUpdateData { return nil }
func (v *ValidationStateCache) ClearSmartContractUpdateDatas() {}
