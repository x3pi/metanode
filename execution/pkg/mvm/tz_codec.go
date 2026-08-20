package mvm

/*
#include "tzproto/mvm_tz_protocol.h"
*/
import "C"
import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// This file implements GĐ2 of note/tee_dual_mode_execution_plan.md: encode/
// decode between Go-native values and the wire format defined in
// pkg/mvm/tzproto/mvm_tz_protocol.h. Fixed-size command headers go through
// the real C struct types (via cgo) so a layout mismatch is a compile-time
// or immediate-runtime failure, not a silent bug — that's the whole point
// of exercising this instead of a hand-rolled Go mirror. Variable-length
// blob-stream fields (see mvm_tz_protocol.h's ExecuteResult response
// comment) are pure Go via blobWriter/blobReader below, since the blob
// stream is this protocol's own invention, not a mirror of any existing C
// struct.

// ───────────────────────── blob-stream primitives ─────────────────────────

type blobWriter struct{ buf []byte }

// writeBytes appends a length-prefixed record: [uint32 LE len][data].
func (w *blobWriter) writeBytes(b []byte) {
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(b)))
	w.buf = append(w.buf, lenBuf[:]...)
	w.buf = append(w.buf, b...)
}

// writeRaw appends bytes with no length prefix — for fields whose width is
// already fixed/known from the command header (addresses, uint256 values).
func (w *blobWriter) writeRaw(b []byte) {
	w.buf = append(w.buf, b...)
}

func (w *blobWriter) writeU32(v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	w.buf = append(w.buf, b[:]...)
}

type blobReader struct {
	buf []byte
	off int
}

func (r *blobReader) readBytes() ([]byte, error) {
	if r.off+4 > len(r.buf) {
		return nil, fmt.Errorf("blobReader.readBytes: %w (need 4 len-prefix bytes at offset %d, have %d)", io.ErrUnexpectedEOF, r.off, len(r.buf))
	}
	n := int(binary.LittleEndian.Uint32(r.buf[r.off : r.off+4]))
	r.off += 4
	return r.readRaw(n)
}

func (r *blobReader) readRaw(n int) ([]byte, error) {
	if n < 0 || r.off+n > len(r.buf) {
		return nil, fmt.Errorf("blobReader.readRaw: %w (need %d bytes at offset %d, have %d)", io.ErrUnexpectedEOF, n, r.off, len(r.buf))
	}
	out := make([]byte, n)
	copy(out, r.buf[r.off:r.off+n])
	r.off += n
	return out, nil
}

func (r *blobReader) readU32() (uint32, error) {
	b, err := r.readRaw(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

// ───────────────────── per-shape map encode/decode helpers ─────────────────

// writeAddrValueMap/readAddrValueMap: fixed-width [20-byte addr][32-byte
// value] entries, back to back, count already known from the caller
// (the command header's own *_count field) — no per-entry length prefix.
func writeAddrValueMap(w *blobWriter, m map[string][]byte) {
	for addrHex, val := range m {
		addr, _ := hex.DecodeString(addrHex)
		var a [20]byte
		copy(a[:], addr)
		w.writeRaw(a[:])
		var v [32]byte
		copy(v[:], val)
		w.writeRaw(v[:])
	}
}

func readAddrValueMap(r *blobReader, count int) (map[string][]byte, error) {
	m := make(map[string][]byte, count)
	for i := 0; i < count; i++ {
		addr, err := r.readRaw(20)
		if err != nil {
			return nil, err
		}
		val, err := r.readRaw(32)
		if err != nil {
			return nil, err
		}
		m[hex.EncodeToString(addr)] = val
	}
	return m, nil
}

// writeAddrBytesMap/readAddrBytesMap: [20-byte addr][uint32 len][len bytes].
func writeAddrBytesMap(w *blobWriter, m map[string][]byte) {
	for addrHex, data := range m {
		addr, _ := hex.DecodeString(addrHex)
		var a [20]byte
		copy(a[:], addr)
		w.writeRaw(a[:])
		w.writeBytes(data)
	}
}

func readAddrBytesMap(r *blobReader, count int) (map[string][]byte, error) {
	m := make(map[string][]byte, count)
	for i := 0; i < count; i++ {
		addr, err := r.readRaw(20)
		if err != nil {
			return nil, err
		}
		data, err := r.readBytes()
		if err != nil {
			return nil, err
		}
		m[hex.EncodeToString(addr)] = data
	}
	return m, nil
}

// writeStorageChange/readStorageChange: [20-byte addr][uint32 pair_count]
// [pair_count * (32-byte key + 32-byte value)].
func writeStorageChange(w *blobWriter, m map[string]map[string][]byte) {
	for addrHex, kv := range m {
		addr, _ := hex.DecodeString(addrHex)
		var a [20]byte
		copy(a[:], addr)
		w.writeRaw(a[:])
		w.writeU32(uint32(len(kv)))
		for keyHex, val := range kv {
			k, _ := hex.DecodeString(keyHex)
			var kb [32]byte
			copy(kb[:], k)
			w.writeRaw(kb[:])
			var vb [32]byte
			copy(vb[:], val)
			w.writeRaw(vb[:])
		}
	}
}

func readStorageChange(r *blobReader, count int) (map[string]map[string][]byte, error) {
	m := make(map[string]map[string][]byte, count)
	for i := 0; i < count; i++ {
		addr, err := r.readRaw(20)
		if err != nil {
			return nil, err
		}
		pairCount, err := r.readU32()
		if err != nil {
			return nil, err
		}
		kv := make(map[string][]byte, pairCount)
		for j := uint32(0); j < pairCount; j++ {
			k, err := r.readRaw(32)
			if err != nil {
				return nil, err
			}
			v, err := r.readRaw(32)
			if err != nil {
				return nil, err
			}
			kv[hex.EncodeToString(k)] = v
		}
		m[hex.EncodeToString(addr)] = kv
	}
	return m, nil
}

// writeStorageRead/readStorageRead: [20-byte addr][uint32 key_count]
// [key_count * 32-byte key].
func writeStorageRead(w *blobWriter, m map[string][][]byte) {
	for addrHex, keys := range m {
		addr, _ := hex.DecodeString(addrHex)
		var a [20]byte
		copy(a[:], addr)
		w.writeRaw(a[:])
		w.writeU32(uint32(len(keys)))
		for _, k := range keys {
			var kb [32]byte
			copy(kb[:], k)
			w.writeRaw(kb[:])
		}
	}
}

func readStorageRead(r *blobReader, count int) (map[string][][]byte, error) {
	m := make(map[string][][]byte, count)
	for i := 0; i < count; i++ {
		addr, err := r.readRaw(20)
		if err != nil {
			return nil, err
		}
		keyCount, err := r.readU32()
		if err != nil {
			return nil, err
		}
		keys := make([][]byte, keyCount)
		for j := uint32(0); j < keyCount; j++ {
			k, err := r.readRaw(32)
			if err != nil {
				return nil, err
			}
			keys[j] = k
		}
		m[hex.EncodeToString(addr)] = keys
	}
	return m, nil
}

// writeAccountType/readAccountType: [20-byte addr][1-byte type].
func writeAccountType(w *blobWriter, m map[string]uint8) {
	for addrHex, t := range m {
		addr, _ := hex.DecodeString(addrHex)
		var a [20]byte
		copy(a[:], addr)
		w.writeRaw(a[:])
		w.buf = append(w.buf, t)
	}
}

func readAccountType(r *blobReader, count int) (map[string]uint8, error) {
	m := make(map[string]uint8, count)
	for i := 0; i < count; i++ {
		addr, err := r.readRaw(20)
		if err != nil {
			return nil, err
		}
		t, err := r.readRaw(1)
		if err != nil {
			return nil, err
		}
		m[hex.EncodeToString(addr)] = t[0]
	}
	return m, nil
}

// encodeNativeLogsBlob/decodeNativeLogsBlob: byte-for-byte the same packed
// format C++ already uses for ExecuteResult.b_native_logs — repeated
// [4-byte LE flag][4-byte LE msg_len][msg bytes] records — see
// extractNativeLogs (helpers.go) and processResult()'s doc comment
// (mvm_linker.cpp). Reused here unchanged rather than reinvented.
func encodeNativeLogsBlob(entries []NativeLogEntry) []byte {
	var buf []byte
	for _, e := range entries {
		var flagBuf, lenBuf [4]byte
		binary.LittleEndian.PutUint32(flagBuf[:], uint32(e.Flag))
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(e.Message)))
		buf = append(buf, flagBuf[:]...)
		buf = append(buf, lenBuf[:]...)
		buf = append(buf, []byte(e.Message)...)
	}
	return buf
}

