package executor

/*
#cgo CFLAGS: -I../../consensus/metanode/src/ffi
#cgo LDFLAGS: -L${SRCDIR}/../../consensus/metanode/target/release -lmetanode -Wl,--allow-multiple-definition
#cgo linux,amd64 LDFLAGS: -lpthread -ldl -lm -lutil -lrt
#include <stdlib.h>
#include <stdint.h>
#include <stdbool.h>

typedef struct {
    bool (*execute_block)(uint8_t* payload, size_t len, uint8_t** out_payload, size_t* out_len);
    bool (*process_rpc_request)(uint8_t* req_payload, size_t req_len, uint8_t** out_payload, size_t* out_len);
    void (*free_go_buffer)(uint8_t* ptr);
    char* (*get_state_root)();
    void (*update_tx_trace)(uint8_t* hash_ptr, char* step_ptr, char* details_ptr);
} GoCallbacks;

void metanode_register_callbacks(GoCallbacks callbacks);
void metanode_start_consensus(const char* config_path, const char* data_dir);
void metanode_pause_consensus();
void metanode_resume_consensus();
bool metanode_submit_transaction_batch(const uint8_t* payload, size_t len);
bool metanode_restore_from_snapshot(const char* data_dir, const char* snapshot_dir);

// Gateway functions that we will export
extern bool cgo_execute_block(uint8_t* payload, size_t len, uint8_t** out_payload, size_t* out_len);
extern bool cgo_process_rpc_request(uint8_t* req_payload, size_t req_len, uint8_t** out_payload, size_t* out_len);
extern void cgo_free_go_buffer(uint8_t* ptr);
extern char* cgo_get_state_root();
extern void cgo_update_tx_trace(uint8_t* hash_ptr, char* step_ptr, char* details_ptr);

static inline void register_callbacks_to_rust() {
    GoCallbacks cbs = {
        .execute_block = cgo_execute_block,
        .process_rpc_request = cgo_process_rpc_request,
        .free_go_buffer = cgo_free_go_buffer,
        .get_state_root = cgo_get_state_root,
        .update_tx_trace = cgo_update_tx_trace,
    };
    metanode_register_callbacks(cbs);
}
*/
import "C"
import (
	"fmt"
	"time"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

// Global reference for our handlers since CGo callbacks are global.
var defaultRequestHandler *RequestHandler
var defaultListenerBlockQueue chan *pb.ExecutableBlock
var traceCallback func(hash common.Hash, step string, details string)

// executeBlockResponseTimeout bounds how long cgo_execute_block waits for
// the speculative-execution/commit pipeline to respond after a block has
// been successfully enqueued. See the call site for why this must never be
// unbounded — a stuck response here previously froze the entire Rust->Go
// delivery pipeline permanently, with no self-recovery.
const executeBlockResponseTimeout = 10 * time.Second

// RegisterTraceCallback sets the callback function to update transaction traces in Go's memory
func RegisterTraceCallback(cb func(hash common.Hash, step string, details string)) {
	traceCallback = cb
}

// AuthoritativeBlockRequest wraps a block + response channel for synchronous GEI assignment.
// When is_authoritative_gei=true, the block processor sends back the assigned GEI via ResponseCh.
type AuthoritativeBlockRequest struct {
	Block      *pb.ExecutableBlock
	ResponseCh chan *pb.ExecuteBlockResponse
}

// Global channel for authoritative mode block requests (synchronous).
// When nil, falls back to legacy fire-and-forget mode.
var defaultAuthoritativeBlockQueue chan *AuthoritativeBlockRequest

// InitFFIBridge is called from main application startup
func InitFFIBridge(configPath string, dataDir string, reqHandler *RequestHandler, blockQueue chan *pb.ExecutableBlock) error {
	defaultRequestHandler = reqHandler
	defaultListenerBlockQueue = blockQueue

	// Initialize authoritative block queue (buffered to prevent deadlock during burst)
	defaultAuthoritativeBlockQueue = make(chan *AuthoritativeBlockRequest, 1000)

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

// GetAuthoritativeBlockQueue returns the channel for authoritative mode block processing.
// The block processor (processRustEpochData) reads from this channel when authoritative mode is enabled.
func GetAuthoritativeBlockQueue() <-chan *AuthoritativeBlockRequest {
	return defaultAuthoritativeBlockQueue
}

//export cgo_execute_block
func cgo_execute_block(payload *C.uint8_t, length C.size_t, outPayload **C.uint8_t, outLen *C.size_t) (ret C.bool) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("[FFI Bridge] ⚠️ PANIC recovered in cgo_execute_block: %v", r)
			serializeAndSetResponse(&pb.ExecuteBlockResponse{
				Success: false,
				Error:   fmt.Sprintf("Panic in Go FFI handler: %v", r),
			}, outPayload, outLen)
			ret = C.bool(false)
		}
	}()

	// Sanity check length to prevent overflow or out-of-memory allocations
	if payload == nil || length == 0 || length > 100*1024*1024 {
		logger.Error("[FFI Bridge] Invalid block payload or length from Rust: %d", length)
		serializeAndSetResponse(&pb.ExecuteBlockResponse{
			Success: false,
			Error:   "invalid block payload or length",
		}, outPayload, outLen)
		return C.bool(false)
	}

	data := C.GoBytes(unsafe.Pointer(payload), C.int(length))

	var subDag pb.ExecutableBlock
	err := subDag.UnmarshalVT(data)
	if err != nil {
		logger.Error("[FFI Bridge] Failed to unmarshal ExecutableBlock: %v", err)
		serializeAndSetResponse(&pb.ExecuteBlockResponse{
			Success: false,
			Error:   err.Error(),
		}, outPayload, outLen)
		return C.bool(false)
	}

	logger.Debug("[FFI Bridge] Received block from Rust: block_height=%d, authoritative=%v",
		subDag.GetBlockNumber(), subDag.GetIsAuthoritativeGei())

	if defaultAuthoritativeBlockQueue != nil {
		responseCh := make(chan *pb.ExecuteBlockResponse, 1)
		req := &AuthoritativeBlockRequest{
			Block:      &subDag,
			ResponseCh: responseCh,
		}

		// Non-blocking send to authQueue (1000 buffer).
		// If queue is full, Go is severely behind — drop block.
		select {
		case defaultAuthoritativeBlockQueue <- req:
			// Wait for speculative executor to finish and return actual authoritative response.
			//
			// BOUNDED WAIT (Aug 2026): previously this was an unbounded `<-req.ResponseCh`
			// with no timeout at all. If anything downstream (ExecuteSpeculative's goroutine,
			// StartCommitterLoop, commitSpeculativeResult) got stuck for any reason and never
			// sent a response, this CGO call — and therefore the calling Rust goroutine
			// (tokio::task::spawn_blocking, itself awaited with no timeout on the Rust side) —
			// would block forever. Since Rust's block-sending pipeline sends one block at a
			// time and waits for each CGO call to return before sending the next, one stuck
			// response permanently froze the entire Rust->Go delivery pipeline (observed
			// directly: 3-node cluster stalled 31+ minutes, consensus rounds kept advancing
			// fine, only delivery to Go was stuck). A bounded wait here turns that into a
			// bounded, visible failure that Rust's existing retry path already handles
			// (record_send_failure -> circuit breaker / later resend), instead of a silent
			// permanent hang requiring a manual restart.
			select {
			case response := <-req.ResponseCh:
				serializeAndSetResponse(response, outPayload, outLen)
				return C.bool(true)
			case <-time.After(executeBlockResponseTimeout):
				logger.Error("[FFI BRIDGE] Timeout waiting for speculative execution response (GEI=%d) — treating as failure so Rust can retry instead of hanging forever", subDag.GetGlobalExecIndex())
				serializeAndSetResponse(&pb.ExecuteBlockResponse{
					Success: false,
					Error:   "timed out waiting for Go execution response",
				}, outPayload, outLen)
				return C.bool(false)
			}
		case <-time.After(5 * time.Second):
			// Timeout if authoritative queue is completely blocked
			logger.Error("[FFI BRIDGE] Timeout sending to authoritative queue")
			return C.bool(false)
		}
	}

	// FALLBACK: Use dataChan if authQueue not initialized
	if defaultListenerBlockQueue != nil {
		select {
		case defaultListenerBlockQueue <- &subDag:
			serializeAndSetResponse(&pb.ExecuteBlockResponse{
				Success: true,
			}, outPayload, outLen)
			return C.bool(true)
		case <-time.After(5 * time.Second):
			logger.Error("[FFI Bridge] dataChan blocked for 5s. GEI=%d",
				subDag.GetGlobalExecIndex())
			serializeAndSetResponse(&pb.ExecuteBlockResponse{
				Success: false,
				Error:   "dataChan blocked for 5s",
			}, outPayload, outLen)
			return C.bool(false)
		}
	}

	logger.Error("[FFI Bridge] Block queue is not initialized!")
	serializeAndSetResponse(&pb.ExecuteBlockResponse{
		Success: false,
		Error:   "block queue is not initialized",
	}, outPayload, outLen)
	return C.bool(false)
}

