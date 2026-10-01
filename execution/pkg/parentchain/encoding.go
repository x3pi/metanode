package parentchain

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
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
)

var (
	ErrEncodingCorrupt = errors.New("parentchain: binary encoding corrupt or incomplete")
)

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

const CanonicalHeaderSize = 8 + 32 + 32 + 32 + 32 + 8 + 8 + 4 + 8 + 20 + 32 + 4 // 230 bytes

func EncodeHeader(h *Header) []byte {
	buf := make([]byte, CanonicalHeaderSize)
	binary.BigEndian.PutUint64(buf[0:8], h.Number)
	copy(buf[8:40], h.ParentHash.Bytes())
	copy(buf[40:72], h.StateRoot.Bytes())
	copy(buf[72:104], h.TxsRoot.Bytes())
	copy(buf[104:136], h.ReceiptsRoot.Bytes())
	binary.BigEndian.PutUint64(buf[136:144], h.TimestampMs)
	binary.BigEndian.PutUint64(buf[144:152], h.Epoch)
	binary.BigEndian.PutUint32(buf[152:156], h.CommitIndex)
	binary.BigEndian.PutUint64(buf[156:164], h.GEI)
	copy(buf[164:184], h.LeaderAddress.Bytes())
	copy(buf[184:216], h.CommitDigest.Bytes())
	binary.BigEndian.PutUint32(buf[216:220], h.TxCount)
	return buf
}

func DecodeHeader(data []byte) (*Header, error) {
	if len(data) < CanonicalHeaderSize {
		return nil, fmt.Errorf("%w: header expected %d bytes, got %d", ErrEncodingCorrupt, CanonicalHeaderSize, len(data))
	}
	h := &Header{
		Number:        binary.BigEndian.Uint64(data[0:8]),
		ParentHash:    common.BytesToHash(data[8:40]),
		StateRoot:     common.BytesToHash(data[40:72]),
		TxsRoot:       common.BytesToHash(data[72:104]),
		ReceiptsRoot:  common.BytesToHash(data[104:136]),
		TimestampMs:   binary.BigEndian.Uint64(data[136:144]),
		Epoch:         binary.BigEndian.Uint64(data[144:152]),
		CommitIndex:   binary.BigEndian.Uint32(data[152:156]),
		GEI:           binary.BigEndian.Uint64(data[156:164]),
		LeaderAddress: common.BytesToAddress(data[164:184]),
		CommitDigest:  common.BytesToHash(data[184:216]),
		TxCount:       binary.BigEndian.Uint32(data[216:220]),
	}
	return h, nil
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
	// 32 (TxHash) + 1 (Status) + 4 (ErrorCode) + 4 (events count) + each event (4 len + bytes)
	totalLen := 32 + 1 + 4 + 4
	for _, ev := range r.Events {
		totalLen += 4 + len(ev)
	}
	buf := make([]byte, totalLen)
	copy(buf[0:32], r.TxHash.Bytes())
	buf[32] = r.Status
	binary.BigEndian.PutUint32(buf[33:37], r.ErrorCode)
	binary.BigEndian.PutUint32(buf[37:41], uint32(len(r.Events)))
	offset := 41
	for _, ev := range r.Events {
		binary.BigEndian.PutUint32(buf[offset:offset+4], uint32(len(ev)))
		offset += 4
		copy(buf[offset:offset+len(ev)], ev)
		offset += len(ev)
	}
	return buf
}

func DecodeReceipt(data []byte) (*Receipt, error) {
	if len(data) < 41 {
		return nil, fmt.Errorf("%w: receipt too short", ErrEncodingCorrupt)
	}
	r := &Receipt{
		TxHash:    common.BytesToHash(data[0:32]),
		Status:    data[32],
		ErrorCode: binary.BigEndian.Uint32(data[33:37]),
	}
	evCount := int(binary.BigEndian.Uint32(data[37:41]))
	r.Events = make([][]byte, 0, evCount)
	offset := 41
	for i := 0; i < evCount; i++ {
		if offset+4 > len(data) {
			return nil, fmt.Errorf("%w: receipt truncated reading event len", ErrEncodingCorrupt)
		}
		l := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		offset += 4
		if offset+l > len(data) {
			return nil, fmt.Errorf("%w: receipt truncated reading event payload", ErrEncodingCorrupt)
		}
		ev := make([]byte, l)
		copy(ev, data[offset:offset+l])
		r.Events = append(r.Events, ev)
		offset += l
	}
	return r, nil
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
	buf := make([]byte, 48+8)
	copy(buf[0:48], e.FloatIdentityKey[:])
	binary.BigEndian.PutUint64(buf[48:56], e.ClusterIDDescriptive)
	return buf
}

