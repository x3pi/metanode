package mvm

/*
#cgo CFLAGS: -w -O3
#cgo CXXFLAGS: -std=c++17 -w -O3
// -march=native/-mtune=native only make sense when the compiler runs on the
// same machine it targets -- meaningless (and rejected outright by gcc/g++)
// under a cross toolchain like aarch64-linux-gnu-gcc. Mirrors the same
// CMAKE_CROSSCOMPILING guard already in c_mvm/CMakeLists.txt and
// linker/CMakeLists.txt (added 2026-08-16 for the chcore/musl TA target) --
// this is the Go-side cgo equivalent, added 2026-08-21 while cross-compiling
// pkg/mvm for the real board (aarch64-linux-gnu, NOT the musl/chcore TA
// target -- see note/tee_dual_mode_execution_plan.md's cross-compile
// section). Default (no explicit GOARCH, i.e. the host's own arch) still
// gets the amd64 line on this development machine, unchanged from before.
#cgo linux,amd64 CFLAGS: -march=native -mtune=native
#cgo linux,arm64 CFLAGS: -march=armv8-a -mtune=generic
#cgo linux,amd64 CXXFLAGS: -march=native -mtune=native
#cgo linux,arm64 CXXFLAGS: -march=armv8-a -mtune=generic
#cgo LDFLAGS: -lgmp -lmpfr -lm -ltbb -lxapian -L./linker/build/lib/static -lleveldb -lmvm_linker -L./c_mvm/build/lib/static -lmvm -lstdc++ -luuid
#cgo CPPFLAGS: -I./linker/build/include
#include "mvm_linker.hpp"
#include <stdlib.h>
#include <math.h>
#include <mpfr.h>
#include <string.h>

typedef struct {
    unsigned char *data_p;
    int data_size;
	bool success;
} Value_return;

extern void clear_xapian_tx_buffer(unsigned char *b_tx_hash);
extern void commit_xapian_tx_buffer(unsigned char *b_tx_hash);
extern void clear_xapian_tx_buffer_batch(unsigned char *b_tx_hashes, int count);
extern void commit_xapian_tx_buffer_batch(unsigned char *b_tx_hashes, int count);
extern void MVM_commitAllXapian();
*/
import "C"
import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/mvcc"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types"
)

var (
	apiInstances          sync.Map
	protectedApiInstances sync.Map
	apiInstanceCount      atomic.Int32
	offChainCounter       uint64
	ethCallSemaphore      = make(chan struct{}, 200)
)

type AccountStateDB interface {
	AccountState(address common.Address) (types.AccountState, error)
	InjectLoadedAccount(types.AccountState)
	PublicSetDirtyAccountState(as types.AccountState)
}

type SmartContractDB interface {
	Code(address common.Address) []byte
	StorageValue(address common.Address, key []byte, customRoot ...*common.Hash) ([]byte, bool)
}

func ProtectMVMApi(mvmId common.Address) {
	protectedApiInstances.LoadOrStore(mvmId, struct{}{})
	logger.Debug("Protected MVMApi instance:", mvmId.Hex())
}

func UnprotectMVMApi(mvmId common.Address) {
	protectedApiInstances.Delete(mvmId)
	logger.Debug("Unprotected MVMApi instance:", mvmId.Hex())
}

func ClearAllProtectedMVMApi() {
	logger.Info("Clearing all protected MVMApi instance markers...")
	count := 0
	protectedApiInstances.Range(func(key, value interface{}) bool {
		protectedApiInstances.Delete(key)
		count++
		return true
	})
	logger.Info("Finished clearing protection markers. Count:", count)
}

// Struct MVMApi đã được dọn dẹp, không còn các thành phần quản lý bộ nhớ C.
type MVMApi struct {
	key                     common.Address
	smartContractDb         SmartContractDB
	accountStateDb          AccountStateDB
	currentRelatedAddresses sync.Map
	extendedMode            bool
	createdAt               time.Time
	rs                      *MVMExecuteResult

	// Cross-chain precompile context (address 263)
	// Set trước khi execute contract call xuyên chain, clear sau khi xong.
	crossChainSender   common.Address // pkt.Sender (user gốc từ chain nguồn)
	crossChainSourceId uint64         // pkt.SourceNationId
	crossChainActive   bool           // có đang trong cross-chain call không

	// EIP-4844 context (BLOBHASH / BLOBBASEFEE opcodes). Set once per tx
	// execution alongside SetRelatedAddresses — see VmProcessor.ExecuteTransactionWithMvmId.
	// blobVersionedHashes is empty for any non-blob tx; blobBaseFee is always
	// set from the current block regardless of tx type, since BLOBBASEFEE is
	// valid to call in any Cancun+ context.
	blobVersionedHashes [][]byte
	blobBaseFee         *uint256.Int
}

func CallReplayFullDbLogs(logs map[string][]byte) int {
	if len(logs) == 0 {
		return 1
	}
	fmt.Printf("[Go CallReplay] Chuẩn bị %d entry log từ map để gọi C++ ReplayFullDbLogs...\n", len(logs))
	cEntries := make([]C.LogReplayEntryC, 0, len(logs))
	tempAllocs := make([]unsafe.Pointer, 0, len(logs)*2)
	defer func() {
		fmt.Printf("[Go CallReplay] Giải phóng %d vùng nhớ C tạm thời đã cấp phát.\n", len(tempAllocs))
		for _, ptr := range tempAllocs {
			C.free(ptr)
		}
	}()
	
	// Sắp xếp các địa chỉ (keys) để đảm bảo tính tuần tự (determinism)
	var addrs []string
	for addrHex := range logs {
		addrs = append(addrs, addrHex)
	}
	sort.Strings(addrs)

	for _, addrHex := range addrs {
		logData := logs[addrHex]
		fmt.Printf("  - Xử lý log cho địa chỉ hex: %s\n", addrHex)
		processedAddrHex := strings.TrimPrefix(addrHex, "0x")
		if len(processedAddrHex)%2 != 0 {
			processedAddrHex = "0" + processedAddrHex
		}
		if len(processedAddrHex) != 40 {
			fmt.Printf("    - LỖI: Độ dài địa chỉ hex không hợp lệ (%d chars) cho '%s'. Bỏ qua.\n", len(processedAddrHex), addrHex)
			continue
		}
		addrBytes, err := hex.DecodeString(processedAddrHex)
		if err != nil {
			fmt.Printf("    - LỖI: Không thể decode địa chỉ hex '%s': %v. Bỏ qua.\n", addrHex, err)
			continue
		}
		if len(logData) == 0 {
			fmt.Printf("    - CẢNH BÁO: Dữ liệu log rỗng cho địa chỉ '%s'. Bỏ qua.\n", addrHex)
			continue
		}
		cAddrDataPtr := C.malloc(20)
		if cAddrDataPtr == nil {
			fmt.Printf("    - LỖI: Không thể cấp phát bộ nhớ C cho địa chỉ '%s'. Dừng xử lý.\n", addrHex)
			return 0
		}
		tempAllocs = append(tempAllocs, cAddrDataPtr)
		C.memcpy(cAddrDataPtr, unsafe.Pointer(&addrBytes[0]), 20)
		logDataLen := len(logData)
		cLogDataPtr := C.malloc(C.size_t(logDataLen))
		if cLogDataPtr == nil {
			fmt.Printf("    - LỖI: Không thể cấp phát bộ nhớ C cho dữ liệu log (len %d) của địa chỉ '%s'. Dừng xử lý.\n", logDataLen, addrHex)
			return 0
		}
		tempAllocs = append(tempAllocs, cLogDataPtr)
		C.memcpy(cLogDataPtr, unsafe.Pointer(&logData[0]), C.size_t(logDataLen))
		entry := C.LogReplayEntryC{
			address_ptr:  (*C.uchar)(cAddrDataPtr),
			address_len:  20,
			log_data_ptr: (*C.uchar)(cLogDataPtr),
			log_data_len: C.int(logDataLen),
		}
		cEntries = append(cEntries, entry)
		fmt.Printf("    - Đã chuẩn bị entry (AddrLen: 20, LogDataLen: %d)\n", logDataLen)
	}
	numValidEntries := len(cEntries)
	if numValidEntries == 0 {
		fmt.Println("[Go CallReplay] Không có entry hợp lệ nào được chuẩn bị sau khi lọc. Không gọi C++.")
		return 1
	}
	cEntriesPtr := (*C.LogReplayEntryC)(unsafe.Pointer(&cEntries[0]))
	fmt.Printf("[Go CallReplay] Gọi hàm C.ReplayFullDbLogs với %d entries...\n", numValidEntries)
	result := C.ReplayFullDbLogs(cEntriesPtr, C.int(numValidEntries))
	fmt.Printf("[Go CallReplay] Hàm C.ReplayFullDbLogs trả về: %d\n", result)
	return int(result)
}

// CallClearAllStateInstances clears the C++ EVM's internal global state cache
// (State::instances). This MUST be called after sync→validator transition
// (LAZY REFRESH) to prevent the EVM from using stale nonce/balance values.
func CallClearAllStateInstances() {
	C.clearAllStateInstances()
}

