use rayon::prelude::*;
use revm::primitives::{Address, Bytes, U256, AccountInfo, B256, Bytecode};
use revm::{Database, DatabaseRef};
use metanode_tee_revm::evm::MetanodeEvm;
use crate::execution::nomt_db::NomtDatabase;
use std::collections::{HashMap, HashSet};
use std::sync::Arc;
use std::cell::RefCell;

pub struct TrackingDB {
    base_db: NomtDatabase,
    pub reads: RefCell<HashSet<[u8; 32]>>,
    pub writes: RefCell<HashMap<[u8; 32], Option<Vec<u8>>>>,
}

impl TrackingDB {
    pub fn new(base_db: NomtDatabase) -> Self {
        Self {
            base_db,
            reads: RefCell::new(HashSet::new()),
            writes: RefCell::new(HashMap::new()),
        }
    }
}

impl DatabaseRef for TrackingDB {
    type Error = String;

    fn basic_ref(&self, address: Address) -> Result<Option<AccountInfo>, Self::Error> {
        let mut key = [0u8; 32];
        key[12..].copy_from_slice(address.as_slice());
        self.reads.borrow_mut().insert(key);
        self.base_db.basic_ref(address)
    }

    fn code_by_hash_ref(&self, code_hash: B256) -> Result<Bytecode, Self::Error> {
        self.reads.borrow_mut().insert(code_hash.0);
        self.base_db.code_by_hash_ref(code_hash)
    }

    fn storage_ref(&self, address: Address, index: U256) -> Result<U256, Self::Error> {
        let mut buf = Vec::new();
        buf.extend_from_slice(address.as_slice());
        buf.extend_from_slice(&index.to_be_bytes::<32>());
        let key = revm::primitives::keccak256(&buf).0;
        self.reads.borrow_mut().insert(key);
        self.base_db.storage_ref(address, index)
    }

    fn block_hash_ref(&self, number: u64) -> Result<B256, Self::Error> {
        self.base_db.block_hash_ref(number)
    }
}

impl Database for TrackingDB {
    type Error = String;

    fn basic(&mut self, address: Address) -> Result<Option<AccountInfo>, Self::Error> {
        self.basic_ref(address)
    }

    fn code_by_hash(&mut self, code_hash: B256) -> Result<Bytecode, Self::Error> {
        self.code_by_hash_ref(code_hash)
    }

    fn storage(&mut self, address: Address, index: U256) -> Result<U256, Self::Error> {
        self.storage_ref(address, index)
    }

    fn block_hash(&mut self, number: u64) -> Result<B256, Self::Error> {
        self.block_hash_ref(number)
    }
}

pub struct BlockStmScheduler {
    // We will expand this to full Block-STM later
}

