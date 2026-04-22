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
	"strings"
	"sync"
	"time"
)

// Global log directory configuration
var globalLogDir = "logs"

const defaultMaxLogReadBytes int64 = 4 * 1024 * 1024 // 4MB

// SetGlobalLogDir sets the global log directory
func SetGlobalLogDir(logDir string) {
	globalLogDir = logDir
}

// GetGlobalLogDir returns the current global log directory
func GetGlobalLogDir() string {
	return globalLogDir
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

// normalizeDateToPath converts an input date string (e.g. 2025-11-13 or 2025/11/13)
// to the directory hierarchy used by the logger (YYYY/MM/DD).
func normalizeDateToPath(date string) (string, error) {
	date = strings.TrimSpace(date)
	if date == "" {
		return "", errors.New("date must not be empty")
	}

	replacer := strings.NewReplacer("\\", "/", "_", "-", ".", "-", " ", "")
	normalized := replacer.Replace(date)
	normalized = strings.Trim(normalized, "/-")

	if len(normalized) == 8 && strings.Count(normalized, "-") == 0 && strings.Count(normalized, "/") == 0 {
		// Format like 20251113
		normalized = fmt.Sprintf("%s-%s-%s", normalized[0:4], normalized[4:6], normalized[6:8])
	} else {
		normalized = strings.ReplaceAll(normalized, "/", "-")
	}

	if strings.Count(normalized, "-") != 2 {
		return "", fmt.Errorf("invalid date format %q", date)
	}

	parsed, err := time.Parse("2006-01-02", normalized)
	if err != nil {
		return "", fmt.Errorf("invalid date value %q: %w", date, err)
	}

	return filepath.Join(parsed.Format("2006"), parsed.Format("01"), parsed.Format("02")), nil
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

// ListLogFiles liệt kê danh sách file log tại ngày tương ứng.
// - root: thư mục gốc chứa logs, mặc định lấy từ globalLogDir nếu bỏ trống.
// - date: định dạng hỗ trợ YYYY-MM-DD, YYYY/MM/DD hoặc YYYYMMDD. Nếu rỗng thì liệt kê trực tiếp tại root.
func ListLogFiles(root, date string) ([]string, error) {
	absRoot, err := resolveLogRoot(root)
	if err != nil {
		return nil, err
	}

	targetDir := absRoot
	if strings.TrimSpace(date) != "" {
		datePath, err := normalizeDateToPath(date)
		if err != nil {
			return nil, err
		}
		targetDir = filepath.Join(targetDir, datePath)
	}

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

// ReadLogFile đọc nội dung file log theo ngày.
// Nếu file lớn hơn maxBytes, chỉ đọc phần cuối file (theo maxBytes) và loại bỏ dòng đầu bị cắt dở.
func ReadLogFile(root, date, fileName string, maxBytes int64) (string, error) {
	fullPath, err := ResolveLogFilePath(root, date, fileName)
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

// ResolveLogFilePath trả về đường dẫn tuyệt đối tới file log theo ngày.
func ResolveLogFilePath(root, date, fileName string) (string, error) {
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

	targetDir := absRoot
	if strings.TrimSpace(date) != "" {
		datePath, err := normalizeDateToPath(date)
		if err != nil {
			return "", err
		}
		targetDir = filepath.Join(targetDir, datePath)
	}

	if err := ensureWithinRoot(absRoot, targetDir); err != nil {
		return "", err
	}

	fullPath := filepath.Clean(filepath.Join(targetDir, fileName))
	if err := ensureWithinRoot(absRoot, fullPath); err != nil {
		return "", err
	}
	return fullPath, nil
}

// FileLogger struct quản lý ghi log vào file
type FileLogger struct {
	file    *os.File
	entries []LogEntry
	mutex   sync.Mutex
}

// NewFileLogger tạo mới một FileLogger
func NewFileLogger(filePath string) (*FileLogger, error) {
	// Tự động tạo cấu trúc thư mục theo ngày
	logDir := globalLogDir
	dateDir := time.Now().Format("2006/01/02")
	fullPath := filepath.Join(logDir, dateDir, filePath)

	// Tạo thư mục logs theo ngày nếu chưa tồn tại
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
	return &FileLogger{
		file:    file,
		entries: make([]LogEntry, 0),
	}, nil
}

// File trả về *os.File gốc để có thể dùng làm output cho logger chuẩn.
func (fl *FileLogger) File() *os.File {
	if fl == nil {
		return nil
	}
	return fl.file
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
	if _, err := fl.file.WriteString(logMessage); err != nil {
		log.Printf("Failed to write log message: %v", err)
	}
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
	if _, err := fl.file.WriteString(logMessage); err != nil {
		log.Printf("Failed to write log message: %v", err)
	}
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
	if _, err := fl.file.Write(data); err != nil {
		log.Printf("Error writing to file: %v", err)
		return
	}
	fl.entries = fl.entries[:0] // Xóa dữ liệu sau khi ghi
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