func decodeNativeLogsBlob(blob []byte) []NativeLogEntry {
	var entries []NativeLogEntry
	off := 0
	for off+8 <= len(blob) {
		flag := int32(binary.LittleEndian.Uint32(blob[off : off+4]))
		msgLen := int(binary.LittleEndian.Uint32(blob[off+4 : off+8]))
		off += 8
		if msgLen < 0 || off+msgLen > len(blob) {
			break
		}
		entries = append(entries, NativeLogEntry{Flag: flag, Message: string(blob[off : off+msgLen])})
		off += msgLen
	}
	return entries
}

// ───────────────────────── ExecuteResult codec ─────────────────────────

// encodeExecuteResult serializes a real *MVMExecuteResult (as returned by
// *MVMApi's Call/Execute/Deploy/... today) into the wire format shared by
// all 6 forward commands' responses. See mvm_tz_protocol.h's
// mvm_tz_execute_result_hdr_t doc comment for the full field/order
// contract this must stay in sync with.
func encodeExecuteResult(r *MVMExecuteResult) (header, blob []byte, err error) {
	if r == nil {
		return nil, nil, fmt.Errorf("mvm: encodeExecuteResult: nil result")
	}

	var hdr C.mvm_tz_execute_result_hdr_t
	hdr.status = C.uint8_t(r.Status)
	hdr.exception = C.uint8_t(r.Exception)
	if r.SimpleDbHash != nil {
		hdr.has_simple_db_hash = 1
	}
	hdr.gas_used = C.uint64_t(r.GasUsed)
	hdr.full_db_hash_count = C.uint32_t(len(r.MapFullDbHash))
	hdr.full_db_logs_count = C.uint32_t(len(r.MapFullDbLogs))
	hdr.add_balance_change_count = C.uint32_t(len(r.MapAddBalance))
	hdr.nonce_change_count = C.uint32_t(len(r.MapNonce))
	hdr.sub_balance_change_count = C.uint32_t(len(r.MapSubBalance))
	hdr.code_change_count = C.uint32_t(len(r.MapCodeChange))
	hdr.storage_change_count = C.uint32_t(len(r.MapStorageChange))
	hdr.storage_read_count = C.uint32_t(len(r.MapStorageRead))
	hdr.public_key_bls_count = C.uint32_t(len(r.MapPublicKeyBls))
	hdr.account_type_count = C.uint32_t(len(r.MapAccountType))
	hdr.new_device_key_count = C.uint32_t(len(r.MapNewDeviceKey))
	header = C.GoBytes(unsafe.Pointer(&hdr), C.int(C.sizeof_mvm_tz_execute_result_hdr_t))

	w := &blobWriter{}
	w.writeBytes([]byte(r.Exmsg))
	w.writeBytes(r.Return)
	writeAddrValueMap(w, r.MapFullDbHash)
	writeAddrBytesMap(w, r.MapFullDbLogs)
	writeAddrValueMap(w, r.MapAddBalance)
	writeAddrValueMap(w, r.MapNonce)
	writeAddrValueMap(w, r.MapSubBalance)
	writeAddrBytesMap(w, r.MapCodeChange)
	writeStorageChange(w, r.MapStorageChange)
	writeStorageRead(w, r.MapStorageRead)
	writeAddrBytesMap(w, r.MapPublicKeyBls)
	writeAccountType(w, r.MapAccountType)
	writeAddrBytesMap(w, r.MapNewDeviceKey)
	if r.SimpleDbHash != nil {
		var sb [32]byte
		copy(sb[:], r.SimpleDbHash)
		w.writeRaw(sb[:])
	}
	logsJSON, jerr := json.Marshal(r.JEventLogs.Logs)
	if jerr != nil {
		return nil, nil, fmt.Errorf("mvm: encodeExecuteResult: marshal event logs: %w", jerr)
	}
	w.writeBytes(logsJSON)
	w.writeBytes(encodeNativeLogsBlob(r.NativeLogs))
	return header, w.buf, nil
}

