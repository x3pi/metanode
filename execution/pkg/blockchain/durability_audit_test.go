package blockchain

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	state_db "github.com/meta-node-blockchain/meta-node/pkg/state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

type auditFailedDurableStorage struct {
	storage.Storage
	heightAtSync uint64
}

func (s *auditFailedDurableStorage) SyncDurable() error {
	s.heightAtSync = storage.GetLastBlockNumber()
	return errors.New("injected durability barrier failure")
}

func TestAuditHeightMustNotAdvanceBeforeDurability(t *testing.T) {
	cs, _ := newCommitTestState(t)
	cs.SetStakeStateDB(state_db.NewStakeStateDB(cs.GetAccountStateDB().Trie(), storage.NewMemoryDb()))
	prevBC := blockChainInstance
	prevHeight := storage.GetLastBlockNumber()
	defer func() { blockChainInstance = prevBC; storage.ResetAllBlockCounters(prevHeight) }()
	blockChainInstance = newTestBlockChain()
	height := prevHeight + 1
	db := &auditFailedDurableStorage{Storage: storage.NewMemoryDb()}
	cs.blockDatabase = block.NewBlockDatabase(db)
	hdr := block.NewBlockHeader(common.Hash{}, height, cs.GetAccountStateDB().GetOriginRootHash(), cs.GetStakeStateDB().GetOriginRootHash(), common.Hash{}, common.Address{}, 1, common.Hash{}, 0)
	_, err := cs.CommitBlockState(block.NewBlock(hdr, nil, nil), WithPersistToDB())
	if err == nil {
		t.Fatal("injected durability failure did not reach caller")
	}
	if db.heightAtSync >= height {
		t.Errorf("GetLastBlockNumber already exposed block %d before its durability barrier completed", db.heightAtSync)
	}
	if got := storage.GetLastBlockNumber(); got >= height {
		t.Errorf("GetLastBlockNumber still exposes failed commit %d after SyncDurable error", got)
	}
}
