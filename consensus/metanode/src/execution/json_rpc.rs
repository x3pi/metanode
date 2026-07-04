use anyhow::Result;
use jsonrpsee::server::ServerBuilder;
use jsonrpsee::RpcModule;
use serde::Serialize;
use prost::Message;
use tracing::info;

use crate::node::executor_client::block_store;

#[derive(Serialize)]
pub struct BlockResponse {
    pub number: String, // hex string
    pub hash: String,   // hex string
    #[serde(rename = "parentHash")]
    pub parent_hash: String,
    pub nonce: String,
    #[serde(rename = "sha3Uncles")]
    pub sha3_uncles: String,
    #[serde(rename = "logsBloom")]
    pub logs_bloom: String,
    #[serde(rename = "transactionsRoot")]
    pub transactions_root: String,
    #[serde(rename = "stateRoot")]
    pub state_root: String,
    #[serde(rename = "receiptsRoot")]
    pub receipts_root: String,
    pub miner: String,
    pub difficulty: String,
    #[serde(rename = "totalDifficulty")]
    pub total_difficulty: String,
    #[serde(rename = "extraData")]
    pub extra_data: String,
    pub size: String,
    #[serde(rename = "gasLimit")]
    pub gas_limit: String,
    #[serde(rename = "gasUsed")]
    pub gas_used: String,
    pub timestamp: String,
    pub transactions: Vec<serde_json::Value>, // We'll return full tx objects or hashes based on _is_full_tx
    pub uncles: Vec<String>,
    #[serde(rename = "globalExecIndex")]
    pub global_exec_index: String,
    pub epoch: String,
    #[serde(rename = "commitIndex")]
    pub commit_index: String,
    #[serde(rename = "stakeStatesRoot")]
    pub stake_states_root: String,
    #[serde(rename = "aggregateSignature")]
    pub aggregate_signature: String,
}

pub struct Context {
    pub storage_path: std::path::PathBuf,
}

