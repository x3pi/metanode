#![no_std]

extern crate alloc;

use revm::{
    primitives::{address, AccountInfo, ExecutionResult, TransactTo, U256},
    db::{CacheDB, EmptyDB},
    Evm,
};

pub fn run_empty_contract() -> bool {
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
        .build();

    let tx = evm.tx_mut();
    tx.caller = address!("0000000000000000000000000000000000000001");
    tx.transact_to = TransactTo::Call(address!("0000000000000000000000000000000000000002"));
    tx.value = U256::from(100);

    let result = evm.transact();
    match result {
        Ok(res) => match res.result {
            ExecutionResult::Success { .. } => true,
            _ => false
        },
        Err(_) => false,
    }
}
