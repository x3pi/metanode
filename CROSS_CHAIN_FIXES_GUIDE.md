# 📘 Hướng dẫn Tổng hợp các Lỗi Cross-Chain & Cách Khắc phục (Comprehensive Fix Guide)

Tài liệu này tổng hợp toàn bộ các lỗi thực tế phát hiện trong quá trình vận hành cụm Cross-Chain Mesh (Root Anchor + Private Chains), nguyên nhân gốc rễ và hướng dẫn chi tiết từng bước để áp dụng sang các môi trường hoặc repo khác.

---

## 📑 Mục lục
1. [Lỗi 1: Xung đột cổng RPC giữa các Private Chains](#lỗi-1-xung-đột-cổng-rpc-giữa-các-private-chains)
2. [Lỗi 2: Relayer Daemon thiếu cấu hình theo dõi Root Anchor & Reserve Chain](#lỗi-2-relayer-daemon-thiếu-cấu-hình-theo-dõi-root-anchor--reserve-chain)
3. [Lỗi 3: Chuyển tiền Native Token trực tiếp giữa 2 Private Chain bị Revert (Quy tắc trần C8)](#lỗi-3-chuyển-tiền-native-token-trực-tiếp-giữa-2-private-chain-bị-revert-quy-tắc-trần-c8)
4. [Lỗi 4: Root Anchor (Chain 991) chưa được đăng ký vào `ChainRegistry`](#lỗi-4-root-anchor-chain-991-chưa-được-đăng-ký-vào-chainregistry)
5. [Lỗi 5: Lỗi `governance proposal not found` khi nạp quỹ ban đầu (Genesis Allocation)](#lỗi-5-lỗi-governance-proposal-not-found-khi-nạp-quỹ-ban-đầu-genesis-allocation)
6. [Lỗi 6: Lỗi `quorum not reached for chain 991 after 30 polls` khi gom QuorumCert](#lỗi-6-lỗi-quorum-not-reached-for-chain-991-after-30-polls-khi-gom-quorumcert)

---

## Lỗi 1: Xung đột cổng RPC giữa các Private Chains

### 🔴 Hiện tượng & Nguyên nhân
Trong cấu hình `inventory.yml` của các Private Chain, cả Chain 101 và Chain 102 đều được đặt mặc định cổng `rpc_port: 8546`. Khi cả 2 node cùng khởi chạy trên cùng một máy chủ, node thứ 2 không bind được cổng RPC hoặc ghi đè request.

### 🛠️ Cách khắc phục
Chỉnh sửa file `deploy/ansible_private_chains/inventory.yml`:
```yaml
server_233_chain_101:
  chain_id: 101
  rpc_port: 8546

server_233_chain_102:
  chain_id: 102
  rpc_port: 8547  # <-- Đổi sang 8547
```

---

## Lỗi 2: Relayer Daemon thiếu cấu hình theo dõi Root Anchor & Reserve Chain

### 🔴 Hiện tượng & Nguyên nhân
Khi thực hiện luồng chuyển tiền 2 chặng (2-hop: `Chain 101 ➔ Root Anchor 991 ➔ Chain 102`), chặng thứ 2 xuất phát từ Root Anchor sang Chain 102. Nếu Relayer chỉ nhận cờ `-chains 101=...,102=...` mà không có Root Anchor `991`, Relayer sẽ:
1. Không theo dõi các outbound batch sinh ra từ Root Anchor (`991 ➔ 102`).
2. Không biết chuỗi nào là Reserve Chain để thực hiện phương thức miễn trừ kiểm tra trần (`attestReserveIssuedCommit`).

### 🛠️ Cách khắc phục
Cập nhật script chạy Relayer (`deploy/ansible_private_chains/run_relayer_tmux.sh`):
1. Tự động truy vấn `eth_chainId` của Root Anchor.
2. Thêm Root Anchor vào danh sách `--chains`.
3. Bổ sung cờ `--reserve-chain-id <ID>`.

```bash
# Lấy chainId thực tế của Root Anchor
ROOT_CHAIN_ID=$(curl -s -X POST "$ROOT_ANCHOR_RPC" -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}' | jq -r '.result')
ROOT_CHAIN_ID_DEC=$(printf "%d" "$ROOT_CHAIN_ID")

# Bổ sung Root Anchor vào chuỗi kết nối
CHAINS_ARG="${CHAINS_ARG},${ROOT_CHAIN_ID_DEC}=${ROOT_ANCHOR_RPC}"

# Khởi chạy Relayer với đầy đủ tham số
./cross_chain_relayer \
    --root-anchor "$ROOT_ANCHOR_RPC" \
    --reserve-chain-id "$ROOT_CHAIN_ID_DEC" \
    --chains "$CHAINS_ARG" \
    --relayer-key "$SUBMITTER_KEY" \
    --poll-interval 100ms
```

---

## Lỗi 3: Chuyển tiền Native Token trực tiếp giữa 2 Private Chain bị Revert (Quy tắc trần C8)

### 🔴 Hiện tượng
Khi client gửi Outbound chuyển tiền `value > 0` trực tiếp từ `Chain 101 ➔ Chain 102`, giao dịch chứng thực `attestCommit` trên Chain 102 bị Revert với lỗi:
> *"only the configured Reserve chain may perform a ceiling-enforced attestCommit of a nonzero-value commit from another chain"*

### 🔍 Nguyên nhân
Đây là cơ chế bảo mật tối thượng **C8 (Native Ceiling & Supply Integrity)** để chống việc các Private Chain tự in tiền (mint vô tội vạ) rồi chuyển sang chuỗi khác. Mọi luồng chuyển tiền Native Token (`value > 0`) **bắt buộc phải đi qua Reserve Chain (Root Anchor)** để khấu trừ và cấp phát hạn mức (2-hop Relay).

### 🛠️ Cách khắc phục trong Code Client
Khi nộp lệnh chuyển tiền Native Token từ Chain 101 sang Chain 102:
1. **DestChainID** gửi đến Gateway đặt là `991` (Reserve Chain ID).
2. **Target**: Địa chỉ ví nhận tiền trên Chain 102.
3. **Payload**: Đóng gói với tiền tố `MTNRELAY1:` kèm 8 bytes Big-Endian của `DestChainID` cuối cùng (`102`).
4. **Value**: Số lượng MTN muốn chuyển.

```go
// Helper đóng gói payload chuyển tiếp 2 chặng
func EncodeRelayPayload(finalDestChainID uint64, innerPayload []byte) []byte {
    prefix := []byte("MTNRELAY1:")
    buf := make([]byte, len(prefix)+8+len(innerPayload))
    copy(buf, prefix)
    for i := 0; i < 8; i++ {
        buf[len(prefix)+i] = byte(finalDestChainID >> (56 - 8*i))
    }
    copy(buf[len(prefix)+8:], innerPayload)
    return buf
}

// Client gửi Outbound:
gatewayABI.Pack("outbound",
    big.NewInt(991),                           // destChainId: Gửi tới Reserve 991
    recipientAddr,                             // target: Ví nhận trên Chain 102
    EncodeRelayPayload(102, nil),              // payload: Tiền tố MTNRELAY1: + 102
    big.NewInt(0),                             // assetId: 0 (Native MTN)
    amountWei,                                 // value: Số tiền chuyển
    big.NewInt(0), big.NewInt(0),              // tip, gasFee
    uint8(2),                                  // hopCount: 2 chặng
    false,                                     // ordered
)
```
*(Lưu ý: Đối với Smart Contract GMP Call không kèm tiền `value = 0`, client vẫn có thể gọi trực tiếp `101 ➔ 102` bình thường).*

---

## Lỗi 4: Root Anchor (Chain 991) chưa được đăng ký vào `ChainRegistry`

### 🔴 Hiện tượng
Relayer Daemon khi xử lý chặng 2 (`991 ➔ 102`) gặp lỗi:
> *`⚠️ [RELAYER DAEMON] batch/relay chain 991 -> 102 failed: poll and aggregate QuorumCert: chain 991 is not registered on Root Anchor`*

### 🔍 Nguyên nhân
Công cụ `register_chains` trước đây chỉ lặp qua danh sách `--chains 101,102` mà bỏ quên việc đăng ký chính Root Anchor (`Chain 991`) vào `ChainRegistry`. Do đó, khi Root Anchor phát hành commit batch, Relayer không tìm thấy danh bạ Validator của Chain 991 để gom `QuorumCert`, và Chain 102 cũng không có public key của Root Anchor để xác thực chữ ký.

### 🛠️ Cách khắc phục trong `register_chains` (`execution/cmd/tool/register_chains/main.go`)
1. Thêm cờ `--root-anchor-keys-dir` trỏ tới thư mục chứa keys của Root Anchor (`deploy/systemd`).
2. Dò tìm các khóa `BLSPrivateKey` của các Validator Root Anchor trong `node-*_keys/execution.json`.
3. Sinh `PubkeyBLS`, tính chữ ký `PopSignature` (Proof-of-Possession).
4. Đăng ký `registerChainViaStake(991)` lên cả **Root Anchor**, **Chain 101** và **Chain 102**.

---

## Lỗi 5: Lỗi `governance proposal not found` khi nạp quỹ ban đầu (Genesis Allocation)

### 🔴 Hiện tượng
Khi chạy `./deploy_private_chains.sh --reset-all`, công cụ dừng lại với lỗi:
> *`❌ fundGenesis: genesis mint failed: mint genesis supply: executeProposal: reverted: governance proposal not found`*

### 🔍 Nguyên nhân
Trong Smart Contract `Gateway.sol`, hàm `propose` sử dụng **`blockTime`** thực tế của block để hash `proposalID`:
`proposalID = Keccak256(kind + blockTime + payload)`
Và trả về `proposalID` trong `receipt["return"]`.

Tuy nhiên, công cụ `register_chains` cũ lại tự tính `proposalID` bằng `time.Now().Unix()` trên máy client. Sự chênh lệch vài giây giữa giờ máy tính và `blockTime` làm cho mã `proposalID` gửi đi lúc `vote` và `executeProposal` không khớp với bản ghi đang lưu on-chain.

### 🛠️ Cách khắc phục
Trong hàm `proposeVoteExecute` của `register_chains/main.go`, trích xuất trực tiếp `proposalID` từ biên lai trả về của giao dịch `propose`:

```go
// 1. Gửi lệnh propose
proposeReceipt, err := sendTxAndWait(ctx, client, privKey, fromAddress, rpcURL, proposeFee, 2_000_000, proposeCalldata, label)
if err != nil {
    return err
}

// 2. Trích xuất chính xác proposalID do Smart Contract trả về trong Receipt
var proposalID common.Hash
returnHex := extractRawReturnHex(rpcURL, proposeReceipt.TxHash)
if returnBytes, err := hex.DecodeString(strings.TrimPrefix(returnHex, "0x")); err == nil && len(returnBytes) >= 32 {
    proposalID = common.BytesToHash(returnBytes[len(returnBytes)-32:])
} else {
    // Fallback chuẩn xác bằng block.Time()
    propTs := now
    if block, err := client.BlockByNumber(ctx, proposeReceipt.BlockNumber); err == nil && block != nil {
        propTs = block.Time()
    }
    var buf []byte
    buf = append(buf, kind)
    var tsBytes [8]byte
    binary.BigEndian.PutUint64(tsBytes[:], propTs)
    buf = append(buf, tsBytes[:]...)
    buf = append(buf, payload...)
    proposalID = crypto.Keccak256Hash(buf)
}

// 3. Tiến hành vote và execute theo proposalID chuẩn xác này
```

---

## Lỗi 6: Lỗi `quorum not reached for chain 991 after 30 polls` khi gom QuorumCert

### 🔴 Hiện tượng
Relayer Daemon khi xử lý batch chuyển tiền từ Root Anchor sang Chain 102 báo lỗi:
> *`⚠️ [RELAYER DAEMON] batch/relay chain 991 -> 102 failed: poll and aggregate QuorumCert: quorum not reached for chain 991 epoch 0 commit ... after 30 polls`*

### 🔍 Nguyên nhân
1. Cụm Root Anchor thực tế chỉ triển khai **3 Validator** (`node_ids: [0, 1, 2]`) theo `deploy/ansible/inventory.yml`.
2. Tuy nhiên, trong thư mục `deploy/systemd/` còn sót lại các thư mục cũ `node-3_keys` và `node-4_keys` từ các đợt test trước.
3. Khi `discoverRootAnchorCommittee` quét thư mục, nó đọc cả 5 node ➔ Đăng ký `ChainRegistry[991]` với 5 Validator (Tổng stake = 5000, Ngưỡng quorum 66.67% tương đương 3334 stake, tức cần ít nhất 4 node ký).
4. Vì chỉ có 3 node đang chạy và ký commit, tổng stake thu được là 3000 / 5000 (60% < 66.67%) ➔ Relayer không bao giờ gom đủ chữ ký Quorum!

### 🛠️ Cách khắc phục
1. **Xóa các thư mục key cũ thừa:** `rm -rf deploy/systemd/node-3_keys deploy/systemd/node-4_keys`.
2. **Cập nhật `discoverRootAnchorCommittee`:** Tự động đọc `deploy/ansible/inventory.yml` để lọc đúng các `node_ids` thực tế đang chạy trong cụm:

```go
func parseInventoryActiveNodeIDs(deployDir string) map[int]bool {
    active := make(map[int]bool)
    inventoryPaths := []string{
        filepath.Join(deployDir, "inventory.yml"),
        filepath.Join(deployDir, "../ansible/inventory.yml"),
        "deploy/ansible/inventory.yml",
    }
    re := regexp.MustCompile(`node_ids:\s*\[([^\]]+)\]`)
    for _, p := range inventoryPaths {
        if data, err := os.ReadFile(p); err == nil {
            for _, m := range re.FindAllStringSubmatch(string(data), -1) {
                if len(m) >= 2 {
                    for _, part := range strings.Split(m[1], ",") {
                        if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
                            active[id] = true
                        }
                    }
                }
            }
            if len(active) > 0 { break }
        }
    }
    return active
}
```

---

## Lỗi 7: Thiếu cấu hình `CommitAttestationWorker` trên các Validator Node của Root Anchor

### 🔴 Hiện tượng
Khi Root Anchor phát hành commit chuyển tiền sang chuỗi con (chặng 2 `991 ➔ 102`), Relayer không bao giờ thu thập được chữ ký nào từ Root Anchor:
> *`⚠️ [RELAYER DAEMON] batch/relay chain 991 -> 102 failed: poll and aggregate QuorumCert: quorum not reached for chain 991 epoch 0 commit ... after 30 polls`*

### 🔍 Nguyên nhân
Trong hàm khởi động `block_processor_core.go`, tiến trình `CommitAttestationWorker` (chịu trách nhiệm tự động ký chữ ký BLS cho commit khi block được finalize) chỉ được khởi chạy khi file cấu hình `execution.json` có khai báo 2 trường:
1. `cross_chain.root_anchor_rpc_urls`
2. `cross_chain.root_anchor_submitter_private_key_hex`

Trước đó, các script sinh cấu hình chỉ gán 2 trường này cho các **Private Chains**, mà quên gán cho chính các **Root Anchor Nodes**. Do đó, không có Validator nào của Root Anchor đứng ra ký chữ ký BLS cho commit của Root Anchor ➔ `getCommitAttestationShares(991)` luôn trả về 0 chữ ký!

### 🛠️ Cách khắc phục
1. Cập nhật `deploy/systemd/gen_validator_entry.py` để bổ sung cấu hình `cross_chain`:
```python
"cross_chain": {
    "config_contract": "0x4c1c27b3147820915431554F2B2383175FAAd198",
    "reserve_chain_id": args.reserve_chain_id if getattr(args, "reserve_chain_id", None) is not None else args.chain_id,
    "root_anchor_rpc_urls": (
        args.root_anchor_rpc.split(",") if getattr(args, "root_anchor_rpc", None)
        else [f"http://127.0.0.1:{rpc_port.lstrip(':')}" if rpc_port else "http://127.0.0.1:10746"]
    ),
    "root_anchor_submitter_private_key_hex": getattr(args, "root_anchor_submitter_key", None) or "d3d8157f2571153bcb664233f998a82b9b475fe509f92caf65ca2461bae7f1a9",
    "root_anchor_poll_interval_seconds": 1,
    "min_native_stake_to_register_wei": getattr(args, "min_native_stake_to_register_wei", "1000000000000000000"),
    "devnet_governance_timelock_seconds_override": 10
},
```
2. Cập nhật `deploy/ansible/roles/local_build/tasks/main.yml` truyền cờ `--root-anchor-rpc` và `--root-anchor-submitter-key`.

---

## 🎯 Quy trình Triển khai & Kiểm tra Chuẩn (Checklist)

``` bash
cd /home/abc/nhat/consensus-chain/metanode/deploy/ansible && ./ansible_deploy.sh --reset-all && \
cd /home/abc/nhat/consensus-chain/metanode/deploy/ansible_private_chains && ./deploy_private_chains.sh --reset-all && \
./run_relayer_tmux.sh restart && \
sleep 3 && \
cd /home/abc/nhat/consensus-chain/metanode-suite/test-simple/test-rpc/test-blockstm/cross-chain/01-client-only-transfer && go run main.go
```