package loggerfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Global log directory configuration
var globalLogDir = "logs"

// Global epoch tracking for log directories
var (
	globalEpochMu sync.RWMutex
	globalEpoch   uint64 = 0
)

const (
	defaultMaxLogReadBytes int64 = 4 * 1024 * 1024   // 4MB
	DefaultMaxLogFileSize  int64 = 200 * 1024 * 1024 // 200MB — rotate khi vượt quá
	DefaultMaxLogFiles     int   = 5                 // Giữ tối đa 5 file log cũ (.1 → .5)
)

// SetGlobalLogDir sets the global log directory
func SetGlobalLogDir(logDir string) {
	globalLogDir = logDir
}

// GetGlobalLogDir returns the current global log directory
func GetGlobalLogDir() string {
	return globalLogDir
}

// SetGlobalEpoch cập nhật epoch hiện tại cho log system
// Gọi khi epoch transition xảy ra
func SetGlobalEpoch(epoch uint64) {
	globalEpochMu.Lock()
	defer globalEpochMu.Unlock()
	globalEpoch = epoch
}

// GetGlobalEpoch trả về epoch hiện tại
func GetGlobalEpoch() uint64 {
	globalEpochMu.RLock()
	defer globalEpochMu.RUnlock()
	return globalEpoch
}

// resolveLogRoot prepares the absolute path for the log root directory.
func resolveLogRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = globalLogDir
	}
	if strings.TrimSpace(root) == "" {
		return "", errors.New("log root directory is empty")
	}

	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("failed to resolve log root %q: %w", root, err)
	}
	return absRoot, nil
}

// normalizeEpochToPath converts an epoch string (e.g. "2") to the epoch directory name ("epoch_2").
// Nếu epoch rỗng, sử dụng epoch hiện tại từ GetGlobalEpoch().
func normalizeEpochToPath(epoch string) (string, error) {
	epoch = strings.TrimSpace(epoch)
	if epoch == "" {
		// Sử dụng epoch hiện tại
		return fmt.Sprintf("epoch_%d", GetGlobalEpoch()), nil
	}

	// Hỗ trợ cả "epoch_2" và "2"
	if strings.HasPrefix(epoch, "epoch_") {
		epoch = strings.TrimPrefix(epoch, "epoch_")
	}

	epochNum, err := strconv.ParseUint(epoch, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid epoch value %q: must be a non-negative integer", epoch)
	}

	return fmt.Sprintf("epoch_%d", epochNum), nil
}

func ensureWithinRoot(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("failed to resolve path relation: %w", err)
	}
	if strings.HasPrefix(rel, "..") || rel == ".." {
		return fmt.Errorf("path %q escapes log root %q", target, root)
	}
	return nil
}

// ListLogFiles liệt kê danh sách file log tại epoch tương ứng.
// - root: thư mục gốc chứa logs, mặc định lấy từ globalLogDir nếu bỏ trống.
// - epoch: số epoch (vd: "0", "2", "epoch_2"). Nếu rỗng thì dùng epoch hiện tại.
func ListLogFiles(root, epoch string) ([]string, error) {
	absRoot, err := resolveLogRoot(root)
	if err != nil {
		return nil, err
	}

	epochPath, err := normalizeEpochToPath(epoch)
	if err != nil {
		return nil, err
	}
	targetDir := filepath.Join(absRoot, epochPath)

	if err := ensureWithinRoot(absRoot, targetDir); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read log directory %q: %w", targetDir, err)
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			files = append(files, entry.Name())
		}
	}

	sort.Strings(files)
	return files, nil
}

// ReadLogFile đọc nội dung file log theo epoch.
// Nếu file lớn hơn maxBytes, chỉ đọc phần cuối file (theo maxBytes) và loại bỏ dòng đầu bị cắt dở.
func ReadLogFile(root, epoch, fileName string, maxBytes int64) (string, error) {
	fullPath, err := ResolveLogFilePath(root, epoch, fileName)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to stat log file %q: %w", fullPath, err)
	}

	if maxBytes <= 0 || maxBytes > defaultMaxLogReadBytes {
		maxBytes = defaultMaxLogReadBytes
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to open log file %q: %w", fullPath, err)
	}
	defer file.Close()

	var start int64
	if info.Size() > maxBytes {
		start = info.Size() - maxBytes
	}

	if start > 0 {
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return "", fmt.Errorf("failed to seek log file %q: %w", fullPath, err)
		}
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("failed to read log file %q: %w", fullPath, err)
	}

	if start > 0 {
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 && idx+1 < len(data) {
			data = data[idx+1:]
		}
	}

	return string(data), nil
}

