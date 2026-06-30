#![cfg_attr(not(feature = "std"), no_std)]
extern crate alloc;

pub mod evm;
pub mod ffi;
pub mod rpmb;
pub mod search_provider;
pub mod nomt_verifier;
#[cfg(feature = "std")]
pub mod poc;
#[cfg(feature = "std")]
pub use poc::*;

use revm::primitives::U256;

use search_provider::SearchProvider;
use alloc::sync::Arc;

pub struct SearchInspector {
    pub provider: Arc<dyn SearchProvider>,
}

impl<DB: revm::Database> revm::Inspector<DB> for SearchInspector {
    fn call(
        &mut self,
        _context: &mut revm::EvmContext<DB>,
        inputs: &mut revm::interpreter::CallInputs,
    ) -> Option<revm::interpreter::CallOutcome> {
        let data = inputs.input.as_ref();
        
        // Cần tối thiểu 128 bytes để chứa (Action, dbName_offset, arg3, dbName_length)
        if data.len() >= 128 {
            // Đọc offset của dbName (Luôn nằm ở bytes 32..64 vì Action chiếm 0..32)
            let db_name_offset_u256 = U256::from_be_bytes::<32>(data[32..64].try_into().unwrap());
            if db_name_offset_u256 < U256::from(100000) { // Limit sanity check
                let offset = db_name_offset_u256.as_limbs()[0] as usize;
                if offset + 32 <= data.len() {
                    let db_name_len = U256::from_be_bytes::<32>(data[offset..offset+32].try_into().unwrap()).as_limbs()[0] as usize;
                    if offset + 32 + db_name_len <= data.len() {
                        let db_name_bytes = &data[offset+32 .. offset+32+db_name_len];
                        
                        // 1. Tính toán HASH: keccak256(caller + db_name)
                        let mut payload = alloc::vec::Vec::new();
                        payload.extend_from_slice(inputs.caller.as_slice());
                        payload.extend_from_slice(db_name_bytes);
                        let hash = revm::primitives::keccak256(&payload);
                        
                        let mut addr_bytes = [0u8; 20];
                        addr_bytes.copy_from_slice(&hash[12..32]);
                        let expected_target = revm::primitives::Address::from(addr_bytes);
                        
                        // 2. Kiểm tra nếu địa chỉ đích hoàn toàn khớp với địa chỉ được băm
                        if expected_target == inputs.target_address {
                            let action = U256::from_be_bytes::<32>(data[0..32].try_into().unwrap()).as_limbs()[0] as u8;
                            
                            let db_name = alloc::string::String::from_utf8_lossy(db_name_bytes).into_owned();
                            
                            // ACTION 0: SEARCH
                            if action == 0 {
                                let q_offset_u256 = U256::from_be_bytes::<32>(data[64..96].try_into().unwrap());
                                let q_offset = q_offset_u256.as_limbs()[0] as usize;
                                let mut query = alloc::string::String::new();
                                if q_offset + 32 <= data.len() {
                                    let q_len = U256::from_be_bytes::<32>(data[q_offset..q_offset+32].try_into().unwrap()).as_limbs()[0] as usize;
                                    if q_offset + 32 + q_len <= data.len() {
                                        query = alloc::string::String::from_utf8_lossy(&data[q_offset+32 .. q_offset+32+q_len]).into_owned();
                                    }
                                }
                                
                                let u256_results = self.provider.search(&db_name, &query);
                                
                                // Đóng gói mảng U256[] chuẩn ABI: [Offset = 0x20] [Length] [Items...]
                                let mut out = alloc::vec::Vec::new();
                                out.extend_from_slice(&U256::from(32).to_be_bytes::<32>());
                                out.extend_from_slice(&U256::from(u256_results.len()).to_be_bytes::<32>());
                                for val in u256_results {
                                    out.extend_from_slice(&val.to_be_bytes::<32>());
                                }
                                
                                return Some(revm::interpreter::CallOutcome {
                                    result: revm::interpreter::InterpreterResult {
                                        result: revm::interpreter::InstructionResult::Return,
                                        output: revm::primitives::Bytes::from(out),
                                        gas: revm::interpreter::Gas::new(inputs.gas_limit),
                                    },
                                    memory_offset: inputs.return_memory_offset.clone(),
                                });
                            } 
                            // ACTION 1: INSERT
                            else if action == 1 {
                                let id = U256::from_be_bytes::<32>(data[64..96].try_into().unwrap());
                                let m_offset_u256 = U256::from_be_bytes::<32>(data[96..128].try_into().unwrap());
                                let m_offset = m_offset_u256.as_limbs()[0] as usize;
                                let mut metadata = alloc::string::String::new();
                                if m_offset + 32 <= data.len() {
                                    let m_len = U256::from_be_bytes::<32>(data[m_offset..m_offset+32].try_into().unwrap()).as_limbs()[0] as usize;
                                    if m_offset + 32 + m_len <= data.len() {
                                        metadata = alloc::string::String::from_utf8_lossy(&data[m_offset+32 .. m_offset+32+m_len]).into_owned();
                                    }
                                }
                                
                                self.provider.insert(&db_name, id, &metadata);
                                
                                return Some(revm::interpreter::CallOutcome {
                                    result: revm::interpreter::InterpreterResult {
                                        result: revm::interpreter::InstructionResult::Return,
                                        output: revm::primitives::Bytes::new(),
                                        gas: revm::interpreter::Gas::new(10000),
                                    },
                                    memory_offset: inputs.return_memory_offset.clone(),
                                });
                            }
                            // ACTION 2: DELETE
                            else if action == 2 {
                                let id = U256::from_be_bytes::<32>(data[64..96].try_into().unwrap());
                                self.provider.delete(&db_name, id);
                                
                                return Some(revm::interpreter::CallOutcome {
                                    result: revm::interpreter::InterpreterResult {
                                        result: revm::interpreter::InstructionResult::Return,
                                        output: revm::primitives::Bytes::new(),
                                        gas: revm::interpreter::Gas::new(10000),
                                    },
                                    memory_offset: inputs.return_memory_offset.clone(),
                                });
                            }
                        }
                    }
                }
            }
        }
        None
    }
}
