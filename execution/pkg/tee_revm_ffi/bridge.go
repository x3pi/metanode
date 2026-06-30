package tee_revm_ffi

/*
#cgo CFLAGS: -I${SRCDIR}
#cgo LDFLAGS: -L${SRCDIR}/../../../target/release -lmetanode_tee_revm -lm -ldl -lpthread
#include "metanode_tee_revm.h"
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"unsafe"
)

type TeeRevm struct {
}

func NewTeeRevm() *TeeRevm {
	return &TeeRevm{}
}

// ExecuteTx calls the Rust TEE REVM execution engine.
// Returns a deserialized State Diff if successful.
func (t *TeeRevm) ExecuteTx(caller, target, calldata []byte, gasLimit uint64) (*TeeStateDiff, error) {
	if len(caller) != 20 {
		return nil, fmt.Errorf("caller must be 20 bytes")
	}
	if len(target) != 20 {
		return nil, fmt.Errorf("target must be 20 bytes")
	}

	cCaller := (*C.uint8_t)(unsafe.Pointer(&caller[0]))
	cTarget := (*C.uint8_t)(unsafe.Pointer(&target[0]))
	
	var cCalldata *C.uint8_t
	var cCalldataLen C.size_t
	
	if len(calldata) > 0 {
		cCalldata = (*C.uint8_t)(unsafe.Pointer(&calldata[0]))
		cCalldataLen = C.size_t(len(calldata))
	} else {
		cCalldata = nil
		cCalldataLen = 0
	}

	cGasLimit := C.uint64_t(gasLimit)

	// Allocate buffer for State Diff
	outBuffer := make([]byte, 8192)
	cOutBuffer := (*C.uint8_t)(unsafe.Pointer(&outBuffer[0]))
	cOutBufferLen := C.size_t(len(outBuffer))

	written := int32(C.revm_execute_tx(cCaller, cTarget, cCalldata, cCalldataLen, cGasLimit, cOutBuffer, cOutBufferLen))

	if written < 0 {
		return nil, fmt.Errorf("revm_execute_tx failed with code: %d", written)
	}

	return DeserializeTeeStateDiff(outBuffer[:written])
}
