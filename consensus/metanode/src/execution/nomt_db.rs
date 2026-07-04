use revm::primitives::{AccountInfo, Address, Bytecode, B256, U256};
use revm::{Database, DatabaseRef};
use nomt_db::RustStateDB;
use std::sync::Arc;
use once_cell::sync::Lazy;

pub static ACCOUNT_CACHE: Lazy<dashmap::DashMap<Address, AccountInfo>> = Lazy::new(|| dashmap::DashMap::new());

pub struct NomtDatabase {
    pub db: Arc<RustStateDB>,
    pub session: Option<Arc<nomt_db::Session<nomt_db::Blake3Hasher>>>,
}

impl NomtDatabase {
    pub fn new(
        db: Arc<RustStateDB>,
        session: Option<Arc<nomt_db::Session<nomt_db::Blake3Hasher>>>,
    ) -> Self {
        Self { db, session }
    }

    pub fn address_to_key(address: &Address) -> [u8; 32] {
        let mut key = [0u8; 32];
        key[12..].copy_from_slice(address.as_slice());
        key
    }

    pub fn storage_to_key(address: &Address, index: &U256) -> [u8; 32] {
        let mut buf = Vec::new();
        buf.extend_from_slice(address.as_slice());
        buf.extend_from_slice(&index.to_be_bytes::<32>());
        revm::primitives::keccak256(&buf).into()
    }

}

impl DatabaseRef for NomtDatabase {
    type Error = String;

    fn basic_ref(&self, address: Address) -> Result<Option<AccountInfo>, Self::Error> {
        if let Some(info) = ACCOUNT_CACHE.get(&address) {
            return Ok(Some(info.clone()));
        }

        let key = Self::address_to_key(&address);
        let mut nonce_key = [0u8; 32];
        nonce_key[12..].copy_from_slice(address.as_slice());
        nonce_key[0] = 1;

        let balance = if let Some(ref session) = self.session {
            match session.read(key).map_err(|e| e.to_string()) {
                Ok(Some(bytes)) => {
                    if bytes.len() == 32 {
                        U256::from_be_bytes::<32>(bytes.try_into().unwrap_or([0u8; 32]))
                    } else {
                        U256::ZERO
                    }
                }
                _ => U256::ZERO,
            }
        } else {
            match self.db.read(key) {
                Ok(Some(bytes)) => {
                    if bytes.len() == 32 {
                        U256::from_be_bytes::<32>(bytes.try_into().unwrap_or([0u8; 32]))
                    } else {
                        U256::ZERO
                    }
                }
                _ => U256::ZERO,
            }
        };

        let nonce = if let Some(ref session) = self.session {
            match session.read(nonce_key).map_err(|e| e.to_string()) {
                Ok(Some(bytes)) => {
                    if bytes.len() == 8 {
                        u64::from_be_bytes(bytes.try_into().unwrap_or_default())
                    } else {
                        0
                    }
                }
                _ => 0,
            }
        } else {
            match self.db.read(nonce_key) {
                Ok(Some(bytes)) => {
                    if bytes.len() == 8 {
                        u64::from_be_bytes(bytes.try_into().unwrap_or_default())
                    } else {
                        0
                    }
                }
                _ => 0,
            }
        };

        let info = AccountInfo {
            balance,
            nonce,
            code_hash: revm::primitives::KECCAK_EMPTY,
            code: None,
        };

        ACCOUNT_CACHE.insert(address, info.clone());

        Ok(Some(info))
    }

    fn code_by_hash_ref(&self, code_hash: B256) -> Result<Bytecode, Self::Error> {
        let key = code_hash.0;
        let res = if let Some(ref session) = self.session {
            session.read(key).map_err(|e| e.to_string())
        } else {
            self.db.read(key)
        };
        match res {
            Ok(Some(bytes)) => Ok(Bytecode::new_raw(bytes.into())),
            Ok(None) => Ok(Bytecode::new()),
            Err(e) => Err(e),
        }
    }

    fn storage_ref(&self, address: Address, index: U256) -> Result<U256, Self::Error> {
        let key = Self::storage_to_key(&address, &index);
        let res = if let Some(ref session) = self.session {
            session.read(key).map_err(|e| e.to_string())
        } else {
            self.db.read(key)
        };
        match res {
            Ok(Some(bytes)) => {
                if bytes.len() == 32 {
                    Ok(U256::from_be_bytes::<32>(bytes.try_into().unwrap()))
                } else {
                    Ok(U256::ZERO)
                }
            }
            Ok(None) => Ok(U256::ZERO),
            Err(e) => Err(e),
        }
    }

    fn block_hash_ref(&self, _number: u64) -> Result<B256, Self::Error> {
        Ok(B256::default())
    }
}

impl Database for NomtDatabase {
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