// CallUpdateStateNonce updates the C++ State::instances cache nonce for a specific address.
// This MUST be called when Go changes nonce directly (e.g., BLS SetPublicKey, setAccountType)
// to keep C++ cache in sync with Go state.
func CallUpdateStateNonce(address common.Address, nonce uint64) {
	addrBytes := address.Bytes()
	C.updateStateNonce((*C.uchar)(unsafe.Pointer(&addrBytes[0])), C.ulonglong(nonce))
}

// CallUpdateStateBalance updates the C++ State::instances cache balance for a specific address.
// This MUST be called when Go changes balance directly (e.g., Native Transfer)
// to keep C++ cache in sync with Go state.
func CallUpdateStateBalance(address common.Address, balance *big.Int) {
	addrBytes := address.Bytes()
	var balanceBytes [32]byte
	balance.FillBytes(balanceBytes[:])
	C.updateStateBalance((*C.uchar)(unsafe.Pointer(&addrBytes[0])), (*C.uchar)(unsafe.Pointer(&balanceBytes[0])))
}

// ExportAllXapianLogs extracts the uncommitted Xapian logs from all active C++ instances.
// This MUST be called BEFORE CommitAllXapian() during block processing,
// so that the logs can be packaged into the BackUpDb for P2P Sync.
func ExportAllXapianLogs() map[string][]byte {
	cArray := C.MVM_exportAllXapianLogs()
	defer C.MVM_freeExportedXapianLogs(cArray)

	count := int(cArray.count)
	if count == 0 {
		return nil
	}

	result := make(map[string][]byte, count)
	
	// Create a Go slice backed by the C array
	cDataSlice := unsafe.Slice(cArray.data, count)
	
	for i := 0; i < count; i++ {
		cEntry := cDataSlice[i]
		
		// Parse address (20 bytes)
		addressBytes := C.GoBytes(unsafe.Pointer(&cEntry.address[0]), 20)
		addressHex := common.BytesToAddress(addressBytes).Hex()
		
		// Parse serialized logs
		if cEntry.logs != nil && cEntry.logs_length > 0 {
			logData := C.GoBytes(unsafe.Pointer(cEntry.logs), cEntry.logs_length)
			result[addressHex] = logData
		}
	}
	
	return result
}

// ConfigureXapianBasePath sets XAPIAN_BASE_PATH env var so that C++ createFullPath()
// picks it up via getenv(). Must be called before any MVM/Xapian operation.
// CGo-direct approach (SetXapianBasePath) requires C++ rebuild; this is equivalent
// since getenv() is called per-request, not at init time.
func ConfigureXapianBasePath(path string) {
	if path == "" {
		return
	}
	os.Setenv("XAPIAN_BASE_PATH", path)
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	C.SetXapianBasePath(cPath)
}

func init() {
	// Tăng giới hạn xả tự động (flush threshold) của Xapian lên 10 triệu modifications (~1GB)
	// Điều này ngăn chặn Xapian tự động xả dữ liệu gây block quá trình thực thi,
	// để dành việc xả đĩa cho Background Worker (chạy mỗi 10s)
	if os.Getenv("XAPIAN_FLUSH_THRESHOLD") == "" {
		os.Setenv("XAPIAN_FLUSH_THRESHOLD", "10000000")
	}
}

func GetOrCreateMVMApi(
	key common.Address,
	smartContractDb SmartContractDB,
	accountStateDb AccountStateDB,
	extendedMode bool,
) *MVMApi {
	// Bước 1: Chạy thử Load nhanh (fast path)
	if api, exists := apiInstances.Load(key); exists {
		cached := api.(*MVMApi)
		cached.accountStateDb = accountStateDb
		cached.smartContractDb = smartContractDb
		return cached
	}

	// Bước 2: Chuẩn bị một instance mới trong trường hợp chưa ai tạo
	newApi := &MVMApi{
		key:             key,
		smartContractDb: smartContractDb,
		accountStateDb:  accountStateDb,
		extendedMode:    extendedMode,
		createdAt:       time.Now(),
	}

	// Bước 3: Dùng LoadOrStore (Atomic Check-And-Act)
	// Tránh trường hợp 2 luồng cùng thấy exists=false và cùng Store đè lên nhau.
	actualApi, loaded := apiInstances.LoadOrStore(key, newApi)

	if loaded {
		// Luồng khác đã nhanh tay tạo và Store vào map ngay trước chúng ta mili-giây!
		// Thay vì đè lên của họ (gây lỗi mất pointer cũ), ta TÁI SỬ DỤNG CHUNG chính cái họ vừa tạo.
		cached := actualApi.(*MVMApi)
		cached.accountStateDb = accountStateDb
		cached.smartContractDb = smartContractDb
		return cached
	}

	// Chúng ta là người đầu tiên Store thành công
	apiInstanceCount.Add(1)
	return newApi
}

func LenApiInstances() int {
	return int(apiInstanceCount.Load())
}

func RemoveOldApiInstances() {
	const targetSize = 2000

	// Lớp bảo vệ 1: Fast-path check với atomic counter
	// Nếu tổng số instance (bao gồm cả protected) còn nhỏ hơn targetSize,
	// thì chắc chắn số unprotected cũng nhỏ hơn, không cần quét map.
	count := int(apiInstanceCount.Load())
	if count < 0 {
		actualCount := 0
		apiInstances.Range(func(key, value interface{}) bool {
			actualCount++
			return true
		})
		apiInstanceCount.Store(int32(actualCount))
		count = actualCount
	}
	if count < targetSize {
		return
	}

	type apiInstanceInfo struct {
		key       common.Address
		createdAt time.Time
	}
	startScan := time.Now()

	// Tối ưu 1: Chuẩn bị mảng với Capacity lớn để tránh re-allocation
	instancesToRemove := make([]apiInstanceInfo, 0, targetSize)
	currentTotalCount := 0 // Tối ưu 2: Đếm trực tiếp trong lúc Range
	apiInstances.Range(func(key, value interface{}) bool {
		currentTotalCount++
		mvmId := key.(common.Address)
		if _, protected := protectedApiInstances.Load(mvmId); !protected {
			instance := value.(*MVMApi)
			instancesToRemove = append(instancesToRemove, apiInstanceInfo{
				key:       mvmId,
				createdAt: instance.createdAt,
			})
		}
		return true
	})
	scanDuration := time.Since(startScan)

	// Đã bỏ lời gọi LenApiInstances() ở đây
	numToRemove := currentTotalCount - targetSize

	// Chỉ sắp xếp nếu THỰC SỰ cần dọn bớt
	if numToRemove > 0 {
		sort.Slice(instancesToRemove, func(i, j int) bool {
			return instancesToRemove[i].createdAt.Before(instancesToRemove[j].createdAt)
		})
	}

	if numToRemove > len(instancesToRemove) {
		numToRemove = len(instancesToRemove)
	}

	deletedCount := 0
	for i := 0; i < numToRemove; i++ {
		instanceInfo := instancesToRemove[i]
		if _, protected := protectedApiInstances.Load(instanceInfo.key); !protected {
			ClearMVMApi(instanceInfo.key)
			deletedCount++
			logger.Debug("Removed old unprotected MVMApi instance:", instanceInfo.key.Hex(), "created at:", instanceInfo.createdAt)
		}
	}
	logger.Info("🧹 [MVM CLEANUP] scan=%v, total=%d, unprotected=%d, removed=%d, remaining_atomic=%d",
		scanDuration, currentTotalCount, len(instancesToRemove), deletedCount, apiInstanceCount.Load())
}

func GetMVMApi(mvmId common.Address) *MVMApi {
	value, ok := apiInstances.Load(mvmId)
	if !ok {
		return nil
	}
	return value.(*MVMApi)
}

func ClearMVMApi(mvmId common.Address) {
	if _, protected := protectedApiInstances.Load(mvmId); protected {
		logger.Debug("Skipping deletion of protected MVMApi instance:", mvmId.Hex())
		return
	}
	instance, loaded := apiInstances.LoadAndDelete(mvmId)

	// FORK-SAFETY FIX: BẮT BUỘC phải dọn dẹp state bên C++ EVM
	// (Note: C++ EVM state cleanup is now handled automatically by MyGlobalState lifecycle)

	if !loaded {
		return
	}
	apiInstanceCount.Add(-1)

	mvmApi, ok := instance.(*MVMApi)
	if !ok || mvmApi == nil {
		logger.Debug("Removed invalid/nil MVMApi entry from map:", mvmId.Hex())
		return
	}
	logger.Debug("Cleared unprotected MVMApi instance:", mvmId.Hex())
}

func ClearAllMVMApi() {
	logger.Info("Clearing all unprotected MVMApi instances...")
	apiInstances.Range(func(key, value interface{}) bool {
		mvmId := key.(common.Address)
		if _, protected := protectedApiInstances.Load(mvmId); !protected {
			// logger.Error("ClearMVM: 7", mvmId)
			ClearMVMApi(mvmId)
		} else {
			logger.Debug("Skipping protected MVMApi instance during ClearAll:", mvmId.Hex())
		}
		return true
	})
	logger.Info("Finished clearing all unprotected instances.")
}

