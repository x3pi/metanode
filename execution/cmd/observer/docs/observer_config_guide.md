# 📡 Observer (Cross-Chain Relayer) — Hướng dẫn cấu hình & chạy

## Tổng quan

Observer là dịch vụ **relay cross-chain messages** giữa các blockchain. Mỗi observer instance là 1 **relayer** — lắng nghe events từ chain remote, verify, ký, và submit lên chain local.

Hệ thống sử dụng **multi-sig**: cần ≥ 2/3 relayer cùng approve 1 message trước khi execute.

## Kiến trúc

```
Chain A (local, nationId=1)              Chain B (remote, nationId=2)
  ┌──────────────────┐                     ┌──────────────────┐
  │ CrossChainGateway │                     │ CrossChainGateway │
  │    (local_contract)│◄─── Observer ────►│  (remote_contract) │
  └──────────────────┘    lắng nghe +      └──────────────────┘
                          relay
                          
  Observer-1 ──┐
  Observer-2 ──┤── Cùng relay, cần 2/3 approve
  Observer-3 ──┘
```

---

## Cấu hình Observer

### File config: `config-client-tcp-{N}.json`

Mỗi observer cần 1 file config riêng. Ví dụ `config-client-tcp-1.json`:

```jsonc
{
  // ═══════════════════════════════════════════════════════════
  // PHẦN 1: CẤU HÌNH LOCAL (chain mà observer chạy trên)
  // ═══════════════════════════════════════════════════════════
  
  "connection_address": "0.0.0.0:4900",
  // Port TCP mà observer instance này sử dụng
  // Mỗi observer PHẢI dùng port KHÁC NHAU (4900, 4901, 4902...)

  "parent_connection_address": "0.0.0.0:6200",
  // Địa chỉ node blockchain LOCAL (Chain A) mà observer kết nối
  // Dùng để GỬI giao dịch (receiveMessage, processConfirmation) lên chain local

  "chain_id": 991,
  // Chain ID của blockchain local

  "nation_id": 1,
  // Nation ID của chain LOCAL — dùng để verify DestNationId trong event
  // Event MessageSent trên remote phải có destNationId == 1 mới xử lý

  "parent_connection_type": "client",
  // Kiểu kết nối ("client" = TCP client)

  "cross_chain_abi_path": "pkg/abi/cross_chain_abi.json",
  // Đường dẫn đến ABI của CrossChainGateway contract

  "log_path": "./logs/log-1",
  // Thư mục lưu log cho observer này

  // ═══════════════════════════════════════════════════════════
  // PHẦN 2: DANH SÁCH REMOTE CHAINS (chain cần relay)
  // ═══════════════════════════════════════════════════════════

  "remote_chains": [
    {
      "name": "Chain B",
      // Tên hiển thị — chỉ dùng trong log

      "nation_id": 2,
      // Nation ID của chain REMOTE
      // Dùng để verify SourceNationId trong event
      // Event phải có sourceNationId == 2 mới xử lý

      "parent_connection_address": "192.168.1.233:6200",
      // Địa chỉ node blockchain REMOTE (Chain B)
      // Observer kết nối đến đây để LẮNG NGHE events

      "private_key": "2b3aa0f6...",
      // Private key để GỬI giao dịch lên chain LOCAL
      // Tất cả observer dùng CÙNG key này (chỉ để submit tx)
      // KHÔNG phải key ký message (xem eth_private_key)

      "parent_address": "0xdbA14EEF...",
      // Wallet address tương ứng với private_key
      // Là "sender" khi gọi receiveMessage/processConfirmation

      "eth_private_key": "05cd9f0d...",
      // 🔑 ETH ECDSA private key — MỖI OBSERVER KHÁC NHAU
      // Dùng để KÝ messageId (ecrecover trên contract)
      // Contract verify: ecrecover(messageId, ethSig) == msg.sender
      // ĐÂY LÀ KEY QUAN TRỌNG NHẤT — xác định danh tính relayer

      "local_contract": "0x4c1c27b3...",
      // Địa chỉ CrossChainGateway trên chain LOCAL (Chain A)
      // Observer gọi receiveMessage() / processConfirmation() vào đây

      "remote_contract": "0xac744eBB..."
      // Địa chỉ CrossChainGateway trên chain REMOTE (Chain B)
      // Observer SUBSCRIBE events (MessageSent, MessageReceived) từ đây
    },
    {
      "name": "Chain B1",
      // Có thể relay NHIỀU channels cùng lúc
      // Mỗi channel có contract pair riêng (local + remote)
      // ...cấu hình tương tự...
    }
  ]
}
```

---

## Phân biệt các key

| Key | Mục đích | Giống/khác giữa observers? |
|-----|----------|---------------------------|
| `private_key` | Gửi transaction lên chain local | **GIỐNG** — cùng 1 wallet gửi tx |
| `parent_address` | Wallet address (từ private_key) | **KHÁC** — mỗi observer 1 wallet |
| `eth_private_key` | Ký messageId (ecrecover on-chain) | **KHÁC** — danh tính relayer |

### Tại sao parent_address khác nhau?

Mỗi observer dùng `parent_address` khác nhau trên chain local. Đây là địa chỉ mà contract `CrossChainGateway` kiểm tra khi gọi `receiveMessage()`:

```solidity
// Trong contract:
modifier onlyRelayer() {
    require(isRelayer[msg.sender], "Not a relayer");
    _;
}
```

