package mvcc

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

// MVCCAccountStateDB wraps the global AccountStateDB and the block's MVCC maps.
// It intercepts all reads and writes for a specific transaction (TxIndex).
// Any read is recorded in the ReadSet. Any write is recorded in the WriteSet and MVCC maps.
type MVCCAccountStateDB struct {
	baseDB       types.AccountStateDB
	accountMap   *MVCCAccountMap
	txIndex      Version

	// ReadSet tracks the addresses read by this transaction and their versions.
	// Map of address -> read version
	ReadSet map[common.Address]Version
	
	// WriteSet tracks the addresses modified by this transaction.
	WriteSet map[common.Address]bool
}

func NewMVCCAccountStateDB(baseDB types.AccountStateDB, accountMap *MVCCAccountMap, txIndex uint32) *MVCCAccountStateDB {
	return &MVCCAccountStateDB{
		baseDB:     baseDB,
		accountMap: accountMap,
		txIndex:    Version(txIndex),
		ReadSet:    make(map[common.Address]Version),
		WriteSet:   make(map[common.Address]bool),
	}
}

// AccountState reads the state. First checks MVCC, then BaseDB.
func (db *MVCCAccountStateDB) AccountState(addr common.Address) (types.AccountState, error) {
	// Find the highest version < current txIndex
	state, version := db.accountMap.Read(addr, db.txIndex)
	
	// If not found in MVCC, read from baseDB
	if state == nil {
		s, err := db.baseDB.AccountState(addr)
		if err != nil {
			return nil, err
		}
		if s != nil {
			state = s.Copy()
		}
		version = BaseVersion
	} else {
		state = state.Copy()
	}

	// Record read set
	db.ReadSet[addr] = version

	return state, nil
}

func (db *MVCCAccountStateDB) AddBalance(addr common.Address, amount *big.Int) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.AddBalance(amount)
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) SubBalance(addr common.Address, amount *big.Int) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		if err := state.SubBalance(amount); err != nil {
			return err
		}
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) SetNonce(addr common.Address, nonce uint64) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.SetNonce(nonce)
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) PlusOneNonce(addr common.Address) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.PlusOneNonce()
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) SubPendingBalance(addr common.Address, amount *big.Int) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		if err := state.SubPendingBalance(amount); err != nil {
			return err
		}
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) AddPendingBalance(addr common.Address, amount *big.Int) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.AddPendingBalance(amount)
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) SubTotalBalance(addr common.Address, amount *big.Int) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		if err := state.SubTotalBalance(amount); err != nil {
			return err
		}
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) SetLastHash(addr common.Address, h common.Hash) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.SetLastHash(h)
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) SetNewDeviceKey(addr common.Address, h common.Hash) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.SetNewDeviceKey(h)
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) SetAccountType(addr common.Address, t pb.ACCOUNT_TYPE) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.SetAccountType(t)
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) AddLogHash(addr common.Address, logsHash common.Hash) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.AddLogHash(logsHash)
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) CopyFrom(as types.AccountStateDB) error {
	return nil
}

func (db *MVCCAccountStateDB) SetCodeHash(addr common.Address, hash common.Hash) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.SetCodeHash(hash)
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) SetCreatorPublicKey(addr common.Address, pubKey p_common.PublicKey) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.SetCreatorPublicKey(pubKey)
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) SetStorageRoot(addr common.Address, storageRoot common.Hash) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.SetStorageRoot(storageRoot)
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) SetStorageAddress(addr common.Address, storageAddress common.Address) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.SetStorageAddress(storageAddress)
	}
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, state)
	return nil
}

func (db *MVCCAccountStateDB) Commit() (common.Hash, error) { return common.Hash{}, nil }
func (db *MVCCAccountStateDB) Discard() error               { return nil }
func (db *MVCCAccountStateDB) SetState(s types.AccountState) {
	if s != nil {
		addr := s.Address()
		db.WriteSet[addr] = true
		db.accountMap.Write(addr, db.txIndex, s)
	}
}
func (db *MVCCAccountStateDB) InjectLoadedAccount(s types.AccountState) {}
func (db *MVCCAccountStateDB) PublicSetDirtyAccountState(s types.AccountState) {}
func (db *MVCCAccountStateDB) DirtyAccountCount() int { return 0 }
func (db *MVCCAccountStateDB) IntermediateRoot(isLockProcess ...bool) (common.Hash, error) { return common.Hash{}, nil }
