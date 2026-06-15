// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

//! Peer RPC client functions — query remote peers over TCP/HTTP.
//!
//! Used for epoch discovery, fetching boundary data from peers, and
//! forwarding transactions from SyncOnly nodes to validators.

use anyhow::Result;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tracing::{info, warn};

use super::types::*;

// Re-export executor proto for block data types
use crate::node::executor_client::proto::BlockData;

/// Query peer info from a remote node via HTTP
pub async fn query_peer_info(peer_address: &str) -> Result<PeerInfoResponse> {
    use tokio::net::TcpStream;

    // Connect with timeout
    let mut stream = tokio::time::timeout(
        std::time::Duration::from_secs(5),
        TcpStream::connect(peer_address),
    )
    .await
    .map_err(|_| anyhow::anyhow!("Connection timeout to {}", peer_address))?
    .map_err(|e| anyhow::anyhow!("Failed to connect to {}: {}", peer_address, e))?;

    // Send HTTP GET request
    let request = format!(
        "GET /peer_info HTTP/1.1\r\nHost: {}\r\nConnection: close\r\n\r\n",
        peer_address
    );
    stream.write_all(request.as_bytes()).await?;

    // Read response with timeout
    let mut buffer = Vec::new();
    let mut temp = [0u8; 4096];
    let read_result = tokio::time::timeout(std::time::Duration::from_secs(5), async {
        loop {
            match stream.read(&mut temp).await {
                Ok(0) => break,
                Ok(n) => buffer.extend_from_slice(&temp[..n]),
                Err(e) => return Err(e),
            }
        }
        Ok(())
    })
    .await;

    match read_result {
        Ok(Ok(_)) => {}
        Ok(Err(e)) => return Err(anyhow::anyhow!("Failed to read response: {}", e)),
        Err(_) => {
            return Err(anyhow::anyhow!(
                "Timeout reading response from {}",
                peer_address
            ))
        }
    }

    // Parse HTTP response
    let response_str = String::from_utf8_lossy(&buffer);

    // Find JSON body (after empty line)
    let body_start = response_str
        .find("\r\n\r\n")
        .map(|i| i + 4)
        .or_else(|| response_str.find("\n\n").map(|i| i + 2))
        .unwrap_or(0);

    let body = &response_str[body_start..];

    // Parse JSON
    let info: PeerInfoResponse = serde_json::from_str(body.trim()).map_err(|e| {
        anyhow::anyhow!(
            "Failed to parse peer info JSON: {} (body: {})",
            e,
            body.trim()
        )
    })?;

    Ok(info)
}

/// Query multiple peers and return the best one (highest epoch/block/global_exec_index)
pub async fn query_peer_epochs_network(
    peer_addresses: &[String],
) -> Result<(u64, u64, String, u64)> {
    // info!(
    //     "🌐 [PEER RPC] Querying {} peer(s) over network for epoch discovery...",
    //     peer_addresses.len()
    // );

    let mut best_epoch = 0u64;
    let mut best_block = 0u64;
    let mut best_address = String::new();
    let mut best_global_exec_index = 0u64;

    for peer_addr in peer_addresses {
        match query_peer_info(peer_addr).await {
            Ok(info) => {
                info!(
                    "🌐 [PEER RPC] Peer ({}): epoch={}, block={}, global_exec_index={}",
                    peer_addr, info.epoch, info.last_block, info.last_global_exec_index
                );

                // Use this peer if it has higher epoch, or same epoch and higher global_exec_index
                if best_address.is_empty()
                    || info.epoch > best_epoch
                    || (info.epoch == best_epoch
                        && info.last_global_exec_index > best_global_exec_index)
                {
                    best_epoch = info.epoch;
                    best_block = info.last_block;
                    best_global_exec_index = info.last_global_exec_index;
                    best_address = peer_addr.clone();
                    info!(
                        "🌐 [PEER RPC] New best peer: epoch={} block={} global_exec_index={} from {}",
                        best_epoch, best_block, best_global_exec_index, peer_addr
                    );
                }
            }
            Err(e) => {
                warn!("🌐 [PEER RPC] Failed to query peer ({}): {}", peer_addr, e);
            }
        }
    }

    if best_address.is_empty() {
        return Err(anyhow::anyhow!("No reachable peers found"));
    }

    info!(
        "🌐 [PEER RPC] Best peer found: epoch={} block={} global_exec_index={} from {}",
        best_epoch, best_block, best_global_exec_index, best_address
    );

    Ok((best_epoch, best_block, best_address, best_global_exec_index))
}

