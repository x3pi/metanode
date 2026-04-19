package account_state_db

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	// Assume these paths are correct for your project structure
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

// --- Modification Methods ---
// These methods modify an account's state. They first retrieve the state
// (creating it if necessary), modify it, and then mark it as dirty using setDirtyAccountState.

func (db *AccountStateDB) SubPendingBalance(address common.Address, amount *big.Int) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("SubPendingBalance db.lockedFlag is already locked")
	}

	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("SubPendingBalance: %w", err)
	}
	if as == nil {
		return errors.New("SubPendingBalance: account state is nil")
	} // Safety check
	err = as.SubPendingBalance(amount)
	if err != nil {
		return fmt.Errorf("SubPendingBalance: %w", err)
	}
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) RefreshPendingBalance(address common.Address) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("RefreshPendingBalance db.lockedFlag is already locked")
	}

	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("RefreshPendingBalance: %w", err)
	}
	if as == nil {
		return errors.New("RefreshPendingBalance: account state is nil")
	} // Safety check

	pendingBalance := as.PendingBalance()
	// Check if subtraction is necessary/possible
	if pendingBalance != nil && pendingBalance.Sign() > 0 {
		err = as.SubPendingBalance(pendingBalance)
		if err != nil {
			// Log or handle potential underflow if SubPendingBalance can fail
			logger.Warn("RefreshPendingBalance: Error subtracting pending balance", "address", address.Hex(), "error", err)
			// Decide whether to continue or return error based on AccountState logic
			return fmt.Errorf("RefreshPendingBalance: sub pending failed: %w", err)
		}
		as.AddBalance(pendingBalance) // Add the amount back to the main balance
	}
	// No else needed: if pending is zero or nil, nothing to subtract or add

	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) AddPendingBalance(address common.Address, amount *big.Int) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("AddPendingBalance db.lockedFlag is already locked")
	}

	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("AddPendingBalance: %w", err)
	}
	if as == nil {
		return errors.New("AddPendingBalance: account state is nil")
	} // Safety check
	as.AddPendingBalance(amount) // Assuming this cannot fail
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) PlusOneNonce(address common.Address) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("PlusOneNonce db.lockedFlag is already locked")
	}

	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("PlusOneNonce: %w", err)
	}
	if as == nil {
		return errors.New("PlusOneNonce: account state is nil")
	} // Safety check
	oldNonce := as.Nonce()
	as.PlusOneNonce()
	logger.Debug("[NONCE-TRACE] PlusOneNonce: addr=%s, old=%d → new=%d", address.Hex(), oldNonce, as.Nonce())
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) SetAccountType(address common.Address, accountTypeNew pb.ACCOUNT_TYPE) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("SetAccountType db.lockedFlag is already locked")
	}

	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("SetAccountType: %w", err)
	}
	if as == nil {
		return errors.New("SetAccountType: account state is nil")
	} // Safety check
	err = as.SetAccountType(accountTypeNew) // Assuming this can fail (e.g., invalid type transition)
	if err != nil {
		return fmt.Errorf("SetAccountType: %w", err)
	}
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) SetPublicKeyBls(address common.Address, publicKeyBls []byte) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("SetPublicKeyBls db.lockedFlag is already locked")
	}
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("SetPublicKeyBls: %w", err)
	}
	if as == nil {
		return errors.New("SetPublicKeyBls: account state is nil")
	} // Safety check
	as.SetPublicKeyBls(publicKeyBls)
	db.setDirtyAccountState(as)
	return nil
}
func (db *AccountStateDB) GetPublicKeyBls(address common.Address) ([]byte, error) {
	if db.lockedFlag.Load() {
		return nil, errors.New("GetPublicKeyBls db.lockedFlag is already locked")
	}
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return nil, fmt.Errorf("GetPublicKeyBls: %w", err)
	}
	if as == nil {
		return nil, errors.New("GetPublicKeyBls: account state is nil")
	} // Safety check
	return as.PublicKeyBls(), nil
}

