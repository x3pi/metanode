#![cfg_attr(not(feature = "std"), no_std)]
extern crate alloc;
use revm::{primitives::{address, AccountInfo, ExecutionResult, TransactTo, U256, Bytes, Bytecode}, db::{CacheDB, EmptyDB}, Evm};
use alloc::sync::Arc;
use crate::search_provider::SearchProvider;
use crate::SearchInspector;

pub fn run_empty_contract() -> alloc::string::String {
    let mut db = CacheDB::new(EmptyDB::default());
    db.insert_account_info(
        address!("0000000000000000000000000000000000000001"),
        AccountInfo {
            balance: U256::from(100000000000000_u64),
            ..Default::default()
        },
    );

    let mut evm = Evm::builder()
        .with_db(db)
        .modify_block_env(|block| {
            block.basefee = U256::from(0);
        })
        .build();

    let tx = evm.tx_mut();
    tx.caller = address!("0000000000000000000000000000000000000001");
    tx.transact_to = TransactTo::Call(address!("0000000000000000000000000000000000000099"));
    tx.value = U256::from(100);
    tx.gas_limit = 21000;
    tx.gas_price = U256::from(0);

    let result = evm.transact();
    alloc::format!("{:?}", result)
}

// Removed duplicate imports
// Bài test phức tạp: Cấp phát 5MB bộ nhớ trong EVM và dùng vòng lặp tiêu hao CPU
pub fn run_complex_contract() -> (bool, u64, alloc::string::String) {
    let mut db = CacheDB::new(EmptyDB::default());
    
    // Bytecode: 
    // MSTORE(0x4C4B40, 1) -> Mở rộng bộ nhớ EVM lên 5MB (tốn ~47 triệu Gas)
    // Sau đó chạy vòng lặp vô hạn cho đến khi hết Gas (OOG).
    // Opcodes: 
    // PUSH1 0x01 (6001)
    // PUSH3 0x4C4B40 (624c4b40)
    // MSTORE (52)
    // JUMPDEST (5B)
    // PUSH1 0x05 (6005)
    // JUMP (56)
    let code: [u8; 11] = [
        0x60, 0x01, 0x62, 0x4c, 0x4b, 0x40, 0x52, // MSTORE(5000000, 1)
        0x5b, 0x60, 0x07, 0x56 // Infinite Loop jumping to JUMPDEST at 0x07
    ];
    let contract_address = address!("0000000000000000000000000000000000000099");
    
    db.insert_account_info(
        contract_address,
        AccountInfo {
            balance: U256::from(0),
            nonce: 1,
            code_hash: revm::primitives::KECCAK_EMPTY,
            code: Some(Bytecode::new_raw(Bytes::from(code.to_vec()))),
        },
    );
    
    db.insert_account_info(
        address!("0000000000000000000000000000000000000001"),
        AccountInfo {
            balance: U256::from(100000000000000_u64),
            ..Default::default()
        },
    );

    let mut evm = Evm::builder()
        .with_db(db)
        .modify_block_env(|block| {
            block.basefee = U256::from(0);
            block.gas_limit = U256::from(100_000_000); // Tăng block gas limit
        })
        .build();

    let tx = evm.tx_mut();
    tx.caller = address!("0000000000000000000000000000000000000001");
    tx.transact_to = TransactTo::Call(contract_address);
    tx.value = U256::from(0);
    tx.gas_limit = 50_000_000; // Cho phép dùng tới 50 triệu Gas
    tx.gas_price = U256::from(0);

    let result = evm.transact();
    let debug_str = alloc::format!("{:?}", result);
    match result {
        Ok(res) => {
            // Because of infinite loop, it should return OutOfGas or Halt
            match res.result {
                ExecutionResult::Halt { gas_used, .. } => (true, gas_used, debug_str),
                ExecutionResult::Revert { gas_used, .. } => (true, gas_used, debug_str),
                ExecutionResult::Success { gas_used, .. } => (false, gas_used, debug_str),
            }
        },
        Err(_) => (false, 0, debug_str),
    }
}