/// Query epoch boundary data from a remote peer via HTTP
/// This is used by late-joining validators to get epoch boundary data from peers
/// who have already witnessed the epoch transition
pub async fn query_peer_epoch_boundary_data(
    peer_address: &str,
    epoch: u64,
) -> Result<EpochBoundaryDataResponse> {
    use tokio::net::TcpStream;

    info!(
        "🌐 [PEER RPC] Querying epoch boundary data for epoch {} from {}",
        epoch, peer_address
    );

    // Connect with timeout
    let mut stream = tokio::time::timeout(
        std::time::Duration::from_secs(10),
        TcpStream::connect(peer_address),
    )
    .await
    .map_err(|_| anyhow::anyhow!("Connection timeout to {}", peer_address))?
    .map_err(|e| anyhow::anyhow!("Failed to connect to {}: {}", peer_address, e))?;

    // Send HTTP GET request
    let request = format!(
        "GET /get_epoch_boundary_data?epoch={} HTTP/1.1\r\nHost: {}\r\nConnection: close\r\n\r\n",
        epoch, peer_address
    );
    stream.write_all(request.as_bytes()).await?;

    // Read response with timeout
    let mut buffer = Vec::new();
    let mut temp = [0u8; 16384]; // Larger buffer for validator data
    let read_result = tokio::time::timeout(std::time::Duration::from_secs(15), async {
        loop {
            match stream.read(&mut temp).await {
                Ok(0) => break,
                Ok(n) => buffer.extend_from_slice(&temp[..n]),
                Err(e) => return Err(e),
            }
        }
        Ok(())
    })
    .await;

    match read_result {
        Ok(Ok(_)) => {}
        Ok(Err(e)) => return Err(anyhow::anyhow!("Failed to read response: {}", e)),
        Err(_) => {
            return Err(anyhow::anyhow!(
                "Timeout reading response from {}",
                peer_address
            ))
        }
    }

    // Parse HTTP response
    let response_str = String::from_utf8_lossy(&buffer);

    // Find JSON body (after empty line)
    let body_start = response_str
        .find("\r\n\r\n")
        .map(|i| i + 4)
        .or_else(|| response_str.find("\n\n").map(|i| i + 2))
        .unwrap_or(0);

    let body = &response_str[body_start..];

    // Parse JSON
    let response: EpochBoundaryDataResponse = serde_json::from_str(body.trim()).map_err(|e| {
        anyhow::anyhow!(
            "Failed to parse epoch boundary data JSON: {} (body: {})",
            e,
            body.trim()
        )
    })?;

    // Check for error in response
    if let Some(error) = &response.error {
        return Err(anyhow::anyhow!("Peer returned error: {}", error));
    }

    info!(
        "🌐 [PEER RPC] Received epoch boundary data from {}: epoch={}, timestamp={}, boundary_block={}, validators={}",
        peer_address, response.epoch, response.timestamp_ms, response.boundary_block, response.validators.len()
    );

    Ok(response)
}