// decodeExecuteResult is encodeExecuteResult's exact inverse. MapCodeHash
// is NOT carried over the wire — it's re-derived from MapCodeChange here,
// matching extractCodeChange()'s mapCodeHash[address] =
// crypto.Keccak256(code) exactly (helpers.go), so no behavior changes for
// callers that read MapCodeHash.
func decodeExecuteResult(header, blob []byte) (*MVMExecuteResult, error) {
	if len(header) != int(C.sizeof_mvm_tz_execute_result_hdr_t) {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: bad header length %d, want %d", len(header), int(C.sizeof_mvm_tz_execute_result_hdr_t))
	}
	hdr := (*C.mvm_tz_execute_result_hdr_t)(unsafe.Pointer(&header[0]))

	r := &MVMExecuteResult{
		Status: pb.RECEIPT_STATUS(hdr.status),
		// pb.EXCEPTION's whole defined range is -1 (EXCEPTION_NONE, the
		// overwhelmingly common "success" value) through 15 -- comfortably
		// inside int8. hdr.exception is C.uint8_t (unsigned) purely because
		// that's the wire's byte layout (mvm_tz_protocol.h); converting it
		// straight to pb.EXCEPTION (int32-based) would zero-extend instead
		// of sign-extend, turning EXCEPTION_NONE=-1 into 255 on every single
		// successful tx. Found 2026-08-20 via cmd/tool/tz_replay_check's
		// real-block differential replay (note/tee_dual_mode_execution_plan.md
		// §9's "Giai đoạn 4") -- every ModeCgo-vs-ModeTrustzone receipt
		// otherwise matched byte-for-byte except this field. int8(...) first
		// reinterprets the raw byte as signed before widening, which is the
		// correct inverse of encodeExecuteResult's C.uint8_t(r.Exception)
		// truncation below -- no wire layout change, so this does NOT need
		// a TA rebuild/reflash to take effect on the Go side.
		Exception: pb.EXCEPTION(int8(hdr.exception)),
		GasUsed:   uint64(hdr.gas_used),
	}

	reader := &blobReader{buf: blob}
	var err error

	exmsgBytes, err := reader.readBytes()
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: exmsg: %w", err)
	}
	r.Exmsg = string(exmsgBytes)

	r.Return, err = reader.readBytes()
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: output: %w", err)
	}

	r.MapFullDbHash, err = readAddrValueMap(reader, int(hdr.full_db_hash_count))
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: full_db_hash: %w", err)
	}
	r.MapFullDbLogs, err = readAddrBytesMap(reader, int(hdr.full_db_logs_count))
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: full_db_logs: %w", err)
	}
	r.MapAddBalance, err = readAddrValueMap(reader, int(hdr.add_balance_change_count))
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: add_balance: %w", err)
	}
	r.MapNonce, err = readAddrValueMap(reader, int(hdr.nonce_change_count))
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: nonce: %w", err)
	}
	r.MapSubBalance, err = readAddrValueMap(reader, int(hdr.sub_balance_change_count))
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: sub_balance: %w", err)
	}
	r.MapCodeChange, err = readAddrBytesMap(reader, int(hdr.code_change_count))
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: code_change: %w", err)
	}
	r.MapCodeHash = make(map[string][]byte, len(r.MapCodeChange))
	for addr, code := range r.MapCodeChange {
		r.MapCodeHash[addr] = crypto.Keccak256(code)
	}
	r.MapStorageChange, err = readStorageChange(reader, int(hdr.storage_change_count))
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: storage_change: %w", err)
	}
	r.MapStorageRead, err = readStorageRead(reader, int(hdr.storage_read_count))
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: storage_read: %w", err)
	}

	// The next 3 fields are confirmed dead in cgo mode today (nothing in
	// pkg/mvm ever populates MapPublicKeyBls/MapAccountType/
	// MapNewDeviceKey — see mvm_tz_protocol.h's doc comment) — always nil,
	// never an empty map, in the real cgo-mode result. Match that exactly
	// (rather than always-allocate like the 8 fields above, which DO
	// always come back non-nil from extractExecuteResult even when empty)
	// so GĐ4's byte/value comparison between cgo and TZ mode doesn't see a
	// spurious nil-vs-empty-map mismatch.
	r.MapPublicKeyBls, err = readAddrBytesMap(reader, int(hdr.public_key_bls_count))
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: public_key_bls: %w", err)
	}
	if hdr.public_key_bls_count == 0 {
		r.MapPublicKeyBls = nil
	}
	r.MapAccountType, err = readAccountType(reader, int(hdr.account_type_count))
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: account_type: %w", err)
	}
	if hdr.account_type_count == 0 {
		r.MapAccountType = nil
	}
	r.MapNewDeviceKey, err = readAddrBytesMap(reader, int(hdr.new_device_key_count))
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: new_device_key: %w", err)
	}
	if hdr.new_device_key_count == 0 {
		r.MapNewDeviceKey = nil
	}

	if hdr.has_simple_db_hash != 0 {
		r.SimpleDbHash, err = reader.readRaw(32)
		if err != nil {
			return nil, fmt.Errorf("mvm: decodeExecuteResult: simple_db_hash: %w", err)
		}
	}

	logsJSON, err := reader.readBytes()
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: event_logs: %w", err)
	}
	if len(logsJSON) > 0 {
		if jerr := json.Unmarshal(logsJSON, &r.JEventLogs.Logs); jerr != nil {
			return nil, fmt.Errorf("mvm: decodeExecuteResult: unmarshal event logs: %w", jerr)
		}
	}

	nativeLogsBlob, err := reader.readBytes()
	if err != nil {
		return nil, fmt.Errorf("mvm: decodeExecuteResult: native_logs: %w", err)
	}
	r.NativeLogs = decodeNativeLogsBlob(nativeLogsBlob)

	return r, nil
}

// ───────────────────────── forward request codecs ─────────────────────────
//
// Each pair mirrors one mvm_tz_*_req_t struct from mvm_tz_protocol.h
// exactly. big.Int amounts are always encoded as 32-byte big-endian (same
// convention the C++ side already uses via mvm::to_big_endian).

func bigIntTo32(v *big.Int) [32]byte {
	var b [32]byte
	if v != nil {
		v.FillBytes(b[:])
	}
	return b
}

