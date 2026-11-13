package storage

import (
	"sort"
	"strings"
)

// Address-indexed store for the most recently persisted full_db_logs bytes
// per address — see note/tee_dual_mode_execution_plan.md §5b (2026-08-16).
//
// BackUpDb (batchstore.go) already persists FullDbLogs on every block, but
// keyed by block number ("block_data_topic-<N>") — fine for sequential
// replay during node sync, but not for a point lookup by address, which is
// exactly what a TrustZone TA needs after a restart (its in-memory-only
// State/Xapian singleton starts empty every session, unlike cgo mode's
// long-lived process): MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS asks Host "do
// you have the latest full_db_logs for address X", and Host must answer
// without scanning block history. This is a small, additive last-write-wins
// index alongside the existing block-indexed backup, not a replacement for
// it — written from the same per-block pass (see block_processor_commit.go)
// so there is only ever one place that produces full_db_logs bytes.
//
// This is a best-effort cache, not a source of truth: if a lookup misses or
// errors, the caller (eventually, GĐ3's real reverse-callback dispatch —
// not wired yet as of 2026-08-16, see plan §5b's "việc còn lại") has no
// worse an answer than "nothing saved for this address yet", which is
// exactly the state a fresh TA session already assumes for every address
// until GlobalStateGet/Xapian populates it. That's why GetLatestFullDbLogsForAddress
// collapses every failure mode (missing key, backend error) into a single
// ok=false, rather than distinguishing them — nothing downstream needs to
// react differently to "not found" vs. "storage hiccup" here.

const fullDbLogsLatestKeyPrefix = "full_db_logs_latest-"

// NormalizeAddressHexKey canonicalizes an Ethereum address string (with or
// without "0x"/"0X" prefix, EIP-55 mixed-case or lowercase) to lowercase hex
// without prefix so writers (ExportAllXapianLogs / MapFullDbLogs) and readers
// (GetLatestFullDbLogsForAddress) always compute the exact same storage key.
func NormalizeAddressHexKey(addressHex string) string {
	s := strings.TrimSpace(addressHex)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		s = s[2:]
	}
	return strings.ToLower(s)
}

// FullDbLogsLatestKey returns the storage key under which the most
// recently persisted full_db_logs bytes for addressHex (normalized to
// lowercase hex without 0x prefix) are kept. Shared by the writer
// (block_processor_commit.go / unix_socket_handler_sync.go) and the reader
// (GetLatestFullDbLogsForAddress) so the two can never drift apart.
func FullDbLogsLatestKey(addressHex string) []byte {
	return []byte(fullDbLogsLatestKeyPrefix + NormalizeAddressHexKey(addressHex))
}

// PersistLatestFullDbLogsBatch merges serialized ComprehensiveLog frames per
// normalized contract address across all maps in fullDbLogs in deterministic
// order and writes each address's combined payload via PutLatestFullDbLogsForAddress.
func PersistLatestFullDbLogsBatch(db Storage, fullDbLogs []map[string][]byte) error {
	if db == nil || len(fullDbLogs) == 0 {
		return nil
	}
	mergedByAddr := make(map[string][]byte)
	for _, logMap := range fullDbLogs {
		if len(logMap) == 0 {
			continue
		}
		rawKeys := make([]string, 0, len(logMap))
		for k := range logMap {
			rawKeys = append(rawKeys, k)
		}
		sort.Strings(rawKeys)
		for _, rawKey := range rawKeys {
			logBytes := logMap[rawKey]
			if len(logBytes) == 0 {
				continue
			}
			normAddr := NormalizeAddressHexKey(rawKey)
			if existing, exists := mergedByAddr[normAddr]; exists {
				mergedByAddr[normAddr] = append(existing, logBytes...)
			} else {
				copied := make([]byte, len(logBytes))
				copy(copied, logBytes)
				mergedByAddr[normAddr] = copied
			}
		}
	}
	if len(mergedByAddr) == 0 {
		return nil
	}
	normAddrs := make([]string, 0, len(mergedByAddr))
	for addr := range mergedByAddr {
		normAddrs = append(normAddrs, addr)
	}
	sort.Strings(normAddrs)
	var firstErr error
	for _, addr := range normAddrs {
		if err := PutLatestFullDbLogsForAddress(db, addr, mergedByAddr[addr]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// PutLatestFullDbLogsForAddress records logBytes as the latest known
// full_db_logs payload for addressHex. Called once per (address, block)
// pair from the same pass that already writes the block-indexed BackUpDb
// entry — see block_processor_commit.go.
func PutLatestFullDbLogsForAddress(db Storage, addressHex string, logBytes []byte) error {
	return db.Put(FullDbLogsLatestKey(addressHex), logBytes)
}

// GetLatestFullDbLogsForAddress looks up the most recently persisted
// full_db_logs bytes for addressHex. ok=false means "nothing saved" —
// covers both a genuine miss (this address has never touched Xapian/State)
// and any backend error (see doc comment above for why those are not
// distinguished here).
func GetLatestFullDbLogsForAddress(db Storage, addressHex string) (logBytes []byte, ok bool) {
	v, err := db.Get(FullDbLogsLatestKey(addressHex))
	if err != nil || len(v) == 0 {
		return nil, false
	}
	return v, true
}
