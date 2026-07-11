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
// AccountReadRecord records a read dependency for a transaction
type AccountReadRecord struct {
	Address common.Address
	Version Version
	WriteID WriteID
}

type MVCCAccountStateDB struct {
	baseDB     types.AccountStateDB
	accountMap *MVCCAccountMap
	txIndex    Version

	// ReadSet tracks the version and WriteID of the state read by this transaction
	ReadSet []AccountReadRecord
	// WriteSet tracks which addresses this transaction has modified
	WriteSet []common.Address

	// localState caches reads/writes during execution
	localState []types.AccountState
}

func NewMVCCAccountStateDB(baseDB types.AccountStateDB, accountMap *MVCCAccountMap, txIndex Version) *MVCCAccountStateDB {
	return &MVCCAccountStateDB{
		baseDB:     baseDB,
		accountMap: accountMap,
		txIndex:    txIndex,
		ReadSet:    make([]AccountReadRecord, 0, 8),
		WriteSet:   make([]common.Address, 0, 8),
		localState: make([]types.AccountState, 0, 8),
	}
}

func (db *MVCCAccountStateDB) markWrite(addr common.Address) {
	for _, w := range db.WriteSet {
		if w == addr {
			return
		}
	}
	db.WriteSet = append(db.WriteSet, addr)
}

// AccountState reads the state. First checks localState, then MVCC, then BaseDB.
func (db *MVCCAccountStateDB) AccountState(addr common.Address) (types.AccountState, error) {
	for _, s := range db.localState {
		if s.Address() == addr {
			return s, nil
		}
	}

	state, version, writeID := db.accountMap.Read(addr, db.txIndex)
	if state == nil {
		s, _ := db.baseDB.AccountState(addr)
		if s != nil {
			state = s.Copy()
		}
	} else {
		state = state.Copy()
	}

	if state != nil {
		db.localState = append(db.localState, state)
	}
	
	isWritten := false
	for _, w := range db.WriteSet {
		if w == addr {
			isWritten = true
			break
		}
	}
	
	if !isWritten {
		db.ReadSet = append(db.ReadSet, AccountReadRecord{Address: addr, Version: version, WriteID: writeID})
		db.accountMap.AddReader(addr, db.txIndex)
	}
	return state, nil
}

func safeCopy(state types.AccountState) types.AccountState {
	if state == nil {
		return nil
	}
	return state.Copy()
}

func (db *MVCCAccountStateDB) AddBalance(addr common.Address, amount *big.Int) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.AddBalance(amount)
	}
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
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
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
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
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
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
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
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
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
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
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
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
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
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
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
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
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
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
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
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
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
	return nil
}

func (db *MVCCAccountStateDB) CopyFrom(as types.AccountStateDB) error {
	return nil
}

func (db *MVCCAccountStateDB) SetCodeHash(addr common.Address, h common.Hash) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.SetCodeHash(h)
	}
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
	return nil
}

func (db *MVCCAccountStateDB) SetCreatorPublicKey(addr common.Address, pk p_common.PublicKey) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.SetCreatorPublicKey(pk)
	}
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
	return nil
}

func (db *MVCCAccountStateDB) SetStorageRoot(addr common.Address, h common.Hash) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.SetStorageRoot(h)
	}
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
	return nil
}

func (db *MVCCAccountStateDB) SetStorageAddress(addr common.Address, h common.Address) error {
	state, err := db.AccountState(addr)
	if err != nil {
		return err
	}
	if state != nil {
		state.SetStorageAddress(h)
	}
	db.markWrite(addr)
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
	return nil
}

func (db *MVCCAccountStateDB) Commit() (common.Hash, error) { return common.Hash{}, nil }
func (db *MVCCAccountStateDB) Discard() error               { return nil }
func (db *MVCCAccountStateDB) SetState(s types.AccountState) {
	if s != nil {
		addr := s.Address()
		db.markWrite(addr)
		
		found := false
		for i, ls := range db.localState {
			if ls.Address() == addr {
				db.localState[i] = s
				found = true
				break
			}
		}
		if !found {
			db.localState = append(db.localState, s)
		}
		
		db.accountMap.Write(addr, db.txIndex, safeCopy(s))
	}
}
func (db *MVCCAccountStateDB) InjectLoadedAccount(s types.AccountState) {}
func (db *MVCCAccountStateDB) PublicSetDirtyAccountState(s types.AccountState) {}
func (db *MVCCAccountStateDB) DirtyAccountCount() int { return 0 }
func (db *MVCCAccountStateDB) IntermediateRoot(isLockProcess ...bool) (common.Hash, error) { return common.Hash{}, nil }
