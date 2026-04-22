package mvm

/*
#cgo CFLAGS: -w
#cgo CXXFLAGS: -std=c++17 -w
#cgo LDFLAGS: -L./linker/build/lib/static -lmvm_linker -L./c_mvm/build/lib/static -lmvm -lstdc++
#cgo CPPFLAGS: -I./linker/build/include
#include "mvm_linker.hpp"
#include <stdlib.h>
*/
import "C"
import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"unsafe"

	"github.com/ethereum/go-ethereum/crypto"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

func extractExecuteResult(cExecuteResult *C.struct_ExecuteResult) *MVMExecuteResult {
	// ... (các phần trích xuất khác không đổi) ...

	mapAddBalance := extractAddBalance(cExecuteResult)
	mapSubBalance := extractSubBalance(cExecuteResult)
	mapNonce := extractNonce(cExecuteResult)
	mapCodeChange, mapCodeHash := extractCodeChange(cExecuteResult)
	mapStorageChange := extractStorageChange(cExecuteResult)
	jEventLogs := extractEventLogs(cExecuteResult) // JSON logs từ log_handler

	uptrEx := unsafe.Pointer(cExecuteResult.b_exmsg)
	exmsg := string(C.GoBytes(uptrEx, cExecuteResult.length_exmsg))

	MapFullDbHash := extractMapFullDbHash(cExecuteResult)
	mapFullDbLogs := extractFullDbLogs(cExecuteResult) // <-- Gọi hàm mới

	uptrOut := unsafe.Pointer(cExecuteResult.b_output)
	rt := C.GoBytes(uptrOut, cExecuteResult.length_output)

	gasUsed := uint64(cExecuteResult.gas_used)

	// ... (xác định status, exception) ...
	status := pb.RECEIPT_STATUS(cExecuteResult.b_exitReason)
	var exception pb.EXCEPTION
	// 🐛 FIX: Only read exception for THREW status.
	// C++ ExecResult::ex defaults to 0 = ErrOutOfGas (first enum value).
	// When EVM halts normally (e.g. constructor runs to end), er=halted(1)
	// but ex stays at default 0. Previously we read ex for HALTED too,
	// causing false "out of gas" errors.
	if status == pb.RECEIPT_STATUS_THREW {
		exception = pb.EXCEPTION(cExecuteResult.b_exception)
	} else {
		exception = pb.EXCEPTION_NONE
	}

	// Tạo struct kết quả, bao gồm cả mapFullDbLogs
	MVMEx := &MVMExecuteResult{
		MapAddBalance:    mapAddBalance,
		MapSubBalance:    mapSubBalance,
		MapNonce:         mapNonce,
		MapCodeChange:    mapCodeChange,
		MapCodeHash:      mapCodeHash,
		MapStorageChange: mapStorageChange,
		JEventLogs:       jEventLogs, // JSON logs
		Status:           status,
		Exception:        exception,
		Exmsg:            exmsg,
		Return:           rt,
		MapFullDbHash:    MapFullDbHash,
		MapFullDbLogs:    mapFullDbLogs,
		GasUsed:          gasUsed,
	}
	processDecodedLogs(MVMEx)

	return MVMEx
}