func (a *MVMApi) GetKey() common.Address {
	return a.key
}
func (a *MVMApi) SetSmartContractDb(smartContractDb SmartContractDB) {
	a.smartContractDb = smartContractDb
}
func (a *MVMApi) SmartContractDatas() SmartContractDB {
	return a.smartContractDb
}
func (a *MVMApi) SetAccountStateDb(accountStateDb AccountStateDB) {
	a.accountStateDb = accountStateDb
}
func (a *MVMApi) AccountStateDb() AccountStateDB {
	return a.accountStateDb
}
func (a *MVMApi) SetRelatedAddresses(addresses []common.Address) {
	// Clear existing stored addresses to prevent accumulation across transactions
	// that reuse the same MVMApi instance (e.g., same GroupID or same ToAddress).
	a.currentRelatedAddresses.Range(func(key, value interface{}) bool {
		a.currentRelatedAddresses.Delete(key)
		return true
	})

	for _, v := range addresses {
		a.currentRelatedAddresses.Store(v, struct{}{})
	}
}

// SetBlobContext sets the EIP-4844 context for the BLOBHASH/BLOBBASEFEE
// opcodes, consumed by the GetBlobHash/GetBlobBaseFee cgo exports below.
// blobVersionedHashes may be nil for a non-blob tx (BLOBHASH then always
// resolves to 0, matching EIP-4844's out-of-range rule). Called once per tx
// execution alongside SetRelatedAddresses.
func (a *MVMApi) SetBlobContext(blobVersionedHashes [][]byte, blobBaseFee *uint256.Int) {
	a.blobVersionedHashes = blobVersionedHashes
	a.blobBaseFee = blobBaseFee
}

// blobHashAt implements the BLOBHASH opcode's index lookup as plain Go, kept
// separate from the GetBlobHash cgo export below so it's unit-testable
// without going through the C boundary. EIP-4844: an out-of-range index must
// resolve to 0 (ok=false here), never an error or garbage value.
func (a *MVMApi) blobHashAt(index uint64) (hash []byte, ok bool) {
	if index >= uint64(len(a.blobVersionedHashes)) {
		return nil, false
	}
	return a.blobVersionedHashes[index], true
}

// ═══════════════════════════════════════════════════════════════════════
// TEE-PACKAGING B1 CONTEXT (see note/tee_core_packaging_plan.md)
//
// b1Context builds the cgo-owned buffers for the chain-id/blob/cross-chain
// context that Call/Execute/Deploy now pass directly into C.call/execute/
// deploy, instead of the C++ side calling back into Go mid-execution
// (GetChainId/GetBlobHash/GetBlobBaseFee/GetCrossChainSender/
// GetCrossChainSourceId — those //export functions still exist but are no
// longer invoked from C++, see my_global_state.cpp). Every pointer here may
// be nil, matching "not supplied" — see mvm_linker.hpp's MVM_B1_CONTEXT_PARAMS
// doc comment and block_context.h for the exact semantics on the C++ side.
// ═══════════════════════════════════════════════════════════════════════
type b1Context struct {
	chainID          unsafe.Pointer // 32 bytes big-endian, or nil
	blobHashes       unsafe.Pointer // flat array of 32-byte hashes, or nil
	blobHashCount    C.int
	blobBaseFee      unsafe.Pointer // 32 bytes big-endian, or nil
	crossChainSender unsafe.Pointer // 20 bytes, or nil
	crossChainSource unsafe.Pointer // 8 bytes big-endian uint64, or nil
	blockHashes      unsafe.Pointer // flat array of 32-byte hashes, or nil
	blockHashCount   C.int
}

// maxBlockhashLookback matches BLOCKHASH's own EVM-spec window — a query
// further back than this always resolves to 0, so fetching more would be
// pure waste.
const maxBlockhashLookback = 256

// blockhashRelevantOpcodes are the bytes that make HasBlockhashOpcode
// report true: BLOCKHASH itself (0x40), plus every CALL-family opcode
// (CALL/CALLCODE/DELEGATECALL/STATICCALL: 0xf1/0xf2/0xf4/0xfa). The
// CALL-family bytes matter because they mean this code can hand control to
// OTHER code this function was never given the chance to scan — a nested
// call reaching a contract that itself uses BLOCKHASH would otherwise
// silently see block_hashes as empty (0 for every query) with no way to
// tell the difference from "genuinely no BLOCKHASH usage anywhere in this
// call". CREATE/CREATE2 deliberately excluded: a newly-deployed
// contract's constructor runs through its own separate Deploy() call,
// which gets its own independent scan of the constructor bytecode — not a
// gap this function needs to cover.
var blockhashRelevantOpcodes = [256]bool{0x40: true, 0xf1: true, 0xf2: true, 0xf4: true, 0xfa: true}

// HasBlockhashOpcode reports whether code might need BLOCKHASH context —
// see blockhashRelevantOpcodes for exactly which bytes trigger this and
// why. Deliberately conservative, not a real disassembler: any of these
// bytes sitting inside a PUSH's immediate data still counts, so this can
// over-report (fetch block hashes a contract never actually queries) — but
// erring toward over-fetching is the only safe direction, since
// under-fetching would silently corrupt BLOCKHASH's result rather than
// just waste work. Note this is still a heuristic, not a proof: it cannot
// see through a CALL target resolved by an address computed at runtime
// (e.g. from storage) any better than it can see through push-data — it
// only needs to notice that *some* CALL-family opcode is reachable, which
// a plain byte scan does reliably regardless of how the target address
// itself is computed.
func HasBlockhashOpcode(code []byte) bool {
	for _, b := range code {
		if blockhashRelevantOpcodes[b] {
			return true
		}
	}
	return false
}

// fetchRecentBlockHashes returns up to maxBlockhashLookback preceding block
// hashes for blockNumber, most-recent-first (index 0 = blockNumber-1),
// matching BLOCKHASH's own "how many blocks back" framing — see
// block_context.h's block_hashes field doc. Stops at the first gap (a
// blockNumber the chain doesn't have a mapping for) rather than fetching
// a sparse/partial-with-holes array, since BLOCKHASH's replacement on the
// C++ side (MyGlobalState::get_block_hash) indexes this contiguously by
// distance from the current block and has no way to represent "unknown" at
// a specific index other than the array simply not extending that far.
func fetchRecentBlockHashes(blockNumber uint64) [][]byte {
	bc := blockchain.GetBlockChainInstance()
	if bc == nil || blockNumber == 0 {
		return nil
	}
	lookback := uint64(maxBlockhashLookback)
	if blockNumber < lookback {
		lookback = blockNumber
	}
	hashes := make([][]byte, 0, lookback)
	for i := uint64(1); i <= lookback; i++ {
		h, ok := bc.GetBlockHashByNumber(blockNumber - i)
		if !ok {
			break
		}
		hashes = append(hashes, h.Bytes())
	}
	return hashes
}

// buildB1Context reads this instance's already-set context (SetBlobContext/
// SetCrossChainContext, called by the same callers that used to rely on the
// callbacks) plus the chain id — from the same config.ConfigApp.ChainId
// global GetChainId's cgo export used to read, just fetched here instead of
// via a mid-execution callback — and copies it into cgo-owned buffers.
//
// code/blockNumber are only used to decide whether BLOCKHASH's context is
// worth fetching at all (see HasBlockhashOpcode's doc for why this one
// field, unlike the others above, is NOT fetched unconditionally). Pass the
// bytecode that's actually about to execute: the constructor for Deploy, or
// the target contract's deployed code for Call/Execute.
//
// Known, accepted cost (code review, 2026-08-14): for Call/Execute, the
// caller fetches that target code via smartContractDb.Code() specifically
// to feed this scan, duplicating the read the C++ side performs anyway via
// the GlobalStateGet callback when it resolves the target account
// mid-execution. Not fixed here: avoiding it would mean threading the
// already-fetched code across the cgo boundary to replace GlobalStateGet's
// own fetch for that one address — a real restructuring of the account/
// code resolution path for a plain cache read's worth of savings (this
// codebase already warms these reads via PreloadAccounts elsewhere), not
// worth the added risk on a consensus-critical path for this pass.
//
// Caller MUST defer the returned value's free().
func (a *MVMApi) buildB1Context(code []byte, blockNumber uint64) b1Context {
	var ctx b1Context

	// Defensive nil guard: the old GetChainId() callback read this exact
	// same global with no guard, but it was only ever invoked lazily, on
	// demand, if bytecode actually executed CHAINID — so a nil
	// config.ConfigApp only crashed if BOTH conditions held. This function
	// now runs unconditionally on every Call/Execute/Deploy, regardless of
	// whether the tx touches CHAINID at all, so the same unconditional
	// access would crash universally (e.g. every unit test that doesn't
	// call config.Init first, not just ones exercising CHAINID) — caught
	// by go test's full suite. Falling back to "not supplied" (chain_id=0
	// on the C++ side) here is strictly safer than the old behavior, not
	// just equivalent: config is always loaded before real tx processing
	// starts in production, so this guard only ever engages in exactly the
	// anomalous states (tests, any future out-of-order init) where the old
	// code would have crashed the whole node instead.
	if config.ConfigApp != nil {
		if chainID := config.ConfigApp.ChainId; chainID != nil {
			b := make([]byte, 32)
			chainID.FillBytes(b)
			ctx.chainID = C.CBytes(b)
		}
	}

	if n := len(a.blobVersionedHashes); n > 0 {
		flat := make([]byte, n*32)
		for i, h := range a.blobVersionedHashes {
			// Defensive: h is expected to already be exactly 32 bytes
			// (EIP-4844 versioned hashes); right-align if it isn't, rather
			// than panicking or silently misreading adjacent hashes.
			dst := flat[i*32 : i*32+32]
			if len(h) >= 32 {
				copy(dst, h[len(h)-32:])
			} else {
				copy(dst[32-len(h):], h)
			}
		}
		ctx.blobHashes = C.CBytes(flat)
		ctx.blobHashCount = C.int(n)
	}

	if a.blobBaseFee != nil {
		b := a.blobBaseFee.Bytes32()
		ctx.blobBaseFee = C.CBytes(b[:])
	}

	if a.crossChainActive {
		ctx.crossChainSender = C.CBytes(a.crossChainSender.Bytes())
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, a.crossChainSourceId)
		ctx.crossChainSource = C.CBytes(b)
	}

	if HasBlockhashOpcode(code) {
		hashes := fetchRecentBlockHashes(blockNumber)
		if n := len(hashes); n > 0 {
			flat := make([]byte, n*32)
			for i, h := range hashes {
				dst := flat[i*32 : i*32+32]
				if len(h) >= 32 {
					copy(dst, h[len(h)-32:])
				} else {
					copy(dst[32-len(h):], h)
				}
			}
			ctx.blockHashes = C.CBytes(flat)
			ctx.blockHashCount = C.int(n)
		}
	}

	return ctx
}

