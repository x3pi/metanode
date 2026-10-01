package parentchain

import (
	"bytes"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// TreeKV is the storage interface for 32-byte key path Merkle trees.
type TreeKV interface {
	Get(key [32]byte) ([]byte, bool, error)
	Put(key [32]byte, val []byte) error
}

// MemTreeKV is an in-memory TreeKV for testing.
type MemTreeKV struct {
	data map[[32]byte][]byte
}

func NewMemTreeKV() *MemTreeKV {
	return &MemTreeKV{data: make(map[[32]byte][]byte)}
}

func (m *MemTreeKV) Get(key [32]byte) ([]byte, bool, error) {
	v, ok := m.data[key]
	if !ok {
		return nil, false, nil
	}
	res := make([]byte, len(v))
	copy(res, v)
	return res, true, nil
}

func (m *MemTreeKV) Put(key [32]byte, val []byte) error {
	v := make([]byte, len(val))
	copy(v, val)
	m.data[key] = v
	return nil
}

// ─── OVERLAY TREE ────────────────────────────────────────────────────────────

// OverlayTree manages block-level and transaction-level uncommitted state writes.
// layers[0] is the block-level layer.
// layers[1..] are transaction in-flight layers.
type OverlayTree struct {
	base   TreeKV
	layers []map[[32]byte][]byte
}

func NewOverlayTree(base TreeKV) *OverlayTree {
	return &OverlayTree{
		base:   base,
		layers: []map[[32]byte][]byte{{}},
	}
}

// Push starts a new transaction-level write layer.
func (o *OverlayTree) Push() {
	o.layers = append(o.layers, make(map[[32]byte][]byte))
}

// Drop discards the topmost transaction layer (on transaction execution failure).
func (o *OverlayTree) Drop() {
	if len(o.layers) > 1 {
		o.layers = o.layers[:len(o.layers)-1]
	}
}

// Merge merges the topmost transaction layer into the layer beneath it.
func (o *OverlayTree) Merge() {
	if len(o.layers) < 2 {
		return
	}
	top := o.layers[len(o.layers)-1]
	below := o.layers[len(o.layers)-2]
	for k, v := range top {
		below[k] = v
	}
	o.layers = o.layers[:len(o.layers)-1]
}

func (o *OverlayTree) Get(key [32]byte) ([]byte, bool, error) {
	for i := len(o.layers) - 1; i >= 0; i-- {
		if v, ok := o.layers[i][key]; ok {
			res := make([]byte, len(v))
			copy(res, v)
			return res, true, nil
		}
	}
	if o.base != nil {
		return o.base.Get(key)
	}
	return nil, false, nil
}

func (o *OverlayTree) Put(key [32]byte, val []byte) error {
	top := o.layers[len(o.layers)-1]
	v := make([]byte, len(val))
	copy(v, val)
	top[key] = v
	return nil
}

// BlockWrites returns the final merged writes in the block layer (layer 0)
// sorted lexicographically by 32-byte key path.
func (o *OverlayTree) BlockWrites() ([][32]byte, map[[32]byte][]byte) {
	w := o.layers[0]
	keys := make([][32]byte, 0, len(w))
	for k := range w {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare(keys[i][:], keys[j][:]) < 0
	})
	return keys, w
}

// ─── TREE STORE (TYPED STORE OVER TREEKV) ───────────────────────────────────

type TreeStore struct {
	kv TreeKV
}

func NewTreeStore(kv TreeKV) *TreeStore {
	return &TreeStore{kv: kv}
}

func (s *TreeStore) GetChainRegistry(key common.Hash) (ChainRegistryEntry, bool, error) {
	k := TreeKey(NamespaceChainRegistry, key.Bytes())
	data, found, err := s.kv.Get(k)
	if err != nil || !found {
		return ChainRegistryEntry{}, false, err
	}
	entry, err := DecodeChainRegistryEntry(data)
	if err != nil {
		return ChainRegistryEntry{}, false, err
	}
	return *entry, true, nil
}

