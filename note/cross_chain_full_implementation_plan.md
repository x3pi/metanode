# Kế hoạch triển khai đầy đủ Root Anchor Cross-Chain Bridge — để chạy thực tế

Tài liệu này là **danh sách việc cần làm theo thứ tự**, viết cho một agent/dev mới hoàn
toàn chưa có ngữ cảnh, để đưa cầu nối liên chuỗi từ trạng thái hiện tại (lớp xác minh
mật mã đã xong và test kỹ, nhưng **chưa từng di chuyển giá trị thật**) tới trạng thái
chạy thật trên testnet/mainnet. Nó không lặp lại chi tiết đã có ở
`note/cross_chain_production_readiness_plan.md` (tài liệu tra cứu chi tiết, cập nhật liên
tục) — mà sắp xếp lại toàn bộ thành 1 trình tự thực thi được, kèm quy tắc bắt buộc đọc
trước khi bắt đầu.

**Trước khi làm bất cứ task nào:** đọc mục "How to work" trong
`note/cross_chain_production_readiness_plan.md` (nguyên tắc Zero-Fork, bar verify bắt buộc
`go build && go vet && go test` + `gofmt -l`, quy trình PR qua branch từ `dev`, không tự
merge, triết lý test dùng crypto/production code path thật chứ không mock). Đây không phải
gợi ý — đây là lý do 4 lỗ hổng nghiêm trọng đã được tìm thấy và vá thành công trong dự án
này, đừng bỏ qua để tiết kiệm thời gian.

---

## Trạng thái hiện tại (đọc để hiểu vì sao thứ tự dưới đây là bắt buộc)

| Lớp | Trạng thái |
| :--- | :--- |
| BLS/Merkle verification, anti-fraud (attest/claim/refund/governance) | ✅ Thật, đã test kỹ (Milestones A-I + Phase 0/0.5) |
| **Di chuyển giá trị thật (native coin + custom asset)** | 🔴 **Chưa làm** — xem Task 1, đây là việc lớn nhất còn lại |
| Genesis ceremony nhiều tổ chức | ✅ Có runbook + tooling, có 1 lỗ hổng trung bình cần vá (Task 2) |
| Chạy thật nhiều máy (T2) | 🔴 Chưa làm |
| Audit bảo mật độc lập (P5) | 🔴 Chưa làm, không thể tự làm — cần bên ngoài |
| Dashboard giám sát | 🟡 Đã có `metrics_dashboard.go` + `cross_chain_dashboard`, chưa chạy trên hạ tầng thật |

---

## Task 1 — 🔴 ƯU TIÊN CAO NHẤT: Nối lớp verify vào giá trị thật

**Đọc chi tiết đầy đủ ở Phase 0.6 trong `cross_chain_production_readiness_plan.md` trước
khi code** — phần dưới đây chỉ tóm tắt trình tự thực thi.

Tại sao đây là ưu tiên #1: mọi thứ khác trong tài liệu này (T2 testnet, audit P5, rollout
staged) đều giả định cầu nối *di chuyển được giá trị*, chỉ cần kiểm chứng/siết chặt thêm.
Thực tế: `outbound()`/`claimMessage()`/`verifyAndExecute()`/`refund()`/
`claimDeadChainBalance()` **chỉ sửa đổi struct Go nội bộ** (`GlobalSupplyLedger`,
`AssetRegistryEngine`), không bao giờ gọi `AccountStateDB.AddBalance`/`SubBalance` hay
`VmProcessor.ProcessNativeMintBurn` — hàm đã tồn tại sẵn, đúng như kiến trúc mục 2.4 mô tả,
nhưng **0 nơi gọi nó** trong toàn bộ codebase hiện tại.

### 1.1 Native coin (ưu tiên trước — đơn giản hơn, dùng primitive có sẵn)

- [ ] `outbound()`: gọi `VmProcessor.ProcessNativeMintBurn(ctx, tx, mvmE, 1 /*burn*/)` cho
      `params.Value` từ `tx.FromAddress()` — fail-closed, revert cả giao dịch nếu burn thất
      bại. Không được emit message nếu tiền chưa thực sự bị trừ.
- [ ] **Quyết định thiết kế cần chốt trước khi code** (hỏi task owner nếu chưa rõ, đừng đoán
      — đúng nguyên tắc "How to work"): người nhận ở đích là `message.Sender` (cùng địa chỉ
      2 chain) hay cần thêm field `Recipient` riêng (giống `AssetRegistryEngine.LockAndBridgeAsset`
      đã có sẵn `recipient` tách biệt `sender`)? Quyết định này ảnh hưởng ABI của
      `outbound()`/`claimMessage()`/`verifyAndExecute()` — làm 1 lần, tránh phải đổi ABI 2 lần.
