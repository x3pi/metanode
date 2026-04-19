package executor

/*
#cgo CFLAGS: -I../../consensus/metanode/src/ffi
#cgo LDFLAGS: -L${SRCDIR}/../../consensus/metanode/target/release -lmetanode -Wl,--allow-multiple-definition
#cgo linux,amd64 LDFLAGS: -lpthread -ldl -lm -lutil -lrt
#include <stdlib.h>
#include <stdint.h>
#include <stdbool.h>

typedef struct {
    bool (*execute_block)(uint8_t* payload, size_t len);
    bool (*process_rpc_request)(uint8_t* req_payload, size_t req_len, uint8_t** out_payload, size_t* out_len);
    void (*free_go_buffer)(uint8_t* ptr);
} GoCallbacks;

void metanode_register_callbacks(GoCallbacks callbacks);
void metanode_start_consensus(const char* config_path, const char* data_dir);
void metanode_pause_consensus();
void metanode_resume_consensus();
bool metanode_submit_transaction_batch(const uint8_t* payload, size_t len);
bool metanode_restore_from_snapshot(const char* data_dir, const char* snapshot_dir);

// Gateway functions that we will export
extern bool cgo_execute_block(uint8_t* payload, size_t len);
extern bool cgo_process_rpc_request(uint8_t* req_payload, size_t req_len, uint8_t** out_payload, size_t* out_len);
extern void cgo_free_go_buffer(uint8_t* ptr);

static inline void register_callbacks_to_rust() {
    GoCallbacks cbs = {
        .execute_block = cgo_execute_block,
        .process_rpc_request = cgo_process_rpc_request,
        .free_go_buffer = cgo_free_go_buffer,
    };
    metanode_register_callbacks(cbs);
}
*/
import "C"
import (
	"fmt"
	"unsafe"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

// Global reference for our handlers since CGo callbacks are global.
var defaultRequestHandler *RequestHandler
var defaultListenerBlockQueue chan *pb.ExecutableBlock

// InitFFIBridge is called from main application startup
func InitFFIBridge(configPath string, dataDir string, reqHandler *RequestHandler, blockQueue chan *pb.ExecutableBlock) error {
	defaultRequestHandler = reqHandler
	defaultListenerBlockQueue = blockQueue

	// Register the global Go functions for Rust to call us
	C.register_callbacks_to_rust()

	// Start the Rust thread asynchronously
	cConfigPath := C.CString(configPath)
	cDataDir := C.CString(dataDir)
	// We do NOT defer C.free(cConfigPath) here if the Rust side takes ownership,
	// but Rust converts to string_lossy. So we can free it.
	defer C.free(unsafe.Pointer(cConfigPath))
	defer C.free(unsafe.Pointer(cDataDir))

	logger.Info("[FFI Bridge] Starting MetaNode Consensus Engine via CGo FFI")
	C.metanode_start_consensus(cConfigPath, cDataDir)

	return nil
}

//export cgo_execute_block
func cgo_execute_block(payload *C.uint8_t, length C.size_t) C.bool {
	data := C.GoBytes(unsafe.Pointer(payload), C.int(length))

	var subDag pb.ExecutableBlock
	err := proto.Unmarshal(data, &subDag)
	if err != nil {
		logger.Error("[FFI Bridge] Failed to unmarshal ExecutableBlock: %v", err)
		return C.bool(false)
	}

	logger.Info("[FFI Bridge] Received block from Rust: block_height=%d", subDag.GetBlockNumber())

	// Dispatch to the listener's channel exactly like unix socket did
	if defaultListenerBlockQueue != nil {
		// Blocking send, exactly as listener.go
		defaultListenerBlockQueue <- &subDag
		return C.bool(true)
	}

	logger.Error("[FFI Bridge] Block queue is not initialized!")
	return C.bool(false)
}

//export cgo_process_rpc_request
func cgo_process_rpc_request(reqPayload *C.uint8_t, reqLen C.size_t, outPayload **C.uint8_t, outLen *C.size_t) C.bool {
	if defaultRequestHandler == nil {
		logger.Error("[FFI Bridge] defaultRequestHandler is nil")
		return C.bool(false)
	}

	data := C.GoBytes(unsafe.Pointer(reqPayload), C.int(reqLen))
	var request pb.Request
	if err := proto.Unmarshal(data, &request); err != nil {
		logger.Error("[FFI Bridge] Failed to unmarshal Request: %v", err)
		return C.bool(false)
	}

	// Dispatch to the request handler directly
	wrappedResponse := defaultRequestHandler.ProcessProtobufRequest(&request)

	// Always send response (even on error)
	if wrappedResponse == nil {
		logger.Error("[FFI Bridge] wrappedResponse is nil - this is a bug!")
		wrappedResponse = &pb.Response{
			Payload: &pb.Response_Error{
				Error: "Internal server error: response is nil",
			},
		}
	}

	// Serialize response
	resData, err := proto.Marshal(wrappedResponse)
	if err != nil {
		logger.Error("[FFI Bridge] Failed to marshal Response: %v", err)
		return C.bool(false)
	}

	// Allocate memory in C to return the response
	cResLen := C.size_t(len(resData))
	cResData := (*C.uint8_t)(C.malloc(cResLen))
	
	// Copy to C memory
	// There is a neat trick: unsafe.Slice
	cSlice := unsafe.Slice((*byte)(unsafe.Pointer(cResData)), len(resData))
	copy(cSlice, resData)

	// Set output pointers
	*outPayload = cResData
	*outLen = cResLen

	return C.bool(true)
}

//export cgo_free_go_buffer
//export cgo_free_go_buffer
func cgo_free_go_buffer(ptr *C.uint8_t) {
	if ptr != nil {
		C.free(unsafe.Pointer(ptr))
	}
}

// SubmitTransactionBatch directly submits a transaction batch to the Rust consensus via zero-copy FFI
func SubmitTransactionBatch(batch []byte) bool {
	if len(batch) == 0 {
		return true // nothing to send
	}

	cPayload := (*C.uint8_t)(unsafe.Pointer(&batch[0]))
	cLen := C.size_t(len(batch))

	// Calling the Rust extern "C" function synchronously.
	// This will just put it in a channel on the Rust side, so it doesn't block.
	res := C.metanode_submit_transaction_batch(cPayload, cLen)
	return bool(res)
}

// PauseRustConsensus signals the Rust side to pause its consensus operations (e.g. before snapshot)
func PauseRustConsensus() {
	C.metanode_pause_consensus()
}

// ResumeRustConsensus signals the Rust side to resume its consensus operations (e.g. after snapshot)
func ResumeRustConsensus() {
	C.metanode_resume_consensus()
}

// RestoreRustConsensusFromSnapshot purges local DAG and restores from the snapshot payload
func RestoreRustConsensusFromSnapshot(dataDir string, snapshotDir string) error {
	cDataDir := C.CString(dataDir)
	cSnapshotDir := C.CString(snapshotDir)
	defer C.free(unsafe.Pointer(cDataDir))
	defer C.free(unsafe.Pointer(cSnapshotDir))

	success := C.metanode_restore_from_snapshot(cDataDir, cSnapshotDir)
	if !success {
		return fmt.Errorf("failed to restore rust_consensus via FFI")
	}
	return nil
}