func encodeCallReq(
	bSender, bContractAddress, bInput []byte, amount *big.Int,
	gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber uint64,
	blockCoinbase, mvmId common.Address, readOnly bool, bTxHash []byte, relatedAddresses []common.Address,
	isDebug, isOffChain bool,
) (header, blob []byte) {
	var req C.mvm_tz_call_req_t
	amt := bigIntTo32(amount)
	copyToCBytes(req.amount[:], amt[:])
	req.gas_price = C.uint64_t(gasPrice)
	req.gas_limit = C.uint64_t(gasLimit)
	req.block_prevrandao = C.uint64_t(blockPrevrandao)
	req.block_gas_limit = C.uint64_t(blockGasLimit)
	req.block_time = C.uint64_t(blockTime)
	req.block_base_fee = C.uint64_t(blockBaseFee)
	req.block_number = C.uint64_t(blockNumber)
	copyToCBytes(req.block_coinbase[:], blockCoinbase[:])
	copyToCBytes(req.mvm_id[:], mvmId[:])
	req.read_only = boolToU8(readOnly)
	req.is_debug = boolToU8(isDebug)
	req.is_off_chain = boolToU8(isOffChain)
	req.related_addresses_count = C.uint32_t(len(relatedAddresses))
	header = C.GoBytes(unsafe.Pointer(&req), C.int(C.sizeof_mvm_tz_call_req_t))

	w := &blobWriter{}
	w.writeBytes(bSender)
	w.writeBytes(bContractAddress)
	w.writeBytes(bInput)
	w.writeBytes(bTxHash)
	for _, a := range relatedAddresses {
		w.writeBytes(a[:])
	}
	return header, w.buf
}

func decodeCallReq(header, blob []byte) (
	bSender, bContractAddress, bInput []byte, amount *big.Int,
	gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber uint64,
	blockCoinbase, mvmId common.Address, readOnly bool, bTxHash []byte, relatedAddresses []common.Address,
	isDebug, isOffChain bool, err error,
) {
	if len(header) != int(C.sizeof_mvm_tz_call_req_t) {
		err = fmt.Errorf("mvm: decodeCallReq: bad header length %d", len(header))
		return
	}
	req := (*C.mvm_tz_call_req_t)(unsafe.Pointer(&header[0]))
	amount = new(big.Int).SetBytes(cBytesToGo(req.amount[:]))
	gasPrice = uint64(req.gas_price)
	gasLimit = uint64(req.gas_limit)
	blockPrevrandao = uint64(req.block_prevrandao)
	blockGasLimit = uint64(req.block_gas_limit)
	blockTime = uint64(req.block_time)
	blockBaseFee = uint64(req.block_base_fee)
	blockNumber = uint64(req.block_number)
	blockCoinbase = common.BytesToAddress(cBytesToGo(req.block_coinbase[:]))
	mvmId = common.BytesToAddress(cBytesToGo(req.mvm_id[:]))
	readOnly = u8ToBool(req.read_only)
	isDebug = u8ToBool(req.is_debug)
	isOffChain = u8ToBool(req.is_off_chain)
	relatedCount := int(req.related_addresses_count)

	r := &blobReader{buf: blob}
	if bSender, err = r.readBytes(); err != nil {
		return
	}
	if bContractAddress, err = r.readBytes(); err != nil {
		return
	}
	if bInput, err = r.readBytes(); err != nil {
		return
	}
	if bTxHash, err = r.readBytes(); err != nil {
		return
	}
	relatedAddresses = make([]common.Address, relatedCount)
	for i := 0; i < relatedCount; i++ {
		var ab []byte
		if ab, err = r.readBytes(); err != nil {
			return
		}
		relatedAddresses[i] = common.BytesToAddress(ab)
	}
	return
}

func encodeExecuteReq(
	bSender, bContractAddress, bInput []byte, amount *big.Int,
	gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber uint64,
	blockCoinbase, mvmId common.Address, bTxHash []byte, relatedAddresses []common.Address,
	isDebug, isCache bool,
) (header, blob []byte) {
	var req C.mvm_tz_execute_req_t
	amt := bigIntTo32(amount)
	copyToCBytes(req.amount[:], amt[:])
	req.gas_price = C.uint64_t(gasPrice)
	req.gas_limit = C.uint64_t(gasLimit)
	req.block_prevrandao = C.uint64_t(blockPrevrandao)
	req.block_gas_limit = C.uint64_t(blockGasLimit)
	req.block_time = C.uint64_t(blockTime)
	req.block_base_fee = C.uint64_t(blockBaseFee)
	req.block_number = C.uint64_t(blockNumber)
	copyToCBytes(req.block_coinbase[:], blockCoinbase[:])
	copyToCBytes(req.mvm_id[:], mvmId[:])
	req.is_debug = boolToU8(isDebug)
	req.is_cache = boolToU8(isCache)
	req.related_addresses_count = C.uint32_t(len(relatedAddresses))
	header = C.GoBytes(unsafe.Pointer(&req), C.int(C.sizeof_mvm_tz_execute_req_t))

	w := &blobWriter{}
	w.writeBytes(bSender)
	w.writeBytes(bContractAddress)
	w.writeBytes(bInput)
	w.writeBytes(bTxHash)
	for _, a := range relatedAddresses {
		w.writeBytes(a[:])
	}
	return header, w.buf
}

func decodeExecuteReq(header, blob []byte) (
	bSender, bContractAddress, bInput []byte, amount *big.Int,
	gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber uint64,
	blockCoinbase, mvmId common.Address, bTxHash []byte, relatedAddresses []common.Address,
	isDebug, isCache bool, err error,
) {
	if len(header) != int(C.sizeof_mvm_tz_execute_req_t) {
		err = fmt.Errorf("mvm: decodeExecuteReq: bad header length %d", len(header))
		return
	}
	req := (*C.mvm_tz_execute_req_t)(unsafe.Pointer(&header[0]))
	amount = new(big.Int).SetBytes(cBytesToGo(req.amount[:]))
	gasPrice = uint64(req.gas_price)
	gasLimit = uint64(req.gas_limit)
	blockPrevrandao = uint64(req.block_prevrandao)
	blockGasLimit = uint64(req.block_gas_limit)
	blockTime = uint64(req.block_time)
	blockBaseFee = uint64(req.block_base_fee)
	blockNumber = uint64(req.block_number)
	blockCoinbase = common.BytesToAddress(cBytesToGo(req.block_coinbase[:]))
	mvmId = common.BytesToAddress(cBytesToGo(req.mvm_id[:]))
	isDebug = u8ToBool(req.is_debug)
	isCache = u8ToBool(req.is_cache)
	relatedCount := int(req.related_addresses_count)

	r := &blobReader{buf: blob}
	if bSender, err = r.readBytes(); err != nil {
		return
	}
	if bContractAddress, err = r.readBytes(); err != nil {
		return
	}
	if bInput, err = r.readBytes(); err != nil {
		return
	}
	if bTxHash, err = r.readBytes(); err != nil {
		return
	}
	relatedAddresses = make([]common.Address, relatedCount)
	for i := 0; i < relatedCount; i++ {
		var ab []byte
		if ab, err = r.readBytes(); err != nil {
			return
		}
		relatedAddresses[i] = common.BytesToAddress(ab)
	}
	return
}