- [ ] `ClaimMessage()` + `VerifyAndExecute()`: sau khi mọi bước verify hiện có PASS (giữ
      nguyên toàn bộ, đây là thay đổi thuần cộng thêm), gọi
      `ProcessNativeMintBurn(ctx, tx, mvmE, 0 /*mint*/)` credit `message.Value` cho người
      nhận thật.
- [ ] `Refund()`: gọi mint credit lại cho `message.Sender` trên chain nguồn.
- [ ] `ClaimDeadChainBalance()`: gọi mint credit cho `account` (param đã tồn tại, đã verify
      qua Merkle proof, chỉ chưa được dùng).
- [ ] **Bài test bắt buộc (không có test này = chưa Done):** test end-to-end thật (không
      phải kiểu in-process của `gateway_test.go` — đúng kiểu test đã che giấu lỗ hổng này) —
      khởi động 2 chain thật + Root Anchor thật, gọi `outbound()` qua RPC thật, xác nhận
      `eth_getBalance` người gửi **giảm** đúng số lượng; relay + `claimMessage()` qua RPC
      thật, xác nhận `eth_getBalance` người nhận **tăng** đúng số lượng, trên cả 2 chain thật.

### 1.2 Custom asset / wrapped token (sau 1.1 — phức tạp hơn, cần gọi contract thật)

- [ ] `AssetRegistryEngine.LockAndBridgeAsset`/`ReceiveAndSettleAsset`: khác native coin (có
      primitive `ProcessNativeMintBurn` sẵn dùng ngay), token ERC-20/wrapped cần **gọi thật
      vào contract token** qua cơ chế EVM/MVM `Call` hiện có (không phải hàm mới) — lock =
      gọi `transferFrom`/`burn` thật trên contract nguồn, mint = gọi `mint` thật trên
      contract wrapped ở đích. Xem cách `vm_processor` gọi `Call` cho giao dịch EVM thường
      làm mẫu.
- [ ] Test end-to-end tương tự 1.1 nhưng verify qua `eth_call balanceOf()` trên contract
      ERC-20 thật, không phải `eth_getBalance`.

### 1.3 CONTRACT_CALL — gọi contract tuỳ ý ở chain đích (sau 1.1, có thể song song 1.2)

- [ ] Theo kiến trúc mục 2.6.5: người gửi khoá 1 khoản "gas liên chuỗi" bằng native coin lúc
      `outbound()` (dùng cơ chế khoá đã làm ở 1.1). `ClaimMessage()` khi `message.Target !=
      address(0)` thực sự gọi `Target` với `Payload` qua EVM/MVM `Call`, gas cap = khoản đã
      khoá quy đổi, phần dư hoàn lại (message hoàn tiền dùng chung cơ chế Refund), phần đã
      dùng bị đốt thật. **Không áp dụng "free gas" cho nhánh này** — đây là vector DoS đã
      được tài liệu kiến trúc cảnh báo rõ.
- [ ] Test: 1 message CONTRACT_CALL thật gọi 1 contract thử nghiệm ở chain đích, xác nhận
      state của contract đó thay đổi đúng ý, và 1 test riêng xác nhận payload tốn gas vượt
      mức khoá bị revert đúng cách (không cho thực thi vô hạn).

### 1.4 Relayer tip — cho phép rút thành tiền thật

- [ ] `RelayerBalances[relayer]` hiện chỉ là số cộng dồn nội bộ, không có đường rút. Thêm
      method `withdrawRelayerTip()` (hoặc gộp vào lần credit luôn thay vì tích luỹ) gọi
      `ProcessNativeMintBurn(..., 0)` credit thật cho relayer.

---

## Task 2 — 🟡 Vá lỗ hổng front-run `bootstrapFoundingChains` (trung bình, không mất giá trị)

Chi tiết đầy đủ ở mục hardening item mới nhất trong `cross_chain_production_readiness_plan.md`
(gần cuối Phase 1, mục về `bootstrapFoundingChains`). Tóm tắt: hàm không kiểm tra người gửi
giao dịch, tạo cửa sổ front-run khi làm lễ genesis nếu `founding_entry.json` bị công khai
trước khi coordinator gửi tx. Vá thật: giới hạn người gọi vào 1 địa chỉ coordinator cam kết
trước (out-of-band, cùng kiểu `genesis_digest.txt`), hoặc yêu cầu khai báo trước tập chain
ID kỳ vọng. Không chặn Task 1, có thể làm song song hoặc sau.

