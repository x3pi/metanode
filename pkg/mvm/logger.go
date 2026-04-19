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
	"encoding/hex"
	"fmt"
	"os"
	"unsafe"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// InitMVMCppLog redirect C++ stdout/stderr sang file riêng.
// Gọi hàm này 1 lần khi khởi động, TRƯỚC khi chạy bất kỳ MVM execution nào.
// logDir: thư mục chứa file log. Nếu rỗng, dùng MVM_LOG_DIR env var.
// processName: tên process (ví dụ "master", "sub-write") → tạo file mvm_cpp_{processName}.log
func InitMVMCppLog(logDir string, processName string) {
	if logDir == "" {
		logDir = os.Getenv("MVM_LOG_DIR")
	}
	if logDir == "" {
		return
	}
	cLogDir := C.CString(logDir)
	defer C.free(unsafe.Pointer(cLogDir))
	cName := C.CString(processName)
	defer C.free(unsafe.Pointer(cName))
	C.InitCppFileLog(cLogDir, cName)
	logFile := "mvm_cpp_" + processName + ".log"
	if processName == "" {
		logFile = "mvm_cpp_debug.log"
	}
	fmt.Printf("[MVM_LOG] ✅ C++ log redirected → %s/%s\n", logDir, logFile)
}

// CloseMVMCppLog đóng file log C++ và restore stdout/stderr.
func CloseMVMCppLog() {
	C.CloseCppFileLog()
}

//export GoLogString
func GoLogString(
	flag C.int,
	cString *C.char,
) {
	message := C.GoString(cString)

	// Thử ghi vào file MVM riêng trước
	var level string
	switch int(flag) {
	case 0:
		level = "INFO"
	case 1:
		level = "DEBUG"
	case 2:
		level = "DEBUGP"
	case 3:
		level = "WARN"
	case 4:
		level = "ERROR"
	default:
		level = "INFO"
	}

	if mvmFileLog(level, message) {
		return // Đã ghi vào file MVM riêng, không cần log ra stdout
	}

	// Fallback: ghi vào logger bình thường nếu MVM file log tắt
	switch int(flag) {
	case 0:
		logger.Info(message)
	case 1:
		logger.Debug(message)
	case 2:
		logger.DebugP(message)
	case 3:
		logger.Warn(message)
	case 4:
		logger.Error(message)
	}
}

//export GoLogBytes
func GoLogBytes(
	flag C.int,
	bytes *C.uchar,
	size C.int,
) {
	bMessage := C.GoBytes(unsafe.Pointer(bytes), size)
	hexStr := hex.EncodeToString(bMessage)

	// Thử ghi vào file MVM riêng trước
	var level string
	switch int(flag) {
	case 0:
		level = "INFO"
	case 1:
		level = "DEBUG"
	case 2:
		level = "DEBUGP"
	case 3:
		level = "WARN"
	case 4:
		level = "ERROR"
	default:
		level = "INFO"
	}

	if mvmFileLog(level, hexStr) {
		return // Đã ghi vào file MVM riêng
	}

	// Fallback: ghi vào logger bình thường
	switch int(flag) {
	case 0:
		logger.Info(hexStr)
	case 1:
		logger.Debug(hexStr)
	case 2:
		logger.DebugP(hexStr)
	case 3:
		logger.Warn(hexStr)
	case 4:
		logger.Error(hexStr)
	}
}
