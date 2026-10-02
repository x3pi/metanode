package parentchain

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"google.golang.org/protobuf/proto"
)

var (
	ErrBlockGap      = errors.New("parentchain: block gap detected (sync required)")
	ErrBlockConflict = errors.New("parentchain: block conflict detected (fork attempt)")
)

type BlockInput struct {
	Number        uint64
	GEI           uint64
	CommitIndex   uint32
	Epoch         uint64
	TimestampMs   uint64
	LeaderAddress common.Address
	CommitDigest  common.Hash
	Txs           [][]byte // Raw transaction bytes (e.g., marshaled pb.Transaction)
	RawBlock      []byte
}

type BlockProgress struct {
	LastBlock       uint64
	LastGEI         uint64
	LastTimestampMs uint64
	LastHash        common.Hash
	LastStateRoot   common.Hash
}

type BlockRecord struct {
	Header    Header
	BlockHash common.Hash
	TxHashes  []common.Hash
	Receipts  []*Receipt
	RawBlock  []byte
}

type BlockResult struct {
	Record   BlockRecord
	TxErrors []error
	Replayed bool
}

type TxExecutor func(store Store, txIndex int, rawTx []byte) (*Receipt, error)

type BlockCommitter interface {
	LastApplied() (BlockProgress, error)
	GetBlockRecord(number uint64) (BlockRecord, bool, error)
	GetBlockRecordByHash(hash common.Hash) (BlockRecord, bool, error)
	GetBlockRecords(from, to uint64, limit int) ([]BlockRecord, error)
	GetTxLocation(txHash common.Hash) (blockNum uint64, txIndex uint32, found bool, err error)
	GetReceipt(txHash common.Hash) (*Receipt, bool, error)
	ApplyBlock(in BlockInput, exec TxExecutor) (BlockResult, error)
	GenerateProof(key [32]byte) ([]byte, error)
	Store() Store
	NomtRoot() (common.Hash, error)
}

// ─── KEYS FOR PERSISTENT STORE ───────────────────────────────────────────────

var (
	keyProgressPrefix = []byte("sys:progress")
	prefixBlockByNum  = []byte("blk:num:")
	prefixBlockByHash = []byte("blk:hash:")
	prefixTxLocation  = []byte("blk:tx:")
)

func blockNumKey(n uint64) []byte {
	return append(prefixBlockByNum, EncodeUint64(n)...)
}

func blockHashKey(h common.Hash) []byte {
	return append(prefixBlockByHash, h.Bytes()...)
}

func txLocKey(h common.Hash) []byte {
	return append(prefixTxLocation, h.Bytes()...)
}

// ─── CANONICAL SERIALIZATION FOR BLOCK RECORD & PROGRESS ─────────────────────

func EncodeBlockProgress(p *BlockProgress) []byte {
	if p == nil {
		return nil
	}
	prog := &pb.ParentChainBlockProgress{
		LastBlock:       p.LastBlock,
		LastGei:         p.LastGEI,
		LastTimestampMs: p.LastTimestampMs,
		LastHash:        p.LastHash.Bytes(),
		LastStateRoot:   p.LastStateRoot.Bytes(),
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(prog)
	return b
}

func DecodeBlockProgress(data []byte) (*BlockProgress, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty BlockProgress data", ErrEncodingCorrupt)
	}
	var prog pb.ParentChainBlockProgress
	if err := proto.Unmarshal(data, &prog); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodingCorrupt, err)
	}
	return &BlockProgress{
		LastBlock:       prog.LastBlock,
		LastGEI:         prog.LastGei,
		LastTimestampMs: prog.LastTimestampMs,
		LastHash:        common.BytesToHash(prog.LastHash),
		LastStateRoot:   common.BytesToHash(prog.LastStateRoot),
	}, nil
}

