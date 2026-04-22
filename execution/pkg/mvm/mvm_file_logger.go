package mvm

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// mvmFileLogger quản lý việc ghi log MVM ra file riêng để dễ debug.
// Tất cả log liên quan MVM (từ C++ callback VÀ từ Go code) sẽ được ghi vào file này
// thay vì trộn lẫn vào stdout/log chung của Go process.
//
// Cách 1: Set biến môi trường MVM_LOG_DIR trước khi chạy.
// Cách 2: Gọi SetMVMLogDir(dir) từ Go code khi init.
//
// File log sẽ được tạo tại: $MVM_LOG_DIR/mvm_debug.log

var (
	mvmLogMu       sync.Mutex
	mvmLogFile     *os.File
	mvmLogEnabled  bool
	mvmLogInitOnce sync.Once
)

// SetMVMLogDir cho phép Go code set thư mục log MVM trực tiếp.
// Gọi hàm này TRƯỚC khi bất kỳ MVM log nào được ghi.
// Nếu đã init rồi (qua env hoặc lần gọi trước), hàm này sẽ bị bỏ qua.
func SetMVMLogDir(logDir string) {
	if logDir == "" {
		return
	}
	// Set env var để initMVMFileLogger pick up
	os.Setenv("MVM_LOG_DIR", logDir)
}

// initMVMFileLogger khởi tạo file logger cho MVM.
// Chỉ chạy 1 lần duy nhất khi có log đầu tiên.
func initMVMFileLogger() {
	mvmLogInitOnce.Do(func() {
		logDir := os.Getenv("MVM_LOG_DIR")
		if logDir == "" {
			// Không set MVM_LOG_DIR → tắt file logging cho MVM
			mvmLogEnabled = false
			return
		}

		// Tạo thư mục log nếu chưa tồn tại
		if err := os.MkdirAll(logDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "[MVM_LOG] Error creating log dir %s: %v\n", logDir, err)
			mvmLogEnabled = false
			return
		}

		logPath := filepath.Join(logDir, "mvm_debug.log")
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[MVM_LOG] Error opening log file %s: %v\n", logPath, err)
			mvmLogEnabled = false
			return
		}

		mvmLogFile = f
		mvmLogEnabled = true
		fmt.Printf("[MVM_LOG] ✅ MVM debug log enabled → %s\n", logPath)
	})
}

// mvmFileLog ghi một dòng log vào file MVM riêng.
// Trả về true nếu đã ghi thành công (caller không cần log ra stdout nữa).
// Trả về false nếu file logging tắt (caller nên fallback về logger bình thường).
func mvmFileLog(level string, message string) bool {
	initMVMFileLogger()

	if !mvmLogEnabled || mvmLogFile == nil {
		return false
	}

	mvmLogMu.Lock()
	defer mvmLogMu.Unlock()

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	line := fmt.Sprintf("[%s][%s][MVM] %s\n", timestamp, level, message)
	_, err := mvmLogFile.WriteString(line)
	if err != nil {
		// Nếu ghi lỗi, trả về false để fallback
		return false
	}
	return true
}

// CloseMVMFileLogger đóng file log MVM.
// Gọi khi shutdown gracefully.
func CloseMVMFileLogger() {
	mvmLogMu.Lock()
	defer mvmLogMu.Unlock()

	if mvmLogFile != nil {
		mvmLogFile.Sync()
		mvmLogFile.Close()
		mvmLogFile = nil
		mvmLogEnabled = false
	}
}