func encodeDeployReq(
	bSender, bContractConstructor []byte, amount *big.Int,
	gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber uint64,
	blockCoinbase, mvmId common.Address, bTxHash []byte, isDebug, isCache, isOffChain bool,
) (header, blob []byte) {
	var req C.mvm_tz_deploy_req_t
	amt := bigIntTo32(amount)
	copyToCBytes(req.amount[:], amt[:])
	req.gas_price = C.uint64_t(gasPrice)
	req.gas_limit = C.uint64_t(gasLimit)
	req.block_prevrandao = C.uint64_t(blockPrevrandao)
	req.block_gas_limit = C.uint64_t(blockGasLimit)
	req.block_time = C.uint64_t(blockTime)
	req.block_base_fee = C.uint64_t(blockBaseFee)
	req.block_number = C.uint64_t(blockNumber)
	copyToCBytes(req.block_coinbase[:], blockCoinbase[:])
	copyToCBytes(req.mvm_id[:], mvmId[:])
	req.is_debug = boolToU8(isDebug)
	req.is_cache = boolToU8(isCache)
	req.is_off_chain = boolToU8(isOffChain)
	header = C.GoBytes(unsafe.Pointer(&req), C.int(C.sizeof_mvm_tz_deploy_req_t))

	w := &blobWriter{}
	w.writeBytes(bSender)
	w.writeBytes(bContractConstructor)
	w.writeBytes(bTxHash)
	return header, w.buf
}

func decodeDeployReq(header, blob []byte) (
	bSender, bContractConstructor []byte, amount *big.Int,
	gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber uint64,
	blockCoinbase, mvmId common.Address, bTxHash []byte, isDebug, isCache, isOffChain bool, err error,
) {
	if len(header) != int(C.sizeof_mvm_tz_deploy_req_t) {
		err = fmt.Errorf("mvm: decodeDeployReq: bad header length %d", len(header))
		return
	}
	req := (*C.mvm_tz_deploy_req_t)(unsafe.Pointer(&header[0]))
	amount = new(big.Int).SetBytes(cBytesToGo(req.amount[:]))
	gasPrice = uint64(req.gas_price)
	gasLimit = uint64(req.gas_limit)
	blockPrevrandao = uint64(req.block_prevrandao)
	blockGasLimit = uint64(req.block_gas_limit)
	blockTime = uint64(req.block_time)
	blockBaseFee = uint64(req.block_base_fee)
	blockNumber = uint64(req.block_number)
	blockCoinbase = common.BytesToAddress(cBytesToGo(req.block_coinbase[:]))
	mvmId = common.BytesToAddress(cBytesToGo(req.mvm_id[:]))
	isDebug = u8ToBool(req.is_debug)
	isCache = u8ToBool(req.is_cache)
	isOffChain = u8ToBool(req.is_off_chain)

	r := &blobReader{buf: blob}
	if bSender, err = r.readBytes(); err != nil {
		return
	}
	if bContractConstructor, err = r.readBytes(); err != nil {
		return
	}
	if bTxHash, err = r.readBytes(); err != nil {
		return
	}
	return
}

func encodeSendNativeReq(
	bSender, bContractAddress []byte, amount *big.Int,
	gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber uint64,
	blockCoinbase, mvmId common.Address, isCache bool,
) (header, blob []byte) {
	var req C.mvm_tz_send_native_req_t
	amt := bigIntTo32(amount)
	copyToCBytes(req.amount[:], amt[:])
	req.gas_price = C.uint64_t(gasPrice)
	req.gas_limit = C.uint64_t(gasLimit)
	req.block_prevrandao = C.uint64_t(blockPrevrandao)
	req.block_gas_limit = C.uint64_t(blockGasLimit)
	req.block_time = C.uint64_t(blockTime)
	req.block_base_fee = C.uint64_t(blockBaseFee)
	req.block_number = C.uint64_t(blockNumber)
	copyToCBytes(req.block_coinbase[:], blockCoinbase[:])
	copyToCBytes(req.mvm_id[:], mvmId[:])
	req.is_cache = boolToU8(isCache)
	header = C.GoBytes(unsafe.Pointer(&req), C.int(C.sizeof_mvm_tz_send_native_req_t))

	w := &blobWriter{}
	w.writeBytes(bSender)
	w.writeBytes(bContractAddress)
	return header, w.buf
}

func decodeSendNativeReq(header, blob []byte) (
	bSender, bContractAddress []byte, amount *big.Int,
	gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber uint64,
	blockCoinbase, mvmId common.Address, isCache bool, err error,
) {
	if len(header) != int(C.sizeof_mvm_tz_send_native_req_t) {
		err = fmt.Errorf("mvm: decodeSendNativeReq: bad header length %d", len(header))
		return
	}
	req := (*C.mvm_tz_send_native_req_t)(unsafe.Pointer(&header[0]))
	amount = new(big.Int).SetBytes(cBytesToGo(req.amount[:]))
	gasPrice = uint64(req.gas_price)
	gasLimit = uint64(req.gas_limit)
	blockPrevrandao = uint64(req.block_prevrandao)
	blockGasLimit = uint64(req.block_gas_limit)
	blockTime = uint64(req.block_time)
	blockBaseFee = uint64(req.block_base_fee)
	blockNumber = uint64(req.block_number)
	blockCoinbase = common.BytesToAddress(cBytesToGo(req.block_coinbase[:]))
	mvmId = common.BytesToAddress(cBytesToGo(req.mvm_id[:]))
	isCache = u8ToBool(req.is_cache)

	r := &blobReader{buf: blob}
	if bSender, err = r.readBytes(); err != nil {
		return
	}
	if bContractAddress, err = r.readBytes(); err != nil {
		return
	}
	return
}