func serializeAndSetResponse(resp *pb.ExecuteBlockResponse, outPayload **C.uint8_t, outLen *C.size_t) {
	if resp == nil || outPayload == nil || outLen == nil {
		return
	}
	resData, err := proto.Marshal(resp)
	if err != nil {
		logger.Error("[FFI Bridge] Failed to marshal ExecuteBlockResponse: %v", err)
		return
	}
	cResLen := C.size_t(len(resData))
	cResData := (*C.uint8_t)(C.malloc(cResLen))
	cSlice := unsafe.Slice((*byte)(unsafe.Pointer(cResData)), len(resData))
	copy(cSlice, resData)
	*outPayload = cResData
	*outLen = cResLen
}

//export cgo_process_rpc_request
func cgo_process_rpc_request(reqPayload *C.uint8_t, reqLen C.size_t, outPayload **C.uint8_t, outLen *C.size_t) (ret C.bool) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("[FFI Bridge] ⚠️ PANIC recovered in cgo_process_rpc_request: %v", r)
			ret = C.bool(false)
		}
	}()

	if defaultRequestHandler == nil {
		logger.Error("[FFI Bridge] defaultRequestHandler is nil")
		return C.bool(false)
	}

	// Sanity check length to prevent overflow or out-of-memory allocations
	// Raise limit to 1GB to support large SyncBlocksRequest payloads safely
	if reqPayload == nil || reqLen == 0 || reqLen > 1024*1024*1024 {
		logger.Error("[FFI Bridge] Invalid rpc request payload or length from Rust: %d", reqLen)
		return C.bool(false)
	}

	data := C.GoBytes(unsafe.Pointer(reqPayload), C.int(reqLen))
	var request pb.Request
	if err := request.UnmarshalVT(data); err != nil {
		logger.Error("[FFI Bridge] Failed to unmarshal Request: %v", err)
		return C.bool(false)
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// FORK FIX (Zero-Fork Invariant): Remove timeout for RPC requests.
	// Previously, if an RPC request (like SyncBlocks) took longer than rpcTimeout,
	// Go would return an error to Rust, causing Rust to retry. BUT the Go goroutine
	// would STILL be running in the background, mutating the DB! A retry would spawn
	// a second concurrent mutation on PebbleDB/NOMT, leading to massive DB corruption
	// and FORKS. We MUST process synchronously and wait indefinitely (honest backpressure).
	// ═══════════════════════════════════════════════════════════════════════════

	var wrappedResponse *pb.Response

	func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("[FFI Bridge] ⚠️ PANIC recovered in cgo_process_rpc_request: %v", r)
				wrappedResponse = &pb.Response{
					Payload: &pb.Response_Error{
						Error: fmt.Sprintf("Panic in Go RPC handler: %v", r),
					},
				}
			}
		}()
		wrappedResponse = defaultRequestHandler.ProcessProtobufRequest(&request)
	}()

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
func cgo_free_go_buffer(ptr *C.uint8_t) {
	if ptr != nil {
		C.free(unsafe.Pointer(ptr))
	}
}

//export cgo_get_state_root
func cgo_get_state_root() *C.char {
	sm := GetGlobalSnapshotManager()
	if sm != nil && sm.stateRootCallback != nil {
		root := sm.stateRootCallback()
		if root != "" {
			return C.CString(root) // Remember that Rust calls free_go_buffer on this pointer
		}
	}
	return nil
}

//export cgo_update_tx_trace
func cgo_update_tx_trace(hashPtr *C.uint8_t, stepPtr *C.char, detailsPtr *C.char) {
	if hashPtr == nil || traceCallback == nil {
		return
	}
	hash := *(*common.Hash)(unsafe.Pointer(hashPtr))
	step := C.GoString(stepPtr)
	details := C.GoString(detailsPtr)
	traceCallback(hash, step, details)
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