func EncodeBlockRecord(r *BlockRecord) []byte {
	if r == nil {
		return nil
	}
	hdr := &pb.ParentChainBlockHeader{
		Number:        r.Header.Number,
		ParentHash:    r.Header.ParentHash.Bytes(),
		StateRoot:     r.Header.StateRoot.Bytes(),
		TxsRoot:       r.Header.TxsRoot.Bytes(),
		ReceiptsRoot:  r.Header.ReceiptsRoot.Bytes(),
		TimestampMs:   r.Header.TimestampMs,
		Epoch:         r.Header.Epoch,
		CommitIndex:   uint64(r.Header.CommitIndex),
		Gei:           r.Header.GEI,
		LeaderAddress: r.Header.LeaderAddress.Bytes(),
		CommitDigest:  r.Header.CommitDigest.Bytes(),
		TxCount:       r.Header.TxCount,
	}
	txHashes := make([][]byte, len(r.TxHashes))
	for i, th := range r.TxHashes {
		txHashes[i] = th.Bytes()
	}
	receipts := make([]*pb.ParentChainReceipt, len(r.Receipts))
	for i, rc := range r.Receipts {
		receipts[i] = &pb.ParentChainReceipt{
			TxHash:    rc.TxHash.Bytes(),
			Status:    uint32(rc.Status),
			ErrorCode: rc.ErrorCode,
			Events:    rc.Events,
		}
	}
	rec := &pb.ParentChainBlockRecord{
		Header:    hdr,
		BlockHash: r.BlockHash.Bytes(),
		TxHashes:  txHashes,
		Receipts:  receipts,
		RawBlock:  r.RawBlock,
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(rec)
	return b
}

func DecodeBlockRecord(data []byte) (*BlockRecord, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty BlockRecord data", ErrEncodingCorrupt)
	}
	var rec pb.ParentChainBlockRecord
	if err := proto.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodingCorrupt, err)
	}
	if rec.Header == nil {
		return nil, fmt.Errorf("%w: missing header", ErrEncodingCorrupt)
	}
	txHashes := make([]common.Hash, len(rec.TxHashes))
	for i, h := range rec.TxHashes {
		txHashes[i] = common.BytesToHash(h)
	}
	receipts := make([]*Receipt, len(rec.Receipts))
	for i, pr := range rec.Receipts {
		receipts[i] = &Receipt{
			TxHash:    common.BytesToHash(pr.TxHash),
			Status:    uint8(pr.Status),
			ErrorCode: pr.ErrorCode,
			Events:    pr.Events,
		}
	}
	return &BlockRecord{
		Header: Header{
			Number:        rec.Header.Number,
			ParentHash:    common.BytesToHash(rec.Header.ParentHash),
			StateRoot:     common.BytesToHash(rec.Header.StateRoot),
			TxsRoot:       common.BytesToHash(rec.Header.TxsRoot),
			ReceiptsRoot:  common.BytesToHash(rec.Header.ReceiptsRoot),
			TimestampMs:   rec.Header.TimestampMs,
			Epoch:         rec.Header.Epoch,
			CommitIndex:   uint32(rec.Header.CommitIndex),
			GEI:           rec.Header.Gei,
			LeaderAddress: common.BytesToAddress(rec.Header.LeaderAddress),
			CommitDigest:  common.BytesToHash(rec.Header.CommitDigest),
			TxCount:       rec.Header.TxCount,
		},
		BlockHash: common.BytesToHash(rec.BlockHash),
		TxHashes:  txHashes,
		Receipts:  receipts,
		RawBlock:  rec.RawBlock,
	}, nil
}

// ─── COMMITTER IMPLEMENTATION ────────────────────────────────────────────────

// TreeBlockCommitter combines persistent DB (LevelDB) and NOMT Merkle trie.
type TreeBlockCommitter struct {
	db         *leveldb.DB
	nomtHandle *nomt_ffi.Handle
	treeStore  *TreeStore
	applyMu    sync.Mutex
	// genesisInit, when set, runs inside block 1 before its transactions (see SetGenesisInit).
	genesisInit func(Store) error
}