// extract funcs
func extractMapFullDbHash(
	cExecuteResult *C.struct_ExecuteResult,
) (
	MapFullDbHash map[string][]byte,
) {
	count := int(cExecuteResult.length_full_db_hash)

	MapFullDbHash = make(map[string][]byte, count)

	if count == 0 {
		return // Trả về map rỗng nếu không có dữ liệu
	}

	if cExecuteResult.full_db_hash == nil {
		return
	}
	if cExecuteResult.length_full_db_hashes == nil {
		return
	}

	// Bây giờ mới an toàn để lấy slice
	cPtrArray := unsafe.Slice(cExecuteResult.full_db_hash, count)
	cLenArray := unsafe.Slice(cExecuteResult.length_full_db_hashes, count)

	for i := 0; i < count; i++ {
		cPtr := cPtrArray[i]
		cLen := int(cLenArray[i])

		if cPtr == nil {
			continue
		}

		expectedLen := 64
		if cLen != expectedLen {
			if cLen < expectedLen {
				continue
			}
			cLen = expectedLen
		}

		// Chuyển đổi dữ liệu C thành Go byte slice (copy dữ liệu)
		goBytes := C.GoBytes(unsafe.Pointer(cPtr), C.int(cLen))

		if len(goBytes) < expectedLen {
			continue
		}
		// Trích xuất địa chỉ (32 bytes đầu, lấy 20 byte cuối)
		addressBytes := goBytes[12:32]
		addressHex := hex.EncodeToString(addressBytes)

		// Trích xuất hash (32 bytes tiếp theo)
		hashBytes := goBytes[32:64]
		// Thêm vào map Go (copy hashBytes để đảm bảo an toàn)
		valueCopy := make([]byte, len(hashBytes))
		copy(valueCopy, hashBytes)
		MapFullDbHash[addressHex] = valueCopy
	}

	return
}

// extract funcs
func extractAddBalance(
	cExecuteResult *C.struct_ExecuteResult,
) (
	mapAddBalance map[string][]byte,
) {
	// extract add balance
	bAddBalanceChange := unsafe.Slice(cExecuteResult.b_add_balance_change, cExecuteResult.length_add_balance_change)
	mapAddBalance = make(map[string][]byte, len(bAddBalanceChange))
	for _, v := range bAddBalanceChange {
		uptr := unsafe.Pointer(v)
		addrWithAddBalanceChange := C.GoBytes(uptr, (C.int)(64))
		// C.free(uptr)
		mapAddBalance[hex.EncodeToString(addrWithAddBalanceChange[12:32])] = addrWithAddBalanceChange[32:]
	}
	// C.free(unsafe.Pointer(cExecuteResult.b_add_balance_change))
	return
}

func extractSubBalance(
	cExecuteResult *C.struct_ExecuteResult,
) (
	mapSubBalance map[string][]byte,
) {
	bSubBalanceChange := unsafe.Slice(cExecuteResult.b_sub_balance_change, cExecuteResult.length_sub_balance_change)
	mapSubBalance = make(map[string][]byte, len(bSubBalanceChange))
	for _, v := range bSubBalanceChange {
		uptr := unsafe.Pointer(v)
		addrWithSubBalanceChange := C.GoBytes(uptr, (C.int)(64))
		// C.free(uptr)
		mapSubBalance[hex.EncodeToString(addrWithSubBalanceChange[12:32])] = addrWithSubBalanceChange[32:]
	}
	// C.free(unsafe.Pointer(cExecuteResult.b_sub_balance_change))
	return
}

// extract funcs
func extractNonce(
	cExecuteResult *C.struct_ExecuteResult,
) (
	mapNonce map[string][]byte,
) {
	// extract add balance
	bNonceChange := unsafe.Slice(cExecuteResult.b_nonce_change, cExecuteResult.length_nonce_change)
	mapNonce = make(map[string][]byte, len(bNonceChange))
	for _, v := range bNonceChange {
		uptr := unsafe.Pointer(v)
		addrWithNonceChange := C.GoBytes(uptr, (C.int)(64))
		// C.free(uptr)
		mapNonce[hex.EncodeToString(addrWithNonceChange[12:32])] = addrWithNonceChange[32:]
	}
	// C.free(unsafe.Pointer(cExecuteResult.b_add_balance_change))
	return
}

