package tx_processor

import (
	"fmt"
	"log"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	// Cần cho Keccak256 nếu dùng helper
)

// --- Contract ABI Definition ---
const (
	BLSManagerABI = `[
		{
			"inputs": [
				{
					"internalType": "bytes",
					"name": "publicKey",
					"type": "bytes"
				}
			],
			"name": "setBlsPublicKey",
			"outputs": [
				{
					"internalType": "bool",
					"name": "",
					"type": "bool"
				}
			],
			"stateMutability": "nonpayable",
			"type": "function"
		},
		{
			"inputs": [
				{
					"internalType": "enum BLSManager.AccountType",
					"name": "accountType",
					"type": "uint8"
				}
			],
			"name": "setAccountType",
			"outputs": [
				{
					"internalType": "bool",
					"name": "",
					"type": "bool"
				}
			],
			"stateMutability": "nonpayable",
			"type": "function"
		},
		{
			"inputs": [
				{
					"internalType": "address",
					"name": "user",
					"type": "address"
				}
			],
			"name": "getAccountType",
			"outputs": [
				{
					"internalType": "enum BLSManager.AccountType",
					"name": "",
					"type": "uint8"
				}
			],
			"stateMutability": "view",
			"type": "function"
		},
		{
			"inputs": [],
			"name": "blsPublicKey",
			"outputs": [
				{
					"internalType": "bytes",
					"name": "",
					"type": "bytes"
				}
			],
			"stateMutability": "view",
			"type": "function"
		},
		{
			"inputs": [
				{
					"internalType": "address",
					"name": "",
					"type": "address"
				}
			],
			"name": "accountTypes",
			"outputs": [
				{
					"internalType": "enum BLSManager.AccountType",
					"name": "",
					"type": "uint8"
				}
			],
			"stateMutability": "view",
			"type": "function"
		}
	]`
)

// isValidGoAccountType checks if the given Go ACCOUNT_TYPE is one of the defined constants.
func isValidGoAccountType(a pb.ACCOUNT_TYPE) bool {
	switch a {
	case pb.ACCOUNT_TYPE_REGULAR_ACCOUNT, pb.ACCOUNT_TYPE_READ_WRITE_STRICT:
		return true
	default:
		return false
	}
}

// blsManagerABI is a parsed ABI object for the BLSManager contract.
var blsManagerABI abi.ABI

func init() {
	var err error
	blsManagerABI, err = abi.JSON(strings.NewReader(BLSManagerABI))
	if err != nil {
		log.Fatalf("FATAL: Failed to parse BLSManager ABI: %v", err)
	}
}

// --- Calldata Generation Functions ---

// PackSetBlsPublicKey generates the calldata for the setBlsPublicKey function.
func PackSetBlsPublicKey(publicKey []byte) ([]byte, error) {
	if len(publicKey) == 0 { // Omit nil check, len(nil) is 0
		return nil, fmt.Errorf("publicKey không được rỗng")
	}
	// Thêm kiểm tra độ dài cụ thể ở đây nếu cần khi đóng gói
	// const expectedBlsPublicKeyLength = 48 // HOẶC 96, TÙY YÊU CẦU CỦA BẠN
	// if len(publicKey) != expectedBlsPublicKeyLength {
	// 	return nil, fmt.Errorf("publicKey để đóng gói có độ dài không chính xác: nhận %d bytes, yêu cầu %d bytes", len(publicKey), expectedBlsPublicKeyLength)
	// }

	packedData, err := blsManagerABI.Pack("setBlsPublicKey", publicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to pack setBlsPublicKey: %w", err)
	}
	return packedData, nil
}

// PackSetAccountType generates the calldata for the setAccountType function.
func PackSetAccountType(accountType pb.ACCOUNT_TYPE) ([]byte, error) {
	if !isValidGoAccountType(accountType) {
		return nil, fmt.Errorf("go ACCOUNT_TYPE không hợp lệ: %d (%s). Chỉ cho phép các loại đã định nghĩa",
			accountType, accountType.String())
	}
	val := uint8(accountType)
	packedData, err := blsManagerABI.Pack("setAccountType", val)
	if err != nil {
		return nil, fmt.Errorf("failed to pack setAccountType cho loại %s (giá trị %d): %w",
			accountType.String(), val, err)
	}
	return packedData, nil
}

