package mvm

/*
#cgo CFLAGS: -w -O3 -march=native -mtune=native
#cgo CXXFLAGS: -std=c++17 -w -O3 -march=native -mtune=native
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

*/
import "C"
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"math/rand"
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
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types"
)

var (
	apiInstances          sync.Map
	protectedApiInstances sync.Map
	mvmIdCounter          uint64
)

// GenerateUniqueMvmId tạo một mvmId độc nhất, sẽ tạo lại nếu bị trùng với cache hiện tại.
func GenerateUniqueMvmId() common.Address {
	for {
		count := atomic.AddUint64(&mvmIdCounter, 1)
		// Băm kết hợp thời gian và counter để đảm bảo tỉ lệ trùng lặp gần như bằng 0
		hash := sha256.Sum256([]byte(fmt.Sprintf("mvm-temp-%d-%d-%d", time.Now().UnixNano(), rand.Int63(), count)))

		// Lấy 20 bytes cuối làm Address
		mvmId := common.Address(hash[12:])

		// Kiểm tra xem mvmId này đã tồn tại trong mvm cache chưa, nếu chưa thì lấy cái này
		if GetMVMApi(mvmId) == nil {
			return mvmId
		}
	}
}

type AccountStateDB interface {
	AccountState(address common.Address) (types.AccountState, error)
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
	for addrHex, logData := range logs {
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
// CallClearAllStateInstances clears the C++ EVM's internal global state cache
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
	// Không cần khởi động goroutine dọn dẹp nữa.
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
	return newApi
}

func LenApiInstances() int {
	count := 0
	apiInstances.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}

func RemoveOldApiInstances() {
	const targetSize = 50000
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
	sort.Slice(instancesToRemove, func(i, j int) bool {
		return instancesToRemove[i].createdAt.Before(instancesToRemove[j].createdAt)
	})

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
	logger.Info("🧹 [MVM CLEANUP] scan=%v, total=%d, unprotected=%d, removed=%d",
		scanDuration, currentTotalCount, len(instancesToRemove), deletedCount)
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
	instance, ok := apiInstances.Load(mvmId)
	if !ok {
		return
	}
	mvmApi, ok := instance.(*MVMApi)
	if !ok || mvmApi == nil {
		apiInstances.Delete(mvmId)
		logger.Debug("Removed invalid/nil MVMApi entry from map:", mvmId.Hex())
		return
	}
	apiInstances.Delete(mvmId)
	logger.Debug("Cleared unprotected MVMApi instance:", mvmId.Hex())
}

func ClearAllMVMApi() {
	logger.Info("Clearing all unprotected MVMApi instances...")
	apiInstances.Range(func(key, value interface{}) bool {
		mvmId := key.(common.Address)
		if _, protected := protectedApiInstances.Load(mvmId); !protected {
			logger.Error("ClearMVM: 7", mvmId)
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
	cBBmvmId := C.CBytes(mvmId.Bytes())
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
	)
	a.rs = extractExecuteResult(cRs)
	C.freeResult(cRs)
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
	)
	a.rs = extractExecuteResult(cRs)
	C.freeResult(cRs)
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
	)
	a.rs = extractExecuteResult(cRs)
	C.freeResult(cRs)
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
	)
	a.rs = extractExecuteResult(cRs)
	C.freeResult(cRs)
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
	)
	a.rs = extractExecuteResult(cRs)
	C.freeResult(cRs)
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
	cBBmvmId := C.CBytes(mvmId.Bytes())
	cBTxHash := C.CBytes(bTxHash)
	defer C.free(unsafe.Pointer(cBSender))
	defer C.free(unsafe.Pointer(cBContractConstructor))
	defer C.free(unsafe.Pointer(cBAmount))
	defer C.free(unsafe.Pointer(cBBlockNumber))
	defer C.free(unsafe.Pointer(cBBlockCoinbase))
	defer C.free(unsafe.Pointer(cBBmvmId))
	defer C.free(unsafe.Pointer(cBTxHash))
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
	)
	a.rs = extractExecuteResult(cRs)
	C.freeResult(cRs)
	return a.rs
}

func (a *MVMApi) GetExecuteResult() *MVMExecuteResult {
	return a.rs
}
func (a *MVMApi) CommitFullDb() bool {
	if a == nil {
		return false
	}
	mvmId := a.key
	cBBmvmId := C.CBytes(mvmId.Bytes())
	defer C.free(unsafe.Pointer(cBBmvmId))
	status := C.commit_full_db((*C.uchar)(cBBmvmId))
	return status != 0
}
func (a *MVMApi) RevertFullDb() bool {
	if a == nil {
		return false
	}
	mvmId := a.key
	cBBmvmId := C.CBytes(mvmId.Bytes())
	defer C.free(unsafe.Pointer(cBBmvmId))
	status := C.revert_full_db((*C.uchar)(cBBmvmId))
	return status != 0
}

