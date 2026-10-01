package parentchain

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
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
	buf := make([]byte, 8+8+8+32+32)
	binary.BigEndian.PutUint64(buf[0:8], p.LastBlock)
	binary.BigEndian.PutUint64(buf[8:16], p.LastGEI)
	binary.BigEndian.PutUint64(buf[16:24], p.LastTimestampMs)
	copy(buf[24:56], p.LastHash.Bytes())
	copy(buf[56:88], p.LastStateRoot.Bytes())
	return buf
}

func DecodeBlockProgress(data []byte) (*BlockProgress, error) {
	if len(data) < 88 {
		return nil, fmt.Errorf("%w: BlockProgress expected 88 bytes, got %d", ErrEncodingCorrupt, len(data))
	}
	return &BlockProgress{
		LastBlock:       binary.BigEndian.Uint64(data[0:8]),
		LastGEI:         binary.BigEndian.Uint64(data[8:16]),
		LastTimestampMs: binary.BigEndian.Uint64(data[16:24]),
		LastHash:        common.BytesToHash(data[24:56]),
		LastStateRoot:   common.BytesToHash(data[56:88]),
	}, nil
}

func EncodeBlockRecord(r *BlockRecord) []byte {
	hdrBytes := EncodeHeader(&r.Header)
	// hdr (len) + blockHash (32) + txHashesCount (4) + txHashes (N*32) + receiptsCount (4) + each receipt + rawBlock (4 len + bytes)
	totalLen := len(hdrBytes) + 32 + 4 + len(r.TxHashes)*32 + 4
	encodedReceipts := make([][]byte, len(r.Receipts))
	for i, rc := range r.Receipts {
		encodedReceipts[i] = EncodeReceipt(rc)
		totalLen += 4 + len(encodedReceipts[i])
	}
	totalLen += 4 + len(r.RawBlock)

	buf := make([]byte, totalLen)
	offset := 0

	copy(buf[offset:offset+len(hdrBytes)], hdrBytes)
	offset += len(hdrBytes)

	copy(buf[offset:offset+32], r.BlockHash.Bytes())
	offset += 32

	binary.BigEndian.PutUint32(buf[offset:offset+4], uint32(len(r.TxHashes)))
	offset += 4
	for _, th := range r.TxHashes {
		copy(buf[offset:offset+32], th.Bytes())
		offset += 32
	}

	binary.BigEndian.PutUint32(buf[offset:offset+4], uint32(len(r.Receipts)))
	offset += 4
	for _, rb := range encodedReceipts {
		binary.BigEndian.PutUint32(buf[offset:offset+4], uint32(len(rb)))
		offset += 4
		copy(buf[offset:offset+len(rb)], rb)
		offset += len(rb)
	}

	binary.BigEndian.PutUint32(buf[offset:offset+4], uint32(len(r.RawBlock)))
	offset += 4
	copy(buf[offset:offset+len(r.RawBlock)], r.RawBlock)

	return buf
}

func DecodeBlockRecord(data []byte) (*BlockRecord, error) {
	if len(data) < CanonicalHeaderSize+32+8 {
		return nil, fmt.Errorf("%w: BlockRecord too short", ErrEncodingCorrupt)
	}
	offset := 0

	hdr, err := DecodeHeader(data[offset : offset+CanonicalHeaderSize])
	if err != nil {
		return nil, err
	}
	offset += CanonicalHeaderSize

	blockHash := common.BytesToHash(data[offset : offset+32])
	offset += 32

	txCount := int(binary.BigEndian.Uint32(data[offset : offset+4]))
	offset += 4
	if offset+txCount*32 > len(data) {
		return nil, fmt.Errorf("%w: BlockRecord txHashes truncated", ErrEncodingCorrupt)
	}
	txHashes := make([]common.Hash, txCount)
	for i := 0; i < txCount; i++ {
		txHashes[i] = common.BytesToHash(data[offset : offset+32])
		offset += 32
	}

	if offset+4 > len(data) {
		return nil, fmt.Errorf("%w: BlockRecord receipts header truncated", ErrEncodingCorrupt)
	}
	rcCount := int(binary.BigEndian.Uint32(data[offset : offset+4]))
	offset += 4
	receipts := make([]*Receipt, rcCount)
	for i := 0; i < rcCount; i++ {
		if offset+4 > len(data) {
			return nil, fmt.Errorf("%w: BlockRecord receipt len truncated", ErrEncodingCorrupt)
		}
		rcLen := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		offset += 4
		if offset+rcLen > len(data) {
			return nil, fmt.Errorf("%w: BlockRecord receipt body truncated", ErrEncodingCorrupt)
		}
		rc, err := DecodeReceipt(data[offset : offset+rcLen])
		if err != nil {
			return nil, err
		}
		receipts[i] = rc
		offset += rcLen
	}

	var rawBlock []byte
	if offset+4 <= len(data) {
		rawLen := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		offset += 4
		if offset+rawLen <= len(data) {
			rawBlock = make([]byte, rawLen)
			copy(rawBlock, data[offset:offset+rawLen])
		}
	}

	return &BlockRecord{
		Header:    *hdr,
		BlockHash: blockHash,
		TxHashes:  txHashes,
		Receipts:  receipts,
		RawBlock:  rawBlock,
	}, nil
}

// ─── COMMITTER IMPLEMENTATION ────────────────────────────────────────────────

// TreeBlockCommitter combines persistent DB (LevelDB) and NOMT Merkle trie.
type TreeBlockCommitter struct {
	db         *leveldb.DB
	nomtHandle *nomt_ffi.Handle
	treeStore  *TreeStore
	applyMu    sync.Mutex
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

	txErrors := make([]error, len(in.Txs))
	receipts := make([]*Receipt, len(in.Txs))
	txHashes := make([]common.Hash, len(in.Txs))
	encodedReceipts := make([][]byte, len(in.Txs))

	for i, rawTx := range in.Txs {
		ov.Push()
		rc, err := exec(view, i, rawTx)
		if err != nil {
			ov.Drop() // Drop writes from failed tx (fix G6)
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
