package smart_contract_db

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

// probingDB runs hook inside BatchPut, before the write reaches the store, and can make the write fail.
type probingDB struct {
	*testDB
	hook func()
	err  error
}

func (p *probingDB) BatchPut(pairs [][2][]byte) error {
	if p.hook != nil {
		p.hook()
	}
	if p.err != nil {
		return p.err
	}
	return p.testDB.BatchPut(pairs)
}

// Code written in a block lives in pendingCode until Commit() writes it to the code store. Between "no longer pending"
// and "readable from the store" there must be no moment in which a concurrent reader (the next block's speculative
// execution, an RPC call) finds neither: the EVM would see an account whose code hash is set but whose code is empty.
func TestCommit_CodeIsReadableThroughoutTheWrite(t *testing.T) {
	store := &probingDB{testDB: newTestDB()}
	db := NewSmartContractDB(store, newTestDB(), nil, nil)
	addr := common.HexToAddress("0x1000000000000000000000000000000000000001")
	codeHash := common.HexToHash("0xc0de")
	code := []byte{0x60, 0x80, 0x60, 0x40}
	db.SetCode(addr, codeHash, code)

	var during []byte
	store.hook = func() { during = db.GetCodeByCodeHash(addr, codeHash) }
	_, err := db.Commit()
	require.NoError(t, err)
	require.Equal(t, code, during, "while the code store is being written the code must still be readable from pendingCode")
	require.Equal(t, code, db.GetCodeByCodeHash(addr, codeHash), "after the commit the code is read from the store")
}

// A failed write must not lose the code: Commit() reports the error and the code stays pending, so a retry can write it
// and readers keep seeing it in the meantime.
func TestCommit_FailedCodeWriteKeepsTheCodePending(t *testing.T) {
	store := &probingDB{testDB: newTestDB(), err: errors.New("disk full")}
	db := NewSmartContractDB(store, newTestDB(), nil, nil)
	addr := common.HexToAddress("0x1000000000000000000000000000000000000002")
	codeHash := common.HexToHash("0xc0df")
	code := []byte{0x60, 0x01}
	db.SetCode(addr, codeHash, code)

	_, err := db.Commit()
	require.Error(t, err)
	require.Equal(t, code, db.GetCodeByCodeHash(addr, codeHash), "the code must not be dropped when the store write failed")

	store.err = nil
	_, err = db.Commit()
	require.NoError(t, err)
	stored, getErr := store.Get(codeHash.Bytes())
	require.NoError(t, getErr)
	require.Equal(t, code, stored, "the retry writes the code that was kept pending")
}