func encodeProcessNativeMintBurnReq(
	bFrom, bTo []byte, amount *big.Int, operationType uint64,
	gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber uint64,
	blockCoinbase, mvmId common.Address, isCache bool,
) (header, blob []byte) {
	var req C.mvm_tz_process_native_mint_burn_req_t
	amt := bigIntTo32(amount)
	copyToCBytes(req.amount[:], amt[:])
	req.operation_type = C.uint64_t(operationType)
	req.gas_price = C.uint64_t(gasPrice)
	req.gas_limit = C.uint64_t(gasLimit)
	req.block_prevrandao = C.uint64_t(blockPrevrandao)
	req.block_gas_limit = C.uint64_t(blockGasLimit)
	req.block_time = C.uint64_t(blockTime)
	req.block_base_fee = C.uint64_t(blockBaseFee)
	req.block_number = C.uint64_t(blockNumber)
	copyToCBytes(req.block_coinbase[:], blockCoinbase[:])
	copyToCBytes(req.mvm_id[:], mvmId[:])
	req.is_cache = boolToU8(isCache)
	header = C.GoBytes(unsafe.Pointer(&req), C.int(C.sizeof_mvm_tz_process_native_mint_burn_req_t))

	w := &blobWriter{}
	w.writeBytes(bFrom)
	w.writeBytes(bTo)
	return header, w.buf
}

func decodeProcessNativeMintBurnReq(header, blob []byte) (
	bFrom, bTo []byte, amount *big.Int, operationType uint64,
	gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber uint64,
	blockCoinbase, mvmId common.Address, isCache bool, err error,
) {
	if len(header) != int(C.sizeof_mvm_tz_process_native_mint_burn_req_t) {
		err = fmt.Errorf("mvm: decodeProcessNativeMintBurnReq: bad header length %d", len(header))
		return
	}
	req := (*C.mvm_tz_process_native_mint_burn_req_t)(unsafe.Pointer(&header[0]))
	amount = new(big.Int).SetBytes(cBytesToGo(req.amount[:]))
	operationType = uint64(req.operation_type)
	gasPrice = uint64(req.gas_price)
	gasLimit = uint64(req.gas_limit)
	blockPrevrandao = uint64(req.block_prevrandao)
	blockGasLimit = uint64(req.block_gas_limit)
	blockTime = uint64(req.block_time)
	blockBaseFee = uint64(req.block_base_fee)
	blockNumber = uint64(req.block_number)
	blockCoinbase = common.BytesToAddress(cBytesToGo(req.block_coinbase[:]))
	mvmId = common.BytesToAddress(cBytesToGo(req.mvm_id[:]))
	isCache = u8ToBool(req.is_cache)

	r := &blobReader{buf: blob}
	if bFrom, err = r.readBytes(); err != nil {
		return
	}
	if bTo, err = r.readBytes(); err != nil {
		return
	}
	return
}

func encodeNoncePlusOneReq(
	bSender []byte,
	gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber uint64,
	blockCoinbase, mvmId common.Address, isCache bool,
) (header, blob []byte) {
	var req C.mvm_tz_nonce_plus_one_req_t
	req.gas_price = C.uint64_t(gasPrice)
	req.gas_limit = C.uint64_t(gasLimit)
	req.block_prevrandao = C.uint64_t(blockPrevrandao)
	req.block_gas_limit = C.uint64_t(blockGasLimit)
	req.block_time = C.uint64_t(blockTime)
	req.block_base_fee = C.uint64_t(blockBaseFee)
	req.block_number = C.uint64_t(blockNumber)
	copyToCBytes(req.block_coinbase[:], blockCoinbase[:])
	copyToCBytes(req.mvm_id[:], mvmId[:])
	req.is_cache = boolToU8(isCache)
	header = C.GoBytes(unsafe.Pointer(&req), C.int(C.sizeof_mvm_tz_nonce_plus_one_req_t))

	w := &blobWriter{}
	w.writeBytes(bSender)
	return header, w.buf
}

func decodeNoncePlusOneReq(header, blob []byte) (
	bSender []byte,
	gasPrice, gasLimit, blockPrevrandao, blockGasLimit, blockTime, blockBaseFee, blockNumber uint64,
	blockCoinbase, mvmId common.Address, isCache bool, err error,
) {
	if len(header) != int(C.sizeof_mvm_tz_nonce_plus_one_req_t) {
		err = fmt.Errorf("mvm: decodeNoncePlusOneReq: bad header length %d", len(header))
		return
	}
	req := (*C.mvm_tz_nonce_plus_one_req_t)(unsafe.Pointer(&header[0]))
	gasPrice = uint64(req.gas_price)
	gasLimit = uint64(req.gas_limit)
	blockPrevrandao = uint64(req.block_prevrandao)
	blockGasLimit = uint64(req.block_gas_limit)
	blockTime = uint64(req.block_time)
	blockBaseFee = uint64(req.block_base_fee)
	blockNumber = uint64(req.block_number)
	blockCoinbase = common.BytesToAddress(cBytesToGo(req.block_coinbase[:]))
	mvmId = common.BytesToAddress(cBytesToGo(req.mvm_id[:]))
	isCache = u8ToBool(req.is_cache)

	r := &blobReader{buf: blob}
	if bSender, err = r.readBytes(); err != nil {
		return
	}
	return
}

// ───── The 6 live reverse-callback commands (plan §2.1 / GĐ3, 2026-08-16) ─────
//
// GlobalStateGet/GetStorageValue/Extension{CallGetApi,ExtractJsonField,Blst,
// GetOrCreateSimpleDb} — mirror today's cgo `//export` functions
// (mvm_api.go's GlobalStateGet/GetStorageValue, extension.go's Extension*)
// exactly, field-for-field, per mvm_tz_protocol.h's struct comments. Unlike
// the 6 forward commands, these are NOT yet wired into tz_loopback_engine.go
// — GĐ2's loopback calls straight into the real *MVMApi in-process, which
// still uses the existing direct cgo callbacks (see tz_loopback_engine.go's
// doc comment); a real TA (GĐ3) is what will actually need these to leave
// the process, so codec-only for now, unit-tested via round-trip below.
// ClearProcessingPointers is deliberately NOT here — it's dead code kept
// only so the C++ side still links (my_global_state.cpp calls it, but
// today's Go implementation is an empty no-op — see mvm_api.go's own comment
// there); a real TA can give it a local no-op C stub without ever crossing
// the wire.

func encodeGlobalStateGetReq(mvmId, address common.Address) (header []byte) {
	var req C.mvm_tz_global_state_get_req_t
	copyToCBytes(req.mvm_id[:], mvmId[:])
	copyToCBytes(req.address[:], address[:])
	return C.GoBytes(unsafe.Pointer(&req), C.int(C.sizeof_mvm_tz_global_state_get_req_t))
}