→ `parent_address` phải nằm trong `relayerList` của contract.

### Tại sao eth_private_key khác nhau?

Contract dùng `ecrecover` để verify chữ ký ETH:

```solidity
// ecrecover(prefixedHash, v, r, s) == msg.sender
```

→ Mỗi observer ký bằng key riêng → contract biết **ai** đã approve.

---

## Bảng tổng hợp 3 Observer hiện tại

### Channel "Chain B" (A ↔ B)

| | Observer-1 | Observer-2 | Observer-3 |
|---|-----------|-----------|-----------|
| **Port** | 4900 | 4901 | 4902 |
| **parent_address** | `0xdbA14...B42f` | `0x3f2D9...281c` | `0xf8BC1...c75` |
| **eth_private_key** | `05cd9f...` | `9dbce7...` | `c87176...` |
| **local_contract** | `0x4c1c27...` | `0x4c1c27...` | `0x4c1c27...` |
| **remote_contract** | `0xac744e...` | `0xac744e...` | `0xac744e...` |

> `local_contract` và `remote_contract` **GIỐNG NHAU** — vì tất cả relay cùng 1 channel.

### Channel "Chain B1" (A ↔ B, contract pair khác)

| | Observer-1 | Observer-2 | Observer-3 |
|---|-----------|-----------|-----------|
| **parent_address** | `0xc4D57...31f3` | `0x54BEa...9B79` | `0xdbA14...B42f` |
| **eth_private_key** | `c1cb71...` | `450549...` | `05cd9f...` |
| **local_contract** | `0x29D9A1...` | `0x29D9A1...` | `0xeFFC60...` |
| **remote_contract** | `0xd34203...` | `0xd34203...` | `0x37175...` |

---

## Cách chạy

### Sử dụng `./run.sh`

```bash
cd /home/abc/nhat/consensus-chain/mtn-simple-2025/cmd/observer
./run.sh
```

Script này:

1. **Xóa session tmux cũ** `observer` (nếu có)
2. **Xóa log cũ** (`rm -rf logs`)
3. **Tạo tmux session** mới với 3 pane
4. **Chạy 2-3 observer instances** song song:

```
┌─────────────────────────┬─────────────────────────┐
│ Observer-1 (port 4900)  │ Observer-2 (port 4901)  │
│ config-client-tcp-1.json│ config-client-tcp-2.json │
├─────────────────────────┤                         │
│ Observer-3 (port 4902)  │                         │
│ (hiện tại bị comment)   │                         │
└─────────────────────────┴─────────────────────────┘
```

### Xem log

```bash
# Attach vào tmux session
tmux attach -t observer

# Hoặc xem log file trực tiếp
tail -f logs/log-1/epoch_0/Observer-1.log
tail -f logs/log-2/epoch_0/Observer-2.log
```

### Thêm observer mới

1. **Tạo config mới**: copy `config-client-tcp-1.json` → `config-client-tcp-4.json`
2. **Đổi các trường**:
   - `connection_address`: port mới (vd: `4903`)
   - `parent_address`: wallet address mới
   - `eth_private_key`: ETH key mới (khác tất cả observer khác)
   - `log_path`: `./logs/log-4`
3. **Add relayer trên contract**: gọi `addRelayer(eth_address)` cho cả `local_contract`
4. **Thêm vào `run.sh`**: thêm dòng chạy observer mới
5. **Restart**: `./run.sh`

> **Threshold tự động**: contract tính `ceil(relayerList.length * 2/3)`.
> Thêm relayer → threshold tự tăng.

---

## Flow xử lý message

```
1. User gọi sendMessage() trên Chain B (remote)
   → Chain B emit event MessageSent(nonce=0, sourceId=2, destId=1, ...)

2. Observer-1 lắng nghe remote_contract → nhận MessageSent
   ├── Verify: sourceNationId == remoteCfg.NationId (2) ✅
   ├── Verify: destNationId == localCfg.NationId (1) ✅
   ├── Verify: verifyRemoteTxReceipt (tx receipt thật trên chain B) ✅
   ├── Ký messageId bằng eth_private_key
   └── Gọi receiveMessage(packet, ethSig) trên local_contract (Chain A)

3. Observer-2 cũng nhận MessageSent → làm tương tự
   → Contract: approvalCount[messageId] = 2 ≥ threshold(2) → EXECUTE!

4. Chain A execute message → emit MessageReceived(nonce=0, status, ...)

5. Observer-1 lắng nghe remote_contract trên Chain B → nhận MessageReceived
   ├── Verify nationId ✅
   ├── Ký bằng eth_private_key  
   └── Gọi processConfirmation() trên local_contract (Chain A)

6. Observer-2 cũng submit → threshold đạt → Chain A xử lý confirmation
```

---

## Bảo mật — 6 lớp bảo vệ

| # | Lớp | Mô tả |
|---|-----|-------|
| 1 | **Contract address filter** | Chỉ subscribe events từ `remote_contract` cụ thể |
| 2 | **NationId verify** | SourceNationId + DestNationId phải khớp config |
| 3 | **verifyRemoteTxReceipt** | Verify tx receipt thật trên remote chain |
| 4 | **ETH Signature (ecrecover)** | Contract verify danh tính relayer |
| 5 | **Multi-sig threshold** | Cần ≥ 2/3 relayer approve |
| 6 | **Nonce ordering** | `packet.nonce == inboundNonce` on-chain |
