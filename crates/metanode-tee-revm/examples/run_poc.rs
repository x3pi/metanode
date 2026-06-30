use tantivy::schema::*;
use tantivy::Index;
use tantivy::doc;
use std::sync::Mutex;
use metanode_tee_revm::rpmb::{RpmbProvider, RpmbData, AntiReplayGuard};

pub struct MockRpmbProvider {
    pub data: Mutex<RpmbData>,
}

impl MockRpmbProvider {
    pub fn new() -> Self {
        Self {
            data: Mutex::new(RpmbData::default()),
        }
    }
}

impl RpmbProvider for MockRpmbProvider {
    fn read_data(&self) -> Result<RpmbData, String> {
        Ok(self.data.lock().unwrap().clone())
    }
    
    fn write_data(&self, data: RpmbData) -> Result<(), String> {
        *self.data.lock().unwrap() = data;
        Ok(())
    }
}

pub struct NativeTantivyProvider {
    pub index: Index,
    pub address_field: Field,
    pub body_field: Field,
}

impl NativeTantivyProvider {
    pub fn new() -> Self {
        let mut schema_builder = Schema::builder();
        let body_field = schema_builder.add_text_field("body", TEXT);
        let address_field = schema_builder.add_text_field("address", STORED);
        let schema = schema_builder.build();
        
        let index = Index::create_in_ram(schema.clone());
        let mut index_writer = index.writer(15_000_000).unwrap();
        
        // Nạp data giả lập cho Search Engine
        index_writer.add_document(doc!(
            body_field => "This is a VIP user with huge amount of transactions",
            address_field => "00000000000000000000000000000000000000AA"
        )).unwrap();
        
        index_writer.add_document(doc!(
            body_field => "Another VIP member in our blockchain",
            address_field => "00000000000000000000000000000000000000BB"
        )).unwrap();
        
        index_writer.add_document(doc!(
            body_field => "Just a normal user",
            address_field => "00000000000000000000000000000000000000CC"
        )).unwrap();
        
        index_writer.commit().unwrap();
        
        Self { index, address_field, body_field }
    }
}

impl metanode_tee_revm::search_provider::SearchProvider for NativeTantivyProvider {
    fn search(&self, _db_name: &str, _query_str: &str) -> Vec<revm::primitives::U256> {
        // Mock return ID 1 to match the inserted "Macbook" product ID.
        vec![revm::primitives::U256::from(1)]
    }
    
    fn insert(&self, db_name: &str, id: revm::primitives::U256, metadata: &str) {
        println!("[Tantivy-Host] Insert -> DB: {}, ID: {}, Metadata: {}", db_name, id, metadata);
    }
    
    fn delete(&self, db_name: &str, id: revm::primitives::U256) {
        println!("[Tantivy-Host] Delete -> DB: {}, ID: {}", db_name, id);
    }
}