/// Fetch blocks from a peer node via HTTP /get_blocks endpoint.
/// Batches requests by 100 blocks (server-side limit).
/// Returns Vec<BlockData> ready for sync_blocks().
///
/// Used by Rust to orchestrate block sync during cross-epoch catch-up:
/// peer Go master → peer Rust → this function → local sync_blocks() → local Go master
pub async fn fetch_blocks_from_peer(
    peer_addresses: &[String],
    from_block: u64,
    to_block: u64,
) -> Result<Vec<BlockData>> {
    if peer_addresses.is_empty() {
        return Err(anyhow::anyhow!("No peer addresses configured"));
    }

    let total_blocks = to_block.saturating_sub(from_block) + 1;
    info!(
        "🔄 [BLOCK-FETCH] Fetching {} blocks ({} to {}) from {} peer(s) in PARALLEL",
        total_blocks,
        from_block,
        to_block,
        peer_addresses.len()
    );

    let batch_size = if total_blocks >= 1000 {
        10u64 // Safe batch to prevent server OOM
    } else if total_blocks >= 200 {
        10u64
    } else if total_blocks >= 50 {
        10u64
    } else {
        10u64
    };

    let max_concurrent = std::cmp::min(peer_addresses.len() * 2, 8);
    let semaphore = std::sync::Arc::new(tokio::sync::Semaphore::new(max_concurrent));

    let mut join_handles = Vec::new();
    let mut current_from = from_block;
    let mut peer_idx = 0;

    let peer_list = peer_addresses.to_vec();

    while current_from <= to_block {
        let current_to = std::cmp::min(current_from + batch_size - 1, to_block);
        
        let permit = semaphore.clone().acquire_owned().await.map_err(|e| anyhow::anyhow!("Semaphore closed: {}", e))?;
        let peers = peer_list.clone();
        
        // Spawn a task for this chunk
        let handle = tokio::spawn(async move {
            let _permit = permit;
            let mut last_err = None;
            
            for i in 0..peers.len() {
                let peer_addr = &peers[(peer_idx + i) % peers.len()];
                match fetch_block_batch(peer_addr, current_from, current_to).await {
                    Ok(blocks) => {
                        let expected = (current_to - current_from + 1) as usize;
                        if blocks.len() < expected {
                            warn!(
                                "⚠️ [BLOCK-FETCH] Peer {} returned incomplete blocks ({}/{}) for range {}-{}",
                                peer_addr, blocks.len(), expected, current_from, current_to
                            );
                            last_err = Some(anyhow::anyhow!(
                                "Incomplete blocks: got {}, expected {}",
                                blocks.len(), expected
                            ));
                            continue;
                        }
                        info!(
                            "✅ [BLOCK-FETCH] Got {} blocks ({}-{}) from peer {}",
                            blocks.len(), current_from, current_to, peer_addr
                        );
                        return Ok((current_from, current_to, blocks));
                    }
                    Err(e) => {
                        warn!(
                            "⚠️ [BLOCK-FETCH] Peer {} failed for blocks {}-{}: {}",
                            peer_addr, current_from, current_to, e
                        );
                        last_err = Some(e);
                    }
                }
            }
            Err(anyhow::anyhow!(
                "All peers failed for batch {}-{}. Last error: {:?}",
                current_from, current_to, last_err
            ))
        });
        
        join_handles.push(handle);
        current_from = current_to + 1;
        peer_idx += 1;
    }

    let mut all_blocks_map = std::collections::BTreeMap::new();
    
    for handle in join_handles {
        match handle.await.map_err(|e| anyhow::anyhow!("Task panicked: {}", e))? {
            Ok((_, _, blocks)) => {
                for block in blocks {
                    all_blocks_map.insert(block.block_number, block);
                }
            }
            Err(e) => return Err(e),
        }
    }

    let all_blocks: Vec<BlockData> = all_blocks_map.into_values().collect();

    info!(
        "📦 [BLOCK-FETCH] Total: {} blocks fetched ({} to {})",
        all_blocks.len(),
        from_block,
        to_block
    );

    Ok(all_blocks)
}

