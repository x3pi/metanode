# Tham số giao thức (protocol parameters) và cách nâng cấp không ảnh hưởng dữ liệu — rà soát và đề xuất, 2026-10-22

Trạng thái: **P0 và P1 đã triển khai (2026-10-22); P2 và P3 vẫn là đề xuất.** P0 và P1 không đổi bất kỳ luật thực thi nào: chúng chỉ đo, báo cáo, và từ chối khởi động khi cấu hình lệch với genesis đã khai báo. P2/P3 là thay đổi consensus nên cần bạn duyệt và phải triển khai đồng loạt (xem mục 6).

Nhãn bằng chứng: `[ĐỌC CODE]` kèm vị trí; `[ĐO]` có lệnh chạy; `[CHƯA ĐO]`; `[GIẢ THUYẾT]`. Phần Sui lấy từ mã nguồn `MystenLabs/sui` nhánh `main` đọc qua công cụ tóm tắt trang, nên khi cần dùng làm căn cứ chính thức hãy mở lại file gốc.

---

## 0. Đã triển khai: P0 và P1

### 0.1. Có gì
| Thành phần | Vị trí |
|---|---|
| Gói lõi: chuẩn hóa các tham số, dấu vân tay từng trường và tổng, kiểm tra với genesis | `execution/pkg/protocolparams/` |
| Mục `protocol` tùy chọn trong genesis (`config.protocol`), các trường con đều tùy chọn: không khai báo thì không kiểm | `execution/pkg/config/genesis.go` (`ProtocolSection`) |
| Kiểm tra khi khởi động: genesis có mục `protocol` mà `config.json` lệch thì **node không khởi động**, in ra từng trường lệch; không có mục `protocol` thì chỉ ghi cảnh báo kèm dấu vân tay | `execution/cmd/simple_chain/app_network.go` (`initNetwork`) |
| RPC chỉ đọc `mtn_protocolFingerprint` | `execution/cmd/simple_chain/mtn_api.go` |
| Công cụ so sánh cả cụm, tính offline, và sinh đoạn `protocol` cho genesis | `execution/cmd/tool/protocol_fingerprint/` |
| Rust: metric `consensus_max_transaction_size_bytes`, `consensus_max_transactions_in_block_bytes`, `consensus_max_num_transactions_in_block` (giá trị hiệu lực của node) và cảnh báo `[PROTOCOL]` khi giới hạn bị ghi đè từ cấu hình riêng của node | `consensus/metanode/meta-consensus/core/src/{metrics.rs, authority_node/mod.rs}`, `consensus/metanode/src/node/setup_consensus/mod.rs` |
| Cảnh báo ở hướng dẫn chỉnh kích thước block | `HOW_TO_TUNE_BLOCK_SIZE.md` |

### 0.2. Khác với đề xuất ban đầu, và vì sao
- **P1 đặt nguồn chung ở genesis, chưa ở trạng thái on-chain.** Ghi một khóa vào trạng thái làm đổi state root, tức là một thay đổi consensus; P1 phải không đổi kết quả thực thi. Bản sao on-chain chuyển sang P2.
- **Không nên thêm mục `protocol` vào genesis của chain đã công bố `GenesisDigest`.** `GenesisDigest` là keccak256 của chính nội dung `genesis.json` (`pkg/cross_chain/types.go:84`, `ceremony/digest.go`); sửa file làm file không còn khớp digest đã công bố. Với chain mới, khai báo `protocol` **trước khi** công bố digest thì digest cam kết luôn cả tham số giao thức. Với chain đang chạy, dùng P0 (so sánh dấu vân tay) thay vì sửa genesis.
- **Không kiểm khi bắt tay peer:** các validator Go không kết nối trực tiếp với nhau (không có chỗ nào trong `cmd/simple_chain` dựng kết nối master/child), nên không có bước bắt tay để gắn kiểm tra. Việc so sánh làm qua RPC bằng công cụ.
- **Giới hạn block của Rust xuất qua metric, chưa nằm trong dấu vân tay Go.** So sánh chúng bằng Prometheus; công cụ chưa đọc metric.
- **`verify_device_key`, `state_backend`, `raft.max_batch_bytes`, `attestation_interval`, `epoch_duration_seconds`, `chain_id` chỉ vào dấu vân tay, không vào mục `protocol` của genesis** (chưa xác minh được đường thực thi hoặc đã có nguồn chung). Một giá trị Raft `max_batch_bytes` bỏ trống được chuẩn hóa thành mặc định 4 MiB, nên "bỏ trống" và "đặt tường minh bằng mặc định" cho cùng dấu vân tay.

