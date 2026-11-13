package storage

import (
	"encoding/binary"
	"fmt"
)

const (
	// NomtUndoKindAccount identifies compact NOMT pre-image undo records for account_state.
	NomtUndoKindAccount = "ua"
	// NomtUndoKindStake identifies compact NOMT pre-image undo records for stake_db.
	NomtUndoKindStake = "us"
	// NomtUndoRetentionBlocks bounds the rolling undo window stored in StorageBackupDb.
	NomtUndoRetentionBlocks uint64 = 256

	maxNomtUndoEntries = 1_000_000
)

// NomtUndoKey builds the compact storage key (<kind> + 8-byte big-endian blockNum).
func NomtUndoKey(kind string, blockNum uint64) []byte {
	key := make([]byte, len(kind)+8)
	copy(key, kind)
	binary.BigEndian.PutUint64(key[len(kind):], blockNum)
	return key
}

// EncodeNomtUndoBatch encodes [rawKey, oldVal] pre-image pairs using compact uvarints.
// A zero-length oldVal represents a key that did not exist before this block (newly inserted).
func EncodeNomtUndoBatch(entries [][2][]byte) []byte {
	totalSize := binary.MaxVarintLen64
	for _, kv := range entries {
		totalSize += binary.MaxVarintLen64*2 + len(kv[0]) + len(kv[1])
	}
	buf := make([]byte, 0, totalSize)
	buf = binary.AppendUvarint(buf, uint64(len(entries)))
	for _, kv := range entries {
		buf = binary.AppendUvarint(buf, uint64(len(kv[0])))
		buf = append(buf, kv[0]...)
		buf = binary.AppendUvarint(buf, uint64(len(kv[1])))
		buf = append(buf, kv[1]...)
	}
	return buf
}

// DecodeNomtUndoBatch decodes a compact NOMT undo batch.
// Entries encoded with oldVal length 0 decode with value == nil (newly inserted key).
func DecodeNomtUndoBatch(raw []byte) ([][2][]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty nomt undo payload")
	}
	offset := 0
	count, n := binary.Uvarint(raw[offset:])
	if n <= 0 {
		return nil, fmt.Errorf("invalid nomt undo entry count")
	}
	offset += n
	if count > maxNomtUndoEntries {
		return nil, fmt.Errorf("nomt undo entry count %d exceeds max %d", count, maxNomtUndoEntries)
	}

	entries := make([][2][]byte, count)
	for i := uint64(0); i < count; i++ {
		keyLenU64, nKey := binary.Uvarint(raw[offset:])
		if nKey <= 0 {
			return nil, fmt.Errorf("invalid nomt undo key length at index %d", i)
		}
		offset += nKey
		if keyLenU64 > uint64(len(raw)-offset) {
			return nil, fmt.Errorf("truncated nomt undo key at index %d", i)
		}
		keyLen := int(keyLenU64)
		var keyCopy []byte
		if keyLen > 0 {
			keyCopy = make([]byte, keyLen)
			copy(keyCopy, raw[offset:offset+keyLen])
			offset += keyLen
		}

		valLenU64, nVal := binary.Uvarint(raw[offset:])
		if nVal <= 0 {
			return nil, fmt.Errorf("invalid nomt undo value length at index %d", i)
		}
		offset += nVal
		if valLenU64 > uint64(len(raw)-offset) {
			return nil, fmt.Errorf("truncated nomt undo value at index %d", i)
		}
		valLen := int(valLenU64)
		var valCopy []byte
		if valLen > 0 {
			valCopy = make([]byte, valLen)
			copy(valCopy, raw[offset:offset+valLen])
			offset += valLen
		}

		entries[i] = [2][]byte{keyCopy, valCopy}
	}
	return entries, nil
}

// PersistNomtUndoBatch stores a compact pre-image batch for blockNum and prunes any
// entry older than NomtUndoRetentionBlocks to bound disk overhead.
func PersistNomtUndoBatch(backupDB Storage, kind string, blockNum uint64, entries [][2][]byte) error {
	if backupDB == nil || blockNum == 0 || len(entries) == 0 {
		return nil
	}
	encoded := EncodeNomtUndoBatch(entries)
	if err := backupDB.Put(NomtUndoKey(kind, blockNum), encoded); err != nil {
		return err
	}
	if blockNum > NomtUndoRetentionBlocks {
		_ = backupDB.Delete(NomtUndoKey(kind, blockNum-NomtUndoRetentionBlocks))
	}
	return nil
}

// LoadNomtUndoBatch retrieves and decodes a stored pre-image batch for kind and blockNum.
func LoadNomtUndoBatch(backupDB Storage, kind string, blockNum uint64) ([][2][]byte, bool) {
	if backupDB == nil {
		return nil, false
	}
	raw, err := backupDB.Get(NomtUndoKey(kind, blockNum))
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	entries, err := DecodeNomtUndoBatch(raw)
	if err != nil {
		return nil, false
	}
	return entries, true
}

// DeleteNomtUndoForBlock removes both account and stake undo records for blockNum.
func DeleteNomtUndoForBlock(backupDB Storage, blockNum uint64) {
	if backupDB == nil {
		return
	}
	_ = backupDB.Delete(NomtUndoKey(NomtUndoKindAccount, blockNum))
	_ = backupDB.Delete(NomtUndoKey(NomtUndoKindStake, blockNum))
}