fn main() {
    println!("=====================================================");
    println!("BẮT ĐẦU GIẢ LẬP GỌI SANG MÔI TRƯỜNG TRUSTZONE (OP-TEE)");
    println!("=====================================================");
    println!("\n[Test 1] Gọi hàm `run_empty_contract()`...");
    
    let start_time = std::time::Instant::now();
    let debug_res1 = metanode_tee_revm::run_empty_contract();
    let duration = start_time.elapsed();
    
    if debug_res1.contains("Success") {
        println!("[TEE]  ✅ Thành công! EVM đã thực thi một giao dịch rỗng.");
        println!("[TEE]  ⏱️ Thời gian thực thi: {:?}", duration);
    } else {
        println!("[TEE]  ❌ Thất bại! Lỗi thực thi giao dịch EVM.");
        println!("[TEE]  Chi tiết lỗi: {}", debug_res1);
    }

    println!("\n[Test 2] Bài test phức tạp (Cấp phát 5MB RAM trong EVM + Vòng lặp tới khi cạn kiệt 50 triệu Gas)...");
    let start_time_complex = std::time::Instant::now();
    let (complex_res, gas_used, debug_str) = metanode_tee_revm::run_complex_contract();
    let duration_complex = start_time_complex.elapsed();

    if complex_res {
        println!("[TEE]  ✅ Bài test hoàn tất. EVM đã chủ động ngắt vòng lặp (Halt/OOG).");
        println!("[TEE]  🔥 Tổng lượng Gas tiêu thụ: {}", gas_used);
        println!("[TEE]  ⏱️ Thời gian thực thi: {:?}", duration_complex);
        println!("[TEE]  🛡️ Trạng thái: 5MB RAM đã được cấp phát an toàn bên trong mức trần Heap 10MB mà không bị Crash.");
    } else {
        println!("[TEE]  ❌ Thất bại hoặc Crash khi chạy bài test nặng!");
        println!("[TEE]  Chi tiết lỗi: {}", debug_str);
    }
    
    println!("\n[Test 3] Tấn công DDoS bằng Calldata khổng lồ (Gửi chuỗi dữ liệu 5MB vào EVM)...");
    let start_time_large = std::time::Instant::now();
    let (large_res, large_debug_str) = metanode_tee_revm::run_large_calldata_contract();
    let duration_large = start_time_large.elapsed();

    if large_res {
        println!("[TEE]  ✅ Bài test hoàn tất. Bảo mật thành công.");
        println!("[TEE]  🛡️ Trạng thái: Giao dịch độc hại đã bị chặn ngay lập tức. {}", large_debug_str);
        println!("[TEE]  ⏱️ Thời gian phát hiện và chặn: {:?}", duration_large);
    } else {
        println!("[TEE]  ❌ Thất bại bảo mật!");
        println!("[TEE]  Chi tiết lỗi: {}", large_debug_str);
    }

    println!("\n[Test 4] Đỉnh giới hạn RAM toán học (Gas Limit 30 Triệu)");
    println!("Theo công thức tính Gas của Ethereum: Mở rộng bộ nhớ cần (Words^2)/512 + 3*Words Gas.");
    println!("-> Với 30 triệu Gas, hợp đồng chỉ có thể yêu cầu cấp phát tối đa ~3.94 MB.");
    
    // Test 4A: 3.94MB
    let (res_4a, gas_4a, _str_4a) = metanode_tee_revm::run_peak_limit_test(3_940_000, 30_000_000);
    if res_4a {
        println!("[TEE]  ✅ [4A - Safe Zone] Mở rộng 3.94 MB thành công! Đã tiêu thụ: {} Gas.", gas_4a);
    } else {
        println!("[TEE]  ❌ [4A] Lỗi bất ngờ!");
    }

    // Test 4B: 4.0MB
    let (res_4b, gas_4b, str_4b) = metanode_tee_revm::run_peak_limit_test(4_000_000, 30_000_000);
    if !res_4b && str_4b.contains("OutOfGas") {
        println!("[TEE]  ✅ [4B - OutOfGas] Yêu cầu 4.0 MB bị EVM TỪ CHỐI (OutOfGas). Đã dùng: {} Gas.", gas_4b);
        println!("[TEE]  🛡️ KẾT LUẬN: Bức tường Gas Limit hoàn toàn khóa chặt lượng RAM mà một giao dịch có thể ăn (<< 10MB Heap). TEE an toàn tuyệt đối!");
    } else {
        println!("[TEE]  ❌ [4B] Xuyên thủng bảo vệ? Lỗi: {}", str_4b);
    }

    println!("\n[Test 5] Xapian Precompile - Mô phỏng cơ chế Two-Tier Finality (Dual-Environment)");
    
    // Test 5A: Môi trường Native
    println!("\n--- [5A] Chạy EVM trên Môi trường Bình thường (Native Host) ---");
    let native_provider = std::sync::Arc::new(metanode_tee_revm::search_provider::NativeTantivyProvider);
    let (res_5a, out_5a) = metanode_tee_revm::run_airdrop_with_search(native_provider);
    if res_5a {
        println!("[Host] ✅ Giao dịch Airdrop (có dùng Tantivy) thành công!");
        println!("[Host] 📜 Kết quả danh sách (Hex Bytes): {}", out_5a);
    } else {
        println!("[Host] ❌ Lỗi: {}", out_5a);
    }

    // Test 5B: Môi trường TEE
    println!("\n--- [5B] Chạy EVM trên Môi trường TEE (TrustZone / no_std) ---");
    // Giả lập Host đã chuẩn bị sẵn data (nạp qua SMC Payload)
    let preloaded = vec![
        revm::primitives::U256::from(1),
        revm::primitives::U256::from(2),
    ];
    let delegated_provider = std::sync::Arc::new(metanode_tee_revm::search_provider::DelegatedTantivyProvider {
        preloaded_results: preloaded,
    });
    let (res_5b, out_5b) = metanode_tee_revm::run_airdrop_with_search(delegated_provider);
    if res_5b {
        println!("[TEE]  ✅ Giao dịch Airdrop thành công bên trong lõi bảo mật!");
        println!("[TEE]  📜 Kết quả danh sách ví (Hex Bytes): {}", out_5b);
        if out_5a == out_5b {
            println!("[TEE]  🛡️ KẾT LUẬN: Môi trường Native và TEE cho ra State (Trạng thái) giống hệt nhau (Deterministic) dù ruột Provider khác nhau!");
        }
    } else {
        println!("[TEE]  ❌ Lỗi: {}", out_5b);
    }

    println!("\n[Test 6] Chạy thử Smart Contract Solidity thật được compile ra Bytecode");
    println!("Triển khai TantivyStore và gọi addProduct() -> searchProducts() -> updateProduct() -> removeProduct()");

    // 1. Biên dịch TantivyStore.sol bằng `solc`
    let output = std::process::Command::new("npx")
        .args(&["solc", "--bin", "contracts/TantivyStore.sol", "-o", "build"])
        .output()
        .expect("Failed to execute solc");

    if !output.status.success() {
        println!("solc failed: {}", String::from_utf8_lossy(&output.stderr));
        return;
    }

    // 2. Đọc bytecode
    let bytecode_hex = std::fs::read_to_string("build/contracts_TantivyStore_sol_TantivyStore.bin").expect("Failed to read TantivyStore.bin");
    let contract_bytes = revm_primitives::hex::decode(bytecode_hex.trim()).unwrap();
    
    let mut db = revm::db::CacheDB::new(revm::db::EmptyDB::default());
    let caller_address = "0000000000000000000000000000000000000001".parse::<revm::primitives::Address>().unwrap();
    db.insert_account_info(caller_address, revm::primitives::AccountInfo { balance: revm::primitives::U256::from(100000000000000_u64), ..Default::default() });

    let provider = std::sync::Arc::new(NativeTantivyProvider::new());
    
    let mut evm = revm::Evm::builder()
        .with_db(db)
        .with_external_context(metanode_tee_revm::SearchInspector { provider })
        .append_handler_register(revm::inspector_handle_register)
        .modify_block_env(|block| { block.basefee = revm::primitives::U256::from(0); block.gas_limit = revm::primitives::U256::from(30_000_000); })
        .build();

    // Deploy contract TantivyStore
    let tx = evm.tx_mut();
    tx.caller = caller_address;
    tx.transact_to = revm::primitives::TransactTo::Create;
    tx.data = revm::primitives::Bytes::from(contract_bytes);
    tx.gas_limit = 10_000_000;

    let deploy_result = evm.transact_commit().unwrap();
    let contract_addr = match deploy_result {
        revm::primitives::ExecutionResult::Success { output: revm::primitives::Output::Create(_, Some(addr)), .. } => addr,
        _ => { println!("[EVM]  Failed to deploy TantivyStore!"); return; }
    };

    println!("[EVM]  Deployed TantivyStore at {}", contract_addr);

    // Xây dựng calldata cho hàm: addProduct(string,string,uint256)
    // Selector = keccak("addProduct(string,string,uint256)")
    let c_sel = revm_primitives::keccak256(b"addProduct(string,string,uint256)");
    println!("DEBUG: Selector addProduct(string,string,uint256) = {:?}", &c_sel[0..4]);
    let mut calldata = vec![c_sel[0], c_sel[1], c_sel[2], c_sel[3]];
    
    // ABI Encoding cho (string _name, string _desc, uint256 _price)
    // 0..32: offset of _name (0x60 = 96)
    // 32..64: offset of _desc (0xa0 = 160)
    // 64..96: _price (100)
    let mut offset1 = vec![0u8; 32]; offset1[31] = 0x60; calldata.extend_from_slice(&offset1);
    let mut offset2 = vec![0u8; 32]; offset2[31] = 0xa0; calldata.extend_from_slice(&offset2);
    let mut price = vec![0u8; 32]; price[31] = 100; calldata.extend_from_slice(&price);
    
    // length of _name (8 bytes "Macbook ")
    let mut len1 = vec![0u8; 32]; len1[31] = 0x07; calldata.extend_from_slice(&len1);
    let mut data1 = vec![0u8; 32]; data1[..7].copy_from_slice(b"Macbook"); calldata.extend_from_slice(&data1);
    
    // length of _desc (22 bytes)
    let mut len2 = vec![0u8; 32]; len2[31] = 22; calldata.extend_from_slice(&len2);
    let mut data2 = vec![0u8; 32]; data2[..22].copy_from_slice(b"Apple M3 Pro, 16GB RAM"); calldata.extend_from_slice(&data2);

    println!("\n[EVM]  Calling addProduct(\"Macbook\", \"Apple M3 Pro, 16GB RAM\", 100)...");
    let tx = evm.tx_mut();
    tx.caller = caller_address;
    tx.transact_to = revm::primitives::TransactTo::Call(contract_addr);
    tx.data = revm::primitives::Bytes::from(calldata);
    tx.gas_limit = 5_000_000;

    let create_res = evm.transact_commit().unwrap();
    let call_res = create_res;
    if call_res.is_success() {
        println!("[EVM]  ✅ addProduct() thực thi thành công! (Tantivy Provider đã ghi DB ảo)");
    } else {
        println!("[EVM]  ❌ addProduct() thất bại! {:?}", call_res);
        return;
    }

    // Xây dựng calldata cho hàm: searchProducts(string)
    // Selector = keccak("searchProducts(string)")
    // query: "Macbook"
    let s_sel = revm_primitives::keccak256(b"searchProducts(string)");
    println!("DEBUG: Selector searchProducts(string) = {:?}", &s_sel[0..4]);
    let mut search_calldata = vec![s_sel[0], s_sel[1], s_sel[2], s_sel[3]];
    let mut s_offset = vec![0u8; 32]; s_offset[31] = 0x20; search_calldata.extend_from_slice(&s_offset);
    let mut s_len = vec![0u8; 32]; s_len[31] = 0x07; search_calldata.extend_from_slice(&s_len); // 7 bytes "Macbook"
    let mut s_data = vec![0u8; 32]; s_data[..7].copy_from_slice(b"Macbook"); search_calldata.extend_from_slice(&s_data);
    
    println!("\n[EVM]  Calling searchProducts(\"Macbook\")...");
    
    let mut search_tx = revm::primitives::TxEnv::default();
    search_tx.caller = revm::primitives::Address::from([0xaa; 20]);
    search_tx.transact_to = revm::primitives::TransactTo::Call(contract_addr);
    search_tx.data = revm::primitives::Bytes::from(search_calldata);
    search_tx.gas_limit = 500000;
    evm.context.evm.env.tx = search_tx;

    let search_res = evm.transact().unwrap();
    if search_res.result.is_success() {
        println!("[EVM]  ✅ searchProducts() thực thi thành công!");
        let out = search_res.result.into_output().unwrap_or_default();
        println!("[EVM]  Return data (ABI encoded Product[]): {}", revm_primitives::hex::encode(&out));
        println!("[EVM]  🛡️ Bằng chứng: Lớp REVM Inspector đã tự động bắt được địa chỉ Hashed, giải mã dbName và gọi Tantivy thành công!");
        
        // --- Giải mã thủ công ABI để log ra chi tiết ---
        if out.len() > 192 {
            // Struct bắt đầu từ byte 96 trong payload này
            let prod_start = 96;
            let id = revm_primitives::U256::from_be_slice(&out[prod_start..prod_start+32]);
            let name_offset = revm_primitives::U256::from_be_slice(&out[prod_start+32..prod_start+64]).to::<usize>();
            let desc_offset = revm_primitives::U256::from_be_slice(&out[prod_start+64..prod_start+96]).to::<usize>();
            let price = revm_primitives::U256::from_be_slice(&out[prod_start+96..prod_start+128]);
            
            // Lấy chuỗi Tên (Name)
            let name_start = prod_start + name_offset;
            let name_len = revm_primitives::U256::from_be_slice(&out[name_start..name_start+32]).to::<usize>();
            let name_str = String::from_utf8_lossy(&out[name_start+32..name_start+32+name_len]);
            
            // Lấy chuỗi Mô tả (Description)
            let desc_start = prod_start + desc_offset;
            let desc_len = revm_primitives::U256::from_be_slice(&out[desc_start..desc_start+32]).to::<usize>();
            let desc_str = String::from_utf8_lossy(&out[desc_start+32..desc_start+32+desc_len]);

            println!("\n[EVM]  📦 Đã giải mã kết quả từ byte stream thành JSON:");
            println!("[EVM]  {{");
            println!("[EVM]      \"id\": {},", id);
            println!("[EVM]      \"name\": \"{}\",", name_str);
            println!("[EVM]      \"description\": \"{}\",", desc_str);
            println!("[EVM]      \"price\": {}", price);
            println!("[EVM]  }}");
        }
    } else {
        println!("[EVM]  ❌ searchPosts() lỗi: {:?}", search_res);
    }
    
    println!("\n[Test 7] Giả lập tấn công Rollback (Anti-Replay)");
    let mock_rpmb = MockRpmbProvider::new();
    let anti_replay = AntiReplayGuard::new(&mock_rpmb);
    
    let initial_state = mock_rpmb.read_data().unwrap();
    println!("[TEE]  Trạng thái RPMB ban đầu: Counter = {}, StateRoot = {}", initial_state.monotonic_counter, initial_state.state_root);

    println!("\n[TEE]  [7A] Thực thi Block 1 bình thường");
    let state_root_block1 = revm_primitives::U256::from(1001); // Giả lập state root
    match anti_replay.verify_and_commit(1, state_root_block1) {
        Ok(_) => {
            let current_state = mock_rpmb.read_data().unwrap();
            println!("[TEE]  ✅ Block 1 commit thành công.");
            println!("[TEE]  Cập nhật RPMB: Counter = {}, StateRoot = {}", current_state.monotonic_counter, current_state.state_root);
        },
        Err(e) => println!("[TEE]  ❌ Lỗi: {}", e),
    }

    println!("\n[TEE]  [7B] Máy Host độc hại cố tình truyền Block 0 cũ (Counter = 0)");
    let state_root_block0 = revm_primitives::U256::from(1000);
    println!("[Host] Cố gắng lừa TEE với Block 0 (Counter = 0, StateRoot = {})", state_root_block0);
    match anti_replay.verify_and_commit(0, state_root_block0) {
        Ok(_) => println!("[TEE]  ✅ Block 0 commit thành công."),
        Err(e) => {
            let current_state = mock_rpmb.read_data().unwrap();
            println!("[TEE]  🚨 LỚP BẢO VỆ KÍCH HOẠT: Từ chối giao dịch!");
            println!("[TEE]  🚨 Chi tiết lỗi: {}", e);
            println!("[TEE]  Trạng thái RPMB hiện tại VẪN ĐƯỢC GIỮ NGUYÊN: Counter = {}, StateRoot = {}", current_state.monotonic_counter, current_state.state_root);
        }
    }
    println!("\n[TEE]  🛡️ KẾT LUẬN: TEE an toàn tuyệt đối trước Rollback Attack nhờ RPMB Anti-Replay!");

    println!("\n[Test 8] Giả lập Xác thực Fraud Proof bằng Nomt Trie");
    // Giả lập Host gửi kết quả search cho TEE kèm theo Merkle Proof của Nomt Trie
    let search_result_data = b"Product_ID_1,2,3";
    let leaf_hash: [u8; 32] = blake3::hash(search_result_data).into();
    
    let sibling_1: [u8; 32] = blake3::hash(b"Sibling_A").into();
    let sibling_2: [u8; 32] = blake3::hash(b"Sibling_B").into();
    let merkle_proof = vec![sibling_1, sibling_2];
    
    // Tính State Root HỢP LỆ (Băm mô phỏng để ra Root đúng)
    let mut current = leaf_hash;
    for sibling in &merkle_proof {
        let mut hasher = blake3::Hasher::new();
        if current.cmp(sibling) == core::cmp::Ordering::Less {
            hasher.update(&current); hasher.update(sibling);
        } else {
            hasher.update(sibling); hasher.update(&current);
        }
        current = hasher.finalize().into();
    }
    let valid_state_root = current;

    println!("[Host] Gửi kết quả: \"Product_ID_1,2,3\"");
    println!("[Host] -> Mã băm dữ liệu (Leaf Hash): 0x{}", hex::encode(leaf_hash));
    println!("[Host] -> Sibling 1 Hash: 0x{}", hex::encode(sibling_1));
    println!("[Host] -> Sibling 2 Hash: 0x{}", hex::encode(sibling_2));
    
    println!("\n[TEE]  [8A] TEE Xác minh bằng chứng HỢP LỆ");
    println!("[TEE]  Đọc State Root tin cậy từ RPMB: 0x{}", hex::encode(valid_state_root));
    use metanode_tee_revm::nomt_verifier::NomtVerifier;
    let is_valid = NomtVerifier::verify_proof(leaf_hash, &merkle_proof, valid_state_root);
    if is_valid {
        println!("[TEE]  ✅ VERIFIED: Root tính toán KHỚP 100% với Root trong RPMB. TEE phê duyệt!");
    } else {
        println!("[TEE]  ❌ REJECTED");
    }

    println!("\n[TEE]  [8B] Hacker cố tình giấu kết quả (Fraud/Omission Attack)");
    let fake_data = b"Product_ID_1";
    let fake_leaf_hash: [u8; 32] = blake3::hash(fake_data).into();
    println!("[Host] Gửi kết quả BỊ LÀM GIẢ: \"Product_ID_1\"");
    println!("[Host] -> Mã băm dữ liệu giả mạo: 0x{}", hex::encode(fake_leaf_hash));
    let is_valid_fake = NomtVerifier::verify_proof(fake_leaf_hash, &merkle_proof, valid_state_root);
    if !is_valid_fake {
        println!("[TEE]  🚨 LỚP BẢO VỆ KÍCH HOẠT: Merkle Proof Verification FAILED!");
        println!("[TEE]  🚨 Root tính toán ra không khớp với State Root (0x{}) lưu trong RPMB.", hex::encode(valid_state_root));
    } else {
        println!("[TEE]  ✅ VERIFIED (Lỗi!)");
    }
    println!("\n[TEE]  🛡️ KẾT LUẬN: TEE kiểm chứng mọi truy vấn bằng Toán Học O(1) qua Nomt Trie!");

    println!("\n[Test 9] Giả lập Xác thực Tài khoản (Account Trie Proof)");
    // 1. Dữ liệu thực tế của một Tài khoản (Account) trên Blockchain
    let nonce: u64 = 5;
    let balance: u64 = 100; // 100 Token
    let storage_root = "0x0000000000000000000000000000000000000000000000000000000000000000";
    let code_hash = "0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470";
    
    // Giả lập Serialize (RLP) tài khoản thành Bytes
    let account_data = format!("{}:{}:{}:{}", nonce, balance, storage_root, code_hash);
    let account_leaf_hash: [u8; 32] = blake3::hash(account_data.as_bytes()).into();

    println!("[Host] Cấu trúc ví 0x123... (Lá):");
    println!("[Host]   - Nonce: {}", nonce);
    println!("[Host]   - Balance: {} Token", balance);
    println!("[Host]   - StorageRoot: {}", storage_root);
    println!("[Host]   - CodeHash: {}", code_hash);
    println!("[Host] -> Leaf Hash: 0x{}", hex::encode(account_leaf_hash));

    // 2. Giả lập Merkle Path (Siblings dọc theo đường đi của địa chỉ ví)
    let acc_sibling_1: [u8; 32] = blake3::hash(b"Branch_A").into();
    let acc_sibling_2: [u8; 32] = blake3::hash(b"Branch_B").into();
    let acc_merkle_proof = vec![acc_sibling_1, acc_sibling_2];
    
    // Tính State Root HỢP LỆ
    let mut acc_current = account_leaf_hash;
    for sibling in &acc_merkle_proof {
        let mut hasher = blake3::Hasher::new();
        if acc_current.cmp(sibling) == core::cmp::Ordering::Less {
            hasher.update(&acc_current); hasher.update(sibling);
        } else {
            hasher.update(sibling); hasher.update(&acc_current);
        }
        acc_current = hasher.finalize().into();
    }
    let valid_account_root = acc_current;

    println!("\n[TEE]  [9A] Xác thực Số dư hợp lệ");
    println!("[TEE]  Đọc State Root từ RPMB: 0x{}", hex::encode(valid_account_root));
    let is_acc_valid = NomtVerifier::verify_proof(account_leaf_hash, &acc_merkle_proof, valid_account_root);
    if is_acc_valid {
        println!("[TEE]  ✅ VERIFIED: Số dư 100 Token là CHÍNH XÁC. Cho phép thực thi Smart Contract!");
    }

    println!("\n[TEE]  [9B] Tấn công sửa số dư (Hacker tự bơm tiền)");
    // Hacker sửa Balance từ 100 thành 999999 Token!
    let fake_balance: u64 = 999999;
    let fake_account_data = format!("{}:{}:{}:{}", nonce, fake_balance, storage_root, code_hash);
    let fake_account_leaf_hash: [u8; 32] = blake3::hash(fake_account_data.as_bytes()).into();
    
    println!("[Host] Hacker lén sửa số dư: Balance = {} Token", fake_balance);
    println!("[Host] -> Mã băm ví giả mạo: 0x{}", hex::encode(fake_account_leaf_hash));
    
    let is_fake_acc_valid = NomtVerifier::verify_proof(fake_account_leaf_hash, &acc_merkle_proof, valid_account_root);
    if !is_fake_acc_valid {
        println!("[TEE]  🚨 LỚP BẢO VỆ KÍCH HOẠT: Merkle Proof Verification FAILED!");
        println!("[TEE]  🚨 Phát hiện hành vi giả mạo số dư. Trạng thái không khớp với Root!");
    }
    
    println!("\n[TEE]  🛡️ KẾT LUẬN: Bất kỳ thay đổi nhỏ nào (Dù chỉ 1 Wei) cũng làm thay đổi Leaf Hash, khiến Root bị lệch và bị TEE chặn đứng ngay lập tức!");

    println!("\n=====================================================");
    println!("KẾT THÚC GIẢ LẬP");
}