// ResolveLogFilePath trả về đường dẫn tuyệt đối tới file log theo epoch.
func ResolveLogFilePath(root, epoch, fileName string) (string, error) {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return "", errors.New("fileName must not be empty")
	}
	if fileName != filepath.Base(fileName) {
		return "", fmt.Errorf("invalid file name %q", fileName)
	}

	absRoot, err := resolveLogRoot(root)
	if err != nil {
		return "", err
	}

	epochPath, err := normalizeEpochToPath(epoch)
	if err != nil {
		return "", err
	}
	targetDir := filepath.Join(absRoot, epochPath)

	if err := ensureWithinRoot(absRoot, targetDir); err != nil {
		return "", err
	}

	fullPath := filepath.Clean(filepath.Join(targetDir, fileName))
	if err := ensureWithinRoot(absRoot, fullPath); err != nil {
		return "", err
	}
	return fullPath, nil
}

// ============================================================================
// FileLogger — ghi log vào file với auto size-based rotation
// ============================================================================

// FileLogger struct quản lý ghi log vào file
type FileLogger struct {
	file         *os.File
	filePath     string // Full path đến file log hiện tại
	baseName     string // Tên file gốc (vd: "App.log")
	epochDir     string // Thư mục epoch hiện tại
	entries      []LogEntry
	mutex        sync.Mutex
	maxFileSize  int64 // Kích thước tối đa trước khi rotate (bytes)
	maxLogFiles  int   // Số file cũ tối đa giữ lại
	bytesWritten int64 // Số bytes đã ghi từ lần rotate cuối
}

// getEpochDir trả về tên thư mục epoch hiện tại: "epoch_N"
func getEpochDir() string {
	epoch := GetGlobalEpoch()
	return fmt.Sprintf("epoch_%d", epoch)
}

