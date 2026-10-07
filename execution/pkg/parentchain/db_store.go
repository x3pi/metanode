package parentchain

import (
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
	"github.com/syndtr/goleveldb/leveldb"
)

type DBStore struct {
	db         *leveldb.DB
	nomtHandle *nomt_ffi.Handle
	committer  *TreeBlockCommitter
	mu         sync.RWMutex
}

func NewDBStore(path string) (*DBStore, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base db directory: %w", err)
	}

	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		return nil, err
	}

	nomtPath := filepath.Join(path, "nomt")
	if err := os.MkdirAll(nomtPath, 0755); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to create NOMT directory: %w", err)
	}

	// Open NOMT with: 4 workers, 64MB page cache, 64MB leaf cache, 64000 hashtable buckets, preallocate
	handle, err := nomt_ffi.Open(nomtPath, 4, 64, 64, 64000, true)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	committer := NewTreeBlockCommitter(db, handle)

	// Startup verification (following simple_chain app_blockchain.go pattern)
	prog, err := committer.LastApplied()
	if err == nil && prog.LastBlock > 0 {
		if actualRoot, rErr := handle.Root(); rErr == nil {
			actualRootHash := common.BytesToHash(actualRoot[:])
			if actualRootHash != prog.LastStateRoot {
				log.Printf("⚠️ [PARENT-CHAIN-STARTUP] NOMT root mismatch: nomt=%s, leveldb=%s (lastBlock=%d)",
					actualRootHash.Hex(), prog.LastStateRoot.Hex(), prog.LastBlock)
			} else {
				log.Printf("🔍 [PARENT-CHAIN-STARTUP] NOMT state root verified with block #%d: %s",
					prog.LastBlock, actualRootHash.Hex())
			}
		}
	}

	return &DBStore{
		db:         db,
		nomtHandle: handle,
		committer:  committer,
	}, nil
}

func (s *DBStore) NomtHandle() *nomt_ffi.Handle {
	return s.nomtHandle
}

func (s *DBStore) NomtRoot() (common.Hash, error) {
	if s.nomtHandle == nil {
		return common.Hash{}, fmt.Errorf("nomt handle not initialized")
	}
	r, err := s.nomtHandle.Root()
	if err != nil {
		return common.Hash{}, err
	}
	return common.BytesToHash(r[:]), nil
}