func (db *AccountStateDB) AddBalance(address common.Address, amount *big.Int) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("AddBalance db.lockedFlag is already locked")
	}

	if amount == nil || amount.Sign() <= 0 {
		// Adding zero or negative is a no-op or potentially an error depending on semantics
		// logger.Debug("AddBalance: Attempted to add zero or negative amount", "address", address.Hex(), "amount", amount)
		return nil // Or return an error if negative amounts are invalid
	}
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("AddBalance: %w", err)
	}
	if as == nil {
		return errors.New("AddBalance: account state is nil")
	} // Safety check
	as.AddBalance(amount) // Assuming this cannot fail
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) SubBalance(address common.Address, amount *big.Int) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("SubBalance db.lockedFlag is already locked")
	}
	if amount == nil || amount.Sign() <= 0 {
		// Subtracting zero or negative is a no-op or potentially an error
		// logger.Debug("SubBalance: Attempted to subtract zero or negative amount", "address", address.Hex(), "amount", amount)
		return nil // Or return an error if negative amounts are invalid
	}
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("SubBalance: %w", err)
	}
	if as == nil {
		return errors.New("SubBalance: account state is nil")
	} // Safety check
	err = as.SubBalance(amount) // This can fail (insufficient funds)
	if err != nil {
		return fmt.Errorf("SubBalance: %w", err)
	}
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) SubTotalBalance(address common.Address, amount *big.Int) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("SubTotalBalance db.lockedFlag is already locked")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil // Or error
	}
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("SubTotalBalance: %w", err)
	}
	if as == nil {
		return errors.New("SubTotalBalance: account state is nil")
	} // Safety check
	err = as.SubTotalBalance(amount) // This can fail
	if err != nil {
		return fmt.Errorf("SubTotalBalance: %w", err)
	}
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) SetNonce(address common.Address, nonce uint64) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("SetNonce db.lockedFlag is already locked")
	}
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("SetNonce: %w", err)
	}
	if as == nil {
		return errors.New("SetNonce: account state is nil")
	} // Safety check
	oldNonce := as.Nonce()
	as.SetNonce(nonce) // Assuming this cannot fail
	if nonce != oldNonce+1 {
		logger.Warn("🚨 [NONCE-TRACE] SetNonce JUMP: addr=%s, old=%d → new=%d (delta=%d, expected delta=1)", address.Hex(), oldNonce, nonce, int64(nonce)-int64(oldNonce))
	} else {
		logger.Debug("[NONCE-TRACE] SetNonce: addr=%s, old=%d → new=%d", address.Hex(), oldNonce, nonce)
	}
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) SetLastHash(address common.Address, hash common.Hash) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("SetLastHash db.lockedFlag is already locked")
	}
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("SetLastHash: %w", err)
	}
	if as == nil {
		return errors.New("SetLastHash: account state is nil")
	} // Safety check
	as.SetLastHash(hash) // Assuming this cannot fail
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) GetLastHash(address common.Address) (common.Hash, error) {

	if db.lockedFlag.Load() {
		return common.Hash{}, errors.New("GetLastHash db.lockedFlag is already locked")
	}
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return common.Hash{}, fmt.Errorf("SetLastHash: %w", err)
	}
	if as == nil {
		return common.Hash{}, errors.New("SetLastHash: account state is nil")
	} // Safety check
	return as.LastHash(), nil
}

func (db *AccountStateDB) SetNewDeviceKey(address common.Address, newDeviceKey common.Hash) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("SetNewDeviceKey db.lockedFlag is already locked")
	}
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("SetNewDeviceKey: %w", err)
	}
	if as == nil {
		return errors.New("SetNewDeviceKey: account state is nil")
	} // Safety check
	as.SetNewDeviceKey(newDeviceKey) // Assuming this cannot fail
	db.setDirtyAccountState(as)
	return nil
}

// SetState explicitly sets an account state, marking it dirty.
// Useful for initializing or overwriting an account state directly.
func (db *AccountStateDB) SetState(as types.AccountState) {
	if as == nil {
		logger.Warn("SetState: Attempted to set nil account state")
		return
	}
	// Use the public setter which correctly uses the internal setDirtyAccountState
	db.setDirtyAccountState(as)
}

// --- Smart Contract State Methods ---

func (db *AccountStateDB) SetCreatorPublicKey(
	address common.Address,
	creatorPublicKey p_common.PublicKey,
) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("SetCreatorPublicKey db.lockedFlag is already locked")
	}
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("SetCreatorPublicKey: %w", err)
	}
	if as == nil {
		return errors.New("SetCreatorPublicKey: account state is nil")
	} // Safety check
	as.SetCreatorPublicKey(creatorPublicKey) // Assuming this cannot fail
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) SetCodeHash(address common.Address, codeHash common.Hash) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("SetCodeHash db.lockedFlag is already locked")
	}

	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("SetCodeHash: %w", err)
	}
	if as == nil {
		return errors.New("SetCodeHash: account state is nil")
	} // Safety check
	as.SetCodeHash(codeHash) // Assuming this cannot fail
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) SetStorageRoot(address common.Address, storageRoot common.Hash) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("SetStorageRoot db.lockedFlag is already locked")
	}
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("SetStorageRoot: %w", err)
	}
	if as == nil {
		return errors.New("SetStorageRoot: account state is nil")
	} // Safety check
	as.SetStorageRoot(storageRoot) // Assuming this cannot fail
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) SetStorageAddress(
	address common.Address,
	storageAddress common.Address,
) error {
	db.accountLocks[address[0]].Lock()
	defer db.accountLocks[address[0]].Unlock()

	if db.lockedFlag.Load() {
		return errors.New("SetStorageAddress db.lockedFlag is already locked")
	}
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("SetStorageAddress: %w", err)
	}
	if as == nil {
		return errors.New("SetStorageAddress: account state is nil")
	} // Safety check
	as.SetStorageAddress(storageAddress) // Assuming this cannot fail
	db.setDirtyAccountState(as)
	return nil
}

func (db *AccountStateDB) AddLogHash(address common.Address, logsHash common.Hash) error {
	as, err := db.getOrCreateAccountState(address)
	if err != nil {
		return fmt.Errorf("AddLogHash: %w", err)
	}
	if as == nil {
		return errors.New("AddLogHash: account state is nil")
	} // Safety check
	as.AddLogHash(logsHash) // Assuming this cannot fail
	db.setDirtyAccountState(as)
	return nil
}
