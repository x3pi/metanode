// Standalone-link stubs for the Go-side (`//export`) CGO callbacks
// XapianManager's background cleaner_thread calls (xapian_manager.cpp).
//
// Added 2026-09 (Phuong an A mục 22, "check kỹ luôn phần xapian trong mvm"):
// mvm_xapian_manager_tests links only mvm_linker + xapian/tbb/crypto/ssl/
// uuid/mpfr/gmp (see CMakeLists.txt) -- no Go runtime at all -- but
// xapian_manager.cpp's cleaner_thread references GetXapianPruneRetentionBlocks
// and GetCurrentBlockNumberForXapianPrune, whose only real implementation
// lives in mvm_api.go (`//export`, linked in only as part of the full
// simple_chain Go binary). This test target could not even LINK before this
// file existed -- a pre-existing gap, confirmed present regardless of this
// pass's other test-content fixes.
//
// These stubs return the same "pruning disabled" default the real Go side
// documents as its own fail-safe value (config.ConfigApp == nil ->
// retention = 0, see mvm_api.go's own comment on
// GetXapianPruneRetentionBlocks) -- retention=0 makes xapian_manager.cpp's
// cleaner_thread skip calling GetCurrentBlockNumberForXapianPrune entirely,
// so pruneOldVersions() never runs during these tests, matching this test
// file's own scope (it does not exercise pruning).
//
// malloc() here to match xapian_manager.cpp's `free(data_p)` on the
// returned buffer (mirroring Go's own C.CBytes(), which likewise allocates
// with C's allocator for the C++ side to free).

#include "mvm_linker.hpp"
#include <cstdint>
#include <cstdlib>
#include <cstring>

extern "C" {

struct Value_return GetXapianPruneRetentionBlocks() {
    uint8_t *buf = static_cast<uint8_t *>(malloc(8));
    memset(buf, 0, 8); // retention = 0 -> pruning stays off, as documented above
    Value_return ret;
    ret.data_p = buf;
    ret.data_size = 8;
    ret.success = true;
    return ret;
}

struct Value_return GetCurrentBlockNumberForXapianPrune() {
    // Never actually reached while retention == 0 above, but must still
    // link: report failure rather than fabricate a block height, matching
    // the real implementation's own "no blockchain instance" failure path.
    Value_return ret;
    ret.data_p = nullptr;
    ret.data_size = 0;
    ret.success = false;
    return ret;
}

} // extern "C"
