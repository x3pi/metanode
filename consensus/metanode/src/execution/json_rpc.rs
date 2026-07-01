use anyhow::Result;
use jsonrpsee::server::ServerBuilder;
use jsonrpsee::RpcModule;
use serde::Serialize;
use prost::Message;
use tracing::info;

use crate::node::executor_client::block_store;

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct BlockResponse {
    pub number: String, // hex string
    pub hash: String,   // hex string
    pub parent_hash: String,
    pub nonce: String,
    pub sha3_uncles: String,
    pub logs_bloom: String,
    pub transactions_root: String,
    pub state_root: String,
    pub receipts_root: String,
    pub miner: String,
    pub difficulty: String,
    pub total_difficulty: String,
    pub extra_data: String,
    pub size: String,
    pub gas_limit: String,
    pub gas_used: String,
    pub timestamp: String,
    pub transactions: Vec<String>, // We'll just return hashes if include_tx is false, or omit full tx support for now
    pub uncles: Vec<String>,
    pub global_exec_index: String,
    pub epoch: String,
    pub commit_index: String,
}

pub struct Context {
    pub storage_path: std::path::PathBuf,
}

pub async fn start_json_rpc_server(port: u16, storage_path: std::path::PathBuf) -> Result<()> {
    let server = ServerBuilder::default().build(format!("0.0.0.0:{}", port)).await?;
    
    let mut module = RpcModule::new(Context { storage_path });
    
    module.register_async_method("eth_getBlockByNumber", |params, ctx| async move {
        
        // We only care about block number for now
        let mut block_number_str = String::new();
        let mut is_full_tx = false;
        
        let arr: Result<(String, bool), _> = params.parse();
        if let Ok((b_str, full_tx)) = arr {
            block_number_str = b_str;
            is_full_tx = full_tx;
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
                        is_full_tx = b;
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
                        transactions: vec![],
                        uncles: vec![],
                        global_exec_index: format!("0x{:x}", bdata.global_exec_index),
                        epoch: format!("0x{:x}", bdata.epoch),
                        commit_index: format!("0x{:x}", bdata.commit_index),
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
        
        let response = serde_json::json!({
            "publicKeyBls": "",
            "address": address_str,
            "nonce": 0,
            "balance": "10000000000000000000000000"
        });
        Ok::<serde_json::Value, jsonrpsee::types::ErrorObjectOwned>(response)
    })?;

    info!("🚀 [JSON-RPC] Server started on port {}", port);
    
    let handle = server.start(module);
    
    // We just spawn it and let it run
    tokio::spawn(handle.stopped());
    
    Ok(())
}
