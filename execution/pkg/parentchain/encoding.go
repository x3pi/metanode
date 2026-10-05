package parentchain

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

// Namespace byte constants (Section 5.2 of plan)
const (
	NamespaceChainRegistry    byte = 0x01
	NamespaceFloat            byte = 0x02
	NamespaceClaimed          byte = 0x03
	NamespaceTransferRecord   byte = 0x04
	NamespaceSeq              byte = 0x05
	NamespaceVelocity         byte = 0x06
	NamespaceAccountRegistry  byte = 0x07
	NamespaceClusterStateRoot byte = 0x08
	NamespaceInboundLog       byte = 0x09
	NamespaceInboundSeq       byte = 0x0A
	NamespaceSenderNonce      byte = 0x0B
	NamespaceConfigSystem     byte = 0x0C
	NamespaceAccountRegistrationLog byte = 0x0D
	NamespaceAccountRegistrationSeq byte = 0x0E
)

var (
	ErrEncodingCorrupt = errors.New("parentchain: binary encoding corrupt or incomplete")
)

var deterministicMarshal = proto.MarshalOptions{Deterministic: true}

// TreeKey computes the 32-byte key for the NOMT state tree:
// keccak256(namespace_byte || business_key)
func TreeKey(ns byte, businessKey []byte) [32]byte {
	buf := make([]byte, 1+len(businessKey))
	buf[0] = ns
	copy(buf[1:], businessKey)
	return crypto.Keccak256Hash(buf)
}

// ─── CANONICAL HEADER ────────────────────────────────────────────────────────

type Header struct {
	Number        uint64
	ParentHash    common.Hash
	StateRoot     common.Hash
	TxsRoot       common.Hash
	ReceiptsRoot  common.Hash
	TimestampMs   uint64
	Epoch         uint64
	CommitIndex   uint32
	GEI           uint64
	LeaderAddress common.Address
	CommitDigest  common.Hash
	TxCount       uint32
}

func EncodeHeader(h *Header) []byte {
	if h == nil {
		return nil
	}
	protoHdr := &pb.ParentChainBlockHeader{
		Number:        h.Number,
		ParentHash:    h.ParentHash.Bytes(),
		StateRoot:     h.StateRoot.Bytes(),
		TxsRoot:       h.TxsRoot.Bytes(),
		ReceiptsRoot:  h.ReceiptsRoot.Bytes(),
		TimestampMs:   h.TimestampMs,
		Epoch:         h.Epoch,
		CommitIndex:   uint64(h.CommitIndex),
		Gei:           h.GEI,
		LeaderAddress: h.LeaderAddress.Bytes(),
		CommitDigest:  h.CommitDigest.Bytes(),
		TxCount:       h.TxCount,
	}
	b, _ := deterministicMarshal.Marshal(protoHdr)
	return b
}

func DecodeHeader(data []byte) (*Header, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty header data", ErrEncodingCorrupt)
	}
	var protoHdr pb.ParentChainBlockHeader
	if err := proto.Unmarshal(data, &protoHdr); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodingCorrupt, err)
	}
	return &Header{
		Number:        protoHdr.Number,
		ParentHash:    common.BytesToHash(protoHdr.ParentHash),
		StateRoot:     common.BytesToHash(protoHdr.StateRoot),
		TxsRoot:       common.BytesToHash(protoHdr.TxsRoot),
		ReceiptsRoot:  common.BytesToHash(protoHdr.ReceiptsRoot),
		TimestampMs:   protoHdr.TimestampMs,
		Epoch:         protoHdr.Epoch,
		CommitIndex:   uint32(protoHdr.CommitIndex),
		GEI:           protoHdr.Gei,
		LeaderAddress: common.BytesToAddress(protoHdr.LeaderAddress),
		CommitDigest:  common.BytesToHash(protoHdr.CommitDigest),
		TxCount:       protoHdr.TxCount,
	}, nil
}

func (h *Header) Hash() common.Hash {
	return crypto.Keccak256Hash(EncodeHeader(h))
}

// ─── CANONICAL RECEIPT ───────────────────────────────────────────────────────

type Receipt struct {
	TxHash    common.Hash
	Status    uint8 // 1 = success, 0 = failed
	ErrorCode uint32
	Events    [][]byte
}