// PackGetAccountType generates the calldata for the getAccountType function.
func PackGetAccountType(userAddress common.Address) ([]byte, error) {
	packedData, err := blsManagerABI.Pack("getAccountType", userAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to pack getAccountType cho địa chỉ %s: %w", userAddress.Hex(), err)
	}
	return packedData, nil
}

// PackBlsPublicKey generates the calldata for reading the blsPublicKey state variable.
func PackBlsPublicKey() ([]byte, error) {
	packedData, err := blsManagerABI.Pack("blsPublicKey")
	if err != nil {
		return nil, fmt.Errorf("failed to pack blsPublicKey getter: %w", err)
	}
	return packedData, nil
}

// PackAccountTypes generates the calldata for reading an account type from the accountTypes mapping.
func PackAccountTypes(userAddress common.Address) ([]byte, error) {
	packedData, err := blsManagerABI.Pack("accountTypes", userAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to pack accountTypes getter cho địa chỉ %s: %w", userAddress.Hex(), err)
	}
	return packedData, nil
}

// --- Functions to Unpack Call Arguments from Calldata ---

// UnpackSetBlsPublicKeyInput giải mã tham số `publicKey` từ calldata của hàm `setBlsPublicKey`.
// Calldata phải bao gồm cả 4 byte function selector.
func UnpackSetBlsPublicKeyInput(calldata []byte) ([]byte, error) {
	if len(calldata) < 4 { // 4 byte cho function selector
		return nil, fmt.Errorf("calldata quá ngắn, không chứa function selector")
	}

	method, err := blsManagerABI.MethodById(calldata[:4])
	if err != nil {
		return nil, fmt.Errorf("không thể lấy method từ ID (function selector %x): %w", calldata[:4], err)
	}

	if method.Name != "setBlsPublicKey" {
		return nil, fmt.Errorf("calldata không dành cho hàm setBlsPublicKey. Hàm được xác định: %s", method.Name)
	}

	argsData := calldata[4:]
	// Giả định rằng method.Inputs.Unpack chỉ nhận calldata[4:] và trả về []interface{}, error
	// Dựa trên lỗi compiler mà bạn gặp phải.
	unpackedArgs, err := method.Inputs.Unpack(argsData)
	if err != nil {
		return nil, fmt.Errorf("lỗi giải mã tham số setBlsPublicKey từ calldata (hàm Unpack trả về giá trị): %w", err)
	}

	if len(unpackedArgs) < 1 {
		return nil, fmt.Errorf("không đủ tham số được giải mã, mong đợi 1, nhận được %d", len(unpackedArgs))
	}

	publicKey, ok := unpackedArgs[0].([]byte)
	if !ok {
		if ptrBytes, isPtr := unpackedArgs[0].(*[]byte); isPtr && ptrBytes != nil {
			publicKey = *ptrBytes
			ok = true
		} else {
			return nil, fmt.Errorf("tham số đầu tiên ('publicKey') không phải kiểu []byte hoặc *[]byte, nhận được: %T", unpackedArgs[0])
		}
	}

	// KIỂM TRA ĐỘ DÀI CỦA PUBLICKEY SAU KHI GIẢI MÃ
	if len(publicKey) == 0 { // Kiểm tra cơ bản từ logic contract
		return nil, fmt.Errorf("publicKey được giải mã không hợp lệ: độ dài bằng 0. Contract sẽ từ chối giá trị này")
	}

	// --- QUAN TRỌNG: THAY THẾ ĐỘ DÀI MONG MUỐN CỦA BẠN VÀO ĐÂY ---
	// Ví dụ: Kiểm tra độ dài chính xác. BLS public key thường có độ dài cố định.
	// Phổ biến là 48 bytes (nén) hoặc 96 bytes (không nén) cho BLS12-381.
	const expectedBlsPublicKeyLength = 48 // << THAY ĐỔI GIÁ TRỊ NÀY thành 96 hoặc độ dài bạn cần

	if len(publicKey) != expectedBlsPublicKeyLength {
		return nil, fmt.Errorf("publicKey được giải mã có độ dài không chính xác: nhận được %d bytes, yêu cầu %d bytes", len(publicKey), expectedBlsPublicKeyLength)
	}
	// --- KẾT THÚC KIỂM TRA ĐỘ DÀI CỤ THỂ ---

	return publicKey, nil
}

// --- Functions to Unpack Return Values ---

