package argument_encode

import (
	"github.com/holiman/uint256"
)

func DecodeFuncHash(input []byte) ([]byte, []byte) {
	return input[:4], input[4:]
}

//	func DecodeStringInput(input []byte, idx int) string {
//		// TODO: find out how to update Uint64 to Uint256 for start and len
//		start := uint256.NewInt(0).SetBytes(input[idx*32 : idx*32+32]).Uint64()
//		len := uint256.NewInt(0).SetBytes(input[start : start+32]).Uint64()
//		str := input[start+32 : start+32+len]
//		return string(str)
//	}

func DecodeStringInput(input []byte, idx int) string {
	// Kích thước mỗi phần tử là 32 byte (Ethereum ABI encoding)
	const wordSize = 32

	// Kiểm tra giới hạn idx
	if idx < 0 || (idx+1)*wordSize > len(input) {
		return ""
	}

	// Lấy start từ input
	start := uint256.NewInt(0).SetBytes(input[idx*wordSize : (idx+1)*wordSize]).Uint64()

	// Kiểm tra start hợp lệ
	if start+wordSize > uint64(len(input)) {
		return ""
	}

	// Lấy độ dài chuỗi, đổi tên biến `len` thành `strLen`
	strLen := uint256.NewInt(0).SetBytes(input[start : start+wordSize]).Uint64()

	// Kiểm tra strLen hợp lệ
	if start+wordSize+strLen > uint64(len(input)) {
		return ""
	}

	// Lấy chuỗi
	str := input[start+wordSize : start+wordSize+strLen]
	return string(str)
}

// ... existing code ...

func EncodeStringInput(input string) []byte {
	// Kích thước mỗi phần tử là 32 byte (Ethereum ABI encoding)
	const wordSize = 32

	// Độ dài chuỗi
	strLen := len(input)

	// Tính toán offset cho dữ liệu chuỗi
	dataOffset := wordSize * 1 // Offset = 32

	// Tạo buffer để chứa dữ liệu đã encode
	encodedData := make([]byte, dataOffset+wordSize+strLen)

	// Encode offset
	offset := uint256.NewInt(uint64(dataOffset))
	offsetBytes := offset.Bytes()
	copy(encodedData[:wordSize], offsetBytes)

	// Encode độ dài chuỗi
	length := uint256.NewInt(uint64(strLen))
	lengthBytes := length.Bytes()
	copy(encodedData[dataOffset:dataOffset+wordSize], lengthBytes)

	// Copy chuỗi vào buffer
	copy(encodedData[dataOffset+wordSize:], input)

	// Padding
	paddingSize := wordSize - (strLen % wordSize)
	if paddingSize < wordSize {
		padding := make([]byte, paddingSize)
		for i := 0; i < paddingSize; i++ {
			padding[i] = 0
		}
		copy(encodedData[dataOffset+wordSize+strLen:], padding)
	}

	return encodedData
}

// ... existing code ...