// SetGenesisInit installs the function that creates the genesis state (float balances). It runs inside the
// execution of block 1, so every validator applies it identically and it is part of block 1's state root.
func (c *TreeBlockCommitter) SetGenesisInit(f func(Store) error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.genesisInit = f
}

func NewTreeBlockCommitter(db *leveldb.DB, nomtHandle *nomt_ffi.Handle) *TreeBlockCommitter {
	c := &TreeBlockCommitter{
		db:         db,
		nomtHandle: nomtHandle,
	}
	c.treeStore = NewTreeStore(&nomtTreeKV{handle: nomtHandle})
	return c
}

type nomtTreeKV struct {
	handle *nomt_ffi.Handle
}

func (n *nomtTreeKV) Get(key [32]byte) ([]byte, bool, error) {
	if n.handle == nil {
		return nil, false, nil
	}
	return n.handle.Read(key)
}

func (n *nomtTreeKV) Put(key [32]byte, val []byte) error {
	return errors.New("cannot write directly to base NOMT: writes must go through ApplyBlock")
}

func (c *TreeBlockCommitter) Store() Store {
	return c.treeStore
}

func (c *TreeBlockCommitter) LastApplied() (BlockProgress, error) {
	data, err := c.db.Get(keyProgressPrefix, nil)
	if err == leveldb.ErrNotFound {
		return BlockProgress{}, nil
	}
	if err != nil {
		return BlockProgress{}, err
	}
	p, err := DecodeBlockProgress(data)
	if err != nil {
		return BlockProgress{}, err
	}
	return *p, nil
}

func (c *TreeBlockCommitter) GetBlockRecord(number uint64) (BlockRecord, bool, error) {
	data, err := c.db.Get(blockNumKey(number), nil)
	if err == leveldb.ErrNotFound {
		return BlockRecord{}, false, nil
	}
	if err != nil {
		return BlockRecord{}, false, err
	}
	rec, err := DecodeBlockRecord(data)
	if err != nil {
		return BlockRecord{}, false, err
	}
	return *rec, true, nil
}

func (c *TreeBlockCommitter) GetBlockRecordByHash(hash common.Hash) (BlockRecord, bool, error) {
	numBytes, err := c.db.Get(blockHashKey(hash), nil)
	if err == leveldb.ErrNotFound {
		return BlockRecord{}, false, nil
	}
	if err != nil {
		return BlockRecord{}, false, err
	}
	num, err := DecodeUint64(numBytes)
	if err != nil {
		return BlockRecord{}, false, err
	}
	return c.GetBlockRecord(num)
}

func (c *TreeBlockCommitter) GetBlockRecords(from, to uint64, limit int) ([]BlockRecord, error) {
	var records []BlockRecord
	for n := from; n <= to; n++ {
		rec, found, err := c.GetBlockRecord(n)
		if err != nil {
			return nil, err
		}
		if !found {
			break
		}
		records = append(records, rec)
		if limit > 0 && len(records) >= limit {
			break
		}
	}
	return records, nil
}