func (s *DBStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var errs []error
	if s.nomtHandle != nil {
		s.nomtHandle.Close()
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

func (s *DBStore) Committer() BlockCommitter {
	return s.committer
}

// ─── IMPLEMENT BlockCommitter ────────────────────────────────────────────────

func (s *DBStore) LastApplied() (BlockProgress, error) {
	return s.committer.LastApplied()
}

func (s *DBStore) GetBlockRecord(number uint64) (BlockRecord, bool, error) {
	return s.committer.GetBlockRecord(number)
}

func (s *DBStore) GetBlockRecordByHash(hash common.Hash) (BlockRecord, bool, error) {
	return s.committer.GetBlockRecordByHash(hash)
}

func (s *DBStore) GetBlockRecords(from, to uint64, limit int) ([]BlockRecord, error) {
	return s.committer.GetBlockRecords(from, to, limit)
}

func (s *DBStore) GetTxLocation(txHash common.Hash) (uint64, uint32, bool, error) {
	return s.committer.GetTxLocation(txHash)
}

func (s *DBStore) GetReceipt(txHash common.Hash) (*Receipt, bool, error) {
	return s.committer.GetReceipt(txHash)
}

// SetGenesisInit installs the genesis state initializer run inside block 1.
func (s *DBStore) SetGenesisInit(f func(Store) error) {
	s.committer.SetGenesisInit(f)
}

func (s *DBStore) ApplyBlock(in BlockInput, exec TxExecutor) (BlockResult, error) {
	return s.committer.ApplyBlock(in, exec)
}

func (s *DBStore) GenerateProof(key [32]byte) ([]byte, error) {
	return s.committer.GenerateProof(key)
}

func (s *DBStore) Store() Store {
	return s.committer.Store()
}

// ─── IMPLEMENT Store INTERFACE VIA committer.Store() ─────────────────────────

func (s *DBStore) GetChainRegistry(key common.Hash) (ChainRegistryEntry, bool, error) {
	return s.committer.Store().GetChainRegistry(key)
}

func (s *DBStore) SetChainRegistry(key common.Hash, entry ChainRegistryEntry) error {
	return s.committer.Store().SetChainRegistry(key, entry)
}

func (s *DBStore) GetAllChainRegistryKeys() ([]common.Hash, error) {
	return s.committer.Store().GetAllChainRegistryKeys()
}

func (s *DBStore) GetFloat(key common.Hash) (*big.Int, error) {
	return s.committer.Store().GetFloat(key)
}

func (s *DBStore) SetFloat(key common.Hash, balance *big.Int) error {
	return s.committer.Store().SetFloat(key, balance)
}

func (s *DBStore) GetClaimed(messageID common.Hash) (FloatOutcome, error) {
	return s.committer.Store().GetClaimed(messageID)
}

func (s *DBStore) SetClaimed(messageID common.Hash, outcome FloatOutcome) error {
	return s.committer.Store().SetClaimed(messageID, outcome)
}

func (s *DBStore) GetTransferRecord(messageID common.Hash) (FloatTransferRecord, bool, error) {
	return s.committer.Store().GetTransferRecord(messageID)
}

func (s *DBStore) SetTransferRecord(messageID common.Hash, rec FloatTransferRecord) error {
	return s.committer.Store().SetTransferRecord(messageID, rec)
}

func (s *DBStore) GetFloatSeq(key common.Hash) (uint64, error) {
	return s.committer.Store().GetFloatSeq(key)
}

func (s *DBStore) SetFloatSeq(key common.Hash, seq uint64) error {
	return s.committer.Store().SetFloatSeq(key, seq)
}

func (s *DBStore) GetVelocity(key common.Hash) (FloatVelocityState, error) {
	return s.committer.Store().GetVelocity(key)
}

func (s *DBStore) SetVelocity(key common.Hash, st FloatVelocityState) error {
	return s.committer.Store().SetVelocity(key, st)
}

func (s *DBStore) GetAccountRegistry(userAddress common.Address) (cm.PublicKey, bool, error) {
	return s.committer.Store().GetAccountRegistry(userAddress)
}

func (s *DBStore) SetAccountRegistry(userAddress common.Address, floatIdentityKey cm.PublicKey) error {
	return s.committer.Store().SetAccountRegistry(userAddress, floatIdentityKey)
}

func (s *DBStore) GetStateRoot(clusterKeyHash common.Hash, epoch uint64) (common.Hash, bool, error) {
	return s.committer.Store().GetStateRoot(clusterKeyHash, epoch)
}

func (s *DBStore) SetStateRoot(clusterKeyHash common.Hash, epoch uint64, root common.Hash) error {
	return s.committer.Store().SetStateRoot(clusterKeyHash, epoch, root)
}

func (s *DBStore) AppendInboundTransfer(destKeyHash common.Hash, event *TransferEvent) error {
	return s.committer.Store().AppendInboundTransfer(destKeyHash, event)
}

func (s *DBStore) GetInboundTransfers(destKeyHash common.Hash, cursor uint64) ([]*TransferEvent, uint64, error) {
	return s.committer.Store().GetInboundTransfers(destKeyHash, cursor)
}

func (s *DBStore) AppendAccountRegistration(clusterKeyHash common.Hash, event *AccountRegisteredEvent) error {
	return s.committer.Store().AppendAccountRegistration(clusterKeyHash, event)
}

func (s *DBStore) GetAccountRegistrations(clusterKeyHash common.Hash, cursor uint64) ([]*AccountRegisteredEvent, uint64, error) {
	return s.committer.Store().GetAccountRegistrations(clusterKeyHash, cursor)
}

func (s *DBStore) GetNonce(sender common.Address) (uint64, error) {
	return s.committer.Store().GetNonce(sender)
}

func (s *DBStore) SetNonce(sender common.Address, nonce uint64) error {
	return s.committer.Store().SetNonce(sender, nonce)
}