func (c *b1Context) free() {
	if c.chainID != nil {
		C.free(c.chainID)
	}
	if c.blobHashes != nil {
		C.free(c.blobHashes)
	}
	if c.blobBaseFee != nil {
		C.free(c.blobBaseFee)
	}
	if c.crossChainSender != nil {
		C.free(c.crossChainSender)
	}
	if c.crossChainSource != nil {
		C.free(c.crossChainSource)
	}
	if c.blockHashes != nil {
		C.free(c.blockHashes)
	}
}

func (a *MVMApi) GetCurrentRelatedAddresses() []common.Address {
	var addresses []common.Address
	a.currentRelatedAddresses.Range(func(key, value interface{}) bool {
		if addr, ok := key.(common.Address); ok {
			addresses = append(addresses, addr)
		}
		return true
	})
	sort.Slice(addresses, func(i, j int) bool {
		return bytes.Compare(addresses[i].Bytes(), addresses[j].Bytes()) < 0
	})
	return addresses
}
func (a *MVMApi) InRelatedAddress(address common.Address) bool {
	_, ok := a.currentRelatedAddresses.Load(address)
	return ok
}
func (a *MVMApi) AddRelatedAddress(address common.Address) {
	a.currentRelatedAddresses.Store(address, struct{}{})
}
func (a *MVMApi) Call(
	bSender []byte,
	bContractAddress []byte,
	bInput []byte,
	amount *big.Int,
	gasPrice uint64,
	gasLimit uint64,
	blockPrevrandao uint64,
	blockGasLimit uint64,
	blockTime uint64,
	blockBaseFee uint64,
	blockNumber uint64,
	blockCoinbase common.Address,
	mvmId common.Address,
	readOnly bool,
	bTxHash []byte,
	relatedAddresses []common.Address,
	isDebug bool,
	isOffChain bool,
) *MVMExecuteResult {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("Panic trong MVMApi.Call:", r)
			a.rs = &MVMExecuteResult{}
		}
	}()

	if isOffChain {
		ethCallSemaphore <- struct{}{}
		defer func() { <-ethCallSemaphore }()
	}

	bAmount := [32]byte{}
	amount.FillBytes(bAmount[:])
	cBSender := C.CBytes(bSender)
	cBContractAddress := C.CBytes(bContractAddress)
	cBInput := C.CBytes(bInput)
	cBAmount := C.CBytes(bAmount[:])
	bBlockNumber := [32]byte{}
	big.NewInt(int64(blockNumber)).FillBytes(bBlockNumber[:])
	bBlockCoinbase := blockCoinbase.Bytes()
	cBBlockNumber := C.CBytes(bBlockNumber[:])
	cBBlockCoinbase := C.CBytes(bBlockCoinbase)
	var randomizedMvmId common.Address
	if isOffChain {
		bBmvmIdBytes := make([]byte, 20)
		copy(bBmvmIdBytes[0:8], mvmId.Bytes()[0:8])
		val := atomic.AddUint64(&offChainCounter, 1)
		binary.BigEndian.PutUint64(bBmvmIdBytes[8:16], val)
		_, _ = rand.Read(bBmvmIdBytes[16:20])

		randomizedMvmId = common.BytesToAddress(bBmvmIdBytes)
		apiInstances.Store(randomizedMvmId, a)
		apiInstanceCount.Add(1)
		ProtectMVMApi(randomizedMvmId)
		defer func() {
			UnprotectMVMApi(randomizedMvmId)
			ClearMVMApi(randomizedMvmId)
		}()
	} else {
		randomizedMvmId = mvmId
	}
	cBBmvmId := C.CBytes(randomizedMvmId.Bytes())
	cBTxHash := C.CBytes(bTxHash)
	defer C.free(unsafe.Pointer(cBSender))
	defer C.free(unsafe.Pointer(cBContractAddress))
	defer C.free(unsafe.Pointer(cBInput))
	defer C.free(unsafe.Pointer(cBAmount))
	defer C.free(unsafe.Pointer(cBBlockNumber))
	defer C.free(unsafe.Pointer(cBBlockCoinbase))
	defer C.free(unsafe.Pointer(cBBmvmId))
	defer C.free(unsafe.Pointer(cBTxHash))
	if cBSender == nil || cBContractAddress == nil || cBInput == nil || cBAmount == nil ||
		cBBlockNumber == nil || cBBlockCoinbase == nil || cBBmvmId == nil || cBTxHash == nil {
		logger.Error("Một hoặc nhiều con trỏ C bị nil trong MVMApi.Call")
		a.rs = &MVMExecuteResult{}
		return a.rs
	}
	totalAddresses := len(relatedAddresses)
	bRelatedAddresses := make([]byte, 0, totalAddresses*20)
	for _, addr := range relatedAddresses {
		bRelatedAddresses = append(bRelatedAddresses, addr.Bytes()...)
	}
	var cBRelatedAddresses unsafe.Pointer
	if totalAddresses > 0 {
		cBRelatedAddresses = C.CBytes(bRelatedAddresses)
		defer C.free(cBRelatedAddresses)
	} else {
		cBRelatedAddresses = nil
	}
	b1ctx := a.buildB1Context(a.smartContractDb.Code(common.BytesToAddress(bContractAddress)), blockNumber)
	defer b1ctx.free()
	cRs := C.call(
		(*C.uchar)(cBSender),
		(*C.uchar)(cBContractAddress),
		(*C.uchar)(cBInput),
		(C.int)(len(bInput)),
		(*C.uchar)(cBAmount),
		(C.ulonglong)(gasPrice),
		(C.ulonglong)(gasLimit),
		(C.ulonglong)(blockPrevrandao),
		(C.ulonglong)(blockGasLimit),
		(C.ulonglong)(blockTime),
		(C.ulonglong)(blockBaseFee),
		(*C.uchar)(cBBlockNumber),
		(*C.uchar)(cBBlockCoinbase),
		(*C.uchar)(cBBmvmId),
		C._Bool(readOnly),
		(*C.uchar)(cBTxHash),
		C._Bool(isDebug),
		(*C.uchar)(cBRelatedAddresses), // Mảng bytes (20 * count)
		(C.int)(totalAddresses),
		C._Bool(isOffChain),
		(*C.uchar)(b1ctx.chainID),
		(*C.uchar)(b1ctx.blobHashes),
		b1ctx.blobHashCount,
		(*C.uchar)(b1ctx.blobBaseFee),
		(*C.uchar)(b1ctx.crossChainSender),
		(*C.uchar)(b1ctx.crossChainSource),
		(*C.uchar)(b1ctx.blockHashes),
		b1ctx.blockHashCount,
	)
	a.rs = extractExecuteResult(cRs)
	FlushNativeLogs(a.rs.NativeLogs) // TEE-packaging B2
	C.freeResult(cRs)
	a.enforceStrictAccessLists()
	return a.rs
}