func extractCodeChange(
	cExecuteResult *C.struct_ExecuteResult,
) (
	mapCodeChange map[string][]byte,
	mapCodeHash map[string][]byte,
) {
	mapCodeChange = make(map[string][]byte, cExecuteResult.length_code_change)
	mapCodeHash = make(map[string][]byte, cExecuteResult.length_code_change)

	bCodeChange := unsafe.Slice(cExecuteResult.b_code_change, cExecuteResult.length_code_change)
	cLengthCodes := unsafe.Slice(cExecuteResult.length_codes, cExecuteResult.length_code_change)
	lengthCodes := make([]int, cExecuteResult.length_code_change)
	for i, v := range cLengthCodes {
		lengthCodes[i] = int(v)
	}

	for i, v := range lengthCodes {
		sptr := unsafe.Pointer(bCodeChange[i])
		uptr := unsafe.Pointer(sptr)
		addrWithCode := C.GoBytes(uptr, (C.int)(v))
		address := hex.EncodeToString(addrWithCode[12:32])
		code := addrWithCode[32:]
		mapCodeChange[address] = code
		mapCodeHash[address] = crypto.Keccak256(code)
	}

	return
}

func extractStorageChange(
	cExecuteResult *C.struct_ExecuteResult,
) (
	mapStorageChange map[string]map[string][]byte,
) {
	// extract storage changes
	mapStorageChange = make(map[string]map[string][]byte, cExecuteResult.length_storage_change)

	bStorageChange := unsafe.Slice(cExecuteResult.b_storage_change, cExecuteResult.length_storage_change)
	cLengthStorages := unsafe.Slice(cExecuteResult.length_storages, cExecuteResult.length_storage_change)
	lengthStorages := make([]int, cExecuteResult.length_storage_change)
	for i, v := range cLengthStorages {
		lengthStorages[i] = int(v)
	}

	for i, v := range lengthStorages {
		sprt := unsafe.Pointer(bStorageChange[i])
		addrWithStorageChanges := C.GoBytes(sprt, (C.int)(v+32))
		// C.free(sprt)
		address := hex.EncodeToString(addrWithStorageChanges[12:32])
		addrWithStorageChanges = addrWithStorageChanges[32:]
		storageCount := v / 64
		mapStorageChange[address] = make(map[string][]byte, storageCount)
		for j := 0; j < storageCount; j++ {
			// 32 bytes for key, 32 bytes for value
			key := hex.EncodeToString(addrWithStorageChanges[j*64 : j*64+32])
			value := addrWithStorageChanges[j*64+32 : (j+1)*64]
			mapStorageChange[address][key] = value
		}
	}

	return
}

func extractEventLogs(
	cExecuteResult *C.struct_ExecuteResult,
) (
	logJson LogsJson,
) {
	sptr := unsafe.Pointer(cExecuteResult.b_logs)
	rawLogs := C.GoBytes(sptr, cExecuteResult.length_logs)
	// C.free(sptr)
	json.Unmarshal(rawLogs, &logJson.Logs)
	return
}