/// Fetch a single batch of blocks from one peer via HTTP
async fn fetch_block_batch(
    peer_addr: &str,
    from_block: u64,
    to_block: u64,
) -> Result<Vec<BlockData>> {
    use prost::Message;
    use tokio::net::TcpStream;

    // Connect with timeout
    let mut stream = tokio::time::timeout(
        std::time::Duration::from_secs(10),
        TcpStream::connect(peer_addr),
    )
    .await
    .map_err(|_| anyhow::anyhow!("Connection timeout to {}", peer_addr))?
    .map_err(|e| anyhow::anyhow!("Failed to connect to {}: {}", peer_addr, e))?;

    // Send HTTP GET request
    let request = format!(
        "GET /get_blocks?from={}&to={} HTTP/1.1\r\nHost: {}\r\nConnection: close\r\n\r\n",
        from_block, to_block, peer_addr
    );
    stream.write_all(request.as_bytes()).await?;

    // Read response with timeout (block data can be large)
    let mut buffer = Vec::new();
    let mut temp = [0u8; 65536]; // 64KB buffer for block data
    let read_result = tokio::time::timeout(std::time::Duration::from_secs(300), async {
        loop {
            match stream.read(&mut temp).await {
                Ok(0) => break,
                Ok(n) => buffer.extend_from_slice(&temp[..n]),
                Err(e) => return Err(e),
            }
        }
        Ok(())
    })
    .await;

    match read_result {
        Ok(Ok(_)) => {}
        Ok(Err(e)) => return Err(anyhow::anyhow!("Failed to read response: {}", e)),
        Err(_) => {
            return Err(anyhow::anyhow!(
                "Timeout reading response from {}",
                peer_addr
            ))
        }
    }

    // Parse HTTP response
    let response_str = String::from_utf8_lossy(&buffer);

    // Find JSON body
    let body_start = response_str
        .find("\r\n\r\n")
        .map(|i| i + 4)
        .or_else(|| response_str.find("\n\n").map(|i| i + 2))
        .unwrap_or(0);

    let body = &response_str[body_start..];

    // Parse GetBlocksResponse JSON
    let response: GetBlocksResponse = serde_json::from_str(body.trim())
        .map_err(|e| anyhow::anyhow!("Failed to parse blocks response JSON: {}", e))?;

    if let Some(error) = &response.error {
        return Err(anyhow::anyhow!("Peer returned error: {}", error));
    }

    // Decode protobuf-encoded BlockData from hex strings
    let mut blocks = Vec::with_capacity(response.blocks.len());
    for (block_num, hex_data) in &response.blocks {
        let proto_bytes = hex::decode(hex_data)
            .map_err(|e| anyhow::anyhow!("Failed to decode hex for block {}: {}", block_num, e))?;
        let block_data = BlockData::decode(&proto_bytes[..]).map_err(|e| {
            anyhow::anyhow!("Failed to decode protobuf for block {}: {}", block_num, e)
        })?;
        blocks.push(block_data);
    }

    // Sort blocks by block_number for sequential processing
    blocks.sort_by_key(|b| b.block_number);

    Ok(blocks)
}