### 0.3. Cách dùng
```bash
# so sánh cả cụm (danh sách nút theo định dạng /tmp/rpc_nodes.json hoặc {"tên":"url"}): exit 0 đồng nhất, 1 lệch, 2 có nút không hỏi được
protocol_fingerprint -nodes rpc_nodes.json

# trước khi triển khai: chạy kiểm tra khởi động trên file thật, không cần khởi động node
protocol_fingerprint -config config.json -genesis genesis.json

# chain MỚI: sinh mục protocol để dán vào config của genesis.json TRƯỚC khi công bố digest
protocol_fingerprint -config config.json -genesis genesis.json -snippet
```
Node cũ chưa có RPC này sẽ được báo là lỗi ("node too old"), không bao giờ bị tính là đồng nhất.

### 0.4. Bằng chứng
- `[ĐO]` Test đơn vị: `execution/pkg/protocolparams` (mỗi trường được dấu vân tay có một test đột biến riêng; test phản xạ buộc mọi trường mới phải vào danh sách; chuẩn hóa địa chỉ free-fee; mọi trường khai báo trong genesis bị bắt khi lệch; tương thích ngược khi đọc genesis cũ và mới) và `execution/cmd/tool/protocol_fingerprint` (so sánh, nút không hỏi được không bao giờ tính là đồng nhất, kết quả ổn định khi hòa, truy vấn HTTP thật). Tôi phá thử ba chỗ trong `protocolparams.go` (bỏ một trường khỏi danh sách, không chuẩn hóa free-fee, bỏ kiểm `account_gate`) và test bắt được cả ba.
- `[ĐO]` Chạy thật trên cụm Raft 3 node với binary mới (cổng 31xxx, tách khỏi cụm CI): (1) đồng nhất → công cụ báo cả 3 giống nhau, exit 0; (2) đổi `cross_chain.velocity_limit_disabled` trên đúng một node rồi khởi động lại nó → công cụ exit 1 và nêu đúng node `n1` cùng trường lệch; (3) genesis có mục `protocol` khớp → node khởi động và ghi `config.json matches the genesis protocol section`; (4) cùng genesis nhưng cấu hình node lệch → node thoát với mã 1 và thông báo nêu `cross_chain.velocity_limit_disabled: config.json has true, genesis protocol section declares false`. Dấu vân tay tính offline bằng `-config ... -genesis ...` khớp từng ký tự với giá trị node đang chạy báo.
- `[CHƯA ĐO]` Chưa chạy trên cụm nhiều máy 231/230; chưa chạy toàn bộ CI cục bộ trên binary có thay đổi này; chưa đo tác động hiệu năng (kiểm tra chỉ chạy một lần lúc khởi động).

## 1. Hiện trạng: luật giao thức đang đọc từ cấu hình riêng từng node

### 1.1. Chain chưa có cơ chế kích hoạt theo phiên bản
- `[ĐỌC CODE]` Ba comment trong Go nói thẳng "this chain has no hardfork-activation mechanism": `execution/pkg/common/constant.go:45`, `execution/pkg/blockchain/vm_processor/intrinsic_gas.go:19`, `execution/pkg/block/blob_gas.go:21`. Mọi luật EVM luôn bật; không có lịch kích hoạt.
- `[ĐỌC CODE]` Runbook hiện hành (`note/runbook_deploy_audit_rounds_and_stm_fix_20261019.md`, mục 1.1) kết luận: node chạy code cũ và code mới ra kết quả khác nhau cho cùng một block, nên **không nâng cấp cuốn chiếu**; phải dừng cả cụm, giữ dữ liệu, triển khai đồng loạt.

