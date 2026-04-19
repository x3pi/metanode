package storage

import (
	"bufio" // Thêm thư viện bufio để đọc/ghi có đệm
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync" // Thêm thư viện sync để sử dụng sync.Pool
)

// bufferPool dùng để tái sử dụng các đối tượng bytes.Buffer
var bufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// getBuffer lấy một buffer từ pool.
func getBuffer() *bytes.Buffer {
	return bufferPool.Get().(*bytes.Buffer)
}

// putBuffer trả một buffer về pool.
// CRITICAL FIX: Only return buffers to the pool if they are reasonably sized.
// If a buffer grew to gigabytes during a massive block serialization, returning it
// keeps that memory permanently claimed!
func putBuffer(buf *bytes.Buffer) {
	// If the buffer capacity exceeds 1MB, discard it entirely so GC reclaims it.
	if buf.Cap() > 1024*1024 {
		return
	}
	buf.Reset() // Reset buffer trước khi trả về pool
	bufferPool.Put(buf)
}

type BackUpDb struct {
	BockNumber                uint64
	NodeId                    string
	TxBatchPut                []byte
	AccountBatch              []byte
	BockBatch                 []byte
	BockPut                   []byte
	ReceiptBatchPut           []byte
	SmartContractBatch        []byte
	SmartContractStorageBatch []byte
	TrieDatabaseBatchPut      map[string][]byte

	CodeBatchPut  []byte
	MapppingBatch []byte

	DevicePut    []byte
	DeviceDelete []byte
	FullDbLogs   []map[string][]byte

	Receipts   []byte
	EventLogs  []byte
	StakeState []byte
}

// AddToFullDbLogs thêm một map[string][]byte vào FullDbLogs
func (b *BackUpDb) AddToFullDbLogs(data map[string][]byte) {
	b.FullDbLogs = append(b.FullDbLogs, data)
}

// SerializeByteArrays chuyển đổi [][]byte thành []byte
func SerializeByteArrays(data [][]byte) ([]byte, error) {
	size := 4
	for _, b := range data {
		size += 4 + len(b)
	}

	var buf bytes.Buffer
	buf.Grow(size)

	var count [4]byte
	binary.LittleEndian.PutUint32(count[:], uint32(len(data)))
	buf.Write(count[:])

	for _, b := range data {
		if err := writeBytes(&buf, b); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

// DeserializeByteArrays chuyển đổi []byte trở lại [][]byte
func DeserializeByteArrays(encodedData []byte) ([][]byte, error) {
	buf := bytes.NewReader(encodedData)
	var countBuf [4]byte
	if _, err := io.ReadFull(buf, countBuf[:]); err != nil {
		return nil, err
	}
	count := binary.LittleEndian.Uint32(countBuf[:])

	if count > 1000000 {
		return nil, fmt.Errorf("array count too large: %d", count)
	}

	var data [][]byte = make([][]byte, count)
	for i := uint32(0); i < count; i++ {
		b, err := readBytes(buf)
		if err != nil {
			return nil, err
		}
		data[i] = b
	}
	return data, nil
}

func writeBytes(buf io.Writer, b []byte) error {
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(b)))
	if _, err := buf.Write(size[:]); err != nil {
		return err
	}
	if len(b) > 0 {
		if _, err := buf.Write(b); err != nil {
			return err
		}
	}
	return nil
}

func readBytes(buf io.Reader) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(buf, size[:]); err != nil {
		return nil, err
	}
	l := binary.LittleEndian.Uint32(size[:])
	if l == 0 {
		return []byte{}, nil
	}
	b := make([]byte, l)
	if _, err := io.ReadFull(buf, b); err != nil {
		return nil, err
	}
	return b, nil
}

// calculateBackupSize tính toán trước kích thước toàn bộ BackUpDb giúp tiền cấp phát bộ nhớ chuẩn xác
func calculateBackupSize(backup *BackUpDb) int {
	size := 8 // BockNumber
	size += 4 + len(backup.NodeId)
	size += 4 + len(backup.TxBatchPut)
	size += 4 + len(backup.AccountBatch)
	size += 4 + len(backup.BockBatch)
	size += 4 + len(backup.BockPut)
	size += 4 + len(backup.ReceiptBatchPut)
	size += 4 + len(backup.SmartContractBatch)
	size += 4 + len(backup.SmartContractStorageBatch)

	size += 4 // uint32 count
	for k, v := range backup.TrieDatabaseBatchPut {
		size += 4 + len(k)
		size += 4 + len(v)
	}

	size += 4 + len(backup.CodeBatchPut)
	size += 4 + len(backup.MapppingBatch)
	size += 4 + len(backup.DevicePut)
	size += 4 + len(backup.DeviceDelete)

	size += 4 // uint32 count
	for _, m := range backup.FullDbLogs {
		size += 4 // inner uint32 count
		for k, v := range m {
			size += 4 + len(k)
			size += 4 + len(v)
		}
	}

	size += 4 + len(backup.Receipts)
	size += 4 + len(backup.EventLogs)
	size += 4 + len(backup.StakeState)

	return size
}

