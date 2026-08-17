package mvm

/*
#include <stdlib.h>
#include <string.h>
#include "tzproto/mvm_tz_protocol.h"
*/
import "C"
import (
	"fmt"
	"sync"
	"unsafe"
)

// tzChannel wraps a single mvm_tz_channel_t (pkg/mvm/tzproto/mvm_tz_protocol.h)
// allocated via C.malloc — GĐ2's stand-in, on x86, for the real driver-owned
// shared page GĐ3 will use on hardware. See pkg/mvm/tzproto/README.md for
// the protocol this implements and note/tee_dual_mode_execution_plan.md for
// why execution_mode=trustzone is strictly single-flight (one channel,
// guarded by tzSessionMu below — not per mvmId instance).
type tzChannel struct {
	ptr *C.mvm_tz_channel_t
}

func newTZChannel() *tzChannel {
	raw := C.malloc(C.size_t(C.sizeof_mvm_tz_channel_t))
	if raw == nil {
		panic("mvm: tzChannel: C.malloc failed")
	}
	C.memset(raw, 0, C.size_t(C.sizeof_mvm_tz_channel_t))
	ch := (*C.mvm_tz_channel_t)(raw)
	ch.protocol_version = C.MVM_TZ_PROTOCOL_VERSION
	return &tzChannel{ptr: ch}
}

func (c *tzChannel) free() {
	if c.ptr != nil {
		C.free(unsafe.Pointer(c.ptr))
		c.ptr = nil
	}
}

// writeMessage copies header+blob into the shared blob_region and sets
// cmd/direction/lengths, all under the spinlock — mirroring how a real
// CA/TA side populates the page before flipping the atomic ready flag the
// other side polls (postRequestReady/postResponseReady, called separately
// by the caller once writeMessage returns).
func (c *tzChannel) writeMessage(cmd C.mvm_tz_cmd_t, direction C.mvm_tz_direction_t, header, blob []byte) error {
	total := len(header) + len(blob)
	if total > C.MVM_TZ_BLOB_REGION_SIZE {
		return fmt.Errorf("mvm: tzChannel: message %d bytes exceeds blob region %d bytes", total, C.MVM_TZ_BLOB_REGION_SIZE)
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

// readMessage copies the currently-posted header+blob back out into fresh
// Go byte slices — a real deep copy across the "boundary", same as the
// existing ta_boundary_harness_test.go's JSON round-trip forces, just
// through the real wire encoding instead.
func (c *tzChannel) readMessage() (cmd C.mvm_tz_cmd_t, direction C.mvm_tz_direction_t, header, blob []byte) {
	C.mvm_tz_spinlock_lock(&c.ptr.lock)
	defer C.mvm_tz_spinlock_unlock(&c.ptr.lock)

	cmd = c.ptr.cmd
	direction = c.ptr.direction
	hlen := int(c.ptr.header_len)
	blen := int(c.ptr.blob_len)
	if hlen > 0 {
		header = C.GoBytes(unsafe.Pointer(&c.ptr.blob_region[0]), C.int(hlen))
	}
	if blen > 0 {
		blob = C.GoBytes(unsafe.Pointer(&c.ptr.blob_region[hlen]), C.int(blen))
	}
	return
}

// postRequestReady/postResponseReady and consumeRequestReady/
// consumeResponseReady exercise the real atomic-flag handshake
// (mvm_tz_flag_*, mvm_tz_protocol.h) even though, in this same-process
// loopback, consume always succeeds immediately — GĐ2's purpose is to
// rehearse the real primitives (and catch layout/alignment bugs), not to
// model real cross-world latency or actual concurrent contention. That is
// GĐ3's job, on hardware.
func (c *tzChannel) postRequestReady()  { C.mvm_tz_flag_set(&c.ptr.request_ready, 1) }
func (c *tzChannel) postResponseReady() { C.mvm_tz_flag_set(&c.ptr.response_ready, 1) }

func (c *tzChannel) consumeRequestReady() bool {
	return C.mvm_tz_flag_cas(&c.ptr.request_ready, 1, 0) != 0
}
func (c *tzChannel) consumeResponseReady() bool {
	return C.mvm_tz_flag_cas(&c.ptr.response_ready, 1, 0) != 0
}

// Package-wide singleton channel + session mutex: execution_mode=trustzone
// is strictly single-flight (note/tee_dual_mode_execution_plan.md's "Hệ quả
// thiết kế then chốt" #5) — there is exactly one physical channel to the one
// TA in the real design, so GĐ2 models that as one process-wide channel
// guarded by one mutex, not one per mvmId instance.
var (
	tzChannelOnce sync.Once
	tzChannelInst *tzChannel
	tzSessionMu   sync.Mutex
)

func getTZChannel() *tzChannel {
	tzChannelOnce.Do(func() {
		tzChannelInst = newTZChannel()
	})
	return tzChannelInst
}