impl BlockStmScheduler {
    pub fn execute_batch(
        db: Arc<nomt_db::RustStateDB>,
        txs: Vec<(Address, Address, Bytes, u64, U256, u64)>,
    ) -> Result<([u8; 32], std::time::Duration, std::time::Duration), String> {
        let max_gas_limit = txs.iter().map(|tx| tx.3).max().unwrap_or(30_000_000);
        let num_threads = rayon::current_num_threads().max(1);
        let chunk_size = (txs.len() / num_threads).max(1);
        let chunks: Vec<_> = txs.chunks(chunk_size).map(|c| c.to_vec()).collect();

        let start_evm = std::time::Instant::now();

        let session = db.begin_session();
        txs.par_iter().for_each(|(caller, target, _, _, _, _)| {
            use crate::execution::nomt_db::ACCOUNT_CACHE;

            let caller_key = NomtDatabase::address_to_key(caller);
            let mut caller_nonce_key = caller_key;
            caller_nonce_key[0] = 1;

            let target_key = NomtDatabase::address_to_key(target);
            let mut target_nonce_key = target_key;
            target_nonce_key[0] = 1;

            session.warm_up(caller_key);
            session.warm_up(caller_nonce_key);
            session.warm_up(target_key);
            session.warm_up(target_nonce_key);

            if !ACCOUNT_CACHE.contains_key(caller) {
                let balance = match session.read(caller_key) {
                    Ok(Some(bytes)) => {
                        if bytes.len() == 32 {
                            U256::from_be_bytes::<32>(bytes.try_into().unwrap_or([0u8; 32]))
                        } else {
                            U256::ZERO
                        }
                    }
                    _ => U256::ZERO,
                };
                let nonce = match session.read(caller_nonce_key) {
                    Ok(Some(bytes)) => {
                        if bytes.len() == 8 {
                            u64::from_be_bytes(bytes.try_into().unwrap_or_default())
                        } else {
                            0
                        }
                    }
                    _ => 0,
                };
                ACCOUNT_CACHE.insert(*caller, AccountInfo {
                    balance,
                    nonce,
                    code_hash: revm::primitives::KECCAK_EMPTY,
                    code: None,
                });
            }

            if !ACCOUNT_CACHE.contains_key(target) {
                let balance = match session.read(target_key) {
                    Ok(Some(bytes)) => {
                        if bytes.len() == 32 {
                            U256::from_be_bytes::<32>(bytes.try_into().unwrap_or([0u8; 32]))
                        } else {
                            U256::ZERO
                        }
                    }
                    _ => U256::ZERO,
                };
                let nonce = match session.read(target_nonce_key) {
                    Ok(Some(bytes)) => {
                        if bytes.len() == 8 {
                            u64::from_be_bytes(bytes.try_into().unwrap_or_default())
                        } else {
                            0
                        }
                    }
                    _ => 0,
                };
                ACCOUNT_CACHE.insert(*target, AccountInfo {
                    balance,
                    nonce,
                    code_hash: revm::primitives::KECCAK_EMPTY,
                    code: None,
                });
            }
        });


        let shared_session = Arc::new(session);

        // Phase 1: Parallel speculative execution
        let chunk_results: Vec<_> = chunks.into_par_iter().map(|chunk| {
            let nomt_db = NomtDatabase::new(db.clone(), Some(shared_session.clone()));
            let tracking_db = TrackingDB::new(nomt_db);
            let mut cache_db = revm::db::CacheDB::new(tracking_db);
            let mut evm = MetanodeEvm::new(&mut cache_db, max_gas_limit);
            
            for (caller, target, calldata, gas_limit, value, nonce) in chunk.clone() {
                let _ = evm.transact_commit(caller, target, calldata, gas_limit, value, nonce);
            }
            std::mem::drop(evm);
            
            let reads = cache_db.db.reads.into_inner();
            let writes = cache_db.db.writes.into_inner();
            (reads, writes, cache_db.accounts, chunk)
        }).collect();

        // Phase 2: Conflict detection and sequential resolution
        let mut global_writes: HashMap<[u8; 32], Option<Vec<u8>>> = HashMap::new();
        let mut global_accounts: HashMap<Address, AccountInfo> = HashMap::new();
        let mut modified_keys: HashSet<[u8; 32]> = HashSet::new();

        let mut fallback_triggered = false;
        let mut fallback_db = None;

        for (i, (reads, writes, accounts, chunk)) in chunk_results.into_iter().enumerate() {
            if fallback_triggered {
                // Execute remaining transactions sequentially in the fallback DB
                let f_db = fallback_db.as_mut().unwrap();
                let mut evm = MetanodeEvm::new(f_db, max_gas_limit);
                for (caller, target, calldata, gas_limit, value, nonce) in chunk {
                    let _ = evm.transact_commit(caller, target, calldata, gas_limit, value, nonce);
                }
                continue;
            }

            let mut conflict = false;
            if i > 0 {
                // Check read set against modified keys
                for r in &reads {
                    if modified_keys.contains(r) {
                        conflict = true;
                        break;
                    }
                }
                // Check write set against modified keys (Write-Write conflicts)
                if !conflict {
                    for w in writes.keys() {
                        if modified_keys.contains(w) {
                            conflict = true;
                            break;
                        }
                    }
                }
            }

            if conflict {
                fallback_triggered = true;
                
                // Initialize fallback DB and seed it with all global accounts up to now
                let nomt_db = NomtDatabase::new(db.clone(), Some(shared_session.clone()));
                let tracking_db = TrackingDB::new(nomt_db);
                let mut f_db = revm::db::CacheDB::new(tracking_db);
                for (addr, acc) in &global_accounts {
                    f_db.insert_account_info(*addr, acc.clone());
                }
                
                let mut evm = MetanodeEvm::new(&mut f_db, max_gas_limit);
                for (caller, target, calldata, gas_limit, value, nonce) in chunk {
                    let _ = evm.transact_commit(caller, target, calldata, gas_limit, value, nonce);
                }
                std::mem::drop(evm);
                fallback_db = Some(f_db);
            } else {
                // Merge speculative results directly
                for (address, account) in accounts {
                    global_accounts.insert(address, account.info.clone());
                    let mut key = [0u8; 32];
                    key[12..].copy_from_slice(address.as_slice());
                    modified_keys.insert(key);

                    let mut key_nonce = [0u8; 32];
                    key_nonce[12..].copy_from_slice(address.as_slice());
                    key_nonce[0] = 1;
                    modified_keys.insert(key_nonce);
                }
            }
        }

        // Merge final fallback DB results if fallback was triggered
        if let Some(f_db) = fallback_db {
            for (address, account) in f_db.accounts {
                global_accounts.insert(address, account.info.clone());
            }
        }

        let evm_duration = start_evm.elapsed();

        // Convert global_accounts into global_writes for PebbleDB commit
        for (address, info) in global_accounts {
            crate::execution::nomt_db::ACCOUNT_CACHE.insert(address, info.clone());
            
            let mut key = [0u8; 32];
            key[12..].copy_from_slice(address.as_slice());
            let balance_bytes = info.balance.to_be_bytes::<32>();
            global_writes.insert(key, Some(balance_bytes.to_vec()));

            let mut key_nonce = [0u8; 32];
            key_nonce[12..].copy_from_slice(address.as_slice());
            key_nonce[0] = 1;
            let nonce_bytes = info.nonce.to_be_bytes();
            global_writes.insert(key_nonce, Some(nonce_bytes.to_vec()));
        }

        let read_vec = Vec::new();

        let write_vec = global_writes.into_iter().collect();
        
        let session = match Arc::try_unwrap(shared_session) {
            Ok(s) => s,
            Err(_) => {
                panic!("❌ [RUST-EXEC] Arc reference count of session is > 1. Cannot unwrap.");
            }
        };

        let start_commit = std::time::Instant::now();
        let commit_res = db.commit_session(session, write_vec, read_vec);
        let commit_duration = start_commit.elapsed();
        
        tracing::info!("⏱️ [RUST-EXEC-TIME] BlockSTM EVM duration: {:?}, Commit duration: {:?}", evm_duration, commit_duration);
        
        commit_res.map(|root| (root, evm_duration, commit_duration))
    }
}