var (
	// Biến này không còn được sử dụng và có thể xóa.
	processingPointers []unsafe.Pointer
)

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

	mvmApi := GetMVMApi(fmvmId)
	if mvmApi == nil {
		logger.Error("[GLOBAL_STATE_GET] ERROR: mvmApi is nil for mvmId=%v", fmvmId.Hex())
		log.Printf("mvmApi nil: %v", fmvmId)
		return C.int(0), nil, nil, nil, 0
	}

	if fAddress == common.HexToAddress("0x0000000000000000000000000000000000000101") ||
		fAddress == common.HexToAddress("0x0000000000000000000000000000000000000102") ||
		fAddress == common.HexToAddress("0x0000000000000000000000000000000000000004") {
		logger.Debug("[GLOBAL_STATE_GET] Precompiled contract detected: %v", fAddress.Hex())
		balance := uint256.NewInt(0).Bytes32()
		cBBalance := C.CBytes(balance[:])
		bNonce := [32]byte{}
		big.NewInt(0).FillBytes(bNonce[:])
		cBNonce := C.CBytes(bNonce[:])
		code := []byte{0x01}
		cBCode := C.CBytes(code)
		// Không gửi con trỏ đi đâu cả, C++ sẽ tự quản lý.
		return C.int(1), (*C.uchar)(cBBalance), (*C.uchar)(cBNonce), (*C.uchar)(cBCode), C.int(len(code))
	}
	if mvmApi.extendedMode {
		if _, loaded := mvmApi.currentRelatedAddresses.LoadOrStore(fAddress, struct{}{}); !loaded {
			logger.Debug("add RelatedAddresses", fmvmId, fAddress)
		}
	} else {
		if !mvmApi.InRelatedAddress(fAddress) {
			logger.Error("❌ [DEBUG Exception 15] Address not in RelatedAddresses: %s for mvmId: %s", fAddress.Hex(), fmvmId.Hex())
			return C.int(2), nil, nil, nil, 0
		}
	}

	accountState, err := mvmApi.accountStateDb.AccountState(fAddress)
	if err != nil || accountState == nil {
		logger.Error("[GLOBAL_STATE_GET] ❌ AccountState nil for %s, err=%v", fAddress.Hex(), err)
		return C.int(0), nil, nil, nil, 0
	}

	bigBalance := big.NewInt(0).Add(
		accountState.Balance(),
		accountState.PendingBalance(),
	)

	b32Balance := [32]byte{}
	bigBalance.FillBytes(b32Balance[:])
	cBBalance := C.CBytes(b32Balance[:])
	bigIntNonce := big.NewInt(0)
	bigIntNonce.SetUint64(accountState.Nonce())
	bNonce := [32]byte{}
	bigIntNonce.FillBytes(bNonce[:])
	cBNonce := C.CBytes(bNonce[:])
	var bCode []byte
	if smartContractState := accountState.SmartContractState(); smartContractState != nil {
		bCode = mvmApi.smartContractDb.Code(fAddress)
		logger.Debug("[GLOBAL_STATE_GET] Smart contract code loaded, codeLen=%d", len(bCode))
	}

	cBCode := C.CBytes(bCode)
	// Không gửi con trỏ đi đâu cả, C++ sẽ tự quản lý.
	return C.int(1), (*C.uchar)(cBBalance), (*C.uchar)(cBNonce), (*C.uchar)(cBCode), C.int(len(bCode))
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

//export GetStorageValue
func GetStorageValue(
	mvmId *C.uchar,
	address *C.uchar,
	key *C.uchar,
) (value *C.uchar, success bool) {
	bmvmId := C.GoBytes(unsafe.Pointer(mvmId), 20)
	fmvmId := common.BytesToAddress(bmvmId)
	mvmApi := GetMVMApi(fmvmId)
	if mvmApi == nil {
		return nil, false
	}
	bAddress := C.GoBytes(unsafe.Pointer(address), 20)
	bKey := C.GoBytes(unsafe.Pointer(key), 32)
	fAddress := common.BytesToAddress(bAddress)
	logger.Debug("GetStorageValue address: ", fAddress, hex.EncodeToString(bKey))
	bValue, success := mvmApi.smartContractDb.StorageValue(fAddress, bKey)
	cValue := C.CBytes(bValue)
	// Không gửi con trỏ đi đâu cả, C++ sẽ tự quản lý.
	return (*C.uchar)(cValue), success
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

// ClearAllStateInstances clears the C++ state cache
// This is necessary when snapshot state changes or sync process resets the chain
func ClearAllStateInstances() {
	C.clearAllStateInstances()
}