// Hàm mới để trích xuất full_db_logs
func extractFullDbLogs(
	cExecuteResult *C.struct_ExecuteResult,
) (
	mapFullDbLogs map[string][]byte, // Map từ address hex string sang serialized log bytes
) {
	count := int(cExecuteResult.length_full_db_logs) // Lấy số lượng log groups

	mapFullDbLogs = make(map[string][]byte, count) // Khởi tạo map

	if count == 0 {
		return // Trả về map rỗng nếu không có dữ liệu
	}

	// *** KIỂM TRA CON TRỎ NIL TRƯỚC KHI DÙNG unsafe.Slice ***
	if cExecuteResult.full_db_logs == nil {
		return // Trả về map rỗng (hoặc panic tùy logic xử lý lỗi)
	}
	if cExecuteResult.length_full_db_logs_data == nil {
		return // Trả về map rỗng
	}

	// Bây giờ mới an toàn để lấy slice từ các con trỏ C
	cLogPtrArray := unsafe.Slice(cExecuteResult.full_db_logs, count)
	cLogLenArray := unsafe.Slice(cExecuteResult.length_full_db_logs_data, count)

	for i := 0; i < count; i++ {
		cPtr := cLogPtrArray[i]      // Con trỏ tới dữ liệu (Address + SerializedLog)
		cLen := int(cLogLenArray[i]) // Độ dài của dữ liệu (32 + serialized log size)

		if cPtr == nil {
			continue
		}

		// Kiểm tra độ dài tối thiểu (phải đủ chứa Address)
		if cLen < 32 {
			continue
		}

		// Chuyển đổi dữ liệu C thành Go byte slice (copy dữ liệu)
		goBytes := C.GoBytes(unsafe.Pointer(cPtr), C.int(cLen))

		if len(goBytes) != cLen {
			// C.GoBytes có thể trả về slice ngắn hơn nếu có lỗi cấp phát bộ nhớ trong Go runtime
			continue
		}
		// fmt.Printf("[DEBUG] extractFullDbLogs: Log Element %d raw bytes (%d): %s\n", i, len(goBytes), hex.EncodeToString(goBytes)) // Log raw bytes nếu cần debug sâu

		// Trích xuất địa chỉ (32 bytes đầu, lấy 20 byte cuối)
		addressBytes := goBytes[12:32]
		addressHex := hex.EncodeToString(addressBytes)

		// Trích xuất dữ liệu log đã serialize (từ byte 32 đến hết)
		logDataBytes := goBytes[32:]

		// Thêm vào map Go (copy logDataBytes để đảm bảo an toàn)
		logValueCopy := make([]byte, len(logDataBytes))
		copy(logValueCopy, logDataBytes)
		mapFullDbLogs[addressHex] = logValueCopy
	}

	return
}

// Hàm min helper (Go không có sẵn)
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Định nghĩa các loại Operation tương ứng với C++ enum
type XapianOperation uint8

const (
	OpUnknown   XapianOperation = 0
	OpNewDoc    XapianOperation = 1
	OpDelDoc    XapianOperation = 2
	OpAddValue  XapianOperation = 3
	OpAddTerm   XapianOperation = 4
	OpSetData   XapianOperation = 5
	OpIndexText XapianOperation = 6
)

// --- Structs cho data của từng operation ---
type NewDocData struct {
	DocID uint32
	Data  string
}

type DelDocData struct {
	DocID uint32
}

type AddValueData struct {
	DocID        uint32
	Slot         uint32
	Value        string
	IsSerialised bool
}

type AddTermData struct {
	DocID uint32
	Term  string
}

type SetDataData struct {
	DocID uint32
	Data  string
}

type IndexTextData struct {
	DocID  uint32
	Text   string
	WdfInc uint32
	Prefix string
}

// --- Struct LogEntry chính ---
// Sử dụng interface{} hoặc các struct riêng lẻ thay vì variant
type GoLogEntry struct {
	Op   XapianOperation
	Data interface{} // Hoặc định nghĩa các trường cụ thể nếu không muốn dùng interface{}
}

// --- Struct ComprehensiveLog chính ---
type GoComprehensiveLog struct {
	DbName        string       `json:"dbName"` // Thêm tag JSON
	XapianDocLogs []GoLogEntry `json:"xapianDocLogs"`
}

// --- Các hàm helper để đọc dữ liệu nhị phân ---

var errOutOfBounds = errors.New("read out of bounds")

// Đọc uint32 Big Endian
func readUint32BE(data []byte, offset *int) (uint32, error) {
	if *offset+4 > len(data) {
		return 0, fmt.Errorf("readUint32BE: %w at offset %d (len %d)", errOutOfBounds, *offset, len(data))
	}
	val := binary.BigEndian.Uint32(data[*offset : *offset+4])
	*offset += 4
	return val, nil
}

// Đọc uint8
func readUint8(data []byte, offset *int) (uint8, error) {
	if *offset+1 > len(data) {
		return 0, fmt.Errorf("readUint8: %w at offset %d (len %d)", errOutOfBounds, *offset, len(data))
	}
	val := data[*offset]
	*offset += 1
	return val, nil
}

// Đọc bool (lưu dưới dạng uint8)
func readBoolU8(data []byte, offset *int) (bool, error) {
	val, err := readUint8(data, offset)
	if err != nil {
		return false, fmt.Errorf("readBoolU8: %w", err)
	}
	return val != 0, nil
}

