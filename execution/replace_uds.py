import re

with open('/home/abc/chain-n/metanode/consensus/metanode/src/network/tx_socket_server.rs', 'r') as f:
    content = f.read()

# Replace listener start up to the loop
start_rx = re.compile(r'pub async fn start\(self\) -> Result<\(\)> \{.*?loop \{.*?// PERSISTENT CONNECTION:', re.DOTALL)
content = start_rx.sub('''pub async fn start(self) -> Result<()> {
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
                ).await;
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
        // Retry loop for epoch transition scenarios
        let mut attempt = 0;
        loop {
            // INNER LOGIC START''', content)

# Remove Unnecessary stream components
content = content.replace('mut stream: UnixStream,', '')
content = content.replace('_pending_transactions_queue: Option<Arc<Mutex<Vec<Vec<u8>>>>>,', '')
content = content.replace('_storage_path: Option<std::path::PathBuf>,', '')

# Replace reading from stream
stream_read_rx = re.compile(r'// Use the new codec module.*?let mut parse_error = false;', re.DOTALL)
content = stream_read_rx.sub('''let mut parse_error = false;''', content)

with open('/home/abc/chain-n/metanode/consensus/metanode/src/network/tx_socket_server.rs', 'w') as f:
    f.write(content)
