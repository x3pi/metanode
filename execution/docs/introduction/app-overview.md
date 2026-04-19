# Meta Node Blockchain - Application Overview

## 📋 Mục lục
- [Tổng quan](#tổng-quan)
- [Kiến trúc hệ thống](#kiến-trúc-hệ-thống)
- [Quy trình khởi tạo](#quy-trình-khởi-tạo)
- [Các loại Node](#các-loại-node)
- [Storage Architecture](#storage-architecture)
- [Network Communication](#network-communication)
- [Transaction Processing](#transaction-processing)
- [Block Generation](#block-generation)
- [Genesis Block](#genesis-block)
- [API Endpoints](#api-endpoints)

---

## 🎯 Tổng quan

**Meta Node Blockchain** là một blockchain node implementation với các tính năng:

- ✅ **Full EVM Compatibility** - Hỗ trợ smart contracts Solidity
- ✅ **Multi-Node Architecture** - Master, Write, Read-Only nodes
- ✅ **BLS Consensus** - Validators sử dụng BLS signatures
- ✅ **Merkle Patricia Trie** - Efficient state management
- ✅ **P2P Network** - libp2p protocol
- ✅ **JSON-RPC API** - Ethereum-compatible endpoints
- ✅ **Stake-based Validators** - Proof of Stake consensus

---

## 🏗️ Kiến trúc hệ thống

```
┌─────────────────────────────────────────────────────────────┐
│                      APPLICATION                            │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌────────────────┐  ┌────────────────┐  ┌──────────────┐ │
│  │  API Layer     │  │  Network Layer │  │  Processors  │ │
│  │  (JSON-RPC)    │  │  (P2P/Socket)  │  │              │ │
│  └────────┬───────┘  └────────┬───────┘  └──────┬───────┘ │
│           │                   │                  │          │
│           └───────────────────┴──────────────────┘          │
│                            ↓                                │
│  ┌──────────────────────────────────────────────────────┐  │
│  │              BLOCKCHAIN CORE                         │  │
│  ├──────────────────────────────────────────────────────┤  │
│  │  ChainState                                          │  │
│  │  ├─ AccountStateDB (Merkle Trie)                    │  │
│  │  ├─ StakeStateDB (Validators)                       │  │
│  │  └─ SmartContractDB (EVM)                           │  │
│  └──────────────────────────────────────────────────────┘  │
│                            ↓                                │
│  ┌──────────────────────────────────────────────────────┐  │
│  │           STORAGE MANAGER                            │  │
│  ├──────────────────────────────────────────────────────┤  │
│  │  10+ Specialized Databases (LevelDB/RemoteDB)       │  │
│  │  ├─ Account State Storage                           │  │
│  │  ├─ Transaction State Storage                       │  │
│  │  ├─ Block Storage                                   │  │
│  │  ├─ Stake Storage                                   │  │
│  │  ├─ Smart Contract Code Storage                     │  │
│  │  ├─ Smart Contract Storage                          │  │
│  │  ├─ Receipts Storage                                │  │
│  │  ├─ Trie Database                                   │  │
│  │  ├─ Mapping Storage                                 │  │
│  │  └─ Backup Storage                                  │  │
│  └──────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

---

## 🚀 Quy trình khởi tạo

### **Step 1: Load Configuration**

```go
// File: cmd/simple_chain/app.go
func NewApp(configPath string) *App {
    // Load config from config.json
    app.config, err = config.LoadConfig(configPath)
    
    // Config chứa:
    // - Node type: Master/Write/ReadOnly
    // - Database paths
    // - Network settings (P2P ports, addresses)
    // - Genesis file path
    // - Free fee addresses
}
```

**Config Example**:
```json
{
  "mode": "single_node",
  "chainId": 1000,
  "service_type": "MASTER",
  "genesis_file_path": "genesis.json",
  "connection_address": "0.0.0.0:4201",
  "rpc_port": ":8747",
  "Databases": {
    "RootPath": "./data",
    "DBEngine": "sharded"
  }
}
```

---

### **Step 2: Initialize Storage Manager**

```go
func (app *App) initStorageManager() error {
    app.storageManager = storage.NewStorageManager(
        app.config.Databases.RootPath,
        app.config.Databases,
    )
    
    // Khởi tạo 10+ databases:
    storageManager.GetStorageAccountState()      // Account balances, nonces
    storageManager.GetStorageTransactionState()  // Transaction receipts
    storageManager.GetStorageBlock()             // Blocks
    storageManager.GetStorageStake()             // Validators, delegations
    storageManager.GetStorageCode()              // Smart contract bytecode
    storageManager.GetStorageSmartContract()     // Contract storage
    storageManager.GetStorageReceipts()          // Tx execution logs
    storageManager.GetStorageTrie()              // Merkle tree nodes
    storageManager.GetStorageMapping()           // Address → Tx mapping
}
```

---

### **Step 3: Initialize Network**

```go
func (app *App) initNetwork() error {
    // 1. Load BLS Key Pair (cho validators)
    app.keyPair, _ = bls_service.LoadBLSKeyPair(app.config.Databases.BLSPrivateKey)
    
    // 2. Create libp2p Host Node
    app.node, _ = node.NewNode(
        app.config.Nodes.PrivateKey,
        app.config.Nodes.ListenPort,
    )
    
    // 3. Subscribe to P2P topics
    topicsToSubscribe := []string{
        common.BlockDataTopic,              // Nhận blocks từ master
        common.TransactionsFromSubTopic,    // Nhận transactions
        common.ReadTransactionsFromSubTopic,// Nhận read queries
    }
    
    // 4. Start Socket Server
    app.socketServer.Listen(app.config.ConnectionAddress)
}
```

---

### **Step 4: Initialize Blockchain**

```go
func (app *App) initBlockchain() error {
    blockDatabase := block.NewBlockDatabase(app.storageManager.GetStorageBlock())
    
    // Thử lấy last block từ database
    lastBlock, err := blockDatabase.GetLastBlock()
    
    if err != nil {
        // ❌ Không tìm thấy → Tạo GENESIS BLOCK
        app.initGenesisBlock(blockDatabase)
    } else {
        // ✅ Tìm thấy → Load existing blockchain
        app.startLastBlock = lastBlock
        
        // Khởi tạo ChainState từ last block
        app.chainState, _ = blockchain.NewChainState(
            app.storageManager,
            blockDatabase,
            *lastBlock.Header(),
            app.config,
            app.config.FreeFeeAddresses,
        )
    }
}
```

---

### **Step 5: Initialize Processors**

```go
func (app *App) initProcessors() {
    // Connection Processor - Handle peer connections
    app.connectionProcessor = connection_processor.NewConnectionProcessor(
        app.node,
        app.socketServer,
        app.storageManager,
        app.chainState,
    )
    
    // State Processor - Query blockchain state
    app.stateProcessor = state_processor.NewStateProcessor(
        app.storageManager,
        app.chainState,
    )
    
    // Transaction Processor - Process transactions
    app.transactionProcessor = transaction_processor.NewTransactionProcessor(
        app.storageManager,
        app.chainState,
        app.node,
    )
    
    // Block Processor - Generate/receive blocks
    app.blockProcessor = block_processor.NewBlockProcessor(
        app.storageManager,
        app.chainState,
        app.node,
        app.keyPair,
    )
    
    // Subscribe Processor - Event subscriptions
    app.subscribeProcessor = subscribe_processor.NewSubscribeProcessor(
        app.node,
    )
}
```

---

### **Step 6: Start Runtime**

```go
func (app *App) Run() {
    // Start các processors dựa trên node type
    
    if app.config.ServiceType == "MASTER" {
        // Master node: Generate blocks
        go app.blockProcessor.GenerateBlock()
        go app.transactionProcessor.ProcessTransactionsFromSub()
    }
    
    if app.config.ServiceType == "WRITE" || app.config.ServiceType == "MASTER" {
        // Write nodes: Process incoming transactions
        go app.blockProcessor.TxsProcessor()
    }
    
    // All nodes: Process read queries
    go app.blockProcessor.TxsProcessor2()
    
    // Connect to other nodes
    app.connectToNodes()
}
```

---

## 🏛️ Các loại Node

### **1️⃣ MASTER NODE** (`ServiceType = "MASTER"`)

**Chức năng chính**:
- ✅ **Generate blocks** mới mỗi N giây (block time)
- ✅ **Process transactions** từ transaction pool
- ✅ **Execute smart contracts** và update state
- ✅ **Broadcast blocks** đến các sub nodes
- ✅ **Validate validator signatures** (BLS consensus)
- ✅ **Handle read/write queries**

**Goroutines chạy**:
```go
go app.blockProcessor.GenerateBlock()           // Tạo block mới
go app.transactionProcessor.ProcessTransactionsFromSub() // Xử lý txs
go app.blockProcessor.TxsProcessor()            // Xử lý write queries
go app.blockProcessor.TxsProcessor2()           // Xử lý read queries
go app.blockProcessor.ProcessorPool()           // Background jobs
```

**Kết nối**:
- Không kết nối tới node nào khác (standalone hoặc accept connections)
- Các sub nodes kết nối tới master

---

### **2️⃣ WRITE NODE** (`ServiceType = "WRITE"`)

**Chức năng chính**:
- ✅ **Receive transactions** từ clients
- ✅ **Validate transactions** locally
- ✅ **Forward transactions** to master node
- ✅ **Sync blocks** from master
- ✅ **Answer read queries** using local state
- ❌ **KHÔNG generate blocks** (chỉ master mới generate)

**Goroutines chạy**:
```go
go app.ConnectTo(masterAddress, MASTER_CONNECTION_TYPE, true)
go app.blockProcessor.TxsProcessor()            // Xử lý write queries
go app.blockProcessor.TxsProcessor2()           // Xử lý read queries
```

**Kết nối**:
- Kết nối tới Master node (nhận blocks + forward txs)
- Clients kết nối tới Write node (gửi transactions)

---

### **3️⃣ READ-ONLY NODE** (`ServiceType = "READONLY"`)

**Chức năng chính**:
- ✅ **Sync blocks** from master (read-only)
- ✅ **Answer read queries**: `eth_call`, `eth_getBalance`, `eth_getLogs`
- ❌ **KHÔNG nhận transactions**
- ❌ **KHÔNG generate blocks**
- ❌ **KHÔNG forward transactions**

**Goroutines chạy**:
```go
go app.ConnectTo(masterReadOnlyAddress, MASTER_READ_ONLY_CONNECTION_TYPE, false)
go app.blockProcessor.TxsProcessor2()           // CHỈ xử lý read queries
```

**Kết nối**:
- Kết nối tới Master node (read-only endpoint)
- Clients kết nối tới Read node (chỉ queries)

---

## 💾 Storage Architecture

### **Database List**

| **Database** | **Chức năng** | **Storage Location** |
|-------------|---------------|---------------------|
| **AccountState** | Lưu trữ balance, nonce của accounts | `/account_state/` |
| **TransactionState** | Lưu trữ receipts, status của transactions | `/transaction_state/` |
| **Blocks** | Lưu trữ block headers + bodies | `/blocks/` |
| **Stake** | Lưu trữ validators, delegations | `/stake_db/` |
| **SmartContractCode** | Lưu trữ bytecode của contracts | `/smart_contract_code/` |
| **SmartContractStorage** | Lưu trữ storage variables của contracts | `/smart_contract_storage/` |
| **Receipts** | Lưu trữ logs, events từ transactions | `/receipts/` |
| **Trie** | Lưu trữ Merkle Patricia Trie nodes | `/trie_database/` |
| **Mapping** | Lưu trữ Address → Transaction history | `/mapping/` |
| **Backup** | Lưu trữ backups | `/backup_db/` |

### **Database Engine Options**

```go
// Config.Databases.DBEngine
"sharded"  // Default - Mỗi database riêng biệt
"remote"   // Database ở remote server (network DB)
```

---

## 📡 Network Communication

### **P2P Topics (libp2p)**

```go
const (
    BlockDataTopic              = "/metanode/blocks/1.0.0"
    TransactionsFromSubTopic    = "/metanode/transactions/1.0.0"
    ReadTransactionsFromSubTopic = "/metanode/read-transactions/1.0.0"
)
```

**Workflow**:
```
Master Node                Write Node              Read Node
    │                          │                       │
    │◄──── Subscribe ──────────┤                       │
    │◄──── Subscribe ───────────────────────────────────┤
    │                          │                       │
    │──── Publish Block ──────►│                       │
    │──── Publish Block ────────────────────────────────►│
    │                          │                       │
    │◄─── Forward Tx ──────────┤                       │
    │                          │                       │
```

### **Socket Server (Custom Protocol)**

```go
// Listen address
app.config.ConnectionAddress = "0.0.0.0:4201"

// Socket connections for:
// - Direct peer connections
// - Client connections
// - Inter-node communication
```

---

## 🔄 Transaction Processing

### **Flow: User gửi transaction**

```
┌─────────────────────────────────────────────────────────────┐
│  1. Client → Write Node                                     │
│     POST /                                                  │
│     {                                                       │
│       "jsonrpc": "2.0",                                     │
│       "method": "eth_sendRawTransaction",                   │
│       "params": ["0x...signed_tx..."]                       │
│     }                                                       │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│  2. Write Node validates                                    │
│     ├─ Decode RLP transaction                              │
│     ├─ Verify signature                                    │
│     ├─ Check nonce (must be current_nonce + 1)             │
│     ├─ Check balance (must have enough for value + gas)    │
│     └─ Add to local transaction pool                       │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│  3. Write Node → Master Node                                │
│     Forward via P2P (TransactionsFromSubTopic)              │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│  4. Master Node processes                                   │
│     ├─ Collect transactions from pool                      │
│     ├─ Generate new block                                  │
│     ├─ Execute transactions:                               │
│     │   ├─ Deduct gas from sender                          │
│     │   ├─ Transfer value to recipient                     │
│     │   ├─ Execute smart contract (if contract call)       │
│     │   ├─ Update state (balances, nonces, storage)        │
│     │   └─ Generate receipt (logs, status)                 │
│     ├─ Calculate state roots:                              │
│     │   ├─ AccountStatesRoot (Merkle root)                 │
│     │   ├─ StakeStatesRoot                                 │
│     │   ├─ TransactionsRoot                                │
│     │   └─ ReceiptsRoot                                    │
│     ├─ Sign block with BLS signature                       │
│     ├─ Commit state to database                            │
│     └─ Broadcast block via P2P                             │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│  5. All Nodes receive block                                 │
│     ├─ Validate block:                                     │
│     │   ├─ Verify block hash                               │
│     │   ├─ Verify BLS signature                            │
│     │   ├─ Verify state roots                              │
│     │   └─ Verify transactions                             │
│     ├─ Apply state changes                                 │
│     ├─ Store block in local database                       │
│     └─ Update lastBlock pointer                            │
└─────────────────────────────────────────────────────────────┘
```

---

## 🧱 Block Generation

### **Master Node Block Generation Loop**

```go
func (bp *BlockProcessor) GenerateBlock() {
    ticker := time.NewTicker(time.Second * time.Duration(bp.blockTime))
    
    for {
        select {
        case <-ticker.C:
            // 1. Get transactions from pool
            txs := bp.transactionPool.GetTransactions(maxTxsPerBlock)
            
            // 2. Create new block
            newBlock := block.NewBlock(
                block.NewBlockHeader(
                    lastBlock.Hash(),           // Previous block hash
                    lastBlock.Number() + 1,     // Block number
                    time.Now().Unix(),          // Timestamp
                    ...
                ),
                txs,                            // Transactions
                nil,                            // Validator votes
            )
            
            // 3. Execute transactions
            receipts := []types.Receipt{}
            for _, tx := range txs {
                receipt, err := bp.ExecuteTransaction(tx)
                receipts = append(receipts, receipt)
            }
            
            // 4. Calculate state roots
            accountRoot := bp.chainState.GetAccountStateDB().IntermediateRoot()
            stakeRoot := bp.chainState.GetStakeStateDB().IntermediateRoot()
            txRoot := CalculateMerkleRoot(txs)
            receiptRoot := CalculateMerkleRoot(receipts)
            
            // 5. Update block header
            newBlock.Header().SetAccountStatesRoot(accountRoot)
            newBlock.Header().SetStakeStatesRoot(stakeRoot)
            newBlock.Header().SetTransactionsRoot(txRoot)
            newBlock.Header().SetReceiptsRoot(receiptRoot)
            
            // 6. Sign block with BLS
            signature := bp.keyPair.Sign(newBlock.Hash())
            newBlock.SetValidatorSignature(signature)
            
            // 7. Commit state
            bp.chainState.Commit()
            
            // 8. Save block
            bp.blockDatabase.SaveBlock(newBlock)
            bp.blockDatabase.SaveLastBlock(newBlock)
            
            // 9. Broadcast block
            bp.node.BroadcastBlock(newBlock)
        }
    }
}
```

---

## 🌱 Genesis Block

### **Genesis Block Creation**

**File: `cmd/simple_chain/app.go` - Function `initGenesisBlock()`**

```go
func (app *App) initGenesisBlock(blockDatabase *block.BlockDatabase) error {
    // 1. Load genesis data from JSON file
    app.genesis, err = config.LoadGenesisData(app.config.GenesisFilePath)
    
    // 2. Create empty genesis block
    app.startLastBlock = block.NewBlock(
        block.NewBlockHeader(
            e_common.Hash{},           // No previous block
            0,                          // Block number = 0
            trie.EmptyRootHash,         // Empty account root
            e_common.Hash{},            // Empty tx root
            e_common.Hash{},            // Empty receipt root
            e_common.Address{},         // No miner
            0,                          // Timestamp = 0
            trie.EmptyRootHash,         // Empty stake root
        ),
        nil,                            // No transactions
        nil,                            // No votes
    )
    
    // 3. ✅ CỘNG TIỀN CHO TẤT CẢ ACCOUNTS
    addressMap := make(map[e_common.Address]bool)
    for _, account := range app.genesis.Alloc {
        a := account.ToAccountState()
        
        // Check duplicate
        if _, exists := addressMap[a.Address()]; exists {
            panic("duplicate address in genesis allocation")
        }
        addressMap[a.Address()] = true
        
        // Tăng nonce
        a.PlusOneNonce()
        
        // ✅ LƯU ACCOUNT VÀO DATABASE (Cộng tiền)
        app.chainState.GetAccountStateDB().SetState(a)
    }
    
    // 4. Commit account state
    app.chainState.GetAccountStateDB().IntermediateRoot(true)
    hash, err := app.chainState.GetAccountStateDB().Commit()
    
    // 5. Set account state root vào header
    app.startLastBlock.Header().SetAccountStatesRoot(hash)
    
    // 6. ✅ TẠO VALIDATORS
    for _, validator := range app.genesis.Validators {
        v := validator.ToValidator()
        
        // Lưu validator vào stake DB
        app.chainState.GetStakeStateDB().SetValidator(v)
    }
    
    // 7. Commit stake state
    hashStake, _ := app.chainState.GetStakeStateDB().IntermediateRoot(true)
    app.chainState.GetStakeStateDB().Commit()
    
    // 8. Set stake state root vào header
    app.startLastBlock.Header().SetStakeStatesRoot(hashStake)
    
    // 9. Save genesis block
    blockDatabase.SaveLastBlock(app.startLastBlock)
    
    return nil
}
```

### **Genesis File Format** (`genesis.json`)

```json
{
  "chainId": 1000,
  "alloc": {
    "0x742d35Cc6634C0532925a3b844Bc9e7595f0bEb": {
      "balance": "1000000000000000000000"
    },
    "0xEa004b9aE1F60516210df2fDfcE9342618729d98": {
      "balance": "5000000000000000000000"
    }
  },
  "validators": [
    {
      "net_address": "0.0.0.0:4201",
      "name": "Validator 1",
      "voting_power": 10,
      "pub_key": "0x123abc...",
      "proposer_priority": 0
    }
  ]
}
```

---

## 🌐 API Endpoints

### **JSON-RPC Methods**

| **Method** | **Chức năng** | **Node hỗ trợ** |
|-----------|---------------|----------------|
| `eth_call` | Execute smart contract (read-only) | All |
| `eth_sendRawTransaction` | Gửi transaction | Write, Master |
| `eth_getBalance` | Query balance của address | All |
| `eth_getTransactionCount` | Query nonce của address | All |
| `eth_getTransactionReceipt` | Query receipt của transaction | All |
| `eth_getBlockByNumber` | Query block theo số | All |
| `eth_getBlockByHash` | Query block theo hash | All |
| `eth_getLogs` | Query events/logs | All |
| `eth_getCode` | Query bytecode của contract | All |
| `eth_getStorageAt` | Query storage của contract | All |

### **Custom Methods**

| **Method** | **Chức năng** |
|-----------|---------------|
| `meta_getValidators` | Lấy danh sách validators |
| `meta_getStakeInfo` | Lấy thông tin stake của address |
| `meta_getChainConfig` | Lấy config của blockchain |

---

## 🔐 Security Features

### **1. BLS Signatures**
```go
// Validators ký blocks bằng BLS signature
signature := validator.keyPair.Sign(block.Hash())
block.SetValidatorSignature(signature)

// Verify signature
isValid := bls.Verify(block.Hash(), signature, validator.PublicKey)
```

### **2. Merkle Proofs**
```go
// State roots trong block header
type BlockHeader struct {
    AccountStatesRoot  Hash  // Merkle root của account trie
    StakeStatesRoot    Hash  // Merkle root của stake trie
    TransactionsRoot   Hash  // Merkle root của transactions
    ReceiptsRoot       Hash  // Merkle root của receipts
}

// Verify account tồn tại mà không cần toàn bộ state
proof := trie.GetProof(address)
isValid := trie.VerifyProof(accountStatesRoot, address, proof)
```

### **3. Nonce Protection**
```go
// Mỗi transaction phải có nonce = current_nonce + 1
currentNonce := accountState.Nonce()
if tx.Nonce() != currentNonce {
    return errors.New("invalid nonce")
}
```

### **4. Gas Limits**
```go
// Mỗi transaction có gas limit để prevent DOS
if tx.Gas() > block.GasLimit() {
    return errors.New("gas limit exceeded")
}
```

### **5. Free Fee Addresses**
```go
// Whitelist addresses không phải trả gas fee
freeFeeAddresses := []common.Address{
    common.HexToAddress("0x742d35Cc..."),
    ...
}

if isFreeAddress(tx.From(), freeFeeAddresses) {
    tx.SetGasPrice(0)
}
```

---

## 📊 Performance Optimizations

### **1. Read Transaction Limiter**
```go
// Giới hạn số lượng read queries đồng thời
readTxLimiter := make(chan struct{}, 100)  // Max 100 concurrent reads

readTxLimiter <- struct{}{}  // Acquire
defer func() { <-readTxLimiter }()  // Release
```

### **2. Prefetch for Merkle Trie**
```go
// Prefetch trie nodes để giảm I/O
trie.New(root, storage, true)  // enablePrefetch = true
```

### **3. Batch Database Writes**
```go
// Batch writes để giảm I/O operations
batch := [][2][]byte{}
batch = append(batch, [2][]byte{key1, value1})
batch = append(batch, [2][]byte{key2, value2})
db.BatchPut(batch)
```

### **4. Connection Pooling**
```go
// Reuse connections để giảm overhead
type ConnectionPool struct {
    connections []*Connection
    maxSize     int
}
```

---

## 🐛 Common Issues

### **Issue 1: "protobuf tag not enough fields"**

**Nguyên nhân**: Proto definitions không đồng bộ giữa Go và Rust

**Giải pháp**:
```bash
# Regenerate proto
cd pkg/proto
protoc --go_out=. validator.proto

# Hoặc copy từ Go sang Rust
cp pkg/proto/validator.proto socket-rust/socket/proto/
cd socket-rust/socket
cargo clean && cargo build
```

### **Issue 2: "Address already in use"**

**Nguyên nhân**: Socket file vẫn tồn tại từ lần chạy trước

**Giải pháp**:
```bash
rm -f /tmp/rust-go.sock_1 /tmp/rust-go.sock_2
```

### **Issue 3: Genesis file quá lớn**

**Nguyên nhân**: File genesis.json > 50MB

**Giải pháp**:
- Split genesis data thành nhiều files
- Load incrementally
- Sử dụng compression

---

## 📚 References

### **Key Files**

| **File** | **Chức năng** |
|---------|---------------|
| `cmd/simple_chain/app.go` | Main application entry point |
| `cmd/simple_chain/main.go` | Binary entry point |
| `pkg/blockchain/chain_state.go` | ChainState implementation |
| `pkg/block/block.go` | Block structure |
| `pkg/account_state_db/account_state_db.go` | Account state management |
| `pkg/stake_state_db/stake_state_db.go` | Validator stake management |
| `executor/unitSokcet.go` | Unix socket integration |

### **Dependencies**

```go
require (
    github.com/ethereum/go-ethereum  // EVM implementation
    github.com/libp2p/go-libp2p      // P2P networking
    github.com/gogo/protobuf         // Protocol buffers
    github.com/syndtr/goleveldb      // LevelDB storage
    // ... và nhiều libraries khác
)
```

---

## 🎯 Summary

**Meta Node Blockchain App** là một:

✅ **Full-featured blockchain node** với EVM compatibility  
✅ **Multi-node architecture** (Master/Write/ReadOnly)  
✅ **BLS consensus** với stake-based validators  
✅ **Merkle Patricia Trie** cho efficient state management  
✅ **10+ specialized databases** cho different data types  
✅ **P2P network** qua libp2p + custom socket protocol  
✅ **JSON-RPC API** tương thích Ethereum  
✅ **Security features**: BLS signatures, Merkle proofs, nonce protection  

**Use Cases**:
- Private blockchain networks
- Consortium blockchains
- Test networks
- Development environments

---

**Last Updated**: October 17, 2025  
**Version**: 0.0.1.0  
**Author**: Meta Node Team