// Đọc một số lượng bytes cụ thể
func readBytes(data []byte, offset *int, length uint32) ([]byte, error) {
	if *offset+int(length) > len(data) {
		// Kiểm tra tràn số học tiềm ẩn trước khi báo lỗi out of bounds
		if uint64(*offset)+uint64(length) > uint64(len(data)) {
			return nil, fmt.Errorf("readBytes: calculated end position overflows or %w at offset %d with length %d (len %d)", errOutOfBounds, *offset, length, len(data))
		}
		return nil, fmt.Errorf("readBytes: %w at offset %d with length %d (len %d)", errOutOfBounds, *offset, length, len(data))
	}
	val := data[*offset : *offset+int(length)]
	*offset += int(length)
	// Trả về bản sao để tránh sửa đổi ngoài ý muốn slice gốc
	retVal := make([]byte, length)
	copy(retVal, val)
	return retVal, nil
}

// Đọc string có tiền tố độ dài uint32
func readLengthPrefixedString(data []byte, offset *int) (string, error) {
	length, err := readUint32BE(data, offset)
	if err != nil {
		return "", fmt.Errorf("readLengthPrefixedString failed to read length: %w", err)
	}
	strBytes, err := readBytes(data, offset, length)
	if err != nil {
		return "", fmt.Errorf("readLengthPrefixedString failed to read string bytes (len %d): %w", length, err)
	}
	return string(strBytes), nil
}

// --- Hàm giải mã LogEntry ---
func deserializeLogEntry(data []byte) (*GoLogEntry, error) {
	offset := 0
	entry := &GoLogEntry{}

	opVal, err := readUint8(data, &offset)
	if err != nil {
		return nil, fmt.Errorf("deserializeLogEntry failed to read operation: %w", err)
	}
	entry.Op = XapianOperation(opVal)

	switch entry.Op {
	case OpNewDoc:
		docID, err := readUint32BE(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpNewDoc failed read docID: %w", err)
		}
		dataStr, err := readLengthPrefixedString(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpNewDoc failed read data: %w", err)
		}
		entry.Data = NewDocData{DocID: docID, Data: dataStr}
	case OpDelDoc:
		docID, err := readUint32BE(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpDelDoc failed read docID: %w", err)
		}
		entry.Data = DelDocData{DocID: docID}
	case OpAddValue:
		docID, err := readUint32BE(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpAddValue failed read docID: %w", err)
		}
		slot, err := readUint32BE(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpAddValue failed read slot: %w", err)
		}
		isSerialised, err := readBoolU8(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpAddValue failed read isSerialised: %w", err)
		}
		valueStr, err := readLengthPrefixedString(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpAddValue failed read value: %w", err)
		}
		entry.Data = AddValueData{DocID: docID, Slot: slot, Value: valueStr, IsSerialised: isSerialised}
	case OpAddTerm:
		docID, err := readUint32BE(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpAddTerm failed read docID: %w", err)
		}
		termStr, err := readLengthPrefixedString(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpAddTerm failed read term: %w", err)
		}
		entry.Data = AddTermData{DocID: docID, Term: termStr}
	case OpSetData:
		docID, err := readUint32BE(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpSetData failed read docID: %w", err)
		}
		dataStr, err := readLengthPrefixedString(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpSetData failed read data: %w", err)
		}
		entry.Data = SetDataData{DocID: docID, Data: dataStr}
	case OpIndexText:
		docID, err := readUint32BE(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpIndexText failed read docID: %w", err)
		}
		wdfInc, err := readUint32BE(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpIndexText failed read wdfInc: %w", err)
		}
		prefixStr, err := readLengthPrefixedString(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpIndexText failed read prefix: %w", err)
		}
		textStr, err := readLengthPrefixedString(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("OpIndexText failed read text: %w", err)
		}
		entry.Data = IndexTextData{DocID: docID, WdfInc: wdfInc, Prefix: prefixStr, Text: textStr}
	default:
		fmt.Printf("Warning: Unknown or unhandled LogEntry operation type: %d\n", entry.Op)
		// entry.Data sẽ là nil (hoặc giá trị mặc định của interface{})
	}

	// Optional: Check if all bytes were consumed
	// if offset != len(data) {
	//  	fmt.Printf("Warning: Extra data remaining after LogEntry deserialization (%d bytes)\n", len(data)-offset)
	// }

	return entry, nil
}

