use core::cmp::Ordering;

/// Trình xác thực Merkle Proof siêu nhẹ (no_std) dành cho cấu trúc Nomt Trie (Nearly Optimal Merkle Trie)
/// Metanode sử dụng Blake3 cho hash. TEE chỉ cần đúng hàm verify này để check tính toàn vẹn của kết quả.
pub struct NomtVerifier;

impl NomtVerifier {
    /// Hàm xác thực Merkle Proof từ một node lá (leaf) lên đến gốc (root).
    /// * `leaf_hash`: Băm của dữ liệu thực tế (VD: `blake3(Product_ID_List)`)
    /// * `merkle_proof`: Mảng các sibling hashes để leo lên root
    /// * `expected_root`: Trạng thái state_root tin cậy (Lấy từ RPMB Anti-Replay Guard)
    /// Trả về `true` nếu Proof hợp lệ, `false` nếu bị làm giả.
    pub fn verify_proof(
        leaf_hash: [u8; 32],
        merkle_proof: &[[u8; 32]],
        expected_root: [u8; 32],
    ) -> bool {
        let mut current_hash = leaf_hash;

        for sibling in merkle_proof {
            let mut hasher = blake3::Hasher::new();
            
            if current_hash.cmp(sibling) == Ordering::Less {
                hasher.update(&current_hash);
                hasher.update(sibling);
            } else {
                hasher.update(sibling);
                hasher.update(&current_hash);
            }
            
            current_hash = hasher.finalize().into();
        }

        current_hash == expected_root
    }

    /// Xác thực Proof thật sự của thuật toán Nomt Sparse Trie.
    /// Hàm này được dùng trong môi trường TEE (no_std) để thay thế cho logic Mock bên trên.
    pub fn verify_nomt_core_proof(
        proof_bytes: &[u8],
        key_path_bytes: [u8; 32],
        value_hash: [u8; 32],
        expected_root: [u8; 32],
    ) -> bool {
        use nomt_core::proof::PathProof;
        use nomt_core::trie::LeafData;
        use nomt_core::hasher::Blake3Hasher;
        use bitvec::prelude::*;

        // 1. TEE tự thân giải mã cấu trúc Proof (từ byte array)
        let parsed_proof: PathProof = match bincode::deserialize(proof_bytes) {
            Ok(p) => p,
            Err(_) => return false,
        };

        // 2. Thiết lập mục tiêu cần chứng minh
        let expected_leaf = LeafData {
            key_path: key_path_bytes,
            value_hash,
        };
        let key_bits = key_path_bytes.view_bits::<Msb0>();

        // 3. Sử dụng lõi Nomt-Core để băm và leo lên đỉnh Trie
        let verified = match parsed_proof.verify::<Blake3Hasher>(key_bits, expected_root) {
            Ok(v) => v,
            Err(_) => return false, // Lỗi băm sai root hoặc cấu trúc Proof không hợp lệ
        };

        // 4. So sánh lá của Proof có trùng khớp với Dữ liệu ví (Account) ta đang kiểm tra không
        match verified.confirm_value(&expected_leaf) {
            Ok(true) => true,
            _ => false, // Hacker cố tình giả mạo số dư ví!
        }
    }
}
