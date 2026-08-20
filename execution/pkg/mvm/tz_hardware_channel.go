package mvm

/*
#include <fcntl.h>
#include <unistd.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <string.h>
#include <stdlib.h>
#include <errno.h>
#include "tzproto/mvm_tz_protocol.h"

// ─── driver ioctl/struct definitions ───
// Byte-identical copy of tz-llm-trustzone's tzdriver/tc_ns_client.h
// struct, taken from ta/ca_test/mvm_ca_test.cpp (the hardware-proven
// reference this whole file mirrors) rather than re-derived — see that
// file's own comment for the static_assert(sizeof==24) cross-check
// against tz-llm/llama.cpp/src/alloc-stage.cpp's own hand-copy.
struct llm_client_op_pages {
    int cma_index;
    int entry_index;
    unsigned long size;
    unsigned long offset;
};

#define TC_NS_CLIENT_IOC_MAGIC 't'

// Exposed as real C constants (not left as bare macros) so Go can call
// C.ioctl(...) directly and get cgo's normal errno-populating two-value
// return convention, instead of routing through a hand-written C helper
// that would need its own separate errno plumbing back to Go.
const unsigned long MVM_TZ_HW_IOCTL_SET_PAGES =
    _IOWR(TC_NS_CLIENT_IOC_MAGIC, 27, struct llm_client_op_pages);
const unsigned long MVM_TZ_HW_IOCTL_RUN =
    _IOWR(TC_NS_CLIENT_IOC_MAGIC, 24, int);

#define MVM_TZASC_CMA_INDEX 1
#define MVM_TZASC_ENTRY_INDEX 0

enum smc_loop_exit {
    SMC_LOOP_EXIT_FINISH = 1,
    SMC_LOOP_EXIT_NPU_SUBMIT,
    SMC_LOOP_EXIT_NPU_DONE,
    SMC_LOOP_EXIT_IO_STEP,
};

// cgo cannot call ioctl() directly (its real libc prototype is variadic:
// `int ioctl(int fd, unsigned long request, ...)`) -- these two thin,
// non-variadic wrappers are the ONLY reason this file needs any C helper
// functions at all; everything else (open/mmap/close) cgo binds to
// directly below, using its normal two-value errno-return convention.

// Returns the raw ioctl() return value; *out_errno is errno right after
// that call iff it was non-zero, 0 otherwise (captured inside the same C
// call so no intervening Go/cgo transition can clobber it first).
static int mvm_tz_hw_ioctl_set_pages(int fd, struct llm_client_op_pages *req, int *out_errno) {
    int rc = ioctl(fd, MVM_TZ_HW_IOCTL_SET_PAGES, req);
    *out_errno = (rc != 0) ? errno : 0;
    return rc;
}

// LLM_CLIENT_IOCTL_RUN's own return value is never checked on the C++
// reference side either (ta/ca_test/mvm_ca_test.cpp:118) -- only out_cmd
// matters.
static void mvm_tz_hw_ioctl_run(int fd, int *out_cmd) {
    *out_cmd = -1;
    ioctl(fd, MVM_TZ_HW_IOCTL_RUN, out_cmd);
}
*/
import "C"

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// tzHardwareChannel is tzChannel's real-hardware sibling (tz_channel.go)
// — same wire format, same C.mvm_tz_channel_t layout, same spinlock, but
// backed by the actual TZASC CMA page the driver maps in from mvm_ta
// (via /dev/tc_ns_client) instead of a C.malloc'd stand-in. See
// note/tee_dual_mode_execution_plan.md's "Giai đoạn 3b" step 4.
//
// Only ever constructed once per process (mirrors tzChannel's own
// sync.Once singleton, tz_channel.go's getTZChannel) — there is exactly
// one TZASC CMA page mvm_ta pushes, at a fixed (cma_index=1,
// entry_index=0), matching mvm_ta_main.cpp's own
// MVM_TZASC_CMA_INDEX/MVM_TZASC_ENTRY_INDEX.
type tzHardwareChannel struct {
	dev *os.File // kept alive here -- its finalizer would close the fd otherwise
	fd  C.int
	ptr *C.mvm_tz_channel_t
}