func decodeGlobalStateGetReq(header []byte) (mvmId, address common.Address, err error) {
	if len(header) != int(C.sizeof_mvm_tz_global_state_get_req_t) {
		err = fmt.Errorf("mvm: decodeGlobalStateGetReq: bad header length %d", len(header))
		return
	}
	req := (*C.mvm_tz_global_state_get_req_t)(unsafe.Pointer(&header[0]))
	mvmId = common.BytesToAddress(cBytesToGo(req.mvm_id[:]))
	address = common.BytesToAddress(cBytesToGo(req.address[:]))
	return
}

// encodeGlobalStateGetResp/decodeGlobalStateGetResp: status semantics match
// GlobalStateGet_return.status exactly (my_global_state.cpp) — 0=not found,
// 1=found (blob carries balance/nonce/code), 3=Block-STM suspend. balance/
// nonce/code are only meaningful (and only present in blob) when status==1.
func encodeGlobalStateGetResp(status int32, balance, nonce, code []byte) (header, blob []byte) {
	var hdr C.mvm_tz_global_state_get_resp_t
	hdr.status = C.int32_t(status)
	header = C.GoBytes(unsafe.Pointer(&hdr), C.int(C.sizeof_mvm_tz_global_state_get_resp_t))
	if status != 1 {
		return header, nil
	}
	w := &blobWriter{}
	writeFixed32(w, balance)
	writeFixed32(w, nonce)
	w.writeBytes(code)
	return header, w.buf
}

func decodeGlobalStateGetResp(header, blob []byte) (status int32, balance, nonce, code []byte, err error) {
	if len(header) != int(C.sizeof_mvm_tz_global_state_get_resp_t) {
		err = fmt.Errorf("mvm: decodeGlobalStateGetResp: bad header length %d", len(header))
		return
	}
	hdr := (*C.mvm_tz_global_state_get_resp_t)(unsafe.Pointer(&header[0]))
	status = int32(hdr.status)
	if status != 1 {
		return
	}
	r := &blobReader{buf: blob}
	if balance, err = r.readRaw(32); err != nil {
		err = fmt.Errorf("mvm: decodeGlobalStateGetResp: balance: %w", err)
		return
	}
	if nonce, err = r.readRaw(32); err != nil {
		err = fmt.Errorf("mvm: decodeGlobalStateGetResp: nonce: %w", err)
		return
	}
	if code, err = r.readBytes(); err != nil {
		err = fmt.Errorf("mvm: decodeGlobalStateGetResp: code: %w", err)
		return
	}
	return
}

func encodeGetStorageValueReq(mvmId, address common.Address, key []byte) (header []byte) {
	var req C.mvm_tz_get_storage_value_req_t
	copyToCBytes(req.mvm_id[:], mvmId[:])
	copyToCBytes(req.address[:], address[:])
	copyToCBytes(req.key[:], key)
	return C.GoBytes(unsafe.Pointer(&req), C.int(C.sizeof_mvm_tz_get_storage_value_req_t))
}

func decodeGetStorageValueReq(header []byte) (mvmId, address common.Address, key []byte, err error) {
	if len(header) != int(C.sizeof_mvm_tz_get_storage_value_req_t) {
		err = fmt.Errorf("mvm: decodeGetStorageValueReq: bad header length %d", len(header))
		return
	}
	req := (*C.mvm_tz_get_storage_value_req_t)(unsafe.Pointer(&header[0]))
	mvmId = common.BytesToAddress(cBytesToGo(req.mvm_id[:]))
	address = common.BytesToAddress(cBytesToGo(req.address[:]))
	key = cBytesToGo(req.key[:])
	return
}

// encodeGetStorageValueResp/decodeGetStorageValueResp: status matches
// StorageStatus (mvm_linker.hpp) — 0=STORAGE_SUCCESS (blob carries the
// 32-byte value), 1=STORAGE_NOT_FOUND, 2=STORAGE_SUSPEND.
func encodeGetStorageValueResp(status int32, value []byte) (header, blob []byte) {
	var hdr C.mvm_tz_get_storage_value_resp_t
	hdr.status = C.int32_t(status)
	header = C.GoBytes(unsafe.Pointer(&hdr), C.int(C.sizeof_mvm_tz_get_storage_value_resp_t))
	if status != 0 {
		return header, nil
	}
	w := &blobWriter{}
	writeFixed32(w, value)
	return header, w.buf
}

func decodeGetStorageValueResp(header, blob []byte) (status int32, value []byte, err error) {
	if len(header) != int(C.sizeof_mvm_tz_get_storage_value_resp_t) {
		err = fmt.Errorf("mvm: decodeGetStorageValueResp: bad header length %d", len(header))
		return
	}
	hdr := (*C.mvm_tz_get_storage_value_resp_t)(unsafe.Pointer(&header[0]))
	status = int32(hdr.status)
	if status != 0 {
		return
	}
	r := &blobReader{buf: blob}
	if value, err = r.readRaw(32); err != nil {
		err = fmt.Errorf("mvm: decodeGetStorageValueResp: value: %w", err)
	}
	return
}

// encodeExtensionBytesReq/decodeExtensionBytesReq and
// encodeExtensionBytesResp/decodeExtensionBytesResp: the shared bytes-in/
// bytes-out shape for MVM_TZ_RCMD_EXTENSION_CALL_GET_API,
// MVM_TZ_RCMD_EXTENSION_EXTRACT_JSON_FIELD and MVM_TZ_RCMD_EXTENSION_BLST —
// no fixed header, per mvm_tz_protocol.h's comment ("No fixed header beyond
// the blob stream itself"), so these are trivial identity passthroughs: the
// tzChannel's blob_len already frames the payload exactly, no extra
// length-prefix needed on top. An empty response blob is the
// Extension_return{nullptr,0} failure case (e.g. ExtensionCallGetApi's HTTP
// error path) — a real 0-length success payload is not distinguishable from
// that today, matching the existing cgo behavior's own ambiguity, not a new
// limitation introduced here.
func encodeExtensionBytesReq(input []byte) (blob []byte)   { return input }
func decodeExtensionBytesReq(blob []byte) (input []byte)   { return blob }
func encodeExtensionBytesResp(output []byte) (blob []byte) { return output }
func decodeExtensionBytesResp(blob []byte) (output []byte) { return blob }