/// Fetch ExecutableBlock protobuf bytes from peer Rust's /get_executable_blocks endpoint.
/// Returns Vec<(global_exec_index, raw_protobuf_bytes)> — these are EXACTLY what Go expects
/// on the dataChan, ready to be sent via send_block_data().
///
/// NO Go PebbleDB involved — pure Rust-to-Rust sync.
#[allow(dead_code)]
pub async fn fetch_executable_blocks_from_peer(
    peer_addresses: &[String],
    from_gei: u64,
    to_gei: u64,
) -> Result<Vec<(u64, Vec<u8>)>> {
    if peer_addresses.is_empty() {
        return Err(anyhow::anyhow!("No peer addresses configured"));
    }

    let total = to_gei.saturating_sub(from_gei) + 1;
    info!(
        "🔄 [EXEC-BLOCK-FETCH] Fetching {} executable blocks (GEI {} to {}) from {} peer(s) in PARALLEL",
        total,
        from_gei,
        to_gei,
        peer_addresses.len()
    );

    let batch_size = if total >= 1000 {
        10u64
    } else if total >= 200 {
        10u64
    } else if total >= 50 {
        10u64
    } else {
        10u64
    };

    let max_concurrent = std::cmp::min(peer_addresses.len() * 2, 8);
    let semaphore = std::sync::Arc::new(tokio::sync::Semaphore::new(max_concurrent));

    let mut join_handles = Vec::new();
    let mut current_from = from_gei;
    let mut peer_idx = 0;

    let peer_list = peer_addresses.to_vec();

    while current_from <= to_gei {
        let current_to = std::cmp::min(current_from + batch_size - 1, to_gei);
        
        let permit = semaphore.clone().acquire_owned().await.map_err(|e| anyhow::anyhow!("Semaphore closed: {}", e))?;
        let peers = peer_list.clone();
        
        let handle = tokio::spawn(async move {
            let _permit = permit;
            let mut last_err = None;
            
            for i in 0..peers.len() {
                let peer_addr = &peers[(peer_idx + i) % peers.len()];
                match fetch_executable_block_batch(peer_addr, current_from, current_to).await {
                    Ok(blocks) => {
                        let expected = (current_to - current_from + 1) as usize;
                        if blocks.len() < expected {
                            warn!(
                                "⚠️ [EXEC-BLOCK-FETCH] Peer {} returned incomplete blocks ({}/{}) for GEI {}-{}",
                                peer_addr, blocks.len(), expected, current_from, current_to
                            );
                            last_err = Some(anyhow::anyhow!(
                                "Incomplete blocks: got {}, expected {}",
                                blocks.len(), expected
                            ));
                            continue;
                        }
                        info!(
                            "✅ [EXEC-BLOCK-FETCH] Got {} executable blocks (GEI {}-{}) from peer {}",
                            blocks.len(), current_from, current_to, peer_addr
                        );
                        return Ok((current_from, current_to, blocks));
                    }
                    Err(e) => {
                        warn!(
                            "⚠️ [EXEC-BLOCK-FETCH] Peer {} failed for GEI {}-{}: {}",
                            peer_addr, current_from, current_to, e
                        );
                        last_err = Some(e);
                    }
                }
            }
            Err(anyhow::anyhow!(
                "All peers failed for batch GEI {}-{}. Last error: {:?}",
                current_from, current_to, last_err
            ))
        });

        join_handles.push(handle);
        current_from = current_to + 1;
        peer_idx += 1;
    }

    let mut all_blocks_map = std::collections::BTreeMap::new();
    
    for handle in join_handles {
        match handle.await.map_err(|e| anyhow::anyhow!("Task panicked: {}", e))? {
            Ok((_, _, blocks)) => {
                for (gei, data) in blocks {
                    all_blocks_map.insert(gei, data);
                }
            }
            Err(e) => return Err(e),
        }
    }

    let mut all_blocks: Vec<(u64, Vec<u8>)> = all_blocks_map.into_iter().collect();

    // Sort by GEI for sequential execution
    all_blocks.sort_by_key(|(gei, _)| *gei);

    info!(
        "📦 [EXEC-BLOCK-FETCH] Total: {} executable blocks fetched",
        all_blocks.len()
    );
    Ok(all_blocks)
}

