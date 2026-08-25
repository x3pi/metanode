package mvcc

import (
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

// MVCCAccountStateDB wraps the global AccountStateDB and the block's MVCC maps.
// It intercepts all reads and writes for a specific transaction (TxIndex).
// Any read is recorded in the ReadSet. Any write is recorded in the WriteSet and MVCC maps.
var ErrEstimateHit = errors.New("estimate hit")

type MVCCAccountStateDB struct {
	baseDB          types.AccountStateDB
	accountMap      *MVCCAccountMap
	txIndex         Version
	localState      map[common.Address]types.AccountState
	ReadSet         map[common.Address]ReadVersion
	WriteSet        map[common.Address]bool
	BlockingVersion Version
}

func NewMVCCAccountStateDB(baseDB types.AccountStateDB, accountMap *MVCCAccountMap, txIndex Version) *MVCCAccountStateDB {
	return &MVCCAccountStateDB{
		baseDB:          baseDB,
		accountMap:      accountMap,
		txIndex:         txIndex,
		localState:      make(map[common.Address]types.AccountState),
		ReadSet:         make(map[common.Address]ReadVersion),
		WriteSet:        make(map[common.Address]bool),
		BlockingVersion: BaseVersion,
	}
}

// AccountState reads the state. First checks localState, then MVCC, then BaseDB.
func (db *MVCCAccountStateDB) AccountState(addr common.Address) (types.AccountState, error) {
	if state, ok := db.localState[addr]; ok {
		return state, nil
	}

	state, version, writeID, blockingVer := db.accountMap.Read(addr, db.txIndex)
	if blockingVer != BaseVersion {
		db.BlockingVersion = blockingVer
		return nil, ErrEstimateHit
	}
	if state == nil {
		s, _ := db.baseDB.AccountState(addr)
		if s != nil {
			state = s.Copy()
		}
	} else {
		state = state.Copy()
	}

	db.localState[addr] = state
	if _, ok := db.WriteSet[addr]; !ok {
		db.ReadSet[addr] = ReadVersion{Version: version, WriteID: writeID}
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
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
	db.WriteSet[addr] = true
	db.accountMap.Write(addr, db.txIndex, safeCopy(state))
	return nil
}

func (db *MVCCAccountStateDB) Commit() (common.Hash, error) { return common.Hash{}, nil }
func (db *MVCCAccountStateDB) Discard() error               { return nil }
func (db *MVCCAccountStateDB) SetState(s types.AccountState) {
	if s != nil {
		addr := s.Address()
		db.WriteSet[addr] = true
		db.localState[addr] = s
		db.accountMap.Write(addr, db.txIndex, safeCopy(s))
	}
}
func (db *MVCCAccountStateDB) InjectLoadedAccount(s types.AccountState)        {}
func (db *MVCCAccountStateDB) PublicSetDirtyAccountState(s types.AccountState) {}
func (db *MVCCAccountStateDB) DirtyAccountCount() int                          { return 0 }
func (db *MVCCAccountStateDB) IntermediateRoot(isLockProcess ...bool) (common.Hash, error) {
	return common.Hash{}, nil
}