func (s *TreeStore) SetChainRegistry(key common.Hash, entry ChainRegistryEntry) error {
	k := TreeKey(NamespaceChainRegistry, key.Bytes())
	_, found, err := s.kv.Get(k)
	if err != nil {
		return err
	}
	if !found {
		// Index this new key in tree for GetAllChainRegistryKeys enumeration
		countKey := TreeKey(NamespaceChainRegistry, []byte("count"))
		countBytes, cFound, err := s.kv.Get(countKey)
		if err != nil {
			return err
		}
		var count uint64
		if cFound {
			count, _ = DecodeUint64(countBytes)
		}
		idxKey := TreeKey(NamespaceChainRegistry, append([]byte("idx:"), EncodeUint64(count)...))
		if err := s.kv.Put(idxKey, key.Bytes()); err != nil {
			return err
		}
		if err := s.kv.Put(countKey, EncodeUint64(count+1)); err != nil {
			return err
		}
	}

	return s.kv.Put(k, EncodeChainRegistryEntry(&entry))
}

func (s *TreeStore) GetAllChainRegistryKeys() ([]common.Hash, error) {
	countKey := TreeKey(NamespaceChainRegistry, []byte("count"))
	countBytes, found, err := s.kv.Get(countKey)
	if err != nil || !found {
		return nil, err
	}
	count, err := DecodeUint64(countBytes)
	if err != nil {
		return nil, err
	}
	res := make([]common.Hash, 0, count)
	for i := uint64(0); i < count; i++ {
		idxKey := TreeKey(NamespaceChainRegistry, append([]byte("idx:"), EncodeUint64(i)...))
		kBytes, kFound, err := s.kv.Get(idxKey)
		if err != nil {
			return nil, err
		}
		if kFound && len(kBytes) >= 32 {
			res = append(res, common.BytesToHash(kBytes))
		}
	}
	return res, nil
}

func (s *TreeStore) GetFloat(key common.Hash) (*big.Int, error) {
	k := TreeKey(NamespaceFloat, key.Bytes())
	data, found, err := s.kv.Get(k)
	if err != nil {
		return nil, err
	}
	if !found {
		return big.NewInt(0), nil
	}
	return DecodeBigInt(data)
}

func (s *TreeStore) SetFloat(key common.Hash, balance *big.Int) error {
	k := TreeKey(NamespaceFloat, key.Bytes())
	return s.kv.Put(k, EncodeBigInt(balance))
}

func (s *TreeStore) GetClaimed(messageID common.Hash) (FloatOutcome, error) {
	k := TreeKey(NamespaceClaimed, messageID.Bytes())
	data, found, err := s.kv.Get(k)
	if err != nil {
		return FloatOutcomeNone, err
	}
	if !found || len(data) == 0 {
		return FloatOutcomeNone, nil
	}
	return FloatOutcome(data[0]), nil
}

func (s *TreeStore) SetClaimed(messageID common.Hash, outcome FloatOutcome) error {
	k := TreeKey(NamespaceClaimed, messageID.Bytes())
	return s.kv.Put(k, []byte{byte(outcome)})
}

func (s *TreeStore) GetTransferRecord(messageID common.Hash) (FloatTransferRecord, bool, error) {
	k := TreeKey(NamespaceTransferRecord, messageID.Bytes())
	data, found, err := s.kv.Get(k)
	if err != nil || !found {
		return FloatTransferRecord{}, false, err
	}
	rec, err := DecodeFloatTransferRecord(data)
	if err != nil {
		return FloatTransferRecord{}, false, err
	}
	return *rec, true, nil
}

func (s *TreeStore) SetTransferRecord(messageID common.Hash, rec FloatTransferRecord) error {
	k := TreeKey(NamespaceTransferRecord, messageID.Bytes())
	return s.kv.Put(k, EncodeFloatTransferRecord(&rec))
}

func (s *TreeStore) GetFloatSeq(key common.Hash) (uint64, error) {
	k := TreeKey(NamespaceSeq, key.Bytes())
	data, found, err := s.kv.Get(k)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	return DecodeUint64(data)
}

func (s *TreeStore) SetFloatSeq(key common.Hash, seq uint64) error {
	k := TreeKey(NamespaceSeq, key.Bytes())
	return s.kv.Put(k, EncodeUint64(seq))
}

func (s *TreeStore) GetVelocity(key common.Hash) (FloatVelocityState, error) {
	k := TreeKey(NamespaceVelocity, key.Bytes())
	data, found, err := s.kv.Get(k)
	if err != nil {
		return FloatVelocityState{}, err
	}
	if !found {
		return FloatVelocityState{
			WindowBaseAlloc: big.NewInt(0),
			Spent:           big.NewInt(0),
		}, nil
	}
	st, err := DecodeFloatVelocityState(data)
	if err != nil {
		return FloatVelocityState{}, err
	}
	return *st, nil
}