// newTZHardwareChannel runs the exact proven open -> start-relay -> sleep
// -> SET_PAGES -> mmap -> protocol_version-check sequence from
// ta/ca_test/mvm_ca_test.cpp's main() (lines ~1008-1055), not a
// re-derivation of it — ONE fd, shared between the relay goroutine and
// SET_PAGES/mmap, in that exact order: mvm_ta's push_pages() SMC cannot
// complete (SET_PAGES will hang) until something is actively servicing
// MVM_TZ_HW_IOCTL_RUN, so the relay must already be running first.
//
// Does NOT write protocol_version itself (the TA owns initializing the
// page it pushes) — only verifies it, per this project's own
// "don't guess, verify" convention.
func newTZHardwareChannel() (*tzHardwareChannel, error) {
	// os.OpenFile, not C.open: ioctl() is variadic in its real libc
	// prototype and cgo cannot call variadic C functions directly (a real
	// build error, not a style choice) -- open()/close() have the same
	// restriction on this platform's headers, so this file uses Go's own
	// os package for those two and keeps C helper wrappers (below) only
	// for the two ioctl calls, which genuinely need one.
	dev, err := os.OpenFile("/dev/tc_ns_client", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("mvm: newTZHardwareChannel: open(/dev/tc_ns_client): %w", err)
	}
	fd := C.int(dev.Fd())

	startReverseCallRelay(fd)
	// Give push_pages a moment to actually land before SET_PAGES checks
	// for it -- mirrors mvm_ca_test.cpp's own 200ms sleep right here (its
	// own comment: SET_PAGES itself doesn't strictly depend on push
	// having landed yet, but this makes the intent -- and any early
	// failure -- visible in the right order).
	time.Sleep(200 * time.Millisecond)

	var req C.struct_llm_client_op_pages
	req.cma_index = C.MVM_TZASC_CMA_INDEX
	req.entry_index = C.MVM_TZASC_ENTRY_INDEX
	var setPagesErrno C.int
	if rc := C.mvm_tz_hw_ioctl_set_pages(fd, &req, &setPagesErrno); rc != 0 {
		dev.Close()
		return nil, fmt.Errorf("mvm: newTZHardwareChannel: MVM_TZ_HW_IOCTL_SET_PAGES failed, rc=%d errno=%d", int(rc), int(setPagesErrno))
	}

	// Round channel_size up to a page boundary, same as mvm_ca_test.cpp.
	const pageSize = 4096
	channelSize := (C.sizeof_mvm_tz_channel_t + pageSize - 1) &^ (pageSize - 1)

	addr, mmapErr := C.mmap(nil, C.size_t(channelSize), C.PROT_READ|C.PROT_WRITE, C.MAP_SHARED, fd, 0)
	if addr == nil || uintptr(addr) == ^uintptr(0) /* MAP_FAILED */ {
		dev.Close()
		return nil, fmt.Errorf("mvm: newTZHardwareChannel: mmap: %w", mmapErr)
	}

	ch := &tzHardwareChannel{dev: dev, fd: fd, ptr: (*C.mvm_tz_channel_t)(addr)}
	if ch.ptr.protocol_version != C.MVM_TZ_PROTOCOL_VERSION {
		return nil, fmt.Errorf("mvm: newTZHardwareChannel: protocol_version mismatch: got %d, want %d "+
			"(mvm_ta hasn't initialized this page yet -- wrong entry_index, or mvm_launcher hasn't launched it)",
			ch.ptr.protocol_version, C.MVM_TZ_PROTOCOL_VERSION)
	}
	return ch, nil
}

// writeMessage/readMessage/postRequestReady/postResponseReady mirror
// tzChannel's own (tz_channel.go) byte-for-byte — same struct, same
// spinlock, same blob_region copy logic. Duplicated rather than shared
// via an interface/embedding because tzChannel's fields (a bare
// *C.mvm_tz_channel_t) are already exactly what's needed here too; the
// ONLY difference between the two types is how the page was obtained
// (newTZChannel's C.malloc vs newTZHardwareChannel's mmap above) and
// that this one also owns an fd to close.
func (c *tzHardwareChannel) writeMessage(cmd C.mvm_tz_cmd_t, direction C.mvm_tz_direction_t, header, blob []byte) error {
	total := len(header) + len(blob)
	if total > C.MVM_TZ_BLOB_REGION_SIZE {
		return fmt.Errorf("mvm: tzHardwareChannel.writeMessage: total %d exceeds blob region %d", total, C.MVM_TZ_BLOB_REGION_SIZE)
	}
	C.mvm_tz_spinlock_lock(&c.ptr.lock)
	defer C.mvm_tz_spinlock_unlock(&c.ptr.lock)
	if len(header) > 0 {
		C.memcpy(unsafe.Pointer(&c.ptr.blob_region[0]), unsafe.Pointer(&header[0]), C.size_t(len(header)))
	}
	if len(blob) > 0 {
		C.memcpy(unsafe.Pointer(&c.ptr.blob_region[len(header)]), unsafe.Pointer(&blob[0]), C.size_t(len(blob)))
	}
	c.ptr.cmd = cmd
	c.ptr.direction = direction
	c.ptr.header_len = C.uint32_t(len(header))
	c.ptr.blob_len = C.uint32_t(len(blob))
	c.ptr.seq++
	return nil
}