func (c *TreeBlockCommitter) GetTxLocation(txHash common.Hash) (uint64, uint32, bool, error) {
	data, err := c.db.Get(txLocKey(txHash), nil)
	if err == leveldb.ErrNotFound {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	if len(data) < 12 {
		return 0, 0, false, fmt.Errorf("%w: invalid tx location entry", ErrEncodingCorrupt)
	}
	bNum := binary.BigEndian.Uint64(data[0:8])
	tIdx := binary.BigEndian.Uint32(data[8:12])
	return bNum, tIdx, true, nil
}

func (c *TreeBlockCommitter) GetReceipt(txHash common.Hash) (*Receipt, bool, error) {
	bNum, tIdx, found, err := c.GetTxLocation(txHash)
	if err != nil || !found {
		return nil, found, err
	}
	rec, bFound, err := c.GetBlockRecord(bNum)
	if err != nil || !bFound {
		return nil, false, err
	}
	if int(tIdx) >= len(rec.Receipts) {
		return nil, false, nil
	}
	return rec.Receipts[tIdx], true, nil
}

func (c *TreeBlockCommitter) GenerateProof(key [32]byte) ([]byte, error) {
	if c.nomtHandle == nil {
		return nil, errors.New("nomt handle not initialized")
	}
	return c.nomtHandle.GenerateProof(key)
}

func (c *TreeBlockCommitter) NomtRoot() (common.Hash, error) {
	if c.nomtHandle == nil {
		return common.Hash{}, errors.New("nomt handle not initialized")
	}
	r, err := c.nomtHandle.Root()
	if err != nil {
		return common.Hash{}, err
	}
	return common.BytesToHash(r[:]), nil
}

// ApplyBlock executes in atomically and idempotently.
func (c *TreeBlockCommitter) ApplyBlock(in BlockInput, exec TxExecutor) (BlockResult, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	prog, err := c.LastApplied()
	if err != nil {
		return BlockResult{}, err
	}

	txsRoot := TxsRoot(in.Txs)

	// 1. Exactly-once: Number <= LastBlock
	if in.Number != 0 && in.Number <= prog.LastBlock {
		rec, found, err := c.GetBlockRecord(in.Number)
		if err != nil {
			return BlockResult{}, err
		}
		if !found {
			return BlockResult{}, fmt.Errorf("parentchain: block %d already applied but record missing", in.Number)
		}
		if rec.Header.TxsRoot != txsRoot || (in.CommitDigest != common.Hash{} && rec.Header.CommitDigest != in.CommitDigest) {
			return BlockResult{}, fmt.Errorf("%w: block %d content mismatch (txs_root: %s vs %s, commit_digest: %s vs %s)",
				ErrBlockConflict, in.Number, rec.Header.TxsRoot.Hex(), txsRoot.Hex(),
				rec.Header.CommitDigest.Hex(), in.CommitDigest.Hex())
		}
		return BlockResult{Record: rec, Replayed: true}, nil
	}

	// 2. Gap check: Number must equal LastBlock + 1
	if prog.LastBlock == 0 {
		if in.Number != 1 {
			return BlockResult{}, fmt.Errorf("%w: first block delivered is %d, expected 1", ErrBlockGap, in.Number)
		}
	} else if in.Number != prog.LastBlock+1 {
		return BlockResult{}, fmt.Errorf("%w: last applied %d, got %d", ErrBlockGap, prog.LastBlock, in.Number)
	}

	// 3. Two-layer overlay execution
	ov := NewOverlayTree(&nomtTreeKV{handle: c.nomtHandle})
	view := NewTreeStore(ov)

	if prog.LastBlock == 0 && c.genesisInit != nil {
		ov.Push()
		if err := c.genesisInit(view); err != nil {
			ov.Drop()
			return BlockResult{}, fmt.Errorf("parentchain: genesis state init failed: %w", err)
		}
		ov.Merge()
	}

	txErrors := make([]error, len(in.Txs))
	receipts := make([]*Receipt, len(in.Txs))
	txHashes := make([]common.Hash, len(in.Txs))
	encodedReceipts := make([][]byte, len(in.Txs))

	for i, rawTx := range in.Txs {
		ov.Push()
		rc, err := exec(view, i, rawTx)
		if err != nil {
			if rc != nil && rc.ErrorCode >= 200 {
				// Method execution failure (H7):
				// Valid signature and valid nonce => sender consumed their nonce.
				// The handler's partial writes were already reverted by the execution layer,
				// so only the nonce increment remains in this layer.
				// We merge this layer into the block so nonce is advanced, preventing replay.
				ov.Merge()
			} else {
				// Validation / consensus failure (invalid sig, bad nonce, unmarshal err):
				// Discard all writes.
				ov.Drop()
			}
			txErrors[i] = err
			if rc == nil {
				rc = &Receipt{
					TxHash:    TxsRoot([][]byte{rawTx}),
					Status:    0,
					ErrorCode: 1,
				}
			}
		} else {
			ov.Merge() // Merge writes into block layer
		}
		receipts[i] = rc
		txHashes[i] = rc.TxHash
		encodedReceipts[i] = EncodeReceipt(rc)
	}

	// 4. Compute roots
	keys, writes := ov.BlockWrites()
	var stateRoot common.Hash
	var finSession *nomt_ffi.FinishedSession

	if c.nomtHandle != nil {
		session := nomt_ffi.BeginSession(c.nomtHandle)
		if len(keys) > 0 {
			vals := make([][]byte, len(keys))
			for i, k := range keys {
				vals[i] = writes[k]
			}
			if err := session.BatchWrite(keys, vals); err != nil {
				session.Abort()
				return BlockResult{}, fmt.Errorf("nomt batch write failed: %w", err)
			}
		}
		rootBytes, fs, err := session.Finish(c.nomtHandle)
		if err != nil {
			return BlockResult{}, fmt.Errorf("nomt session finish failed: %w", err)
		}
		finSession = fs
		copy(stateRoot[:], rootBytes[:])
	} else {
		// Mock root if running without CGo NOMT
		stateRoot = prog.LastStateRoot
	}

	receiptsRoot := ReceiptsRoot(encodedReceipts)

	ts := in.TimestampMs
	if ts == 0 {
		ts = prog.LastTimestampMs
	}

	hdr := Header{
		Number:        in.Number,
		ParentHash:    prog.LastHash,
		StateRoot:     stateRoot,
		TxsRoot:       txsRoot,
		ReceiptsRoot:  receiptsRoot,
		TimestampMs:   ts,
		Epoch:         in.Epoch,
		CommitIndex:   in.CommitIndex,
		GEI:           in.GEI,
		LeaderAddress: in.LeaderAddress,
		CommitDigest:  in.CommitDigest,
		TxCount:       uint32(len(in.Txs)),
	}
	blockHash := hdr.Hash()

	rec := BlockRecord{
		Header:    hdr,
		BlockHash: blockHash,
		TxHashes:  txHashes,
		Receipts:  receipts,
		RawBlock:  in.RawBlock,
	}
	newProg := BlockProgress{
		LastBlock:       in.Number,
		LastGEI:         in.GEI,
		LastTimestampMs: ts,
		LastHash:        blockHash,
		LastStateRoot:   stateRoot,
	}

	// 5. Durability Barrier (D10, Rule 8):
	// Write block database with Sync: true FIRST, before committing NOMT.
	batch := new(leveldb.Batch)
	recBytes := EncodeBlockRecord(&rec)
	progBytes := EncodeBlockProgress(&newProg)

	batch.Put(blockNumKey(in.Number), recBytes)
	batch.Put(blockHashKey(blockHash), EncodeUint64(in.Number))
	batch.Put(keyProgressPrefix, progBytes)

	for i, th := range txHashes {
		var locBuf [12]byte
		binary.BigEndian.PutUint64(locBuf[0:8], in.Number)
		binary.BigEndian.PutUint32(locBuf[8:12], uint32(i))
		batch.Put(txLocKey(th), locBuf[:])
	}

	if err := c.db.Write(batch, &opt.WriteOptions{Sync: true}); err != nil {
		if finSession != nil {
			finSession.Abort()
		}
		return BlockResult{}, fmt.Errorf("durable block db write failed: %w", err)
	}

	// 6. Commit NOMT to disk after durable block DB lands
	if finSession != nil {
		if err := finSession.CommitPayload(c.nomtHandle); err != nil {
			return BlockResult{}, fmt.Errorf("nomt commit payload failed: %w", err)
		}
	}

	// 7. Update storage tracking
	storage.UpdateLastBlockNumber(in.Number)
	storage.UpdateLastAssignedBlockNumber(in.Number)
	storage.UpdateLastGlobalExecIndex(in.GEI)

	return BlockResult{
		Record:   rec,
		TxErrors: txErrors,
	}, nil
}