func (a *MVMApi) Execute(
	bSender []byte,
	bContractAddress []byte,
	bInput []byte,
	amount *big.Int,
	gasPrice uint64,
	gasLimit uint64,
	blockPrevrandao uint64,
	blockGasLimit uint64,
	blockTime uint64,
	blockBaseFee uint64,
	blockNumber uint64,
	blockCoinbase common.Address,
	mvmId common.Address,
	bTxHash []byte,
	relatedAddresses []common.Address,
	isDebug bool,
	isCache bool,
) *MVMExecuteResult {
	bAmount := [32]byte{}
	amount.FillBytes(bAmount[:])
	cBSender := C.CBytes(bSender)
	cBContractAddress := C.CBytes(bContractAddress)
	cBInput := C.CBytes(bInput)
	cBAmount := C.CBytes(bAmount[:])
	bBlockNumber := [32]byte{}
	big.NewInt(int64(blockNumber)).FillBytes(bBlockNumber[:])
	bBlockCoinbase := blockCoinbase.Bytes()
	cBBlockNumber := C.CBytes(bBlockNumber[:])
	cBBlockCoinbase := C.CBytes(bBlockCoinbase)
	cBTxHash := C.CBytes(bTxHash)
	cBBmvmId := C.CBytes(mvmId.Bytes())
	//
	totalAddresses := len(relatedAddresses)
	bRelatedAddresses := make([]byte, 0, totalAddresses*20)
	for _, addr := range relatedAddresses {
		bRelatedAddresses = append(bRelatedAddresses, addr.Bytes()...)
	}
	var cBRelatedAddresses unsafe.Pointer
	if totalAddresses > 0 {
		cBRelatedAddresses = C.CBytes(bRelatedAddresses)
		defer C.free(cBRelatedAddresses)
	} else {
		cBRelatedAddresses = nil
	}
	defer C.free(unsafe.Pointer(cBSender))
	defer C.free(unsafe.Pointer(cBContractAddress))
	defer C.free(unsafe.Pointer(cBInput))
	defer C.free(unsafe.Pointer(cBAmount))
	defer C.free(unsafe.Pointer(cBBlockNumber))
	defer C.free(unsafe.Pointer(cBBlockCoinbase))
	defer C.free(unsafe.Pointer(cBTxHash))
	defer C.free(unsafe.Pointer(cBBmvmId))

	b1ctx := a.buildB1Context(a.smartContractDb.Code(common.BytesToAddress(bContractAddress)), blockNumber)
	defer b1ctx.free()
	cRs := C.execute(
		(*C.uchar)(cBSender),
		(*C.uchar)(cBContractAddress),
		(*C.uchar)(cBInput),
		(C.int)(len(bInput)),
		(*C.uchar)(cBAmount),
		(C.ulonglong)(gasPrice),
		(C.ulonglong)(gasLimit),
		(C.ulonglong)(blockPrevrandao),
		(C.ulonglong)(blockGasLimit),
		(C.ulonglong)(blockTime),
		(C.ulonglong)(blockBaseFee),
		(*C.uchar)(cBBlockNumber),
		(*C.uchar)(cBBlockCoinbase),
		(*C.uchar)(cBBmvmId),
		(*C.uchar)(cBTxHash),
		C._Bool(isDebug),
		(*C.uchar)(cBRelatedAddresses), // Mảng bytes (20 * count)
		(C.int)(totalAddresses),        // Số lượng addresses
		C._Bool(isCache),
		(*C.uchar)(b1ctx.chainID),
		(*C.uchar)(b1ctx.blobHashes),
		b1ctx.blobHashCount,
		(*C.uchar)(b1ctx.blobBaseFee),
		(*C.uchar)(b1ctx.crossChainSender),
		(*C.uchar)(b1ctx.crossChainSource),
		(*C.uchar)(b1ctx.blockHashes),
		b1ctx.blockHashCount,
	)
	a.rs = extractExecuteResult(cRs)
	FlushNativeLogs(a.rs.NativeLogs) // TEE-packaging B2
	C.freeResult(cRs)
	a.enforceStrictAccessLists()
	return a.rs
}

type ExecuteBatchInput struct {
	Sender           []byte
	ContractAddress  []byte
	Input            []byte
	Amount           *big.Int
	GasPrice         uint64
	GasLimit         uint64
	TxHash           []byte
	RelatedAddresses []common.Address
	IsDebug          bool
}

func (a *MVMApi) ExecuteBatch(
	inputs []ExecuteBatchInput,
	blockPrevrandao uint64,
	blockGasLimit uint64,
	blockTime uint64,
	blockBaseFee uint64,
	blockNumber uint64,
	blockCoinbase common.Address,
	mvmId common.Address,
) []*MVMExecuteResult {

	numInputs := len(inputs)
	if numInputs == 0 {
		return nil
	}

	bBlockNumber := [32]byte{}
	big.NewInt(int64(blockNumber)).FillBytes(bBlockNumber[:])
	bBlockCoinbase := blockCoinbase.Bytes()
	cBBlockNumber := C.CBytes(bBlockNumber[:])
	cBBlockCoinbase := C.CBytes(bBlockCoinbase)
	cBBmvmId := C.CBytes(mvmId.Bytes())
	defer C.free(unsafe.Pointer(cBBlockNumber))
	defer C.free(unsafe.Pointer(cBBlockCoinbase))
	defer C.free(unsafe.Pointer(cBBmvmId))

	cInputs := make([]C.ExecuteBatchInputC, numInputs)

	// Keep track of C pointers to free them later
	var cPointers []unsafe.Pointer
	defer func() {
		for _, ptr := range cPointers {
			if ptr != nil {
				C.free(ptr)
			}
		}
	}()

	for i, in := range inputs {
		bAmount := [32]byte{}
		in.Amount.FillBytes(bAmount[:])

		cBSender := C.CBytes(in.Sender)
		cBContractAddress := C.CBytes(in.ContractAddress)
		cBInput := C.CBytes(in.Input)
		cBAmount := C.CBytes(bAmount[:])
		cBTxHash := C.CBytes(in.TxHash)

		cPointers = append(cPointers, cBSender, cBContractAddress, cBInput, cBAmount, cBTxHash)

		totalAddresses := len(in.RelatedAddresses)
		var cBRelatedAddresses unsafe.Pointer
		if totalAddresses > 0 {
			bRelatedAddresses := make([]byte, 0, totalAddresses*20)
			for _, addr := range in.RelatedAddresses {
				bRelatedAddresses = append(bRelatedAddresses, addr.Bytes()...)
			}
			cBRelatedAddresses = C.CBytes(bRelatedAddresses)
			cPointers = append(cPointers, cBRelatedAddresses)
		} else {
			cBRelatedAddresses = nil
		}

		cInputs[i].b_caller_address = (*C.uchar)(cBSender)
		cInputs[i].b_contract_address = (*C.uchar)(cBContractAddress)
		cInputs[i].b_input = (*C.uchar)(cBInput)
		cInputs[i].length_input = (C.int)(len(in.Input))
		cInputs[i].b_amount = (*C.uchar)(cBAmount)
		cInputs[i].gas_price = (C.ulonglong)(in.GasPrice)
		cInputs[i].gas_limit = (C.ulonglong)(in.GasLimit)
		cInputs[i].b_tx_hash = (*C.uchar)(cBTxHash)
		cInputs[i].is_debug = C._Bool(in.IsDebug)
		cInputs[i].b_related_addresses = (*C.uchar)(cBRelatedAddresses)
		cInputs[i].related_addresses_count = (C.int)(totalAddresses)
	}

	cInputsPtr := (*C.ExecuteBatchInputC)(unsafe.Pointer(&cInputs[0]))

	cBatchRs := C.executeBatch(
		cInputsPtr,
		(C.int)(numInputs),
		(C.ulonglong)(blockPrevrandao),
		(C.ulonglong)(blockGasLimit),
		(C.ulonglong)(blockTime),
		(C.ulonglong)(blockBaseFee),
		(*C.uchar)(cBBlockNumber),
		(*C.uchar)(cBBlockCoinbase),
		(*C.uchar)(cBBmvmId),
	)

	results := make([]*MVMExecuteResult, numInputs)
	if cBatchRs != nil && cBatchRs.num_results == (C.int)(numInputs) {
		// Convert C array of pointers to Go slice
		cResultsSlice := (*[1 << 30]*C.struct_ExecuteResult)(unsafe.Pointer(cBatchRs.results))[:numInputs:numInputs]
		for i := 0; i < numInputs; i++ {
			if cResultsSlice[i] != nil {
				results[i] = extractExecuteResult(cResultsSlice[i])
				FlushNativeLogs(results[i].NativeLogs) // TEE-packaging B2
			} else {
				results[i] = &MVMExecuteResult{}
			}
		}
		C.freeBatchResult(cBatchRs)
	} else {
		logger.Error("ExecuteBatch returned invalid or nil results")
		for i := 0; i < numInputs; i++ {
			results[i] = &MVMExecuteResult{}
		}
	}

	return results
}

func (a *MVMApi) SendNative(
	bSender []byte,
	bContractAddress []byte,
	amount *big.Int,
	gasPrice uint64,
	gasLimit uint64,
	blockPrevrandao uint64,
	blockGasLimit uint64,
	blockTime uint64,
	blockBaseFee uint64,
	blockNumber uint64,
	blockCoinbase common.Address,
	mvmId common.Address,
	isCache bool,
) *MVMExecuteResult {
	bAmount := [32]byte{}
	amount.FillBytes(bAmount[:])
	cBFrom := C.CBytes(bSender)
	cBTo := C.CBytes(bContractAddress)
	cBAmount := C.CBytes(bAmount[:])
	bBlockNumber := [32]byte{}
	big.NewInt(int64(blockNumber)).FillBytes(bBlockNumber[:])
	bBlockCoinbase := blockCoinbase.Bytes()
	cBBlockNumber := C.CBytes(bBlockNumber[:])
	cBBlockCoinbase := C.CBytes(bBlockCoinbase)
	cBBmvmId := C.CBytes(mvmId.Bytes())
	defer C.free(unsafe.Pointer(cBFrom))
	defer C.free(unsafe.Pointer(cBTo))
	defer C.free(unsafe.Pointer(cBAmount))
	defer C.free(unsafe.Pointer(cBBlockNumber))
	defer C.free(unsafe.Pointer(cBBlockCoinbase))
	defer C.free(unsafe.Pointer(cBBmvmId))

	cRs := C.sendNative(
		(*C.uchar)(cBFrom),
		(*C.uchar)(cBTo),
		(*C.uchar)(cBAmount),
		(C.ulonglong)(gasPrice),
		(C.ulonglong)(gasLimit),
		(C.ulonglong)(blockPrevrandao),
		(C.ulonglong)(blockGasLimit),
		(C.ulonglong)(blockTime),
		(C.ulonglong)(blockBaseFee),
		(*C.uchar)(cBBlockNumber),
		(*C.uchar)(cBBlockCoinbase),
		(*C.uchar)(cBBmvmId),
		C._Bool(isCache),
	)
	a.rs = extractExecuteResult(cRs)
	FlushNativeLogs(a.rs.NativeLogs) // TEE-packaging B2
	C.freeResult(cRs)
	a.enforceStrictAccessLists()
	return a.rs
}