// SerializeBackupDb chuyển đổi BackUpDb thành []byte, sử dụng cấu trúc nhị phân thuần tuý cực nhanh.
func SerializeBackupDb(backup BackUpDb) ([]byte, error) {
	exactSize := calculateBackupSize(&backup)
	var buf bytes.Buffer
	buf.Grow(exactSize)

	var u64 [8]byte
	binary.LittleEndian.PutUint64(u64[:], backup.BockNumber)
	buf.Write(u64[:])

	writeBytes(&buf, []byte(backup.NodeId))
	writeBytes(&buf, backup.TxBatchPut)
	writeBytes(&buf, backup.AccountBatch)
	writeBytes(&buf, backup.BockBatch)
	writeBytes(&buf, backup.BockPut)
	writeBytes(&buf, backup.ReceiptBatchPut)
	writeBytes(&buf, backup.SmartContractBatch)
	writeBytes(&buf, backup.SmartContractStorageBatch)

	var count [4]byte
	binary.LittleEndian.PutUint32(count[:], uint32(len(backup.TrieDatabaseBatchPut)))
	buf.Write(count[:])
	for k, v := range backup.TrieDatabaseBatchPut {
		writeBytes(&buf, []byte(k))
		writeBytes(&buf, v)
	}

	writeBytes(&buf, backup.CodeBatchPut)
	writeBytes(&buf, backup.MapppingBatch)
	writeBytes(&buf, backup.DevicePut)
	writeBytes(&buf, backup.DeviceDelete)

	binary.LittleEndian.PutUint32(count[:], uint32(len(backup.FullDbLogs)))
	buf.Write(count[:])
	for _, m := range backup.FullDbLogs {
		binary.LittleEndian.PutUint32(count[:], uint32(len(m)))
		buf.Write(count[:])
		for k, v := range m {
			writeBytes(&buf, []byte(k))
			writeBytes(&buf, v)
		}
	}

	writeBytes(&buf, backup.Receipts)
	writeBytes(&buf, backup.EventLogs)
	writeBytes(&buf, backup.StakeState)

	// Trả về trực tiếp memory slice bên trong cấu trúc Buffer để giải phóng allocation và copy
	return buf.Bytes(), nil
}

// DeserializeBackupDb giải mã nhị phân cực nhanh
func DeserializeBackupDb(encodedData []byte) (BackUpDb, error) {
	buf := bytes.NewReader(encodedData)
	var backup BackUpDb
	var err error

	var u64 [8]byte
	if _, err := io.ReadFull(buf, u64[:]); err != nil {
		return backup, err
	}
	backup.BockNumber = binary.LittleEndian.Uint64(u64[:])

	var nodeIdBytes []byte
	if nodeIdBytes, err = readBytes(buf); err != nil {
		return backup, err
	}
	backup.NodeId = string(nodeIdBytes)

	if backup.TxBatchPut, err = readBytes(buf); err != nil {
		return backup, err
	}
	if backup.AccountBatch, err = readBytes(buf); err != nil {
		return backup, err
	}
	if backup.BockBatch, err = readBytes(buf); err != nil {
		return backup, err
	}
	if backup.BockPut, err = readBytes(buf); err != nil {
		return backup, err
	}
	if backup.ReceiptBatchPut, err = readBytes(buf); err != nil {
		return backup, err
	}
	if backup.SmartContractBatch, err = readBytes(buf); err != nil {
		return backup, err
	}
	if backup.SmartContractStorageBatch, err = readBytes(buf); err != nil {
		return backup, err
	}

	var count [4]byte
	if _, err := io.ReadFull(buf, count[:]); err != nil {
		return backup, err
	}
	mapCount := binary.LittleEndian.Uint32(count[:])
	backup.TrieDatabaseBatchPut = make(map[string][]byte, mapCount)
	for i := uint32(0); i < mapCount; i++ {
		k, err := readBytes(buf)
		if err != nil {
			return backup, err
		}
		v, err := readBytes(buf)
		if err != nil {
			return backup, err
		}
		backup.TrieDatabaseBatchPut[string(k)] = v
	}

	if backup.CodeBatchPut, err = readBytes(buf); err != nil {
		return backup, err
	}
	if backup.MapppingBatch, err = readBytes(buf); err != nil {
		return backup, err
	}
	if backup.DevicePut, err = readBytes(buf); err != nil {
		return backup, err
	}
	if backup.DeviceDelete, err = readBytes(buf); err != nil {
		return backup, err
	}

	if _, err := io.ReadFull(buf, count[:]); err != nil {
		return backup, err
	}
	listCount := binary.LittleEndian.Uint32(count[:])
	backup.FullDbLogs = make([]map[string][]byte, listCount)
	for i := uint32(0); i < listCount; i++ {
		if _, err := io.ReadFull(buf, count[:]); err != nil {
			return backup, err
		}
		innerCount := binary.LittleEndian.Uint32(count[:])
		backup.FullDbLogs[i] = make(map[string][]byte, innerCount)
		for j := uint32(0); j < innerCount; j++ {
			k, err := readBytes(buf)
			if err != nil {
				return backup, err
			}
			v, err := readBytes(buf)
			if err != nil {
				return backup, err
			}
			backup.FullDbLogs[i][string(k)] = v
		}
	}

	if backup.Receipts, err = readBytes(buf); err != nil {
		return backup, err
	}
	if backup.EventLogs, err = readBytes(buf); err != nil {
		return backup, err
	}
	if backup.StakeState, err = readBytes(buf); err != nil {
		return backup, err
	}

	return backup, nil
}