func EncodeReceipt(r *Receipt) []byte {
	if r == nil {
		return nil
	}
	pr := &pb.ParentChainReceipt{
		TxHash:    r.TxHash.Bytes(),
		Status:    uint32(r.Status),
		ErrorCode: r.ErrorCode,
		Events:    r.Events,
	}
	b, _ := deterministicMarshal.Marshal(pr)
	return b
}

func DecodeReceipt(data []byte) (*Receipt, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty receipt data", ErrEncodingCorrupt)
	}
	var pr pb.ParentChainReceipt
	if err := proto.Unmarshal(data, &pr); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodingCorrupt, err)
	}
	return &Receipt{
		TxHash:    common.BytesToHash(pr.TxHash),
		Status:    uint8(pr.Status),
		ErrorCode: pr.ErrorCode,
		Events:    pr.Events,
	}, nil
}

// ─── STATE VALUES CANONICAL ENCODINGS ───────────────────────────────────────

func EncodeBigInt(v *big.Int) []byte {
	return padTo32(v)
}

func DecodeBigInt(data []byte) (*big.Int, error) {
	if len(data) == 0 {
		return big.NewInt(0), nil
	}
	return new(big.Int).SetBytes(data), nil
}

func EncodeUint64(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

func DecodeUint64(data []byte) (uint64, error) {
	if len(data) < 8 {
		return 0, fmt.Errorf("%w: expected 8 bytes for uint64, got %d", ErrEncodingCorrupt, len(data))
	}
	return binary.BigEndian.Uint64(data[:8]), nil
}

func EncodeChainRegistryEntry(e *ChainRegistryEntry) []byte {
	if e == nil {
		return nil
	}
	entry := &pb.ChainRegistryEntryProto{
		PubKey:     e.FloatIdentityKey[:],
		ClusterId:  e.ClusterIDDescriptive,
		Authorized: e.Authorized,
	}
	b, _ := deterministicMarshal.Marshal(entry)
	return b
}

func DecodeChainRegistryEntry(data []byte) (*ChainRegistryEntry, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty ChainRegistryEntry data", ErrEncodingCorrupt)
	}
	var entry pb.ChainRegistryEntryProto
	if err := proto.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodingCorrupt, err)
	}
	var pubKey cm.PublicKey
	copy(pubKey[:], entry.PubKey)
	return &ChainRegistryEntry{
		FloatIdentityKey:     pubKey,
		ClusterIDDescriptive: entry.ClusterId,
		ChainIDDescriptive:   entry.ClusterId,
		Authorized:           entry.Authorized,
	}, nil
}

func EncodeFloatTransferRecord(r *FloatTransferRecord) []byte {
	if r == nil {
		return nil
	}
	rec := &pb.FloatTransferRecordProto{
		CommitTime: r.ConfirmedAtBlockTime,
	}
	if r.SourceKey != nil {
		rec.SourceKey = r.SourceKey[:]
	}
	rec.ToKey = r.DestKey[:]
	if r.Value != nil {
		rec.Value = r.Value.Bytes()
	}
	b, _ := deterministicMarshal.Marshal(rec)
	return b
}

func DecodeFloatTransferRecord(data []byte) (*FloatTransferRecord, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty FloatTransferRecord data", ErrEncodingCorrupt)
	}
	var rec pb.FloatTransferRecordProto
	if err := proto.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodingCorrupt, err)
	}
	var srcKey *cm.PublicKey
	if len(rec.SourceKey) == 48 {
		var k cm.PublicKey
		copy(k[:], rec.SourceKey)
		srcKey = &k
	}
	var toKey cm.PublicKey
	copy(toKey[:], rec.ToKey)

	var val *big.Int
	if len(rec.Value) > 0 {
		val = new(big.Int).SetBytes(rec.Value)
	} else {
		val = big.NewInt(0)
	}

	return &FloatTransferRecord{
		SourceKey:            srcKey,
		DestKey:              toKey,
		Value:                val,
		ConfirmedAtBlockTime: rec.CommitTime,
	}, nil
}

func EncodeFloatVelocityState(s *FloatVelocityState) []byte {
	if s == nil {
		return nil
	}
	st := &pb.FloatVelocityStateProto{
		WindowStart: s.WindowStart,
	}
	if s.WindowBaseAlloc != nil {
		st.WindowBaseAlloc = s.WindowBaseAlloc.Bytes()
	}
	if s.Spent != nil {
		st.Spent = s.Spent.Bytes()
	}
	b, _ := deterministicMarshal.Marshal(st)
	return b
}