// --- Hàm giải mã ComprehensiveLog ---
func deserializeComprehensiveLog(data []byte) (*GoComprehensiveLog, error) {
	offset := 0 // Bắt đầu đọc từ đầu dữ liệu
	logs := &GoComprehensiveLog{}
	var err error

	// --- BƯỚC MỚI: Đọc db_path trước tiên ---
	dbNameStr, err := readLengthPrefixedString(data, &offset)
	if err != nil {
		return nil, fmt.Errorf("failed to read db_path: %w", err)
	}
	logs.DbName = dbNameStr // Lưu db_path đã đọc
	// fmt.Printf("  [Deserialize] Read DbName: %s (Offset now: %d)\n", logs.DbName, offset)
	// -----------------------------------------

	// --- Các bước còn lại giữ nguyên, nhưng offset đã được cập nhật sau khi đọc db_path ---

	// 1. Giải mã Xapian Doc Logs
	docLogCount, err := readUint32BE(data, &offset)
	if err != nil {
		return nil, fmt.Errorf("failed to read docLogCount: %w", err)
	}
	// fmt.Printf("  [Deserialize] Doc Log Count: %d (Offset now: %d)\n", docLogCount, offset)
	logs.XapianDocLogs = make([]GoLogEntry, 0, docLogCount)
	for i := uint32(0); i < docLogCount; i++ {
		entrySize, err := readUint32BE(data, &offset)
		if err != nil {
			return nil, fmt.Errorf("failed to read entrySize for doc log %d: %w", i, err)
		}
		entryBytes, err := readBytes(data, &offset, entrySize)
		if err != nil {
			return nil, fmt.Errorf("failed to read entryBytes for doc log %d (size %d): %w", i, entrySize, err)
		}

		logEntry, err := deserializeLogEntry(entryBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to deserialize doc log entry %d: %w", i, err)
		}
		if logEntry != nil {
			logs.XapianDocLogs = append(logs.XapianDocLogs, *logEntry)
		} else {
			fmt.Printf("Warning: Skipping potentially corrupted doc log entry %d\n", i)
		}
	}

	return logs, nil
}

func processDecodedLogs(result *MVMExecuteResult) {
	if result == nil || result.MapFullDbLogs == nil {
		// fmt.Println("No logs to process.")
		return
	}

	for _, serializedLogData := range result.MapFullDbLogs {
		if len(serializedLogData) == 0 {
			// fmt.Println("  (No log data)")
			continue
		}

		decodedLog, err := deserializeComprehensiveLog(serializedLogData)
		if err != nil {
			// logger.Error("Error deserializing log data for address %s: %v", addr, err)
			continue
		}

		// FORK-SAFE OPTIMIZATION: Removed expensive logJSON marshaling and fmt.Printf
		// JSON Marshal runs on every single TX and bottlenecks the Go execution phase.
		// Un-comment lines below only during local debug if required.
		_ = decodedLog
		/*
			logJSON, err := json.MarshalIndent(decodedLog, "  ", "  ")
			if err != nil {
				fmt.Printf("  Error marshalling decoded log to JSON: %v\n", err)
				// Hoặc in trực tiếp struct nếu không cần JSON
				fmt.Printf("  Decoded Log (struct):\n%+v\n", decodedLog)
			} else {
				fmt.Printf("  Decoded Log (JSON):\n%s\n", string(logJSON))
			}
		*/
	}
}
