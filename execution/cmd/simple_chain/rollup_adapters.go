package main

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract_db"
)

// accountStateDBAdapter adapts account_state_db to rollup.AccountStateDB
type accountStateDBAdapter struct {
	db *account_state_db.AccountStateDB
}

func (a *accountStateDBAdapter) GetBalance(address common.Address) *big.Int {
	state, err := a.db.AccountState(address)
	if err != nil || state == nil {
		return big.NewInt(0)
	}
	return state.Balance()
}

func (a *accountStateDBAdapter) AddBalance(address common.Address, amount *big.Int) error {
	return a.db.AddBalance(address, amount)
}

func (a *accountStateDBAdapter) SubBalance(address common.Address, amount *big.Int) {
	_ = a.db.SubBalance(address, amount)
}

func (a *accountStateDBAdapter) GetNonce(address common.Address) uint64 {
	state, err := a.db.AccountState(address)
	if err != nil || state == nil {
		return 0
	}
	return state.Nonce()
}

func (a *accountStateDBAdapter) SetNonce(address common.Address, nonce uint64) {
	_ = a.db.SetNonce(address, nonce)
}

// smartContractDBAdapter adapts smart_contract_db to rollup.SmartContractDB
type smartContractDBAdapter struct {
	db *smart_contract_db.SmartContractDB
}

func (s *smartContractDBAdapter) StorageValue(address common.Address, key common.Hash) ([]byte, bool) {
	val, found := s.db.StorageValue(address, key[:])
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
	_ = s.db.SetStorageValue(address, key[:], value)
}