// SerializeBatch chuyển đổi [][2][]byte thành []byte
func SerializeBatch(data [][2][]byte) ([]byte, error) {
	size := 4
	for _, pair := range data {
		size += 4 + len(pair[0])
		size += 4 + len(pair[1])
	}
	var buf bytes.Buffer
	buf.Grow(size)

	var count [4]byte
	binary.LittleEndian.PutUint32(count[:], uint32(len(data)))
	buf.Write(count[:])

	for _, pair := range data {
		if err := writeBytes(&buf, pair[0]); err != nil {
			return nil, err
		}
		if err := writeBytes(&buf, pair[1]); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

// DeserializeBatch chuyển đổi []byte trở lại [][2][]byte
func DeserializeBatch(encodedData []byte) ([][2][]byte, error) {
	buf := bytes.NewReader(encodedData)
	var countBuf [4]byte
	if _, err := io.ReadFull(buf, countBuf[:]); err != nil {
		return nil, err
	}
	count := binary.LittleEndian.Uint32(countBuf[:])

	if count > 1000000 { // Giới hạn mảng an toàn
		return nil, fmt.Errorf("batch count too large: %d", count)
	}

	data := make([][2][]byte, count)
	for i := uint32(0); i < count; i++ {
		b1, err := readBytes(buf)
		if err != nil {
			return nil, err
		}
		b2, err := readBytes(buf)
		if err != nil {
			return nil, err
		}
		data[i][0] = b1
		data[i][1] = b2
	}
	return data, nil
}

// AppendBatch thêm dữ liệu batch vào cuối file, với tiền tố kích thước, sử dụng I/O có đệm.
func AppendBatch(filename string, data [][2][]byte) error {
	dir := filepath.Dir(filename)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, os.ModePerm); err != nil {
			return fmt.Errorf("failed to create directory for batch file: %w", err)
		}
	}

	encodedBatchData, err := SerializeBatch(data) // Sử dụng buffer từ pool bên trong
	if err != nil {
		return fmt.Errorf("failed to serialize batch for append: %w", err)
	}

	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return fmt.Errorf("failed to open batch file for append: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file) // Sử dụng writer có đệm
	defer writer.Flush()            // Đảm bảo dữ liệu được ghi trước khi đóng

	batchDataSize := int64(len(encodedBatchData))
	if err := binary.Write(writer, binary.LittleEndian, batchDataSize); err != nil {
		return fmt.Errorf("failed to write batch data size: %w", err)
	}

	if _, err := writer.Write(encodedBatchData); err != nil {
		return fmt.Errorf("failed to write batch data: %w", err)
	}

	return nil
}

// LoadBatch khôi phục tất cả các batch đã được append từ file,
// gộp tất cả các cặp key-value thành một slice [][2][]byte duy nhất, sử dụng I/O có đệm.
func LoadBatch(filename string) ([][2][]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("failed to open batch file for loading: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file) // Sử dụng reader có đệm
	var finalCombinedBatch [][2][]byte

	for {
		var batchDataSize int64
		if err := binary.Read(reader, binary.LittleEndian, &batchDataSize); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to read batch data size: %w", err)
		}

		if batchDataSize <= 0 || batchDataSize > 50*1024*1024 { // Giới hạn 50MB
			return nil, fmt.Errorf("invalid batch data size: %d (must be > 0 and <= 50MB)", batchDataSize)
		}

		encodedBatchData := make([]byte, batchDataSize)
		if _, err := io.ReadFull(reader, encodedBatchData); err != nil {
			return nil, fmt.Errorf("failed to read batch data (expected %d bytes): %w", batchDataSize, err)
		}

		currentBatch, err := DeserializeBatch(encodedBatchData) // Sử dụng buffer từ pool bên trong
		if err != nil {
			return nil, fmt.Errorf("failed to deserialize batch data: %w", err)
		}
		finalCombinedBatch = append(finalCombinedBatch, currentBatch...)
	}

	return finalCombinedBatch, nil
}

// AppendPut ghi một cặp key-value vào file, sử dụng I/O có đệm.
func AppendPut(filename string, key, value []byte) error {
	dir := filepath.Dir(filename)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, os.ModePerm); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}
	}

	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file) // Sử dụng writer có đệm
	defer writer.Flush()            // Đảm bảo dữ liệu được ghi

	// Mã hóa sử dụng buffer từ pool thông qua PutToBytes
	entryData, err := PutToBytes(key, value)
	if err != nil {
		return fmt.Errorf("failed to encode data: %w", err)
	}

	size := int64(len(entryData))
	if err := binary.Write(writer, binary.LittleEndian, size); err != nil {
		return fmt.Errorf("failed to write entry size: %w", err)
	}

	if _, err := writer.Write(entryData); err != nil {
		return fmt.Errorf("failed to write entry data: %w", err)
	}

	return nil
}