func encodeGetOrCreateSimpleDbReq(address, mvmId common.Address, input []byte) (header, blob []byte) {
	var hdr C.mvm_tz_get_or_create_simple_db_req_t
	copyToCBytes(hdr.address[:], address[:])
	copyToCBytes(hdr.mvm_id[:], mvmId[:])
	header = C.GoBytes(unsafe.Pointer(&hdr), C.int(C.sizeof_mvm_tz_get_or_create_simple_db_req_t))
	return header, input
}

func decodeGetOrCreateSimpleDbReq(header, blob []byte) (address, mvmId common.Address, input []byte, err error) {
	if len(header) != int(C.sizeof_mvm_tz_get_or_create_simple_db_req_t) {
		err = fmt.Errorf("mvm: decodeGetOrCreateSimpleDbReq: bad header length %d", len(header))
		return
	}
	hdr := (*C.mvm_tz_get_or_create_simple_db_req_t)(unsafe.Pointer(&header[0]))
	address = common.BytesToAddress(cBytesToGo(hdr.address[:]))
	mvmId = common.BytesToAddress(cBytesToGo(hdr.mvm_id[:]))
	input = blob
	return
}

// Response reuses the same bytes-out shape as encodeExtensionBytesResp — no
// header, per mvm_tz_protocol.h's comment ("Response blob: [0] output
// bytes").
func encodeGetOrCreateSimpleDbResp(output []byte) (blob []byte) { return output }
func decodeGetOrCreateSimpleDbResp(blob []byte) (output []byte) { return blob }

// ───── MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS (plan §5b, 2026-08-16) ─────
//
// TA-initiated, lazy, per-address pull of previously-saved full_db_logs —
// the TZ-mode counterpart of what cgo mode already does via
// mvm.CallReplayFullDbLogs during node sync (executor/
// unix_socket_handler_sync.go:1039), just reached over the reverse-callback
// channel instead of a direct Go call. Request carries only the address
// (mvm_tz_get_latest_full_db_logs_req_t); response reuses
// mvm_tz_replay_full_db_logs_req_t's shape (entry_count + blob of
// [addr][len][log bytes]) — the same shape writeAddrBytesMap/
// readAddrBytesMap already produce for full_db_logs elsewhere in this file,
// so no new blob-shape helper is needed, only the two header (de)serializers
// below.

func encodeGetLatestFullDbLogsReq(address common.Address) (header []byte) {
	var req C.mvm_tz_get_latest_full_db_logs_req_t
	copyToCBytes(req.address[:], address[:])
	return C.GoBytes(unsafe.Pointer(&req), C.int(C.sizeof_mvm_tz_get_latest_full_db_logs_req_t))
}

func decodeGetLatestFullDbLogsReq(header []byte) (address common.Address, err error) {
	if len(header) != int(C.sizeof_mvm_tz_get_latest_full_db_logs_req_t) {
		err = fmt.Errorf("mvm: decodeGetLatestFullDbLogsReq: bad header length %d", len(header))
		return
	}
	req := (*C.mvm_tz_get_latest_full_db_logs_req_t)(unsafe.Pointer(&header[0]))
	address = common.BytesToAddress(cBytesToGo(req.address[:]))
	return
}

// encodeReplayFullDbLogsResp/decodeReplayFullDbLogsResp: logs is 0 or 1
// entries for the lazy single-address pull (MVM_TZ_RCMD_
// GET_LATEST_FULL_DB_LOGS's response), but the wire shape itself is
// generic — it doubles as MVM_TZ_CMD_REPLAY_FULL_DB_LOGS's forward-command
// payload (Host proactively pushing N entries; not yet wired into the Go
// codec/loopback as of 2026-08-16, tracked separately) — hence "Resp" in
// the Go name here but "_req_t" in the shared C struct's name.
func encodeReplayFullDbLogsResp(logs map[string][]byte) (header, blob []byte) {
	var hdr C.mvm_tz_replay_full_db_logs_req_t
	hdr.entry_count = C.uint32_t(len(logs))
	header = C.GoBytes(unsafe.Pointer(&hdr), C.int(C.sizeof_mvm_tz_replay_full_db_logs_req_t))

	w := &blobWriter{}
	writeAddrBytesMap(w, logs)
	return header, w.buf
}

func decodeReplayFullDbLogsResp(header, blob []byte) (logs map[string][]byte, err error) {
	if len(header) != int(C.sizeof_mvm_tz_replay_full_db_logs_req_t) {
		err = fmt.Errorf("mvm: decodeReplayFullDbLogsResp: bad header length %d", len(header))
		return
	}
	hdr := (*C.mvm_tz_replay_full_db_logs_req_t)(unsafe.Pointer(&header[0]))
	r := &blobReader{buf: blob}
	logs, err = readAddrBytesMap(r, int(hdr.entry_count))
	return
}

// ───────────────────────── small shared helpers ─────────────────────────

func boolToU8(b bool) C.uint8_t {
	if b {
		return 1
	}
	return 0
}

func u8ToBool(v C.uint8_t) bool { return v != 0 }

// cBytesToGo/copyToCBytes: C.uint8_t is a distinct Go type from byte/uint8
// (cgo does not alias stdint typedefs to Go's built-in numeric types), so
// a plain copy()/slice-conversion between []C.uint8_t and []byte doesn't
// type-check — these two helpers are the explicit bridge, used everywhere
// a fixed-size C array field (address, uint256 amount) is set from or read
// into a Go []byte.
func cBytesToGo(src []C.uint8_t) []byte {
	out := make([]byte, len(src))
	for i, v := range src {
		out[i] = byte(v)
	}
	return out
}

func copyToCBytes(dst []C.uint8_t, src []byte) {
	n := len(dst)
	if len(src) < n {
		n = len(src)
	}
	for i := 0; i < n; i++ {
		dst[i] = C.uint8_t(src[i])
	}
}

// writeFixed32 zero-pads or truncates to 32 bytes — the same defensive
// posture common.BytesToAddress already uses, so a caller passing a
// shorter slice (shouldn't happen, but mirrors existing leniency
// elsewhere in pkg/mvm) doesn't panic. Only used for the reverse-call
// response fields (balance/nonce/storage value) that are genuinely raw
// fixed-width per the TA's own wire format — NOT for the 6 forward
// command request blobs (those are length-prefixed via writeBytes, see
// plan §9's "Giai đoạn 3b" framing-bug fix, 2026-08-20: a sibling
// writeFixed20 used to exist here for exactly that purpose and was
// removed once it lost its last call site).
func writeFixed32(w *blobWriter, b []byte) {
	var a [32]byte
	copy(a[:], b)
	w.writeRaw(a[:])
}