/// Fetch a single batch of executable blocks from one peer via HTTP
#[allow(dead_code)]
async fn fetch_executable_block_batch(
    peer_addr: &str,
    from_gei: u64,
    to_gei: u64,
) -> Result<Vec<(u64, Vec<u8>)>> {
    use tokio::net::TcpStream;

    let mut stream = tokio::time::timeout(
        std::time::Duration::from_secs(10),
        TcpStream::connect(peer_addr),
    )
    .await
    .map_err(|_| anyhow::anyhow!("Connection timeout to {}", peer_addr))?
    .map_err(|e| anyhow::anyhow!("Failed to connect to {}: {}", peer_addr, e))?;

    // Use /get_executable_blocks — reads from Rust file store, NOT Go PebbleDB
    let request = format!(
        "GET /get_executable_blocks?from={}&to={} HTTP/1.1\r\nHost: {}\r\nConnection: close\r\n\r\n",
        from_gei, to_gei, peer_addr
    );
    stream.write_all(request.as_bytes()).await?;

    let mut buffer = Vec::new();
    let mut temp = [0u8; 65536];
    let read_result = tokio::time::timeout(std::time::Duration::from_secs(300), async {
        loop {
            match stream.read(&mut temp).await {
                Ok(0) => break,
                Ok(n) => buffer.extend_from_slice(&temp[..n]),
                Err(e) => return Err(e),
            }
        }
        Ok(())
    })
    .await;

    match read_result {
        Ok(Ok(_)) => {}
        Ok(Err(e)) => return Err(anyhow::anyhow!("Failed to read response: {}", e)),
        Err(_) => return Err(anyhow::anyhow!("Timeout reading from {}", peer_addr)),
    }

    // Parse HTTP response
    let response_str = String::from_utf8_lossy(&buffer);
    let body_start = response_str
        .find("\r\n\r\n")
        .map(|i| i + 4)
        .or_else(|| response_str.find("\n\n").map(|i| i + 2))
        .unwrap_or(0);

    let body = &response_str[body_start..];

    // Parse response — same GetBlocksResponse format but blocks contain raw ExecutableBlock bytes
    let response: GetBlocksResponse = serde_json::from_str(body.trim())
        .map_err(|e| anyhow::anyhow!("Failed to parse response JSON: {}", e))?;

    if let Some(error) = &response.error {
        return Err(anyhow::anyhow!("Peer returned error: {}", error));
    }

    // Decode hex → raw ExecutableBlock protobuf bytes (NOT proto::BlockData!)
    let mut blocks = Vec::with_capacity(response.blocks.len());
    for (gei, hex_data) in &response.blocks {
        let data = hex::decode(hex_data)
            .map_err(|e| anyhow::anyhow!("Failed to decode hex for GEI {}: {}", gei, e))?;
        blocks.push((*gei, data));
    }

    blocks.sort_by_key(|(gei, _)| *gei);
    Ok(blocks)
}

/// Forward a batch of transactions to a validator node via /submit_transaction HTTP POST endpoint.
pub async fn forward_transactions_to_peer(
    peer_address: &str,
    transactions: Vec<Vec<u8>>,
) -> Result<SubmitTransactionResponse> {
    use tokio::net::TcpStream;

    let tx_hex_list: Vec<String> = transactions.iter().map(hex::encode).collect();
    let req = SubmitTransactionRequest {
        transactions_hex: tx_hex_list,
    };
    let body = serde_json::to_string(&req)?;

    // Connect with timeout
    let mut stream = tokio::time::timeout(
        std::time::Duration::from_secs(5),
        TcpStream::connect(peer_address),
    )
    .await
    .map_err(|_| anyhow::anyhow!("Connection timeout to validator {}", peer_address))?
    .map_err(|e| anyhow::anyhow!("Failed to connect to validator {}: {}", peer_address, e))?;

    // Send HTTP POST request
    let request = format!(
        "POST /submit_transaction HTTP/1.1\r\nHost: {}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
        peer_address,
        body.len(),
        body
    );
    stream.write_all(request.as_bytes()).await?;

    // Read response with timeout
    let mut buffer = Vec::new();
    let mut temp = [0u8; 4096];
    let read_result = tokio::time::timeout(std::time::Duration::from_secs(5), async {
        loop {
            match stream.read(&mut temp).await {
                Ok(0) => break,
                Ok(n) => buffer.extend_from_slice(&temp[..n]),
                Err(e) => return Err(e),
            }
        }
        Ok(())
    })
    .await;

    match read_result {
        Ok(Ok(_)) => {}
        Ok(Err(e)) => return Err(anyhow::anyhow!("Failed to read response from validator: {}", e)),
        Err(_) => {
            return Err(anyhow::anyhow!(
                "Timeout reading response from validator {}",
                peer_address
            ))
        }
    }

    // Parse HTTP response
    let response_str = String::from_utf8_lossy(&buffer);

    // Find JSON body (after empty line)
    let body_start = response_str
        .find("\r\n\r\n")
        .map(|i| i + 4)
        .or_else(|| response_str.find("\n\n").map(|i| i + 2))
        .unwrap_or(0);

    let response_body = &response_str[body_start..];

    // Parse JSON response
    let resp: SubmitTransactionResponse = serde_json::from_str(response_body.trim()).map_err(|e| {
        anyhow::anyhow!(
            "Failed to parse submit transaction response: {} (body: {})",
            e,
            response_body.trim()
        )
    })?;

    Ok(resp)
}