func (a *MVMApi) ProcessNativeMintBurn(
	bFrom []byte,
	bTo []byte,
	amount *big.Int,
	operationType uint64, // 0: mint, 1: burn
	gasPrice uint64,
	gasLimit uint64,
	blockPrevrandao uint64,
	blockGasLimit uint64,
	blockTime uint64,
	blockBaseFee uint64,
	blockNumber uint64,
	blockCoinbase common.Address,
	mvmId common.Address,
	isCache bool,
) *MVMExecuteResult {
	bAmount := [32]byte{}
	amount.FillBytes(bAmount[:])
	cBFrom := C.CBytes(bFrom)
	cBTo := C.CBytes(bTo)
	cBAmount := C.CBytes(bAmount[:])
	bBlockNumber := [32]byte{}
	big.NewInt(int64(blockNumber)).FillBytes(bBlockNumber[:])
	bBlockCoinbase := blockCoinbase.Bytes()
	cBBlockNumber := C.CBytes(bBlockNumber[:])
	cBBlockCoinbase := C.CBytes(bBlockCoinbase)
	cBBmvmId := C.CBytes(mvmId.Bytes())
	defer C.free(unsafe.Pointer(cBFrom))
	defer C.free(unsafe.Pointer(cBTo))
	defer C.free(unsafe.Pointer(cBAmount))
	defer C.free(unsafe.Pointer(cBBlockNumber))
	defer C.free(unsafe.Pointer(cBBlockCoinbase))
	defer C.free(unsafe.Pointer(cBBmvmId))

	cRs := C.processNativeMintBurn(
		(*C.uchar)(cBFrom),
		(*C.uchar)(cBTo),
		(*C.uchar)(cBAmount),
		(C.ulonglong)(operationType),
		(C.ulonglong)(gasPrice),
		(C.ulonglong)(gasLimit),
		(C.ulonglong)(blockPrevrandao),
		(C.ulonglong)(blockGasLimit),
		(C.ulonglong)(blockTime),
		(C.ulonglong)(blockBaseFee),
		(*C.uchar)(cBBlockNumber),
		(*C.uchar)(cBBlockCoinbase),
		(*C.uchar)(cBBmvmId),
		C._Bool(isCache),
	)
	a.rs = extractExecuteResult(cRs)
	FlushNativeLogs(a.rs.NativeLogs) // TEE-packaging B2
	C.freeResult(cRs)
	a.enforceStrictAccessLists()
	return a.rs
}

func (a *MVMApi) NoncePlusOne(
	bSender []byte,
	gasPrice uint64,
	gasLimit uint64,
	blockPrevrandao uint64,
	blockGasLimit uint64,
	blockTime uint64,
	blockBaseFee uint64,
	blockNumber uint64,
	blockCoinbase common.Address,
	mvmId common.Address,
	isCache bool,
) *MVMExecuteResult {
	cBFrom := C.CBytes(bSender)
	bBlockNumber := [32]byte{}
	big.NewInt(int64(blockNumber)).FillBytes(bBlockNumber[:])
	bBlockCoinbase := blockCoinbase.Bytes()
	cBBlockNumber := C.CBytes(bBlockNumber[:])
	cBBlockCoinbase := C.CBytes(bBlockCoinbase)
	cBBmvmId := C.CBytes(mvmId.Bytes())
	defer C.free(unsafe.Pointer(cBFrom))
	defer C.free(unsafe.Pointer(cBBlockNumber))
	defer C.free(unsafe.Pointer(cBBlockCoinbase))
	defer C.free(unsafe.Pointer(cBBmvmId))

	cRs := C.noncePlusOne(
		(*C.uchar)(cBFrom),
		(C.ulonglong)(gasPrice),
		(C.ulonglong)(gasLimit),
		(C.ulonglong)(blockPrevrandao),
		(C.ulonglong)(blockGasLimit),
		(C.ulonglong)(blockTime),
		(C.ulonglong)(blockBaseFee),
		(*C.uchar)(cBBlockNumber),
		(*C.uchar)(cBBlockCoinbase),
		(*C.uchar)(cBBmvmId),
		C._Bool(isCache),
	)
	a.rs = extractExecuteResult(cRs)
	FlushNativeLogs(a.rs.NativeLogs) // TEE-packaging B2
	C.freeResult(cRs)
	a.enforceStrictAccessLists()
	return a.rs
}

func (a *MVMApi) Deploy(
	bSender []byte,
	bContractConstructor []byte,
	amount *big.Int,
	gasPrice uint64,
	gasLimit uint64,
	blockPrevrandao uint64,
	blockGasLimit uint64,
	blockTime uint64,
	blockBaseFee uint64,
	blockNumber uint64,
	blockCoinbase common.Address,
	mvmId common.Address,
	bTxHash []byte,
	isDebug bool,
	isCache bool,
	isOffChain bool,
) *MVMExecuteResult {
	bAmount := [32]byte{}
	amount.FillBytes(bAmount[:])
	constructorLength := len(bContractConstructor)
	cBSender := C.CBytes(bSender)
	cBContractConstructor := C.CBytes(bContractConstructor)
	cBAmount := C.CBytes(bAmount[:])
	bBlockNumber := [32]byte{}
	big.NewInt(int64(blockNumber)).FillBytes(bBlockNumber[:])
	bBlockCoinbase := blockCoinbase.Bytes()
	cBBlockNumber := C.CBytes(bBlockNumber[:])
	cBBlockCoinbase := C.CBytes(bBlockCoinbase)

	var randomizedMvmId common.Address
	if isOffChain {
		bBmvmIdBytes := make([]byte, 20)
		copy(bBmvmIdBytes[0:8], mvmId.Bytes()[0:8])
		val := atomic.AddUint64(&offChainCounter, 1)
		binary.BigEndian.PutUint64(bBmvmIdBytes[8:16], val)
		_, _ = rand.Read(bBmvmIdBytes[16:20])

		randomizedMvmId = common.BytesToAddress(bBmvmIdBytes)
		apiInstances.Store(randomizedMvmId, a)
		apiInstanceCount.Add(1)
		ProtectMVMApi(randomizedMvmId)
		defer func() {
			UnprotectMVMApi(randomizedMvmId)
			ClearMVMApi(randomizedMvmId)
		}()
	} else {
		randomizedMvmId = mvmId
	}

	cBBmvmId := C.CBytes(randomizedMvmId.Bytes())
	cBTxHash := C.CBytes(bTxHash)
	defer C.free(unsafe.Pointer(cBSender))
	defer C.free(unsafe.Pointer(cBContractConstructor))
	defer C.free(unsafe.Pointer(cBAmount))
	defer C.free(unsafe.Pointer(cBBlockNumber))
	defer C.free(unsafe.Pointer(cBBlockCoinbase))
	defer C.free(unsafe.Pointer(cBBmvmId))
	defer C.free(unsafe.Pointer(cBTxHash))
	b1ctx := a.buildB1Context(bContractConstructor, blockNumber)
	defer b1ctx.free()
	cRs := C.deploy(
		(*C.uchar)(cBSender),
		(*C.uchar)(cBContractConstructor),
		(C.int)(constructorLength),
		(*C.uchar)(cBAmount),
		(C.ulonglong)(gasPrice),
		(C.ulonglong)(gasLimit),
		(C.ulonglong)(blockPrevrandao),
		(C.ulonglong)(blockGasLimit),
		(C.ulonglong)(blockTime),
		(C.ulonglong)(blockBaseFee),
		(*C.uchar)(cBBlockNumber),
		(*C.uchar)(cBBlockCoinbase),
		(*C.uchar)(cBBmvmId),
		(*C.uchar)(cBTxHash),
		C._Bool(isDebug),
		C._Bool(isCache),
		C._Bool(isOffChain),
		(*C.uchar)(b1ctx.chainID),
		(*C.uchar)(b1ctx.blobHashes),
		b1ctx.blobHashCount,
		(*C.uchar)(b1ctx.blobBaseFee),
		(*C.uchar)(b1ctx.crossChainSender),
		(*C.uchar)(b1ctx.crossChainSource),
		(*C.uchar)(b1ctx.blockHashes),
		b1ctx.blockHashCount,
	)
	a.rs = extractExecuteResult(cRs)
	FlushNativeLogs(a.rs.NativeLogs) // TEE-packaging B2
	C.freeResult(cRs)
	a.enforceStrictAccessLists()
	return a.rs
}

