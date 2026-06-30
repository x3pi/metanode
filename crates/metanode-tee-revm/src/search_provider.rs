extern crate alloc;

use alloc::vec::Vec;
use revm::primitives::U256;

// Trait giao diện cho Search Engine (Mù môi trường - Environment Agnostic)
pub trait SearchProvider: Send + Sync {
    // Nhận vào db_name và query, trả về danh sách các ID (uint256).
    fn search(&self, db_name: &str, query: &str) -> Vec<U256>;
    fn insert(&self, db_name: &str, id: U256, metadata: &str);
    fn delete(&self, db_name: &str, id: U256);
}

// -------------------------------------------------------------
// MÔI TRƯỜNG 1: NATIVE HOST (Linux / Normal World)
// -------------------------------------------------------------
pub struct NativeTantivyProvider;

impl SearchProvider for NativeTantivyProvider {
    fn search(&self, _db_name: &str, _query: &str) -> Vec<U256> {
        // Trong thực tế, hàm này sẽ gọi vào Tantivy Index của `db_name`
        // Trả về ID giả lập khớp với từ khóa "Macbook" (ID = 1)
        alloc::vec![
            U256::from(1)
        ]
    }
    
    fn insert(&self, _db_name: &str, _id: U256, _metadata: &str) {
        // Ghi log giả lập
        // println!("[NATIVE] Insert DB: {} | ID: {} | Meta: {}", db_name, id, metadata);
    }
    
    fn delete(&self, _db_name: &str, _id: U256) {
        // Ghi log giả lập
    }
}

// -------------------------------------------------------------
// MÔI TRƯỜNG 2: TEE (Secure World / TrustZone - no_std)
// -------------------------------------------------------------
pub struct DelegatedTantivyProvider {
    // Kết quả do Host nạp sẵn vào TEE qua payload SMC (HostContext)
    pub preloaded_results: Vec<U256>,
}

impl SearchProvider for DelegatedTantivyProvider {
    fn search(&self, _db_name: &str, _query: &str) -> Vec<U256> {
        // Bên trong TEE KHÔNG chạy Tantivy. Nó chỉ đọc kết quả Host mớm vào.
        self.preloaded_results.clone()
    }
    
    fn insert(&self, _db_name: &str, _id: U256, _metadata: &str) {}
    fn delete(&self, _db_name: &str, _id: U256) {}
}