func (s *TreeStore) SetVelocity(key common.Hash, st FloatVelocityState) error {
	k := TreeKey(NamespaceVelocity, key.Bytes())
	return s.kv.Put(k, EncodeFloatVelocityState(&st))
}

func (s *TreeStore) GetAccountRegistry(userAddress common.Address) (cm.PublicKey, bool, error) {
	k := TreeKey(NamespaceAccountRegistry, userAddress.Bytes())
	data, found, err := s.kv.Get(k)
	if err != nil || !found || len(data) < 48 {
		return cm.PublicKey{}, false, err
	}
	var pubKey cm.PublicKey
	copy(pubKey[:], data[0:48])
	return pubKey, true, nil
}

func (s *TreeStore) SetAccountRegistry(userAddress common.Address, floatIdentityKey cm.PublicKey) error {
	k := TreeKey(NamespaceAccountRegistry, userAddress.Bytes())
	return s.kv.Put(k, floatIdentityKey[:])
}

func (s *TreeStore) GetStateRoot(clusterKeyHash common.Hash, epoch uint64) (common.Hash, bool, error) {
	bKey := append(clusterKeyHash.Bytes(), EncodeUint64(epoch)...)
	k := TreeKey(NamespaceClusterStateRoot, bKey)
	data, found, err := s.kv.Get(k)
	if err != nil || !found || len(data) < 32 {
		return common.Hash{}, false, err
	}
	return common.BytesToHash(data[:32]), true, nil
}

func (s *TreeStore) SetStateRoot(clusterKeyHash common.Hash, epoch uint64, root common.Hash) error {
	bKey := append(clusterKeyHash.Bytes(), EncodeUint64(epoch)...)
	k := TreeKey(NamespaceClusterStateRoot, bKey)
	return s.kv.Put(k, root.Bytes())
}

func (s *TreeStore) AppendInboundTransfer(destKeyHash common.Hash, event *TransferEvent) error {
	seqKey := TreeKey(NamespaceInboundSeq, destKeyHash.Bytes())
	seqBytes, found, err := s.kv.Get(seqKey)
	if err != nil {
		return err
	}
	var seq uint64
	if found {
		seq, _ = DecodeUint64(seqBytes)
	}

	bKey := append(destKeyHash.Bytes(), EncodeUint64(seq)...)
	evKey := TreeKey(NamespaceInboundLog, bKey)
	if err := s.kv.Put(evKey, EncodeTransferEvent(event)); err != nil {
		return err
	}
	return s.kv.Put(seqKey, EncodeUint64(seq+1))
}

func (s *TreeStore) GetInboundTransfers(destKeyHash common.Hash, cursor uint64) ([]*TransferEvent, uint64, error) {
	seqKey := TreeKey(NamespaceInboundSeq, destKeyHash.Bytes())
	seqBytes, found, err := s.kv.Get(seqKey)
	if err != nil {
		return nil, cursor, err
	}
	if !found {
		return nil, 0, nil
	}
	total, err := DecodeUint64(seqBytes)
	if err != nil {
		return nil, cursor, err
	}

	var events []*TransferEvent
	for i := cursor; i < total; i++ {
		bKey := append(destKeyHash.Bytes(), EncodeUint64(i)...)
		evKey := TreeKey(NamespaceInboundLog, bKey)
		evData, evFound, err := s.kv.Get(evKey)
		if err != nil {
			return nil, i, err
		}
		if evFound {
			ev, err := DecodeTransferEvent(evData)
			if err != nil {
				return nil, i, err
			}
			events = append(events, ev)
		}
	}
	return events, total, nil
}

// GetNonce returns the current sequential nonce for an account (namespace 0x0B).
func (s *TreeStore) GetNonce(sender common.Address) (uint64, error) {
	k := TreeKey(NamespaceSenderNonce, sender.Bytes())
	data, found, err := s.kv.Get(k)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	return DecodeUint64(data)
}

// SetNonce updates the sequential nonce for an account (namespace 0x0B).
func (s *TreeStore) SetNonce(sender common.Address, nonce uint64) error {
	k := TreeKey(NamespaceSenderNonce, sender.Bytes())
	return s.kv.Put(k, EncodeUint64(nonce))
}