func (a *MVMApi) enforceStrictAccessLists() {
	if a.rs == nil {
		return
	}

	// Optimistic Parallel Execution: bypass strict access list enforcement
	return
}

func (a *MVMApi) GetExecuteResult() *MVMExecuteResult {
	return a.rs
}

// globalStateGetCore is the pure-Go core of GlobalStateGet, extracted
// 2026-08-20 (plan §9's "Giai đoạn 3b") so the real-hardware reverse-call
// dispatcher can call it directly and encode the result over the wire
// codec, instead of only being reachable via the //export C signature
// (which the in-process cgo path still uses unchanged below). Mechanical
// extraction — zero behavior change, every branch/log line/precompile
// check identical to before. status: 0=not found (create fresh),
// 1=found, 3=Block-STM suspend (ErrEstimateHit) — matches
// mvm_tz_protocol.h's mvm_tz_global_state_get_resp_t exactly, no
// translation needed by callers.
func globalStateGetCore(fmvmId, fAddress common.Address) (status int32, balance, nonce, code []byte) {
	mvmApi := GetMVMApi(fmvmId)
	if mvmApi == nil {
		logger.Error("[GLOBAL_STATE_GET] ERROR: mvmApi is nil for mvmId=%v", fmvmId.Hex())
		log.Printf("mvmApi nil: %v", fmvmId)
		return 0, nil, nil, nil
	}

	isPrecompile := false
	if fAddress[0] == 0 && fAddress[1] == 0 && fAddress[2] == 0 && fAddress[3] == 0 &&
		fAddress[4] == 0 && fAddress[5] == 0 && fAddress[6] == 0 && fAddress[7] == 0 &&
		fAddress[8] == 0 && fAddress[9] == 0 && fAddress[10] == 0 && fAddress[11] == 0 &&
		fAddress[12] == 0 && fAddress[13] == 0 && fAddress[14] == 0 && fAddress[15] == 0 &&
		fAddress[16] == 0 && fAddress[17] == 0 {
		val := (uint16(fAddress[18]) << 8) | uint16(fAddress[19])
		if val >= 1 && val <= 409 {
			isPrecompile = true
		}
	}
	if !isPrecompile && fAddress == common.HexToAddress("0x00000000000000000000000000000000b429c0b2") {
		isPrecompile = true
	}

	if isPrecompile {
		logger.Debug("[GLOBAL_STATE_GET] Precompiled contract detected: %v", fAddress.Hex())
		b32 := uint256.NewInt(0).Bytes32()
		bNonce := [32]byte{}
		big.NewInt(0).FillBytes(bNonce[:])
		code = []byte{0x01}
		return 1, b32[:], bNonce[:], code
	}
	if mvmApi.extendedMode {
		if _, loaded := mvmApi.currentRelatedAddresses.LoadOrStore(fAddress, struct{}{}); !loaded {
			logger.Debug("add RelatedAddresses", fmvmId, fAddress)
		}
	}

	accountState, err := mvmApi.accountStateDb.AccountState(fAddress)
	if err != nil {
		if errors.Is(err, mvcc.ErrEstimateHit) {
			logger.Debug("[GLOBAL_STATE_GET] Suspend: ErrEstimateHit for %s", fAddress.Hex())
			return 3, nil, nil, nil
		}
		logger.Error("[GLOBAL_STATE_GET] ❌ AccountState err for %s, err=%v", fAddress.Hex(), err)
		return 0, nil, nil, nil
	}
	if accountState == nil {
		logger.Error("[GLOBAL_STATE_GET] ❌ AccountState nil for %s", fAddress.Hex())
		return 0, nil, nil, nil
	}

	bigBalance := big.NewInt(0).Add(
		accountState.Balance(),
		accountState.PendingBalance(),
	)

	b32Balance := [32]byte{}
	bigBalance.FillBytes(b32Balance[:])
	bigIntNonce := big.NewInt(0)
	bigIntNonce.SetUint64(accountState.Nonce())
	bNonce := [32]byte{}
	bigIntNonce.FillBytes(bNonce[:])
	var bCode []byte
	if smartContractState := accountState.SmartContractState(); smartContractState != nil {
		bCode = mvmApi.smartContractDb.Code(fAddress)
		logger.Debug("[GLOBAL_STATE_GET] Smart contract code loaded for %s, codeLen=%d, codeHash=%s, code=%s", fAddress.Hex(), len(bCode), smartContractState.CodeHash().Hex(), hex.EncodeToString(bCode))
	}

	return 1, b32Balance[:], bNonce[:], bCode
}

//export GlobalStateGet
func GlobalStateGet(
	mvmId *C.uchar,
	address *C.uchar,
) (
	status C.int,
	balance_p *C.uchar,
	nonce *C.uchar,
	code_p *C.uchar,
	code_length C.int,
) {
	bmvmId := C.GoBytes(unsafe.Pointer(mvmId), 20)
	fmvmId := common.BytesToAddress(bmvmId)

	bAddress := C.GoBytes(unsafe.Pointer(address), 20)
	fAddress := common.BytesToAddress(bAddress)

	st, balance, gNonce, code := globalStateGetCore(fmvmId, fAddress)
	if st == 0 || st == 3 {
		return C.int(st), nil, nil, nil, 0
	}
	// Không gửi con trỏ đi đâu cả, C++ sẽ tự quản lý.
	cBBalance := C.CBytes(balance)
	cBNonce := C.CBytes(gNonce)
	cBCode := C.CBytes(code)
	return C.int(st), (*C.uchar)(cBBalance), (*C.uchar)(cBNonce), (*C.uchar)(cBCode), C.int(len(code))
}

//export ClearProcessingPointers
func ClearProcessingPointers(mvmId *C.uchar) {
	// HÀM NÀY KHÔNG CÒN CẦN THIẾT và nên được XÓA KHỎI LỆNH GỌI PHÍA C++.
	// Nó được giữ lại ở đây để tránh lỗi linker nếu phía C++ chưa được cập nhật.
}

func TestMemLeak() {
	cRs := C.testMemLeak()
	rs := extractExecuteResult(cRs)
	logger.Debug("TestMemLeak: ", rs)
}

func TestMemLeakGs(addresses []common.Address) {
	totalAddress := len(addresses)
	var bAddress []byte
	for i := range totalAddress {
		bAddress = append(bAddress, addresses[i].Bytes()...)
	}
	cAddress := C.CBytes(bAddress)
	logger.Debug("TotalAddress", totalAddress)
	logger.Debug("bAddress", hex.EncodeToString(bAddress))
	C.testMemLeakGS(
		C.int(totalAddress),
		(*C.uchar)(cAddress),
	)
}

const (
	StorageStatusSuccess  C.int = 0
	StorageStatusNotFound C.int = 1
	StorageStatusSuspend  C.int = 2
)

// getStorageValueCore is the pure-Go core of GetStorageValue, extracted
// 2026-08-20 (plan §9's "Giai đoạn 3b") for the same reason as
// globalStateGetCore above — mechanical, zero behavior change, including
// a pre-existing quirk worth flagging rather than silently fixing here:
// a nil mvmApi returns status=0, which is StorageStatusSuccess, not a
// distinguishable "not found"/error status. Left as-is (out of scope for
// an extraction step); a real fix would need its own deliberate decision
// about what status a nil mvmApi should actually report on the wire.
// status matches mvm_tz_protocol.h's mvm_tz_get_storage_value_resp_t
// exactly (0=success, 1=not found, 2=suspend) — no translation needed.
func getStorageValueCore(fmvmId, fAddress common.Address, bKey []byte) (value []byte, status int32, mvmApiFound bool) {
	mvmApi := GetMVMApi(fmvmId)
	if mvmApi == nil {
		return nil, int32(StorageStatusSuccess), false
	}

	// STRICT ACCESS LIST CHECK FOR SLOAD
	if mvmApi.extendedMode {
		if _, loaded := mvmApi.currentRelatedAddresses.LoadOrStore(fAddress, struct{}{}); !loaded {
			logger.Debug("add RelatedAddresses", fmvmId, fAddress)
		}
	}

	logger.Debug("GetStorageValue address: ", fAddress, hex.EncodeToString(bKey))
	bValue, success := mvmApi.smartContractDb.StorageValue(fAddress, bKey)

	retStatus := StorageStatusSuccess
	if !success {
		retStatus = StorageStatusNotFound
	}

	if mvccDB, ok := mvmApi.smartContractDb.(*mvcc.MVCCSmartContractDB); ok {
		if mvccDB.BlockingVersion != mvcc.BaseVersion {
			retStatus = StorageStatusSuspend
		}
	}

	return bValue, int32(retStatus), true
}

//export GetStorageValue
func GetStorageValue(
	mvmId *C.uchar,
	address *C.uchar,
	key *C.uchar,
) (value *C.uchar, status C.int) {
	bmvmId := C.GoBytes(unsafe.Pointer(mvmId), 20)
	fmvmId := common.BytesToAddress(bmvmId)
	bAddress := C.GoBytes(unsafe.Pointer(address), 20)
	bKey := C.GoBytes(unsafe.Pointer(key), 32)
	fAddress := common.BytesToAddress(bAddress)

	bValue, retStatus, found := getStorageValueCore(fmvmId, fAddress, bKey)
	if !found {
		// Preserves the original nil-mvmApi short-circuit exactly: no
		// C.CBytes call at all, a true nil pointer (not a CBytes(nil)
		// pointer-to-zero-bytes, which is a different C-level value).
		return nil, C.int(retStatus)
	}
	cValue := C.CBytes(bValue)
	// Không gửi con trỏ đi đâu cả, C++ sẽ tự quản lý.
	return (*C.uchar)(cValue), C.int(retStatus)
}

