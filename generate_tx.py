import os

with open("/home/abc/chain-n/mtn-consensus/metanode/src/network/tx_socket_server.rs", "r") as f:
    lines = f.readlines()

out = []
# Find the extraction logic
start_idx = 0
for i, line in enumerate(lines):
    if "let mut individual_txs = Vec::new();" in line:
        start_idx = i
        break

end_idx = 0
for i in range(start_idx, len(lines)):
    if "if all_succeeded {" in lines[i]:
        end_idx = i
        break

parser_lines = lines[start_idx:end_idx]

# Clean up parser lines by removing stream.send_response_string and returning appropriately
cleaned_parser_lines = []
for line in parser_lines:
    if "Self::send_response_string" in line or "return Err" in line:
        continue
    if "continue; // Tiếp tục" in line or "continue; // Continue" in line:
        cleaned_parser_lines.append("                continue;\n")
        continue

    cleaned_parser_lines.append(line)

parser_logic = "".join(cleaned_parser_lines)

full_file = """use crate::node::tx_submitter::TransactionSubmitter;
use crate::node::ConsensusNode;
use crate::tx_recycler::TxRecycler;
use anyhow::Result;
use meta_consensus_core as consensus_core;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use tokio::sync::{Mutex, RwLock};
use tracing::{debug, error, info, warn};

pub struct TxSocketServer {
    transaction_client: Arc<dyn TransactionSubmitter>,
    node: Option<Arc<Mutex<ConsensusNode>>>,
    is_transitioning: Option<Arc<AtomicBool>>,
    peer_rpc_addresses: Vec<String>,
    peer_discovery_addresses: Option<Arc<RwLock<Vec<String>>>>,
    tx_recycler: Option<Arc<TxRecycler>>,
}

impl TxSocketServer {
    pub fn with_node(
        transaction_client: Arc<dyn TransactionSubmitter>,
        node: Option<Arc<Mutex<ConsensusNode>>>,
        is_transitioning: Option<Arc<AtomicBool>>,
        peer_rpc_addresses: Vec<String>,
    ) -> Self {
        Self {
            transaction_client,
            node,
            is_transitioning,
            peer_rpc_addresses,
            peer_discovery_addresses: None,
            tx_recycler: None,
        }
    }

    pub fn with_peer_discovery(mut self, addresses: Arc<RwLock<Vec<String>>>) -> Self {
        self.peer_discovery_addresses = Some(addresses);
        self
    }

    pub fn with_tx_recycler(mut self, recycler: Arc<TxRecycler>) -> Self {
        self.tx_recycler = Some(recycler);
        self
    }

    pub async fn start(self) -> Result<()> {
        let (ffi_tx_sender, mut ffi_tx_receiver) = tokio::sync::mpsc::channel::<Vec<u8>>(1000);
        if crate::ffi::FFI_TX_SENDER.set(ffi_tx_sender).is_err() {
            warn!("⚠️ [FFI TX SENDER] Already initialized!");
        }
        info!("🔌 FFI Transaction Receiver started in place of UDS server");

        let client = self.transaction_client;
        let node = self.node;
        let is_transitioning = self.is_transitioning;
        let peer_rpc_addresses = self.peer_rpc_addresses;
        let peer_discovery_addresses = self.peer_discovery_addresses;
        let tx_recycler = self.tx_recycler;

        while let Some(tx_data) = ffi_tx_receiver.recv().await {
            let client_ref = client.clone();
            let node_ref = node.clone();
            let is_transitioning_ref = is_transitioning.clone();
            let peer_rpc_addresses_ref = peer_rpc_addresses.clone();
            let peer_discovery_addresses_ref = peer_discovery_addresses.clone();
            let tx_recycler_ref = tx_recycler.clone();

            tokio::spawn(async move {
                Self::process_ffi_batch(
                    tx_data,
                    client_ref,
                    node_ref,
                    is_transitioning_ref,
                    peer_rpc_addresses_ref,
                    peer_discovery_addresses_ref,
                    tx_recycler_ref,
                )
                .await;
            });
        }
        Ok(())
    }

    async fn process_ffi_batch(
        tx_data: Vec<u8>,
        client: Arc<dyn TransactionSubmitter>,
        node: Option<Arc<Mutex<ConsensusNode>>>,
        is_transitioning: Option<Arc<AtomicBool>>,
        peer_rpc_addresses: Vec<String>,
        peer_discovery_addresses: Option<Arc<RwLock<Vec<String>>>>,
        tx_recycler: Option<Arc<TxRecycler>>,
    ) {
        let mut attempt = 0;
        loop {
            let data_len = tx_data.len();
""" + parser_logic + """
            if all_succeeded || attempt >= 30 {
                break;
            }
            
            attempt += 1;
            tokio::time::sleep(tokio::time::Duration::from_millis(2000)).await;
        }
    }
}
"""

with open("/home/abc/chain-n/mtn-consensus/metanode/src/network/tx_socket_server.rs", "w") as f:
    f.write(full_file)