// Bài test 3: Tấn công Calldata khổng lồ (Gửi chuỗi dữ liệu 5MB vào EVM)
pub fn run_large_calldata_contract() -> (bool, alloc::string::String) {
    let mut db = CacheDB::new(EmptyDB::default());
    
    // Nạp tiền cho hacker
    db.insert_account_info(
        address!("0000000000000000000000000000000000000111"),
        AccountInfo {
            balance: U256::from(100000000000000_u64),
            ..Default::default()
        },
    );

    let mut evm = Evm::builder()
        .with_db(db)
        .modify_block_env(|block| {
            block.basefee = U256::from(0);
            // Block gas limit của mạng thường là 30 triệu Gas
            block.gas_limit = U256::from(30_000_000); 
        })
        .build();

    let tx = evm.tx_mut();
    tx.caller = address!("0000000000000000000000000000000000000111");
    tx.transact_to = TransactTo::Call(address!("0000000000000000000000000000000000000099"));
    tx.value = U256::from(0);
    
    // Giao dịch thông thường có gas limit là 15 triệu
    tx.gas_limit = 15_000_000; 
    tx.gas_price = U256::from(0);
    
    // Hacker nhồi 5MB Calldata toàn số khác không (tốn 16 Gas mỗi byte)
    // Intrinsic Gas cần thiết: 21,000 + (5,242,880 * 16) = ~83.9 Triệu Gas
    let payload = alloc::vec![1u8; 5 * 1024 * 1024]; 
    tx.data = Bytes::from(payload);

    let result = evm.transact();
    let debug_str = alloc::format!("{:?}", result);
    match result {
        // Nếu EVM bắt chặn thành công từ vòng giữ xe do thiếu Intrinsic Gas, nó sẽ ném lỗi Err
        Err(e) => {
            // Kiểm tra xem có phải lỗi vượt gas_limit không
            if debug_str.contains("CallGasCostMoreThanGasLimit") {
                (true, alloc::format!("Bị triệt tiêu ngay từ lúc kiểm duyệt: {}", e))
            } else {
                (false, alloc::format!("Lỗi khác: {}", e))
            }
        },
        Ok(_) => (false, alloc::format!("Giao dịch lọt qua được EVM! Thất bại bảo mật.")),
    }
}