func (c *tzHardwareChannel) readMessage() (cmd C.mvm_tz_cmd_t, direction C.mvm_tz_direction_t, header, blob []byte) {
	C.mvm_tz_spinlock_lock(&c.ptr.lock)
	defer C.mvm_tz_spinlock_unlock(&c.ptr.lock)
	cmd = c.ptr.cmd
	direction = c.ptr.direction
	hlen := c.ptr.header_len
	blen := c.ptr.blob_len
	if hlen > 0 {
		header = C.GoBytes(unsafe.Pointer(&c.ptr.blob_region[0]), C.int(hlen))
	}
	if blen > 0 {
		blob = C.GoBytes(unsafe.Pointer(&c.ptr.blob_region[hlen]), C.int(blen))
	}
	return
}

func (c *tzHardwareChannel) postRequestReady()  { C.mvm_tz_flag_set(&c.ptr.request_ready, 1) }
func (c *tzHardwareChannel) postResponseReady() { C.mvm_tz_flag_set(&c.ptr.response_ready, 1) }

// consumeRequestReadyForDirection/consumeResponseReadyForDirection are
// direction-aware, UNLIKE tzChannel's plain consumeRequestReady/
// consumeResponseReady (tz_channel.go) — loopback mode never needed this
// because tzLoopbackRoundTrip drives both "sides" itself, strictly
// alternating, so whichever flag it expects next is always the right
// one. A real hardware round trip has no such guarantee (the TA can
// issue any number of reverse calls before its final answer) — consuming
// a flag without checking direction first risks stealing the channel's
// own not-yet-relevant signal, exactly the bidirectional-flag race
// documented in tz-llm-trustzone's own memory
// (mvm-ca-test-bidirectional-flag-race) and fixed in mvm_ca_test.cpp's
// own wait loop (ta/ca_test/mvm_ca_test.cpp:882-931) this mirrors.
func (c *tzHardwareChannel) consumeRequestReadyForDirection(want C.mvm_tz_direction_t) bool {
	if C.mvm_tz_flag_get(&c.ptr.request_ready) != 1 || c.ptr.direction != want {
		return false
	}
	return C.mvm_tz_flag_cas(&c.ptr.request_ready, 1, 0) != 0
}

func (c *tzHardwareChannel) consumeResponseReadyForDirection(want C.mvm_tz_direction_t) bool {
	if C.mvm_tz_flag_get(&c.ptr.response_ready) != 1 || c.ptr.direction != want {
		return false
	}
	return C.mvm_tz_flag_cas(&c.ptr.response_ready, 1, 0) != 0
}

var (
	tzHwChannelOnce sync.Once
	tzHwChannelInst *tzHardwareChannel
	tzHwChannelErr  error
	tzHwRelayOnce   sync.Once
)

// getTZHardwareChannel is tzChannel's getTZChannel (tz_channel.go)
// sibling — lazily constructs the one process-wide hardware channel
// (which, per newTZHardwareChannel's own doc comment, also starts the
// relay goroutine as its first step — callers don't need to do that
// separately).
func getTZHardwareChannel() (*tzHardwareChannel, error) {
	tzHwChannelOnce.Do(func() {
		tzHwChannelInst, tzHwChannelErr = newTZHardwareChannel()
	})
	return tzHwChannelInst, tzHwChannelErr
}

// startReverseCallRelay starts the SMC-servicing relay goroutine exactly
// once per process — mirrors ca_relay_thread (ta/ca_test/mvm_ca_test.cpp:
// 110-129) unchanged, including its idle-backoff (an unthrottled
// busy-poll on this exact ioctl has previously starved wifi/display
// board-wide within minutes on this hardware, per that file's own
// comment — do not simplify this away even though it looks paranoid, per
// this project's own established caution around this exact code path).
// Must be running BEFORE mvm_ta's push_pages() SMC can ever complete —
// called from newTZHardwareChannel itself, before SET_PAGES, on the same
// fd (matches mvm_ca_test.cpp's own ordering and its one-fd-shared-by-
// both design exactly; not called directly by any other code).
func startReverseCallRelay(fd C.int) {
	tzHwRelayOnce.Do(func() {
		go func() {
			// LockOSThread: this goroutine spends its whole life inside a
			// tight ioctl-then-yield loop -- pinning it to one OS thread
			// avoids the Go scheduler moving it mid-loop for no benefit,
			// matching the dedicated pthread ca_relay_thread used on the
			// C++ side.
			runtime.LockOSThread()
			logger.Info("[TZ_HW] reverse-call relay goroutine started (fd=%d)", int(fd))
			var consecutiveIdle uint
			const idleBackoffThreshold = 2000
			var outCmd C.int
			for {
				C.mvm_tz_hw_ioctl_run(fd, &outCmd)
				if outCmd == C.SMC_LOOP_EXIT_FINISH {
					consecutiveIdle++
					if consecutiveIdle > idleBackoffThreshold {
						time.Sleep(time.Millisecond)
					}
				} else {
					consecutiveIdle = 0
				}
				runtime.Gosched()
			}
		}()
	})
}