### 1.2. Rust: phiên bản giao thức luôn là phiên bản cao nhất của binary
- `[ĐỌC CODE]` `consensus/metanode/src/node/setup_consensus/mod.rs:313` gọi `ProtocolConfig::get_for_max_version_UNSAFE()`. Crate `meta-protocol-config` (bản fork của Sui) có `MAX_PROTOCOL_VERSION = 105` (`crates/meta-protocol-config/src/lib.rs:129`). Hệ quả: thay binary là tự động chuyển sang hành vi của phiên bản mới nhất; không có phiên bản nào được mạng đồng thuận.
- `[ĐỌC CODE]` Các tham số riêng của MetaNode được gán cứng **sau** bảng phiên bản, cho mọi phiên bản: `consensus_max_transactions_in_block_bytes = 2 MB`, `consensus_max_num_transactions_in_block = 4000` (`lib.rs` ~4470 đến 4473, comment "MetaNode performance overrides for >30K TPS target"). Đổi chúng cần sửa mã và build lại binary (đúng như `HOW_TO_TUNE_BLOCK_SIZE.md` hướng dẫn).
- `[ĐỌC CODE]` Ngoài ra, số giao dịch tối đa mỗi block còn bị ghi đè từ cấu hình **riêng từng node**: `consensus/metanode/src/config.rs:88` (`consensus_max_num_transactions_in_block`) áp dụng qua `set_consensus_max_num_transactions_in_block_for_testing` tại `setup_consensus/mod.rs:314-316`. Tên hàm là `_for_testing`.
- `[ĐỌC CODE]` Khi nhận block, mỗi validator kiểm giới hạn bằng `protocol_config` **của chính nó** (`consensus/metanode/meta-consensus/core/src/block_verifier.rs:272-294`: kích thước giao dịch, số giao dịch, tổng byte). Hai validator có giới hạn khác nhau sẽ cho kết quả kiểm khác nhau cho cùng một block. Sui yêu cầu điều ngược lại (comment của trait `BlockVerifier`: "All honest validators should produce the same verification outcome for the same block").
- `[GIẢ THUYẾT]` Hậu quả thực tế của giới hạn lệch là block của validator giới hạn rộng bị validator giới hạn hẹp từ chối (`TooManyTransactions`/`TooManyTransactionBytes`), tức mất liveness chứ không fork state. Chưa dựng thử.

### 1.3. Go: luật hợp lệ và luật phí đọc từ `config.json` của từng node
Comment trong `execution/pkg/blockchain/tx_processor/signature_enforcement.go` (khoảng dòng 73) ghi: *"sigPolicy is the per-chain signature rule set, derived from the node config (identical on every validator of a chain)"*. Giả định "giống nhau trên mọi validator" **không được cưỡng chế ở đâu cả** `[ĐỌC CODE]`.

| Tham số (cấu hình node) | Dùng ở đâu | Ảnh hưởng kết quả thực thi? |
|---|---|---|
| `tx_signature_mode` (secp / bls) | `sigPolicyOf` → quyết định chữ ký nào hợp lệ khi thực thi block (`signature_enforcement.go`) | Có: tx hợp lệ ở node này có thể bị từ chối ở node kia |
| `account_gate` | `AccountGateParentRegistered()` ở `signature_enforcement.go` và các handler | Có |
| `verify_device_key` | `tx_processor/validation.go:473` | Có thể; **cần xác minh** đường gọi (admission hay khi thực thi block) |
| `free_fee_addresses` | nạp từ `config.json` vào biến toàn cục (`cmd/simple_chain/app_network.go:99-100`), truyền qua `InitProcessors` (`cmd/simple_chain/app.go:846`) vào `ChainState`; dùng khi tính phí ở `vm_processor` (`GetFreeFeeAddress()`) và ở `tx_processor/fee_floor.go:30,84` | Có: địa chỉ nào được miễn phí quyết định số dư sau giao dịch |
| `cross_chain.*` (`ReserveChainID`, `BondLeverage`, `UnbondingPeriodSeconds`, `MinSecurityBondToRegisterWei`, `MinNativeStakeToRegisterWei`, `VelocityLimitDisabled`) | `applyReserveChainIDConfig`, `applySecurityBondConfig` (`gateway_handler.go` khoảng dòng 256 và 300) sao chép **một lần** từ cấu hình vào trạng thái on-chain của gateway | Có, xem 1.4 |
| `state_backend` | chọn trie (NOMT/MPT) (`cmd/simple_chain/app_storage.go`, `block_state_commit.go`) | `[GIẢ THUYẾT]` có: hai backend lưu dữ liệu khác định dạng nên không thể trộn trên một chain; tôi không tìm thấy trong code một khẳng định rằng root của chúng khác nhau. Đây là lựa chọn bất biến của chain, không phải tham số chỉnh được |
| `chainId` | `app_network.go:34` lấy từ **genesis** (`app.config.ChainId = app.genesis.Config.ChainId`) | Đã có nguồn chung là genesis, miễn là file genesis giống nhau trên mọi node `[CHƯA ĐO]` có kiểm tra hash genesis hay không |
| `attestation_interval`, `epoch_duration_seconds`, `epoch` | genesis (`pkg/config/genesis.go`) | Đã là tham số dùng chung |
| `min_gas_price` | chỉ ở `tx_validator_pool_core.go:388,531` (admission) | Không: chính sách mempool, được phép khác nhau |

