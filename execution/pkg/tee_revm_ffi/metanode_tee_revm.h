#ifndef METANODE_TEE_REVM_H
#define METANODE_TEE_REVM_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

// Returns the number of bytes written to `out_buffer_ptr`.
// If the buffer is too small, returns a negative number indicating required size.
// Returns -1 for invalid pointers.
int32_t revm_execute_tx(
    const uint8_t* caller_ptr,
    const uint8_t* target_ptr,
    const uint8_t* calldata_ptr,
    size_t calldata_len,
    uint64_t gas_limit,
    uint8_t* out_buffer_ptr,
    size_t out_buffer_len
);

#ifdef __cplusplus
}
#endif

#endif // METANODE_TEE_REVM_H
