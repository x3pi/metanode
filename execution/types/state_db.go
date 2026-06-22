package types

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

type AccountStateDB interface {
	AccountState(common.Address) (AccountState, error)

	PlusOneNonce(common.Address) error
	SetNonce(common.Address, uint64) error
	SetAccountType(common.Address, pb.ACCOUNT_TYPE) error

	SubPendingBalance(common.Address, *big.Int) error
	AddPendingBalance(common.Address, *big.Int) error

	AddBalance(common.Address, *big.Int) error
	SubBalance(common.Address, *big.Int) error

	SubTotalBalance(common.Address, *big.Int) error

	SetLastHash(common.Address, common.Hash) error
	SetNewDeviceKey(common.Address, common.Hash) error

	SetState(AccountState)
	InjectLoadedAccount(AccountState)
	PublicSetDirtyAccountState(AccountState)
	DirtyAccountCount() int

	IntermediateRoot(isLockProcess ...bool) (common.Hash, error)
	Commit() (common.Hash, error)
	Discard() error
	// Storage() storage.Storage

	// smart contract state
	SetCreatorPublicKey(address common.Address, creatorPublicKey p_common.PublicKey) error
	SetCodeHash(address common.Address, codeHash common.Hash) error
	SetStorageRoot(address common.Address, storageRoot common.Hash) error
	SetStorageAddress(address common.Address, storageAddress common.Address) error
	AddLogHash(address common.Address, logsHash common.Hash) error

	CopyFrom(as AccountStateDB) error
}

type SmartContractDB interface {
	Code(address common.Address) []byte
	StorageValue(address common.Address, key []byte, customRoot ...*common.Hash) ([]byte, bool)
	SetAccountStateDB(asdb AccountStateDB)
	SetBlockNumber(blockNumber uint64)
	SetCode(
		address common.Address,
		codeHash common.Hash,
		code []byte,
	)
	SetStorageValue(address common.Address, key []byte, value []byte) error
	BatchSetStorageValues(address common.Address, keys, values [][]byte) error
	AddEventLogs(eventLogs []EventLog)
	StorageRoot(
		address common.Address, customRoot ...*common.Hash,
	) common.Hash
	NewTrieStorage(
		address common.Address,
	) common.Hash
	DeleteAddress(address common.Address)

	GetSmartContractUpdateDatas() map[common.Address]SmartContractUpdateData
	ClearSmartContractUpdateDatas()
}