`GenesisConfig` hiện chỉ có `ChainId`, `Epoch`, `EpochTimestampMs`, `AttestationInterval`, `EpochDurationSeconds` (cộng danh sách `alloc` và `validators`) `[ĐỌC CODE: pkg/config/genesis.go]`. Không có tham số luật nào khác.

### 1.4. Mẫu "sao chép một lần từ cấu hình vào trạng thái" có một lỗ hổng vận hành
`[ĐỌC CODE]` `applySecurityBondConfig` chạy ở **mỗi** giao dịch barrier và chỉ điền vào trạng thái gateway khi trạng thái chưa có giá trị (`if engine.BondLeverage == 0 && cc.BondLeverage > 0 { ... }`, và `VelocityLimitDisabled` chỉ chuyển false → true). Nghĩa là:
- giá trị đi vào trạng thái phụ thuộc cấu hình của node **tại thời điểm giao dịch barrier đầu tiên được thực thi** trên node đó;
- đổi `config.json` trên **một** validator rồi khởi động lại sẽ khiến node đó điền giá trị mới ở giao dịch barrier kế tiếp, còn node khác thì không, dẫn tới state root khác nhau.
Đây là một nguy cơ fork do sửa cấu hình, không cần bug code nào. `[GIẢ THUYẾT]` về mức độ dễ xảy ra trong vận hành; chưa dựng thử.

