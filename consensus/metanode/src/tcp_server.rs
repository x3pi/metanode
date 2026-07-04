// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use std::sync::Arc;
use tokio::net::TcpListener;
use tokio::io::AsyncReadExt;
use tracing::{info, error, debug};
use crate::node::tx_submitter::TransactionSubmitter;
use prost::Message;

pub mod p2p_message {
    include!(concat!(env!("OUT_DIR"), "/message.rs"));
}

pub mod transaction_proto {
    include!(concat!(env!("OUT_DIR"), "/transaction.rs"));
}

pub async fn start_tcp_server(
    port: u16,
    tx_submitter: Option<Arc<dyn TransactionSubmitter>>,
    tx_recycler: Option<Arc<crate::consensus::tx_recycler::TxRecycler>>,
) -> Result<(), Box<dyn std::error::Error>> {
    let addr = format!("0.0.0.0:{}", port);
    let listener = TcpListener::bind(&addr).await?;
    info!("Starting legacy TCP P2P Transaction Server on {}", addr);

    tokio::spawn(async move {
        loop {
            match listener.accept().await {
                Ok((mut stream, peer_addr)) => {
                    let submitter_clone = tx_submitter.clone();
                    let recycler_clone = tx_recycler.clone();
                    tokio::spawn(async move {
                        // info!("Accepted legacy TCP connection from {}", peer_addr);
                        let mut length_buf = [0u8; 8];
                        loop {
                            if let Err(_) = stream.read_exact(&mut length_buf).await {
                                // eof or connection closed
                                break;
                            }
                            let msg_len = u64::from_le_bytes(length_buf) as usize;
                            if msg_len > 10 * 1024 * 1024 {
                                error!("TCP message too large: {} bytes from {}", msg_len, peer_addr);
                                break;
                            }

                            let mut msg_buf = vec![0u8; msg_len];
                            if let Err(e) = stream.read_exact(&mut msg_buf).await {
                                error!("Failed to read TCP message body from {}: {}", peer_addr, e);
                                break;
                            }

                            match p2p_message::Message::decode(&msg_buf[..]) {
                                Ok(msg) => {
                                    if let Some(ref submitter) = submitter_clone {
                                        info!("TCP SERVER RECEIVED HEADER: {:?}", msg.header);
                                        let command = msg.header.as_ref().map(|h| h.command.as_str()).unwrap_or("");
                                        debug!("TCP SERVER RECEIVED COMMAND: {:?}", command);
                                        if command == "SendTransactions" {
                                            match crate::tcp_server::transaction_proto::Transactions::decode(&msg.body[..]) {
                                                Ok(transactions) => {
                                                    let mut txs_bytes = Vec::new();
                                                    info!("Decoded SendTransactions with {} txs", transactions.transactions.len());
                                                    for tx in transactions.transactions {
                                                        let mut buf = Vec::new();
                                                        if let Err(e) = prost::Message::encode(&tx, &mut buf) {
                                                            error!("Failed to encode transaction: {}", e);
                                                            continue;
                                                        }
                                                        txs_bytes.push(buf);
                                                    }
                                                    if !txs_bytes.is_empty() {
                                                        // Track in TxRecycler so that if these txs are proposed but not committed,
                                                        // they will be recycled and not lost!
                                                        if let Some(ref recycler) = recycler_clone {
                                                            recycler.track_submitted(&txs_bytes).await;
                                                        }

                                                        // Chunk to stay safely below consensus limit of 10000 per block
                                                        for chunk in txs_bytes.chunks(5000) {
                                                            if let Err(e) = submitter.submit_no_wait(chunk.to_vec()).await {
                                                                error!("Failed to submit transaction chunk from TCP: {:?}", e);
                                                            }
                                                        }
                                                    }
                                                }
                                                Err(e) => {
                                                    error!("Failed to decode Transactions protobuf message: {}", e);
                                                }
                                            }
                                        } else {
                                            // legacy single transaction fallback just in case
                                            if let Err(e) = submitter.submit_no_wait(vec![msg.body]).await {
                                                error!("Failed to submit transaction from TCP: {:?}", e);
                                            }
                                        }
                                    }
                                }
                                Err(e) => {
                                    error!("Failed to decode legacy protobuf message: {}", e);
                                }
                            }
                        }
                    });
                }
                Err(e) => {
                    error!("Failed to accept TCP connection: {}", e);
                }
            }
        }
    });

    Ok(())
}