func DecodeChainRegistryEntry(data []byte) (*ChainRegistryEntry, error) {
	if len(data) < 56 {
		return nil, fmt.Errorf("%w: expected at least 56 bytes for ChainRegistryEntry, got %d", ErrEncodingCorrupt, len(data))
	}
	var pubKey cm.PublicKey
	copy(pubKey[:], data[0:48])
	cid := binary.BigEndian.Uint64(data[48:56])
	return &ChainRegistryEntry{
		FloatIdentityKey:     pubKey,
		ClusterIDDescriptive: cid,
		ChainIDDescriptive:   cid,
	}, nil
}

func EncodeFloatTransferRecord(r *FloatTransferRecord) []byte {
	// 1 (hasSource) + 48 (SourceKey if present) + 48 (DestKey) + 32 (Value) + 8 (ConfirmedAtBlockTime)
	var hasSource byte
	if r.SourceKey != nil {
		hasSource = 1
	}
	buf := make([]byte, 1+48+48+32+8)
	buf[0] = hasSource
	if hasSource == 1 {
		copy(buf[1:49], r.SourceKey[:])
	}
	copy(buf[49:97], r.DestKey[:])
	copy(buf[97:129], padTo32(r.Value))
	binary.BigEndian.PutUint64(buf[129:137], r.ConfirmedAtBlockTime)
	return buf
}

func DecodeFloatTransferRecord(data []byte) (*FloatTransferRecord, error) {
	if len(data) < 137 {
		return nil, fmt.Errorf("%w: expected 137 bytes for FloatTransferRecord, got %d", ErrEncodingCorrupt, len(data))
	}
	var srcKey *cm.PublicKey
	if data[0] == 1 {
		var k cm.PublicKey
		copy(k[:], data[1:49])
		srcKey = &k
	}
	var destKey cm.PublicKey
	copy(destKey[:], data[49:97])
	val := new(big.Int).SetBytes(data[97:129])
	timeMs := binary.BigEndian.Uint64(data[129:137])
	return &FloatTransferRecord{
		SourceKey:            srcKey,
		DestKey:              destKey,
		Value:                val,
		ConfirmedAtBlockTime: timeMs,
	}, nil
}

func EncodeFloatVelocityState(s *FloatVelocityState) []byte {
	buf := make([]byte, 8+32+32)
	binary.BigEndian.PutUint64(buf[0:8], s.WindowStart)
	copy(buf[8:40], padTo32(s.WindowBaseAlloc))
	copy(buf[40:72], padTo32(s.Spent))
	return buf
}

func DecodeFloatVelocityState(data []byte) (*FloatVelocityState, error) {
	if len(data) < 72 {
		return nil, fmt.Errorf("%w: expected 72 bytes for FloatVelocityState, got %d", ErrEncodingCorrupt, len(data))
	}
	wStart := binary.BigEndian.Uint64(data[0:8])
	baseAlloc := new(big.Int).SetBytes(data[8:40])
	spent := new(big.Int).SetBytes(data[40:72])
	return &FloatVelocityState{
		WindowStart:     wStart,
		WindowBaseAlloc: baseAlloc,
		Spent:           spent,
	}, nil
}

func EncodeTransferEvent(ev *TransferEvent) []byte {
	// 32 (MsgID) + 48 (DestPubKey) + 20 (Sender) + 20 (Target) + 32 (Amount) + 8 (BlockTime) = 160 bytes
	buf := make([]byte, 32+48+20+20+32+8)
	copy(buf[0:32], ev.MsgID.Bytes())
	copy(buf[32:80], ev.DestPubKey[:])
	copy(buf[80:100], ev.Sender.Bytes())
	copy(buf[100:120], ev.Target.Bytes())
	copy(buf[120:152], padTo32(ev.Amount))
	binary.BigEndian.PutUint64(buf[152:160], ev.BlockTime)
	return buf
}

func DecodeTransferEvent(data []byte) (*TransferEvent, error) {
	if len(data) < 160 {
		return nil, fmt.Errorf("%w: expected 160 bytes for TransferEvent, got %d", ErrEncodingCorrupt, len(data))
	}
	var destKey cm.PublicKey
	copy(destKey[:], data[32:80])
	return &TransferEvent{
		MsgID:      common.BytesToHash(data[0:32]),
		DestPubKey: destKey,
		Sender:     common.BytesToAddress(data[80:100]),
		Target:     common.BytesToAddress(data[100:120]),
		Amount:     new(big.Int).SetBytes(data[120:152]),
		BlockTime:  binary.BigEndian.Uint64(data[152:160]),
	}, nil
}