// UnpackGetAccountType unpacks the AccountType from the return data of getAccountType.
func UnpackGetAccountType(data []byte) (pb.ACCOUNT_TYPE, error) {
	if len(data) == 0 {
		return 0, fmt.Errorf("không thể giải mã getAccountType từ dữ liệu nil hoặc rỗng")
	}

	var out []interface{}
	err := blsManagerABI.UnpackIntoInterface(&out, "getAccountType", data)
	if err != nil {
		return 0, fmt.Errorf("lỗi giải mã dữ liệu ABI của getAccountType: %w", err)
	}

	if len(out) == 0 {
		return 0, fmt.Errorf("không có dữ liệu trả về từ thao tác giải mã ABI của getAccountType")
	}

	accountTypeValUint8, ok := out[0].(uint8)
	if !ok {
		return 0, fmt.Errorf("kiểu dữ liệu trả về không mong đợi cho accountType từ contract; mong đợi uint8, nhận được %T", out[0])
	}

	goAccountType := pb.ACCOUNT_TYPE(accountTypeValUint8)
	if !isValidGoAccountType(goAccountType) {
		return 0, fmt.Errorf("nhận được AccountType không thể ánh xạ từ contract: giá trị %d. Module Go mong đợi một trong các loại đã định nghĩa (%s, %s)",
			accountTypeValUint8, pb.ACCOUNT_TYPE_REGULAR_ACCOUNT.String(), pb.ACCOUNT_TYPE_READ_WRITE_STRICT.String())
	}
	return goAccountType, nil
}

func UnpackSetAccountTypeInput(calldata []byte) (pb.ACCOUNT_TYPE, error) {
	if len(calldata) < 4 { // 4 byte cho function selector
		return 0, fmt.Errorf("calldata quá ngắn để giải mã setAccountType, không chứa function selector")
	}

	methodID := calldata[:4]
	method, err := blsManagerABI.MethodById(methodID)
	if err != nil {
		return 0, fmt.Errorf("không thể lấy method từ ID cho setAccountType (function selector %x): %w", methodID, err)
	}

	if method.Name != "setAccountType" {
		return 0, fmt.Errorf("calldata không dành cho hàm setAccountType. Hàm được xác định: %s", method.Name)
	}

	argsData := calldata[4:]
	unpackedArgs, err := method.Inputs.Unpack(argsData)
	if err != nil {
		return 0, fmt.Errorf("lỗi giải mã tham số setAccountType từ calldata: %w", err)
	}

	if len(unpackedArgs) < 1 {
		return 0, fmt.Errorf("không đủ tham số được giải mã cho setAccountType, mong đợi 1, nhận được %d", len(unpackedArgs))
	}

	accountTypeUint8, ok := unpackedArgs[0].(uint8)
	if !ok {
		return 0, fmt.Errorf("tham số đầu tiên ('accountType') cho setAccountType không phải kiểu uint8, nhận được: %T", unpackedArgs[0])
	}

	goAccountType := pb.ACCOUNT_TYPE(accountTypeUint8)
	// Validate if the unpacked type is one of the Go enum's defined values.
	// This check ensures that the value from the calldata corresponds to a known account type in your Go system.
	if !isValidGoAccountType(goAccountType) {
		return 0, fmt.Errorf("giá trị accountType được giải mã không hợp lệ từ calldata: %d. Module Go mong đợi một trong các loại đã định nghĩa (%s, %s)",
			accountTypeUint8, pb.ACCOUNT_TYPE_REGULAR_ACCOUNT.String(), pb.ACCOUNT_TYPE_READ_WRITE_STRICT.String())
	}

	return goAccountType, nil
}

// UnpackBool unpacks a boolean value from function call return data.
func UnpackBool(methodName string, data []byte) (bool, error) {
	if methodName == "" {
		return false, fmt.Errorf("methodName không được rỗng để giải mã boolean")
	}
	if len(data) == 0 {
		return false, fmt.Errorf("không thể giải mã boolean cho method %s từ dữ liệu nil hoặc rỗng", methodName)
	}

	var out []interface{}
	err := blsManagerABI.UnpackIntoInterface(&out, methodName, data)
	if err != nil {
		return false, fmt.Errorf("lỗi giải mã boolean cho method %s từ dữ liệu ABI: %w", methodName, err)
	}

	if len(out) == 0 {
		return false, fmt.Errorf("không có dữ liệu trả về từ thao tác giải mã boolean cho method %s", methodName)
	}

	result, ok := out[0].(bool)
	if !ok {
		return false, fmt.Errorf("kiểu dữ liệu trả về không mong đợi cho method %s; mong đợi bool, nhận được %T", methodName, out[0])
	}
	return result, nil
}
