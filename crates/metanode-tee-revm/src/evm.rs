

use revm::{
    primitives::{Address, Bytes, ExecutionResult, TransactTo, U256, ResultAndState},
    Database, Evm,
};

pub struct MetanodeEvm<'a, DB: Database> {
    pub evm: Evm<'a, (), DB>,
}

impl<'a, DB: Database> MetanodeEvm<'a, DB> {
    pub fn new(db: DB, gas_limit: u64) -> Self {
        let evm = Evm::builder()
            .with_db(db)
            .modify_block_env(|block| {
                block.basefee = U256::from(0);
                block.gas_limit = U256::from(gas_limit);
            })
            .build();
        Self { evm }
    }

    pub fn transact_optimistic(
        &mut self,
        caller: Address,
        target: Address,
        calldata: Bytes,
        gas_limit: u64,
        value: U256,
    ) -> Result<ResultAndState, &'static str> {
        let tx = self.evm.tx_mut();
        tx.caller = caller;
        tx.transact_to = TransactTo::Call(target);
        tx.data = calldata;
        tx.gas_limit = gas_limit;
        tx.gas_price = U256::from(0);
        tx.value = value;

        match self.evm.transact() {
            Ok(result_and_state) => Ok(result_and_state),
            Err(_) => Err("EVM execution failed"),
        }
    }
}
