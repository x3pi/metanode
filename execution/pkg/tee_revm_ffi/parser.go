package tee_revm_ffi

import (
	"encoding/binary"
	"fmt"
)

type TeeStateDiff struct {
	Success   bool
	Caller    [20]byte
	Target    [20]byte
	GasUsed   uint64
	ReadKeys  [][32]byte
	WriteKeys [][32]byte
}

// DeserializeTeeStateDiff parses a bincode-encoded byte slice into a TeeStateDiff struct.
// Bincode uses Little-Endian by default and 8-byte lengths for vectors.
func DeserializeTeeStateDiff(data []byte) (*TeeStateDiff, error) {
	if len(data) < 49 {
		return nil, fmt.Errorf("data too short: expected at least 49 bytes, got %d", len(data))
	}

	diff := &TeeStateDiff{}
	
	// 1. Success (1 byte)
	diff.Success = data[0] == 1
	offset := 1

	// 2. Caller (20 bytes)
	copy(diff.Caller[:], data[offset:offset+20])
	offset += 20

	// 3. Target (20 bytes)
	copy(diff.Target[:], data[offset:offset+20])
	offset += 20

	// 4. GasUsed (8 bytes, uint64 little-endian)
	diff.GasUsed = binary.LittleEndian.Uint64(data[offset : offset+8])
	offset += 8

	// 5. ReadKeys (Vec<[u8; 32]>)
	// Length of Vec (8 bytes, uint64 little-endian)
	if offset+8 > len(data) {
		return nil, fmt.Errorf("data too short for ReadKeys length")
	}
	readLen := binary.LittleEndian.Uint64(data[offset : offset+8])
	offset += 8

	if offset+int(readLen*32) > len(data) {
		return nil, fmt.Errorf("data too short for ReadKeys items")
	}
	
	diff.ReadKeys = make([][32]byte, readLen)
	for i := uint64(0); i < readLen; i++ {
		copy(diff.ReadKeys[i][:], data[offset:offset+32])
		offset += 32
	}

	// 6. WriteKeys (Vec<[u8; 32]>)
	if offset+8 > len(data) {
		return nil, fmt.Errorf("data too short for WriteKeys length")
	}
	writeLen := binary.LittleEndian.Uint64(data[offset : offset+8])
	offset += 8

	if offset+int(writeLen*32) > len(data) {
		return nil, fmt.Errorf("data too short for WriteKeys items")
	}

	diff.WriteKeys = make([][32]byte, writeLen)
	for i := uint64(0); i < writeLen; i++ {
		copy(diff.WriteKeys[i][:], data[offset:offset+32])
		offset += 32
	}

	return diff, nil
}
