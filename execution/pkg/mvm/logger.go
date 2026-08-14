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

// logAtFlag maps mvm's numeric log flag (0=INFO/1=DEBUG/2=DEBUGP/3=WARN/
// 4=ERROR) to this package's own logger, trying the dedicated MVM file log
// first and falling back to the normal logger — the single place both
// GoLogString/GoLogBytes (still exported for cgo, no longer called from
// C++, see MyLogger's doc comment in linker/include/my_logger.h) and the
// TEE-packaging B2 post-return flush path (FlushNativeLogs below) route
// through, so the two can never drift in how a given flag gets leveled.
func logAtFlag(flag int, message string) {
	var level string
	switch flag {
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
	switch flag {
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

// FlushNativeLogs (TEE-packaging B2, note/tee_core_packaging_plan.md)
// prints everything the interpreter buffered during one Call/Execute/Deploy
// (see MVMExecuteResult.NativeLogs), in the same order and at the same
// levels GoLogString/GoLogBytes used to apply live, mid-execution — the
// call sites in mvm_api.go invoke this once, after extractExecuteResult,
// instead of C++ calling back into Go per log line.
func FlushNativeLogs(entries []NativeLogEntry) {
	for _, e := range entries {
		logAtFlag(int(e.Flag), e.Message)
	}
}

//export GoLogString
func GoLogString(
	flag C.int,
	cString *C.char,
) {
	logAtFlag(int(flag), C.GoString(cString))
}

//export GoLogBytes
func GoLogBytes(
	flag C.int,
	bytes *C.uchar,
	size C.int,
) {
	bMessage := C.GoBytes(unsafe.Pointer(bytes), size)
	logAtFlag(int(flag), hex.EncodeToString(bMessage))
}