func DecodeFloatVelocityState(data []byte) (*FloatVelocityState, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty FloatVelocityState data", ErrEncodingCorrupt)
	}
	var st pb.FloatVelocityStateProto
	if err := proto.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodingCorrupt, err)
	}
	var baseAlloc, spent *big.Int
	if len(st.WindowBaseAlloc) > 0 {
		baseAlloc = new(big.Int).SetBytes(st.WindowBaseAlloc)
	} else {
		baseAlloc = big.NewInt(0)
	}
	if len(st.Spent) > 0 {
		spent = new(big.Int).SetBytes(st.Spent)
	} else {
		spent = big.NewInt(0)
	}

	return &FloatVelocityState{
		WindowStart:     st.WindowStart,
		WindowBaseAlloc: baseAlloc,
		Spent:           spent,
	}, nil
}

func EncodeTransferEvent(ev *TransferEvent) []byte {
	if ev == nil {
		return nil
	}
	tev := &pb.TransferEventProto{
		MsgId:        ev.MsgID.Bytes(),
		SourcePubKey: ev.SourcePubKey[:],
		DestPubKey:   ev.DestPubKey[:],
		SourceSeq:    ev.SourceSeq,
		Sender:       ev.Sender.Bytes(),
		Target:       ev.Target.Bytes(),
		PayloadHash:  ev.PayloadHash.Bytes(),
		BlockTime:    ev.BlockTime,
		IsRefund:     ev.IsRefund,
	}
	if ev.Amount != nil {
		tev.Amount = ev.Amount.Bytes()
	}
	b, _ := deterministicMarshal.Marshal(tev)
	return b
}

func DecodeTransferEvent(data []byte) (*TransferEvent, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty TransferEvent data", ErrEncodingCorrupt)
	}
	var tev pb.TransferEventProto
	if err := proto.Unmarshal(data, &tev); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodingCorrupt, err)
	}
	var srcKey, destKey cm.PublicKey
	copy(srcKey[:], tev.SourcePubKey)
	copy(destKey[:], tev.DestPubKey)
	var amount *big.Int
	if len(tev.Amount) > 0 {
		amount = new(big.Int).SetBytes(tev.Amount)
	} else {
		amount = big.NewInt(0)
	}
	return &TransferEvent{
		MsgID:        common.BytesToHash(tev.MsgId),
		SourcePubKey: srcKey,
		DestPubKey:   destKey,
		SourceSeq:    tev.SourceSeq,
		Sender:       common.BytesToAddress(tev.Sender),
		Target:       common.BytesToAddress(tev.Target),
		Amount:       amount,
		PayloadHash:  common.BytesToHash(tev.PayloadHash),
		BlockTime:    tev.BlockTime,
		IsRefund:     tev.IsRefund,
	}, nil
}

// AccountRegisteredEvent represents an account registration record for an execution cluster.
type AccountRegisteredEvent struct {
	Seq         uint64         `json:"seq"`
	UserAddress common.Address `json:"user_address"`
	ClusterKey  cm.PublicKey   `json:"cluster_key"`
	ParentBlock uint64         `json:"parent_block,omitempty"`
}

func EncodeAccountRegisteredEvent(ev *AccountRegisteredEvent) []byte {
	if ev == nil {
		return nil
	}
	protoEv := &pb.AccountRegisteredEventProto{
		Seq:         ev.Seq,
		UserAddress: ev.UserAddress.Bytes(),
		ClusterKey:  ev.ClusterKey[:],
		ParentBlock: ev.ParentBlock,
	}
	b, _ := deterministicMarshal.Marshal(protoEv)
	return b
}

func DecodeAccountRegisteredEvent(data []byte) (*AccountRegisteredEvent, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty AccountRegisteredEvent data", ErrEncodingCorrupt)
	}
	var protoEv pb.AccountRegisteredEventProto
	if err := proto.Unmarshal(data, &protoEv); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodingCorrupt, err)
	}
	var clusterKey cm.PublicKey
	copy(clusterKey[:], protoEv.ClusterKey)
	return &AccountRegisteredEvent{
		Seq:         protoEv.Seq,
		UserAddress: common.BytesToAddress(protoEv.UserAddress),
		ClusterKey:  clusterKey,
		ParentBlock: protoEv.ParentBlock,
	}, nil
}