// NewFileLogger tạo mới một FileLogger
// Log được tổ chức theo epoch: logs/epoch_N/App.log
// Tự động rotate khi file vượt quá 50MB
func NewFileLogger(filePath string) (*FileLogger, error) {
	logDir := globalLogDir
	epochDir := getEpochDir()
	fullPath := filepath.Join(logDir, epochDir, filePath)

	// Tạo thư mục logs theo epoch nếu chưa tồn tại
	dir := filepath.Dir(fullPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		err := os.MkdirAll(dir, os.ModePerm)
		if err != nil {
			return nil, fmt.Errorf("failed to create sub logs directory: %w", err)
		}
	}

	file, err := os.OpenFile(fullPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	// Lấy kích thước file hiện tại
	var currentSize int64
	if info, err := file.Stat(); err == nil {
		currentSize = info.Size()
	}

	return &FileLogger{
		file:         file,
		filePath:     fullPath,
		baseName:     filePath,
		epochDir:     epochDir,
		entries:      make([]LogEntry, 0),
		maxFileSize:  DefaultMaxLogFileSize,
		maxLogFiles:  DefaultMaxLogFiles,
		bytesWritten: currentSize,
	}, nil
}

// SetMaxFileSize cấu hình kích thước tối đa file log (bytes)
func (fl *FileLogger) SetMaxFileSize(size int64) {
	if fl == nil {
		return
	}
	fl.mutex.Lock()
	defer fl.mutex.Unlock()
	if size > 0 {
		fl.maxFileSize = size
	}
}

// SetMaxLogFiles cấu hình số file log cũ tối đa giữ lại
func (fl *FileLogger) SetMaxLogFiles(n int) {
	if fl == nil {
		return
	}
	fl.mutex.Lock()
	defer fl.mutex.Unlock()
	if n > 0 {
		fl.maxLogFiles = n
	}
}

// File trả về *os.File gốc để có thể dùng làm output cho logger chuẩn.
func (fl *FileLogger) File() *os.File {
	if fl == nil {
		return nil
	}
	return fl.file
}

// checkAndRotate kiểm tra kích thước file và rotate nếu cần
// Phải gọi khi đã giữ fl.mutex
func (fl *FileLogger) checkAndRotate() {
	if fl.maxFileSize <= 0 || fl.bytesWritten < fl.maxFileSize {
		return
	}

	// Rotate: App.log → App.log.1, App.log.1 → App.log.2, ...
	fl.rotateLocked()
}

// rotateLocked thực hiện rotation — đổi tên file cũ và tạo file mới
// Phải gọi khi đã giữ fl.mutex
func (fl *FileLogger) rotateLocked() {
	// Đóng file hiện tại
	fl.file.Close()

	// Xóa file cũ nhất nếu vượt quá số lượng
	for i := fl.maxLogFiles; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", fl.filePath, i)
		if i == fl.maxLogFiles {
			// Xóa file cũ nhất
			os.Remove(oldPath)
		} else {
			// Đổi tên: .1 → .2, .2 → .3, ...
			newPath := fmt.Sprintf("%s.%d", fl.filePath, i+1)
			os.Rename(oldPath, newPath)
		}
	}

	// Đổi tên file hiện tại → .1
	rotatedPath := fmt.Sprintf("%s.1", fl.filePath)
	os.Rename(fl.filePath, rotatedPath)

	// Tạo file mới
	newFile, err := os.OpenFile(fl.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("🔄 [LOG-ROTATE] Error creating new log file: %v", err)
		// Fallback: mở lại file cũ
		newFile, _ = os.OpenFile(rotatedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	}

	fl.file = newFile
	fl.bytesWritten = 0

	log.Printf("🔄 [LOG-ROTATE] Rotated %s (max %dMB, keeping %d old files)",
		fl.baseName, fl.maxFileSize/(1024*1024), fl.maxLogFiles)
}

// Log ghi một message đơn giản vào file
func (fl *FileLogger) Log(message string) {
	if fl == nil {
		log.Println("FileLogger is nil. Skipping Log.")
		return
	}

	fl.mutex.Lock()
	defer fl.mutex.Unlock()

	timestamp := time.Now().Format(time.RFC3339)
	logMessage := fmt.Sprintf("%s: %s\n", timestamp, message)
	n, err := fl.file.WriteString(logMessage)
	if err != nil {
		log.Printf("Failed to write log message: %v", err)
	}
	fl.bytesWritten += int64(n)
	fl.checkAndRotate()
}

// Info ghi một message định dạng vào file
func (fl *FileLogger) Info(message interface{}, a ...interface{}) {
	if fl == nil {
		log.Println("FileLogger is nil. Skipping Info log.")
		return
	}

	fl.mutex.Lock()
	defer fl.mutex.Unlock()

	timestamp := time.Now().Format(time.RFC3339)
	logMessage := fmt.Sprintf("%s: %s\n", timestamp, fmt.Sprintf(fmt.Sprint(message), a...))
	n, err := fl.file.WriteString(logMessage)
	if err != nil {
		log.Printf("Failed to write log message: %v", err)
	}
	fl.bytesWritten += int64(n)
	fl.checkAndRotate()
}

// LogEntry lưu trữ một mục log chi tiết
type LogEntry struct {
	StartTime                     time.Time `json:"timestamp"`
	GenerateBlockTime             float64   `json:"GenerateBlockTime"`
	ProcessTransactionsInPoolTime float64   `json:"ProcessTransactionsInPoolTime"`
	CreateBlockTime               float64   `json:"CreateBlockTime"`
	LenTxs                        int       `json:"LenTxs"`
	Block                         string    `json:"Block"`
	Txs                           string    `json:"Txs"`
}

// Flush ghi các mục log từ bộ nhớ vào file
func (fl *FileLogger) Flush() {
	if fl == nil {
		log.Println("FileLogger is nil. Skipping Flush.")
		return
	}

	fl.mutex.Lock()
	defer fl.mutex.Unlock()

	if len(fl.entries) == 0 {
		return // Không có dữ liệu để ghi
	}

	data, err := json.MarshalIndent(fl.entries, "", "  ")
	if err != nil {
		log.Printf("Error marshaling JSON: %v", err)
		return
	}

	data = append(data, '\n')
	n, err := fl.file.Write(data)
	if err != nil {
		log.Printf("Error writing to file: %v", err)
		return
	}
	fl.bytesWritten += int64(n)
	fl.entries = fl.entries[:0] // Xóa dữ liệu sau khi ghi
	fl.checkAndRotate()
}

// FlushPeriodically thực hiện Flush theo chu kỳ
func (fl *FileLogger) FlushPeriodically(interval time.Duration) {
	if fl == nil {
		log.Println("FileLogger is nil. Skipping FlushPeriodically.")
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		fl.Flush()
	}
}

// Close đóng file log
func (fl *FileLogger) Close() {
	if fl == nil {
		log.Println("FileLogger is nil. Skipping Close.")
		return
	}

	if err := fl.file.Close(); err != nil {
		log.Printf("Error closing file: %v", err)
	}
}