//export GetBlockHash
func GetBlockHash(blockNumber C.int) C.struct_Value_return {
	hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(uint64(blockNumber))
	if !ok {
		return C.struct_Value_return{data_p: nil, data_size: 0, success: false}
	}
	hashBytes := hash.Bytes()
	data_p := (*C.uchar)(C.CBytes(hashBytes))
	// Không gửi con trỏ đi đâu cả, C++ sẽ tự quản lý.
	return C.struct_Value_return{
		data_p:    data_p,
		data_size: C.int(len(hashBytes)),
		success:   true,
	}
}

//export GetChainId
func GetChainId() C.struct_Value_return {
	chainId := config.ConfigApp.ChainId
	u256ChainId := uint256.NewInt(chainId.Uint64())
	chainIdBytes := u256ChainId.Bytes()
	if len(chainIdBytes) < 32 {
		padding := make([]byte, 32-len(chainIdBytes))
		chainIdBytes = append(padding, chainIdBytes...)
	}
	data_p := (*C.uchar)(C.CBytes(chainIdBytes))
	// Không gửi con trỏ đi đâu cả, C++ sẽ tự quản lý.
	return C.struct_Value_return{
		data_p:    data_p,
		data_size: C.int(len(chainIdBytes)),
		success:   true,
	}
}

// ═══════════════════════════════════════════════════════════════════════
// CROSS-CHAIN PRECOMPILE CONTEXT (address 263)
// ═══════════════════════════════════════════════════════════════════════
//
// Go handler set context trên MVMApi instance trước khi gọi EVM.
// C++ callback nhận mvmId → lookup MVMApi → lấy giá trị.
// Không dùng global → thread-safe, nhiều cross-chain TX song song OK.
func (a *MVMApi) SetCrossChainContext(sender common.Address, sourceChainId uint64) {
	a.crossChainSender = sender
	a.crossChainSourceId = sourceChainId
	a.crossChainActive = true
}

// ClearCrossChainContext reset context sau khi execute xong (defer).
func (a *MVMApi) ClearCrossChainContext() {
	a.crossChainSender = common.Address{}
	a.crossChainSourceId = 0
	a.crossChainActive = false
}

//export GetCrossChainSender
func GetCrossChainSender(mvmId *C.uchar) C.struct_Value_return {
	bmvmId := C.GoBytes(unsafe.Pointer(mvmId), 20)
	fmvmId := common.BytesToAddress(bmvmId)
	mvmApi := GetMVMApi(fmvmId)

	fmt.Printf("[CROSS-CHAIN-DEBUG-GO] GetCrossChainSender called for mvmId: %s\n", fmvmId.Hex())

	if mvmApi == nil || !mvmApi.crossChainActive {
		fmt.Printf("[CROSS-CHAIN-DEBUG-GO] ⚠️ mvmApi == nil (%v) OR !crossChainActive for mvmId: %s\n", mvmApi == nil, fmvmId.Hex())
		return C.struct_Value_return{data_p: nil, data_size: 0, success: false}
	}

	// ABI-encode address: pad to 32 bytes (12 bytes zero + 20 bytes address)
	result := make([]byte, 32)
	copy(result[12:], mvmApi.crossChainSender.Bytes())

	fmt.Printf("[CROSS-CHAIN-DEBUG-GO] ✅ Returning sender: %s\n", mvmApi.crossChainSender.Hex())

	data_p := (*C.uchar)(C.CBytes(result))
	return C.struct_Value_return{
		data_p:    data_p,
		data_size: C.int(32),
		success:   true,
	}
}

//export GetCrossChainSourceId
func GetCrossChainSourceId(mvmId *C.uchar) C.struct_Value_return {
	bmvmId := C.GoBytes(unsafe.Pointer(mvmId), 20)
	fmvmId := common.BytesToAddress(bmvmId)
	mvmApi := GetMVMApi(fmvmId)

	fmt.Printf("[CROSS-CHAIN-DEBUG-GO] GetCrossChainSourceId called for mvmId: %s\n", fmvmId.Hex())

	if mvmApi == nil || !mvmApi.crossChainActive {
		fmt.Printf("[CROSS-CHAIN-DEBUG-GO] ⚠️ mvmApi == nil (%v) OR !crossChainActive for mvmId: %s\n", mvmApi == nil, fmvmId.Hex())
		return C.struct_Value_return{data_p: nil, data_size: 0, success: false}
	}

	// ABI-encode uint256: big-endian 32 bytes
	u256 := uint256.NewInt(mvmApi.crossChainSourceId)
	sourceIdBytes := u256.Bytes32()

	fmt.Printf("[CROSS-CHAIN-DEBUG-GO] ✅ Returning sourceChainId: %d\n", mvmApi.crossChainSourceId)

	data_p := (*C.uchar)(C.CBytes(sourceIdBytes[:]))
	return C.struct_Value_return{
		data_p:    data_p,
		data_size: C.int(32),
		success:   true,
	}
}

//export GetBlobHash
func GetBlobHash(mvmId *C.uchar, index C.ulonglong) C.struct_Value_return {
	bmvmId := C.GoBytes(unsafe.Pointer(mvmId), 20)
	fmvmId := common.BytesToAddress(bmvmId)
	mvmApi := GetMVMApi(fmvmId)

	if mvmApi == nil {
		return C.struct_Value_return{data_p: nil, data_size: 0, success: false}
	}
	hash, ok := mvmApi.blobHashAt(uint64(index))
	if !ok {
		// Out of range: EIP-4844 says BLOBHASH must resolve to 0, not error —
		// the C++ side (MyGlobalState::get_blob_hash) treats success=false the
		// same as "return 0", so this is correct either way.
		return C.struct_Value_return{data_p: nil, data_size: 0, success: false}
	}
	data_p := (*C.uchar)(C.CBytes(hash))
	return C.struct_Value_return{
		data_p:    data_p,
		data_size: C.int(len(hash)),
		success:   true,
	}
}

//export GetBlobBaseFee
func GetBlobBaseFee(mvmId *C.uchar) C.struct_Value_return {
	bmvmId := C.GoBytes(unsafe.Pointer(mvmId), 20)
	fmvmId := common.BytesToAddress(bmvmId)
	mvmApi := GetMVMApi(fmvmId)

	if mvmApi == nil || mvmApi.blobBaseFee == nil {
		return C.struct_Value_return{data_p: nil, data_size: 0, success: false}
	}
	feeBytes := mvmApi.blobBaseFee.Bytes32()
	data_p := (*C.uchar)(C.CBytes(feeBytes[:]))
	return C.struct_Value_return{
		data_p:    data_p,
		data_size: C.int(32),
		success:   true,
	}
}

// ClearAllStateInstances clears the C++ state cache
// This is necessary when snapshot state changes or sync process resets the chain
func ClearAllStateInstances() {
	C.clearAllStateInstances()
}
// CommitAllXapian forces all XapianManager instances in C++ to commit their data to disk
func CommitAllXapian() {
	C.MVM_commitAllXapian()
}
func ClearXapianTxBuffer(txHash []byte) {
	if len(txHash) == 0 {
		return
	}
	cTxHash := C.CBytes(txHash)
	defer C.free(cTxHash)
	C.clear_xapian_tx_buffer((*C.uchar)(cTxHash))
}

func CommitXapianTxBuffer(txHash []byte) {
	if len(txHash) == 0 {
		return
	}
	cTxHash := C.CBytes(txHash)
	defer C.free(cTxHash)
	C.commit_xapian_tx_buffer((*C.uchar)(cTxHash))
}

func ClearXapianTxBufferBatch(txHashes [][]byte) {
	if len(txHashes) == 0 {
		return
	}
	// Flatten array of hashes
	count := len(txHashes)
	flattened := make([]byte, 0, count*32)
	for _, hash := range txHashes {
		if len(hash) != 32 {
			continue // Should not happen, but safe guard
		}
		flattened = append(flattened, hash...)
	}
	if len(flattened) == 0 {
		return
	}
	cTxHashes := C.CBytes(flattened)
	defer C.free(cTxHashes)
	C.clear_xapian_tx_buffer_batch((*C.uchar)(cTxHashes), C.int(len(flattened)/32))
}

func CommitXapianTxBufferBatch(txHashes [][]byte) {
	if len(txHashes) == 0 {
		return
	}
	// Flatten array of hashes
	count := len(txHashes)
	flattened := make([]byte, 0, count*32)
	for _, hash := range txHashes {
		if len(hash) != 32 {
			continue // Should not happen, but safe guard
		}
		flattened = append(flattened, hash...)
	}
	if len(flattened) == 0 {
		return
	}
	cTxHashes := C.CBytes(flattened)
	defer C.free(cTxHashes)
	C.commit_xapian_tx_buffer_batch((*C.uchar)(cTxHashes), C.int(len(flattened)/32))
}


