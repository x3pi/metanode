# ADR: bốn quyết định chốt cho node thực thi Ethereum-only (2026-10-06)

Trạng thái: **ĐÃ CHỐT** (user ủy quyền "phân tích chốt giúp"). Dùng cùng `note/plan_eth_only_production_readiness.md`. Mọi dẫn chứng là code đã đọc ngày 2026-10-06; chỗ chưa kiểm ghi "CHƯA KIỂM".

## D1. Giao dịch vận hành validator: đi bằng envelope Ethereum secp256k1 — không cần kênh riêng
**Phân tích**
- Contract validator là handler Go native (`pkg/blockchain/tx_processor/true_block_stm.go:1345` → `validation_transaction.go`), phân quyền chỉ bằng `tx.FromAddress()` (`handleRegisterValidator`: `GetValidator(tx.FromAddress())`, `CreateRegisterWithKeys(tx.FromAddress(), ...)`). Không có bước nào đòi người gửi giữ khóa BLS. ⇒ tài khoản ECDSA gọi `registerValidator/delegate/...` qua envelope chuẩn **chạy được**, giống ví thường (đúng cơ chế Ethereum-only).
- Hai công cụ bị chặn hôm nay **vốn đã hỏng từ trước**, không phải hồi quy của ta: `cmd/simple_chain/tool_register.go` khai báo ABI riêng 8 tham số rồi `Pack` 9 giá trị, trong khi handler đòi **13** tham số (`len(args) != 13`, kèm `networkKey, hostname, authorityKey, protocolKey`); `cmd/tool/fast_setup` cũng ký BLS device key. Cả hai là công cụ dev "node-4", không nằm trong deploy (grep `deploy/`, `ci.sh`, `scripts/` không gọi `-tool-register-validator`).
- Cách đưa validator vào production hiện là **genesis** (`deploy/systemd/gen_validator_entry.py` → `genesis.json`), không phải giao dịch.
**Quyết định**
1. Tx vận hành (nếu cần sau genesis) = envelope secp của tài khoản vận hành. Không thêm kênh nội bộ/đường BLS công khai.
2. **Xóa** `tool_register.go` (+ cờ `-tool-register-validator` ở `main.go:36,75`) và `cmd/tool/fast_setup`; nếu sau này cần công cụ thì viết mới bằng go-ethereum `SignTx` + 13 tham số đúng ABI.
3. P0-1 hạ từ "luồng bị hỏng" xuống **dọn tool chết** (P1-1) cộng thêm **mục kiểm bảo mật mới (P0-1b)**: handler `registerValidator` không kiểm sở hữu `authorityKey` và không thấy ngưỡng stake (`minSelfDelegation` do người gọi tự đặt, ví dụ 0). **CHƯA KIỂM:** committee mỗi epoch có lọc theo stake không. Nếu không lọc ⇒ bất kỳ tài khoản nào đăng ký được vào tập validator ⇒ phải đóng (chỉ governance/genesis, hoặc ngưỡng stake tối thiểu) trước production.

## D2. Mô hình phí v1: giá hiệu dụng chuẩn Ethereum trên nền phí phẳng
**Phân tích**
- Hiện tại với tx type 2/3/4 người dùng bị trừ `gas × maxFeePerGas` (tip bị bỏ qua, **không hoàn** phần chênh): `EffectiveGasPrice()` trả `GasFeeCap()` (`pkg/transaction/transaction.go:1063-1069`), gas fee = `gas × EffectiveGasPrice()` (`native_fast_path.go:~201`, `vm_processor_state.go:554`). Khác hẳn Ethereum (trả `min(maxFee, base+tip)`).
- RPC gợi ý `maxPriorityFeePerGas` = 1e8 (`backend.go:123`) trong khi `baseFee`/`gasPrice` = 1e5 ⇒ ví mặc định đặt `maxFee ≈ 2×1e5 + 1e8` ⇒ **trả gấp ~1000 lần phí phẳng** và không được hoàn.
**Quyết định**
1. `F = MINIMUM_BASE_FEE` (phí phẳng, hằng tất định). Giá hiệu dụng: Legacy/2930 = `gasPrice`; type 2/3/4 = `min(maxFeePerGas, F + maxPriorityFeePerGas)`.
2. Tách hai hàm trong `pkg/transaction`: **`GasPriceCap()`** (= giá trần: legacy `gasPrice`, 1559 `maxFeePerGas`) dùng cho admission (`≥ F`) và kiểm đủ tiền `gasLimit × cap + value` (như geth); **`EffectiveGasPrice()`** (= công thức trên) dùng cho trừ phí, receipt `effectiveGasPrice`, `eth_getTransactionByHash.gasPrice`. Một nguồn duy nhất cho F.
3. RPC: `eth_gasPrice = F`, `block.baseFeePerGas = F`, `eth_feeHistory` khớp, `eth_maxPriorityFeePerGas = 0`. Nếu thư viện nào (ethers/viem/web3j/foundry) từ chối tip 0 ⇒ dùng 1 wei, ghi lại. Blob fee giữ nguyên (đã tách riêng).
4. Phí thực trả (`gas × effective`) vẫn chuyển cho leader như hiện tại (không đốt) — ghi rõ là đơn giản hóa v1.
5. Thay đổi này làm đổi **kết quả chuyển trạng thái** ⇒ phát hành **cùng đợt cutover có wipe** (mục D3), mọi validator đồng thời; tất định (không phụ thuộc thời gian/trạng thái cục bộ).
**Chấp nhận:** test số dư theo từng type (0/1/2/3/4) với `maxFee` lớn hơn F: bị trừ đúng `gas × min(maxFee, F+tip)`; ethers/viem mặc định (không đặt phí tay) trả ≈ F.

