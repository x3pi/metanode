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
            // Nomt (thường) băm 2 node anh em bằng cách nối chúng theo thứ tự byte nhỏ -> lớn, 
            // hoặc theo cơ chế index của Trie (Trái/Phải). Ở đây ta mock logic ghép cặp (Lexicographical order).
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
}