// Bài test 4: Tìm đỉnh giới hạn bộ nhớ (Peak Limit) an toàn cho TEE
// Toán học: Với Block Gas Limit chuẩn là 30 triệu Gas, dung lượng RAM lớn nhất 
// mà một giao dịch có thể yêu cầu EVM cấp phát là bao nhiêu?
// Công thức Gas: Gas = (Words^2)/512 + 3*Words. (1 Word = 32 bytes)
// Với 30,000,000 Gas -> Max Words = 123,169 -> Max Bytes = ~3.94 MB.
// Hàm này test thử mở rộng RAM ở 2 mức độ.
pub fn run_peak_limit_test(target_memory_bytes: u32, gas_limit: u64) -> (bool, u64, alloc::string::String) {
    let mut db = CacheDB::new(EmptyDB::default());
    
    // Tạo bytecode để MSTORE vào vị trí bộ nhớ `target_memory_bytes`
    // Opcode:
    // PUSH1 0x01 (6001)
    // PUSH4 <target_bytes> (63 .. .. .. ..)
    // MSTORE (52)
    // STOP (00)
    let b1 = ((target_memory_bytes >> 24) & 0xFF) as u8;
    let b2 = ((target_memory_bytes >> 16) & 0xFF) as u8;
    let b3 = ((target_memory_bytes >> 8) & 0xFF) as u8;
    let b4 = (target_memory_bytes & 0xFF) as u8;
    
    let code: [u8; 8] = [
        0x60, 0x01,       // PUSH1 1 (Value to store)
        0x63, b1, b2, b3, b4, // PUSH4 target offset
        0x52,             // MSTORE
    ];
    let contract_address = address!("0000000000000000000000000000000000000099");
    
    db.insert_account_info(
        contract_address,
        AccountInfo {
            balance: U256::from(0),
            nonce: 1,
            code_hash: revm::primitives::KECCAK_EMPTY,
            code: Some(Bytecode::new_raw(Bytes::from(code.to_vec()))),
        },
    );
    
    db.insert_account_info(
        address!("0000000000000000000000000000000000000001"),
        AccountInfo {
            balance: U256::from(100000000000000_u64),
            ..Default::default()
        },
    );

    let mut evm = Evm::builder()
        .with_db(db)
        .modify_block_env(|block| {
            block.basefee = U256::from(0);
            block.gas_limit = U256::from(gas_limit); 
        })
        .build();

    let tx = evm.tx_mut();
    tx.caller = address!("0000000000000000000000000000000000000001");
    tx.transact_to = TransactTo::Call(contract_address);
    tx.value = U256::from(0);
    tx.gas_limit = gas_limit; 
    tx.gas_price = U256::from(0);

    let result = evm.transact();
    let debug_str = alloc::format!("{:?}", result);
    match result {
        Ok(res) => {
            match res.result {
                ExecutionResult::Halt { gas_used, .. } => (false, gas_used, debug_str), // OOG
                ExecutionResult::Revert { gas_used, .. } => (false, gas_used, debug_str),
                ExecutionResult::Success { gas_used, .. } => (true, gas_used, debug_str),
            }
        },
        Err(_) => (false, 0, debug_str),
    }
}
// Bài test 5: Airdrop dựa trên kết quả tìm kiếm Xapian (Dual-Environment)
// Chúng ta sẽ giả lập một hợp đồng gọi STATICCALL tới 0x1000
// Hợp đồng sau đó không thể lặp phức tạp bằng mã asm đơn giản, 
// nên thay vì loop, PoC này chỉ yêu cầu hợp đồng gọi Xapian và nhận kết quả để in ra.
pub fn run_airdrop_with_search(provider: Arc<dyn SearchProvider>) -> (bool, alloc::string::String) {
    let mut db = CacheDB::new(EmptyDB::default());
    
    // Bytecode hợp đồng: 
    // Gọi STATICCALL tới 0x1000, copy kết quả về return data và thoát.
    // 60 00       - PUSH1 0x00 (ret length = 0, we'll copy later)
    // 60 00       - PUSH1 0x00 (ret offset)
    // 60 03       - PUSH1 0x03 (arg length = 3 bytes "VIP")
    // 60 1c       - PUSH1 0x1C (arg offset, after code)
    // 60 00       - PUSH1 0x00 (value = 0)
    // 61 10 00    - PUSH2 0x1000 (Xapian address)
    // 61 FF FF    - PUSH2 0xFFFF (Gas)
    // FA          - STATICCALL
    // 3d          - RETURNDATASIZE
    // 60 00       - PUSH1 0x00
    // 60 00       - PUSH1 0x00
    // 3e          - RETURNDATACOPY
    // 3d          - RETURNDATASIZE
    // 60 00       - PUSH1 0x00
    // F3          - RETURN
    // Dữ liệu chữ "VIP": 56 49 50
    let code: [u8; 27] = [
        0x60, 0x00, 0x60, 0x00, 0x60, 0x03, 0x60, 0x1A, 
        0x60, 0x00, 0x61, 0x10, 0x00, 0x61, 0xFF, 0xFF, 
        0xFA, 0x3D, 0x60, 0x00, 0x60, 0x00, 0x3E, 0x3D, 
        0x60, 0x00, 0xF3
    ];
    let mut full_code = alloc::vec::Vec::new();
    full_code.extend_from_slice(&code);
    full_code.extend_from_slice(b"VIP"); // data ở offset 0x1A (26)

    let contract_address = address!("0000000000000000000000000000000000000099");
    
    db.insert_account_info(
        contract_address,
        AccountInfo {
            balance: U256::from(100000000000000_u64),
            nonce: 1,
            code_hash: revm::primitives::KECCAK_EMPTY,
            code: Some(Bytecode::new_raw(Bytes::from(full_code))),
        },
    );
    
    db.insert_account_info(
        address!("0000000000000000000000000000000000000001"),
        AccountInfo {
            balance: U256::from(100000000000000_u64),
            ..Default::default()
        },
    );

    let mut evm = Evm::builder()
        .with_db(db)
        .with_external_context(SearchInspector { provider })
        .append_handler_register(revm::inspector_handle_register)
        .modify_block_env(|block| {
            block.basefee = U256::from(0);
            block.gas_limit = U256::from(30_000_000); 
        })
        .build();

    let tx = evm.tx_mut();
    tx.caller = address!("0000000000000000000000000000000000000001");
    tx.transact_to = TransactTo::Call(contract_address);
    tx.value = U256::from(0);
    tx.gas_limit = 1_000_000; 
    tx.gas_price = U256::from(0);

    let result = evm.transact();
    let debug_str = alloc::format!("{:?}", result);
    match result {
        Ok(res) => {
            match res.result {
                ExecutionResult::Success { output, .. } => {
                    let hex_out = alloc::format!("{:?}", output);
                    (true, hex_out)
                }
                _ => (false, alloc::format!("Halt or Revert: {}", debug_str)),
            }
        },
        Err(_) => (false, debug_str),
    }
}