pub async fn start_json_rpc_server(port: u16, storage_path: std::path::PathBuf) -> Result<()> {
    let server = ServerBuilder::default()
        .max_request_body_size(200 * 1024 * 1024)
        .max_response_body_size(200 * 1024 * 1024)
        .build(format!("0.0.0.0:{}", port))
        .await?;
    
    let mut module = RpcModule::new(Context { storage_path });
    
    module.register_async_method("eth_getBlockByNumber", |params, ctx| async move {
        
        // We only care about block number for now
        let mut block_number_str = String::new();
        let mut _is_full_tx = false;
        
        let arr: Result<(String, bool), _> = params.parse();
        if let Ok((b_str, full_tx)) = arr {
            block_number_str = b_str;
            _is_full_tx = full_tx;
        } else {
            // Fallback for array params parsing
            let arr_fallback: Result<Vec<serde_json::Value>, _> = params.parse();
            if let Ok(vec_params) = arr_fallback {
                if vec_params.len() > 0 {
                    if let Some(s) = vec_params[0].as_str() {
                        block_number_str = s.to_string();
                    }
                }
                if vec_params.len() > 1 {
                    if let Some(b) = vec_params[1].as_bool() {
                        _is_full_tx = b;
                    }
                }
            }
        }
        
        let block_number = if block_number_str == "latest" {
            // Very naive way to get latest block for mock
            if let Ok(Some(max_gei)) = block_store::get_max_stored_gei(&ctx.storage_path).await {
                max_gei
            } else {
                0
            }
        } else {
            // parse hex
            let hex_str = block_number_str.trim_start_matches("0x");
            u64::from_str_radix(hex_str, 16).unwrap_or(0)
        };
        
        if let Ok(block_list) = block_store::load_executable_blocks_range(&ctx.storage_path, block_number, block_number).await {
            if let Some((_, data)) = block_list.first() {
                if let Ok(bdata) = crate::node::executor_client::proto::ExecutableBlock::decode(&data[..]) {
                    
                    let mut hash_hex = hex::encode(&bdata.commit_hash);
                    if hash_hex.is_empty() { hash_hex = "0000000000000000000000000000000000000000000000000000000000000000".to_string(); }
                    
                    let mut state_root_hex = hex::encode(&bdata.state_root);
                    if state_root_hex.is_empty() { state_root_hex = "0000000000000000000000000000000000000000000000000000000000000000".to_string(); }

                    let tx_root_hex = "0000000000000000000000000000000000000000000000000000000000000000".to_string();

                    let mut parent_hash_hex = "0000000000000000000000000000000000000000000000000000000000000000".to_string();
                    if block_number > 0 {
                        if let Ok(prev_list) = block_store::load_executable_blocks_range(&ctx.storage_path, block_number - 1, block_number - 1).await {
                            if let Some((_, prev_data)) = prev_list.first() {
                                if let Ok(prev_bdata) = crate::node::executor_client::proto::ExecutableBlock::decode(&prev_data[..]) {
                                    parent_hash_hex = hex::encode(&prev_bdata.commit_hash);
                                    if parent_hash_hex.is_empty() {
                                        parent_hash_hex = "0000000000000000000000000000000000000000000000000000000000000000".to_string();
                                    }
                                }
                            }
                        }
                    }

                    let response = BlockResponse {
                        number: format!("0x{:x}", block_number),
                        hash: format!("0x{}", hash_hex),
                        parent_hash: format!("0x{}", parent_hash_hex),
                        nonce: "0x0000000000000042".to_string(),
                        sha3_uncles: "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347".to_string(),
                        logs_bloom: "0x0".to_string(),
                        transactions_root: format!("0x{}", tx_root_hex),
                        state_root: format!("0x{}", state_root_hex),
                        receipts_root: "0x0000000000000000000000000000000000000000000000000000000000000000".to_string(),
                        miner: "0x0000000000000000000000000000000000000000".to_string(),
                        difficulty: "0x0".to_string(),
                        total_difficulty: "0x0".to_string(),
                        extra_data: "0x".to_string(),
                        size: "0x1000".to_string(),
                        gas_limit: format!("0x{:x}", 30000000), // Mock
                        gas_used: format!("0x{:x}", 0),
                        timestamp: format!("0x{:x}", bdata.commit_timestamp_ms / 1000),
                        transactions: bdata.transactions.into_iter().enumerate().map(|(idx, t)| {
                            // Compute the Metanode transaction hash (pbHash)
                            let hash = crate::types::tx_hash::calculate_transaction_hash_single(&t.digest);
                            let hash_hex = hex::encode(hash);

                            if _is_full_tx {
                                serde_json::json!({
                                    "hash": format!("0x{}", hash_hex),
                                    "groupId": t.worker_id.to_string(),
                                    "transactionIndex": format!("0x{:x}", idx)
                                })
                            } else {
                                serde_json::json!(format!("0x{}", hash_hex))
                            }
                        }).collect(),
                        uncles: vec![],
                        global_exec_index: format!("0x{:x}", bdata.global_exec_index),
                        epoch: format!("0x{:x}", bdata.epoch),
                        commit_index: format!("0x{:x}", bdata.commit_index),
                        stake_states_root: "0x0000000000000000000000000000000000000000000000000000000000000000".to_string(),
                        aggregate_signature: "".to_string(),
                    };
                    return Ok::<serde_json::Value, jsonrpsee::types::ErrorObjectOwned>(serde_json::to_value(response).unwrap());
                }
            }
        }
        
        Ok(serde_json::Value::Null)
    })?;

    // Also implement net_version
    module.register_method("net_version", |_, _| {
        Ok::<String, jsonrpsee::types::ErrorObjectOwned>("1337".to_string())
    })?;

    // Implement eth_chainId
    module.register_method("eth_chainId", |_, _| {
        Ok::<String, jsonrpsee::types::ErrorObjectOwned>("0x539".to_string()) // 1337 in hex
    })?;

    // Implement eth_getTransactionCount
    module.register_method("eth_getTransactionCount", |_, _| {
        Ok::<String, jsonrpsee::types::ErrorObjectOwned>("0x0".to_string())
    })?;

    // Implement eth_sendRawTransaction
    module.register_method("eth_sendRawTransaction", |_, _| {
        Ok::<String, jsonrpsee::types::ErrorObjectOwned>("0x0000000000000000000000000000000000000000000000000000000000000000".to_string())
    })?;

    // Implement eth_blockNumber
    module.register_async_method("eth_blockNumber", |_, ctx| async move {
        let max_gei = if let Ok(Some(gei)) = block_store::get_max_stored_gei(&ctx.storage_path).await {
            gei
        } else {
            0
        };
        Ok::<String, jsonrpsee::types::ErrorObjectOwned>(format!("0x{:x}", max_gei))
    })?;

    // Implement eth_getBlockTraces
    module.register_async_method("eth_getBlockTraces", |params, ctx| async move {
        let mut start_block: u64 = 0;
        let mut end_block: u64 = 0;

        let arr_fallback: Result<Vec<serde_json::Value>, _> = params.parse();
        if let Ok(vec_params) = arr_fallback {
            if vec_params.len() > 0 {
                if let Some(s) = vec_params[0].as_u64() {
                    start_block = s;
                }
            }
            if vec_params.len() > 1 {
                if let Some(s) = vec_params[1].as_u64() {
                    end_block = s;
                }
            }
        }

        let mut traces = Vec::new();
        
        if let Ok(block_list) = block_store::load_executable_blocks_range(&ctx.storage_path, start_block, end_block).await {
            for (_, data) in block_list {
                if let Ok(bdata) = crate::node::executor_client::proto::ExecutableBlock::decode(&data[..]) {
                    let trace = serde_json::json!({
                        "block_number": bdata.global_exec_index,
                        "tx_count": bdata.transactions.len(),
                        "evm_execution_duration_us": bdata.block_stm_evm_duration_us,
                        "commit_duration_us": bdata.block_stm_commit_duration_us,
                        "total_execution_us": bdata.block_stm_evm_duration_us + bdata.block_stm_commit_duration_us,
                    });
                    traces.push(trace);
                }
            }
        }
        
        Ok::<serde_json::Value, jsonrpsee::types::ErrorObjectOwned>(serde_json::to_value(traces).unwrap())
    })?;

    // Implement mtn_getAccountState
    module.register_method("mtn_getAccountState", |params, _| {
        let mut address_str = "".to_string();
        let arr_fallback: Result<Vec<serde_json::Value>, _> = params.parse();
        if let Ok(vec_params) = arr_fallback {
            if vec_params.len() > 0 {
                if let Some(s) = vec_params[0].as_str() {
                    address_str = s.to_string();
                }
            }
        }
        
        let mut address_bytes = [0u8; 20];
        let addr_clean = if address_str.starts_with("0x") {
            &address_str[2..]
        } else {
            &address_str
        };
        if let Ok(bytes) = hex::decode(addr_clean) {
            let limit = bytes.len().min(20);
            address_bytes[20 - limit..].copy_from_slice(&bytes[..limit]);
        }

        use crate::execution::global_db::GLOBAL_NOMT_DB;
        use crate::execution::nomt_db::ACCOUNT_CACHE;
        use revm::primitives::{Address, U256};

        let address = Address::from(address_bytes);
        let (nonce, balance) = if let Some(info) = ACCOUNT_CACHE.get(&address) {
            (info.nonce, info.balance.to_string())
        } else {
            let mut key = [0u8; 32];
            key[12..].copy_from_slice(&address_bytes);

            let mut nonce_key = [0u8; 32];
            nonce_key[12..].copy_from_slice(&address_bytes);
            nonce_key[0] = 1;

            let nonce = if let Ok(Some(bytes)) = GLOBAL_NOMT_DB.read(nonce_key) {
                if bytes.len() == 8 {
                    u64::from_be_bytes(bytes.try_into().unwrap_or_default())
                } else {
                    0
                }
            } else {
                0
            };

            let balance = if let Ok(Some(bytes)) = GLOBAL_NOMT_DB.read(key) {
                if bytes.len() == 32 {
                    let val = U256::from_be_bytes::<32>(bytes.try_into().unwrap_or([0u8; 32]));
                    val.to_string()
                } else {
                    "10000000000000000000000000".to_string()
                }
            } else {
                "10000000000000000000000000".to_string()
            };
            (nonce, balance)
        };
        
        let response = serde_json::json!({
            "publicKeyBls": "",
            "address": address_str,
            "nonce": nonce,
            "balance": balance
        });
        Ok::<serde_json::Value, jsonrpsee::types::ErrorObjectOwned>(response)
    })?;

    info!("🚀 [JSON-RPC] Server started on port {}", port);
    
    let handle = server.start(module);
    
    // We just spawn it and let it run
    tokio::spawn(handle.stopped());
    
    Ok(())
}