---

## Task 3 — Các mục còn mở ở Phase 1 (`cross_chain_production_readiness_plan.md`)

Làm theo đúng thứ tự đã ghi trong tài liệu đó (không lặp lại chi tiết ở đây):
1. Epoch catch-up cho `ApplyCommitteeUpdate` (chỉ nhận epoch tuần tự, cần đường phục hồi có
   giới hạn hoặc alert vận hành tường minh).
3. Rà soát đối kháng sâu Milestone F (`CommitteeAttestationWorker`)/I (`RelayerDaemon`) —
   chưa được soi ở mức "what would make this test pass without the real thing being true".
4. Chốt quyết định: `propose()` không giới hạn có chủ ý hay thiếu sót — nếu cố ý, cần quyết
   định có giới hạn spam/storage-growth trước mainnet không.
5. Đo chi phí thật của `accountTreeRootAtBlock` (duyệt toàn bộ account set mỗi epoch) trên
   dữ liệu thật ở Task 4 (T2), không đoán trước khi có số liệu.

---

## Task 4 — T2: chạy thật trên nhiều máy độc lập

Dùng `note/production_deployment_guide.md` mục 5 (Root Anchor nhiều tổ chức) hoặc mục 4
(Ansible, 1 tổ chức nhiều chain) — **trên máy/VM riêng biệt, không chia sẻ với tải nặng khác**
(bài học thật đã ghi lại: 1 lần treo devnet do máy chia sẻ, không phải lỗi code). Đo số liệu
thật cho Phase 2's T2 (mục trong readiness plan): chi phí BLS/commit thật, thông lượng ở
500/2000/4000 msg/batch, và số liệu Task 3 mục 5 (account-tree-root cost). **Chỉ có ý nghĩa
sau khi Task 1 xong** — chạy T2 trước Task 1 chỉ đo được lớp verify, không đo được gì về di
chuyển giá trị thật (đúng cái T2 cần chứng minh).

---

## Task 5 — Audit bảo mật độc lập (P5)

Không phải việc agent tự làm được — cần thuê bên ngoài. Việc agent CÓ THỂ làm: chuẩn bị hồ
sơ đầy đủ cho auditor (toàn bộ luồng verify BLS + 2 loại Merkle proof + replay + double-mint
+ origin-sender + governance + **toàn bộ phần mới ở Task 1**, vì Task 1 là code mới, chưa
qua review nội bộ nào — áp dụng đúng "base rate" đã nêu ở đầu tài liệu readiness plan: code
mới về crypto/fund-safety luôn cần ít nhất 1-2 vòng review nội bộ trước khi đưa cho auditor
ngoài). Không bỏ qua giai đoạn này để tiết kiệm thời gian — đây là cổng cứng trước mainnet.

---

## Task 6 — Rollout theo giai đoạn (mainnet thật)

Theo đúng Phase 4 của readiness plan (Stage 1 messages-only → Stage 2 value nhỏ có trần →
Stage 3 bỏ trần), **chỉ sau khi Task 1 + Task 4 + Task 5 đều xong**. Trước đây tài liệu
readiness plan mô tả Stage 2 như thể chỉ cần "bắt đầu với trần thấp" — thực tế cần Task 1
xong trước, nếu không Stage 2 vẫn đang test 1 tính năng chưa tồn tại.

---

## Việc vận hành cần làm song song, không chặn code (đã ghi trong `production_deployment_guide.md`)

- Thay khoá devnet đóng cứng (`RELAYER_KEY`/`dev_priv_key` trong `deploy/systemd/`) trước
  khi dùng cho bất kỳ mạng có người ngoài truy cập được.
- Bật giám sát (Health/Resource Monitor, Block Hash Checker, Telegram alert) trước khi chạy
  Task 4, không phải sau sự cố đầu tiên.

---

## Tóm tắt thứ tự bắt buộc

```
Task 1 (di chuyển giá trị thật)  ──┬──▶ Task 4 (T2 nhiều máy) ──▶ Task 5 (audit) ──▶ Task 6 (mainnet)
Task 2 (vá front-run bootstrap)  ──┤
Task 3 (Phase 1 còn mở)          ──┘
```

Task 2 và Task 3 không phụ thuộc Task 1 và có thể làm song song/trước. Task 4-6 phải theo
thứ tự, không thể nhảy cóc — mỗi cổng đều là cổng cứng theo đúng nguyên tắc "không có khái
niệm code xong test sau" đã áp dụng xuyên suốt dự án này.
