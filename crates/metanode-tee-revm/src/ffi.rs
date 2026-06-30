use core::slice;
use revm::primitives::{Address, Bytes, U256};
use serde::{Deserialize, Serialize};

// use crate::evm::MetanodeEvm;

/// A simple structure representing the State Diff after an EVM execution.
/// In the future, this will contain ReadSets, WriteSets (Account, Balance, Storage, Nonce).
#[derive(Serialize, Deserialize, Debug)]
pub struct TeeStateDiff {
    pub success: bool,
    pub caller: [u8; 20],
    pub target: [u8; 20],
    pub gas_used: u64,
    pub read_keys: Vec<[u8; 32]>,
    pub write_keys: Vec<[u8; 32]>,
}

/// A simple FFI boundary for executing a transaction via REVM.
/// Returns the number of bytes written to `out_buffer_ptr`.
/// If the buffer is too small, returns a negative number indicating required size (e.g., -size).
/// Returns -1 for invalid pointers.
#[unsafe(no_mangle)]
pub extern "C" fn revm_execute_tx(
    caller_ptr: *const u8,
    target_ptr: *const u8,
    calldata_ptr: *const u8,
    calldata_len: usize,
    _gas_limit: u64,
    out_buffer_ptr: *mut u8,
    out_buffer_len: usize,
) -> i32 {
    if caller_ptr.is_null() || target_ptr.is_null() || out_buffer_ptr.is_null() {
        return -1;
    }

    // Convert pointers to safe slices
    let caller_slice = unsafe { slice::from_raw_parts(caller_ptr, 20) };
    let target_slice = unsafe { slice::from_raw_parts(target_ptr, 20) };
    let calldata_slice = if calldata_len > 0 && !calldata_ptr.is_null() {
        unsafe { slice::from_raw_parts(calldata_ptr, calldata_len) }
    } else {
        &[]
    };

    let caller = Address::from_slice(caller_slice);
    let target = Address::from_slice(target_slice);
    let _calldata = Bytes::from(calldata_slice.to_vec());
    let _value = U256::from(0);

    // TODO: Build actual CacheDB and MetanodeEvm.
    // Call transact_optimistic() to get (ExecutionResult, revm::primitives::State).
    
    // For now, this is a stub simulating a State Diff.
    let mut caller_arr = [0u8; 20];
    caller_arr.copy_from_slice(caller_slice);
    let mut target_arr = [0u8; 20];
    target_arr.copy_from_slice(target_slice);

    // Mock Read and Write keys for Block-STM testing
    let mut mock_read_key = [0u8; 32];
    mock_read_key[31] = 1; // 0x...01
    
    let mut mock_write_key = [0u8; 32];
    mock_write_key[31] = 2; // 0x...02

    let diff = TeeStateDiff {
        success: true,
        caller: caller_arr,
        target: target_arr,
        gas_used: 21000,
        read_keys: vec![mock_read_key],
        write_keys: vec![mock_write_key],
    };

    // Serialize using bincode
    let encoded: Vec<u8> = match bincode::serialize(&diff) {
        Ok(enc) => enc,
        Err(_) => return -2, // serialization error
    };

    if encoded.len() > out_buffer_len {
        return -(encoded.len() as i32);
    }

    let out_slice = unsafe { slice::from_raw_parts_mut(out_buffer_ptr, out_buffer_len) };
    out_slice[..encoded.len()].copy_from_slice(&encoded);

    encoded.len() as i32
}