### 1.5. Raft
- `[ĐỌC CODE]` `raft.max_batch_bytes` là cấu hình riêng từng node. Nó quyết định việc nhận/tách batch ở leader và việc admission/forward ở follower; entry đã commit thì mọi replica áp dụng như nhau. Vì vậy lệch giá trị này gây **mất giao dịch hoặc tắc đường nhận** (đã thấy ở PR #184: giao dịch hợp lệ nằm cạnh giao dịch vượt giới hạn của leader bị loại), không gây lệch state. Mức quan trọng: cao về vận hành, thấp về consensus.

---

## 2. Cách Sui làm (để đối chiếu)
Nguồn: `consensus/core/src/transaction.rs`, `consensus/core/src/block_verifier.rs`, `crates/sui-protocol-config/src/lib.rs`.
- Giới hạn là **tham số protocol có phiên bản**, giống nhau trên mọi validator: giao dịch tối đa 256 KB (từ phiên bản 36); tổng byte giao dịch mỗi block 6 MB (v36) → 512 KB (v55) → 288 KB (v134 non-mainnet, v135 mọi chain); số giao dịch mỗi block 512 (v55) → 128 (v134/v135).
- Bất biến thiết kế: giới hạn một giao dịch (256 KB) nhỏ hơn giới hạn byte mỗi block (288 KB), nên mọi giao dịch đã nhận đều vừa một block rỗng; nếu không, mã dùng `debug_fatal!`.
- Từ chối đồng bộ ở cửa vào (`TransactionClient::submit_no_wait`): vượt bất kỳ giới hạn nào thì không giao dịch nào được gửi; phần dư lúc dựng block được cất lại và đưa vào block sau, không bỏ.
- Mọi validator kiểm lại giới hạn khi nhận block bằng đúng tham số protocol (`TransactionTooLarge`, `TooManyTransactions`, `TooManyTransactionBytes`).
- Đổi giá trị = **tăng phiên bản giao thức** và thêm nhánh trong `get_for_version`; phiên bản hiệu lực được chọn ở ranh giới epoch.

---

## 3. Đề xuất: bản ghi tham số giao thức có phiên bản

### 3.1. Phân loại (cái gì nên là tham số giao thức)
- **Loại A, bắt buộc là tham số giao thức** (ảnh hưởng kết quả thực thi hoặc việc chấp nhận block): `tx_signature_mode`, `account_gate`, `free_fee_addresses`, các tham số `cross_chain.*` ở bảng 1.3, giới hạn kích thước giao dịch/block của consensus Rust, và (nếu xác minh được) `verify_device_key`.
- **Loại B, đã chung hoặc bất biến của chain:** `chainId`, `attestation_interval`, `epoch_duration_seconds`, `state_backend`. Giữ ở genesis; chỉ cần kiểm tra đồng nhất.
- **Loại C, chính sách cục bộ, được khác nhau:** `min_gas_price`, cổng, đường dẫn, rate limit, cache, pruning, snapshot, log, TLS, tham số Go GC. Không đưa vào giao thức.
- **Loại D, vận hành nhưng nên đồng nhất:** `raft.max_batch_bytes` và các giới hạn Raft liên quan. Không phải luật state nhưng lệch gây mất giao dịch; nên đặt chung và kiểm tra lúc khởi động.

### 3.2. Hình dạng
Một bản ghi duy nhất, ví dụ `ProtocolParams`, gồm `version`, các tham số loại A và `activation` (epoch hoặc block bắt đầu hiệu lực):
- **Nơi lưu:** trạng thái on-chain (cùng kiểu với `GatewayEngine`, lưu trong storage của một địa chỉ hệ thống) và/hoặc phần `protocol` trong genesis cho chain mới. Mọi node đọc từ trạng thái, không đọc `config.json`, khi thực thi block.
- **Giá trị hiệu lực là hàm thuần của (trạng thái, số block/epoch).** Không dùng đồng hồ, biến môi trường hay file cục bộ để quyết định thực thi (đúng nguyên tắc Zero-Fork và quy tắc không dùng timeout để quyết định dispatch).
- **Cấu hình cục bộ chỉ còn vai trò xác nhận:** nếu `config.json` khác trạng thái on-chain thì node cảnh báo hoặc từ chối khởi động; không bao giờ ghi đè.
- **Rust:** `ProtocolConfig` được dựng từ phiên bản đã thống nhất lúc bắt đầu epoch thay vì `get_for_max_version_UNSAFE()`. Bỏ đường ghi đè `_for_testing` ra khỏi đường khởi động thật.

### 3.3. Cơ chế kích hoạt
- Binary mới hỗ trợ các phiên bản ≤ N nhưng **phiên bản đang hoạt động** lấy từ trạng thái. Triển khai binary mới vì thế không đổi hành vi (phần logic mới nằm sau cổng `version >= N`); đây là điều khiến nâng cấp cuốn chiếu trở nên an toàn.
- Kích hoạt bằng một giao dịch hệ thống/phiếu bầu đủ ngưỡng của validator tại ranh giới epoch. Hạ tầng sẵn có: `EpochChangeProposal`/`EpochChangeVote` (`consensus/metanode/meta-consensus/core/src/epoch_change.rs`) hiện mang `new_epoch`, `new_epoch_timestamp_ms`, `proposal_commit_index`, chữ ký; có thể mở rộng thêm `protocol_version`. Go đã đọc `Epoch` từ header block (`CheckAndUpdateEpochFromBlock`).
- Metric `protocol_version` đã có sẵn trên Rust (`authority_node/mod.rs:305`) để vận hành theo dõi.

---

## 4. Lộ trình theo mức rủi ro

| Pha | Việc | Ảnh hưởng consensus | Dữ liệu |
|---|---|---|---|
| P0 ✅ | **Dấu vân tay cấu hình:** mỗi node băm các trường loại A/B/D, xuất ra metric/RPC; công cụ so sánh trên cả cụm; kiểm tra khi khởi động và khi bắt tay peer (chỉ cảnh báo). Sửa `HOW_TO_TUNE_BLOCK_SIZE.md` để nói rõ đây là tham số giao thức. | Không | Không đổi |
| P1 ✅ (chỉ genesis, xem mục 0.2) | Thêm `ProtocolParams` vào genesis và trạng thái on-chain, **chỉ đọc và so sánh** với cấu hình cục bộ; lệch thì từ chối khởi động với tham số loại A. | Không đổi kết quả thực thi | Chỉ thêm một khóa trạng thái, trường mới `omitempty` |
| P2 | Chuyển luật loại A sang đọc từ `ProtocolParams` (giữ nguyên giá trị hiện hành khi khởi tạo cho chain đang chạy). | **Có** (nguồn đọc đổi); yêu cầu P0 đã chứng minh các node giống nhau | Cần lệnh khởi tạo một lần, idempotent |
| P3 | Phiên bản có cổng kích hoạt tại ranh giới epoch + phiếu bầu validator (Rust `EpochChangeProposal` mở rộng) + cổng `version >= N` ở Go. | Có, nhưng từ đây trở đi các thay đổi sau có thể nâng cấp cuốn chiếu | Không cần di trú nếu theo mục 5.2 |

Khuyến nghị: làm **P0 và P1** trước (rẻ, không đổi kết quả thực thi, thu hẹp nguy cơ ở mục 1.4); P2/P3 chỉ khi bạn muốn nâng cấp cuốn chiếu cho thay đổi consensus. Không nên làm P2/P3 trên chain đang chạy mà chưa có kết quả P0 trên cụm thật.

---

## 5. Hướng dẫn nâng cấp không ảnh hưởng dữ liệu

### 5.1. Quy trình **hiện nay** (chưa có phiên bản giao thức) cho thay đổi làm đổi kết quả thực thi
Dựa trên `runbook_deploy_audit_rounds_and_stm_fix_20261019.md` và `block_stm_estimate_fixes_upgrade_runbook.md`:
1. **Phân loại thay đổi.** Không đổi kết quả thực thi (RPC, log, hiệu năng thuần) → nâng cấp cuốn chiếu được. Đổi kết quả thực thi → quy trình dưới đây.
2. **Chứng minh bằng dữ liệu thật trước khi chạm cụm** (bước còn thiếu nhất, `[CHƯA ĐO]` trên dữ liệu 231/230):
   - Sao chép dữ liệu một node (bản sao, không đụng node thật).
   - Chạy `execution/cmd/tool/tz_replay_check` hai lần trên **hai bản sao** của cùng dữ liệu, một lần bằng binary cũ, một lần bằng binary mới, cùng khoảng block: `-config <config> -from N -to M -out ket_qua_cu.json`, rồi `-compare-a ket_qua_cu.json -compare-b ket_qua_moi.json`. Mỗi block ghi `root`, `stake_states_root` và từng giao dịch (`hash`, `status`, `exception`, `gas_used`, `return_hash`) `[ĐỌC CODE: cmd/tool/tz_replay_check/main.go:54-73]`. Công cụ chạy đúng đường thực thi production (`block_validator.ProcessBlock`) và cố ý không khởi động consensus Rust nên không làm chain bản sao tiến thêm.
   - Mọi khác biệt = thay đổi consensus. Nếu không có khác biệt trên khoảng block đã chạy, thay đổi **không ảnh hưởng dữ liệu hiện có** trong phạm vi khoảng đó; nếu có, thay đổi cần cổng kích hoạt (P3) hoặc phải chấp nhận cắt mốc (activation block).
   - Bổ sung bằng `admin_verifyHistoricalRoot(password, block)` để xác nhận root lịch sử dựng lại từ changelog khớp header.
3. **Sao lưu:** snapshot và bản sao thư mục dữ liệu của từng node trước khi dừng (xem runbook, mục 3 và 4).
4. **Triển khai:** dừng cả cụm, thay binary đồng loạt (build lại C++ nếu có thay đổi native), giữ dữ liệu, khởi động lại. Đồng thời bảo đảm **cấu hình giống nhau** trên mọi validator (đối chiếu bằng dấu vân tay P0 khi có; trước đó so sánh thủ công các trường ở bảng 1.3).
5. **Go/No-Go sau triển khai:** mọi node cùng chiều cao và cùng hash/state root từng block; `ci.sh run-now --reset` hoặc tập kiểm tra tương đương; test chaos restart.
6. **Rollback:** trước khi có block mới do binary mới tạo, rollback là chạy lại binary cũ trên dữ liệu cũ. Sau đó, rollback nghĩa là khôi phục từ snapshot trước nâng cấp và chấp nhận mất các block mới.

### 5.2. Quy tắc để dữ liệu không bị ảnh hưởng (cho mọi thay đổi trạng thái)
- **Chỉ thêm, không diễn giải lại.** Trường mới trong trạng thái phải `omitempty` và giá trị 0/rỗng phải cho đúng hành vi cũ (mẫu của `GatewayEngine`: `VelocityLimitDisabled` mặc định false giữ nguyên luật cũ). Không đổi nghĩa của byte đã lưu.
- **Giữ bộ giải mã cho định dạng cũ.** Có số phiên bản lược đồ ở nơi lưu dữ liệu bền (ví dụ `FsmSnapshotMeta.SchemaVersion` của Raft đã làm vậy); từ chối khởi động khi gặp lược đồ chưa hỗ trợ thay vì đoán.
- **Không sửa tại chỗ file NOMT/Pebble.** Cần cấu trúc mới thì tạo namespace/kho mới.
- **Di trú dữ liệu (nếu bắt buộc) là một giao dịch hệ thống tại block kích hoạt**, idempotent, có checksum kết quả, chạy giống hệt trên mọi node; không chạy như bước ngoài chuỗi trên từng node.
- **Điểm kích hoạt là hàm xác định của (số block hoặc epoch)**, không phải thời gian đồng hồ.
- **Thà chờ còn hơn lệch:** nếu node thấy tham số loại A khác trạng thái on-chain thì dừng (fail-stop) thay vì tiếp tục thực thi.

### 5.3. Danh sách kiểm tra trước mỗi nâng cấp
- [ ] Đã phân loại thay đổi (đổi kết quả thực thi hay không).
- [ ] Đã chạy `tz_replay_check` cũ/mới trên bản sao dữ liệu và xem khác biệt.
- [ ] Đã so khớp cấu hình loại A/B/D trên mọi validator (dấu vân tay hoặc đối chiếu thủ công).
- [ ] Đã sao lưu snapshot và thư mục dữ liệu từng node.
- [ ] Đã kiểm tra CI xanh ở commit sẽ triển khai (không phải commit trước đó).
- [ ] Đã chốt người/thứ tự dừng, thay binary, khởi động; không để hai validator chạy hai phiên bản luật khác nhau cùng lúc.
- [ ] Đã có điều kiện Go/No-Go và kế hoạch rollback rõ ràng.

---

## 6. Rủi ro và điều chưa biết
- `[CHƯA ĐO]` Chưa chạy `tz_replay_check` cũ/mới trên dữ liệu thật của cụm 231/230; mục 5.1 bước 2 mô tả quy trình đã đọc từ code công cụ, chưa diễn tập.
- `[CHƯA ĐO]` Chưa kiểm tra có so khớp hash genesis giữa các node lúc khởi động hay không.
- `[CHƯA ĐO]` Chưa xác minh `verify_device_key` được dùng ở đường thực thi block hay chỉ ở admission.
- `[GIẢ THUYẾT]` Mọi thay đổi ở P2/P3 là thay đổi consensus: phải triển khai đồng loạt mọi validator và build lại C++ nếu có thay đổi native; không nâng cấp cuốn chiếu cho đến khi P3 xong.
- Việc chuyển `cross_chain.*` từ "sao chép một lần" sang tham số giao thức cần quyết định cho chain đã có trạng thái gateway: giá trị khởi tạo phải đúng bằng giá trị đang nằm trong trạng thái, nếu không sẽ đổi luật của chain đang chạy.
- Tài liệu này chưa đo bất kỳ tác động hiệu năng nào.

## 7. Quyết định cần bạn đưa ra
1. Có làm **P0 (dấu vân tay cấu hình)** và **P1 (bản ghi chỉ đọc)** ngay không? (khuyến nghị: có)
2. Có muốn đi tới **P3 (nâng cấp cuốn chiếu có phiên bản)**, hay tiếp tục dùng quy trình dừng cả cụm và chỉ cải thiện kiểm tra trước nâng cấp?
3. Với `raft.max_batch_bytes`: đưa vào `ProtocolParams` (đồng nhất theo giao thức) hay chỉ kiểm tra đồng nhất lúc khởi động?
4. Cho phép tôi chạy diễn tập `tz_replay_check` cũ/mới trên bản sao dữ liệu cụm cục bộ trước (không đụng cụm 231/230)?
