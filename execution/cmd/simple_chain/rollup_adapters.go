package main

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
)

// accountStateDBAdapter adapts account_state_db to rollup.AccountStateDB.
//
// Holds *blockchain.ChainState rather than a *account_state_db.AccountStateDB captured once at
// construction time -- ChainState.GetAccountStateDB() reads an atomic pointer that gets swapped
// per block / per speculative-execution context, so a captured snapshot goes stale almost
// immediately after app startup. Every write made through a stale snapshot lands in an orphaned
// DB instance that block-root computation never sees, while reads made through that SAME stale
// snapshot stay internally self-consistent -- so the bug hides completely until you check
// whether the write actually reached a real, committed block (found live: CrossNodeHandler's
// EventCreditObserved handling reported success every time, ReceiveWorker never logged an
// error, but the target's balance stayed 0 and the computed block state root never moved off
// genesis no matter how many times the credit was retried).
type accountStateDBAdapter struct {
	chainState *blockchain.ChainState
}

// TotalSupply returns the sum of every account balance (including pending balances) of the cluster, read from the
// committed account state. A cluster's BLS float on the Parent Chain must equal it (see rollup.CheckConservation).
func (a *accountStateDBAdapter) TotalSupply() (*big.Int, error) {
	all, err := a.chainState.GetAccountStateDB().GetAll()
	if err != nil {
		return nil, err
	}
	sum := new(big.Int)
	for _, st := range all {
		if st != nil {
			sum.Add(sum, st.TotalBalance())
		}
	}
	return sum, nil
}

func (a *accountStateDBAdapter) GetBalance(address common.Address) *big.Int {
	state, err := a.chainState.GetAccountStateDB().AccountState(address)
	if err != nil || state == nil {
		return big.NewInt(0)
	}
	return state.Balance()
}

func (a *accountStateDBAdapter) AddBalance(address common.Address, amount *big.Int) error {
	return a.chainState.GetAccountStateDB().AddBalance(address, amount)
}

func (a *accountStateDBAdapter) SubBalance(address common.Address, amount *big.Int) {
	_ = a.chainState.GetAccountStateDB().SubBalance(address, amount)
}

func (a *accountStateDBAdapter) GetNonce(address common.Address) uint64 {
	state, err := a.chainState.GetAccountStateDB().AccountState(address)
	if err != nil || state == nil {
		return 0
	}
	return state.Nonce()
}

func (a *accountStateDBAdapter) SetNonce(address common.Address, nonce uint64) {
	_ = a.chainState.GetAccountStateDB().SetNonce(address, nonce)
}

// smartContractDBAdapter adapts smart_contract_db to rollup.SmartContractDB.
// Same staleness hazard as accountStateDBAdapter above -- see its doc comment.
type smartContractDBAdapter struct {
	chainState *blockchain.ChainState
}

func (s *smartContractDBAdapter) StorageValue(address common.Address, key common.Hash) ([]byte, bool) {
	val, found := s.chainState.GetSmartContractDB().StorageValue(address, key[:])
	if found && len(val) == 32 {
		emptyHash := common.Hash{}
		isEmpty := true
		for i := 0; i < 32; i++ {
			if val[i] != emptyHash[i] {
				isEmpty = false
				break
			}
		}
		if isEmpty {
			return nil, false
		}
	}
	return val, found
}

func (s *smartContractDBAdapter) SetStorageValue(address common.Address, key common.Hash, value []byte) {
	_ = s.chainState.GetSmartContractDB().SetStorageValue(address, key[:], value)
}
