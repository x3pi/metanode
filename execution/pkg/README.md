# Mục lục:
- [Tài liệu cho `account_state_db.go`](#tài-liệu-cho-account_state_dbgo)
- [Tài liệu cho `block.go`](#tài-liệu-cho-blockgo)
- [Tài liệu cho `block_header.go`](#tài-liệu-cho-block_headergo)
- [Tài liệu cho `confirmed_block_data.go`](#tài-liệu-cho-confirmed_block_datago)
- [Tài liệu cho `bls.go`](#tài-liệu-cho-blsgo)
- [Tài liệu cho `key_pair.go`](#tài-liệu-cho-key_pairgo)
- [Tài liệu cho `checkpoint.go`](#tài-liệu-cho-checkpointgo)
- [Tài liệu cho `execute_sc_grouper.go`](#tài-liệu-cho-execute_sc_groupergo)
- [Tài liệu cho `monitor.go`](#tài-liệu-cho-monitorgo)
- [Tài liệu cho `types.go`](#tài-liệu-cho-typesgo)
- [Tài liệu cho `helpers.go`](#tài-liệu-cho-helpersgo)
- [Tài liệu cho `extension.go`](#tài-liệu-cho-extensiongo)
- [Tài liệu cho `mvm_api.go`](#tài-liệu-cho-mvm_apigo)
- [Tài liệu cho `connection.go`](#tài-liệu-cho-connectiongo)
- [Tài liệu cho `connections_manager.go`](#tài-liệu-cho-connections_managergo)
- [Tài liệu cho `message.go`](#tài-liệu-cho-messagego)
- [Tài liệu cho `message_sender.go`](#tài-liệu-cho-message_sendergo)
- [Tài liệu cho `server.go`](#tài-liệu-cho-servergo)
- [Tài liệu cho `nodes_state.go`](#tài-liệu-cho-nodes_statego)
- [Tài liệu cho `pack.go`](#tài-liệu-cho-packgo)
- [Tài liệu cho `packs_from_leader.go`](#tài-liệu-cho-packs_from_leadergo)
- [Tài liệu cho `verify_pack_sign.go`](#tài-liệu-cho-verify_pack_signgo)
- [Tài liệu cho `pack_pool.go`](#tài-liệu-cho-pack_poolgo)
- [Tài liệu cho `receipt.go`](#tài-liệu-cho-receiptgo)
- [Tài liệu cho `receipts.go`](#tài-liệu-cho-receiptsgo)
- [Tài liệu cho `remote_storage_db.go`](#tài-liệu-cho-remote_storage_dbgo)
- [Tài liệu cho `event_log.go`](#tài-liệu-cho-event_loggo)
- [Tài liệu cho `event_logs.go`](#tài-liệu-cho-event_logsgo)
- [Tài liệu cho `execute_sc_result.go`](#tài-liệu-cho-execute_sc_resultgo)
- [Tài liệu cho `execute_sc_results.go`](#tài-liệu-cho-execute_sc_resultsgo)
- [Tài liệu cho `smart_contract_update_data.go`](#tài-liệu-cho-smart_contract_update_datago)
- [Tài liệu cho `smart_contract_update_datas.go`](#tài-liệu-cho-smart_contract_update_datasgo)
- [Tài liệu cho `touched_addresses_data.go`](#tài-liệu-cho-touched_addresses_datago)
- [Tài liệu cho `smart_contract_db.go`](#tài-liệu-cho-smart_contract_dbgo)
- [Tài liệu cho `stake_abi.go`](#tài-liệu-cho-stake_abigo)
- [Tài liệu cho `stake_getter.go`](#tài-liệu-cho-stake_gettergo)
- [Tài liệu cho `stake_info.go`](#tài-liệu-cho-stake_infogo)
- [Tài liệu cho `stake_smart_contract_db.go`](#tài-liệu-cho-stake_smart_contract_dbgo)
- [Tài liệu cho `account_state.go`](#tài-liệu-cho-account_statego)
- [Tài liệu cho `smart_contract_state.go`](#tài-liệu-cho-smart_contract_statego)
- [Tài liệu cho `update_state_fields.go`](#tài-liệu-cho-update_state_fieldsgo)
- [Tài liệu cho `stats.go`](#tài-liệu-cho-statsgo)
- [Tài liệu cho `badger_db.go`](#tài-liệu-cho-badger_dbgo)
- [Tài liệu cho `leveldb.go`](#tài-liệu-cho-leveldbgo)
- [Tài liệu cho `memorydb.go`](#tài-liệu-cho-memorydbgo)
- [Tài liệu cho `node_sync_data.go`](#tài-liệu-cho-node_sync_datago)
- [Tài liệu cho `call_data.go`](#tài-liệu-cho-call_datago)
- [Tài liệu cho `deploy_data.go`](#tài-liệu-cho-deploy_datago)
- [Tài liệu cho `from_node_transaction_result.go`](#tài-liệu-cho-from_node_transaction_resultgo)
- [Tài liệu cho `to_node_transaction_result.go`](#tài-liệu-cho-to_node_transaction_resultgo)
- [Tài liệu cho `open_state_channel_data.go`](#tài-liệu-cho-open_state_channel_datago)
- [Tài liệu cho `execute_sc_transactions.go`](#tài-liệu-cho-execute_sc_transactionsgo)
- [Tài liệu cho `transactions_from_leader.go`](#tài-liệu-cho-transactions_from_leadergo)
- [Tài liệu cho `verify_transaction_sign.go`](#tài-liệu-cho-verify_transaction_signgo)
- [Tài liệu cho `update_storage_host_data.go`](#tài-liệu-cho-update_storage_host_datago)
- [Tài liệu cho `transaction.go`](#tài-liệu-cho-transactiongo)
- [Tài liệu cho `transaction_grouper.go`](#tài-liệu-cho-transaction_groupergo)
- [Tài liệu cho `transaction_pool.go`](#tài-liệu-cho-transaction_poolgo)
- [Tài liệu cho `committer.go`](#tài-liệu-cho-committergo)
- [Tài liệu cho `hasher.go`](#tài-liệu-cho-hashergo)
- [Tài liệu cho `trie_reader.go`](#tài-liệu-cho-trie_readergo)
- [Tài liệu cho `trie.go`](#tài-liệu-cho-triego)
- [Tài liệu cho `block_vote.go`](#tài-liệu-cho-block_votego)
- [Tài liệu cho `vote_pool.go`](#tài-liệu-cho-vote_poolgo)

# Tài liệu cho [`account_state_db.go`](./account_state_db/account_state_db.go)

## Giới thiệu

File `account_state_db.go` định nghĩa một cấu trúc dữ liệu `AccountStateDB` để quản lý trạng thái tài khoản trong một blockchain. Nó sử dụng một cây Merkle Patricia Trie để lưu trữ trạng thái và cung cấp các phương thức để thao tác với trạng thái tài khoản.

## Cấu trúc `AccountStateDB`

### Thuộc tính

- `trie`: Một con trỏ đến `MerklePatriciaTrie`, được sử dụng để lưu trữ trạng thái tài khoản.
- `originRootHash`: Hash gốc ban đầu của trie.
- `db`: Một đối tượng `storage.Storage` để lưu trữ dữ liệu.
- `dirtyAccounts`: Một map lưu trữ các tài khoản đã bị thay đổi nhưng chưa được commit.
- `mu`: Một mutex để đảm bảo tính đồng bộ khi truy cập dữ liệu.

### Hàm khởi tạo

- `NewAccountStateDB(trie *p_trie.MerklePatriciaTrie, db storage.Storage) *AccountStateDB`: Tạo một đối tượng `AccountStateDB` mới với trie và cơ sở dữ liệu được cung cấp.

### Các phương thức

- `AccountState(address common.Address) (types.AccountState, error)`: Lấy trạng thái tài khoản cho một địa chỉ cụ thể.
- `SubPendingBalance(address common.Address, amount *big.Int) error`: Trừ một số tiền từ số dư đang chờ xử lý của tài khoản.
- `AddPendingBalance(address common.Address, amount *big.Int)`: Thêm một số tiền vào số dư đang chờ xử lý của tài khoản.
- `AddBalance(address common.Address, amount *big.Int)`: Thêm một số tiền vào số dư của tài khoản.
- `SubBalance(address common.Address, amount *big.Int) error`: Trừ một số tiền từ số dư của tài khoản.
- `SubTotalBalance(address common.Address, amount *big.Int) error`: Trừ một số tiền từ tổng số dư của tài khoản.
- `SetLastHash(address common.Address, hash common.Hash)`: Đặt hash cuối cùng cho tài khoản.
- `SetNewDeviceKey(address common.Address, newDeviceKey common.Hash)`: Đặt khóa thiết bị mới cho tài khoản.
- `SetState(as types.AccountState)`: Đặt trạng thái cho tài khoản.
- `SetCreatorPublicKey(address common.Address, creatorPublicKey p_common.PublicKey)`: Đặt khóa công khai của người tạo cho tài khoản.
- `SetCodeHash(address common.Address, codeHash common.Hash)`: Đặt hash mã cho tài khoản.
- `SetStorageRoot(address common.Address, storageRoot common.Hash)`: Đặt root lưu trữ cho tài khoản.
- `SetStorageAddress(address common.Address, storageAddress common.Address)`: Đặt địa chỉ lưu trữ cho tài khoản.
- `AddLogHash(address common.Address, logsHash common.Hash)`: Thêm hash log cho tài khoản.
- `Discard() (err error)`: Hủy bỏ các thay đổi chưa được commit.
- `Commit() (common.Hash, error)`: Commit các thay đổi vào trie và trả về hash gốc mới.
- `IntermediateRoot() (common.Hash, error)`: Cập nhật các tài khoản đã thay đổi vào trie và trả về hash gốc trung gian.
- `setDirtyAccountState(as types.AccountState)`: Đánh dấu trạng thái tài khoản là đã thay đổi.
- `getOrCreateAccountState(address common.Address) (types.AccountState, error)`: Lấy hoặc tạo trạng thái tài khoản cho một địa chỉ cụ thể.
- `Storage() storage.Storage`: Trả về đối tượng lưu trữ.
- `CopyFrom(as types.AccountStateDB) error`: Sao chép trạng thái từ một `AccountStateDB` khác.

## Kết luận

File `account_state_db.go` cung cấp một cách tiếp cận có cấu trúc để quản lý trạng thái tài khoản trong một blockchain. Nó sử dụng các kỹ thuật đồng bộ hóa để đảm bảo tính nhất quán của dữ liệu và cung cấp nhiều phương thức để thao tác với trạng thái tài khoản.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`block.go`](./block/block.go)

## Giới thiệu

File `block.go` định nghĩa cấu trúc `Block` và các phương thức liên quan để quản lý các block trong blockchain. Mỗi block bao gồm một tiêu đề, danh sách các giao dịch và kết quả thực thi smart contract.

## Cấu trúc `Block`

### Thuộc tính

- `header`: Thuộc tính kiểu `types.BlockHeader`, lưu trữ thông tin tiêu đề của block.
- `transactions`: Một slice chứa các giao dịch thuộc kiểu `types.Transaction`.
- `executeSCResults`: Một slice chứa kết quả thực thi smart contract thuộc kiểu `types.ExecuteSCResult`.

### Hàm khởi tạo

- `NewBlock(header types.BlockHeader, transactions []types.Transaction, executeSCResults []types.ExecuteSCResult) *Block`: Tạo một đối tượng `Block` mới với tiêu đề, danh sách giao dịch và kết quả thực thi smart contract được cung cấp.

### Các phương thức

- `Header() types.BlockHeader`: Trả về tiêu đề của block.
- `Transactions() []types.Transaction`: Trả về danh sách các giao dịch của block.
- `ExecuteSCResults() []types.ExecuteSCResult`: Trả về danh sách kết quả thực thi smart contract của block.
- `Proto() *pb.Block`: Chuyển đổi block thành đối tượng `pb.Block` để sử dụng với Protobuf.
- `FromProto(pbBlock *pb.Block)`: Khởi tạo block từ một đối tượng `pb.Block`.
- `Marshal() ([]byte, error)`: Chuyển đổi block thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(bData []byte) error`: Khởi tạo block từ một slice byte đã được mã hóa.

## Kết luận

File `block.go` cung cấp các phương thức cần thiết để tạo, quản lý và chuyển đổi các block trong blockchain. Nó sử dụng Protobuf để mã hóa và giải mã dữ liệu, giúp dễ dàng lưu trữ và truyền tải các block.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`block_header.go`](./block/block_header.go)

## Giới thiệu

File `block_header.go` định nghĩa cấu trúc `BlockHeader` và các phương thức liên quan để quản lý tiêu đề của các block trong blockchain. Tiêu đề block chứa thông tin quan trọng như hash của block cuối cùng, số block, root trạng thái tài khoản, root biên nhận, địa chỉ leader, thời gian và chữ ký tổng hợp.

## Cấu trúc `BlockHeader`

### Thuộc tính

- `lastBlockHash`: Hash của block cuối cùng thuộc kiểu `common.Hash`.
- `blockNumber`: Số thứ tự của block thuộc kiểu `uint64`.
- `accountStatesRoot`: Hash của trạng thái tài khoản thuộc kiểu `common.Hash`.
- `receiptRoot`: Hash của biên nhận thuộc kiểu `common.Hash`.
- `leaderAddress`: Địa chỉ của leader thuộc kiểu `common.Address`.
- `timeStamp`: Thời gian tạo block thuộc kiểu `uint64`.
- `aggregateSignature`: Chữ ký tổng hợp thuộc kiểu `[]byte`.

### Hàm khởi tạo

- `NewBlockHeader(lastBlockHash common.Hash, blockNumber uint64, accountStatesRoot common.Hash, receiptRoot common.Hash, leaderAddress common.Address, timeStamp uint64) *BlockHeader`: Tạo một đối tượng `BlockHeader` mới với các thông tin được cung cấp.

### Các phương thức

- `LastBlockHash() common.Hash`: Trả về hash của block cuối cùng.
- `BlockNumber() uint64`: Trả về số thứ tự của block.
- `AccountStatesRoot() common.Hash`: Trả về hash của trạng thái tài khoản.
- `ReceiptRoot() common.Hash`: Trả về hash của biên nhận.
- `LeaderAddress() common.Address`: Trả về địa chỉ của leader.
- `TimeStamp() uint64`: Trả về thời gian tạo block.
- `AggregateSignature() []byte`: Trả về chữ ký tổng hợp.
- `Marshal() ([]byte, error)`: Chuyển đổi `BlockHeader` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(bData []byte) error`: Khởi tạo `BlockHeader` từ một slice byte đã được mã hóa.
- `Hash() common.Hash`: Tính toán và trả về hash của `BlockHeader`.
- `Proto() *pb.BlockHeader`: Chuyển đổi `BlockHeader` thành đối tượng `pb.BlockHeader` để sử dụng với Protobuf.
- `FromProto(pbBlockHeader *pb.BlockHeader)`: Khởi tạo `BlockHeader` từ một đối tượng `pb.BlockHeader`.
- `String() string`: Trả về chuỗi mô tả của `BlockHeader`.

## Kết luận

File `block_header.go` cung cấp các phương thức cần thiết để tạo, quản lý và chuyển đổi tiêu đề của các block trong blockchain. Nó sử dụng Protobuf để mã hóa và giải mã dữ liệu, giúp dễ dàng lưu trữ và truyền tải các tiêu đề block.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`confirmed_block_data.go`](./block/confirmed_block_data.go)

## Giới thiệu

File `confirmed_block_data.go` định nghĩa cấu trúc `ConfirmedBlockData` và các phương thức liên quan để quản lý dữ liệu của các block đã được xác nhận trong blockchain. Nó bao gồm thông tin tiêu đề, biên nhận, root trạng thái nhánh và chữ ký của các validator.

## Cấu trúc `ConfirmedBlockData`

### Thuộc tính

- `header`: Thuộc tính kiểu `types.BlockHeader`, lưu trữ thông tin tiêu đề của block.
- `receipts`: Một slice chứa các biên nhận thuộc kiểu `types.Receipt`.
- `branchStateRoot`: Hash của trạng thái nhánh thuộc kiểu `e_common.Hash`.
- `validatorSigns`: Map lưu trữ chữ ký của các validator, với địa chỉ là key và chữ ký là value.

### Hàm khởi tạo

- `NewConfirmedBlockData(header types.BlockHeader, receipts []types.Receipt, branchStateRoot e_common.Hash, validatorSigns map[e_common.Address][]byte) *ConfirmedBlockData`: Tạo một đối tượng `ConfirmedBlockData` mới với tiêu đề, biên nhận, root trạng thái nhánh và chữ ký của các validator được cung cấp.

### Các phương thức

- `Header() types.BlockHeader`: Trả về tiêu đề của block.
- `Receipts() []types.Receipt`: Trả về danh sách biên nhận của block.
- `BranchStateRoot() e_common.Hash`: Trả về hash của trạng thái nhánh.
- `ValidatorSigns() map[e_common.Address][]byte`: Trả về map chữ ký của các validator.
- `SetHeader(header types.BlockHeader)`: Đặt tiêu đề cho block.
- `SetBranchStateRoot(rootHash e_common.Hash)`: Đặt root trạng thái nhánh.
- `Proto() *pb.ConfirmedBlockData`: Chuyển đổi `ConfirmedBlockData` thành đối tượng `pb.ConfirmedBlockData` để sử dụng với Protobuf.
- `FromProto(pbData *pb.ConfirmedBlockData)`: Khởi tạo `ConfirmedBlockData` từ một đối tượng `pb.ConfirmedBlockData`.
- `Marshal() ([]byte, error)`: Chuyển đổi `ConfirmedBlockData` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(cData []byte) error`: Khởi tạo `ConfirmedBlockData` từ một slice byte đã được mã hóa.
- `LoadConfirmedBlockDataFromFile(path string) (types.ConfirmedBlockData, error)`: Tải `ConfirmedBlockData` từ một file.
- `SaveConfirmedBlockDataToFile(cData types.ConfirmedBlockData, path string) error`: Lưu `ConfirmedBlockData` vào một file.

## Kết luận

File `confirmed_block_data.go` cung cấp các phương thức cần thiết để tạo, quản lý và chuyển đổi dữ liệu của các block đã được xác nhận trong blockchain. Nó sử dụng Protobuf để mã hóa và giải mã dữ liệu, giúp dễ dàng lưu trữ và truyền tải các block.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`bls.go`](./bls/bls.go)

## Giới thiệu

File `bls.go` cung cấp các hàm và cấu trúc để thực hiện các thao tác liên quan đến chữ ký BLS (Boneh-Lynn-Shacham) trong blockchain. Nó bao gồm các hàm để ký, xác minh chữ ký, tạo cặp khóa và xử lý chữ ký tổng hợp.

## Các hàm và cấu trúc

### Biến và kiểu dữ liệu

- `blstPublicKey`, `blstSignature`, `blstAggregateSignature`, `blstAggregatePublicKey`, `blstSecretKey`: Các kiểu dữ liệu đại diện cho khóa công khai, chữ ký, chữ ký tổng hợp, khóa công khai tổng hợp và khóa bí mật trong thư viện BLS.
- `dstMinPk`: Một slice byte được sử dụng làm domain separation tag cho chữ ký BLS.

### Hàm khởi tạo

- `Init()`: Khởi tạo thư viện BLS với số lượng luồng tối đa bằng số lượng CPU khả dụng.

### Các hàm

- `Sign(bPri cm.PrivateKey, bMessage []byte) cm.Sign`: Tạo chữ ký BLS từ khóa bí mật và thông điệp.
- `GetByteAddress(pubkey []byte) []byte`: Tính toán địa chỉ từ khóa công khai.
- `VerifySign(bPub cm.PublicKey, bSig cm.Sign, bMsg []byte) bool`: Xác minh chữ ký BLS với khóa công khai và thông điệp.
- `VerifyAggregateSign(bPubs [][]byte, bSig []byte, bMsgs [][]byte) bool`: Xác minh chữ ký tổng hợp BLS với danh sách khóa công khai và thông điệp.
- `GenerateKeyPairFromSecretKey(hexSecretKey string) (cm.PrivateKey, cm.PublicKey, common.Address)`: Tạo cặp khóa từ khóa bí mật dưới dạng chuỗi hex.
- `randBLSTSecretKey() *blstSecretKey`: Tạo ngẫu nhiên một khóa bí mật BLS.
- `GenerateKeyPair() *KeyPair`: Tạo ngẫu nhiên một cặp khóa BLS.
- `CreateAggregateSign(bSignatures [][]byte) []byte`: Tạo chữ ký tổng hợp từ danh sách chữ ký.

## Kết luận

File `bls.go` cung cấp các hàm cần thiết để thực hiện các thao tác liên quan đến chữ ký BLS trong blockchain. Nó sử dụng thư viện BLS để đảm bảo tính bảo mật và hiệu suất cao trong việc xử lý chữ ký.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`key_pair.go`](./bls/key_pair.go)

## Giới thiệu

File `key_pair.go` định nghĩa cấu trúc `KeyPair` và các phương thức liên quan để quản lý cặp khóa BLS (Boneh-Lynn-Shacham) trong blockchain. Cặp khóa bao gồm khóa công khai, khóa bí mật và địa chỉ tương ứng.

## Cấu trúc `KeyPair`

### Thuộc tính

- `publicKey`: Khóa công khai thuộc kiểu `cm.PublicKey`.
- `privateKey`: Khóa bí mật thuộc kiểu `cm.PrivateKey`.
- `address`: Địa chỉ thuộc kiểu `common.Address`.

### Hàm khởi tạo

- `NewKeyPair(privateKey []byte) *KeyPair`: Tạo một đối tượng `KeyPair` mới từ khóa bí mật được cung cấp. Khóa công khai và địa chỉ được tính toán từ khóa bí mật.

### Các phương thức

- `PrivateKey() cm.PrivateKey`: Trả về khóa bí mật của cặp khóa.
- `BytesPrivateKey() []byte`: Trả về khóa bí mật dưới dạng slice byte.
- `PublicKey() cm.PublicKey`: Trả về khóa công khai của cặp khóa.
- `BytesPublicKey() []byte`: Trả về khóa công khai dưới dạng slice byte.
- `Address() common.Address`: Trả về địa chỉ của cặp khóa.
- `String() string`: Trả về chuỗi mô tả của cặp khóa, bao gồm khóa bí mật, khóa công khai và địa chỉ dưới dạng chuỗi hex.

## Kết luận

File `key_pair.go` cung cấp các phương thức cần thiết để tạo, quản lý và truy xuất thông tin từ cặp khóa BLS trong blockchain. Nó sử dụng các thư viện mã hóa để đảm bảo tính bảo mật và chính xác của dữ liệu.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`checkpoint.go`](./checkpoint/checkpoint.go)

## Giới thiệu

File `checkpoint.go` định nghĩa cấu trúc `CheckPoint` và các phương thức liên quan để quản lý điểm kiểm tra (checkpoint) trong blockchain. Điểm kiểm tra bao gồm thông tin về block đầy đủ cuối cùng, lịch trình của leader hiện tại và lịch trình của leader tiếp theo.

## Cấu trúc `CheckPoint`

### Thuộc tính

- `lastFullBlock`: Thuộc tính kiểu `types.FullBlock`, lưu trữ thông tin về block đầy đủ cuối cùng.
- `thisLeaderSchedule`: Thuộc tính kiểu `types.LeaderSchedule`, lưu trữ lịch trình của leader hiện tại.
- `nextLeaderSchedule`: Thuộc tính kiểu `types.LeaderSchedule`, lưu trữ lịch trình của leader tiếp theo.

### Hàm khởi tạo

- `NewCheckPoint(lastFullBlock types.FullBlock, thisLeaderSchedule types.LeaderSchedule, nextLeaderSchedule types.LeaderSchedule) validator_types.Checkpoint`: Tạo một đối tượng `CheckPoint` mới với block đầy đủ cuối cùng, lịch trình của leader hiện tại và lịch trình của leader tiếp theo được cung cấp.

### Các phương thức

- `Proto() protoreflect.ProtoMessage`: Chuyển đổi `CheckPoint` thành đối tượng `pb.Checkpoint` để sử dụng với Protobuf.
- `Marshal() ([]byte, error)`: Chuyển đổi `CheckPoint` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(b []byte) error`: Khởi tạo `CheckPoint` từ một slice byte đã được mã hóa.
- `LastFullBlock() types.FullBlock`: Trả về block đầy đủ cuối cùng.
- `ThisLeaderSchedule() types.LeaderSchedule`: Trả về lịch trình của leader hiện tại.
- `NextLeaderSchedule() types.LeaderSchedule`: Trả về lịch trình của leader tiếp theo.
- `Save(savePath string) error`: Lưu `CheckPoint` vào một file tại đường dẫn được chỉ định.
- `Load(savePath string) error`: Tải `CheckPoint` từ một file tại đường dẫn được chỉ định.
- `AccountStatesManager(accountStatesDbPath string, dbType string) (types.AccountStatesManager, error)`: Tạo và trả về một `AccountStatesManager` từ đường dẫn cơ sở dữ liệu trạng thái tài khoản và loại cơ sở dữ liệu được chỉ định.

## Kết luận

File `checkpoint.go` cung cấp các phương thức cần thiết để tạo, quản lý và chuyển đổi điểm kiểm tra trong blockchain. Nó sử dụng Protobuf để mã hóa và giải mã dữ liệu, giúp dễ dàng lưu trữ và truyền tải các điểm kiểm tra.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`execute_sc_grouper.go`](./execute_sc_grouper/execute_sc_grouper.go)

## Giới thiệu

File `execute_sc_grouper.go` định nghĩa cấu trúc `ExecuteSmartContractsGrouper` và các phương thức liên quan để quản lý việc nhóm các giao dịch smart contract trong blockchain. Mục tiêu là tổ chức các giao dịch thành các nhóm dựa trên địa chỉ liên quan để thực thi hiệu quả hơn.

## Cấu trúc `ExecuteSmartContractsGrouper`

### Thuộc tính

- `groupCount`: Số lượng nhóm hiện tại thuộc kiểu `uint64`.
- `mapAddressGroup`: Map lưu trữ nhóm của từng địa chỉ, với địa chỉ là key và ID nhóm là value.
- `mapGroupExecuteTransactions`: Map lưu trữ các giao dịch của từng nhóm, với ID nhóm là key và danh sách giao dịch là value.

### Hàm khởi tạo

- `NewExecuteSmartContractsGrouper() *ExecuteSmartContractsGrouper`: Tạo một đối tượng `ExecuteSmartContractsGrouper` mới với các map trống và số lượng nhóm bằng 0.

### Các phương thức

- `AddTransactions(transactions []types.Transaction)`: Thêm các giao dịch vào các nhóm dựa trên địa chỉ liên quan.
- `GetGroupTransactions() map[uint64][]types.Transaction`: Trả về map các giao dịch của từng nhóm.
- `CountGroupWithTransactions() int`: Trả về số lượng nhóm có chứa giao dịch.
- `Clear()`: Xóa tất cả các nhóm và giao dịch, đặt lại số lượng nhóm về 0.
- `assignGroup(addresses []common.Address) uint64`: Gán các địa chỉ vào một nhóm và trả về ID nhóm.

## Kết luận

File `execute_sc_grouper.go` cung cấp các phương thức cần thiết để quản lý và nhóm các giao dịch smart contract trong blockchain. Việc nhóm các giao dịch giúp tối ưu hóa quá trình thực thi và quản lý các giao dịch liên quan đến cùng một địa chỉ.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`monitor.go`](./monitor_service/monitor.go)

## Giới thiệu

File `monitor.go` định nghĩa cấu trúc `MonitorService` và các phương thức liên quan để giám sát và thu thập thông tin hệ thống của một dịch vụ trong blockchain. Nó bao gồm việc thu thập thông tin như sử dụng bộ nhớ, sử dụng CPU, dung lượng đĩa, thời gian hoạt động của dịch vụ và kích thước log.

## Cấu trúc `SystemInfo`

### Thuộc tính

- `IP`: Địa chỉ IP của hệ thống (hiện không sử dụng biến này).
- `ServiceName`: Tên của dịch vụ.
- `ServiceUptime`: Thời gian hoạt động của dịch vụ.
- `MemoryUsed`: Phần trăm bộ nhớ đã sử dụng.
- `DiskUsed`: Phần trăm dung lượng đĩa đã sử dụng.
- `CPUUsed`: Phần trăm CPU đã sử dụng.
- `OutputLogSize`: Kích thước của file log đầu ra.
- `ErrorLogSize`: Kích thước của file log lỗi.
- `ErrorString`: Danh sách các lỗi xảy ra trong quá trình thu thập thông tin.

## Cấu trúc `MonitorService`

### Thuộc tính

- `messageSender`: Đối tượng gửi tin nhắn thuộc kiểu `t_network.MessageSender`.
- `monitorConn`: Kết nối đến máy chủ giám sát thuộc kiểu `t_network.Connection`.
- `serviceName`: Tên của dịch vụ cần giám sát.
- `delayTime`: Thời gian trễ giữa các lần thu thập thông tin.

### Hàm khởi tạo

- `NewMonitorService(messageSender t_network.MessageSender, monitorAddress string, dnsLink string, serviceName string, delayTime time.Duration) *MonitorService`: Tạo một đối tượng `MonitorService` mới với các thông tin được cung cấp.

### Các phương thức

- `Run()`: Chạy dịch vụ giám sát, thu thập thông tin hệ thống và gửi dữ liệu đến máy chủ giám sát theo chu kỳ thời gian đã định.

## Kết luận

File `monitor.go` cung cấp các phương thức cần thiết để giám sát và thu thập thông tin hệ thống của một dịch vụ trong blockchain. Nó sử dụng các script để thu thập thông tin và gửi dữ liệu đến máy chủ giám sát để phân tích và theo dõi.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`types.go`](./mvm/types.go)

## Giới thiệu

File `types.go` trong gói `mvm` định nghĩa các cấu trúc và phương thức liên quan để quản lý kết quả thực thi của Máy ảo Meta (MVM) trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý kết quả thực thi, bao gồm các thay đổi về số dư, mã, lưu trữ, và nhật ký sự kiện.

## Cấu trúc `MVMExecuteResult`

### Thuộc tính

- `MapAddBalance`: Map lưu trữ các thay đổi số dư được thêm, với khóa là địa chỉ và giá trị là số dư dưới dạng `[]byte`.
- `MapSubBalance`: Map lưu trữ các thay đổi số dư bị trừ, với khóa là địa chỉ và giá trị là số dư dưới dạng `[]byte`.
- `MapCodeChange`: Map lưu trữ các thay đổi mã, với khóa là địa chỉ và giá trị là mã dưới dạng `[]byte`.
- `MapCodeHash`: Map lưu trữ hash của mã, với khóa là địa chỉ và giá trị là hash dưới dạng `[]byte`.
- `MapStorageChange`: Map lưu trữ các thay đổi lưu trữ, với khóa là địa chỉ và giá trị là một map khác chứa khóa lưu trữ và giá trị dưới dạng `[]byte`.
- `JEventLogs`: Nhật ký sự kiện dưới dạng `LogsJson`.
- `Status`: Trạng thái của kết quả thực thi, thuộc kiểu `pb.RECEIPT_STATUS`.
- `Exception`: Ngoại lệ xảy ra trong quá trình thực thi, thuộc kiểu `pb.EXCEPTION`.
- `Exmsg`: Thông điệp ngoại lệ dưới dạng `string`.
- `Return`: Dữ liệu trả về từ quá trình thực thi dưới dạng `[]byte`.
- `GasUsed`: Lượng gas đã sử dụng trong quá trình thực thi, thuộc kiểu `uint64`.

### Các phương thức

- `String() string`: Trả về chuỗi biểu diễn của kết quả thực thi, bao gồm thông tin về lý do thoát, ngoại lệ, thông điệp ngoại lệ, đầu ra, gas đã sử dụng, và các thay đổi về số dư, mã, lưu trữ, và nhật ký sự kiện.
- `EventLogs(blockNumber uint64, transactionHash common.Hash) []types.EventLog`: Trả về danh sách các nhật ký sự kiện hoàn chỉnh dựa trên số block và hash giao dịch.

## Cấu trúc `LogsJson`

### Các phương thức

- `CompleteEventLogs(blockNumber uint64, transactionHash common.Hash) []types.EventLog`: Tạo và trả về danh sách các nhật ký sự kiện hoàn chỉnh từ `LogsJson`, bao gồm thông tin về số block, hash giao dịch, địa chỉ, dữ liệu, và các chủ đề.

## Kết luận

File `types.go` cung cấp các cấu trúc và phương thức cần thiết để quản lý kết quả thực thi của Máy ảo Meta trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý kết quả thực thi, giúp dễ dàng lưu trữ và truyền tải thông tin về các thay đổi trong hệ thống.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`helpers.go`](./mvm/helpers.go)

## Giới thiệu

File `helpers.go` trong gói `mvm` định nghĩa các hàm hỗ trợ để xử lý kết quả thực thi của Máy ảo Meta (MVM) trong blockchain. Mục tiêu là cung cấp các phương thức để trích xuất và chuyển đổi dữ liệu từ kết quả thực thi của MVM, bao gồm các thay đổi về số dư, mã, lưu trữ, và nhật ký sự kiện.

## Các hàm hỗ trợ

### `extractExecuteResult`

- **Mô tả**: Trích xuất kết quả thực thi từ cấu trúc `C.struct_ExecuteResult` và chuyển đổi thành `MVMExecuteResult`.
- **Tham số**: `cExecuteResult *C.struct_ExecuteResult` - Kết quả thực thi từ MVM.
- **Trả về**: `*MVMExecuteResult` - Kết quả thực thi đã được chuyển đổi.

### `extractAddBalance`

- **Mô tả**: Trích xuất các thay đổi số dư được thêm từ kết quả thực thi.
- **Tham số**: `cExecuteResult *C.struct_ExecuteResult` - Kết quả thực thi từ MVM.
- **Trả về**: `map[string][]byte` - Map lưu trữ các thay đổi số dư được thêm.

### `extractSubBalance`

- **Mô tả**: Trích xuất các thay đổi số dư bị trừ từ kết quả thực thi.
- **Tham số**: `cExecuteResult *C.struct_ExecuteResult` - Kết quả thực thi từ MVM.
- **Trả về**: `map[string][]byte` - Map lưu trữ các thay đổi số dư bị trừ.

### `extractCodeChange`

- **Mô tả**: Trích xuất các thay đổi mã và hash mã từ kết quả thực thi.
- **Tham số**: `cExecuteResult *C.struct_ExecuteResult` - Kết quả thực thi từ MVM.
- **Trả về**: 
  - `map[string][]byte` - Map lưu trữ các thay đổi mã.
  - `map[string][]byte` - Map lưu trữ hash của mã.

### `extractStorageChange`

- **Mô tả**: Trích xuất các thay đổi lưu trữ từ kết quả thực thi.
- **Tham số**: `cExecuteResult *C.struct_ExecuteResult` - Kết quả thực thi từ MVM.
- **Trả về**: `map[string]map[string][]byte` - Map lưu trữ các thay đổi lưu trữ.

### `extractEventLogs`

- **Mô tả**: Trích xuất các nhật ký sự kiện từ kết quả thực thi.
- **Tham số**: `cExecuteResult *C.struct_ExecuteResult` - Kết quả thực thi từ MVM.
- **Trả về**: `LogsJson` - Nhật ký sự kiện dưới dạng JSON.

## Kết luận

File `helpers.go` cung cấp các hàm hỗ trợ cần thiết để trích xuất và chuyển đổi dữ liệu từ kết quả thực thi của Máy ảo Meta (MVM) trong blockchain. Nó hỗ trợ việc xử lý các thay đổi về số dư, mã, lưu trữ, và nhật ký sự kiện, giúp dễ dàng quản lý và xử lý dữ liệu trong hệ thống.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`extension.go`](./mvm/extension.go)

## Giới thiệu

File `extension.go` định nghĩa các hàm mở rộng cho Máy ảo Meta (MVM) trong blockchain. Mục tiêu là cung cấp các hàm để thực hiện các tác vụ như gọi API, trích xuất trường từ JSON, và xác thực chữ ký BLS.

## Các hàm mở rộng

### ExtensionCallGetApi

- **Mô tả**: Gọi một API GET và trả về dữ liệu phản hồi.
- **Tham số**:
  - `bytes *C.uchar`: Dữ liệu đầu vào dưới dạng con trỏ đến mảng byte.
  - `size C.int`: Kích thước của dữ liệu đầu vào.
- **Trả về**:
  - `data_p *C.uchar`: Con trỏ đến dữ liệu phản hồi đã mã hóa.
  - `data_size C.int`: Kích thước của dữ liệu phản hồi.

### ExtensionExtractJsonField

- **Mô tả**: Trích xuất một trường từ chuỗi JSON.
- **Tham số**:
  - `bytes *C.uchar`: Dữ liệu đầu vào dưới dạng con trỏ đến mảng byte.
  - `size C.int`: Kích thước của dữ liệu đầu vào.
- **Trả về**:
  - `data_p *C.uchar`: Con trỏ đến dữ liệu trường đã mã hóa.
  - `data_size C.int`: Kích thước của dữ liệu trường.

### ExtensionBlst

- **Mô tả**: Xác thực chữ ký BLS hoặc chữ ký tổng hợp BLS.
- **Tham số**:
  - `bytes *C.uchar`: Dữ liệu đầu vào dưới dạng con trỏ đến mảng byte.
  - `size C.int`: Kích thước của dữ liệu đầu vào.
- **Trả về**:
  - `data_p *C.uchar`: Con trỏ đến dữ liệu kết quả đã mã hóa.
  - `data_size C.int`: Kích thước của dữ liệu kết quả.

### WrapExtensionBlst

- **Mô tả**: Gói dữ liệu và gọi hàm `ExtensionBlst`.
- **Tham số**:
  - `data []byte`: Dữ liệu đầu vào dưới dạng mảng byte.
- **Trả về**: Mảng byte chứa dữ liệu kết quả.

## Kết luận

File `extension.go` cung cấp các hàm mở rộng cần thiết cho Máy ảo Meta (MVM) để thực hiện các tác vụ như gọi API, trích xuất dữ liệu từ JSON, và xác thực chữ ký BLS. Các hàm này giúp mở rộng khả năng của MVM trong việc xử lý các tác vụ phức tạp trong blockchain.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`mvm_api.go`](./mvm/mvm_api.go)

## Giới thiệu

File `mvm_api.go` định nghĩa cấu trúc `MVMApi` và các phương thức liên quan để quản lý và tương tác với Máy ảo Meta (MVM) trong blockchain. Mục tiêu là cung cấp các phương thức để khởi tạo, quản lý trạng thái tài khoản và smart contract, cũng như thực hiện các cuộc gọi đến MVM.

## Cấu trúc `MVMApi`

### Thuộc tính

- `smartContractDb`: Cơ sở dữ liệu của smart contract, thuộc kiểu `SmartContractDB`.
- `accountStateDb`: Cơ sở dữ liệu trạng thái tài khoản, thuộc kiểu `AccountStateDB`.
- `currentRelatedAddresses`: Map lưu trữ các địa chỉ liên quan hiện tại, với khóa là địa chỉ và giá trị là struct rỗng.

### Hàm khởi tạo

- `InitMVMApi(smartContractDb SmartContractDB, accountStateDb AccountStateDB)`: Khởi tạo một đối tượng `MVMApi` mới với cơ sở dữ liệu smart contract và trạng thái tài khoản được cung cấp.

### Các phương thức

- `MVMApiInstance() *MVMApi`: Trả về instance hiện tại của `MVMApi`.
- `Clear()`: Xóa instance hiện tại của `MVMApi`.
- `SetSmartContractDb(smartContractDb SmartContractDB)`: Thiết lập cơ sở dữ liệu smart contract.
- `SmartContractDatas() SmartContractDB`: Trả về cơ sở dữ liệu smart contract.
- `SetAccountStateDb(accountStateDb AccountStateDB)`: Thiết lập cơ sở dữ liệu trạng thái tài khoản.
- `AccountStateDb() AccountStateDB`: Trả về cơ sở dữ liệu trạng thái tài khoản.
- `SetRelatedAddresses(addresses []common.Address)`: Thiết lập các địa chỉ liên quan hiện tại.
- `InRelatedAddress(address common.Address) bool`: Kiểm tra xem địa chỉ có nằm trong danh sách địa chỉ liên quan hay không.
- `Call(...)`: Thực hiện cuộc gọi đến MVM với dữ liệu giao dịch và ngữ cảnh block.

### Các hàm hỗ trợ

- `ClearProcessingPointers()`: Giải phóng bộ nhớ cho các con trỏ đang xử lý.
- `TestMemLeak()`: Kiểm tra rò rỉ bộ nhớ trong quá trình thực thi.
- `TestMemLeakGs(addresses []common.Address)`: Kiểm tra rò rỉ bộ nhớ với danh sách địa chỉ.
- `GetStorageValue(address *C.uchar, key *C.uchar) (value *C.uchar)`: Lấy giá trị lưu trữ từ cơ sở dữ liệu smart contract.

## Kết luận

File `mvm_api.go` cung cấp các phương thức và hàm hỗ trợ cần thiết để quản lý và tương tác với Máy ảo Meta (MVM) trong blockchain. Nó hỗ trợ việc khởi tạo, quản lý trạng thái tài khoản và smart contract, cũng như thực hiện các cuộc gọi đến MVM, giúp dễ dàng quản lý và xử lý dữ liệu trong hệ thống.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`connection.go`](./network/connection.go)

## Giới thiệu

File `connection.go` định nghĩa cấu trúc `Connection` và các phương thức liên quan để quản lý kết nối mạng trong blockchain. Nó bao gồm các phương thức để tạo, quản lý, gửi và nhận tin nhắn qua kết nối TCP.

## Cấu trúc `Connection`

### Thuộc tính

- `mu`: Mutex để đồng bộ hóa truy cập vào kết nối.
- `address`: Địa chỉ của kết nối thuộc kiểu `common.Address`.
- `cType`: Loại kết nối.
- `requestChan`: Kênh để gửi yêu cầu thuộc kiểu `network.Request`.
- `errorChan`: Kênh để gửi lỗi.
- `tcpConn`: Kết nối TCP thuộc kiểu `net.Conn`.
- `connect`: Trạng thái kết nối (đã kết nối hay chưa).
- `dnsLink`: Liên kết DNS của kết nối.
- `realConnAddr`: Địa chỉ thực của kết nối.

### Hàm khởi tạo

- `ConnectionFromTcpConnection(tcpConn net.Conn, dnsLink string) (network.Connection, error)`: Tạo một đối tượng `Connection` từ một kết nối TCP.
- `NewConnection(address common.Address, cType string, dnsLink string) network.Connection`: Tạo một đối tượng `Connection` mới với địa chỉ, loại và liên kết DNS được cung cấp.

### Các phương thức

- `Address() common.Address`: Trả về địa chỉ của kết nối.
- `ConnectionAddress() (string, error)`: Trả về địa chỉ thực của kết nối.
- `RequestChan() (chan network.Request, chan error)`: Trả về kênh yêu cầu và kênh lỗi.
- `Type() string`: Trả về loại kết nối.
- `String() string`: Trả về chuỗi mô tả của kết nối.
- `Init(address common.Address, cType string)`: Khởi tạo kết nối với địa chỉ và loại được cung cấp.
- `SendMessage(message network.Message) error`: Gửi tin nhắn qua kết nối.
- `Connect() (err error)`: Kết nối đến địa chỉ thực.
- `Disconnect() error`: Ngắt kết nối.
- `IsConnect() bool`: Kiểm tra trạng thái kết nối.
- `ReadRequest()`: Đọc yêu cầu từ kết nối.
- `Clone() network.Connection`: Tạo một bản sao của kết nối.
- `RemoteAddr() string`: Trả về địa chỉ từ xa của kết nối.

## Kết luận

File `connection.go` cung cấp các phương thức cần thiết để quản lý kết nối mạng trong blockchain. Nó hỗ trợ việc gửi và nhận tin nhắn qua kết nối TCP, đồng thời cung cấp các công cụ để quản lý và theo dõi trạng thái kết nối.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`connections_manager.go`](./network/connections_manager.go)

## Giới thiệu

File `connections_manager.go` định nghĩa cấu trúc `ConnectionsManager` và các phương thức liên quan để quản lý các kết nối mạng trong blockchain. Nó bao gồm các phương thức để thêm, xóa và truy xuất kết nối theo loại và địa chỉ.

## Cấu trúc `ConnectionsManager`

### Thuộc tính

- `mu`: Mutex để đồng bộ hóa truy cập vào các kết nối.
- `parentConnection`: Kết nối cha thuộc kiểu `network.Connection`.
- `typeToMapAddressConnections`: Mảng các map lưu trữ kết nối theo loại và địa chỉ.

### Hàm khởi tạo

- `NewConnectionsManager() network.ConnectionsManager`: Tạo một đối tượng `ConnectionsManager` mới.

### Các phương thức

- `ConnectionsByType(cType int) map[common.Address]network.Connection`: Trả về các kết nối theo loại.
- `ConnectionByTypeAndAddress(cType int, address common.Address) network.Connection`: Trả về kết nối theo loại và địa chỉ.
- `ConnectionsByTypeAndAddresses(cType int, addresses []common.Address) map[common.Address]network.Connection`: Trả về các kết nối theo loại và danh sách địa chỉ.
- `FilterAddressAvailable(cType int, addresses map[common.Address]*uint256.Int) map[common.Address]*uint256.Int`: Lọc và trả về các địa chỉ có kết nối khả dụng.
- `ParentConnection() network.Connection`: Trả về kết nối cha.
- `Stats() *pb.NetworkStats`: Trả về thống kê mạng.
- `AddParentConnection(conn network.Connection)`: Thêm kết nối cha.
- `RemoveConnection(conn network.Connection)`: Xóa kết nối.
- `AddConnection(conn network.Connection, replace bool, connectionType string)`: Thêm kết nối mới hoặc thay thế kết nối hiện tại.
- `MapAddressConnectionToInterface(data map[common.Address]network.Connection) map[common.Address]interface{}`: Chuyển đổi map kết nối thành map giao diện.

## Kết luận

File `connections_manager.go` cung cấp các phương thức cần thiết để quản lý các kết nối mạng trong blockchain. Nó hỗ trợ việc thêm, xóa và truy xuất kết nối theo loại và địa chỉ, đồng thời cung cấp các công cụ để quản lý và theo dõi trạng thái kết nối.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`message.go`](./network/message.go)

## Giới thiệu

File `message.go` định nghĩa cấu trúc `Message` và các phương thức liên quan để quản lý tin nhắn mạng trong blockchain. Tin nhắn bao gồm thông tin tiêu đề và nội dung.

## Cấu trúc `Message`

### Thuộc tính

- `proto`: Tin nhắn thuộc kiểu `pb.Message`.

### Hàm khởi tạo

- `NewMessage(pbMessage *pb.Message) network.Message`: Tạo một đối tượng `Message` mới từ một tin nhắn Protobuf.

### Các phương thức

- `Marshal() ([]byte, error)`: Chuyển đổi tin nhắn thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(protoStruct protoreflect.ProtoMessage) error`: Khởi tạo tin nhắn từ một slice byte đã được mã hóa.
- `String() string`: Trả về chuỗi mô tả của tin nhắn.
- `Command() string`: Trả về lệnh của tin nhắn.
- `Body() []byte`: Trả về nội dung của tin nhắn.
- `Pubkey() cm.PublicKey`: Trả về khóa công khai của tin nhắn.
- `Sign() cm.Sign`: Trả về chữ ký của tin nhắn.
- `ToAddress() common.Address`: Trả về địa chỉ đích của tin nhắn.
- `ID() string`: Trả về ID của tin nhắn.

## Kết luận

File `message.go` cung cấp các phương thức cần thiết để quản lý tin nhắn mạng trong blockchain. Nó hỗ trợ việc mã hóa và giải mã tin nhắn, đồng thời cung cấp các công cụ để truy xuất thông tin từ tin nhắn.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`message_sender.go`](./network/message_sender.go)

## Giới thiệu

File `message_sender.go` định nghĩa cấu trúc `MessageSender` và các phương thức liên quan để gửi tin nhắn mạng trong blockchain. Nó bao gồm các phương thức để gửi tin nhắn đơn lẻ hoặc phát tin nhắn đến nhiều kết nối.

## Cấu trúc `MessageSender`

### Thuộc tính

- `version`: Phiên bản của tin nhắn.

### Hàm khởi tạo

- `NewMessageSender(version string) network.MessageSender`: Tạo một đối tượng `MessageSender` mới với phiên bản được cung cấp.

### Các phương thức

- `SendMessage(connection network.Connection, command string, pbMessage protoreflect.ProtoMessage) error`: Gửi tin nhắn Protobuf qua kết nối.
- `SendBytes(connection network.Connection, command string, b []byte) error`: Gửi tin nhắn dưới dạng byte qua kết nối.
- `BroadcastMessage(mapAddressConnections map[common.Address]network.Connection, command string, marshaler network.Marshaler) error`: Phát tin nhắn đến nhiều kết nối.

### Các hàm hỗ trợ

- `getHeaderForCommand(command string, toAddress common.Address, version string) *pb.Header`: Tạo tiêu đề cho tin nhắn.
- `generateMessage(toAddress common.Address, command string, body []byte, version string) network.Message`: Tạo tin nhắn từ địa chỉ đích, lệnh, nội dung và phiên bản.
- `SendMessage(connection network.Connection, command string, pbMessage proto.Message, version string) (err error)`: Gửi tin nhắn Protobuf qua kết nối.
- `SendBytes(connection network.Connection, command string, bytes []byte, version string) error`: Gửi tin nhắn dưới dạng byte qua kết nối.

## Kết luận

File `message_sender.go` cung cấp các phương thức cần thiết để gửi tin nhắn mạng trong blockchain. Nó hỗ trợ việc gửi tin nhắn đơn lẻ hoặc phát tin nhắn đến nhiều kết nối, đồng thời cung cấp các công cụ để quản lý và theo dõi quá trình gửi tin nhắn.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`server.go`](./network/server.go)

## Giới thiệu

File `server.go` định nghĩa cấu trúc `SocketServer` và các phương thức liên quan để quản lý máy chủ socket trong blockchain. Nó bao gồm các phương thức để lắng nghe kết nối, xử lý kết nối, và quản lý các sự kiện kết nối và ngắt kết nối.

## Cấu trúc `SocketServer`

### Thuộc tính

- `connectionsManager`: Quản lý các kết nối thuộc kiểu `network.ConnectionsManager`.
- `listener`: Đối tượng lắng nghe kết nối thuộc kiểu `net.Listener`.
- `handler`: Bộ xử lý yêu cầu thuộc kiểu `network.Handler`.
- `nodeType`: Loại node.
- `version`: Phiên bản của máy chủ.
- `dnsLink`: Liên kết DNS của máy chủ.
- `keyPair`: Cặp khóa BLS thuộc kiểu `*bls.KeyPair`.
- `ctx`: Ngữ cảnh để quản lý vòng đời của máy chủ thuộc kiểu `context.Context`.
- `cancelFunc`: Hàm hủy ngữ cảnh.
- `onConnectedCallBack`: Danh sách các hàm callback khi kết nối thành công.
- `onDisconnectedCallBack`: Danh sách các hàm callback khi ngắt kết nối.

### Hàm khởi tạo

- `NewSocketServer(keyPair *bls.KeyPair, connectionsManager network.ConnectionsManager, handler network.Handler, nodeType string, version string, dnsLink string) network.SocketServer`: Tạo một đối tượng `SocketServer` mới với các thông tin được cung cấp.

### Các phương thức

- `SetContext(ctx context.Context, cancelFunc context.CancelFunc)`: Đặt ngữ cảnh và hàm hủy cho máy chủ.
- `AddOnConnectedCallBack(callBack func(network.Connection))`: Thêm hàm callback khi kết nối thành công.
- `AddOnDisconnectedCallBack(callBack func(network.Connection))`: Thêm hàm callback khi ngắt kết nối.
- `Listen(listenAddress string) error`: Lắng nghe kết nối tại địa chỉ được cung cấp.
- `Stop()`: Dừng máy chủ.
- `OnConnect(conn network.Connection)`: Xử lý sự kiện khi kết nối thành công.
- `OnDisconnect(conn network.Connection)`: Xử lý sự kiện khi ngắt kết nối.
- `HandleConnection(conn network.Connection) error`: Xử lý kết nối và đọc yêu cầu từ kết nối.
- `SetKeyPair(newKeyPair *bls.KeyPair)`: Đặt cặp khóa mới cho máy chủ.
- `StopAndRetryConnectToParent(conn network.Connection)`: Dừng máy chủ và thử kết nối lại với kết nối cha.
- `RetryConnectToParent(conn network.Connection)`: Thử kết nối lại với kết nối cha.

## Kết luận

File `server.go` cung cấp các phương thức cần thiết để quản lý máy chủ socket trong blockchain. Nó hỗ trợ việc lắng nghe và xử lý kết nối, đồng thời cung cấp các công cụ để quản lý và theo dõi trạng thái kết nối.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`nodes_state.go`](./nodes_state/nodes_state.go)

## Giới thiệu

File `nodes_state.go` định nghĩa cấu trúc `NodesState` và các phương thức liên quan để quản lý trạng thái của các node con trong blockchain. Nó bao gồm các phương thức để lấy trạng thái của node, gửi yêu cầu trạng thái, và quản lý các kết nối đến các node con.

## Cấu trúc `NodesState`

### Thuộc tính

- `prefix`: Tiền tố để xác định node.
- `socketServer`: Máy chủ socket thuộc kiểu `network.SocketServer`.
- `messageSender`: Đối tượng gửi tin nhắn thuộc kiểu `network.MessageSender`.
- `connectionsManager`: Quản lý các kết nối thuộc kiểu `network.ConnectionsManager`.
- `childNodes`: Danh sách địa chỉ của các node con.
- `childNodeStateRoots`: Mảng chứa hash trạng thái của các node con.
- `receivedChan`: Kênh để nhận thông báo khi nhận đủ trạng thái từ các node.
- `receivedNodesState`: Số lượng trạng thái node đã nhận.
- `getStateConnections`: Danh sách các kết nối để lấy trạng thái node.
- `currentSession`: ID session hiện tại.

### Hàm khởi tạo

- `NewNodesState(childNodes []common.Address, messageSender network.MessageSender, connectionsManager network.ConnectionsManager) *NodesState`: Tạo một đối tượng `NodesState` mới với danh sách node con, đối tượng gửi tin nhắn và quản lý kết nối được cung cấp.

### Các phương thức

- `SetSocketServer(s network.SocketServer)`: Đặt máy chủ socket cho `NodesState`.
- `GetStateRoot() (common.Hash, error)`: Lấy hash trạng thái của toàn bộ node.
- `GetChildNode(i int) common.Address`: Trả về địa chỉ của node con tại chỉ số `i`.
- `GetChildNodeIdx(nodeAddress common.Address) int`: Trả về chỉ số của node con với địa chỉ được cung cấp.
- `GetChildNodeStateRoot(address common.Address) common.Hash`: Trả về hash trạng thái của node con với địa chỉ được cung cấp.
- `SetChildNode(i int, childNode common.Address)`: Đặt địa chỉ cho node con tại chỉ số `i`.
- `SendCancelPendingStates()`: Gửi yêu cầu hủy trạng thái đang chờ xử lý đến tất cả các node con.
- `SendGetAccountState(address common.Address, id string) error`: Gửi yêu cầu lấy trạng thái tài khoản đến node con tương ứng với địa chỉ được cung cấp.
- `SendGetNodeSyncData(latestCheckPointBlockNumber uint64, validatorAddress common.Address)`: Gửi yêu cầu đồng bộ dữ liệu node đến tất cả các node con.

## Kết luận

File `nodes_state.go` cung cấp các phương thức cần thiết để quản lý trạng thái của các node con trong blockchain. Nó hỗ trợ việc gửi và nhận yêu cầu trạng thái, đồng thời cung cấp các công cụ để quản lý và theo dõi trạng thái của các node con.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`pack.go`](./pack/pack.go)

## Giới thiệu

File `pack.go` định nghĩa cấu trúc `Pack` và các phương thức liên quan để quản lý các gói giao dịch trong blockchain. Nó bao gồm các phương thức để tạo, mã hóa, giải mã, và xác thực chữ ký của các gói giao dịch.

## Cấu trúc `Pack`

### Thuộc tính

- `id`: ID của gói giao dịch, được tạo ngẫu nhiên bằng UUID.
- `transactions`: Danh sách các giao dịch thuộc kiểu `types.Transaction`.
- `aggSign`: Chữ ký tổng hợp của gói giao dịch.
- `timeStamp`: Dấu thời gian của gói giao dịch.

### Hàm khởi tạo

- `NewPack(transactions []types.Transaction, aggSign []byte, timeStamp uint64) types.Pack`: Tạo một đối tượng `Pack` mới với danh sách giao dịch, chữ ký tổng hợp và dấu thời gian được cung cấp.

### Các phương thức

- `NewVerifyPackSignRequest() types.VerifyPackSignRequest`: Tạo một yêu cầu xác thực chữ ký cho gói giao dịch.
- `Unmarshal(b []byte) error`: Giải mã gói giao dịch từ một slice byte.
- `Marshal() ([]byte, error)`: Mã hóa gói giao dịch thành một slice byte.
- `Proto() *pb.Pack`: Chuyển đổi gói giao dịch thành đối tượng Protobuf.
- `FromProto(pbMessage *pb.Pack)`: Khởi tạo gói giao dịch từ một đối tượng Protobuf.
- `Transactions() []types.Transaction`: Trả về danh sách giao dịch của gói.
- `Timestamp() uint64`: Trả về dấu thời gian của gói.
- `Id() string`: Trả về ID của gói.
- `AggregateSign() []byte`: Trả về chữ ký tổng hợp của gói.
- `ValidSign() bool`: Kiểm tra tính hợp lệ của chữ ký tổng hợp.

### Các hàm hỗ trợ

- `PacksToProto(packs []types.Pack) []*pb.Pack`: Chuyển đổi danh sách gói giao dịch thành danh sách đối tượng Protobuf.
- `PackFromProto(pbPack *pb.Pack) types.Pack`: Khởi tạo gói giao dịch từ một đối tượng Protobuf.
- `PacksFromProto(pbPacks []*pb.Pack) []types.Pack`: Chuyển đổi danh sách đối tượng Protobuf thành danh sách gói giao dịch.
- `MarshalPacks(packs []types.Pack) ([]byte, error)`: Mã hóa danh sách gói giao dịch thành một slice byte.
- `UnmarshalTransactions(b []byte) ([]types.Pack, error)`: Giải mã danh sách gói giao dịch từ một slice byte.

## Kết luận

File `pack.go` cung cấp các phương thức cần thiết để quản lý các gói giao dịch trong blockchain. Nó hỗ trợ việc mã hóa, giải mã, và xác thực chữ ký của các gói giao dịch, đồng thời cung cấp các công cụ để chuyển đổi giữa các định dạng dữ liệu khác nhau.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`packs_from_leader.go`](./pack/packs_from_leader.go)

## Giới thiệu

File `packs_from_leader.go` định nghĩa cấu trúc `PacksFromLeader` và các phương thức liên quan để quản lý các gói giao dịch từ leader trong blockchain. Nó bao gồm các phương thức để tạo, mã hóa, giải mã, và xác thực chữ ký của các gói giao dịch.

## Cấu trúc `PacksFromLeader`

### Thuộc tính

- `packs`: Danh sách các gói giao dịch thuộc kiểu `types.Pack`.
- `blockNumber`: Số block của gói giao dịch.
- `timeStamp`: Dấu thời gian của gói giao dịch.

### Hàm khởi tạo

- `NewPacksFromLeader(packs []types.Pack, blockNumber uint64, timeStamp uint64) *PacksFromLeader`: Tạo một đối tượng `PacksFromLeader` mới với danh sách gói giao dịch, số block và dấu thời gian được cung cấp.

### Các phương thức

- `Packs() []types.Pack`: Trả về danh sách các gói giao dịch.
- `BlockNumber() uint64`: Trả về số block của gói giao dịch.
- `TimeStamp() uint64`: Trả về dấu thời gian của gói giao dịch.
- `Marshal() ([]byte, error)`: Mã hóa đối tượng `PacksFromLeader` thành một slice byte.
- `Unmarshal(b []byte) error`: Giải mã đối tượng `PacksFromLeader` từ một slice byte.
- `IsValidSign() bool`: Kiểm tra tính hợp lệ của chữ ký trong tất cả các gói giao dịch.
- `Transactions() []types.Transaction`: Trả về danh sách các giao dịch từ tất cả các gói.
- `Proto() *pb.PacksFromLeader`: Chuyển đổi đối tượng `PacksFromLeader` thành đối tượng Protobuf.
- `FromProto(pbData *pb.PacksFromLeader)`: Khởi tạo đối tượng `PacksFromLeader` từ một đối tượng Protobuf.

## Kết luận

File `packs_from_leader.go` cung cấp các phương thức cần thiết để quản lý các gói giao dịch từ leader trong blockchain. Nó hỗ trợ việc mã hóa, giải mã, và xác thực chữ ký của các gói giao dịch, đồng thời cung cấp các công cụ để chuyển đổi giữa các định dạng dữ liệu khác nhau.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`verify_pack_sign.go`](./pack/verify_pack_sign.go)

## Giới thiệu

File `verify_pack_sign.go` định nghĩa các cấu trúc `VerifyPackSignRequest` và `VerifyPackSignResult` cùng với các phương thức liên quan để quản lý việc xác thực chữ ký của các gói giao dịch trong blockchain. Nó bao gồm các phương thức để tạo, mã hóa, giải mã, và xác thực chữ ký của các yêu cầu và kết quả xác thực.

## Cấu trúc `VerifyPackSignRequest`

### Thuộc tính

- `packId`: ID của gói giao dịch.
- `publicKeys`: Danh sách các khóa công khai liên quan đến các giao dịch.
- `hashes`: Danh sách các hash của giao dịch.
- `aggregateSign`: Chữ ký tổng hợp của gói giao dịch.

### Hàm khởi tạo

- `NewVerifyPackSignRequest(packId string, publicKeys [][]byte, hashes [][]byte, aggregateSign []byte) types.VerifyPackSignRequest`: Tạo một đối tượng `VerifyPackSignRequest` mới với ID gói, danh sách khóa công khai, danh sách hash và chữ ký tổng hợp được cung cấp.

### Các phương thức

- `Unmarshal(b []byte) error`: Giải mã yêu cầu từ một slice byte.
- `Marshal() ([]byte, error)`: Mã hóa yêu cầu thành một slice byte.
- `Id() string`: Trả về ID của gói giao dịch.
- `PublicKeys() [][]byte`: Trả về danh sách khóa công khai.
- `Hashes() [][]byte`: Trả về danh sách hash của giao dịch.
- `AggregateSign() []byte`: Trả về chữ ký tổng hợp.
- `Valid() bool`: Kiểm tra tính hợp lệ của chữ ký tổng hợp.
- `Proto() *pb.VerifyPackSignRequest`: Chuyển đổi yêu cầu thành đối tượng Protobuf.

## Cấu trúc `VerifyPackSignResult`

### Thuộc tính

- `packId`: ID của gói giao dịch.
- `valid`: Trạng thái hợp lệ của chữ ký.

### Hàm khởi tạo

- `NewVerifyPackSignResult(packId string, valid bool) types.VerifyPackSignResult`: Tạo một đối tượng `VerifyPackSignResult` mới với ID gói và trạng thái hợp lệ được cung cấp.

### Các phương thức

- `Unmarshal(b []byte) error`: Giải mã kết quả từ một slice byte.
- `Marshal() ([]byte, error)`: Mã hóa kết quả thành một slice byte.
- `PackId() string`: Trả về ID của gói giao dịch.
- `Valid() bool`: Trả về trạng thái hợp lệ của chữ ký.

## Kết luận

File `verify_pack_sign.go` cung cấp các phương thức cần thiết để quản lý việc xác thực chữ ký của các gói giao dịch trong blockchain. Nó hỗ trợ việc mã hóa, giải mã, và xác thực chữ ký của các yêu cầu và kết quả xác thực, đồng thời cung cấp các công cụ để chuyển đổi giữa các định dạng dữ liệu khác nhau.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`pack_pool.go`](./pack_pool/pack_pool.go)

## Giới thiệu

File `pack_pool.go` định nghĩa cấu trúc `PackPool` và các phương thức liên quan để quản lý một tập hợp các gói giao dịch (`Pack`) trong blockchain. Nó bao gồm các phương thức để thêm gói giao dịch vào pool và lấy tất cả các gói giao dịch từ pool.

## Cấu trúc `PackPool`

### Thuộc tính

- `packs`: Danh sách các gói giao dịch thuộc kiểu `types.Pack`.
- `mutex`: Đối tượng khóa (`sync.Mutex`) để đảm bảo an toàn khi truy cập đồng thời vào `packs`.

### Hàm khởi tạo

- `NewPackPool() *PackPool`: Tạo một đối tượng `PackPool` mới.

### Các phương thức

- `AddPack(pack types.Pack)`: Thêm một gói giao dịch vào `PackPool`. Sử dụng khóa để đảm bảo an toàn khi truy cập đồng thời.
- `Addpacks(packs []types.Pack)`: Thêm nhiều gói giao dịch vào `PackPool`. Sử dụng khóa để đảm bảo an toàn khi truy cập đồng thời.
- `Getpacks() []types.Pack`: Lấy tất cả các gói giao dịch từ `PackPool` và làm rỗng danh sách `packs`. Sử dụng khóa để đảm bảo an toàn khi truy cập đồng thời.

## Kết luận

File `pack_pool.go` cung cấp các phương thức cần thiết để quản lý một tập hợp các gói giao dịch trong blockchain. Nó hỗ trợ việc thêm và lấy các gói giao dịch một cách an toàn trong môi trường đa luồng.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`receipt.go`](./receipt/receipt.go)

## Giới thiệu

File `receipt.go` định nghĩa cấu trúc `Receipt` và các phương thức liên quan để quản lý biên nhận của các giao dịch trong blockchain. Biên nhận chứa thông tin về giao dịch, bao gồm hash giao dịch, địa chỉ gửi và nhận, số lượng, trạng thái, và các log sự kiện.

## Cấu trúc `Receipt`

### Thuộc tính

- `proto`: Đối tượng Protobuf `pb.Receipt` lưu trữ thông tin biên nhận.

### Hàm khởi tạo

- `NewReceipt(transactionHash common.Hash, fromAddress common.Address, toAddress common.Address, amount *big.Int, action pb.ACTION, status pb.RECEIPT_STATUS, returnValue []byte, exception pb.EXCEPTION, gastFee uint64, gasUsed uint64, eventLogs []types.EventLog) types.Receipt`: Tạo một đối tượng `Receipt` mới với các thông tin được cung cấp.

### Các phương thức

#### Getter

- `TransactionHash() common.Hash`: Trả về hash của giao dịch.
- `FromAddress() common.Address`: Trả về địa chỉ gửi.
- `ToAddress() common.Address`: Trả về địa chỉ nhận.
- `GasUsed() uint64`: Trả về lượng gas đã sử dụng.
- `GasFee() uint64`: Trả về phí gas.
- `Amount() *big.Int`: Trả về số lượng giao dịch.
- `Return() []byte`: Trả về giá trị trả về của giao dịch.
- `Status() pb.RECEIPT_STATUS`: Trả về trạng thái của biên nhận.
- `Action() pb.ACTION`: Trả về hành động của biên nhận.
- `EventLogs() []*pb.EventLog`: Trả về danh sách log sự kiện.

#### Setter

- `UpdateExecuteResult(status pb.RECEIPT_STATUS, returnValue []byte, exception pb.EXCEPTION, gasUsed uint64, eventLogs []types.EventLog)`: Cập nhật kết quả thực thi cho biên nhận.

#### Khác

- `Json() ([]byte, error)`: Chuyển đổi biên nhận thành định dạng JSON.
- `ReceiptsToProto(receipts []types.Receipt) []*pb.Receipt`: Chuyển đổi danh sách biên nhận thành danh sách đối tượng Protobuf.
- `ProtoToReceipts(protoReceipts []*pb.Receipt) []types.Receipt`: Chuyển đổi danh sách đối tượng Protobuf thành danh sách biên nhận.

## Kết luận

File `receipt.go` cung cấp các phương thức cần thiết để quản lý biên nhận của các giao dịch trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi biên nhận giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin giao dịch.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`receipts.go`](./receipt/receipts.go)

## Giới thiệu

File `receipts.go` định nghĩa cấu trúc `Receipts` và các phương thức liên quan để quản lý tập hợp các biên nhận (`Receipt`) trong blockchain. Nó bao gồm các phương thức để thêm biên nhận, cập nhật kết quả thực thi, và tính toán tổng lượng gas đã sử dụng.

## Cấu trúc `Receipts`

### Thuộc tính

- `trie`: Cây Merkle Patricia Trie để lưu trữ và quản lý các biên nhận.
- `receipts`: Map lưu trữ các biên nhận với hash giao dịch là key và biên nhận là value.

### Biến toàn cục

- `ErrorReceiptNotFound`: Biến lỗi được trả về khi không tìm thấy biên nhận.

### Hàm khởi tạo

- `NewReceipts() types.Receipts`: Tạo một đối tượng `Receipts` mới với một cây Merkle Patricia Trie trống và một map biên nhận trống.

### Các phương thức

- `ReceiptsRoot() (common.Hash, error)`: Tính toán và trả về hash của gốc cây Merkle Patricia Trie.
- `AddReceipt(receipt types.Receipt) error`: Thêm một biên nhận vào `Receipts` và cập nhật cây Merkle Patricia Trie.
- `ReceiptsMap() map[common.Hash]types.Receipt`: Trả về map các biên nhận.
- `UpdateExecuteResultToReceipt(hash common.Hash, status pb.RECEIPT_STATUS, returnValue []byte, exception pb.EXCEPTION, gasUsed uint64, eventLogs []types.EventLog) error`: Cập nhật kết quả thực thi cho một biên nhận dựa trên hash giao dịch.
- `GasUsed() uint64`: Tính toán và trả về tổng lượng gas đã sử dụng của tất cả các biên nhận.

## Kết luận

File `receipts.go` cung cấp các phương thức cần thiết để quản lý tập hợp các biên nhận trong blockchain. Nó hỗ trợ việc thêm, cập nhật, và tính toán thông tin từ các biên nhận, giúp dễ dàng quản lý và truy xuất dữ liệu giao dịch.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`remote_storage_db.go`](./remote_storage_db/remote_storage_db.go)

## Giới thiệu

File `remote_storage_db.go` định nghĩa cấu trúc `RemoteStorageDB` và các phương thức liên quan để quản lý kết nối từ xa tới cơ sở dữ liệu lưu trữ. Nó cho phép lấy dữ liệu và mã từ hợp đồng thông minh thông qua kết nối mạng.

## Cấu trúc `RemoteStorageDB`

### Thuộc tính

- `remoteConnection`: Đối tượng `network.Connection` để quản lý kết nối từ xa.
- `messageSender`: Đối tượng `network.MessageSender` để gửi tin nhắn qua kết nối.
- `address`: Địa chỉ thuộc kiểu `common.Address` của đối tượng.
- `currentBlockNumber`: Số block hiện tại thuộc kiểu `uint64`.
- `sync.Mutex`: Đối tượng khóa để đảm bảo an toàn khi truy cập đồng thời.

### Hàm khởi tạo

- `NewRemoteStorageDB(remoteConnection network.Connection, messageSender network.MessageSender, address common.Address) *RemoteStorageDB`: Tạo một đối tượng `RemoteStorageDB` mới với kết nối từ xa, người gửi tin nhắn và địa chỉ được cung cấp.

### Các phương thức

- `checkConnection() error`: Kiểm tra và thiết lập kết nối nếu chưa kết nối.
- `Get(key []byte) ([]byte, error)`: Lấy dữ liệu từ cơ sở dữ liệu từ xa dựa trên khóa được cung cấp.
- `GetCode(address common.Address) ([]byte, error)`: Lấy mã hợp đồng thông minh từ cơ sở dữ liệu từ xa dựa trên địa chỉ được cung cấp.
- `SetBlockNumber(blockNumber uint64)`: Đặt số block hiện tại.
- `Close()`: Ngắt kết nối từ xa.

## Kết luận

File `remote_storage_db.go` cung cấp các phương thức cần thiết để quản lý kết nối từ xa tới cơ sở dữ liệu lưu trữ. Nó hỗ trợ việc lấy dữ liệu và mã từ hợp đồng thông minh, đảm bảo an toàn khi truy cập đồng thời và quản lý kết nối hiệu quả.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`event_log.go`](./smart_contract/event_log.go)

## Giới thiệu

File `event_log.go` định nghĩa cấu trúc `EventLog` và các phương thức liên quan để quản lý log sự kiện của các giao dịch trong blockchain. Log sự kiện chứa thông tin về block, giao dịch, địa chỉ, dữ liệu và các chủ đề liên quan.

## Cấu trúc `EventLog`

### Thuộc tính

- `proto`: Đối tượng Protobuf `pb.EventLog` lưu trữ thông tin log sự kiện.

### Hàm khởi tạo

- `NewEventLog(blockNumber uint64, transactionHash common.Hash, address common.Address, data []byte, topics [][]byte) types.EventLog`: Tạo một đối tượng `EventLog` mới với các thông tin được cung cấp.

### Các phương thức

#### General

- `Proto() *pb.EventLog`: Trả về đối tượng Protobuf của log sự kiện.
- `FromProto(logPb *pb.EventLog)`: Khởi tạo `EventLog` từ một đối tượng Protobuf.
- `Unmarshal(b []byte) error`: Giải mã dữ liệu từ một slice byte thành `EventLog`.
- `Marshal() ([]byte, error)`: Mã hóa `EventLog` thành một slice byte.
- `Copy() types.EventLog`: Tạo một bản sao của `EventLog`.

#### Getter

- `Hash() common.Hash`: Tính toán và trả về hash của log sự kiện.
- `Address() common.Address`: Trả về địa chỉ liên quan đến log sự kiện.
- `BlockNumber() string`: Trả về số block dưới dạng chuỗi hex.
- `TransactionHash() string`: Trả về hash của giao dịch dưới dạng chuỗi hex.
- `Data() string`: Trả về dữ liệu của log sự kiện dưới dạng chuỗi hex.
- `Topics() []string`: Trả về danh sách các chủ đề dưới dạng chuỗi hex.

#### Khác

- `String() string`: Trả về chuỗi mô tả của `EventLog`.

## Kết luận

File `event_log.go` cung cấp các phương thức cần thiết để quản lý log sự kiện của các giao dịch trong blockchain. Nó hỗ trợ việc tạo, mã hóa, giải mã và truy xuất thông tin từ log sự kiện.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`event_logs.go`](./smart_contract/event_logs.go)

## Giới thiệu

File `event_logs.go` định nghĩa cấu trúc `EventLogs` và các phương thức liên quan để quản lý tập hợp các log sự kiện trong blockchain. Nó bao gồm các phương thức để tạo, mã hóa, giải mã và truy xuất danh sách log sự kiện.

## Cấu trúc `EventLogs`

### Thuộc tính

- `proto`: Đối tượng Protobuf `pb.EventLogs` lưu trữ thông tin tập hợp log sự kiện.

### Hàm khởi tạo

- `NewEventLogs(eventLogs []types.EventLog) types.EventLogs`: Tạo một đối tượng `EventLogs` mới từ danh sách các log sự kiện.

### Các phương thức

#### General

- `FromProto(logPb *pb.EventLogs)`: Khởi tạo `EventLogs` từ một đối tượng Protobuf.
- `Unmarshal(b []byte) error`: Giải mã dữ liệu từ một slice byte thành `EventLogs`.
- `Marshal() ([]byte, error)`: Mã hóa `EventLogs` thành một slice byte.
- `Proto() *pb.EventLogs`: Trả về đối tượng Protobuf của tập hợp log sự kiện.

#### Getter

- `EventLogList() []types.EventLog`: Trả về danh sách các log sự kiện.

#### Khác

- `Copy() types.EventLogs`: Tạo một bản sao của `EventLogs`.

## Kết luận

File `event_logs.go` cung cấp các phương thức cần thiết để quản lý tập hợp các log sự kiện trong blockchain. Nó hỗ trợ việc tạo, mã hóa, giải mã và truy xuất thông tin từ danh sách log sự kiện.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`execute_sc_result.go`](./smart_contract/execute_sc_result.go)

## Giới thiệu

File `execute_sc_result.go` định nghĩa cấu trúc `ExecuteSCResult` và các phương thức liên quan để quản lý kết quả thực thi của các smart contract trong blockchain. Kết quả thực thi bao gồm thông tin về hash giao dịch, trạng thái, ngoại lệ, dữ liệu trả về, và các thay đổi liên quan đến tài khoản và smart contract.

## Cấu trúc `ExecuteSCResult`

### Thuộc tính

- `transactionHash`: Hash của giao dịch thuộc kiểu `common.Hash`.
- `status`: Trạng thái của biên nhận thuộc kiểu `pb.RECEIPT_STATUS`.
- `exception`: Ngoại lệ xảy ra trong quá trình thực thi thuộc kiểu `pb.EXCEPTION`.
- `returnData`: Dữ liệu trả về từ smart contract thuộc kiểu `[]byte`.
- `gasUsed`: Lượng gas đã sử dụng thuộc kiểu `uint64`.
- `logsHash`: Hash của các log sự kiện thuộc kiểu `common.Hash`.
- `mapAddBalance`, `mapSubBalance`: Map lưu trữ các thay đổi số dư.
- `mapStorageRoot`, `mapCodeHash`: Map lưu trữ root của trạng thái và hash của mã.
- `mapStorageAddress`, `mapCreatorPubkey`: Map lưu trữ địa chỉ và khóa công khai của người tạo.
- `mapStorageAddressTouchedAddresses`: Map lưu trữ các địa chỉ đã được touch.
- `mapNativeSmartContractUpdateStorage`: Map lưu trữ các cập nhật của smart contract gốc.
- `eventLogs`: Danh sách các log sự kiện thuộc kiểu `[]types.EventLog`.

### Hàm khởi tạo

- `NewExecuteSCResult(...) *ExecuteSCResult`: Tạo một đối tượng `ExecuteSCResult` mới với các thông tin được cung cấp.
- `NewErrorExecuteSCResult(...) *ExecuteSCResult`: Tạo một đối tượng `ExecuteSCResult` mới cho trường hợp lỗi.

### Các phương thức

- `FromProto(pbData *pb.ExecuteSCResult)`: Khởi tạo `ExecuteSCResult` từ một đối tượng Protobuf.
- `Proto() protoreflect.ProtoMessage`: Chuyển đổi `ExecuteSCResult` thành đối tượng Protobuf.
- `Unmarshal(b []byte) error`: Khởi tạo `ExecuteSCResult` từ một slice byte đã được mã hóa.
- `Marshal() ([]byte, error)`: Chuyển đổi `ExecuteSCResult` thành một slice byte để lưu trữ hoặc truyền tải.
- `String() string`: Trả về chuỗi mô tả của `ExecuteSCResult`.

### Getter

- `TransactionHash() common.Hash`: Trả về hash của giao dịch.
- `MapAddBalance() map[string][]byte`: Trả về map thay đổi số dư thêm.
- `MapSubBalance() map[string][]byte`: Trả về map thay đổi số dư trừ.
- `MapStorageRoot() map[string][]byte`: Trả về map root của trạng thái.
- `MapCodeHash() map[string][]byte`: Trả về map hash của mã.
- `MapStorageAddress() map[string]common.Address`: Trả về map địa chỉ lưu trữ.
- `MapCreatorPubkey() map[string][]byte`: Trả về map khóa công khai của người tạo.
- `GasUsed() uint64`: Trả về lượng gas đã sử dụng.
- `ReceiptStatus() pb.RECEIPT_STATUS`: Trả về trạng thái của biên nhận.
- `Exception() pb.EXCEPTION`: Trả về ngoại lệ xảy ra.
- `Return() []byte`: Trả về dữ liệu trả về.
- `LogsHash() common.Hash`: Trả về hash của các log sự kiện.
- `EventLogs() []types.EventLog`: Trả về danh sách các log sự kiện.
- `MapStorageAddressTouchedAddresses() map[common.Address][]common.Address`: Trả về map các địa chỉ đã được touch.
- `MapNativeSmartContractUpdateStorage() map[common.Address][][2][]byte`: Trả về map các cập nhật của smart contract gốc.

## Kết luận

File `execute_sc_result.go` cung cấp các phương thức cần thiết để quản lý kết quả thực thi của các smart contract trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi kết quả thực thi giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`execute_sc_results.go`](./smart_contract/execute_sc_results.go)

## Giới thiệu

File `execute_sc_results.go` định nghĩa cấu trúc `ExecuteSCResults` và các phương thức liên quan để quản lý tập hợp các kết quả thực thi của smart contract trong blockchain. Nó bao gồm thông tin về ID nhóm và số block.

## Cấu trúc `ExecuteSCResults`

### Thuộc tính

- `results`: Danh sách các kết quả thực thi thuộc kiểu `[]types.ExecuteSCResult`.
- `groupId`: ID của nhóm thuộc kiểu `uint64`.
- `blockNumber`: Số block thuộc kiểu `uint64`.

### Hàm khởi tạo

- `NewExecuteSCResults(results []types.ExecuteSCResult, groupId uint64, blockNumber uint64) *ExecuteSCResults`: Tạo một đối tượng `ExecuteSCResults` mới với các thông tin được cung cấp.

### Các phương thức

- `Unmarshal(b []byte) error`: Khởi tạo `ExecuteSCResults` từ một slice byte đã được mã hóa.
- `Marshal() ([]byte, error)`: Chuyển đổi `ExecuteSCResults` thành một slice byte để lưu trữ hoặc truyền tải.
- `Proto() protoreflect.ProtoMessage`: Chuyển đổi `ExecuteSCResults` thành đối tượng Protobuf.
- `FromProto(pbData *pb.ExecuteSCResults)`: Khởi tạo `ExecuteSCResults` từ một đối tượng Protobuf.
- `String() string`: Trả về chuỗi mô tả của `ExecuteSCResults`.

### Getter

- `GroupId() uint64`: Trả về ID của nhóm.
- `BlockNumber() uint64`: Trả về số block.
- `Results() []types.ExecuteSCResult`: Trả về danh sách các kết quả thực thi.

## Kết luận

File `execute_sc_results.go` cung cấp các phương thức cần thiết để quản lý tập hợp các kết quả thực thi của smart contract trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi kết quả thực thi giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`smart_contract_update_data.go`](./smart_contract/smart_contract_update_data.go)

## Giới thiệu

File `smart_contract_update_data.go` định nghĩa cấu trúc `SmartContractUpdateData` và các phương thức liên quan để quản lý dữ liệu cập nhật của smart contract trong blockchain. Dữ liệu cập nhật bao gồm mã hợp đồng, lưu trữ và các log sự kiện.

## Cấu trúc `SmartContractUpdateData`

### Thuộc tính

- `code`: Mã của smart contract thuộc kiểu `[]byte`.
- `storage`: Map lưu trữ dữ liệu của smart contract với key là chuỗi và value là `[]byte`.
- `eventLogs`: Danh sách các log sự kiện thuộc kiểu `[]types.EventLog`.

### Hàm khởi tạo

- `NewSmartContractUpdateData(code []byte, storage map[string][]byte, eventLogs []types.EventLog) *SmartContractUpdateData`: Tạo một đối tượng `SmartContractUpdateData` mới với mã, lưu trữ và log sự kiện được cung cấp.

### Các phương thức

- `Code() []byte`: Trả về mã của smart contract.
- `Storage() map[string][]byte`: Trả về map lưu trữ của smart contract.
- `EventLogs() []types.EventLog`: Trả về danh sách log sự kiện.
- `CodeHash() common.Hash`: Tính toán và trả về hash của mã smart contract.
- `SetCode(code []byte)`: Đặt mã mới cho smart contract.
- `UpdateStorage(storage map[string][]byte)`: Cập nhật lưu trữ của smart contract.
- `AddEventLog(eventLog types.EventLog)`: Thêm một log sự kiện vào danh sách.
- `Marshal() ([]byte, error)`: Chuyển đổi `SmartContractUpdateData` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(b []byte) error`: Khởi tạo `SmartContractUpdateData` từ một slice byte đã được mã hóa.
- `FromProto(fbProto *pb.SmartContractUpdateData)`: Khởi tạo `SmartContractUpdateData` từ một đối tượng Protobuf.
- `Proto() *pb.SmartContractUpdateData`: Chuyển đổi `SmartContractUpdateData` thành đối tượng Protobuf.
- `String() string`: Trả về chuỗi mô tả của `SmartContractUpdateData`.

## Kết luận

File `smart_contract_update_data.go` cung cấp các phương thức cần thiết để quản lý và chuyển đổi dữ liệu cập nhật của smart contract trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi dữ liệu giữa các định dạng khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`smart_contract_update_datas.go`](./smart_contract/smart_contract_update_datas.go)

## Giới thiệu

File `smart_contract_update_datas.go` định nghĩa cấu trúc `SmartContractUpdateDatas` và các phương thức liên quan để quản lý tập hợp dữ liệu cập nhật của nhiều smart contract trong blockchain. Nó bao gồm thông tin về số block và dữ liệu cập nhật của từng smart contract.

## Cấu trúc `SmartContractUpdateDatas`

### Thuộc tính

- `blockNumber`: Số block thuộc kiểu `uint64`.
- `data`: Map lưu trữ dữ liệu cập nhật của smart contract với địa chỉ là key và `SmartContractUpdateData` là value.

### Hàm khởi tạo

- `NewSmartContractUpdateDatas(blockNumber uint64, data map[common.Address]types.SmartContractUpdateData) *SmartContractUpdateDatas`: Tạo một đối tượng `SmartContractUpdateDatas` mới với số block và dữ liệu cập nhật được cung cấp.

### Các phương thức

- `Data() map[common.Address]types.SmartContractUpdateData`: Trả về map dữ liệu cập nhật của smart contract.
- `BlockNumber() uint64`: Trả về số block.
- `Proto() *pb.SmartContractUpdateDatas`: Chuyển đổi `SmartContractUpdateDatas` thành đối tượng Protobuf.
- `FromProto(pbData *pb.SmartContractUpdateDatas)`: Khởi tạo `SmartContractUpdateDatas` từ một đối tượng Protobuf.
- `Marshal() ([]byte, error)`: Chuyển đổi `SmartContractUpdateDatas` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(data []byte) error`: Khởi tạo `SmartContractUpdateDatas` từ một slice byte đã được mã hóa.

## Kết luận

File `smart_contract_update_datas.go` cung cấp các phương thức cần thiết để quản lý và chuyển đổi tập hợp dữ liệu cập nhật của smart contract trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi dữ liệu giữa các định dạng khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`touched_addresses_data.go`](./smart_contract/touched_addresses_data.go)

## Giới thiệu

File `touched_addresses_data.go` định nghĩa cấu trúc `TouchedAddressesData` và các phương thức liên quan để quản lý dữ liệu về các địa chỉ đã được touch trong một block của blockchain. Nó bao gồm thông tin về số block và danh sách các địa chỉ liên quan.

## Cấu trúc `TouchedAddressesData`

### Thuộc tính

- `blockNumber`: Số block thuộc kiểu `uint64`.
- `addresses`: Danh sách các địa chỉ thuộc kiểu `[]common.Address`.

### Hàm khởi tạo

- `NewTouchedAddressesData(blockNumber uint64, addresses []common.Address) *TouchedAddressesData`: Tạo một đối tượng `TouchedAddressesData` mới với số block và danh sách địa chỉ được cung cấp.

### Các phương thức

- `BlockNumber() uint64`: Trả về số block.
- `Addresses() []common.Address`: Trả về danh sách các địa chỉ.
- `Proto() *pb.TouchedAddressesData`: Chuyển đổi `TouchedAddressesData` thành đối tượng Protobuf.
- `FromProto(pbTad *pb.TouchedAddressesData)`: Khởi tạo `TouchedAddressesData` từ một đối tượng Protobuf.
- `Unmarshal(data []byte) error`: Khởi tạo `TouchedAddressesData` từ một slice byte đã được mã hóa.
- `Marshal() ([]byte, error)`: Chuyển đổi `TouchedAddressesData` thành một slice byte để lưu trữ hoặc truyền tải.

## Kết luận

File `touched_addresses_data.go` cung cấp các phương thức cần thiết để quản lý và chuyển đổi dữ liệu về các địa chỉ đã được touch trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi dữ liệu giữa các định dạng khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`smart_contract_db.go`](./smart_contract_db/smart_contract_db.go)

## Giới thiệu

File `smart_contract_db.go` định nghĩa cấu trúc `SmartContractDB` và các phương thức liên quan để quản lý cơ sở dữ liệu của smart contract trong blockchain. Nó bao gồm các chức năng để lưu trữ, truy xuất mã và dữ liệu lưu trữ của smart contract, cũng như quản lý các cập nhật và sự kiện liên quan.

## Cấu trúc `SmartContractDB`

### Thuộc tính

- `cacheRemoteDBs`: Bộ nhớ đệm cho các kết nối cơ sở dữ liệu từ xa, sử dụng `otter.Cache`.
- `cacheCode`: Bộ nhớ đệm cho mã của smart contract, sử dụng `otter.Cache`.
- `cacheStorageTrie`: Bộ nhớ đệm cho cây Merkle Patricia Trie của lưu trữ smart contract, sử dụng `otter.Cache`.
- `messageSender`: Đối tượng gửi tin nhắn thuộc kiểu `t_network.MessageSender`.
- `dnsLink`: Chuỗi liên kết DNS.
- `accountStateDB`: Cơ sở dữ liệu trạng thái tài khoản thuộc kiểu `AccountStateDB`.
- `currentBlockNumber`: Số block hiện tại thuộc kiểu `uint64`.
- `updateDatas`: Map lưu trữ dữ liệu cập nhật của smart contract với địa chỉ là key và `SmartContractUpdateData` là value.

### Hàm khởi tạo

- `NewSmartContractDB(messageSender t_network.MessageSender, dnsLink string, accountStateDB AccountStateDB, currentBlockNumber uint64) *SmartContractDB`: Tạo một đối tượng `SmartContractDB` mới với các thông tin được cung cấp.

### Các phương thức

- `SetAccountStateDB(asdb types.AccountStateDB)`: Đặt cơ sở dữ liệu trạng thái tài khoản.
- `CreateRemoteStorageDB(as types.AccountState) (RemoteStorageDB, error)`: Tạo một kết nối cơ sở dữ liệu từ xa mới.
- `Code(address common.Address) []byte`: Trả về mã của smart contract tại địa chỉ được cung cấp.
- `StorageValue(address common.Address, key []byte) []byte`: Trả về giá trị lưu trữ của smart contract tại địa chỉ và khóa được cung cấp.
- `SetBlockNumber(blockNumber uint64)`: Đặt số block hiện tại.
- `SetCode(address common.Address, codeHash common.Hash, code []byte)`: Đặt mã cho smart contract tại địa chỉ được cung cấp.
- `SetStorageValue(address common.Address, key []byte, value []byte) error`: Đặt giá trị lưu trữ cho smart contract tại địa chỉ và khóa được cung cấp.
- `AddEventLogs(eventLogs []types.EventLog)`: Thêm các log sự kiện vào dữ liệu cập nhật của smart contract.
- `NewTrieStorage(address common.Address) common.Hash`: Tạo một cây Merkle Patricia Trie mới cho lưu trữ smart contract.
- `StorageRoot(address common.Address) common.Hash`: Trả về hash của gốc lưu trữ cho smart contract tại địa chỉ được cung cấp.
- `DeleteAddress(address common.Address)`: Xóa địa chỉ khỏi bộ nhớ đệm.
- `GetSmartContractUpdateDatas() map[common.Address]types.SmartContractUpdateData`: Trả về map dữ liệu cập nhật của smart contract.
- `ClearSmartContractUpdateDatas()`: Xóa tất cả dữ liệu cập nhật của smart contract.

## Kết luận

File `smart_contract_db.go` cung cấp các phương thức cần thiết để quản lý cơ sở dữ liệu của smart contract trong blockchain. Nó hỗ trợ việc lưu trữ, truy xuất và cập nhật dữ liệu của smart contract, giúp dễ dàng quản lý và truy xuất thông tin liên quan.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`stake_abi.go`](./stake/stake_abi.go)

## Giới thiệu

File `stake_abi.go` định nghĩa hàm `StakeABI` để cung cấp giao diện nhị phân ứng dụng (ABI) cho các hàm liên quan đến stake trong smart contract. ABI là một tiêu chuẩn để tương tác với các smart contract trên Ethereum, cho phép mã hóa và giải mã các lời gọi hàm và dữ liệu.

## Hàm `StakeABI`

### Mô tả

- `StakeABI() abi.ABI`: Hàm này trả về một đối tượng `abi.ABI` được khởi tạo từ một chuỗi JSON mô tả các hàm có sẵn trong smart contract liên quan đến stake.

### Chi tiết

- **Hàm `getStakeInfo`**:
  - **Inputs**: Nhận một địa chỉ (`address`) làm tham số đầu vào.
  - **Outputs**: Trả về thông tin stake bao gồm:
    - `owner`: Địa chỉ của chủ sở hữu.
    - `amount`: Số lượng stake.
    - `childNodes`: Danh sách các địa chỉ node con.
    - `childExecuteMiners`: Danh sách các địa chỉ thợ mỏ thực thi.
    - `childVerifyMiners`: Danh sách các địa chỉ thợ mỏ xác minh.
  - **State Mutability**: `view` (chỉ đọc, không thay đổi trạng thái blockchain).

- **Hàm `getValidatorsWithStakeAmount`**:
  - **Inputs**: Không có tham số đầu vào.
  - **Outputs**: Trả về danh sách các địa chỉ và số lượng stake tương ứng.
    - `addresses`: Danh sách các địa chỉ.
    - `amounts`: Danh sách số lượng stake tương ứng với các địa chỉ.
  - **State Mutability**: `view` (chỉ đọc, không thay đổi trạng thái blockchain).

## Kết luận

File `stake_abi.go` cung cấp một cách để mã hóa và giải mã các lời gọi hàm liên quan đến stake trong smart contract. Điều này giúp dễ dàng tương tác với các smart contract từ các ứng dụng Go, đảm bảo rằng dữ liệu được truyền tải chính xác và hiệu quả.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`stake_getter.go`](./stake/stake_getter.go)

## Giới thiệu

File `stake_getter.go` định nghĩa cấu trúc `StakeGetter` và các phương thức liên quan để truy xuất thông tin stake từ blockchain. Nó sử dụng các giao diện của MVM (Meta Virtual Machine) để gọi các hàm smart contract và lấy dữ liệu về stake.

## Cấu trúc `StakeGetter`

### Thuộc tính

- `accountStatesDB`: Cơ sở dữ liệu trạng thái tài khoản thuộc kiểu `mvm.AccountStateDB`.
- `smartContractDB`: Cơ sở dữ liệu smart contract thuộc kiểu `mvm.SmartContractDB`.

### Hàm khởi tạo

- `NewStakeGetter(accountStatesDB mvm.AccountStateDB, smartContractDB mvm.SmartContractDB) *StakeGetter`: Tạo một đối tượng `StakeGetter` mới với cơ sở dữ liệu trạng thái tài khoản và smart contract được cung cấp.

### Các phương thức

- `checkMvm() (*mvm.MVMApi, error)`: Kiểm tra và khởi tạo API MVM nếu cần thiết.
- `GetValidatorsWithStakeAmount() (map[common.Address]*big.Int, error)`: Lấy danh sách các validator và số lượng stake tương ứng.
- `GetStakeInfo(nodeAddress common.Address) (*StakeInfo, error)`: Lấy thông tin stake cho một địa chỉ node cụ thể.

## Kết luận

File `stake_getter.go` cung cấp các phương thức cần thiết để truy xuất thông tin stake từ blockchain. Nó sử dụng MVM để gọi các hàm smart contract và lấy dữ liệu, giúp dễ dàng quản lý và truy xuất thông tin stake.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`stake_info.go`](./stake/stake_info.go)

## Giới thiệu

File `stake_info.go` định nghĩa cấu trúc `StakeInfo` và các phương thức liên quan để quản lý thông tin stake trong blockchain. Thông tin stake bao gồm chủ sở hữu, số lượng stake, và danh sách các node con và miner liên quan.

## Cấu trúc `StakeInfo`

### Thuộc tính

- `owner`: Địa chỉ của chủ sở hữu thuộc kiểu `common.Address`.
- `amount`: Số lượng stake thuộc kiểu `*big.Int`.
- `childNodes`: Danh sách các địa chỉ node con thuộc kiểu `[]common.Address`.
- `childExecuteMiners`: Danh sách các địa chỉ miner thực thi thuộc kiểu `[]common.Address`.
- `childVerifyMiners`: Danh sách các địa chỉ miner xác minh thuộc kiểu `[]common.Address`.

### Hàm khởi tạo

- `NewStakeInfo(owner common.Address, amount *big.Int, childNodes []common.Address, childExecuteMiners []common.Address, childVerifyMiners []common.Address) *StakeInfo`: Tạo một đối tượng `StakeInfo` mới với các thông tin được cung cấp.

### Các phương thức

- `Owner() common.Address`: Trả về địa chỉ của chủ sở hữu.
- `Amount() *big.Int`: Trả về số lượng stake.
- `ChildNodes() []common.Address`: Trả về danh sách các địa chỉ node con.
- `ChildExecuteMiners() []common.Address`: Trả về danh sách các địa chỉ miner thực thi.
- `ChildVerifyMiners() []common.Address`: Trả về danh sách các địa chỉ miner xác minh.

## Kết luận

File `stake_info.go` cung cấp các phương thức cần thiết để quản lý thông tin stake trong blockchain. Nó hỗ trợ việc tạo, truy xuất thông tin về chủ sở hữu, số lượng stake, và các node con và miner liên quan.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`stake_smart_contract_db.go`](./stake/stake_smart_contract_db.go)

## Giới thiệu

File `stake_smart_contract_db.go` định nghĩa cấu trúc `StakeSmartContractDb` và các phương thức liên quan để quản lý cơ sở dữ liệu của smart contract liên quan đến stake trong blockchain. Nó bao gồm các chức năng để lưu trữ, truy xuất mã và dữ liệu lưu trữ của smart contract, cũng như quản lý các cập nhật và sự kiện liên quan.

## Cấu trúc `StakeSmartContractDb`

### Thuộc tính

- `codePath`: Đường dẫn tới file chứa mã của smart contract.
- `storageRootPath`: Đường dẫn tới file chứa root của lưu trữ.
- `storageDBPath`: Đường dẫn tới cơ sở dữ liệu lưu trữ.
- `checkPointPath`: Đường dẫn tới file chứa checkpoint.
- `code`: Mã của smart contract thuộc kiểu `[]byte`.
- `storageTrie`: Cây Merkle Patricia Trie để quản lý lưu trữ.
- `storageDB`: Cơ sở dữ liệu lưu trữ thuộc kiểu `storage.Storage`.
- `storageRoot`: Hash của root lưu trữ thuộc kiểu `common.Hash`.
- `hasDirty`: Biến boolean để kiểm tra xem có thay đổi nào chưa được commit hay không.

### Hàm khởi tạo

- `NewStakeSmartContractDb(codePath string, storageRootPath string, storageDBPath string, checkPointPath string) (*StakeSmartContractDb, error)`: Tạo một đối tượng `StakeSmartContractDb` mới với các đường dẫn được cung cấp.

### Các phương thức

- `Code(address common.Address) []byte`: Trả về mã của smart contract.
- `StorageValue(address common.Address, key []byte) []byte`: Trả về giá trị lưu trữ của smart contract tại địa chỉ và khóa được cung cấp.
- `UpdateStorageValue(key []byte, value []byte)`: Cập nhật giá trị lưu trữ cho smart contract.
- `Commit() (common.Hash, error)`: Commit các thay đổi vào lưu trữ và trả về hash của root mới.
- `Discard()`: Hủy bỏ các thay đổi chưa được commit.
- `CopyFrom(from *StakeSmartContractDb)`: Sao chép dữ liệu từ một đối tượng `StakeSmartContractDb` khác.
- `HasDirty() bool`: Kiểm tra xem có thay đổi nào chưa được commit hay không.
- `StakeStorageDB() storage.Storage`: Trả về cơ sở dữ liệu lưu trữ.
- `StakeStorageRoot() common.Hash`: Trả về hash của root lưu trữ.
- `SaveCheckPoint(blockNumber uint64) error`: Lưu checkpoint cho block số `blockNumber`.
- `UpdateFromCheckPointData(storageRoot common.Hash, storageData [][2][]byte) error`: Cập nhật dữ liệu từ checkpoint.
- `CheckPointPath() string`: Trả về đường dẫn tới checkpoint.

## Kết luận

File `stake_smart_contract_db.go` cung cấp các phương thức cần thiết để quản lý cơ sở dữ liệu của smart contract liên quan đến stake trong blockchain. Nó hỗ trợ việc lưu trữ, truy xuất và cập nhật dữ liệu của smart contract, giúp dễ dàng quản lý và truy xuất thông tin liên quan.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`account_state.go`](./state/account_state.go)

## Giới thiệu

File `account_state.go` định nghĩa cấu trúc `AccountState` và các phương thức liên quan để quản lý trạng thái tài khoản trong blockchain. Nó bao gồm thông tin về địa chỉ, số dư, khóa thiết bị, và trạng thái của smart contract liên quan.

## Cấu trúc `AccountState`

### Thuộc tính

- `address`: Địa chỉ của tài khoản thuộc kiểu `common.Address`.
- `lastHash`: Hash cuối cùng của tài khoản thuộc kiểu `common.Hash`.
- `balance`: Số dư của tài khoản thuộc kiểu `*big.Int`.
- `pendingBalance`: Số dư đang chờ xử lý thuộc kiểu `*big.Int`.
- `deviceKey`: Khóa thiết bị thuộc kiểu `common.Hash`.
- `smartContractState`: Trạng thái của smart contract liên quan thuộc kiểu `types.SmartContractState`.

### Hàm khởi tạo

- `NewAccountState(address common.Address) types.AccountState`: Tạo một đối tượng `AccountState` mới với địa chỉ được cung cấp.

### Các phương thức

- `Proto() *pb.AccountState`: Chuyển đổi `AccountState` thành đối tượng Protobuf.
- `FromProto(pbData *pb.AccountState)`: Khởi tạo `AccountState` từ một đối tượng Protobuf.
- `Marshal() ([]byte, error)`: Chuyển đổi `AccountState` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(b []byte) error`: Khởi tạo `AccountState` từ một slice byte đã được mã hóa.
- `Copy() types.AccountState`: Tạo một bản sao của `AccountState`.
- `String() string`: Trả về chuỗi mô tả của `AccountState`.
- `Address() common.Address`: Trả về địa chỉ của tài khoản.
- `Balance() *big.Int`: Trả về số dư của tài khoản.
- `PendingBalance() *big.Int`: Trả về số dư đang chờ xử lý.
- `TotalBalance() *big.Int`: Tính toán và trả về tổng số dư.
- `LastHash() common.Hash`: Trả về hash cuối cùng của tài khoản.
- `SmartContractState() types.SmartContractState`: Trả về trạng thái của smart contract liên quan.
- `DeviceKey() common.Hash`: Trả về khóa thiết bị.
- `SetBalance(newBalance *big.Int)`: Đặt số dư mới cho tài khoản.
- `SetNewDeviceKey(newDeviceKey common.Hash)`: Đặt khóa thiết bị mới.
- `SetLastHash(newLastHash common.Hash)`: Đặt hash cuối cùng mới.
- `SetSmartContractState(smState types.SmartContractState)`: Đặt trạng thái smart contract mới.
- `AddPendingBalance(amount *big.Int)`: Thêm số dư đang chờ xử lý.
- `SubPendingBalance(amount *big.Int) error`: Trừ số dư đang chờ xử lý.
- `SubBalance(amount *big.Int) error`: Trừ số dư.
- `SubTotalBalance(amount *big.Int) error`: Trừ tổng số dư.
- `AddBalance(amount *big.Int)`: Thêm số dư.
- `GetOrCreateSmartContractState() types.SmartContractState`: Lấy hoặc tạo trạng thái smart contract.
- `SetCodeHash(hash common.Hash)`: Đặt hash mã cho smart contract.
- `SetStorageAddress(storageAddress common.Address)`: Đặt địa chỉ lưu trữ cho smart contract.
- `SetStorageRoot(hash common.Hash)`: Đặt root lưu trữ cho smart contract.
- `SetCreatorPublicKey(creatorPublicKey p_common.PublicKey)`: Đặt khóa công khai của người tạo.
- `AddLogHash(hash common.Hash)`: Thêm hash log.
- `SetPendingBalance(newBalance *big.Int)`: Đặt số dư đang chờ xử lý mới.

## Kết luận

File `account_state.go` cung cấp các phương thức cần thiết để quản lý trạng thái tài khoản trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi trạng thái tài khoản giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`smart_contract_state.go`](./state/smart_contract_state.go)

## Giới thiệu

File `smart_contract_state.go` định nghĩa cấu trúc `SmartContractState` và các phương thức liên quan để quản lý trạng thái của smart contract trong blockchain. Nó bao gồm thông tin về khóa công khai của người tạo, địa chỉ lưu trữ, mã hash, và các log liên quan.

## Cấu trúc `SmartContractState`

### Thuộc tính

- `createPublicKey`: Khóa công khai của người tạo thuộc kiểu `p_common.PublicKey`.
- `storageAddress`: Địa chỉ lưu trữ thuộc kiểu `common.Address`.
- `codeHash`: Hash của mã thuộc kiểu `common.Hash`.
- `storageRoot`: Hash của root lưu trữ thuộc kiểu `common.Hash`.
- `logsHash`: Hash của các log thuộc kiểu `common.Hash`.

### Hàm khởi tạo

- `NewSmartContractState(createPublicKey p_common.PublicKey, storageAddress common.Address, codeHash common.Hash, storageRoot common.Hash, logsHash common.Hash) types.SmartContractState`: Tạo một đối tượng `SmartContractState` mới với các thông tin được cung cấp.
- `NewEmptySmartContractState() types.SmartContractState`: Tạo một đối tượng `SmartContractState` trống.

### Các phương thức

- `Proto() *pb.SmartContractState`: Chuyển đổi `SmartContractState` thành đối tượng Protobuf.
- `Marshal() ([]byte, error)`: Chuyển đổi `SmartContractState` thành một slice byte để lưu trữ hoặc truyền tải.
- `FromProto(pbData *pb.SmartContractState)`: Khởi tạo `SmartContractState` từ một đối tượng Protobuf.
- `Unmarshal(b []byte) error`: Khởi tạo `SmartContractState` từ một slice byte đã được mã hóa.
- `String() string`: Trả về chuỗi mô tả của `SmartContractState`.
- `CreatorPublicKey() p_common.PublicKey`: Trả về khóa công khai của người tạo.
- `CreatorAddress() common.Address`: Trả về địa chỉ của người tạo.
- `StorageAddress() common.Address`: Trả về địa chỉ lưu trữ.
- `CodeHash() common.Hash`: Trả về hash của mã.
- `StorageRoot() common.Hash`: Trả về hash của root lưu trữ.
- `LogsHash() common.Hash`: Trả về hash của các log.
- `SetCreatorPublicKey(pk p_common.PublicKey)`: Đặt khóa công khai của người tạo.
- `SetStorageAddress(address common.Address)`: Đặt địa chỉ lưu trữ.
- `SetCodeHash(hash common.Hash)`: Đặt hash của mã.
- `SetStorageRoot(hash common.Hash)`: Đặt hash của root lưu trữ.
- `SetLogsHash(hash common.Hash)`: Đặt hash của các log.
- `Copy() types.SmartContractState`: Tạo một bản sao của `SmartContractState`.

## Kết luận

File `smart_contract_state.go` cung cấp các phương thức cần thiết để quản lý trạng thái của smart contract trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi trạng thái smart contract giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`update_state_fields.go`](./state/update_state_fields.go)

## Giới thiệu

File `update_state_fields.go` định nghĩa cấu trúc `UpdateField` và `UpdateStateFields` cùng các phương thức liên quan để quản lý các trường cập nhật trạng thái trong blockchain. Nó bao gồm các phương thức để tạo, chuyển đổi, và quản lý các trường cập nhật.

## Cấu trúc `UpdateField`

### Thuộc tính

- `field`: Trường cập nhật thuộc kiểu `pb.UPDATE_STATE_FIELD`.
- `value`: Giá trị của trường cập nhật thuộc kiểu `[]byte`.

### Hàm khởi tạo

- `NewUpdateField(field pb.UPDATE_STATE_FIELD, value []byte) *UpdateField`: Tạo một đối tượng `UpdateField` mới với trường và giá trị được cung cấp.

### Các phương thức

- `Field() pb.UPDATE_STATE_FIELD`: Trả về trường cập nhật.
- `Value() []byte`: Trả về giá trị của trường cập nhật.

## Cấu trúc `UpdateStateFields`

### Thuộc tính

- `address`: Địa chỉ liên quan đến các trường cập nhật thuộc kiểu `e_common.Address`.
- `fields`: Danh sách các trường cập nhật thuộc kiểu `[]types.UpdateField`.

### Hàm khởi tạo

- `NewUpdateStateFields(address e_common.Address) types.UpdateStateFields`: Tạo một đối tượng `UpdateStateFields` mới với địa chỉ được cung cấp.

### Các phương thức

- `AddField(field pb.UPDATE_STATE_FIELD, value []byte)`: Thêm một trường cập nhật vào danh sách.
- `Address() e_common.Address`: Trả về địa chỉ liên quan đến các trường cập nhật.
- `Fields() []types.UpdateField`: Trả về danh sách các trường cập nhật.
- `Unmarshal(data []byte) error`: Khởi tạo `UpdateStateFields` từ một slice byte đã được mã hóa.
- `Marshal() ([]byte, error)`: Chuyển đổi `UpdateStateFields` thành một slice byte để lưu trữ hoặc truyền tải.
- `String() string`: Trả về chuỗi mô tả của `UpdateStateFields`.
- `Proto() protoreflect.ProtoMessage`: Chuyển đổi `UpdateStateFields` thành đối tượng Protobuf.
- `FromProto(pbData protoreflect.ProtoMessage)`: Khởi tạo `UpdateStateFields` từ một đối tượng Protobuf.

## Kết luận

File `update_state_fields.go` cung cấp các phương thức cần thiết để quản lý các trường cập nhật trạng thái trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi các trường cập nhật giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`stats.go`](./stats/stats.go)

## Giới thiệu

File `stats.go` định nghĩa cấu trúc `Stats` và các phương thức liên quan để thu thập và quản lý thông tin thống kê của hệ thống blockchain. Nó bao gồm các thông tin về bộ nhớ, số lượng goroutine, thời gian hoạt động, và trạng thái của mạng và cơ sở dữ liệu.

## Cấu trúc `Stats`

### Thuộc tính

- `PbStats`: Đối tượng Protobuf `pb.Stats` chứa thông tin thống kê.

### Hàm khởi tạo

- `GetStats(startTime time.Time, levelDbs []*storage.LevelDB, connectionManager network.ConnectionsManager) *Stats`: Tạo một đối tượng `Stats` mới với thông tin thống kê được thu thập từ thời gian bắt đầu, danh sách cơ sở dữ liệu LevelDB, và trình quản lý kết nối mạng.

### Các phương thức

- `String() string`: Trả về chuỗi mô tả của `Stats`, bao gồm thông tin về bộ nhớ, số lượng goroutine, thời gian hoạt động, và trạng thái của mạng và cơ sở dữ liệu.
- `Unmarshal(b []byte) error`: Khởi tạo `Stats` từ một slice byte đã được mã hóa.
- `Marshal() ([]byte, error)`: Chuyển đổi `Stats` thành một slice byte để lưu trữ hoặc truyền tải.

## Kết luận

File `stats.go` cung cấp các phương thức cần thiết để thu thập và quản lý thông tin thống kê của hệ thống blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi thông tin thống kê giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`badger_db.go`](./storage/badger_db.go)

## Giới thiệu

File `badger_db.go` định nghĩa cấu trúc `BadgerDB` và các phương thức liên quan để quản lý cơ sở dữ liệu sử dụng BadgerDB. BadgerDB là một cơ sở dữ liệu key-value hiệu suất cao, được sử dụng để lưu trữ và truy xuất dữ liệu trong blockchain.

## Cấu trúc `BadgerDB`

### Thuộc tính

- `db`: Đối tượng BadgerDB thuộc kiểu `*badger.DB`.
- `closed`: Biến boolean để kiểm tra trạng thái đóng/mở của cơ sở dữ liệu.
- `path`: Đường dẫn tới thư mục lưu trữ dữ liệu của BadgerDB.
- `mu`: Đối tượng khóa để đồng bộ hóa truy cập dữ liệu.

### Hàm khởi tạo

- `NewBadgerDB(path string) (*BadgerDB, error)`: Tạo một đối tượng `BadgerDB` mới với đường dẫn được cung cấp.

### Các phương thức

- `Get(key []byte) ([]byte, error)`: Lấy giá trị từ cơ sở dữ liệu với khóa được cung cấp.
- `Put(key, value []byte) error`: Lưu trữ giá trị với khóa được cung cấp vào cơ sở dữ liệu.
- `Has(key []byte) bool`: Kiểm tra sự tồn tại của khóa trong cơ sở dữ liệu.
- `Delete(key []byte) error`: Xóa giá trị với khóa được cung cấp khỏi cơ sở dữ liệu.
- `BatchPut(kvs [][2][]byte) error`: Lưu trữ nhiều cặp khóa-giá trị vào cơ sở dữ liệu.
- `Open() error`: Mở cơ sở dữ liệu nếu nó đang bị đóng.
- `Close() error`: Đóng cơ sở dữ liệu.
- `GetSnapShot() SnapShot`: Tạo một bản sao của cơ sở dữ liệu.
- `GetIterator() IIterator`: Trả về một iterator để duyệt qua các cặp khóa-giá trị trong cơ sở dữ liệu.
- `Release()`: Giải phóng tài nguyên của cơ sở dữ liệu.

## Kết luận

File `badger_db.go` cung cấp các phương thức cần thiết để quản lý cơ sở dữ liệu sử dụng BadgerDB. Nó hỗ trợ việc lưu trữ, truy xuất, và quản lý dữ liệu một cách hiệu quả, giúp dễ dàng lưu trữ và truy xuất thông tin trong blockchain.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`leveldb.go`](./storage/leveldb.go)

## Giới thiệu

File `leveldb.go` định nghĩa cấu trúc `LevelDB` và các phương thức liên quan để quản lý cơ sở dữ liệu sử dụng LevelDB. LevelDB là một cơ sở dữ liệu key-value hiệu suất cao, được sử dụng để lưu trữ và truy xuất dữ liệu trong blockchain.

## Cấu trúc `LevelDB`

### Thuộc tính

- `db`: Đối tượng LevelDB thuộc kiểu `*leveldb.DB`.
- `closed`: Biến boolean để kiểm tra trạng thái đóng/mở của cơ sở dữ liệu.
- `path`: Đường dẫn tới thư mục lưu trữ dữ liệu của LevelDB.
- `closeChan`: Kênh để quản lý việc đóng cơ sở dữ liệu.

### Hàm khởi tạo

- `NewLevelDB(path string) (*LevelDB, error)`: Tạo một đối tượng `LevelDB` mới với đường dẫn được cung cấp.

### Các phương thức

- `Get(key []byte) ([]byte, error)`: Lấy giá trị từ cơ sở dữ liệu với khóa được cung cấp.
- `Put(key, value []byte) error`: Lưu trữ giá trị với khóa được cung cấp vào cơ sở dữ liệu.
- `Has(key []byte) bool`: Kiểm tra sự tồn tại của khóa trong cơ sở dữ liệu.
- `Delete(key []byte) error`: Xóa giá trị với khóa được cung cấp khỏi cơ sở dữ liệu.
- `BatchPut(kvs [][2][]byte) error`: Lưu trữ nhiều cặp khóa-giá trị vào cơ sở dữ liệu.
- `Open() error`: Mở cơ sở dữ liệu nếu nó đang bị đóng.
- `Close() error`: Đóng cơ sở dữ liệu.
- `Compact() error`: Thực hiện nén dữ liệu trong cơ sở dữ liệu.
- `GetSnapShot() SnapShot`: Tạo một bản sao của cơ sở dữ liệu.
- `GetIterator() IIterator`: Trả về một iterator để duyệt qua các cặp khóa-giá trị trong cơ sở dữ liệu.

## Kết luận

File `leveldb.go` cung cấp các phương thức cần thiết để quản lý cơ sở dữ liệu sử dụng LevelDB. Nó hỗ trợ việc lưu trữ, truy xuất, và quản lý dữ liệu một cách hiệu quả, giúp dễ dàng lưu trữ và truy xuất thông tin trong blockchain.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`memorydb.go`](./storage/memorydb.go)

## Giới thiệu

File `memorydb.go` định nghĩa cấu trúc `MemoryDB` và các phương thức liên quan để quản lý cơ sở dữ liệu trong bộ nhớ. `MemoryDB` là một cơ sở dữ liệu key-value đơn giản, được sử dụng để lưu trữ và truy xuất dữ liệu trong bộ nhớ.

## Cấu trúc `MemoryDB`

### Thuộc tính

- `db`: Map lưu trữ dữ liệu với khóa là mảng byte 32 phần tử và giá trị là slice byte.
- `RWMutex`: Đối tượng khóa để đồng bộ hóa truy cập dữ liệu.

### Hàm khởi tạo

- `NewMemoryDb() *MemoryDB`: Tạo một đối tượng `MemoryDB` mới.

### Các phương thức

- `Get(key []byte) ([]byte, error)`: Lấy giá trị từ cơ sở dữ liệu với khóa được cung cấp.
- `Put(key, value []byte) error`: Lưu trữ giá trị với khóa được cung cấp vào cơ sở dữ liệu.
- `Has(key []byte) bool`: Kiểm tra sự tồn tại của khóa trong cơ sở dữ liệu.
- `Delete(key []byte) error`: Xóa giá trị với khóa được cung cấp khỏi cơ sở dữ liệu.
- `BatchPut(kvs [][2][]byte) error`: Lưu trữ nhiều cặp khóa-giá trị vào cơ sở dữ liệu.
- `Close() error`: Đóng cơ sở dữ liệu (hiện tại đang không thực hiện gì trong `MemoryDB`).
- `Open() error`: Mở cơ sở dữ liệu (hiện tại đang không thực hiện gì trong `MemoryDB`).
- `Compact() error`: Thực hiện nén dữ liệu trong cơ sở dữ liệu (hiện tại đang không thực hiện gì trong `MemoryDB`).
- `Size() int`: Trả về kích thước của cơ sở dữ liệu.
- `GetSnapShot() SnapShot`: Tạo một bản sao của cơ sở dữ liệu.
- `GetIterator() IIterator`: Trả về một iterator để duyệt qua các cặp khóa-giá trị trong cơ sở dữ liệu.
- `Release()`: Giải phóng tài nguyên của cơ sở dữ liệu (hiện tại đang không thực hiện gì trong `MemoryDB`).

## Kết luận

File `memorydb.go` cung cấp các phương thức cần thiết để quản lý cơ sở dữ liệu trong bộ nhớ. Nó hỗ trợ việc lưu trữ, truy xuất, và quản lý dữ liệu một cách đơn giản, giúp dễ dàng lưu trữ và truy xuất thông tin trong bộ nhớ.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`node_sync_data.go`](./sync/node_sync_data.go)

## Giới thiệu

File `node_sync_data.go` định nghĩa các cấu trúc và phương thức liên quan đến việc đồng bộ dữ liệu node trong blockchain. Nó bao gồm các cấu trúc `GetNodeSyncData` và `NodeSyncData`, cùng với các phương thức để chuyển đổi giữa các định dạng dữ liệu khác nhau và quản lý thông tin đồng bộ.

## Cấu trúc `GetNodeSyncData`

### Thuộc tính

- `latestCheckPointBlockNumber`: Số block checkpoint mới nhất thuộc kiểu `uint64`.
- `validatorAddress`: Địa chỉ của validator thuộc kiểu `common.Address`.
- `nodeStatesIndex`: Chỉ số trạng thái node thuộc kiểu `int`.

### Hàm khởi tạo

- `NewGetNodeSyncData(latestCheckPointBlockNumber uint64, validatorAddress common.Address, nodeStatesIndex int) *GetNodeSyncData`: Tạo một đối tượng `GetNodeSyncData` mới với các thông tin được cung cấp.

### Các phương thức

- `GetNodeSyncDataFromProto(pbData *pb.GetNodeSyncData) *GetNodeSyncData`: Khởi tạo `GetNodeSyncData` từ một đối tượng Protobuf.
- `GetNodeSyncDataToProto(data *GetNodeSyncData) *pb.GetNodeSyncData`: Chuyển đổi `GetNodeSyncData` thành đối tượng Protobuf.
- `Unmarshal(b []byte) error`: Khởi tạo `GetNodeSyncData` từ một slice byte đã được mã hóa.
- `LatestCheckPointBlockNumber() uint64`: Trả về số block checkpoint mới nhất.
- `ValidatorAddress() common.Address`: Trả về địa chỉ của validator.
- `NodeStatesIndex() int`: Trả về chỉ số trạng thái node.

## Cấu trúc `NodeSyncData`

### Thuộc tính

- `validatorAddress`: Địa chỉ của validator thuộc kiểu `common.Address`.
- `nodeStatesIndex`: Chỉ số trạng thái node thuộc kiểu `int`.
- `accountStateRoot`: Hash của root trạng thái tài khoản thuộc kiểu `common.Hash`.
- `data`: Dữ liệu lưu trữ thuộc kiểu `[][2][]byte`.
- `finished`: Trạng thái hoàn thành thuộc kiểu `bool`.

### Hàm khởi tạo

- `NewNodeSyncData(validatorAddress common.Address, nodeStatesIndex int, accountStateRoot common.Hash, data [][2][]byte, finished bool) *NodeSyncData`: Tạo một đối tượng `NodeSyncData` mới với các thông tin được cung cấp.

### Các phương thức

- `NodeSyncDataFromProto(pbData *pb.NodeSyncData) *NodeSyncData`: Khởi tạo `NodeSyncData` từ một đối tượng Protobuf.
- `NodeSyncDataToProto(data *NodeSyncData) *pb.NodeSyncData`: Chuyển đổi `NodeSyncData` thành đối tượng Protobuf.
- `Unmarshal(b []byte) error`: Khởi tạo `NodeSyncData` từ một slice byte đã được mã hóa.
- `ValidatorAddress() common.Address`: Trả về địa chỉ của validator.
- `NodeStatesIndex() int`: Trả về chỉ số trạng thái node.
- `Finished() bool`: Trả về trạng thái hoàn thành.
- `AccountStateRoot() common.Hash`: Trả về hash của root trạng thái tài khoản.
- `Data() [][2][]byte`: Trả về dữ liệu lưu trữ.

## Kết luận

File `node_sync_data.go` cung cấp các phương thức cần thiết để quản lý và đồng bộ dữ liệu node trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi dữ liệu đồng bộ giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`call_data.go`](./transaction/call_data.go)

## Giới thiệu

File `call_data.go` định nghĩa cấu trúc `CallData` và các phương thức liên quan để quản lý dữ liệu cuộc gọi trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý dữ liệu cuộc gọi cho các giao dịch.

## Cấu trúc `CallData`

### Thuộc tính

- `method`: Phương thức được gọi thuộc kiểu `string`.
- `params`: Tham số của cuộc gọi thuộc kiểu `[]byte`.

### Hàm khởi tạo

- `NewCallData(method string, params []byte) *CallData`: Tạo một đối tượng `CallData` mới với phương thức và tham số được cung cấp.

### Các phương thức

- `Method() string`: Trả về phương thức của cuộc gọi.
- `Params() []byte`: Trả về tham số của cuộc gọi.

## Kết luận

File `call_data.go` cung cấp các phương thức cần thiết để quản lý dữ liệu cuộc gọi trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý dữ liệu cuộc gọi, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`deploy_data.go`](./transaction/deploy_data.go)

## Giới thiệu

File `deploy_data.go` định nghĩa cấu trúc `DeployData` và các phương thức liên quan để quản lý dữ liệu triển khai smart contract trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý dữ liệu triển khai.

## Cấu trúc `DeployData`

### Thuộc tính

- `code`: Mã của smart contract thuộc kiểu `[]byte`.
- `initParams`: Tham số khởi tạo thuộc kiểu `[]byte`.

### Hàm khởi tạo

- `NewDeployData(code []byte, initParams []byte) *DeployData`: Tạo một đối tượng `DeployData` mới với mã và tham số khởi tạo được cung cấp.

### Các phương thức

- `Code() []byte`: Trả về mã của smart contract.
- `InitParams() []byte`: Trả về tham số khởi tạo.

## Kết luận

File `deploy_data.go` cung cấp các phương thức cần thiết để quản lý dữ liệu triển khai smart contract trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý dữ liệu triển khai, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`from_node_transaction_result.go`](./transaction/from_node_transaction_result.go)

## Giới thiệu

File `from_node_transaction_result.go` định nghĩa cấu trúc `FromNodeTransactionResult` và các phương thức liên quan để quản lý kết quả giao dịch từ node trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý kết quả giao dịch.

## Cấu trúc `FromNodeTransactionResult`

### Thuộc tính

- `status`: Trạng thái của giao dịch thuộc kiểu `bool`.
- `output`: Kết quả đầu ra của giao dịch thuộc kiểu `[]byte`.

### Hàm khởi tạo

- `NewFromNodeTransactionResult(status bool, output []byte) *FromNodeTransactionResult`: Tạo một đối tượng `FromNodeTransactionResult` mới với trạng thái và kết quả đầu ra được cung cấp.

### Các phương thức

- `Status() bool`: Trả về trạng thái của giao dịch.
- `Output() []byte`: Trả về kết quả đầu ra của giao dịch.

## Kết luận

File `from_node_transaction_result.go` cung cấp các phương thức cần thiết để quản lý kết quả giao dịch từ node trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý kết quả giao dịch, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`to_node_transaction_result.go`](./transaction/to_node_transaction_result.go)

## Giới thiệu

File `to_node_transaction_result.go` định nghĩa cấu trúc `ToNodeTransactionResult` và các phương thức liên quan để quản lý kết quả giao dịch đến node trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý kết quả giao dịch.

## Cấu trúc `ToNodeTransactionResult`

### Thuộc tính

- `success`: Trạng thái thành công của giao dịch thuộc kiểu `bool`.
- `data`: Dữ liệu kết quả của giao dịch thuộc kiểu `[]byte`.

### Hàm khởi tạo

- `NewToNodeTransactionResult(success bool, data []byte) *ToNodeTransactionResult`: Tạo một đối tượng `ToNodeTransactionResult` mới với trạng thái thành công và dữ liệu kết quả được cung cấp.

### Các phương thức

- `Success() bool`: Trả về trạng thái thành công của giao dịch.
- `Data() []byte`: Trả về dữ liệu kết quả của giao dịch.

## Kết luận

File `to_node_transaction_result.go` cung cấp các phương thức cần thiết để quản lý kết quả giao dịch đến node trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý kết quả giao dịch, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`open_state_channel_data.go`](./transaction/open_state_channel_data.go)

## Giới thiệu

File `open_state_channel_data.go` định nghĩa cấu trúc `OpenStateChannelData` và các phương thức liên quan để quản lý dữ liệu mở kênh trạng thái trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý dữ liệu mở kênh trạng thái.

## Cấu trúc `OpenStateChannelData`

### Thuộc tính

- `channelId`: ID của kênh trạng thái thuộc kiểu `string`.
- `participants`: Danh sách các bên tham gia thuộc kiểu `[]common.Address`.

### Hàm khởi tạo

- `NewOpenStateChannelData(channelId string, participants []common.Address) *OpenStateChannelData`: Tạo một đối tượng `OpenStateChannelData` mới với ID kênh và danh sách các bên tham gia được cung cấp.

### Các phương thức

- `ChannelId() string`: Trả về ID của kênh trạng thái.
- `Participants() []common.Address`: Trả về danh sách các bên tham gia.

## Kết luận

File `open_state_channel_data.go` cung cấp các phương thức cần thiết để quản lý dữ liệu mở kênh trạng thái trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý dữ liệu mở kênh trạng thái, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`execute_sc_transactions.go`](./transaction/execute_sc_transactions.go)

## Giới thiệu

File `execute_sc_transactions.go` định nghĩa cấu trúc `ExecuteSCTransactions` và các phương thức liên quan để quản lý việc thực thi các giao dịch smart contract trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý việc thực thi giao dịch smart contract.

## Cấu trúc `ExecuteSCTransactions`

### Thuộc tính

- `transactions`: Danh sách các giao dịch smart contract thuộc kiểu `[]types.Transaction`.
- `results`: Kết quả thực thi của các giao dịch thuộc kiểu `[]types.ExecuteSCResult`.

### Hàm khởi tạo

- `NewExecuteSCTransactions(transactions []types.Transaction, results []types.ExecuteSCResult) *ExecuteSCTransactions`: Tạo một đối tượng `ExecuteSCTransactions` mới với danh sách giao dịch và kết quả thực thi được cung cấp.

### Các phương thức

- `Transactions() []types.Transaction`: Trả về danh sách các giao dịch smart contract.
- `Results() []types.ExecuteSCResult`: Trả về kết quả thực thi của các giao dịch.

## Kết luận

File `execute_sc_transactions.go` cung cấp các phương thức cần thiết để quản lý việc thực thi các giao dịch smart contract trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý việc thực thi giao dịch smart contract, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`transactions_from_leader.go`](./transaction/transactions_from_leader.go)

## Giới thiệu

File `transactions_from_leader.go` định nghĩa cấu trúc `TransactionsFromLeader` và các phương thức liên quan để quản lý các giao dịch được gửi từ leader trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, mã hóa, giải mã, và xác thực chữ ký của các giao dịch từ leader.

## Cấu trúc `TransactionsFromLeader`

### Thuộc tính

- `transactions`: Danh sách các giao dịch thuộc kiểu `types.Transaction`.
- `blockNumber`: Số thứ tự của block, thuộc kiểu `uint64`.
- `aggSign`: Chữ ký tổng hợp của các giao dịch, thuộc kiểu `[]byte`.
- `timeStamp`: Dấu thời gian của các giao dịch, thuộc kiểu `uint64`.

### Hàm khởi tạo

- `NewTransactionsFromLeader(transactions []types.Transaction, blockNumber uint64, aggSign []byte, timeStamp uint64) *TransactionsFromLeader`: Tạo một đối tượng `TransactionsFromLeader` mới với danh sách giao dịch, số thứ tự block, chữ ký tổng hợp và dấu thời gian được cung cấp.

### Các phương thức

- `Transactions() []types.Transaction`: Trả về danh sách các giao dịch.
- `BlockNumber() uint64`: Trả về số thứ tự của block.
- `AggSign() []byte`: Trả về chữ ký tổng hợp của các giao dịch.
- `TimeStamp() uint64`: Trả về dấu thời gian của các giao dịch.
- `Marshal() ([]byte, error)`: Mã hóa `TransactionsFromLeader` thành một slice byte.
- `Unmarshal(b []byte) error`: Giải mã `TransactionsFromLeader` từ một slice byte.
- `IsValidSign() bool`: Kiểm tra tính hợp lệ của chữ ký tổng hợp.
- `Proto() *pb.TransactionsFromLeader`: Chuyển đổi `TransactionsFromLeader` thành đối tượng Protobuf.
- `FromProto(pbData *pb.TransactionsFromLeader)`: Khởi tạo `TransactionsFromLeader` từ một đối tượng Protobuf.

## Kết luận

File `transactions_from_leader.go` cung cấp các phương thức cần thiết để quản lý các giao dịch được gửi từ leader trong blockchain. Nó hỗ trợ việc mã hóa, giải mã, và xác thực chữ ký của các giao dịch, đồng thời cung cấp các công cụ để chuyển đổi giữa các định dạng dữ liệu khác nhau.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`verify_transaction_sign.go`](./transaction/verify_transaction_sign.go)

## Giới thiệu

File `verify_transaction_sign.go` định nghĩa các cấu trúc và phương thức liên quan để quản lý việc xác thực chữ ký của các giao dịch trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, mã hóa, giải mã, và xác thực chữ ký của các giao dịch.

## Cấu trúc

### Thuộc tính

- `transactionId`: ID của giao dịch cần xác thực.
- `publicKeys`: Danh sách các khóa công khai liên quan đến giao dịch.
- `signatures`: Danh sách các chữ ký của giao dịch.
- `hash`: Hash của giao dịch cần xác thực.

### Hàm khởi tạo

- `NewVerifyTransactionSign(transactionId string, publicKeys []cm.PublicKey, signatures []cm.Sign, hash common.Hash) *VerifyTransactionSign`: Tạo một đối tượng `VerifyTransactionSign` mới với các thông tin được cung cấp.

### Các phương thức

- `Verify() bool`: Xác thực chữ ký của giao dịch dựa trên các khóa công khai và hash được cung cấp. Trả về `true` nếu chữ ký hợp lệ, ngược lại trả về `false`.
- `Marshal() ([]byte, error)`: Mã hóa đối tượng `VerifyTransactionSign` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(data []byte) error`: Giải mã dữ liệu từ một slice byte và khởi tạo đối tượng `VerifyTransactionSign`.

## Kết luận

File `verify_transaction_sign.go` cung cấp các phương thức cần thiết để quản lý và xác thực chữ ký của các giao dịch trong blockchain. Nó hỗ trợ việc tạo, mã hóa, giải mã, và xác thực chữ ký, giúp đảm bảo tính toàn vẹn và an toàn của các giao dịch trong hệ thống.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`update_storage_host_data.go`](./transaction/update_storage_host_data.go)

## Giới thiệu

File `update_storage_host_data.go` định nghĩa cấu trúc `UpdateStorageHostData` và các phương thức liên quan để quản lý dữ liệu cập nhật thông tin lưu trữ của host trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý dữ liệu cập nhật thông tin lưu trữ.

## Cấu trúc `UpdateStorageHostData`

### Thuộc tính

- `storageHost`: Tên của host lưu trữ, thuộc kiểu `string`.
- `storageAddress`: Địa chỉ lưu trữ, thuộc kiểu `e_common.Address`.

### Hàm khởi tạo

- `NewUpdateStorageHostData(storageHost string, storageAddress e_common.Address) types.UpdateStorageHostData`: Tạo một đối tượng `UpdateStorageHostData` mới với thông tin host và địa chỉ lưu trữ được cung cấp.

### Các phương thức

- `Unmarshal(b []byte) error`: Giải mã dữ liệu từ một slice byte và cập nhật đối tượng `UpdateStorageHostData`.
- `Marshal() ([]byte, error)`: Mã hóa đối tượng `UpdateStorageHostData` thành một slice byte để lưu trữ hoặc truyền tải.
- `Proto() protoreflect.ProtoMessage`: Chuyển đổi đối tượng `UpdateStorageHostData` thành một đối tượng Protobuf để dễ dàng xử lý và truyền tải.

## Kết luận

File `update_storage_host_data.go` cung cấp các phương thức cần thiết để quản lý dữ liệu cập nhật thông tin lưu trữ của host trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý dữ liệu cập nhật, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`transaction.go`](./transaction/transaction.go)

## Giới thiệu

File `transaction.go` định nghĩa cấu trúc `Transaction` và các phương thức liên quan để quản lý các giao dịch trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, xác thực và quản lý dữ liệu giao dịch.

## Cấu trúc `Transaction`

### Thuộc tính

- `proto`: Đối tượng Protobuf của giao dịch, thuộc kiểu `*pb.Transaction`.

### Hàm khởi tạo

- `NewTransaction(...) types.Transaction`: Tạo một đối tượng `Transaction` mới với các thông tin được cung cấp.

### Các phương thức

#### Chuyển đổi

- `TransactionsToProto(transactions []types.Transaction) []*pb.Transaction`: Chuyển đổi danh sách giao dịch thành danh sách Protobuf.
- `TransactionFromProto(txPb *pb.Transaction) types.Transaction`: Tạo một đối tượng `Transaction` từ Protobuf.
- `TransactionsFromProto(pbTxs []*pb.Transaction) []types.Transaction`: Chuyển đổi danh sách Protobuf thành danh sách giao dịch.

#### Tổng quát

- `Unmarshal(b []byte) error`: Giải mã một giao dịch từ slice byte.
- `Marshal() ([]byte, error)`: Mã hóa giao dịch thành slice byte.
- `Proto() protoreflect.ProtoMessage`: Trả về đối tượng Protobuf của giao dịch.
- `FromProto(pbMessage protoreflect.ProtoMessage)`: Khởi tạo giao dịch từ một đối tượng Protobuf.
- `String() string`: Trả về chuỗi biểu diễn của giao dịch.

#### Xác thực

- `ValidSign() bool`: Kiểm tra tính hợp lệ của chữ ký giao dịch.
- `ValidTransactionHash() bool`: Kiểm tra tính hợp lệ của hash giao dịch.
- `ValidLastHash(fromAccountState types.AccountState) bool`: Kiểm tra tính hợp lệ của hash cuối cùng.
- `ValidDeviceKey(fromAccountState types.AccountState) bool`: Kiểm tra tính hợp lệ của khóa thiết bị.
- `ValidMaxGas() bool`: Kiểm tra tính hợp lệ của gas tối đa.
- `ValidMaxGasPrice(currentGasPrice uint64) bool`: Kiểm tra tính hợp lệ của giá gas tối đa.
- `ValidAmount(fromAccountState types.AccountState, currentGasPrice uint64) bool`: Kiểm tra tính hợp lệ của số lượng giao dịch.
- `ValidPendingUse(fromAccountState types.AccountState) bool`: Kiểm tra tính hợp lệ của số lượng đang chờ sử dụng.
- `ValidDeploySmartContractToAccount(fromAccountState types.AccountState) bool`: Kiểm tra tính hợp lệ của địa chỉ triển khai smart contract.
- `ValidOpenChannelToAccount(fromAccountState types.AccountState) bool`: Kiểm tra tính hợp lệ của địa chỉ mở kênh.
- `ValidCallSmartContractToAccount(toAccountState types.AccountState) bool`: Kiểm tra tính hợp lệ của việc gọi smart contract.

#### Mã hóa và Giải mã

- `MarshalTransactions(txs []types.Transaction) ([]byte, error)`: Mã hóa danh sách giao dịch thành slice byte.
- `UnmarshalTransactions(b []byte) ([]types.Transaction, error)`: Giải mã danh sách giao dịch từ slice byte.
- `MarshalTransactionsWithBlockNumber(txs []types.Transaction, blockNumber uint64) ([]byte, error)`: Mã hóa danh sách giao dịch cùng với số block.
- `UnmarshalTransactionsWithBlockNumber(b []byte) ([]types.Transaction, uint64, error)`: Giải mã danh sách giao dịch cùng với số block từ slice byte.

## Kết luận

File `transaction.go` cung cấp các phương thức cần thiết để quản lý và thao tác với các giao dịch trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, xác thực và quản lý dữ liệu giao dịch, giúp dễ dàng lưu trữ và truyền tải thông tin trong hệ thống.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`transaction_grouper.go`](./transaction_grouper/transaction_grouper.go)

## Giới thiệu

File `transaction_grouper.go` định nghĩa cấu trúc `TransactionGrouper` và các phương thức liên quan để quản lý việc nhóm các giao dịch trong blockchain. Mục tiêu là tổ chức các giao dịch thành các nhóm dựa trên tiền tố của địa chỉ gửi và nhận để thực thi hiệu quả hơn.

## Cấu trúc `TransactionGrouper`

### Thuộc tính

- `groups`: Mảng chứa các nhóm giao dịch, mỗi nhóm là một slice của `types.Transaction`.
- `prefix`: Tiền tố được sử dụng để xác định nhóm của giao dịch.

### Hàm khởi tạo

- `NewTransactionGrouper(prefix []byte) *TransactionGrouper`: Tạo một đối tượng `TransactionGrouper` mới với mảng nhóm trống và tiền tố được cung cấp.

### Các phương thức

- `AddFromTransactions(transactions []types.Transaction)`: Thêm các giao dịch vào các nhóm dựa trên địa chỉ gửi.
- `AddFromTransaction(transaction types.Transaction)`: Thêm một giao dịch vào nhóm dựa trên địa chỉ gửi.
- `AddToTransactions(transactions []types.Transaction)`: Thêm các giao dịch vào các nhóm dựa trên địa chỉ nhận.
- `AddToTransaction(transaction types.Transaction)`: Thêm một giao dịch vào nhóm dựa trên địa chỉ nhận.
- `GetTransactionsGroups() [16][]types.Transaction`: Trả về mảng các nhóm giao dịch.
- `HaveTransactionGroupsCount() int`: Trả về số lượng nhóm có chứa giao dịch.
- `Clear()`: Xóa tất cả các nhóm và giao dịch.

## Kết luận

File `transaction_grouper.go` cung cấp các phương thức cần thiết để quản lý và nhóm các giao dịch trong blockchain. Việc nhóm các giao dịch giúp tối ưu hóa quá trình thực thi và quản lý các giao dịch liên quan đến cùng một tiền tố địa chỉ.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`transaction_pool.go`](./transaction_pool/transaction_pool.go)

## Giới thiệu

File `transaction_pool.go` định nghĩa cấu trúc `TransactionPool` và các phương thức liên quan để quản lý một tập hợp các giao dịch trong blockchain. Mục tiêu là lưu trữ và quản lý các giao dịch, đồng thời tạo chữ ký tổng hợp cho các giao dịch này.

## Cấu trúc `TransactionPool`

### Thuộc tính

- `transactions`: Danh sách các giao dịch thuộc kiểu `types.Transaction`.
- `aggSign`: Chữ ký tổng hợp thuộc kiểu `*blst.P2Aggregate`.
- `mutex`: Đối tượng khóa (`sync.Mutex`) để đảm bảo an toàn khi truy cập đồng thời vào `transactions` và `aggSign`.

### Hàm khởi tạo

- `NewTransactionPool() *TransactionPool`: Tạo một đối tượng `TransactionPool` mới với chữ ký tổng hợp mới.

### Các phương thức

- `AddTransaction(tx types.Transaction)`: Thêm một giao dịch vào `TransactionPool`. Sử dụng khóa để đảm bảo an toàn khi truy cập đồng thời.
- `AddTransactions(txs []types.Transaction)`: Thêm nhiều giao dịch vào `TransactionPool`. Sử dụng khóa để đảm bảo an toàn khi truy cập đồng thời.
- `TransactionsWithAggSign() ([]types.Transaction, []byte)`: Trả về danh sách các giao dịch và chữ ký tổng hợp, đồng thời xóa các giao dịch khỏi pool.
- `addTransaction(tx types.Transaction)`: Phương thức nội bộ để thêm một giao dịch vào `TransactionPool` và cập nhật chữ ký tổng hợp.

## Kết luận

File `transaction_pool.go` cung cấp các phương thức cần thiết để quản lý một tập hợp các giao dịch trong blockchain. Nó hỗ trợ việc thêm và lấy các giao dịch một cách an toàn trong môi trường đa luồng, đồng thời tạo chữ ký tổng hợp cho các giao dịch này.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`committer.go`](./trie/committer.go)

## Giới thiệu

File `committer.go` định nghĩa cấu trúc `committer` và các phương thức liên quan để quản lý quá trình commit các node trong cây Merkle-Patricia. Mục tiêu là thu thập và lưu trữ các node đã thay đổi trong quá trình commit.

## Cấu trúc `committer`

### Thuộc tính

- `nodes`: Tập hợp các node đã thay đổi, thuộc kiểu `*node.NodeSet`.
- `tracer`: Công cụ theo dõi các thay đổi trong cây, thuộc kiểu `*Tracer`.
- `collectLeaf`: Cờ để xác định có thu thập các node lá hay không.

### Hàm khởi tạo

- `newCommitter(nodeset *node.NodeSet, tracer *Tracer, collectLeaf bool) *committer`: Tạo một đối tượng `committer` mới với các thông tin được cung cấp.

### Các phương thức

- `Commit(n node.Node) node.HashNode`: Commit một node và trả về node dạng hash.
- `commit(path []byte, n node.Node) node.Node`: Commit một node và trả về node đã được xử lý.
- `commitChildren(path []byte, n *node.FullNode) [17]node.Node`: Commit các node con của một `FullNode`.
- `store(path []byte, n node.Node) node.Node`: Lưu trữ node và thêm vào tập hợp các node đã thay đổi.

## Kết luận

File `committer.go` cung cấp các phương thức cần thiết để quản lý và commit các node trong cây Merkle-Patricia. Nó hỗ trợ việc thu thập và lưu trữ các node đã thay đổi, giúp dễ dàng quản lý và xử lý các thay đổi trong cây.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`hasher.go`](./trie/hasher.go)

## Giới thiệu

File `hasher.go` định nghĩa các phương thức để tính toán hash cho các node trong cây Merkle-Patricia. Mục tiêu là cung cấp các phương thức để tính toán và trả về hash của các node.

### Các phương thức

- `proofHash(original node.Node) (collapsed, hashed node.Node)`: Tính toán và trả về hash của một node, đồng thời trả về node đã được xử lý.

## Kết luận

File `hasher.go` cung cấp các phương thức cần thiết để tính toán hash cho các node trong cây Merkle-Patricia. Nó hỗ trợ việc tính toán và trả về hash của các node, giúp dễ dàng quản lý và xử lý các node trong cây.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`trie_reader.go`](./trie/trie_reader.go)

## Giới thiệu

File `trie_reader.go` định nghĩa cấu trúc `TrieReader` và các phương thức liên quan để đọc dữ liệu từ cây Merkle-Patricia. Mục tiêu là cung cấp các phương thức để truy xuất và đọc dữ liệu từ cây.

## Cấu trúc `TrieReader`

### Thuộc tính

- `db`: Cơ sở dữ liệu lưu trữ các node của cây, thuộc kiểu `trie_db.DB`.

### Hàm khởi tạo

- `newTrieReader(db trie_db.DB) (*TrieReader, error)`: Tạo một đối tượng `TrieReader` mới với cơ sở dữ liệu được cung cấp.

### Các phương thức

- `node(path []byte, hash e_common.Hash) ([]byte, error)`: Truy xuất và trả về node được mã hóa RLP từ cơ sở dữ liệu dựa trên thông tin node được cung cấp.

## Kết luận

File `trie_reader.go` cung cấp các phương thức cần thiết để đọc dữ liệu từ cây Merkle-Patricia. Nó hỗ trợ việc truy xuất và đọc dữ liệu từ cây, giúp dễ dàng quản lý và xử lý dữ liệu trong cây.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`trie.go`](./trie/trie.go)

## Giới thiệu

File `trie.go` định nghĩa cấu trúc `MerklePatriciaTrie` và các phương thức liên quan để quản lý cây Merkle-Patricia trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, truy xuất, cập nhật dữ liệu trong cây trie, cũng như tính toán hash của cây.

## Cấu trúc `MerklePatriciaTrie`

### Thuộc tính

- `root`: Node gốc của cây, thuộc kiểu `node.Node`.
- `committed`: Trạng thái cam kết của cây, thuộc kiểu `bool`.
- `reader`: Đối tượng `TrieReader` để đọc dữ liệu từ cơ sở dữ liệu.
- `tracer`: Công cụ theo dõi các thay đổi trong cây, thuộc kiểu `*Tracer`.

### Hàm khởi tạo

- `New(root e_common.Hash, db trie_db.DB) (*MerklePatriciaTrie, error)`: Tạo một đối tượng `MerklePatriciaTrie` mới với hash gốc và cơ sở dữ liệu được cung cấp.

### Các phương thức

- `Copy() *MerklePatriciaTrie`: Tạo một bản sao của cây trie hiện tại.
- `NodeIterator(start []byte) (NodeIterator, error)`: Trả về một iterator cho các node trong cây, bắt đầu từ vị trí được chỉ định.
- `Get(key []byte) ([]byte, error)`: Truy xuất giá trị tương ứng với khóa được cung cấp từ cây trie.
- `get(origNode node.Node, key []byte, pos int) (value []byte, newnode node.Node, didResolve bool, err error)`: Phương thức nội bộ để truy xuất giá trị từ cây trie.
- `insert(n node.Node, prefix, key []byte, value node.Node) (bool, node.Node, error)`: Chèn một node mới vào cây trie.
- `update(key, value []byte) error`: Cập nhật giá trị của một khóa trong cây trie.
- `delete(n node.Node, prefix, key []byte) (bool, node.Node, error)`: Xóa một node khỏi cây trie.
- `resolveAndTrack(n node.HashNode, prefix []byte) (node.Node, error)`: Tải node từ cơ sở dữ liệu và theo dõi node đã tải.
- `hashRoot() (node.Node, node.Node)`: Tính toán hash gốc của cây trie.
- `GetStorageKeys() []e_common.Hash`: Lấy danh sách các khóa lưu trữ từ cây trie.
- `String() string`: Trả về chuỗi biểu diễn của cây trie.
- `GetRootHash(data map[string][]byte) (e_common.Hash, error)`: Tính toán và trả về hash gốc của cây trie dựa trên dữ liệu được cung cấp.

## Kết luận

File `trie.go` cung cấp các phương thức cần thiết để quản lý và thao tác với cây Merkle-Patricia trong blockchain. Nó hỗ trợ việc tạo, truy xuất, cập nhật dữ liệu trong cây, cũng như tính toán hash của cây, giúp dễ dàng quản lý và xử lý dữ liệu trong hệ thống.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`block_vote.go`](./vote/block_vote.go)

## Giới thiệu

File `block_vote.go` định nghĩa cấu trúc `BlockVote` và các phương thức liên quan để quản lý việc vote cho các block trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, xác thực và chuyển đổi dữ liệu vote cho các block.

## Cấu trúc `BlockVote`

### Thuộc tính

- `blockHash`: Hash của block thuộc kiểu `common.Hash`.
- `number`: Số thứ tự của block thuộc kiểu `uint64`.
- `publicKey`: Khóa công khai của người vote thuộc kiểu `cm.PublicKey`.
- `sign`: Chữ ký của người vote thuộc kiểu `cm.Sign`.

### Hàm khởi tạo

- `NewBlockVote(blockHash common.Hash, number uint64, publicKey cm.PublicKey, sign cm.Sign) types.BlockVote`: Tạo một đối tượng `BlockVote` mới với các thông tin được cung cấp.

### Các phương thức

- `BlockHash() common.Hash`: Trả về hash của block.
- `Number() uint64`: Trả về số thứ tự của block.
- `PublicKey() cm.PublicKey`: Trả về khóa công khai của người vote.
- `Address() common.Address`: Trả về địa chỉ của người vote, được tạo từ khóa công khai.
- `Sign() cm.Sign`: Trả về chữ ký của người vote.
- `Valid() bool`: Xác thực chữ ký của người vote dựa trên khóa công khai và hash của block.
- `Marshal() ([]byte, error)`: Chuyển đổi `BlockVote` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(bData []byte) error`: Khởi tạo `BlockVote` từ một slice byte đã được mã hóa.
- `Proto() *pb.BlockVote`: Chuyển đổi `BlockVote` thành đối tượng Protobuf.
- `FromProto(v *pb.BlockVote)`: Khởi tạo `BlockVote` từ một đối tượng Protobuf.

## Kết luận

File `block_vote.go` cung cấp các phương thức cần thiết để quản lý và xác thực việc vote cho các block trong blockchain. Nó hỗ trợ việc tạo, xác thực, và chuyển đổi dữ liệu vote giữa các định dạng khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`vote_pool.go`](./vote_pool/vote_pool.go)

## Giới thiệu

File `vote_pool.go` định nghĩa cấu trúc `VotePool` và các phương thức liên quan để quản lý việc bỏ phiếu trong blockchain. Mục tiêu là theo dõi và xác thực các phiếu vote từ các địa chỉ khác nhau, đồng thời xác định kết quả dựa trên tỷ lệ chấp thuận.

## Cấu trúc `VotePool`

### Thuộc tính

- `approveRate`: Tỷ lệ chấp thuận cần thiết để một phiếu vote được coi là hợp lệ, thuộc kiểu `float64`.
- `addresses`: Map lưu trữ các địa chỉ tham gia bỏ phiếu.
- `votes`: Map lưu trữ các phiếu vote, với hash của phiếu vote là key và map các khóa công khai và chữ ký là value.
- `mapAddressVote`: Map lưu trữ hash của phiếu vote cho từng địa chỉ.
- `mapVoteAddresses`: Map lưu trữ danh sách địa chỉ đã bỏ phiếu cho mỗi hash phiếu vote.
- `voteValues`: Map lưu trữ giá trị của phiếu vote cho mỗi hash phiếu vote.
- `result`: Kết quả của phiếu vote, thuộc kiểu `*common.Hash`.
- `closed`: Trạng thái đóng của pool, thuộc kiểu `bool`.
- `voteMu`: Đối tượng khóa (`sync.RWMutex`) để đảm bảo an toàn khi truy cập đồng thời vào dữ liệu phiếu vote.

### Hàm khởi tạo

- `NewVotePool(approveRate float64, addresses map[common.Address]interface{}) *VotePool`: Tạo một đối tượng `VotePool` mới với tỷ lệ chấp thuận và danh sách địa chỉ được cung cấp.

### Các phương thức

- `AddVote(vote types.Vote) error`: Thêm một phiếu vote vào `VotePool`. Xác thực chữ ký và kiểm tra xem địa chỉ đã bỏ phiếu chưa.
- `checkVote(voteHash common.Hash) bool`: Kiểm tra xem số lượng phiếu vote đã đạt tỷ lệ chấp # Mục lục:
- [Tài liệu cho `account_state_db.go`](#tài-liệu-cho-account_state_dbgo)
- [Tài liệu cho `block.go`](#tài-liệu-cho-blockgo)
- [Tài liệu cho `block_header.go`](#tài-liệu-cho-block_headergo)
- [Tài liệu cho `confirmed_block_data.go`](#tài-liệu-cho-confirmed_block_datago)
- [Tài liệu cho `bls.go`](#tài-liệu-cho-blsgo)
- [Tài liệu cho `key_pair.go`](#tài-liệu-cho-key_pairgo)
- [Tài liệu cho `checkpoint.go`](#tài-liệu-cho-checkpointgo)
- [Tài liệu cho `execute_sc_grouper.go`](#tài-liệu-cho-execute_sc_groupergo)
- [Tài liệu cho `monitor.go`](#tài-liệu-cho-monitorgo)
- [Tài liệu cho `types.go`](#tài-liệu-cho-typesgo)
- [Tài liệu cho `helpers.go`](#tài-liệu-cho-helpersgo)
- [Tài liệu cho `extension.go`](#tài-liệu-cho-extensiongo)
- [Tài liệu cho `mvm_api.go`](#tài-liệu-cho-mvm_apigo)
- [Tài liệu cho `connection.go`](#tài-liệu-cho-connectiongo)
- [Tài liệu cho `connections_manager.go`](#tài-liệu-cho-connections_managergo)
- [Tài liệu cho `message.go`](#tài-liệu-cho-messagego)
- [Tài liệu cho `message_sender.go`](#tài-liệu-cho-message_sendergo)
- [Tài liệu cho `server.go`](#tài-liệu-cho-servergo)
- [Tài liệu cho `nodes_state.go`](#tài-liệu-cho-nodes_statego)
- [Tài liệu cho `pack.go`](#tài-liệu-cho-packgo)
- [Tài liệu cho `packs_from_leader.go`](#tài-liệu-cho-packs_from_leadergo)
- [Tài liệu cho `verify_pack_sign.go`](#tài-liệu-cho-verify_pack_signgo)
- [Tài liệu cho `pack_pool.go`](#tài-liệu-cho-pack_poolgo)
- [Tài liệu cho `receipt.go`](#tài-liệu-cho-receiptgo)
- [Tài liệu cho `receipts.go`](#tài-liệu-cho-receiptsgo)
- [Tài liệu cho `remote_storage_db.go`](#tài-liệu-cho-remote_storage_dbgo)
- [Tài liệu cho `event_log.go`](#tài-liệu-cho-event_loggo)
- [Tài liệu cho `event_logs.go`](#tài-liệu-cho-event_logsgo)
- [Tài liệu cho `execute_sc_result.go`](#tài-liệu-cho-execute_sc_resultgo)
- [Tài liệu cho `execute_sc_results.go`](#tài-liệu-cho-execute_sc_resultsgo)
- [Tài liệu cho `smart_contract_update_data.go`](#tài-liệu-cho-smart_contract_update_datago)
- [Tài liệu cho `smart_contract_update_datas.go`](#tài-liệu-cho-smart_contract_update_datasgo)
- [Tài liệu cho `touched_addresses_data.go`](#tài-liệu-cho-touched_addresses_datago)
- [Tài liệu cho `smart_contract_db.go`](#tài-liệu-cho-smart_contract_dbgo)
- [Tài liệu cho `stake_abi.go`](#tài-liệu-cho-stake_abigo)
- [Tài liệu cho `stake_getter.go`](#tài-liệu-cho-stake_gettergo)
- [Tài liệu cho `stake_info.go`](#tài-liệu-cho-stake_infogo)
- [Tài liệu cho `stake_smart_contract_db.go`](#tài-liệu-cho-stake_smart_contract_dbgo)
- [Tài liệu cho `account_state.go`](#tài-liệu-cho-account_statego)
- [Tài liệu cho `smart_contract_state.go`](#tài-liệu-cho-smart_contract_statego)
- [Tài liệu cho `update_state_fields.go`](#tài-liệu-cho-update_state_fieldsgo)
- [Tài liệu cho `stats.go`](#tài-liệu-cho-statsgo)
- [Tài liệu cho `badger_db.go`](#tài-liệu-cho-badger_dbgo)
- [Tài liệu cho `leveldb.go`](#tài-liệu-cho-leveldbgo)
- [Tài liệu cho `memorydb.go`](#tài-liệu-cho-memorydbgo)
- [Tài liệu cho `node_sync_data.go`](#tài-liệu-cho-node_sync_datago)
- [Tài liệu cho `call_data.go`](#tài-liệu-cho-call_datago)
- [Tài liệu cho `deploy_data.go`](#tài-liệu-cho-deploy_datago)
- [Tài liệu cho `from_node_transaction_result.go`](#tài-liệu-cho-from_node_transaction_resultgo)
- [Tài liệu cho `to_node_transaction_result.go`](#tài-liệu-cho-to_node_transaction_resultgo)
- [Tài liệu cho `open_state_channel_data.go`](#tài-liệu-cho-open_state_channel_datago)
- [Tài liệu cho `execute_sc_transactions.go`](#tài-liệu-cho-execute_sc_transactionsgo)
- [Tài liệu cho `transactions_from_leader.go`](#tài-liệu-cho-transactions_from_leadergo)
- [Tài liệu cho `verify_transaction_sign.go`](#tài-liệu-cho-verify_transaction_signgo)
- [Tài liệu cho `update_storage_host_data.go`](#tài-liệu-cho-update_storage_host_datago)
- [Tài liệu cho `transaction.go`](#tài-liệu-cho-transactiongo)
- [Tài liệu cho `transaction_grouper.go`](#tài-liệu-cho-transaction_groupergo)
- [Tài liệu cho `transaction_pool.go`](#tài-liệu-cho-transaction_poolgo)
- [Tài liệu cho `committer.go`](#tài-liệu-cho-committergo)
- [Tài liệu cho `hasher.go`](#tài-liệu-cho-hashergo)
- [Tài liệu cho `trie_reader.go`](#tài-liệu-cho-trie_readergo)
- [Tài liệu cho `trie.go`](#tài-liệu-cho-triego)
- [Tài liệu cho `block_vote.go`](#tài-liệu-cho-block_votego)
- [Tài liệu cho `vote_pool.go`](#tài-liệu-cho-vote_poolgo)

# Tài liệu cho [`account_state_db.go`](./account_state_db/account_state_db.go)

## Giới thiệu

File `account_state_db.go` định nghĩa một cấu trúc dữ liệu `AccountStateDB` để quản lý trạng thái tài khoản trong một blockchain. Nó sử dụng một cây Merkle Patricia Trie để lưu trữ trạng thái và cung cấp các phương thức để thao tác với trạng thái tài khoản.

## Cấu trúc `AccountStateDB`

### Thuộc tính

- `trie`: Một con trỏ đến `MerklePatriciaTrie`, được sử dụng để lưu trữ trạng thái tài khoản.
- `originRootHash`: Hash gốc ban đầu của trie.
- `db`: Một đối tượng `storage.Storage` để lưu trữ dữ liệu.
- `dirtyAccounts`: Một map lưu trữ các tài khoản đã bị thay đổi nhưng chưa được commit.
- `mu`: Một mutex để đảm bảo tính đồng bộ khi truy cập dữ liệu.

### Hàm khởi tạo

- `NewAccountStateDB(trie *p_trie.MerklePatriciaTrie, db storage.Storage) *AccountStateDB`: Tạo một đối tượng `AccountStateDB` mới với trie và cơ sở dữ liệu được cung cấp.

### Các phương thức

- `AccountState(address common.Address) (types.AccountState, error)`: Lấy trạng thái tài khoản cho một địa chỉ cụ thể.
- `SubPendingBalance(address common.Address, amount *big.Int) error`: Trừ một số tiền từ số dư đang chờ xử lý của tài khoản.
- `AddPendingBalance(address common.Address, amount *big.Int)`: Thêm một số tiền vào số dư đang chờ xử lý của tài khoản.
- `AddBalance(address common.Address, amount *big.Int)`: Thêm một số tiền vào số dư của tài khoản.
- `SubBalance(address common.Address, amount *big.Int) error`: Trừ một số tiền từ số dư của tài khoản.
- `SubTotalBalance(address common.Address, amount *big.Int) error`: Trừ một số tiền từ tổng số dư của tài khoản.
- `SetLastHash(address common.Address, hash common.Hash)`: Đặt hash cuối cùng cho tài khoản.
- `SetNewDeviceKey(address common.Address, newDeviceKey common.Hash)`: Đặt khóa thiết bị mới cho tài khoản.
- `SetState(as types.AccountState)`: Đặt trạng thái cho tài khoản.
- `SetCreatorPublicKey(address common.Address, creatorPublicKey p_common.PublicKey)`: Đặt khóa công khai của người tạo cho tài khoản.
- `SetCodeHash(address common.Address, codeHash common.Hash)`: Đặt hash mã cho tài khoản.
- `SetStorageRoot(address common.Address, storageRoot common.Hash)`: Đặt root lưu trữ cho tài khoản.
- `SetStorageAddress(address common.Address, storageAddress common.Address)`: Đặt địa chỉ lưu trữ cho tài khoản.
- `AddLogHash(address common.Address, logsHash common.Hash)`: Thêm hash log cho tài khoản.
- `Discard() (err error)`: Hủy bỏ các thay đổi chưa được commit.
- `Commit() (common.Hash, error)`: Commit các thay đổi vào trie và trả về hash gốc mới.
- `IntermediateRoot() (common.Hash, error)`: Cập nhật các tài khoản đã thay đổi vào trie và trả về hash gốc trung gian.
- `setDirtyAccountState(as types.AccountState)`: Đánh dấu trạng thái tài khoản là đã thay đổi.
- `getOrCreateAccountState(address common.Address) (types.AccountState, error)`: Lấy hoặc tạo trạng thái tài khoản cho một địa chỉ cụ thể.
- `Storage() storage.Storage`: Trả về đối tượng lưu trữ.
- `CopyFrom(as types.AccountStateDB) error`: Sao chép trạng thái từ một `AccountStateDB` khác.

## Kết luận

File `account_state_db.go` cung cấp một cách tiếp cận có cấu trúc để quản lý trạng thái tài khoản trong một blockchain. Nó sử dụng các kỹ thuật đồng bộ hóa để đảm bảo tính nhất quán của dữ liệu và cung cấp nhiều phương thức để thao tác với trạng thái tài khoản.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`block.go`](./block/block.go)

## Giới thiệu

File `block.go` định nghĩa cấu trúc `Block` và các phương thức liên quan để quản lý các block trong blockchain. Mỗi block bao gồm một tiêu đề, danh sách các giao dịch và kết quả thực thi smart contract.

## Cấu trúc `Block`

### Thuộc tính

- `header`: Thuộc tính kiểu `types.BlockHeader`, lưu trữ thông tin tiêu đề của block.
- `transactions`: Một slice chứa các giao dịch thuộc kiểu `types.Transaction`.
- `executeSCResults`: Một slice chứa kết quả thực thi smart contract thuộc kiểu `types.ExecuteSCResult`.

### Hàm khởi tạo

- `NewBlock(header types.BlockHeader, transactions []types.Transaction, executeSCResults []types.ExecuteSCResult) *Block`: Tạo một đối tượng `Block` mới với tiêu đề, danh sách giao dịch và kết quả thực thi smart contract được cung cấp.

### Các phương thức

- `Header() types.BlockHeader`: Trả về tiêu đề của block.
- `Transactions() []types.Transaction`: Trả về danh sách các giao dịch của block.
- `ExecuteSCResults() []types.ExecuteSCResult`: Trả về danh sách kết quả thực thi smart contract của block.
- `Proto() *pb.Block`: Chuyển đổi block thành đối tượng `pb.Block` để sử dụng với Protobuf.
- `FromProto(pbBlock *pb.Block)`: Khởi tạo block từ một đối tượng `pb.Block`.
- `Marshal() ([]byte, error)`: Chuyển đổi block thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(bData []byte) error`: Khởi tạo block từ một slice byte đã được mã hóa.

## Kết luận

File `block.go` cung cấp các phương thức cần thiết để tạo, quản lý và chuyển đổi các block trong blockchain. Nó sử dụng Protobuf để mã hóa và giải mã dữ liệu, giúp dễ dàng lưu trữ và truyền tải các block.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`block_header.go`](./block/block_header.go)

## Giới thiệu

File `block_header.go` định nghĩa cấu trúc `BlockHeader` và các phương thức liên quan để quản lý tiêu đề của các block trong blockchain. Tiêu đề block chứa thông tin quan trọng như hash của block cuối cùng, số block, root trạng thái tài khoản, root biên nhận, địa chỉ leader, thời gian và chữ ký tổng hợp.

## Cấu trúc `BlockHeader`

### Thuộc tính

- `lastBlockHash`: Hash của block cuối cùng thuộc kiểu `common.Hash`.
- `blockNumber`: Số thứ tự của block thuộc kiểu `uint64`.
- `accountStatesRoot`: Hash của trạng thái tài khoản thuộc kiểu `common.Hash`.
- `receiptRoot`: Hash của biên nhận thuộc kiểu `common.Hash`.
- `leaderAddress`: Địa chỉ của leader thuộc kiểu `common.Address`.
- `timeStamp`: Thời gian tạo block thuộc kiểu `uint64`.
- `aggregateSignature`: Chữ ký tổng hợp thuộc kiểu `[]byte`.

### Hàm khởi tạo

- `NewBlockHeader(lastBlockHash common.Hash, blockNumber uint64, accountStatesRoot common.Hash, receiptRoot common.Hash, leaderAddress common.Address, timeStamp uint64) *BlockHeader`: Tạo một đối tượng `BlockHeader` mới với các thông tin được cung cấp.

### Các phương thức

- `LastBlockHash() common.Hash`: Trả về hash của block cuối cùng.
- `BlockNumber() uint64`: Trả về số thứ tự của block.
- `AccountStatesRoot() common.Hash`: Trả về hash của trạng thái tài khoản.
- `ReceiptRoot() common.Hash`: Trả về hash của biên nhận.
- `LeaderAddress() common.Address`: Trả về địa chỉ của leader.
- `TimeStamp() uint64`: Trả về thời gian tạo block.
- `AggregateSignature() []byte`: Trả về chữ ký tổng hợp.
- `Marshal() ([]byte, error)`: Chuyển đổi `BlockHeader` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(bData []byte) error`: Khởi tạo `BlockHeader` từ một slice byte đã được mã hóa.
- `Hash() common.Hash`: Tính toán và trả về hash của `BlockHeader`.
- `Proto() *pb.BlockHeader`: Chuyển đổi `BlockHeader` thành đối tượng `pb.BlockHeader` để sử dụng với Protobuf.
- `FromProto(pbBlockHeader *pb.BlockHeader)`: Khởi tạo `BlockHeader` từ một đối tượng `pb.BlockHeader`.
- `String() string`: Trả về chuỗi mô tả của `BlockHeader`.

## Kết luận

File `block_header.go` cung cấp các phương thức cần thiết để tạo, quản lý và chuyển đổi tiêu đề của các block trong blockchain. Nó sử dụng Protobuf để mã hóa và giải mã dữ liệu, giúp dễ dàng lưu trữ và truyền tải các tiêu đề block.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`confirmed_block_data.go`](./block/confirmed_block_data.go)

## Giới thiệu

File `confirmed_block_data.go` định nghĩa cấu trúc `ConfirmedBlockData` và các phương thức liên quan để quản lý dữ liệu của các block đã được xác nhận trong blockchain. Nó bao gồm thông tin tiêu đề, biên nhận, root trạng thái nhánh và chữ ký của các validator.

## Cấu trúc `ConfirmedBlockData`

### Thuộc tính

- `header`: Thuộc tính kiểu `types.BlockHeader`, lưu trữ thông tin tiêu đề của block.
- `receipts`: Một slice chứa các biên nhận thuộc kiểu `types.Receipt`.
- `branchStateRoot`: Hash của trạng thái nhánh thuộc kiểu `e_common.Hash`.
- `validatorSigns`: Map lưu trữ chữ ký của các validator, với địa chỉ là key và chữ ký là value.

### Hàm khởi tạo

- `NewConfirmedBlockData(header types.BlockHeader, receipts []types.Receipt, branchStateRoot e_common.Hash, validatorSigns map[e_common.Address][]byte) *ConfirmedBlockData`: Tạo một đối tượng `ConfirmedBlockData` mới với tiêu đề, biên nhận, root trạng thái nhánh và chữ ký của các validator được cung cấp.

### Các phương thức

- `Header() types.BlockHeader`: Trả về tiêu đề của block.
- `Receipts() []types.Receipt`: Trả về danh sách biên nhận của block.
- `BranchStateRoot() e_common.Hash`: Trả về hash của trạng thái nhánh.
- `ValidatorSigns() map[e_common.Address][]byte`: Trả về map chữ ký của các validator.
- `SetHeader(header types.BlockHeader)`: Đặt tiêu đề cho block.
- `SetBranchStateRoot(rootHash e_common.Hash)`: Đặt root trạng thái nhánh.
- `Proto() *pb.ConfirmedBlockData`: Chuyển đổi `ConfirmedBlockData` thành đối tượng `pb.ConfirmedBlockData` để sử dụng với Protobuf.
- `FromProto(pbData *pb.ConfirmedBlockData)`: Khởi tạo `ConfirmedBlockData` từ một đối tượng `pb.ConfirmedBlockData`.
- `Marshal() ([]byte, error)`: Chuyển đổi `ConfirmedBlockData` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(cData []byte) error`: Khởi tạo `ConfirmedBlockData` từ một slice byte đã được mã hóa.
- `LoadConfirmedBlockDataFromFile(path string) (types.ConfirmedBlockData, error)`: Tải `ConfirmedBlockData` từ một file.
- `SaveConfirmedBlockDataToFile(cData types.ConfirmedBlockData, path string) error`: Lưu `ConfirmedBlockData` vào một file.

## Kết luận

File `confirmed_block_data.go` cung cấp các phương thức cần thiết để tạo, quản lý và chuyển đổi dữ liệu của các block đã được xác nhận trong blockchain. Nó sử dụng Protobuf để mã hóa và giải mã dữ liệu, giúp dễ dàng lưu trữ và truyền tải các block.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`bls.go`](./bls/bls.go)

## Giới thiệu

File `bls.go` cung cấp các hàm và cấu trúc để thực hiện các thao tác liên quan đến chữ ký BLS (Boneh-Lynn-Shacham) trong blockchain. Nó bao gồm các hàm để ký, xác minh chữ ký, tạo cặp khóa và xử lý chữ ký tổng hợp.

## Các hàm và cấu trúc

### Biến và kiểu dữ liệu

- `blstPublicKey`, `blstSignature`, `blstAggregateSignature`, `blstAggregatePublicKey`, `blstSecretKey`: Các kiểu dữ liệu đại diện cho khóa công khai, chữ ký, chữ ký tổng hợp, khóa công khai tổng hợp và khóa bí mật trong thư viện BLS.
- `dstMinPk`: Một slice byte được sử dụng làm domain separation tag cho chữ ký BLS.

### Hàm khởi tạo

- `Init()`: Khởi tạo thư viện BLS với số lượng luồng tối đa bằng số lượng CPU khả dụng.

### Các hàm

- `Sign(bPri cm.PrivateKey, bMessage []byte) cm.Sign`: Tạo chữ ký BLS từ khóa bí mật và thông điệp.
- `GetByteAddress(pubkey []byte) []byte`: Tính toán địa chỉ từ khóa công khai.
- `VerifySign(bPub cm.PublicKey, bSig cm.Sign, bMsg []byte) bool`: Xác minh chữ ký BLS với khóa công khai và thông điệp.
- `VerifyAggregateSign(bPubs [][]byte, bSig []byte, bMsgs [][]byte) bool`: Xác minh chữ ký tổng hợp BLS với danh sách khóa công khai và thông điệp.
- `GenerateKeyPairFromSecretKey(hexSecretKey string) (cm.PrivateKey, cm.PublicKey, common.Address)`: Tạo cặp khóa từ khóa bí mật dưới dạng chuỗi hex.
- `randBLSTSecretKey() *blstSecretKey`: Tạo ngẫu nhiên một khóa bí mật BLS.
- `GenerateKeyPair() *KeyPair`: Tạo ngẫu nhiên một cặp khóa BLS.
- `CreateAggregateSign(bSignatures [][]byte) []byte`: Tạo chữ ký tổng hợp từ danh sách chữ ký.

## Kết luận

File `bls.go` cung cấp các hàm cần thiết để thực hiện các thao tác liên quan đến chữ ký BLS trong blockchain. Nó sử dụng thư viện BLS để đảm bảo tính bảo mật và hiệu suất cao trong việc xử lý chữ ký.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`key_pair.go`](./bls/key_pair.go)

## Giới thiệu

File `key_pair.go` định nghĩa cấu trúc `KeyPair` và các phương thức liên quan để quản lý cặp khóa BLS (Boneh-Lynn-Shacham) trong blockchain. Cặp khóa bao gồm khóa công khai, khóa bí mật và địa chỉ tương ứng.

## Cấu trúc `KeyPair`

### Thuộc tính

- `publicKey`: Khóa công khai thuộc kiểu `cm.PublicKey`.
- `privateKey`: Khóa bí mật thuộc kiểu `cm.PrivateKey`.
- `address`: Địa chỉ thuộc kiểu `common.Address`.

### Hàm khởi tạo

- `NewKeyPair(privateKey []byte) *KeyPair`: Tạo một đối tượng `KeyPair` mới từ khóa bí mật được cung cấp. Khóa công khai và địa chỉ được tính toán từ khóa bí mật.

### Các phương thức

- `PrivateKey() cm.PrivateKey`: Trả về khóa bí mật của cặp khóa.
- `BytesPrivateKey() []byte`: Trả về khóa bí mật dưới dạng slice byte.
- `PublicKey() cm.PublicKey`: Trả về khóa công khai của cặp khóa.
- `BytesPublicKey() []byte`: Trả về khóa công khai dưới dạng slice byte.
- `Address() common.Address`: Trả về địa chỉ của cặp khóa.
- `String() string`: Trả về chuỗi mô tả của cặp khóa, bao gồm khóa bí mật, khóa công khai và địa chỉ dưới dạng chuỗi hex.

## Kết luận

File `key_pair.go` cung cấp các phương thức cần thiết để tạo, quản lý và truy xuất thông tin từ cặp khóa BLS trong blockchain. Nó sử dụng các thư viện mã hóa để đảm bảo tính bảo mật và chính xác của dữ liệu.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`checkpoint.go`](./checkpoint/checkpoint.go)

## Giới thiệu

File `checkpoint.go` định nghĩa cấu trúc `CheckPoint` và các phương thức liên quan để quản lý điểm kiểm tra (checkpoint) trong blockchain. Điểm kiểm tra bao gồm thông tin về block đầy đủ cuối cùng, lịch trình của leader hiện tại và lịch trình của leader tiếp theo.

## Cấu trúc `CheckPoint`

### Thuộc tính

- `lastFullBlock`: Thuộc tính kiểu `types.FullBlock`, lưu trữ thông tin về block đầy đủ cuối cùng.
- `thisLeaderSchedule`: Thuộc tính kiểu `types.LeaderSchedule`, lưu trữ lịch trình của leader hiện tại.
- `nextLeaderSchedule`: Thuộc tính kiểu `types.LeaderSchedule`, lưu trữ lịch trình của leader tiếp theo.

### Hàm khởi tạo

- `NewCheckPoint(lastFullBlock types.FullBlock, thisLeaderSchedule types.LeaderSchedule, nextLeaderSchedule types.LeaderSchedule) validator_types.Checkpoint`: Tạo một đối tượng `CheckPoint` mới với block đầy đủ cuối cùng, lịch trình của leader hiện tại và lịch trình của leader tiếp theo được cung cấp.

### Các phương thức

- `Proto() protoreflect.ProtoMessage`: Chuyển đổi `CheckPoint` thành đối tượng `pb.Checkpoint` để sử dụng với Protobuf.
- `Marshal() ([]byte, error)`: Chuyển đổi `CheckPoint` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(b []byte) error`: Khởi tạo `CheckPoint` từ một slice byte đã được mã hóa.
- `LastFullBlock() types.FullBlock`: Trả về block đầy đủ cuối cùng.
- `ThisLeaderSchedule() types.LeaderSchedule`: Trả về lịch trình của leader hiện tại.
- `NextLeaderSchedule() types.LeaderSchedule`: Trả về lịch trình của leader tiếp theo.
- `Save(savePath string) error`: Lưu `CheckPoint` vào một file tại đường dẫn được chỉ định.
- `Load(savePath string) error`: Tải `CheckPoint` từ một file tại đường dẫn được chỉ định.
- `AccountStatesManager(accountStatesDbPath string, dbType string) (types.AccountStatesManager, error)`: Tạo và trả về một `AccountStatesManager` từ đường dẫn cơ sở dữ liệu trạng thái tài khoản và loại cơ sở dữ liệu được chỉ định.

## Kết luận

File `checkpoint.go` cung cấp các phương thức cần thiết để tạo, quản lý và chuyển đổi điểm kiểm tra trong blockchain. Nó sử dụng Protobuf để mã hóa và giải mã dữ liệu, giúp dễ dàng lưu trữ và truyền tải các điểm kiểm tra.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`execute_sc_grouper.go`](./execute_sc_grouper/execute_sc_grouper.go)

## Giới thiệu

File `execute_sc_grouper.go` định nghĩa cấu trúc `ExecuteSmartContractsGrouper` và các phương thức liên quan để quản lý việc nhóm các giao dịch smart contract trong blockchain. Mục tiêu là tổ chức các giao dịch thành các nhóm dựa trên địa chỉ liên quan để thực thi hiệu quả hơn.

## Cấu trúc `ExecuteSmartContractsGrouper`

### Thuộc tính

- `groupCount`: Số lượng nhóm hiện tại thuộc kiểu `uint64`.
- `mapAddressGroup`: Map lưu trữ nhóm của từng địa chỉ, với địa chỉ là key và ID nhóm là value.
- `mapGroupExecuteTransactions`: Map lưu trữ các giao dịch của từng nhóm, với ID nhóm là key và danh sách giao dịch là value.

### Hàm khởi tạo

- `NewExecuteSmartContractsGrouper() *ExecuteSmartContractsGrouper`: Tạo một đối tượng `ExecuteSmartContractsGrouper` mới với các map trống và số lượng nhóm bằng 0.

### Các phương thức

- `AddTransactions(transactions []types.Transaction)`: Thêm các giao dịch vào các nhóm dựa trên địa chỉ liên quan.
- `GetGroupTransactions() map[uint64][]types.Transaction`: Trả về map các giao dịch của từng nhóm.
- `CountGroupWithTransactions() int`: Trả về số lượng nhóm có chứa giao dịch.
- `Clear()`: Xóa tất cả các nhóm và giao dịch, đặt lại số lượng nhóm về 0.
- `assignGroup(addresses []common.Address) uint64`: Gán các địa chỉ vào một nhóm và trả về ID nhóm.

## Kết luận

File `execute_sc_grouper.go` cung cấp các phương thức cần thiết để quản lý và nhóm các giao dịch smart contract trong blockchain. Việc nhóm các giao dịch giúp tối ưu hóa quá trình thực thi và quản lý các giao dịch liên quan đến cùng một địa chỉ.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`monitor.go`](./monitor_service/monitor.go)

## Giới thiệu

File `monitor.go` định nghĩa cấu trúc `MonitorService` và các phương thức liên quan để giám sát và thu thập thông tin hệ thống của một dịch vụ trong blockchain. Nó bao gồm việc thu thập thông tin như sử dụng bộ nhớ, sử dụng CPU, dung lượng đĩa, thời gian hoạt động của dịch vụ và kích thước log.

## Cấu trúc `SystemInfo`

### Thuộc tính

- `IP`: Địa chỉ IP của hệ thống (hiện không sử dụng biến này).
- `ServiceName`: Tên của dịch vụ.
- `ServiceUptime`: Thời gian hoạt động của dịch vụ.
- `MemoryUsed`: Phần trăm bộ nhớ đã sử dụng.
- `DiskUsed`: Phần trăm dung lượng đĩa đã sử dụng.
- `CPUUsed`: Phần trăm CPU đã sử dụng.
- `OutputLogSize`: Kích thước của file log đầu ra.
- `ErrorLogSize`: Kích thước của file log lỗi.
- `ErrorString`: Danh sách các lỗi xảy ra trong quá trình thu thập thông tin.

## Cấu trúc `MonitorService`

### Thuộc tính

- `messageSender`: Đối tượng gửi tin nhắn thuộc kiểu `t_network.MessageSender`.
- `monitorConn`: Kết nối đến máy chủ giám sát thuộc kiểu `t_network.Connection`.
- `serviceName`: Tên của dịch vụ cần giám sát.
- `delayTime`: Thời gian trễ giữa các lần thu thập thông tin.

### Hàm khởi tạo

- `NewMonitorService(messageSender t_network.MessageSender, monitorAddress string, dnsLink string, serviceName string, delayTime time.Duration) *MonitorService`: Tạo một đối tượng `MonitorService` mới với các thông tin được cung cấp.

### Các phương thức

- `Run()`: Chạy dịch vụ giám sát, thu thập thông tin hệ thống và gửi dữ liệu đến máy chủ giám sát theo chu kỳ thời gian đã định.

## Kết luận

File `monitor.go` cung cấp các phương thức cần thiết để giám sát và thu thập thông tin hệ thống của một dịch vụ trong blockchain. Nó sử dụng các script để thu thập thông tin và gửi dữ liệu đến máy chủ giám sát để phân tích và theo dõi.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`types.go`](./mvm/types.go)

## Giới thiệu

File `types.go` trong gói `mvm` định nghĩa các cấu trúc và phương thức liên quan để quản lý kết quả thực thi của Máy ảo Meta (MVM) trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý kết quả thực thi, bao gồm các thay đổi về số dư, mã, lưu trữ, và nhật ký sự kiện.

## Cấu trúc `MVMExecuteResult`

### Thuộc tính

- `MapAddBalance`: Map lưu trữ các thay đổi số dư được thêm, với khóa là địa chỉ và giá trị là số dư dưới dạng `[]byte`.
- `MapSubBalance`: Map lưu trữ các thay đổi số dư bị trừ, với khóa là địa chỉ và giá trị là số dư dưới dạng `[]byte`.
- `MapCodeChange`: Map lưu trữ các thay đổi mã, với khóa là địa chỉ và giá trị là mã dưới dạng `[]byte`.
- `MapCodeHash`: Map lưu trữ hash của mã, với khóa là địa chỉ và giá trị là hash dưới dạng `[]byte`.
- `MapStorageChange`: Map lưu trữ các thay đổi lưu trữ, với khóa là địa chỉ và giá trị là một map khác chứa khóa lưu trữ và giá trị dưới dạng `[]byte`.
- `JEventLogs`: Nhật ký sự kiện dưới dạng `LogsJson`.
- `Status`: Trạng thái của kết quả thực thi, thuộc kiểu `pb.RECEIPT_STATUS`.
- `Exception`: Ngoại lệ xảy ra trong quá trình thực thi, thuộc kiểu `pb.EXCEPTION`.
- `Exmsg`: Thông điệp ngoại lệ dưới dạng `string`.
- `Return`: Dữ liệu trả về từ quá trình thực thi dưới dạng `[]byte`.
- `GasUsed`: Lượng gas đã sử dụng trong quá trình thực thi, thuộc kiểu `uint64`.

### Các phương thức

- `String() string`: Trả về chuỗi biểu diễn của kết quả thực thi, bao gồm thông tin về lý do thoát, ngoại lệ, thông điệp ngoại lệ, đầu ra, gas đã sử dụng, và các thay đổi về số dư, mã, lưu trữ, và nhật ký sự kiện.
- `EventLogs(blockNumber uint64, transactionHash common.Hash) []types.EventLog`: Trả về danh sách các nhật ký sự kiện hoàn chỉnh dựa trên số block và hash giao dịch.

## Cấu trúc `LogsJson`

### Các phương thức

- `CompleteEventLogs(blockNumber uint64, transactionHash common.Hash) []types.EventLog`: Tạo và trả về danh sách các nhật ký sự kiện hoàn chỉnh từ `LogsJson`, bao gồm thông tin về số block, hash giao dịch, địa chỉ, dữ liệu, và các chủ đề.

## Kết luận

File `types.go` cung cấp các cấu trúc và phương thức cần thiết để quản lý kết quả thực thi của Máy ảo Meta trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý kết quả thực thi, giúp dễ dàng lưu trữ và truyền tải thông tin về các thay đổi trong hệ thống.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`helpers.go`](./mvm/helpers.go)

## Giới thiệu

File `helpers.go` trong gói `mvm` định nghĩa các hàm hỗ trợ để xử lý kết quả thực thi của Máy ảo Meta (MVM) trong blockchain. Mục tiêu là cung cấp các phương thức để trích xuất và chuyển đổi dữ liệu từ kết quả thực thi của MVM, bao gồm các thay đổi về số dư, mã, lưu trữ, và nhật ký sự kiện.

## Các hàm hỗ trợ

### `extractExecuteResult`

- **Mô tả**: Trích xuất kết quả thực thi từ cấu trúc `C.struct_ExecuteResult` và chuyển đổi thành `MVMExecuteResult`.
- **Tham số**: `cExecuteResult *C.struct_ExecuteResult` - Kết quả thực thi từ MVM.
- **Trả về**: `*MVMExecuteResult` - Kết quả thực thi đã được chuyển đổi.

### `extractAddBalance`

- **Mô tả**: Trích xuất các thay đổi số dư được thêm từ kết quả thực thi.
- **Tham số**: `cExecuteResult *C.struct_ExecuteResult` - Kết quả thực thi từ MVM.
- **Trả về**: `map[string][]byte` - Map lưu trữ các thay đổi số dư được thêm.

### `extractSubBalance`

- **Mô tả**: Trích xuất các thay đổi số dư bị trừ từ kết quả thực thi.
- **Tham số**: `cExecuteResult *C.struct_ExecuteResult` - Kết quả thực thi từ MVM.
- **Trả về**: `map[string][]byte` - Map lưu trữ các thay đổi số dư bị trừ.

### `extractCodeChange`

- **Mô tả**: Trích xuất các thay đổi mã và hash mã từ kết quả thực thi.
- **Tham số**: `cExecuteResult *C.struct_ExecuteResult` - Kết quả thực thi từ MVM.
- **Trả về**: 
  - `map[string][]byte` - Map lưu trữ các thay đổi mã.
  - `map[string][]byte` - Map lưu trữ hash của mã.

### `extractStorageChange`

- **Mô tả**: Trích xuất các thay đổi lưu trữ từ kết quả thực thi.
- **Tham số**: `cExecuteResult *C.struct_ExecuteResult` - Kết quả thực thi từ MVM.
- **Trả về**: `map[string]map[string][]byte` - Map lưu trữ các thay đổi lưu trữ.

### `extractEventLogs`

- **Mô tả**: Trích xuất các nhật ký sự kiện từ kết quả thực thi.
- **Tham số**: `cExecuteResult *C.struct_ExecuteResult` - Kết quả thực thi từ MVM.
- **Trả về**: `LogsJson` - Nhật ký sự kiện dưới dạng JSON.

## Kết luận

File `helpers.go` cung cấp các hàm hỗ trợ cần thiết để trích xuất và chuyển đổi dữ liệu từ kết quả thực thi của Máy ảo Meta (MVM) trong blockchain. Nó hỗ trợ việc xử lý các thay đổi về số dư, mã, lưu trữ, và nhật ký sự kiện, giúp dễ dàng quản lý và xử lý dữ liệu trong hệ thống.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`extension.go`](./mvm/extension.go)

## Giới thiệu

File `extension.go` định nghĩa các hàm mở rộng cho Máy ảo Meta (MVM) trong blockchain. Mục tiêu là cung cấp các hàm để thực hiện các tác vụ như gọi API, trích xuất trường từ JSON, và xác thực chữ ký BLS.

## Các hàm mở rộng

### ExtensionCallGetApi

- **Mô tả**: Gọi một API GET và trả về dữ liệu phản hồi.
- **Tham số**:
  - `bytes *C.uchar`: Dữ liệu đầu vào dưới dạng con trỏ đến mảng byte.
  - `size C.int`: Kích thước của dữ liệu đầu vào.
- **Trả về**:
  - `data_p *C.uchar`: Con trỏ đến dữ liệu phản hồi đã mã hóa.
  - `data_size C.int`: Kích thước của dữ liệu phản hồi.

### ExtensionExtractJsonField

- **Mô tả**: Trích xuất một trường từ chuỗi JSON.
- **Tham số**:
  - `bytes *C.uchar`: Dữ liệu đầu vào dưới dạng con trỏ đến mảng byte.
  - `size C.int`: Kích thước của dữ liệu đầu vào.
- **Trả về**:
  - `data_p *C.uchar`: Con trỏ đến dữ liệu trường đã mã hóa.
  - `data_size C.int`: Kích thước của dữ liệu trường.

### ExtensionBlst

- **Mô tả**: Xác thực chữ ký BLS hoặc chữ ký tổng hợp BLS.
- **Tham số**:
  - `bytes *C.uchar`: Dữ liệu đầu vào dưới dạng con trỏ đến mảng byte.
  - `size C.int`: Kích thước của dữ liệu đầu vào.
- **Trả về**:
  - `data_p *C.uchar`: Con trỏ đến dữ liệu kết quả đã mã hóa.
  - `data_size C.int`: Kích thước của dữ liệu kết quả.

### WrapExtensionBlst

- **Mô tả**: Gói dữ liệu và gọi hàm `ExtensionBlst`.
- **Tham số**:
  - `data []byte`: Dữ liệu đầu vào dưới dạng mảng byte.
- **Trả về**: Mảng byte chứa dữ liệu kết quả.

## Kết luận

File `extension.go` cung cấp các hàm mở rộng cần thiết cho Máy ảo Meta (MVM) để thực hiện các tác vụ như gọi API, trích xuất dữ liệu từ JSON, và xác thực chữ ký BLS. Các hàm này giúp mở rộng khả năng của MVM trong việc xử lý các tác vụ phức tạp trong blockchain.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`mvm_api.go`](./mvm/mvm_api.go)

## Giới thiệu

File `mvm_api.go` định nghĩa cấu trúc `MVMApi` và các phương thức liên quan để quản lý và tương tác với Máy ảo Meta (MVM) trong blockchain. Mục tiêu là cung cấp các phương thức để khởi tạo, quản lý trạng thái tài khoản và smart contract, cũng như thực hiện các cuộc gọi đến MVM.

## Cấu trúc `MVMApi`

### Thuộc tính

- `smartContractDb`: Cơ sở dữ liệu của smart contract, thuộc kiểu `SmartContractDB`.
- `accountStateDb`: Cơ sở dữ liệu trạng thái tài khoản, thuộc kiểu `AccountStateDB`.
- `currentRelatedAddresses`: Map lưu trữ các địa chỉ liên quan hiện tại, với khóa là địa chỉ và giá trị là struct rỗng.

### Hàm khởi tạo

- `InitMVMApi(smartContractDb SmartContractDB, accountStateDb AccountStateDB)`: Khởi tạo một đối tượng `MVMApi` mới với cơ sở dữ liệu smart contract và trạng thái tài khoản được cung cấp.

### Các phương thức

- `MVMApiInstance() *MVMApi`: Trả về instance hiện tại của `MVMApi`.
- `Clear()`: Xóa instance hiện tại của `MVMApi`.
- `SetSmartContractDb(smartContractDb SmartContractDB)`: Thiết lập cơ sở dữ liệu smart contract.
- `SmartContractDatas() SmartContractDB`: Trả về cơ sở dữ liệu smart contract.
- `SetAccountStateDb(accountStateDb AccountStateDB)`: Thiết lập cơ sở dữ liệu trạng thái tài khoản.
- `AccountStateDb() AccountStateDB`: Trả về cơ sở dữ liệu trạng thái tài khoản.
- `SetRelatedAddresses(addresses []common.Address)`: Thiết lập các địa chỉ liên quan hiện tại.
- `InRelatedAddress(address common.Address) bool`: Kiểm tra xem địa chỉ có nằm trong danh sách địa chỉ liên quan hay không.
- `Call(...)`: Thực hiện cuộc gọi đến MVM với dữ liệu giao dịch và ngữ cảnh block.

### Các hàm hỗ trợ

- `ClearProcessingPointers()`: Giải phóng bộ nhớ cho các con trỏ đang xử lý.
- `TestMemLeak()`: Kiểm tra rò rỉ bộ nhớ trong quá trình thực thi.
- `TestMemLeakGs(addresses []common.Address)`: Kiểm tra rò rỉ bộ nhớ với danh sách địa chỉ.
- `GetStorageValue(address *C.uchar, key *C.uchar) (value *C.uchar)`: Lấy giá trị lưu trữ từ cơ sở dữ liệu smart contract.

## Kết luận

File `mvm_api.go` cung cấp các phương thức và hàm hỗ trợ cần thiết để quản lý và tương tác với Máy ảo Meta (MVM) trong blockchain. Nó hỗ trợ việc khởi tạo, quản lý trạng thái tài khoản và smart contract, cũng như thực hiện các cuộc gọi đến MVM, giúp dễ dàng quản lý và xử lý dữ liệu trong hệ thống.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`connection.go`](./network/connection.go)

## Giới thiệu

File `connection.go` định nghĩa cấu trúc `Connection` và các phương thức liên quan để quản lý kết nối mạng trong blockchain. Nó bao gồm các phương thức để tạo, quản lý, gửi và nhận tin nhắn qua kết nối TCP.

## Cấu trúc `Connection`

### Thuộc tính

- `mu`: Mutex để đồng bộ hóa truy cập vào kết nối.
- `address`: Địa chỉ của kết nối thuộc kiểu `common.Address`.
- `cType`: Loại kết nối.
- `requestChan`: Kênh để gửi yêu cầu thuộc kiểu `network.Request`.
- `errorChan`: Kênh để gửi lỗi.
- `tcpConn`: Kết nối TCP thuộc kiểu `net.Conn`.
- `connect`: Trạng thái kết nối (đã kết nối hay chưa).
- `dnsLink`: Liên kết DNS của kết nối.
- `realConnAddr`: Địa chỉ thực của kết nối.

### Hàm khởi tạo

- `ConnectionFromTcpConnection(tcpConn net.Conn, dnsLink string) (network.Connection, error)`: Tạo một đối tượng `Connection` từ một kết nối TCP.
- `NewConnection(address common.Address, cType string, dnsLink string) network.Connection`: Tạo một đối tượng `Connection` mới với địa chỉ, loại và liên kết DNS được cung cấp.

### Các phương thức

- `Address() common.Address`: Trả về địa chỉ của kết nối.
- `ConnectionAddress() (string, error)`: Trả về địa chỉ thực của kết nối.
- `RequestChan() (chan network.Request, chan error)`: Trả về kênh yêu cầu và kênh lỗi.
- `Type() string`: Trả về loại kết nối.
- `String() string`: Trả về chuỗi mô tả của kết nối.
- `Init(address common.Address, cType string)`: Khởi tạo kết nối với địa chỉ và loại được cung cấp.
- `SendMessage(message network.Message) error`: Gửi tin nhắn qua kết nối.
- `Connect() (err error)`: Kết nối đến địa chỉ thực.
- `Disconnect() error`: Ngắt kết nối.
- `IsConnect() bool`: Kiểm tra trạng thái kết nối.
- `ReadRequest()`: Đọc yêu cầu từ kết nối.
- `Clone() network.Connection`: Tạo một bản sao của kết nối.
- `RemoteAddr() string`: Trả về địa chỉ từ xa của kết nối.

## Kết luận

File `connection.go` cung cấp các phương thức cần thiết để quản lý kết nối mạng trong blockchain. Nó hỗ trợ việc gửi và nhận tin nhắn qua kết nối TCP, đồng thời cung cấp các công cụ để quản lý và theo dõi trạng thái kết nối.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`connections_manager.go`](./network/connections_manager.go)

## Giới thiệu

File `connections_manager.go` định nghĩa cấu trúc `ConnectionsManager` và các phương thức liên quan để quản lý các kết nối mạng trong blockchain. Nó bao gồm các phương thức để thêm, xóa và truy xuất kết nối theo loại và địa chỉ.

## Cấu trúc `ConnectionsManager`

### Thuộc tính

- `mu`: Mutex để đồng bộ hóa truy cập vào các kết nối.
- `parentConnection`: Kết nối cha thuộc kiểu `network.Connection`.
- `typeToMapAddressConnections`: Mảng các map lưu trữ kết nối theo loại và địa chỉ.

### Hàm khởi tạo

- `NewConnectionsManager() network.ConnectionsManager`: Tạo một đối tượng `ConnectionsManager` mới.

### Các phương thức

- `ConnectionsByType(cType int) map[common.Address]network.Connection`: Trả về các kết nối theo loại.
- `ConnectionByTypeAndAddress(cType int, address common.Address) network.Connection`: Trả về kết nối theo loại và địa chỉ.
- `ConnectionsByTypeAndAddresses(cType int, addresses []common.Address) map[common.Address]network.Connection`: Trả về các kết nối theo loại và danh sách địa chỉ.
- `FilterAddressAvailable(cType int, addresses map[common.Address]*uint256.Int) map[common.Address]*uint256.Int`: Lọc và trả về các địa chỉ có kết nối khả dụng.
- `ParentConnection() network.Connection`: Trả về kết nối cha.
- `Stats() *pb.NetworkStats`: Trả về thống kê mạng.
- `AddParentConnection(conn network.Connection)`: Thêm kết nối cha.
- `RemoveConnection(conn network.Connection)`: Xóa kết nối.
- `AddConnection(conn network.Connection, replace bool, connectionType string)`: Thêm kết nối mới hoặc thay thế kết nối hiện tại.
- `MapAddressConnectionToInterface(data map[common.Address]network.Connection) map[common.Address]interface{}`: Chuyển đổi map kết nối thành map giao diện.

## Kết luận

File `connections_manager.go` cung cấp các phương thức cần thiết để quản lý các kết nối mạng trong blockchain. Nó hỗ trợ việc thêm, xóa và truy xuất kết nối theo loại và địa chỉ, đồng thời cung cấp các công cụ để quản lý và theo dõi trạng thái kết nối.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`message.go`](./network/message.go)

## Giới thiệu

File `message.go` định nghĩa cấu trúc `Message` và các phương thức liên quan để quản lý tin nhắn mạng trong blockchain. Tin nhắn bao gồm thông tin tiêu đề và nội dung.

## Cấu trúc `Message`

### Thuộc tính

- `proto`: Tin nhắn thuộc kiểu `pb.Message`.

### Hàm khởi tạo

- `NewMessage(pbMessage *pb.Message) network.Message`: Tạo một đối tượng `Message` mới từ một tin nhắn Protobuf.

### Các phương thức

- `Marshal() ([]byte, error)`: Chuyển đổi tin nhắn thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(protoStruct protoreflect.ProtoMessage) error`: Khởi tạo tin nhắn từ một slice byte đã được mã hóa.
- `String() string`: Trả về chuỗi mô tả của tin nhắn.
- `Command() string`: Trả về lệnh của tin nhắn.
- `Body() []byte`: Trả về nội dung của tin nhắn.
- `Pubkey() cm.PublicKey`: Trả về khóa công khai của tin nhắn.
- `Sign() cm.Sign`: Trả về chữ ký của tin nhắn.
- `ToAddress() common.Address`: Trả về địa chỉ đích của tin nhắn.
- `ID() string`: Trả về ID của tin nhắn.

## Kết luận

File `message.go` cung cấp các phương thức cần thiết để quản lý tin nhắn mạng trong blockchain. Nó hỗ trợ việc mã hóa và giải mã tin nhắn, đồng thời cung cấp các công cụ để truy xuất thông tin từ tin nhắn.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`message_sender.go`](./network/message_sender.go)

## Giới thiệu

File `message_sender.go` định nghĩa cấu trúc `MessageSender` và các phương thức liên quan để gửi tin nhắn mạng trong blockchain. Nó bao gồm các phương thức để gửi tin nhắn đơn lẻ hoặc phát tin nhắn đến nhiều kết nối.

## Cấu trúc `MessageSender`

### Thuộc tính

- `version`: Phiên bản của tin nhắn.

### Hàm khởi tạo

- `NewMessageSender(version string) network.MessageSender`: Tạo một đối tượng `MessageSender` mới với phiên bản được cung cấp.

### Các phương thức

- `SendMessage(connection network.Connection, command string, pbMessage protoreflect.ProtoMessage) error`: Gửi tin nhắn Protobuf qua kết nối.
- `SendBytes(connection network.Connection, command string, b []byte) error`: Gửi tin nhắn dưới dạng byte qua kết nối.
- `BroadcastMessage(mapAddressConnections map[common.Address]network.Connection, command string, marshaler network.Marshaler) error`: Phát tin nhắn đến nhiều kết nối.

### Các hàm hỗ trợ

- `getHeaderForCommand(command string, toAddress common.Address, version string) *pb.Header`: Tạo tiêu đề cho tin nhắn.
- `generateMessage(toAddress common.Address, command string, body []byte, version string) network.Message`: Tạo tin nhắn từ địa chỉ đích, lệnh, nội dung và phiên bản.
- `SendMessage(connection network.Connection, command string, pbMessage proto.Message, version string) (err error)`: Gửi tin nhắn Protobuf qua kết nối.
- `SendBytes(connection network.Connection, command string, bytes []byte, version string) error`: Gửi tin nhắn dưới dạng byte qua kết nối.

## Kết luận

File `message_sender.go` cung cấp các phương thức cần thiết để gửi tin nhắn mạng trong blockchain. Nó hỗ trợ việc gửi tin nhắn đơn lẻ hoặc phát tin nhắn đến nhiều kết nối, đồng thời cung cấp các công cụ để quản lý và theo dõi quá trình gửi tin nhắn.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`server.go`](./network/server.go)

## Giới thiệu

File `server.go` định nghĩa cấu trúc `SocketServer` và các phương thức liên quan để quản lý máy chủ socket trong blockchain. Nó bao gồm các phương thức để lắng nghe kết nối, xử lý kết nối, và quản lý các sự kiện kết nối và ngắt kết nối.

## Cấu trúc `SocketServer`

### Thuộc tính

- `connectionsManager`: Quản lý các kết nối thuộc kiểu `network.ConnectionsManager`.
- `listener`: Đối tượng lắng nghe kết nối thuộc kiểu `net.Listener`.
- `handler`: Bộ xử lý yêu cầu thuộc kiểu `network.Handler`.
- `nodeType`: Loại node.
- `version`: Phiên bản của máy chủ.
- `dnsLink`: Liên kết DNS của máy chủ.
- `keyPair`: Cặp khóa BLS thuộc kiểu `*bls.KeyPair`.
- `ctx`: Ngữ cảnh để quản lý vòng đời của máy chủ thuộc kiểu `context.Context`.
- `cancelFunc`: Hàm hủy ngữ cảnh.
- `onConnectedCallBack`: Danh sách các hàm callback khi kết nối thành công.
- `onDisconnectedCallBack`: Danh sách các hàm callback khi ngắt kết nối.

### Hàm khởi tạo

- `NewSocketServer(keyPair *bls.KeyPair, connectionsManager network.ConnectionsManager, handler network.Handler, nodeType string, version string, dnsLink string) network.SocketServer`: Tạo một đối tượng `SocketServer` mới với các thông tin được cung cấp.

### Các phương thức

- `SetContext(ctx context.Context, cancelFunc context.CancelFunc)`: Đặt ngữ cảnh và hàm hủy cho máy chủ.
- `AddOnConnectedCallBack(callBack func(network.Connection))`: Thêm hàm callback khi kết nối thành công.
- `AddOnDisconnectedCallBack(callBack func(network.Connection))`: Thêm hàm callback khi ngắt kết nối.
- `Listen(listenAddress string) error`: Lắng nghe kết nối tại địa chỉ được cung cấp.
- `Stop()`: Dừng máy chủ.
- `OnConnect(conn network.Connection)`: Xử lý sự kiện khi kết nối thành công.
- `OnDisconnect(conn network.Connection)`: Xử lý sự kiện khi ngắt kết nối.
- `HandleConnection(conn network.Connection) error`: Xử lý kết nối và đọc yêu cầu từ kết nối.
- `SetKeyPair(newKeyPair *bls.KeyPair)`: Đặt cặp khóa mới cho máy chủ.
- `StopAndRetryConnectToParent(conn network.Connection)`: Dừng máy chủ và thử kết nối lại với kết nối cha.
- `RetryConnectToParent(conn network.Connection)`: Thử kết nối lại với kết nối cha.

## Kết luận

File `server.go` cung cấp các phương thức cần thiết để quản lý máy chủ socket trong blockchain. Nó hỗ trợ việc lắng nghe và xử lý kết nối, đồng thời cung cấp các công cụ để quản lý và theo dõi trạng thái kết nối.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`nodes_state.go`](./nodes_state/nodes_state.go)

## Giới thiệu

File `nodes_state.go` định nghĩa cấu trúc `NodesState` và các phương thức liên quan để quản lý trạng thái của các node con trong blockchain. Nó bao gồm các phương thức để lấy trạng thái của node, gửi yêu cầu trạng thái, và quản lý các kết nối đến các node con.

## Cấu trúc `NodesState`

### Thuộc tính

- `prefix`: Tiền tố để xác định node.
- `socketServer`: Máy chủ socket thuộc kiểu `network.SocketServer`.
- `messageSender`: Đối tượng gửi tin nhắn thuộc kiểu `network.MessageSender`.
- `connectionsManager`: Quản lý các kết nối thuộc kiểu `network.ConnectionsManager`.
- `childNodes`: Danh sách địa chỉ của các node con.
- `childNodeStateRoots`: Mảng chứa hash trạng thái của các node con.
- `receivedChan`: Kênh để nhận thông báo khi nhận đủ trạng thái từ các node.
- `receivedNodesState`: Số lượng trạng thái node đã nhận.
- `getStateConnections`: Danh sách các kết nối để lấy trạng thái node.
- `currentSession`: ID session hiện tại.

### Hàm khởi tạo

- `NewNodesState(childNodes []common.Address, messageSender network.MessageSender, connectionsManager network.ConnectionsManager) *NodesState`: Tạo một đối tượng `NodesState` mới với danh sách node con, đối tượng gửi tin nhắn và quản lý kết nối được cung cấp.

### Các phương thức

- `SetSocketServer(s network.SocketServer)`: Đặt máy chủ socket cho `NodesState`.
- `GetStateRoot() (common.Hash, error)`: Lấy hash trạng thái của toàn bộ node.
- `GetChildNode(i int) common.Address`: Trả về địa chỉ của node con tại chỉ số `i`.
- `GetChildNodeIdx(nodeAddress common.Address) int`: Trả về chỉ số của node con với địa chỉ được cung cấp.
- `GetChildNodeStateRoot(address common.Address) common.Hash`: Trả về hash trạng thái của node con với địa chỉ được cung cấp.
- `SetChildNode(i int, childNode common.Address)`: Đặt địa chỉ cho node con tại chỉ số `i`.
- `SendCancelPendingStates()`: Gửi yêu cầu hủy trạng thái đang chờ xử lý đến tất cả các node con.
- `SendGetAccountState(address common.Address, id string) error`: Gửi yêu cầu lấy trạng thái tài khoản đến node con tương ứng với địa chỉ được cung cấp.
- `SendGetNodeSyncData(latestCheckPointBlockNumber uint64, validatorAddress common.Address)`: Gửi yêu cầu đồng bộ dữ liệu node đến tất cả các node con.

## Kết luận

File `nodes_state.go` cung cấp các phương thức cần thiết để quản lý trạng thái của các node con trong blockchain. Nó hỗ trợ việc gửi và nhận yêu cầu trạng thái, đồng thời cung cấp các công cụ để quản lý và theo dõi trạng thái của các node con.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`pack.go`](./pack/pack.go)

## Giới thiệu

File `pack.go` định nghĩa cấu trúc `Pack` và các phương thức liên quan để quản lý các gói giao dịch trong blockchain. Nó bao gồm các phương thức để tạo, mã hóa, giải mã, và xác thực chữ ký của các gói giao dịch.

## Cấu trúc `Pack`

### Thuộc tính

- `id`: ID của gói giao dịch, được tạo ngẫu nhiên bằng UUID.
- `transactions`: Danh sách các giao dịch thuộc kiểu `types.Transaction`.
- `aggSign`: Chữ ký tổng hợp của gói giao dịch.
- `timeStamp`: Dấu thời gian của gói giao dịch.

### Hàm khởi tạo

- `NewPack(transactions []types.Transaction, aggSign []byte, timeStamp uint64) types.Pack`: Tạo một đối tượng `Pack` mới với danh sách giao dịch, chữ ký tổng hợp và dấu thời gian được cung cấp.

### Các phương thức

- `NewVerifyPackSignRequest() types.VerifyPackSignRequest`: Tạo một yêu cầu xác thực chữ ký cho gói giao dịch.
- `Unmarshal(b []byte) error`: Giải mã gói giao dịch từ một slice byte.
- `Marshal() ([]byte, error)`: Mã hóa gói giao dịch thành một slice byte.
- `Proto() *pb.Pack`: Chuyển đổi gói giao dịch thành đối tượng Protobuf.
- `FromProto(pbMessage *pb.Pack)`: Khởi tạo gói giao dịch từ một đối tượng Protobuf.
- `Transactions() []types.Transaction`: Trả về danh sách giao dịch của gói.
- `Timestamp() uint64`: Trả về dấu thời gian của gói.
- `Id() string`: Trả về ID của gói.
- `AggregateSign() []byte`: Trả về chữ ký tổng hợp của gói.
- `ValidSign() bool`: Kiểm tra tính hợp lệ của chữ ký tổng hợp.

### Các hàm hỗ trợ

- `PacksToProto(packs []types.Pack) []*pb.Pack`: Chuyển đổi danh sách gói giao dịch thành danh sách đối tượng Protobuf.
- `PackFromProto(pbPack *pb.Pack) types.Pack`: Khởi tạo gói giao dịch từ một đối tượng Protobuf.
- `PacksFromProto(pbPacks []*pb.Pack) []types.Pack`: Chuyển đổi danh sách đối tượng Protobuf thành danh sách gói giao dịch.
- `MarshalPacks(packs []types.Pack) ([]byte, error)`: Mã hóa danh sách gói giao dịch thành một slice byte.
- `UnmarshalTransactions(b []byte) ([]types.Pack, error)`: Giải mã danh sách gói giao dịch từ một slice byte.

## Kết luận

File `pack.go` cung cấp các phương thức cần thiết để quản lý các gói giao dịch trong blockchain. Nó hỗ trợ việc mã hóa, giải mã, và xác thực chữ ký của các gói giao dịch, đồng thời cung cấp các công cụ để chuyển đổi giữa các định dạng dữ liệu khác nhau.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`packs_from_leader.go`](./pack/packs_from_leader.go)

## Giới thiệu

File `packs_from_leader.go` định nghĩa cấu trúc `PacksFromLeader` và các phương thức liên quan để quản lý các gói giao dịch từ leader trong blockchain. Nó bao gồm các phương thức để tạo, mã hóa, giải mã, và xác thực chữ ký của các gói giao dịch.

## Cấu trúc `PacksFromLeader`

### Thuộc tính

- `packs`: Danh sách các gói giao dịch thuộc kiểu `types.Pack`.
- `blockNumber`: Số block của gói giao dịch.
- `timeStamp`: Dấu thời gian của gói giao dịch.

### Hàm khởi tạo

- `NewPacksFromLeader(packs []types.Pack, blockNumber uint64, timeStamp uint64) *PacksFromLeader`: Tạo một đối tượng `PacksFromLeader` mới với danh sách gói giao dịch, số block và dấu thời gian được cung cấp.

### Các phương thức

- `Packs() []types.Pack`: Trả về danh sách các gói giao dịch.
- `BlockNumber() uint64`: Trả về số block của gói giao dịch.
- `TimeStamp() uint64`: Trả về dấu thời gian của gói giao dịch.
- `Marshal() ([]byte, error)`: Mã hóa đối tượng `PacksFromLeader` thành một slice byte.
- `Unmarshal(b []byte) error`: Giải mã đối tượng `PacksFromLeader` từ một slice byte.
- `IsValidSign() bool`: Kiểm tra tính hợp lệ của chữ ký trong tất cả các gói giao dịch.
- `Transactions() []types.Transaction`: Trả về danh sách các giao dịch từ tất cả các gói.
- `Proto() *pb.PacksFromLeader`: Chuyển đổi đối tượng `PacksFromLeader` thành đối tượng Protobuf.
- `FromProto(pbData *pb.PacksFromLeader)`: Khởi tạo đối tượng `PacksFromLeader` từ một đối tượng Protobuf.

## Kết luận

File `packs_from_leader.go` cung cấp các phương thức cần thiết để quản lý các gói giao dịch từ leader trong blockchain. Nó hỗ trợ việc mã hóa, giải mã, và xác thực chữ ký của các gói giao dịch, đồng thời cung cấp các công cụ để chuyển đổi giữa các định dạng dữ liệu khác nhau.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`verify_pack_sign.go`](./pack/verify_pack_sign.go)

## Giới thiệu

File `verify_pack_sign.go` định nghĩa các cấu trúc `VerifyPackSignRequest` và `VerifyPackSignResult` cùng với các phương thức liên quan để quản lý việc xác thực chữ ký của các gói giao dịch trong blockchain. Nó bao gồm các phương thức để tạo, mã hóa, giải mã, và xác thực chữ ký của các yêu cầu và kết quả xác thực.

## Cấu trúc `VerifyPackSignRequest`

### Thuộc tính

- `packId`: ID của gói giao dịch.
- `publicKeys`: Danh sách các khóa công khai liên quan đến các giao dịch.
- `hashes`: Danh sách các hash của giao dịch.
- `aggregateSign`: Chữ ký tổng hợp của gói giao dịch.

### Hàm khởi tạo

- `NewVerifyPackSignRequest(packId string, publicKeys [][]byte, hashes [][]byte, aggregateSign []byte) types.VerifyPackSignRequest`: Tạo một đối tượng `VerifyPackSignRequest` mới với ID gói, danh sách khóa công khai, danh sách hash và chữ ký tổng hợp được cung cấp.

### Các phương thức

- `Unmarshal(b []byte) error`: Giải mã yêu cầu từ một slice byte.
- `Marshal() ([]byte, error)`: Mã hóa yêu cầu thành một slice byte.
- `Id() string`: Trả về ID của gói giao dịch.
- `PublicKeys() [][]byte`: Trả về danh sách khóa công khai.
- `Hashes() [][]byte`: Trả về danh sách hash của giao dịch.
- `AggregateSign() []byte`: Trả về chữ ký tổng hợp.
- `Valid() bool`: Kiểm tra tính hợp lệ của chữ ký tổng hợp.
- `Proto() *pb.VerifyPackSignRequest`: Chuyển đổi yêu cầu thành đối tượng Protobuf.

## Cấu trúc `VerifyPackSignResult`

### Thuộc tính

- `packId`: ID của gói giao dịch.
- `valid`: Trạng thái hợp lệ của chữ ký.

### Hàm khởi tạo

- `NewVerifyPackSignResult(packId string, valid bool) types.VerifyPackSignResult`: Tạo một đối tượng `VerifyPackSignResult` mới với ID gói và trạng thái hợp lệ được cung cấp.

### Các phương thức

- `Unmarshal(b []byte) error`: Giải mã kết quả từ một slice byte.
- `Marshal() ([]byte, error)`: Mã hóa kết quả thành một slice byte.
- `PackId() string`: Trả về ID của gói giao dịch.
- `Valid() bool`: Trả về trạng thái hợp lệ của chữ ký.

## Kết luận

File `verify_pack_sign.go` cung cấp các phương thức cần thiết để quản lý việc xác thực chữ ký của các gói giao dịch trong blockchain. Nó hỗ trợ việc mã hóa, giải mã, và xác thực chữ ký của các yêu cầu và kết quả xác thực, đồng thời cung cấp các công cụ để chuyển đổi giữa các định dạng dữ liệu khác nhau.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`pack_pool.go`](./pack_pool/pack_pool.go)

## Giới thiệu

File `pack_pool.go` định nghĩa cấu trúc `PackPool` và các phương thức liên quan để quản lý một tập hợp các gói giao dịch (`Pack`) trong blockchain. Nó bao gồm các phương thức để thêm gói giao dịch vào pool và lấy tất cả các gói giao dịch từ pool.

## Cấu trúc `PackPool`

### Thuộc tính

- `packs`: Danh sách các gói giao dịch thuộc kiểu `types.Pack`.
- `mutex`: Đối tượng khóa (`sync.Mutex`) để đảm bảo an toàn khi truy cập đồng thời vào `packs`.

### Hàm khởi tạo

- `NewPackPool() *PackPool`: Tạo một đối tượng `PackPool` mới.

### Các phương thức

- `AddPack(pack types.Pack)`: Thêm một gói giao dịch vào `PackPool`. Sử dụng khóa để đảm bảo an toàn khi truy cập đồng thời.
- `Addpacks(packs []types.Pack)`: Thêm nhiều gói giao dịch vào `PackPool`. Sử dụng khóa để đảm bảo an toàn khi truy cập đồng thời.
- `Getpacks() []types.Pack`: Lấy tất cả các gói giao dịch từ `PackPool` và làm rỗng danh sách `packs`. Sử dụng khóa để đảm bảo an toàn khi truy cập đồng thời.

## Kết luận

File `pack_pool.go` cung cấp các phương thức cần thiết để quản lý một tập hợp các gói giao dịch trong blockchain. Nó hỗ trợ việc thêm và lấy các gói giao dịch một cách an toàn trong môi trường đa luồng.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`receipt.go`](./receipt/receipt.go)

## Giới thiệu

File `receipt.go` định nghĩa cấu trúc `Receipt` và các phương thức liên quan để quản lý biên nhận của các giao dịch trong blockchain. Biên nhận chứa thông tin về giao dịch, bao gồm hash giao dịch, địa chỉ gửi và nhận, số lượng, trạng thái, và các log sự kiện.

## Cấu trúc `Receipt`

### Thuộc tính

- `proto`: Đối tượng Protobuf `pb.Receipt` lưu trữ thông tin biên nhận.

### Hàm khởi tạo

- `NewReceipt(transactionHash common.Hash, fromAddress common.Address, toAddress common.Address, amount *big.Int, action pb.ACTION, status pb.RECEIPT_STATUS, returnValue []byte, exception pb.EXCEPTION, gastFee uint64, gasUsed uint64, eventLogs []types.EventLog) types.Receipt`: Tạo một đối tượng `Receipt` mới với các thông tin được cung cấp.

### Các phương thức

#### Getter

- `TransactionHash() common.Hash`: Trả về hash của giao dịch.
- `FromAddress() common.Address`: Trả về địa chỉ gửi.
- `ToAddress() common.Address`: Trả về địa chỉ nhận.
- `GasUsed() uint64`: Trả về lượng gas đã sử dụng.
- `GasFee() uint64`: Trả về phí gas.
- `Amount() *big.Int`: Trả về số lượng giao dịch.
- `Return() []byte`: Trả về giá trị trả về của giao dịch.
- `Status() pb.RECEIPT_STATUS`: Trả về trạng thái của biên nhận.
- `Action() pb.ACTION`: Trả về hành động của biên nhận.
- `EventLogs() []*pb.EventLog`: Trả về danh sách log sự kiện.

#### Setter

- `UpdateExecuteResult(status pb.RECEIPT_STATUS, returnValue []byte, exception pb.EXCEPTION, gasUsed uint64, eventLogs []types.EventLog)`: Cập nhật kết quả thực thi cho biên nhận.

#### Khác

- `Json() ([]byte, error)`: Chuyển đổi biên nhận thành định dạng JSON.
- `ReceiptsToProto(receipts []types.Receipt) []*pb.Receipt`: Chuyển đổi danh sách biên nhận thành danh sách đối tượng Protobuf.
- `ProtoToReceipts(protoReceipts []*pb.Receipt) []types.Receipt`: Chuyển đổi danh sách đối tượng Protobuf thành danh sách biên nhận.

## Kết luận

File `receipt.go` cung cấp các phương thức cần thiết để quản lý biên nhận của các giao dịch trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi biên nhận giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin giao dịch.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`receipts.go`](./receipt/receipts.go)

## Giới thiệu

File `receipts.go` định nghĩa cấu trúc `Receipts` và các phương thức liên quan để quản lý tập hợp các biên nhận (`Receipt`) trong blockchain. Nó bao gồm các phương thức để thêm biên nhận, cập nhật kết quả thực thi, và tính toán tổng lượng gas đã sử dụng.

## Cấu trúc `Receipts`

### Thuộc tính

- `trie`: Cây Merkle Patricia Trie để lưu trữ và quản lý các biên nhận.
- `receipts`: Map lưu trữ các biên nhận với hash giao dịch là key và biên nhận là value.

### Biến toàn cục

- `ErrorReceiptNotFound`: Biến lỗi được trả về khi không tìm thấy biên nhận.

### Hàm khởi tạo

- `NewReceipts() types.Receipts`: Tạo một đối tượng `Receipts` mới với một cây Merkle Patricia Trie trống và một map biên nhận trống.

### Các phương thức

- `ReceiptsRoot() (common.Hash, error)`: Tính toán và trả về hash của gốc cây Merkle Patricia Trie.
- `AddReceipt(receipt types.Receipt) error`: Thêm một biên nhận vào `Receipts` và cập nhật cây Merkle Patricia Trie.
- `ReceiptsMap() map[common.Hash]types.Receipt`: Trả về map các biên nhận.
- `UpdateExecuteResultToReceipt(hash common.Hash, status pb.RECEIPT_STATUS, returnValue []byte, exception pb.EXCEPTION, gasUsed uint64, eventLogs []types.EventLog) error`: Cập nhật kết quả thực thi cho một biên nhận dựa trên hash giao dịch.
- `GasUsed() uint64`: Tính toán và trả về tổng lượng gas đã sử dụng của tất cả các biên nhận.

## Kết luận

File `receipts.go` cung cấp các phương thức cần thiết để quản lý tập hợp các biên nhận trong blockchain. Nó hỗ trợ việc thêm, cập nhật, và tính toán thông tin từ các biên nhận, giúp dễ dàng quản lý và truy xuất dữ liệu giao dịch.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`remote_storage_db.go`](./remote_storage_db/remote_storage_db.go)

## Giới thiệu

File `remote_storage_db.go` định nghĩa cấu trúc `RemoteStorageDB` và các phương thức liên quan để quản lý kết nối từ xa tới cơ sở dữ liệu lưu trữ. Nó cho phép lấy dữ liệu và mã từ hợp đồng thông minh thông qua kết nối mạng.

## Cấu trúc `RemoteStorageDB`

### Thuộc tính

- `remoteConnection`: Đối tượng `network.Connection` để quản lý kết nối từ xa.
- `messageSender`: Đối tượng `network.MessageSender` để gửi tin nhắn qua kết nối.
- `address`: Địa chỉ thuộc kiểu `common.Address` của đối tượng.
- `currentBlockNumber`: Số block hiện tại thuộc kiểu `uint64`.
- `sync.Mutex`: Đối tượng khóa để đảm bảo an toàn khi truy cập đồng thời.

### Hàm khởi tạo

- `NewRemoteStorageDB(remoteConnection network.Connection, messageSender network.MessageSender, address common.Address) *RemoteStorageDB`: Tạo một đối tượng `RemoteStorageDB` mới với kết nối từ xa, người gửi tin nhắn và địa chỉ được cung cấp.

### Các phương thức

- `checkConnection() error`: Kiểm tra và thiết lập kết nối nếu chưa kết nối.
- `Get(key []byte) ([]byte, error)`: Lấy dữ liệu từ cơ sở dữ liệu từ xa dựa trên khóa được cung cấp.
- `GetCode(address common.Address) ([]byte, error)`: Lấy mã hợp đồng thông minh từ cơ sở dữ liệu từ xa dựa trên địa chỉ được cung cấp.
- `SetBlockNumber(blockNumber uint64)`: Đặt số block hiện tại.
- `Close()`: Ngắt kết nối từ xa.

## Kết luận

File `remote_storage_db.go` cung cấp các phương thức cần thiết để quản lý kết nối từ xa tới cơ sở dữ liệu lưu trữ. Nó hỗ trợ việc lấy dữ liệu và mã từ hợp đồng thông minh, đảm bảo an toàn khi truy cập đồng thời và quản lý kết nối hiệu quả.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`event_log.go`](./smart_contract/event_log.go)

## Giới thiệu

File `event_log.go` định nghĩa cấu trúc `EventLog` và các phương thức liên quan để quản lý log sự kiện của các giao dịch trong blockchain. Log sự kiện chứa thông tin về block, giao dịch, địa chỉ, dữ liệu và các chủ đề liên quan.

## Cấu trúc `EventLog`

### Thuộc tính

- `proto`: Đối tượng Protobuf `pb.EventLog` lưu trữ thông tin log sự kiện.

### Hàm khởi tạo

- `NewEventLog(blockNumber uint64, transactionHash common.Hash, address common.Address, data []byte, topics [][]byte) types.EventLog`: Tạo một đối tượng `EventLog` mới với các thông tin được cung cấp.

### Các phương thức

#### General

- `Proto() *pb.EventLog`: Trả về đối tượng Protobuf của log sự kiện.
- `FromProto(logPb *pb.EventLog)`: Khởi tạo `EventLog` từ một đối tượng Protobuf.
- `Unmarshal(b []byte) error`: Giải mã dữ liệu từ một slice byte thành `EventLog`.
- `Marshal() ([]byte, error)`: Mã hóa `EventLog` thành một slice byte.
- `Copy() types.EventLog`: Tạo một bản sao của `EventLog`.

#### Getter

- `Hash() common.Hash`: Tính toán và trả về hash của log sự kiện.
- `Address() common.Address`: Trả về địa chỉ liên quan đến log sự kiện.
- `BlockNumber() string`: Trả về số block dưới dạng chuỗi hex.
- `TransactionHash() string`: Trả về hash của giao dịch dưới dạng chuỗi hex.
- `Data() string`: Trả về dữ liệu của log sự kiện dưới dạng chuỗi hex.
- `Topics() []string`: Trả về danh sách các chủ đề dưới dạng chuỗi hex.

#### Khác

- `String() string`: Trả về chuỗi mô tả của `EventLog`.

## Kết luận

File `event_log.go` cung cấp các phương thức cần thiết để quản lý log sự kiện của các giao dịch trong blockchain. Nó hỗ trợ việc tạo, mã hóa, giải mã và truy xuất thông tin từ log sự kiện.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`event_logs.go`](./smart_contract/event_logs.go)

## Giới thiệu

File `event_logs.go` định nghĩa cấu trúc `EventLogs` và các phương thức liên quan để quản lý tập hợp các log sự kiện trong blockchain. Nó bao gồm các phương thức để tạo, mã hóa, giải mã và truy xuất danh sách log sự kiện.

## Cấu trúc `EventLogs`

### Thuộc tính

- `proto`: Đối tượng Protobuf `pb.EventLogs` lưu trữ thông tin tập hợp log sự kiện.

### Hàm khởi tạo

- `NewEventLogs(eventLogs []types.EventLog) types.EventLogs`: Tạo một đối tượng `EventLogs` mới từ danh sách các log sự kiện.

### Các phương thức

#### General

- `FromProto(logPb *pb.EventLogs)`: Khởi tạo `EventLogs` từ một đối tượng Protobuf.
- `Unmarshal(b []byte) error`: Giải mã dữ liệu từ một slice byte thành `EventLogs`.
- `Marshal() ([]byte, error)`: Mã hóa `EventLogs` thành một slice byte.
- `Proto() *pb.EventLogs`: Trả về đối tượng Protobuf của tập hợp log sự kiện.

#### Getter

- `EventLogList() []types.EventLog`: Trả về danh sách các log sự kiện.

#### Khác

- `Copy() types.EventLogs`: Tạo một bản sao của `EventLogs`.

## Kết luận

File `event_logs.go` cung cấp các phương thức cần thiết để quản lý tập hợp các log sự kiện trong blockchain. Nó hỗ trợ việc tạo, mã hóa, giải mã và truy xuất thông tin từ danh sách log sự kiện.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`execute_sc_result.go`](./smart_contract/execute_sc_result.go)

## Giới thiệu

File `execute_sc_result.go` định nghĩa cấu trúc `ExecuteSCResult` và các phương thức liên quan để quản lý kết quả thực thi của các smart contract trong blockchain. Kết quả thực thi bao gồm thông tin về hash giao dịch, trạng thái, ngoại lệ, dữ liệu trả về, và các thay đổi liên quan đến tài khoản và smart contract.

## Cấu trúc `ExecuteSCResult`

### Thuộc tính

- `transactionHash`: Hash của giao dịch thuộc kiểu `common.Hash`.
- `status`: Trạng thái của biên nhận thuộc kiểu `pb.RECEIPT_STATUS`.
- `exception`: Ngoại lệ xảy ra trong quá trình thực thi thuộc kiểu `pb.EXCEPTION`.
- `returnData`: Dữ liệu trả về từ smart contract thuộc kiểu `[]byte`.
- `gasUsed`: Lượng gas đã sử dụng thuộc kiểu `uint64`.
- `logsHash`: Hash của các log sự kiện thuộc kiểu `common.Hash`.
- `mapAddBalance`, `mapSubBalance`: Map lưu trữ các thay đổi số dư.
- `mapStorageRoot`, `mapCodeHash`: Map lưu trữ root của trạng thái và hash của mã.
- `mapStorageAddress`, `mapCreatorPubkey`: Map lưu trữ địa chỉ và khóa công khai của người tạo.
- `mapStorageAddressTouchedAddresses`: Map lưu trữ các địa chỉ đã được touch.
- `mapNativeSmartContractUpdateStorage`: Map lưu trữ các cập nhật của smart contract gốc.
- `eventLogs`: Danh sách các log sự kiện thuộc kiểu `[]types.EventLog`.

### Hàm khởi tạo

- `NewExecuteSCResult(...) *ExecuteSCResult`: Tạo một đối tượng `ExecuteSCResult` mới với các thông tin được cung cấp.
- `NewErrorExecuteSCResult(...) *ExecuteSCResult`: Tạo một đối tượng `ExecuteSCResult` mới cho trường hợp lỗi.

### Các phương thức

- `FromProto(pbData *pb.ExecuteSCResult)`: Khởi tạo `ExecuteSCResult` từ một đối tượng Protobuf.
- `Proto() protoreflect.ProtoMessage`: Chuyển đổi `ExecuteSCResult` thành đối tượng Protobuf.
- `Unmarshal(b []byte) error`: Khởi tạo `ExecuteSCResult` từ một slice byte đã được mã hóa.
- `Marshal() ([]byte, error)`: Chuyển đổi `ExecuteSCResult` thành một slice byte để lưu trữ hoặc truyền tải.
- `String() string`: Trả về chuỗi mô tả của `ExecuteSCResult`.

### Getter

- `TransactionHash() common.Hash`: Trả về hash của giao dịch.
- `MapAddBalance() map[string][]byte`: Trả về map thay đổi số dư thêm.
- `MapSubBalance() map[string][]byte`: Trả về map thay đổi số dư trừ.
- `MapStorageRoot() map[string][]byte`: Trả về map root của trạng thái.
- `MapCodeHash() map[string][]byte`: Trả về map hash của mã.
- `MapStorageAddress() map[string]common.Address`: Trả về map địa chỉ lưu trữ.
- `MapCreatorPubkey() map[string][]byte`: Trả về map khóa công khai của người tạo.
- `GasUsed() uint64`: Trả về lượng gas đã sử dụng.
- `ReceiptStatus() pb.RECEIPT_STATUS`: Trả về trạng thái của biên nhận.
- `Exception() pb.EXCEPTION`: Trả về ngoại lệ xảy ra.
- `Return() []byte`: Trả về dữ liệu trả về.
- `LogsHash() common.Hash`: Trả về hash của các log sự kiện.
- `EventLogs() []types.EventLog`: Trả về danh sách các log sự kiện.
- `MapStorageAddressTouchedAddresses() map[common.Address][]common.Address`: Trả về map các địa chỉ đã được touch.
- `MapNativeSmartContractUpdateStorage() map[common.Address][][2][]byte`: Trả về map các cập nhật của smart contract gốc.

## Kết luận

File `execute_sc_result.go` cung cấp các phương thức cần thiết để quản lý kết quả thực thi của các smart contract trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi kết quả thực thi giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`execute_sc_results.go`](./smart_contract/execute_sc_results.go)

## Giới thiệu

File `execute_sc_results.go` định nghĩa cấu trúc `ExecuteSCResults` và các phương thức liên quan để quản lý tập hợp các kết quả thực thi của smart contract trong blockchain. Nó bao gồm thông tin về ID nhóm và số block.

## Cấu trúc `ExecuteSCResults`

### Thuộc tính

- `results`: Danh sách các kết quả thực thi thuộc kiểu `[]types.ExecuteSCResult`.
- `groupId`: ID của nhóm thuộc kiểu `uint64`.
- `blockNumber`: Số block thuộc kiểu `uint64`.

### Hàm khởi tạo

- `NewExecuteSCResults(results []types.ExecuteSCResult, groupId uint64, blockNumber uint64) *ExecuteSCResults`: Tạo một đối tượng `ExecuteSCResults` mới với các thông tin được cung cấp.

### Các phương thức

- `Unmarshal(b []byte) error`: Khởi tạo `ExecuteSCResults` từ một slice byte đã được mã hóa.
- `Marshal() ([]byte, error)`: Chuyển đổi `ExecuteSCResults` thành một slice byte để lưu trữ hoặc truyền tải.
- `Proto() protoreflect.ProtoMessage`: Chuyển đổi `ExecuteSCResults` thành đối tượng Protobuf.
- `FromProto(pbData *pb.ExecuteSCResults)`: Khởi tạo `ExecuteSCResults` từ một đối tượng Protobuf.
- `String() string`: Trả về chuỗi mô tả của `ExecuteSCResults`.

### Getter

- `GroupId() uint64`: Trả về ID của nhóm.
- `BlockNumber() uint64`: Trả về số block.
- `Results() []types.ExecuteSCResult`: Trả về danh sách các kết quả thực thi.

## Kết luận

File `execute_sc_results.go` cung cấp các phương thức cần thiết để quản lý tập hợp các kết quả thực thi của smart contract trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi kết quả thực thi giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`smart_contract_update_data.go`](./smart_contract/smart_contract_update_data.go)

## Giới thiệu

File `smart_contract_update_data.go` định nghĩa cấu trúc `SmartContractUpdateData` và các phương thức liên quan để quản lý dữ liệu cập nhật của smart contract trong blockchain. Dữ liệu cập nhật bao gồm mã hợp đồng, lưu trữ và các log sự kiện.

## Cấu trúc `SmartContractUpdateData`

### Thuộc tính

- `code`: Mã của smart contract thuộc kiểu `[]byte`.
- `storage`: Map lưu trữ dữ liệu của smart contract với key là chuỗi và value là `[]byte`.
- `eventLogs`: Danh sách các log sự kiện thuộc kiểu `[]types.EventLog`.

### Hàm khởi tạo

- `NewSmartContractUpdateData(code []byte, storage map[string][]byte, eventLogs []types.EventLog) *SmartContractUpdateData`: Tạo một đối tượng `SmartContractUpdateData` mới với mã, lưu trữ và log sự kiện được cung cấp.

### Các phương thức

- `Code() []byte`: Trả về mã của smart contract.
- `Storage() map[string][]byte`: Trả về map lưu trữ của smart contract.
- `EventLogs() []types.EventLog`: Trả về danh sách log sự kiện.
- `CodeHash() common.Hash`: Tính toán và trả về hash của mã smart contract.
- `SetCode(code []byte)`: Đặt mã mới cho smart contract.
- `UpdateStorage(storage map[string][]byte)`: Cập nhật lưu trữ của smart contract.
- `AddEventLog(eventLog types.EventLog)`: Thêm một log sự kiện vào danh sách.
- `Marshal() ([]byte, error)`: Chuyển đổi `SmartContractUpdateData` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(b []byte) error`: Khởi tạo `SmartContractUpdateData` từ một slice byte đã được mã hóa.
- `FromProto(fbProto *pb.SmartContractUpdateData)`: Khởi tạo `SmartContractUpdateData` từ một đối tượng Protobuf.
- `Proto() *pb.SmartContractUpdateData`: Chuyển đổi `SmartContractUpdateData` thành đối tượng Protobuf.
- `String() string`: Trả về chuỗi mô tả của `SmartContractUpdateData`.

## Kết luận

File `smart_contract_update_data.go` cung cấp các phương thức cần thiết để quản lý và chuyển đổi dữ liệu cập nhật của smart contract trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi dữ liệu giữa các định dạng khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`smart_contract_update_datas.go`](./smart_contract/smart_contract_update_datas.go)

## Giới thiệu

File `smart_contract_update_datas.go` định nghĩa cấu trúc `SmartContractUpdateDatas` và các phương thức liên quan để quản lý tập hợp dữ liệu cập nhật của nhiều smart contract trong blockchain. Nó bao gồm thông tin về số block và dữ liệu cập nhật của từng smart contract.

## Cấu trúc `SmartContractUpdateDatas`

### Thuộc tính

- `blockNumber`: Số block thuộc kiểu `uint64`.
- `data`: Map lưu trữ dữ liệu cập nhật của smart contract với địa chỉ là key và `SmartContractUpdateData` là value.

### Hàm khởi tạo

- `NewSmartContractUpdateDatas(blockNumber uint64, data map[common.Address]types.SmartContractUpdateData) *SmartContractUpdateDatas`: Tạo một đối tượng `SmartContractUpdateDatas` mới với số block và dữ liệu cập nhật được cung cấp.

### Các phương thức

- `Data() map[common.Address]types.SmartContractUpdateData`: Trả về map dữ liệu cập nhật của smart contract.
- `BlockNumber() uint64`: Trả về số block.
- `Proto() *pb.SmartContractUpdateDatas`: Chuyển đổi `SmartContractUpdateDatas` thành đối tượng Protobuf.
- `FromProto(pbData *pb.SmartContractUpdateDatas)`: Khởi tạo `SmartContractUpdateDatas` từ một đối tượng Protobuf.
- `Marshal() ([]byte, error)`: Chuyển đổi `SmartContractUpdateDatas` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(data []byte) error`: Khởi tạo `SmartContractUpdateDatas` từ một slice byte đã được mã hóa.

## Kết luận

File `smart_contract_update_datas.go` cung cấp các phương thức cần thiết để quản lý và chuyển đổi tập hợp dữ liệu cập nhật của smart contract trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi dữ liệu giữa các định dạng khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`touched_addresses_data.go`](./smart_contract/touched_addresses_data.go)

## Giới thiệu

File `touched_addresses_data.go` định nghĩa cấu trúc `TouchedAddressesData` và các phương thức liên quan để quản lý dữ liệu về các địa chỉ đã được touch trong một block của blockchain. Nó bao gồm thông tin về số block và danh sách các địa chỉ liên quan.

## Cấu trúc `TouchedAddressesData`

### Thuộc tính

- `blockNumber`: Số block thuộc kiểu `uint64`.
- `addresses`: Danh sách các địa chỉ thuộc kiểu `[]common.Address`.

### Hàm khởi tạo

- `NewTouchedAddressesData(blockNumber uint64, addresses []common.Address) *TouchedAddressesData`: Tạo một đối tượng `TouchedAddressesData` mới với số block và danh sách địa chỉ được cung cấp.

### Các phương thức

- `BlockNumber() uint64`: Trả về số block.
- `Addresses() []common.Address`: Trả về danh sách các địa chỉ.
- `Proto() *pb.TouchedAddressesData`: Chuyển đổi `TouchedAddressesData` thành đối tượng Protobuf.
- `FromProto(pbTad *pb.TouchedAddressesData)`: Khởi tạo `TouchedAddressesData` từ một đối tượng Protobuf.
- `Unmarshal(data []byte) error`: Khởi tạo `TouchedAddressesData` từ một slice byte đã được mã hóa.
- `Marshal() ([]byte, error)`: Chuyển đổi `TouchedAddressesData` thành một slice byte để lưu trữ hoặc truyền tải.

## Kết luận

File `touched_addresses_data.go` cung cấp các phương thức cần thiết để quản lý và chuyển đổi dữ liệu về các địa chỉ đã được touch trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi dữ liệu giữa các định dạng khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`smart_contract_db.go`](./smart_contract_db/smart_contract_db.go)

## Giới thiệu

File `smart_contract_db.go` định nghĩa cấu trúc `SmartContractDB` và các phương thức liên quan để quản lý cơ sở dữ liệu của smart contract trong blockchain. Nó bao gồm các chức năng để lưu trữ, truy xuất mã và dữ liệu lưu trữ của smart contract, cũng như quản lý các cập nhật và sự kiện liên quan.

## Cấu trúc `SmartContractDB`

### Thuộc tính

- `cacheRemoteDBs`: Bộ nhớ đệm cho các kết nối cơ sở dữ liệu từ xa, sử dụng `otter.Cache`.
- `cacheCode`: Bộ nhớ đệm cho mã của smart contract, sử dụng `otter.Cache`.
- `cacheStorageTrie`: Bộ nhớ đệm cho cây Merkle Patricia Trie của lưu trữ smart contract, sử dụng `otter.Cache`.
- `messageSender`: Đối tượng gửi tin nhắn thuộc kiểu `t_network.MessageSender`.
- `dnsLink`: Chuỗi liên kết DNS.
- `accountStateDB`: Cơ sở dữ liệu trạng thái tài khoản thuộc kiểu `AccountStateDB`.
- `currentBlockNumber`: Số block hiện tại thuộc kiểu `uint64`.
- `updateDatas`: Map lưu trữ dữ liệu cập nhật của smart contract với địa chỉ là key và `SmartContractUpdateData` là value.

### Hàm khởi tạo

- `NewSmartContractDB(messageSender t_network.MessageSender, dnsLink string, accountStateDB AccountStateDB, currentBlockNumber uint64) *SmartContractDB`: Tạo một đối tượng `SmartContractDB` mới với các thông tin được cung cấp.

### Các phương thức

- `SetAccountStateDB(asdb types.AccountStateDB)`: Đặt cơ sở dữ liệu trạng thái tài khoản.
- `CreateRemoteStorageDB(as types.AccountState) (RemoteStorageDB, error)`: Tạo một kết nối cơ sở dữ liệu từ xa mới.
- `Code(address common.Address) []byte`: Trả về mã của smart contract tại địa chỉ được cung cấp.
- `StorageValue(address common.Address, key []byte) []byte`: Trả về giá trị lưu trữ của smart contract tại địa chỉ và khóa được cung cấp.
- `SetBlockNumber(blockNumber uint64)`: Đặt số block hiện tại.
- `SetCode(address common.Address, codeHash common.Hash, code []byte)`: Đặt mã cho smart contract tại địa chỉ được cung cấp.
- `SetStorageValue(address common.Address, key []byte, value []byte) error`: Đặt giá trị lưu trữ cho smart contract tại địa chỉ và khóa được cung cấp.
- `AddEventLogs(eventLogs []types.EventLog)`: Thêm các log sự kiện vào dữ liệu cập nhật của smart contract.
- `NewTrieStorage(address common.Address) common.Hash`: Tạo một cây Merkle Patricia Trie mới cho lưu trữ smart contract.
- `StorageRoot(address common.Address) common.Hash`: Trả về hash của gốc lưu trữ cho smart contract tại địa chỉ được cung cấp.
- `DeleteAddress(address common.Address)`: Xóa địa chỉ khỏi bộ nhớ đệm.
- `GetSmartContractUpdateDatas() map[common.Address]types.SmartContractUpdateData`: Trả về map dữ liệu cập nhật của smart contract.
- `ClearSmartContractUpdateDatas()`: Xóa tất cả dữ liệu cập nhật của smart contract.

## Kết luận

File `smart_contract_db.go` cung cấp các phương thức cần thiết để quản lý cơ sở dữ liệu của smart contract trong blockchain. Nó hỗ trợ việc lưu trữ, truy xuất và cập nhật dữ liệu của smart contract, giúp dễ dàng quản lý và truy xuất thông tin liên quan.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`stake_abi.go`](./stake/stake_abi.go)

## Giới thiệu

File `stake_abi.go` định nghĩa hàm `StakeABI` để cung cấp giao diện nhị phân ứng dụng (ABI) cho các hàm liên quan đến stake trong smart contract. ABI là một tiêu chuẩn để tương tác với các smart contract trên Ethereum, cho phép mã hóa và giải mã các lời gọi hàm và dữ liệu.

## Hàm `StakeABI`

### Mô tả

- `StakeABI() abi.ABI`: Hàm này trả về một đối tượng `abi.ABI` được khởi tạo từ một chuỗi JSON mô tả các hàm có sẵn trong smart contract liên quan đến stake.

### Chi tiết

- **Hàm `getStakeInfo`**:
  - **Inputs**: Nhận một địa chỉ (`address`) làm tham số đầu vào.
  - **Outputs**: Trả về thông tin stake bao gồm:
    - `owner`: Địa chỉ của chủ sở hữu.
    - `amount`: Số lượng stake.
    - `childNodes`: Danh sách các địa chỉ node con.
    - `childExecuteMiners`: Danh sách các địa chỉ thợ mỏ thực thi.
    - `childVerifyMiners`: Danh sách các địa chỉ thợ mỏ xác minh.
  - **State Mutability**: `view` (chỉ đọc, không thay đổi trạng thái blockchain).

- **Hàm `getValidatorsWithStakeAmount`**:
  - **Inputs**: Không có tham số đầu vào.
  - **Outputs**: Trả về danh sách các địa chỉ và số lượng stake tương ứng.
    - `addresses`: Danh sách các địa chỉ.
    - `amounts`: Danh sách số lượng stake tương ứng với các địa chỉ.
  - **State Mutability**: `view` (chỉ đọc, không thay đổi trạng thái blockchain).

## Kết luận

File `stake_abi.go` cung cấp một cách để mã hóa và giải mã các lời gọi hàm liên quan đến stake trong smart contract. Điều này giúp dễ dàng tương tác với các smart contract từ các ứng dụng Go, đảm bảo rằng dữ liệu được truyền tải chính xác và hiệu quả.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`stake_getter.go`](./stake/stake_getter.go)

## Giới thiệu

File `stake_getter.go` định nghĩa cấu trúc `StakeGetter` và các phương thức liên quan để truy xuất thông tin stake từ blockchain. Nó sử dụng các giao diện của MVM (Meta Virtual Machine) để gọi các hàm smart contract và lấy dữ liệu về stake.

## Cấu trúc `StakeGetter`

### Thuộc tính

- `accountStatesDB`: Cơ sở dữ liệu trạng thái tài khoản thuộc kiểu `mvm.AccountStateDB`.
- `smartContractDB`: Cơ sở dữ liệu smart contract thuộc kiểu `mvm.SmartContractDB`.

### Hàm khởi tạo

- `NewStakeGetter(accountStatesDB mvm.AccountStateDB, smartContractDB mvm.SmartContractDB) *StakeGetter`: Tạo một đối tượng `StakeGetter` mới với cơ sở dữ liệu trạng thái tài khoản và smart contract được cung cấp.

### Các phương thức

- `checkMvm() (*mvm.MVMApi, error)`: Kiểm tra và khởi tạo API MVM nếu cần thiết.
- `GetValidatorsWithStakeAmount() (map[common.Address]*big.Int, error)`: Lấy danh sách các validator và số lượng stake tương ứng.
- `GetStakeInfo(nodeAddress common.Address) (*StakeInfo, error)`: Lấy thông tin stake cho một địa chỉ node cụ thể.

## Kết luận

File `stake_getter.go` cung cấp các phương thức cần thiết để truy xuất thông tin stake từ blockchain. Nó sử dụng MVM để gọi các hàm smart contract và lấy dữ liệu, giúp dễ dàng quản lý và truy xuất thông tin stake.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`stake_info.go`](./stake/stake_info.go)

## Giới thiệu

File `stake_info.go` định nghĩa cấu trúc `StakeInfo` và các phương thức liên quan để quản lý thông tin stake trong blockchain. Thông tin stake bao gồm chủ sở hữu, số lượng stake, và danh sách các node con và miner liên quan.

## Cấu trúc `StakeInfo`

### Thuộc tính

- `owner`: Địa chỉ của chủ sở hữu thuộc kiểu `common.Address`.
- `amount`: Số lượng stake thuộc kiểu `*big.Int`.
- `childNodes`: Danh sách các địa chỉ node con thuộc kiểu `[]common.Address`.
- `childExecuteMiners`: Danh sách các địa chỉ miner thực thi thuộc kiểu `[]common.Address`.
- `childVerifyMiners`: Danh sách các địa chỉ miner xác minh thuộc kiểu `[]common.Address`.

### Hàm khởi tạo

- `NewStakeInfo(owner common.Address, amount *big.Int, childNodes []common.Address, childExecuteMiners []common.Address, childVerifyMiners []common.Address) *StakeInfo`: Tạo một đối tượng `StakeInfo` mới với các thông tin được cung cấp.

### Các phương thức

- `Owner() common.Address`: Trả về địa chỉ của chủ sở hữu.
- `Amount() *big.Int`: Trả về số lượng stake.
- `ChildNodes() []common.Address`: Trả về danh sách các địa chỉ node con.
- `ChildExecuteMiners() []common.Address`: Trả về danh sách các địa chỉ miner thực thi.
- `ChildVerifyMiners() []common.Address`: Trả về danh sách các địa chỉ miner xác minh.

## Kết luận

File `stake_info.go` cung cấp các phương thức cần thiết để quản lý thông tin stake trong blockchain. Nó hỗ trợ việc tạo, truy xuất thông tin về chủ sở hữu, số lượng stake, và các node con và miner liên quan.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`stake_smart_contract_db.go`](./stake/stake_smart_contract_db.go)

## Giới thiệu

File `stake_smart_contract_db.go` định nghĩa cấu trúc `StakeSmartContractDb` và các phương thức liên quan để quản lý cơ sở dữ liệu của smart contract liên quan đến stake trong blockchain. Nó bao gồm các chức năng để lưu trữ, truy xuất mã và dữ liệu lưu trữ của smart contract, cũng như quản lý các cập nhật và sự kiện liên quan.

## Cấu trúc `StakeSmartContractDb`

### Thuộc tính

- `codePath`: Đường dẫn tới file chứa mã của smart contract.
- `storageRootPath`: Đường dẫn tới file chứa root của lưu trữ.
- `storageDBPath`: Đường dẫn tới cơ sở dữ liệu lưu trữ.
- `checkPointPath`: Đường dẫn tới file chứa checkpoint.
- `code`: Mã của smart contract thuộc kiểu `[]byte`.
- `storageTrie`: Cây Merkle Patricia Trie để quản lý lưu trữ.
- `storageDB`: Cơ sở dữ liệu lưu trữ thuộc kiểu `storage.Storage`.
- `storageRoot`: Hash của root lưu trữ thuộc kiểu `common.Hash`.
- `hasDirty`: Biến boolean để kiểm tra xem có thay đổi nào chưa được commit hay không.

### Hàm khởi tạo

- `NewStakeSmartContractDb(codePath string, storageRootPath string, storageDBPath string, checkPointPath string) (*StakeSmartContractDb, error)`: Tạo một đối tượng `StakeSmartContractDb` mới với các đường dẫn được cung cấp.

### Các phương thức

- `Code(address common.Address) []byte`: Trả về mã của smart contract.
- `StorageValue(address common.Address, key []byte) []byte`: Trả về giá trị lưu trữ của smart contract tại địa chỉ và khóa được cung cấp.
- `UpdateStorageValue(key []byte, value []byte)`: Cập nhật giá trị lưu trữ cho smart contract.
- `Commit() (common.Hash, error)`: Commit các thay đổi vào lưu trữ và trả về hash của root mới.
- `Discard()`: Hủy bỏ các thay đổi chưa được commit.
- `CopyFrom(from *StakeSmartContractDb)`: Sao chép dữ liệu từ một đối tượng `StakeSmartContractDb` khác.
- `HasDirty() bool`: Kiểm tra xem có thay đổi nào chưa được commit hay không.
- `StakeStorageDB() storage.Storage`: Trả về cơ sở dữ liệu lưu trữ.
- `StakeStorageRoot() common.Hash`: Trả về hash của root lưu trữ.
- `SaveCheckPoint(blockNumber uint64) error`: Lưu checkpoint cho block số `blockNumber`.
- `UpdateFromCheckPointData(storageRoot common.Hash, storageData [][2][]byte) error`: Cập nhật dữ liệu từ checkpoint.
- `CheckPointPath() string`: Trả về đường dẫn tới checkpoint.

## Kết luận

File `stake_smart_contract_db.go` cung cấp các phương thức cần thiết để quản lý cơ sở dữ liệu của smart contract liên quan đến stake trong blockchain. Nó hỗ trợ việc lưu trữ, truy xuất và cập nhật dữ liệu của smart contract, giúp dễ dàng quản lý và truy xuất thông tin liên quan.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`account_state.go`](./state/account_state.go)

## Giới thiệu

File `account_state.go` định nghĩa cấu trúc `AccountState` và các phương thức liên quan để quản lý trạng thái tài khoản trong blockchain. Nó bao gồm thông tin về địa chỉ, số dư, khóa thiết bị, và trạng thái của smart contract liên quan.

## Cấu trúc `AccountState`

### Thuộc tính

- `address`: Địa chỉ của tài khoản thuộc kiểu `common.Address`.
- `lastHash`: Hash cuối cùng của tài khoản thuộc kiểu `common.Hash`.
- `balance`: Số dư của tài khoản thuộc kiểu `*big.Int`.
- `pendingBalance`: Số dư đang chờ xử lý thuộc kiểu `*big.Int`.
- `deviceKey`: Khóa thiết bị thuộc kiểu `common.Hash`.
- `smartContractState`: Trạng thái của smart contract liên quan thuộc kiểu `types.SmartContractState`.

### Hàm khởi tạo

- `NewAccountState(address common.Address) types.AccountState`: Tạo một đối tượng `AccountState` mới với địa chỉ được cung cấp.

### Các phương thức

- `Proto() *pb.AccountState`: Chuyển đổi `AccountState` thành đối tượng Protobuf.
- `FromProto(pbData *pb.AccountState)`: Khởi tạo `AccountState` từ một đối tượng Protobuf.
- `Marshal() ([]byte, error)`: Chuyển đổi `AccountState` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(b []byte) error`: Khởi tạo `AccountState` từ một slice byte đã được mã hóa.
- `Copy() types.AccountState`: Tạo một bản sao của `AccountState`.
- `String() string`: Trả về chuỗi mô tả của `AccountState`.
- `Address() common.Address`: Trả về địa chỉ của tài khoản.
- `Balance() *big.Int`: Trả về số dư của tài khoản.
- `PendingBalance() *big.Int`: Trả về số dư đang chờ xử lý.
- `TotalBalance() *big.Int`: Tính toán và trả về tổng số dư.
- `LastHash() common.Hash`: Trả về hash cuối cùng của tài khoản.
- `SmartContractState() types.SmartContractState`: Trả về trạng thái của smart contract liên quan.
- `DeviceKey() common.Hash`: Trả về khóa thiết bị.
- `SetBalance(newBalance *big.Int)`: Đặt số dư mới cho tài khoản.
- `SetNewDeviceKey(newDeviceKey common.Hash)`: Đặt khóa thiết bị mới.
- `SetLastHash(newLastHash common.Hash)`: Đặt hash cuối cùng mới.
- `SetSmartContractState(smState types.SmartContractState)`: Đặt trạng thái smart contract mới.
- `AddPendingBalance(amount *big.Int)`: Thêm số dư đang chờ xử lý.
- `SubPendingBalance(amount *big.Int) error`: Trừ số dư đang chờ xử lý.
- `SubBalance(amount *big.Int) error`: Trừ số dư.
- `SubTotalBalance(amount *big.Int) error`: Trừ tổng số dư.
- `AddBalance(amount *big.Int)`: Thêm số dư.
- `GetOrCreateSmartContractState() types.SmartContractState`: Lấy hoặc tạo trạng thái smart contract.
- `SetCodeHash(hash common.Hash)`: Đặt hash mã cho smart contract.
- `SetStorageAddress(storageAddress common.Address)`: Đặt địa chỉ lưu trữ cho smart contract.
- `SetStorageRoot(hash common.Hash)`: Đặt root lưu trữ cho smart contract.
- `SetCreatorPublicKey(creatorPublicKey p_common.PublicKey)`: Đặt khóa công khai của người tạo.
- `AddLogHash(hash common.Hash)`: Thêm hash log.
- `SetPendingBalance(newBalance *big.Int)`: Đặt số dư đang chờ xử lý mới.

## Kết luận

File `account_state.go` cung cấp các phương thức cần thiết để quản lý trạng thái tài khoản trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi trạng thái tài khoản giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`smart_contract_state.go`](./state/smart_contract_state.go)

## Giới thiệu

File `smart_contract_state.go` định nghĩa cấu trúc `SmartContractState` và các phương thức liên quan để quản lý trạng thái của smart contract trong blockchain. Nó bao gồm thông tin về khóa công khai của người tạo, địa chỉ lưu trữ, mã hash, và các log liên quan.

## Cấu trúc `SmartContractState`

### Thuộc tính

- `createPublicKey`: Khóa công khai của người tạo thuộc kiểu `p_common.PublicKey`.
- `storageAddress`: Địa chỉ lưu trữ thuộc kiểu `common.Address`.
- `codeHash`: Hash của mã thuộc kiểu `common.Hash`.
- `storageRoot`: Hash của root lưu trữ thuộc kiểu `common.Hash`.
- `logsHash`: Hash của các log thuộc kiểu `common.Hash`.

### Hàm khởi tạo

- `NewSmartContractState(createPublicKey p_common.PublicKey, storageAddress common.Address, codeHash common.Hash, storageRoot common.Hash, logsHash common.Hash) types.SmartContractState`: Tạo một đối tượng `SmartContractState` mới với các thông tin được cung cấp.
- `NewEmptySmartContractState() types.SmartContractState`: Tạo một đối tượng `SmartContractState` trống.

### Các phương thức

- `Proto() *pb.SmartContractState`: Chuyển đổi `SmartContractState` thành đối tượng Protobuf.
- `Marshal() ([]byte, error)`: Chuyển đổi `SmartContractState` thành một slice byte để lưu trữ hoặc truyền tải.
- `FromProto(pbData *pb.SmartContractState)`: Khởi tạo `SmartContractState` từ một đối tượng Protobuf.
- `Unmarshal(b []byte) error`: Khởi tạo `SmartContractState` từ một slice byte đã được mã hóa.
- `String() string`: Trả về chuỗi mô tả của `SmartContractState`.
- `CreatorPublicKey() p_common.PublicKey`: Trả về khóa công khai của người tạo.
- `CreatorAddress() common.Address`: Trả về địa chỉ của người tạo.
- `StorageAddress() common.Address`: Trả về địa chỉ lưu trữ.
- `CodeHash() common.Hash`: Trả về hash của mã.
- `StorageRoot() common.Hash`: Trả về hash của root lưu trữ.
- `LogsHash() common.Hash`: Trả về hash của các log.
- `SetCreatorPublicKey(pk p_common.PublicKey)`: Đặt khóa công khai của người tạo.
- `SetStorageAddress(address common.Address)`: Đặt địa chỉ lưu trữ.
- `SetCodeHash(hash common.Hash)`: Đặt hash của mã.
- `SetStorageRoot(hash common.Hash)`: Đặt hash của root lưu trữ.
- `SetLogsHash(hash common.Hash)`: Đặt hash của các log.
- `Copy() types.SmartContractState`: Tạo một bản sao của `SmartContractState`.

## Kết luận

File `smart_contract_state.go` cung cấp các phương thức cần thiết để quản lý trạng thái của smart contract trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi trạng thái smart contract giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`update_state_fields.go`](./state/update_state_fields.go)

## Giới thiệu

File `update_state_fields.go` định nghĩa cấu trúc `UpdateField` và `UpdateStateFields` cùng các phương thức liên quan để quản lý các trường cập nhật trạng thái trong blockchain. Nó bao gồm các phương thức để tạo, chuyển đổi, và quản lý các trường cập nhật.

## Cấu trúc `UpdateField`

### Thuộc tính

- `field`: Trường cập nhật thuộc kiểu `pb.UPDATE_STATE_FIELD`.
- `value`: Giá trị của trường cập nhật thuộc kiểu `[]byte`.

### Hàm khởi tạo

- `NewUpdateField(field pb.UPDATE_STATE_FIELD, value []byte) *UpdateField`: Tạo một đối tượng `UpdateField` mới với trường và giá trị được cung cấp.

### Các phương thức

- `Field() pb.UPDATE_STATE_FIELD`: Trả về trường cập nhật.
- `Value() []byte`: Trả về giá trị của trường cập nhật.

## Cấu trúc `UpdateStateFields`

### Thuộc tính

- `address`: Địa chỉ liên quan đến các trường cập nhật thuộc kiểu `e_common.Address`.
- `fields`: Danh sách các trường cập nhật thuộc kiểu `[]types.UpdateField`.

### Hàm khởi tạo

- `NewUpdateStateFields(address e_common.Address) types.UpdateStateFields`: Tạo một đối tượng `UpdateStateFields` mới với địa chỉ được cung cấp.

### Các phương thức

- `AddField(field pb.UPDATE_STATE_FIELD, value []byte)`: Thêm một trường cập nhật vào danh sách.
- `Address() e_common.Address`: Trả về địa chỉ liên quan đến các trường cập nhật.
- `Fields() []types.UpdateField`: Trả về danh sách các trường cập nhật.
- `Unmarshal(data []byte) error`: Khởi tạo `UpdateStateFields` từ một slice byte đã được mã hóa.
- `Marshal() ([]byte, error)`: Chuyển đổi `UpdateStateFields` thành một slice byte để lưu trữ hoặc truyền tải.
- `String() string`: Trả về chuỗi mô tả của `UpdateStateFields`.
- `Proto() protoreflect.ProtoMessage`: Chuyển đổi `UpdateStateFields` thành đối tượng Protobuf.
- `FromProto(pbData protoreflect.ProtoMessage)`: Khởi tạo `UpdateStateFields` từ một đối tượng Protobuf.

## Kết luận

File `update_state_fields.go` cung cấp các phương thức cần thiết để quản lý các trường cập nhật trạng thái trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi các trường cập nhật giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`stats.go`](./stats/stats.go)

## Giới thiệu

File `stats.go` định nghĩa cấu trúc `Stats` và các phương thức liên quan để thu thập và quản lý thông tin thống kê của hệ thống blockchain. Nó bao gồm các thông tin về bộ nhớ, số lượng goroutine, thời gian hoạt động, và trạng thái của mạng và cơ sở dữ liệu.

## Cấu trúc `Stats`

### Thuộc tính

- `PbStats`: Đối tượng Protobuf `pb.Stats` chứa thông tin thống kê.

### Hàm khởi tạo

- `GetStats(startTime time.Time, levelDbs []*storage.LevelDB, connectionManager network.ConnectionsManager) *Stats`: Tạo một đối tượng `Stats` mới với thông tin thống kê được thu thập từ thời gian bắt đầu, danh sách cơ sở dữ liệu LevelDB, và trình quản lý kết nối mạng.

### Các phương thức

- `String() string`: Trả về chuỗi mô tả của `Stats`, bao gồm thông tin về bộ nhớ, số lượng goroutine, thời gian hoạt động, và trạng thái của mạng và cơ sở dữ liệu.
- `Unmarshal(b []byte) error`: Khởi tạo `Stats` từ một slice byte đã được mã hóa.
- `Marshal() ([]byte, error)`: Chuyển đổi `Stats` thành một slice byte để lưu trữ hoặc truyền tải.

## Kết luận

File `stats.go` cung cấp các phương thức cần thiết để thu thập và quản lý thông tin thống kê của hệ thống blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi thông tin thống kê giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`badger_db.go`](./storage/badger_db.go)

## Giới thiệu

File `badger_db.go` định nghĩa cấu trúc `BadgerDB` và các phương thức liên quan để quản lý cơ sở dữ liệu sử dụng BadgerDB. BadgerDB là một cơ sở dữ liệu key-value hiệu suất cao, được sử dụng để lưu trữ và truy xuất dữ liệu trong blockchain.

## Cấu trúc `BadgerDB`

### Thuộc tính

- `db`: Đối tượng BadgerDB thuộc kiểu `*badger.DB`.
- `closed`: Biến boolean để kiểm tra trạng thái đóng/mở của cơ sở dữ liệu.
- `path`: Đường dẫn tới thư mục lưu trữ dữ liệu của BadgerDB.
- `mu`: Đối tượng khóa để đồng bộ hóa truy cập dữ liệu.

### Hàm khởi tạo

- `NewBadgerDB(path string) (*BadgerDB, error)`: Tạo một đối tượng `BadgerDB` mới với đường dẫn được cung cấp.

### Các phương thức

- `Get(key []byte) ([]byte, error)`: Lấy giá trị từ cơ sở dữ liệu với khóa được cung cấp.
- `Put(key, value []byte) error`: Lưu trữ giá trị với khóa được cung cấp vào cơ sở dữ liệu.
- `Has(key []byte) bool`: Kiểm tra sự tồn tại của khóa trong cơ sở dữ liệu.
- `Delete(key []byte) error`: Xóa giá trị với khóa được cung cấp khỏi cơ sở dữ liệu.
- `BatchPut(kvs [][2][]byte) error`: Lưu trữ nhiều cặp khóa-giá trị vào cơ sở dữ liệu.
- `Open() error`: Mở cơ sở dữ liệu nếu nó đang bị đóng.
- `Close() error`: Đóng cơ sở dữ liệu.
- `GetSnapShot() SnapShot`: Tạo một bản sao của cơ sở dữ liệu.
- `GetIterator() IIterator`: Trả về một iterator để duyệt qua các cặp khóa-giá trị trong cơ sở dữ liệu.
- `Release()`: Giải phóng tài nguyên của cơ sở dữ liệu.

## Kết luận

File `badger_db.go` cung cấp các phương thức cần thiết để quản lý cơ sở dữ liệu sử dụng BadgerDB. Nó hỗ trợ việc lưu trữ, truy xuất, và quản lý dữ liệu một cách hiệu quả, giúp dễ dàng lưu trữ và truy xuất thông tin trong blockchain.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`leveldb.go`](./storage/leveldb.go)

## Giới thiệu

File `leveldb.go` định nghĩa cấu trúc `LevelDB` và các phương thức liên quan để quản lý cơ sở dữ liệu sử dụng LevelDB. LevelDB là một cơ sở dữ liệu key-value hiệu suất cao, được sử dụng để lưu trữ và truy xuất dữ liệu trong blockchain.

## Cấu trúc `LevelDB`

### Thuộc tính

- `db`: Đối tượng LevelDB thuộc kiểu `*leveldb.DB`.
- `closed`: Biến boolean để kiểm tra trạng thái đóng/mở của cơ sở dữ liệu.
- `path`: Đường dẫn tới thư mục lưu trữ dữ liệu của LevelDB.
- `closeChan`: Kênh để quản lý việc đóng cơ sở dữ liệu.

### Hàm khởi tạo

- `NewLevelDB(path string) (*LevelDB, error)`: Tạo một đối tượng `LevelDB` mới với đường dẫn được cung cấp.

### Các phương thức

- `Get(key []byte) ([]byte, error)`: Lấy giá trị từ cơ sở dữ liệu với khóa được cung cấp.
- `Put(key, value []byte) error`: Lưu trữ giá trị với khóa được cung cấp vào cơ sở dữ liệu.
- `Has(key []byte) bool`: Kiểm tra sự tồn tại của khóa trong cơ sở dữ liệu.
- `Delete(key []byte) error`: Xóa giá trị với khóa được cung cấp khỏi cơ sở dữ liệu.
- `BatchPut(kvs [][2][]byte) error`: Lưu trữ nhiều cặp khóa-giá trị vào cơ sở dữ liệu.
- `Open() error`: Mở cơ sở dữ liệu nếu nó đang bị đóng.
- `Close() error`: Đóng cơ sở dữ liệu.
- `Compact() error`: Thực hiện nén dữ liệu trong cơ sở dữ liệu.
- `GetSnapShot() SnapShot`: Tạo một bản sao của cơ sở dữ liệu.
- `GetIterator() IIterator`: Trả về một iterator để duyệt qua các cặp khóa-giá trị trong cơ sở dữ liệu.

## Kết luận

File `leveldb.go` cung cấp các phương thức cần thiết để quản lý cơ sở dữ liệu sử dụng LevelDB. Nó hỗ trợ việc lưu trữ, truy xuất, và quản lý dữ liệu một cách hiệu quả, giúp dễ dàng lưu trữ và truy xuất thông tin trong blockchain.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`memorydb.go`](./storage/memorydb.go)

## Giới thiệu

File `memorydb.go` định nghĩa cấu trúc `MemoryDB` và các phương thức liên quan để quản lý cơ sở dữ liệu trong bộ nhớ. `MemoryDB` là một cơ sở dữ liệu key-value đơn giản, được sử dụng để lưu trữ và truy xuất dữ liệu trong bộ nhớ.

## Cấu trúc `MemoryDB`

### Thuộc tính

- `db`: Map lưu trữ dữ liệu với khóa là mảng byte 32 phần tử và giá trị là slice byte.
- `RWMutex`: Đối tượng khóa để đồng bộ hóa truy cập dữ liệu.

### Hàm khởi tạo

- `NewMemoryDb() *MemoryDB`: Tạo một đối tượng `MemoryDB` mới.

### Các phương thức

- `Get(key []byte) ([]byte, error)`: Lấy giá trị từ cơ sở dữ liệu với khóa được cung cấp.
- `Put(key, value []byte) error`: Lưu trữ giá trị với khóa được cung cấp vào cơ sở dữ liệu.
- `Has(key []byte) bool`: Kiểm tra sự tồn tại của khóa trong cơ sở dữ liệu.
- `Delete(key []byte) error`: Xóa giá trị với khóa được cung cấp khỏi cơ sở dữ liệu.
- `BatchPut(kvs [][2][]byte) error`: Lưu trữ nhiều cặp khóa-giá trị vào cơ sở dữ liệu.
- `Close() error`: Đóng cơ sở dữ liệu (hiện tại đang không thực hiện gì trong `MemoryDB`).
- `Open() error`: Mở cơ sở dữ liệu (hiện tại đang không thực hiện gì trong `MemoryDB`).
- `Compact() error`: Thực hiện nén dữ liệu trong cơ sở dữ liệu (hiện tại đang không thực hiện gì trong `MemoryDB`).
- `Size() int`: Trả về kích thước của cơ sở dữ liệu.
- `GetSnapShot() SnapShot`: Tạo một bản sao của cơ sở dữ liệu.
- `GetIterator() IIterator`: Trả về một iterator để duyệt qua các cặp khóa-giá trị trong cơ sở dữ liệu.
- `Release()`: Giải phóng tài nguyên của cơ sở dữ liệu (hiện tại đang không thực hiện gì trong `MemoryDB`).

## Kết luận

File `memorydb.go` cung cấp các phương thức cần thiết để quản lý cơ sở dữ liệu trong bộ nhớ. Nó hỗ trợ việc lưu trữ, truy xuất, và quản lý dữ liệu một cách đơn giản, giúp dễ dàng lưu trữ và truy xuất thông tin trong bộ nhớ.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`node_sync_data.go`](./sync/node_sync_data.go)

## Giới thiệu

File `node_sync_data.go` định nghĩa các cấu trúc và phương thức liên quan đến việc đồng bộ dữ liệu node trong blockchain. Nó bao gồm các cấu trúc `GetNodeSyncData` và `NodeSyncData`, cùng với các phương thức để chuyển đổi giữa các định dạng dữ liệu khác nhau và quản lý thông tin đồng bộ.

## Cấu trúc `GetNodeSyncData`

### Thuộc tính

- `latestCheckPointBlockNumber`: Số block checkpoint mới nhất thuộc kiểu `uint64`.
- `validatorAddress`: Địa chỉ của validator thuộc kiểu `common.Address`.
- `nodeStatesIndex`: Chỉ số trạng thái node thuộc kiểu `int`.

### Hàm khởi tạo

- `NewGetNodeSyncData(latestCheckPointBlockNumber uint64, validatorAddress common.Address, nodeStatesIndex int) *GetNodeSyncData`: Tạo một đối tượng `GetNodeSyncData` mới với các thông tin được cung cấp.

### Các phương thức

- `GetNodeSyncDataFromProto(pbData *pb.GetNodeSyncData) *GetNodeSyncData`: Khởi tạo `GetNodeSyncData` từ một đối tượng Protobuf.
- `GetNodeSyncDataToProto(data *GetNodeSyncData) *pb.GetNodeSyncData`: Chuyển đổi `GetNodeSyncData` thành đối tượng Protobuf.
- `Unmarshal(b []byte) error`: Khởi tạo `GetNodeSyncData` từ một slice byte đã được mã hóa.
- `LatestCheckPointBlockNumber() uint64`: Trả về số block checkpoint mới nhất.
- `ValidatorAddress() common.Address`: Trả về địa chỉ của validator.
- `NodeStatesIndex() int`: Trả về chỉ số trạng thái node.

## Cấu trúc `NodeSyncData`

### Thuộc tính

- `validatorAddress`: Địa chỉ của validator thuộc kiểu `common.Address`.
- `nodeStatesIndex`: Chỉ số trạng thái node thuộc kiểu `int`.
- `accountStateRoot`: Hash của root trạng thái tài khoản thuộc kiểu `common.Hash`.
- `data`: Dữ liệu lưu trữ thuộc kiểu `[][2][]byte`.
- `finished`: Trạng thái hoàn thành thuộc kiểu `bool`.

### Hàm khởi tạo

- `NewNodeSyncData(validatorAddress common.Address, nodeStatesIndex int, accountStateRoot common.Hash, data [][2][]byte, finished bool) *NodeSyncData`: Tạo một đối tượng `NodeSyncData` mới với các thông tin được cung cấp.

### Các phương thức

- `NodeSyncDataFromProto(pbData *pb.NodeSyncData) *NodeSyncData`: Khởi tạo `NodeSyncData` từ một đối tượng Protobuf.
- `NodeSyncDataToProto(data *NodeSyncData) *pb.NodeSyncData`: Chuyển đổi `NodeSyncData` thành đối tượng Protobuf.
- `Unmarshal(b []byte) error`: Khởi tạo `NodeSyncData` từ một slice byte đã được mã hóa.
- `ValidatorAddress() common.Address`: Trả về địa chỉ của validator.
- `NodeStatesIndex() int`: Trả về chỉ số trạng thái node.
- `Finished() bool`: Trả về trạng thái hoàn thành.
- `AccountStateRoot() common.Hash`: Trả về hash của root trạng thái tài khoản.
- `Data() [][2][]byte`: Trả về dữ liệu lưu trữ.

## Kết luận

File `node_sync_data.go` cung cấp các phương thức cần thiết để quản lý và đồng bộ dữ liệu node trong blockchain. Nó hỗ trợ việc tạo, cập nhật, và chuyển đổi dữ liệu đồng bộ giữa các định dạng dữ liệu khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`call_data.go`](./transaction/call_data.go)

## Giới thiệu

File `call_data.go` định nghĩa cấu trúc `CallData` và các phương thức liên quan để quản lý dữ liệu cuộc gọi trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý dữ liệu cuộc gọi cho các giao dịch.

## Cấu trúc `CallData`

### Thuộc tính

- `method`: Phương thức được gọi thuộc kiểu `string`.
- `params`: Tham số của cuộc gọi thuộc kiểu `[]byte`.

### Hàm khởi tạo

- `NewCallData(method string, params []byte) *CallData`: Tạo một đối tượng `CallData` mới với phương thức và tham số được cung cấp.

### Các phương thức

- `Method() string`: Trả về phương thức của cuộc gọi.
- `Params() []byte`: Trả về tham số của cuộc gọi.

## Kết luận

File `call_data.go` cung cấp các phương thức cần thiết để quản lý dữ liệu cuộc gọi trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý dữ liệu cuộc gọi, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`deploy_data.go`](./transaction/deploy_data.go)

## Giới thiệu

File `deploy_data.go` định nghĩa cấu trúc `DeployData` và các phương thức liên quan để quản lý dữ liệu triển khai smart contract trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý dữ liệu triển khai.

## Cấu trúc `DeployData`

### Thuộc tính

- `code`: Mã của smart contract thuộc kiểu `[]byte`.
- `initParams`: Tham số khởi tạo thuộc kiểu `[]byte`.

### Hàm khởi tạo

- `NewDeployData(code []byte, initParams []byte) *DeployData`: Tạo một đối tượng `DeployData` mới với mã và tham số khởi tạo được cung cấp.

### Các phương thức

- `Code() []byte`: Trả về mã của smart contract.
- `InitParams() []byte`: Trả về tham số khởi tạo.

## Kết luận

File `deploy_data.go` cung cấp các phương thức cần thiết để quản lý dữ liệu triển khai smart contract trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý dữ liệu triển khai, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`from_node_transaction_result.go`](./transaction/from_node_transaction_result.go)

## Giới thiệu

File `from_node_transaction_result.go` định nghĩa cấu trúc `FromNodeTransactionResult` và các phương thức liên quan để quản lý kết quả giao dịch từ node trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý kết quả giao dịch.

## Cấu trúc `FromNodeTransactionResult`

### Thuộc tính

- `status`: Trạng thái của giao dịch thuộc kiểu `bool`.
- `output`: Kết quả đầu ra của giao dịch thuộc kiểu `[]byte`.

### Hàm khởi tạo

- `NewFromNodeTransactionResult(status bool, output []byte) *FromNodeTransactionResult`: Tạo một đối tượng `FromNodeTransactionResult` mới với trạng thái và kết quả đầu ra được cung cấp.

### Các phương thức

- `Status() bool`: Trả về trạng thái của giao dịch.
- `Output() []byte`: Trả về kết quả đầu ra của giao dịch.

## Kết luận

File `from_node_transaction_result.go` cung cấp các phương thức cần thiết để quản lý kết quả giao dịch từ node trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý kết quả giao dịch, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`to_node_transaction_result.go`](./transaction/to_node_transaction_result.go)

## Giới thiệu

File `to_node_transaction_result.go` định nghĩa cấu trúc `ToNodeTransactionResult` và các phương thức liên quan để quản lý kết quả giao dịch đến node trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý kết quả giao dịch.

## Cấu trúc `ToNodeTransactionResult`

### Thuộc tính

- `success`: Trạng thái thành công của giao dịch thuộc kiểu `bool`.
- `data`: Dữ liệu kết quả của giao dịch thuộc kiểu `[]byte`.

### Hàm khởi tạo

- `NewToNodeTransactionResult(success bool, data []byte) *ToNodeTransactionResult`: Tạo một đối tượng `ToNodeTransactionResult` mới với trạng thái thành công và dữ liệu kết quả được cung cấp.

### Các phương thức

- `Success() bool`: Trả về trạng thái thành công của giao dịch.
- `Data() []byte`: Trả về dữ liệu kết quả của giao dịch.

## Kết luận

File `to_node_transaction_result.go` cung cấp các phương thức cần thiết để quản lý kết quả giao dịch đến node trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý kết quả giao dịch, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`open_state_channel_data.go`](./transaction/open_state_channel_data.go)

## Giới thiệu

File `open_state_channel_data.go` định nghĩa cấu trúc `OpenStateChannelData` và các phương thức liên quan để quản lý dữ liệu mở kênh trạng thái trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý dữ liệu mở kênh trạng thái.

## Cấu trúc `OpenStateChannelData`

### Thuộc tính

- `channelId`: ID của kênh trạng thái thuộc kiểu `string`.
- `participants`: Danh sách các bên tham gia thuộc kiểu `[]common.Address`.

### Hàm khởi tạo

- `NewOpenStateChannelData(channelId string, participants []common.Address) *OpenStateChannelData`: Tạo một đối tượng `OpenStateChannelData` mới với ID kênh và danh sách các bên tham gia được cung cấp.

### Các phương thức

- `ChannelId() string`: Trả về ID của kênh trạng thái.
- `Participants() []common.Address`: Trả về danh sách các bên tham gia.

## Kết luận

File `open_state_channel_data.go` cung cấp các phương thức cần thiết để quản lý dữ liệu mở kênh trạng thái trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý dữ liệu mở kênh trạng thái, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`execute_sc_transactions.go`](./transaction/execute_sc_transactions.go)

## Giới thiệu

File `execute_sc_transactions.go` định nghĩa cấu trúc `ExecuteSCTransactions` và các phương thức liên quan để quản lý việc thực thi các giao dịch smart contract trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý việc thực thi giao dịch smart contract.

## Cấu trúc `ExecuteSCTransactions`

### Thuộc tính

- `transactions`: Danh sách các giao dịch smart contract thuộc kiểu `[]types.Transaction`.
- `results`: Kết quả thực thi của các giao dịch thuộc kiểu `[]types.ExecuteSCResult`.

### Hàm khởi tạo

- `NewExecuteSCTransactions(transactions []types.Transaction, results []types.ExecuteSCResult) *ExecuteSCTransactions`: Tạo một đối tượng `ExecuteSCTransactions` mới với danh sách giao dịch và kết quả thực thi được cung cấp.

### Các phương thức

- `Transactions() []types.Transaction`: Trả về danh sách các giao dịch smart contract.
- `Results() []types.ExecuteSCResult`: Trả về kết quả thực thi của các giao dịch.

## Kết luận

File `execute_sc_transactions.go` cung cấp các phương thức cần thiết để quản lý việc thực thi các giao dịch smart contract trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý việc thực thi giao dịch smart contract, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`transactions_from_leader.go`](./transaction/transactions_from_leader.go)

## Giới thiệu

File `transactions_from_leader.go` định nghĩa cấu trúc `TransactionsFromLeader` và các phương thức liên quan để quản lý các giao dịch được gửi từ leader trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, mã hóa, giải mã, và xác thực chữ ký của các giao dịch từ leader.

## Cấu trúc `TransactionsFromLeader`

### Thuộc tính

- `transactions`: Danh sách các giao dịch thuộc kiểu `types.Transaction`.
- `blockNumber`: Số thứ tự của block, thuộc kiểu `uint64`.
- `aggSign`: Chữ ký tổng hợp của các giao dịch, thuộc kiểu `[]byte`.
- `timeStamp`: Dấu thời gian của các giao dịch, thuộc kiểu `uint64`.

### Hàm khởi tạo

- `NewTransactionsFromLeader(transactions []types.Transaction, blockNumber uint64, aggSign []byte, timeStamp uint64) *TransactionsFromLeader`: Tạo một đối tượng `TransactionsFromLeader` mới với danh sách giao dịch, số thứ tự block, chữ ký tổng hợp và dấu thời gian được cung cấp.

### Các phương thức

- `Transactions() []types.Transaction`: Trả về danh sách các giao dịch.
- `BlockNumber() uint64`: Trả về số thứ tự của block.
- `AggSign() []byte`: Trả về chữ ký tổng hợp của các giao dịch.
- `TimeStamp() uint64`: Trả về dấu thời gian của các giao dịch.
- `Marshal() ([]byte, error)`: Mã hóa `TransactionsFromLeader` thành một slice byte.
- `Unmarshal(b []byte) error`: Giải mã `TransactionsFromLeader` từ một slice byte.
- `IsValidSign() bool`: Kiểm tra tính hợp lệ của chữ ký tổng hợp.
- `Proto() *pb.TransactionsFromLeader`: Chuyển đổi `TransactionsFromLeader` thành đối tượng Protobuf.
- `FromProto(pbData *pb.TransactionsFromLeader)`: Khởi tạo `TransactionsFromLeader` từ một đối tượng Protobuf.

## Kết luận

File `transactions_from_leader.go` cung cấp các phương thức cần thiết để quản lý các giao dịch được gửi từ leader trong blockchain. Nó hỗ trợ việc mã hóa, giải mã, và xác thực chữ ký của các giao dịch, đồng thời cung cấp các công cụ để chuyển đổi giữa các định dạng dữ liệu khác nhau.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`verify_transaction_sign.go`](./transaction/verify_transaction_sign.go)

## Giới thiệu

File `verify_transaction_sign.go` định nghĩa các cấu trúc và phương thức liên quan để quản lý việc xác thực chữ ký của các giao dịch trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, mã hóa, giải mã, và xác thực chữ ký của các giao dịch.

## Cấu trúc

### Thuộc tính

- `transactionId`: ID của giao dịch cần xác thực.
- `publicKeys`: Danh sách các khóa công khai liên quan đến giao dịch.
- `signatures`: Danh sách các chữ ký của giao dịch.
- `hash`: Hash của giao dịch cần xác thực.

### Hàm khởi tạo

- `NewVerifyTransactionSign(transactionId string, publicKeys []cm.PublicKey, signatures []cm.Sign, hash common.Hash) *VerifyTransactionSign`: Tạo một đối tượng `VerifyTransactionSign` mới với các thông tin được cung cấp.

### Các phương thức

- `Verify() bool`: Xác thực chữ ký của giao dịch dựa trên các khóa công khai và hash được cung cấp. Trả về `true` nếu chữ ký hợp lệ, ngược lại trả về `false`.
- `Marshal() ([]byte, error)`: Mã hóa đối tượng `VerifyTransactionSign` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(data []byte) error`: Giải mã dữ liệu từ một slice byte và khởi tạo đối tượng `VerifyTransactionSign`.

## Kết luận

File `verify_transaction_sign.go` cung cấp các phương thức cần thiết để quản lý và xác thực chữ ký của các giao dịch trong blockchain. Nó hỗ trợ việc tạo, mã hóa, giải mã, và xác thực chữ ký, giúp đảm bảo tính toàn vẹn và an toàn của các giao dịch trong hệ thống.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`update_storage_host_data.go`](./transaction/update_storage_host_data.go)

## Giới thiệu

File `update_storage_host_data.go` định nghĩa cấu trúc `UpdateStorageHostData` và các phương thức liên quan để quản lý dữ liệu cập nhật thông tin lưu trữ của host trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, và quản lý dữ liệu cập nhật thông tin lưu trữ.

## Cấu trúc `UpdateStorageHostData`

### Thuộc tính

- `storageHost`: Tên của host lưu trữ, thuộc kiểu `string`.
- `storageAddress`: Địa chỉ lưu trữ, thuộc kiểu `e_common.Address`.

### Hàm khởi tạo

- `NewUpdateStorageHostData(storageHost string, storageAddress e_common.Address) types.UpdateStorageHostData`: Tạo một đối tượng `UpdateStorageHostData` mới với thông tin host và địa chỉ lưu trữ được cung cấp.

### Các phương thức

- `Unmarshal(b []byte) error`: Giải mã dữ liệu từ một slice byte và cập nhật đối tượng `UpdateStorageHostData`.
- `Marshal() ([]byte, error)`: Mã hóa đối tượng `UpdateStorageHostData` thành một slice byte để lưu trữ hoặc truyền tải.
- `Proto() protoreflect.ProtoMessage`: Chuyển đổi đối tượng `UpdateStorageHostData` thành một đối tượng Protobuf để dễ dàng xử lý và truyền tải.

## Kết luận

File `update_storage_host_data.go` cung cấp các phương thức cần thiết để quản lý dữ liệu cập nhật thông tin lưu trữ của host trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, và quản lý dữ liệu cập nhật, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`transaction.go`](./transaction/transaction.go)

## Giới thiệu

File `transaction.go` định nghĩa cấu trúc `Transaction` và các phương thức liên quan để quản lý các giao dịch trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, chuyển đổi, xác thực và quản lý dữ liệu giao dịch.

## Cấu trúc `Transaction`

### Thuộc tính

- `proto`: Đối tượng Protobuf của giao dịch, thuộc kiểu `*pb.Transaction`.

### Hàm khởi tạo

- `NewTransaction(...) types.Transaction`: Tạo một đối tượng `Transaction` mới với các thông tin được cung cấp.

### Các phương thức

#### Chuyển đổi

- `TransactionsToProto(transactions []types.Transaction) []*pb.Transaction`: Chuyển đổi danh sách giao dịch thành danh sách Protobuf.
- `TransactionFromProto(txPb *pb.Transaction) types.Transaction`: Tạo một đối tượng `Transaction` từ Protobuf.
- `TransactionsFromProto(pbTxs []*pb.Transaction) []types.Transaction`: Chuyển đổi danh sách Protobuf thành danh sách giao dịch.

#### Tổng quát

- `Unmarshal(b []byte) error`: Giải mã một giao dịch từ slice byte.
- `Marshal() ([]byte, error)`: Mã hóa giao dịch thành slice byte.
- `Proto() protoreflect.ProtoMessage`: Trả về đối tượng Protobuf của giao dịch.
- `FromProto(pbMessage protoreflect.ProtoMessage)`: Khởi tạo giao dịch từ một đối tượng Protobuf.
- `String() string`: Trả về chuỗi biểu diễn của giao dịch.

#### Xác thực

- `ValidSign() bool`: Kiểm tra tính hợp lệ của chữ ký giao dịch.
- `ValidTransactionHash() bool`: Kiểm tra tính hợp lệ của hash giao dịch.
- `ValidLastHash(fromAccountState types.AccountState) bool`: Kiểm tra tính hợp lệ của hash cuối cùng.
- `ValidDeviceKey(fromAccountState types.AccountState) bool`: Kiểm tra tính hợp lệ của khóa thiết bị.
- `ValidMaxGas() bool`: Kiểm tra tính hợp lệ của gas tối đa.
- `ValidMaxGasPrice(currentGasPrice uint64) bool`: Kiểm tra tính hợp lệ của giá gas tối đa.
- `ValidAmount(fromAccountState types.AccountState, currentGasPrice uint64) bool`: Kiểm tra tính hợp lệ của số lượng giao dịch.
- `ValidPendingUse(fromAccountState types.AccountState) bool`: Kiểm tra tính hợp lệ của số lượng đang chờ sử dụng.
- `ValidDeploySmartContractToAccount(fromAccountState types.AccountState) bool`: Kiểm tra tính hợp lệ của địa chỉ triển khai smart contract.
- `ValidOpenChannelToAccount(fromAccountState types.AccountState) bool`: Kiểm tra tính hợp lệ của địa chỉ mở kênh.
- `ValidCallSmartContractToAccount(toAccountState types.AccountState) bool`: Kiểm tra tính hợp lệ của việc gọi smart contract.

#### Mã hóa và Giải mã

- `MarshalTransactions(txs []types.Transaction) ([]byte, error)`: Mã hóa danh sách giao dịch thành slice byte.
- `UnmarshalTransactions(b []byte) ([]types.Transaction, error)`: Giải mã danh sách giao dịch từ slice byte.
- `MarshalTransactionsWithBlockNumber(txs []types.Transaction, blockNumber uint64) ([]byte, error)`: Mã hóa danh sách giao dịch cùng với số block.
- `UnmarshalTransactionsWithBlockNumber(b []byte) ([]types.Transaction, uint64, error)`: Giải mã danh sách giao dịch cùng với số block từ slice byte.

## Kết luận

File `transaction.go` cung cấp các phương thức cần thiết để quản lý và thao tác với các giao dịch trong blockchain. Nó hỗ trợ việc tạo, chuyển đổi, xác thực và quản lý dữ liệu giao dịch, giúp dễ dàng lưu trữ và truyền tải thông tin trong hệ thống.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`transaction_grouper.go`](./transaction_grouper/transaction_grouper.go)

## Giới thiệu

File `transaction_grouper.go` định nghĩa cấu trúc `TransactionGrouper` và các phương thức liên quan để quản lý việc nhóm các giao dịch trong blockchain. Mục tiêu là tổ chức các giao dịch thành các nhóm dựa trên tiền tố của địa chỉ gửi và nhận để thực thi hiệu quả hơn.

## Cấu trúc `TransactionGrouper`

### Thuộc tính

- `groups`: Mảng chứa các nhóm giao dịch, mỗi nhóm là một slice của `types.Transaction`.
- `prefix`: Tiền tố được sử dụng để xác định nhóm của giao dịch.

### Hàm khởi tạo

- `NewTransactionGrouper(prefix []byte) *TransactionGrouper`: Tạo một đối tượng `TransactionGrouper` mới với mảng nhóm trống và tiền tố được cung cấp.

### Các phương thức

- `AddFromTransactions(transactions []types.Transaction)`: Thêm các giao dịch vào các nhóm dựa trên địa chỉ gửi.
- `AddFromTransaction(transaction types.Transaction)`: Thêm một giao dịch vào nhóm dựa trên địa chỉ gửi.
- `AddToTransactions(transactions []types.Transaction)`: Thêm các giao dịch vào các nhóm dựa trên địa chỉ nhận.
- `AddToTransaction(transaction types.Transaction)`: Thêm một giao dịch vào nhóm dựa trên địa chỉ nhận.
- `GetTransactionsGroups() [16][]types.Transaction`: Trả về mảng các nhóm giao dịch.
- `HaveTransactionGroupsCount() int`: Trả về số lượng nhóm có chứa giao dịch.
- `Clear()`: Xóa tất cả các nhóm và giao dịch.

## Kết luận

File `transaction_grouper.go` cung cấp các phương thức cần thiết để quản lý và nhóm các giao dịch trong blockchain. Việc nhóm các giao dịch giúp tối ưu hóa quá trình thực thi và quản lý các giao dịch liên quan đến cùng một tiền tố địa chỉ.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`transaction_pool.go`](./transaction_pool/transaction_pool.go)

## Giới thiệu

File `transaction_pool.go` định nghĩa cấu trúc `TransactionPool` và các phương thức liên quan để quản lý một tập hợp các giao dịch trong blockchain. Mục tiêu là lưu trữ và quản lý các giao dịch, đồng thời tạo chữ ký tổng hợp cho các giao dịch này.

## Cấu trúc `TransactionPool`

### Thuộc tính

- `transactions`: Danh sách các giao dịch thuộc kiểu `types.Transaction`.
- `aggSign`: Chữ ký tổng hợp thuộc kiểu `*blst.P2Aggregate`.
- `mutex`: Đối tượng khóa (`sync.Mutex`) để đảm bảo an toàn khi truy cập đồng thời vào `transactions` và `aggSign`.

### Hàm khởi tạo

- `NewTransactionPool() *TransactionPool`: Tạo một đối tượng `TransactionPool` mới với chữ ký tổng hợp mới.

### Các phương thức

- `AddTransaction(tx types.Transaction)`: Thêm một giao dịch vào `TransactionPool`. Sử dụng khóa để đảm bảo an toàn khi truy cập đồng thời.
- `AddTransactions(txs []types.Transaction)`: Thêm nhiều giao dịch vào `TransactionPool`. Sử dụng khóa để đảm bảo an toàn khi truy cập đồng thời.
- `TransactionsWithAggSign() ([]types.Transaction, []byte)`: Trả về danh sách các giao dịch và chữ ký tổng hợp, đồng thời xóa các giao dịch khỏi pool.
- `addTransaction(tx types.Transaction)`: Phương thức nội bộ để thêm một giao dịch vào `TransactionPool` và cập nhật chữ ký tổng hợp.

## Kết luận

File `transaction_pool.go` cung cấp các phương thức cần thiết để quản lý một tập hợp các giao dịch trong blockchain. Nó hỗ trợ việc thêm và lấy các giao dịch một cách an toàn trong môi trường đa luồng, đồng thời tạo chữ ký tổng hợp cho các giao dịch này.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`committer.go`](./trie/committer.go)

## Giới thiệu

File `committer.go` định nghĩa cấu trúc `committer` và các phương thức liên quan để quản lý quá trình commit các node trong cây Merkle-Patricia. Mục tiêu là thu thập và lưu trữ các node đã thay đổi trong quá trình commit.

## Cấu trúc `committer`

### Thuộc tính

- `nodes`: Tập hợp các node đã thay đổi, thuộc kiểu `*node.NodeSet`.
- `tracer`: Công cụ theo dõi các thay đổi trong cây, thuộc kiểu `*Tracer`.
- `collectLeaf`: Cờ để xác định có thu thập các node lá hay không.

### Hàm khởi tạo

- `newCommitter(nodeset *node.NodeSet, tracer *Tracer, collectLeaf bool) *committer`: Tạo một đối tượng `committer` mới với các thông tin được cung cấp.

### Các phương thức

- `Commit(n node.Node) node.HashNode`: Commit một node và trả về node dạng hash.
- `commit(path []byte, n node.Node) node.Node`: Commit một node và trả về node đã được xử lý.
- `commitChildren(path []byte, n *node.FullNode) [17]node.Node`: Commit các node con của một `FullNode`.
- `store(path []byte, n node.Node) node.Node`: Lưu trữ node và thêm vào tập hợp các node đã thay đổi.

## Kết luận

File `committer.go` cung cấp các phương thức cần thiết để quản lý và commit các node trong cây Merkle-Patricia. Nó hỗ trợ việc thu thập và lưu trữ các node đã thay đổi, giúp dễ dàng quản lý và xử lý các thay đổi trong cây.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`hasher.go`](./trie/hasher.go)

## Giới thiệu

File `hasher.go` định nghĩa các phương thức để tính toán hash cho các node trong cây Merkle-Patricia. Mục tiêu là cung cấp các phương thức để tính toán và trả về hash của các node.

### Các phương thức

- `proofHash(original node.Node) (collapsed, hashed node.Node)`: Tính toán và trả về hash của một node, đồng thời trả về node đã được xử lý.

## Kết luận

File `hasher.go` cung cấp các phương thức cần thiết để tính toán hash cho các node trong cây Merkle-Patricia. Nó hỗ trợ việc tính toán và trả về hash của các node, giúp dễ dàng quản lý và xử lý các node trong cây.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`trie_reader.go`](./trie/trie_reader.go)

## Giới thiệu

File `trie_reader.go` định nghĩa cấu trúc `TrieReader` và các phương thức liên quan để đọc dữ liệu từ cây Merkle-Patricia. Mục tiêu là cung cấp các phương thức để truy xuất và đọc dữ liệu từ cây.

## Cấu trúc `TrieReader`

### Thuộc tính

- `db`: Cơ sở dữ liệu lưu trữ các node của cây, thuộc kiểu `trie_db.DB`.

### Hàm khởi tạo

- `newTrieReader(db trie_db.DB) (*TrieReader, error)`: Tạo một đối tượng `TrieReader` mới với cơ sở dữ liệu được cung cấp.

### Các phương thức

- `node(path []byte, hash e_common.Hash) ([]byte, error)`: Truy xuất và trả về node được mã hóa RLP từ cơ sở dữ liệu dựa trên thông tin node được cung cấp.

## Kết luận

File `trie_reader.go` cung cấp các phương thức cần thiết để đọc dữ liệu từ cây Merkle-Patricia. Nó hỗ trợ việc truy xuất và đọc dữ liệu từ cây, giúp dễ dàng quản lý và xử lý dữ liệu trong cây.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`trie.go`](./trie/trie.go)

## Giới thiệu

File `trie.go` định nghĩa cấu trúc `MerklePatriciaTrie` và các phương thức liên quan để quản lý cây Merkle-Patricia trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, truy xuất, cập nhật dữ liệu trong cây trie, cũng như tính toán hash của cây.

## Cấu trúc `MerklePatriciaTrie`

### Thuộc tính

- `root`: Node gốc của cây, thuộc kiểu `node.Node`.
- `committed`: Trạng thái cam kết của cây, thuộc kiểu `bool`.
- `reader`: Đối tượng `TrieReader` để đọc dữ liệu từ cơ sở dữ liệu.
- `tracer`: Công cụ theo dõi các thay đổi trong cây, thuộc kiểu `*Tracer`.

### Hàm khởi tạo

- `New(root e_common.Hash, db trie_db.DB) (*MerklePatriciaTrie, error)`: Tạo một đối tượng `MerklePatriciaTrie` mới với hash gốc và cơ sở dữ liệu được cung cấp.

### Các phương thức

- `Copy() *MerklePatriciaTrie`: Tạo một bản sao của cây trie hiện tại.
- `NodeIterator(start []byte) (NodeIterator, error)`: Trả về một iterator cho các node trong cây, bắt đầu từ vị trí được chỉ định.
- `Get(key []byte) ([]byte, error)`: Truy xuất giá trị tương ứng với khóa được cung cấp từ cây trie.
- `get(origNode node.Node, key []byte, pos int) (value []byte, newnode node.Node, didResolve bool, err error)`: Phương thức nội bộ để truy xuất giá trị từ cây trie.
- `insert(n node.Node, prefix, key []byte, value node.Node) (bool, node.Node, error)`: Chèn một node mới vào cây trie.
- `update(key, value []byte) error`: Cập nhật giá trị của một khóa trong cây trie.
- `delete(n node.Node, prefix, key []byte) (bool, node.Node, error)`: Xóa một node khỏi cây trie.
- `resolveAndTrack(n node.HashNode, prefix []byte) (node.Node, error)`: Tải node từ cơ sở dữ liệu và theo dõi node đã tải.
- `hashRoot() (node.Node, node.Node)`: Tính toán hash gốc của cây trie.
- `GetStorageKeys() []e_common.Hash`: Lấy danh sách các khóa lưu trữ từ cây trie.
- `String() string`: Trả về chuỗi biểu diễn của cây trie.
- `GetRootHash(data map[string][]byte) (e_common.Hash, error)`: Tính toán và trả về hash gốc của cây trie dựa trên dữ liệu được cung cấp.

## Kết luận

File `trie.go` cung cấp các phương thức cần thiết để quản lý và thao tác với cây Merkle-Patricia trong blockchain. Nó hỗ trợ việc tạo, truy xuất, cập nhật dữ liệu trong cây, cũng như tính toán hash của cây, giúp dễ dàng quản lý và xử lý dữ liệu trong hệ thống.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`block_vote.go`](./vote/block_vote.go)

## Giới thiệu

File `block_vote.go` định nghĩa cấu trúc `BlockVote` và các phương thức liên quan để quản lý việc vote cho các block trong blockchain. Mục tiêu là cung cấp các phương thức để tạo, xác thực và chuyển đổi dữ liệu vote cho các block.

## Cấu trúc `BlockVote`

### Thuộc tính

- `blockHash`: Hash của block thuộc kiểu `common.Hash`.
- `number`: Số thứ tự của block thuộc kiểu `uint64`.
- `publicKey`: Khóa công khai của người vote thuộc kiểu `cm.PublicKey`.
- `sign`: Chữ ký của người vote thuộc kiểu `cm.Sign`.

### Hàm khởi tạo

- `NewBlockVote(blockHash common.Hash, number uint64, publicKey cm.PublicKey, sign cm.Sign) types.BlockVote`: Tạo một đối tượng `BlockVote` mới với các thông tin được cung cấp.

### Các phương thức

- `BlockHash() common.Hash`: Trả về hash của block.
- `Number() uint64`: Trả về số thứ tự của block.
- `PublicKey() cm.PublicKey`: Trả về khóa công khai của người vote.
- `Address() common.Address`: Trả về địa chỉ của người vote, được tạo từ khóa công khai.
- `Sign() cm.Sign`: Trả về chữ ký của người vote.
- `Valid() bool`: Xác thực chữ ký của người vote dựa trên khóa công khai và hash của block.
- `Marshal() ([]byte, error)`: Chuyển đổi `BlockVote` thành một slice byte để lưu trữ hoặc truyền tải.
- `Unmarshal(bData []byte) error`: Khởi tạo `BlockVote` từ một slice byte đã được mã hóa.
- `Proto() *pb.BlockVote`: Chuyển đổi `BlockVote` thành đối tượng Protobuf.
- `FromProto(v *pb.BlockVote)`: Khởi tạo `BlockVote` từ một đối tượng Protobuf.

## Kết luận

File `block_vote.go` cung cấp các phương thức cần thiết để quản lý và xác thực việc vote cho các block trong blockchain. Nó hỗ trợ việc tạo, xác thực, và chuyển đổi dữ liệu vote giữa các định dạng khác nhau, giúp dễ dàng lưu trữ và truyền tải thông tin.

- [Trở về Mục lục](#mục-lục)

# Tài liệu cho [`vote_pool.go`](./vote_pool/vote_pool.go)

## Giới thiệu

File `vote_pool.go` định nghĩa cấu trúc `VotePool` và các phương thức liên quan để quản lý việc bỏ phiếu trong blockchain. Mục tiêu là theo dõi và xác thực các phiếu vote từ các địa chỉ khác nhau, đồng thời xác định kết quả dựa trên tỷ lệ chấp thuận.

## Cấu trúc `VotePool`

### Thuộc tính

- `approveRate`: Tỷ lệ chấp thuận cần thiết để một phiếu vote được coi là hợp lệ, thuộc kiểu `float64`.
- `addresses`: Map lưu trữ các địa chỉ tham gia bỏ phiếu.
- `votes`: Map lưu trữ các phiếu vote, với hash của phiếu vote là key và map các khóa công khai và chữ ký là value.
- `mapAddressVote`: Map lưu trữ hash của phiếu vote cho từng địa chỉ.
- `mapVoteAddresses`: Map lưu trữ danh sách địa chỉ đã bỏ phiếu cho mỗi hash phiếu vote.
- `voteValues`: Map lưu trữ giá trị của phiếu vote cho mỗi hash phiếu vote.
- `result`: Kết quả của phiếu vote, thuộc kiểu `*common.Hash`.
- `closed`: Trạng thái đóng của pool, thuộc kiểu `bool`.
- `voteMu`: Đối tượng khóa (`sync.RWMutex`) để đảm bảo an toàn khi truy cập đồng thời vào dữ liệu phiếu vote.

### Hàm khởi tạo

- `NewVotePool(approveRate float64, addresses map[common.Address]interface{}) *VotePool`: Tạo một đối tượng `VotePool` mới với tỷ lệ chấp thuận và danh sách địa chỉ được cung cấp.

### Các phương thức

- `AddVote(vote types.Vote) error`: Thêm một phiếu vote vào `VotePool`. Xác thực chữ ký và kiểm tra xem địa chỉ đã bỏ phiếu chưa.
- `checkVote(voteHash common.Hash) bool`: Kiểm tra xem số lượng phiếu vote đã đạt tỷ lệ chấp thuận hay chưa.
- `Addresses() map[common.Address]interface{}`: Trả về danh sách các địa chỉ tham gia bỏ phiếu.
- `Result() *common.Hash`: Trả về kết quả của phiếu vote.
- `ResultValue() interface{}`: Trả về giá trị của kết quả phiếu vote.
- `RewardAddresses() []common.Address`: Trả về danh sách các địa chỉ đã bỏ phiếu cho kết quả.
- `PunishAddresses() []common.Address`: Trả về danh sách các địa chỉ đã bỏ phiếu cho các kết quả không thành công.

## Kết luận

File `vote_pool.go` cung cấp các phương thức cần thiết để quản lý và xác thực việc bỏ phiếu trong blockchain. Nó hỗ trợ việc theo dõi, xác thực, và tính toán kết quả phiếu vote dựa trên tỷ lệ chấp thuận, giúp dễ dàng quản lý và xử lý các phiếu vote trong hệ thống.

- [Trở về Mục lục](#mục-lục)thuận hay chưa.
- `Addresses() map[common.Address]interface{}`: Trả về danh sách các địa chỉ tham gia bỏ phiếu.
- `Result() *common.Hash`: Trả về kết quả của phiếu vote.
- `ResultValue() interface{}`: Trả về giá trị của kết quả phiếu vote.
- `RewardAddresses() []common.Address`: Trả về danh sách các địa chỉ đã bỏ phiếu cho kết quả.
- `PunishAddresses() []common.Address`: Trả về danh sách các địa chỉ đã bỏ phiếu cho các kết quả không thành công.

## Kết luận

File `vote_pool.go` cung cấp các phương thức cần thiết để quản lý và xác thực việc bỏ phiếu trong blockchain. Nó hỗ trợ việc theo dõi, xác thực, và tính toán kết quả phiếu vote dựa trên tỷ lệ chấp thuận, giúp dễ dàng quản lý và xử lý các phiếu vote trong hệ thống.

- [Trở về Mục lục](#mục-lục)