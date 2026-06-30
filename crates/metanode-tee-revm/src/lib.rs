#![no_std]

extern crate alloc;

use revm::{
    primitives::{address, AccountInfo, ExecutionResult, TransactTo, U256},
    db::{CacheDB, EmptyDB},
    Evm,
};

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

use revm::primitives::{Bytes, Bytecode};

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