## D3. Đổi hash (W4): LÀM, trước khi ra production, trong cùng đợt cutover — không hoãn
**Phân tích**
- Thiết kế hiện tại giữ ánh xạ `ethHash → metaHash` **bền** cho MỌI giao dịch (`blockchain.go:693-697`, khóa `ethHashMapBlsHashPrefix + ethHash.Hex()`, ~100+ byte/khóa). Với 1.000 tx/s ≈ 86 triệu khóa/ngày (~10 GB/ngày), 7.000 tx/s gấp bảy lần; **chưa thấy cơ chế prune/TTL cho khóa bền** (CHƯA KIỂM kỹ — TTL hiện chỉ cho cache RAM `mappingCacheTTL`). Đây là chi phí tăng vô hạn theo số giao dịch.
- Hash kép còn gây: hồi quy ghi-trước-khi-nhận (P0-3), mọi RPC phải dò ngược, `input` thô không có (bọc `CallData`), xác thực chữ ký trên bản tái mã hóa.
- Chưa có dữ liệu production nên chi phí đổi bây giờ thấp nhất; sau khi ra mắt, đổi hash là hard fork. Cutover chain 991 vốn đã đòi wipe + redeploy (runbook có sẵn).
**Quyết định**
1. W4 là **chặn ra mắt (làm một lần duy nhất)**, gộp vào "Cutover bundle v1" = chainId 991 + payload proto/hash (W4) + ngữ nghĩa phí (D2). Không phát hành định dạng tx trước cutover.
2. Thiết kế đã chốt (`plan_eth_native_eip2718.md` G1): `bytes raw_envelope` (tag mới) ở **3 bản `.proto`**; `hash = keccak256(raw_envelope)` giống hệt ở Go (`Transaction.Hash`) và Rust (`consensus/metanode/src/types/tx_hash.rs` + `queue.rs`, `block_sending.rs`, `executor.rs`, `network/rpc.rs`, `tx_socket_server.rs`, `peer_rpc/server.rs`, `tx_recovery.rs`); xác thực chữ ký trên bytes gốc; lưu `input` thô; bỏ `SetEthHashMapblsHash`/`AddTxToCache` ánh xạ. **Tx hệ thống BLS của node (không có envelope) giữ hash proto như cũ** (phân nhánh theo `raw_envelope` rỗng/không rỗng) — quy ước này phải viết vào ADR/code comment và test chéo Go↔Rust (sai = fork).
3. Thứ tự: P0-2…P0-7 + P1 nền tảng → W4 + D2 → kiểm chứng đa validator (P0-6) trên bản đã gộp → cutover. Trong lúc chờ W4, vẫn sửa P0-3 (chỉ ghi ánh xạ **sau** khi pool nhận) vì rẻ và cụm hiện tại đang chạy.
4. "Derive key": **bỏ khỏi phạm vi** (không còn device key; chưa có yêu cầu thật). Nếu về sau cần khóa con theo ứng dụng thì làm bằng contract/precompile, không phải trường tx.

## D4. Tag/nhánh legacy: nên đẩy lên remote (chờ user xác nhận khi thực thi)
- `legacy-simple-chain-bls-v1` và `legacy/simple-chain-bls` (cùng `a25cb135`) hiện chỉ ở local; nhánh `dev` đã xóa dần code legacy nên đây là đường lùi duy nhất, mất máy là mất mốc (memory ghi từng mất commit do squash-merge; tag độc lập với việc đó).
- Quyết định: **đẩy** `git push origin legacy-simple-chain-bls-v1 legacy/simple-chain-bls`. Đây là thao tác công khai nên chỉ chạy khi user nói "đẩy"; không đẩy `dev`.

## Tác động lên kế hoạch
`note/plan_eth_only_production_readiness.md`: P0-1 → dọn tool chết (P1-1) + P0-1b (kiểm quyền `registerValidator`); P0-7 → áp D2; P2-1 → nâng thành **P0-8 chặn ra mắt** theo D3; mục 7 "còn mở" → đã chốt.