// PutToBytes chuyển đổi một lệnh put thành []byte
func PutToBytes(key, value []byte) ([]byte, error) {
	size := 4 + len(key) + 4 + len(value)
	var buf bytes.Buffer
	buf.Grow(size)

	if err := writeBytes(&buf, key); err != nil {
		return nil, err
	}
	if err := writeBytes(&buf, value); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// BytesToPut chuyển đổi []byte trở lại thành một cặp key-value
func BytesToPut(encodedData []byte) ([2][]byte, error) {
	buf := bytes.NewReader(encodedData)
	var data [2][]byte
	var err error

	data[0], err = readBytes(buf)
	if err != nil {
		return data, err
	}
	data[1], err = readBytes(buf)
	if err != nil {
		return data, err
	}

	return data, nil
}

// LoadAllPut đọc toàn bộ các lệnh put từ file, sử dụng I/O có đệm.
func LoadAllPut(filename string) ([][2][]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file) // Sử dụng reader có đệm
	var allData [][2][]byte

	for {
		var entrySize int64
		if err := binary.Read(reader, binary.LittleEndian, &entrySize); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to read entry size: %w", err)
		}

		if entrySize <= 0 || entrySize > 10*1024*1024 { // Giới hạn 10MB
			return nil, fmt.Errorf("invalid entry size: %d", entrySize)
		}

		entryData := make([]byte, entrySize)
		if _, err := io.ReadFull(reader, entryData); err != nil {
			return nil, fmt.Errorf("failed to read entry data: %w", err)
		}

		// Giải mã sử dụng buffer từ pool thông qua BytesToPut
		entry, err := BytesToPut(entryData)
		if err != nil {
			return nil, fmt.Errorf("failed to decode entry data: %w", err)
		}
		allData = append(allData, entry)
	}

	return allData, nil
}

// AppendDelete ghi một key vào file delete, sử dụng I/O có đệm.
func AppendDelete(filename string, key []byte) error {
	dir := filepath.Dir(filename)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, os.ModePerm); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}
	}

	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file) // Sử dụng writer có đệm
	defer writer.Flush()            // Đảm bảo dữ liệu được ghi

	size := int64(len(key))
	if err := binary.Write(writer, binary.LittleEndian, size); err != nil {
		return fmt.Errorf("failed to write entry size: %w", err)
	}

	if _, err := writer.Write(key); err != nil {
		return fmt.Errorf("failed to write entry data: %w", err)
	}

	return nil
}

// LoadAllDelete đọc toàn bộ các key delete từ file, sử dụng I/O có đệm.
func LoadAllDelete(filename string) ([][]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file) // Sử dụng reader có đệm
	var allKeys [][]byte

	for {
		var entrySize int64
		if err := binary.Read(reader, binary.LittleEndian, &entrySize); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to read entry size: %w", err)
		}

		if entrySize <= 0 || entrySize > 10*1024*1024 { // Giới hạn 10MB
			return nil, fmt.Errorf("invalid entry size: %d", entrySize)
		}

		entryData := make([]byte, entrySize)
		if _, err := io.ReadFull(reader, entryData); err != nil {
			return nil, fmt.Errorf("failed to read entry data: %w", err)
		}

		allKeys = append(allKeys, entryData)
	}

	return allKeys, nil
}
