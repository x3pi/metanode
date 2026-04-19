import re

orig_path = "/home/abc/chain-n/mtn-consensus/metanode/src/network/tx_socket_server.rs"
with open(orig_path, 'r') as f:
    content = f.read()

start_parser = content.find("            let mut individual_txs = Vec::new();")
end_parser = content.find("            if all_succeeded {")

if start_parser != -1 and end_parser != -1:
    parser_logic = content[start_parser:end_parser]
    
    # Strip any UDS return errors
    parser_logic = parser_logic.replace(
        "if let Err(e) = Self::send_response_string(&mut stream, error_response).await {",
        "if false {"
    )
    parser_logic = parser_logic.replace(
        "if let Err(e) = Self::send_response_string(&mut stream, &error_response).await {",
        "if false {"
    )
    parser_logic = parser_logic.replace(
        "if let Err(e) = Self::send_response_string(&mut stream, reject_response).await {",
        "if false {"
    )
    parser_logic = parser_logic.replace(
        "if let Err(e) = Self::send_response_string(&mut stream, &success_response).await {",
        "if false {"
    )
    parser_logic = parser_logic.replace("if let Err(e) = Self::send_response_string(&mut stream, error_response).await {", "if false {")

    parser_logic = parser_logic.replace("return Err(e);", "")
    parser_logic = parser_logic.replace("continue; // Tiếp tục xử lý request tiếp theo", "continue;")
    parser_logic = parser_logic.replace("return Ok(());", "return;")
    
new_file = """use crate::node::tx_submitter::TransactionSubmitter;
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
___PARSER_LOGIC___

            if all_succeeded || attempt >= 30 {
                break;
            }
            
            attempt += 1;
            tokio::time::sleep(tokio::time::Duration::from_millis(2000)).await;
        }
    }
}
"""

new_file = new_file.replace("___PARSER_LOGIC___", parser_logic)

with open(orig_path, 'w') as f:
    f.write(new_file)

