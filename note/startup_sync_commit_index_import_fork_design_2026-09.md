# Thiết kế: đóng lỗ hổng "STARTUP-SYNC import CommitIndex thô từ 1 peer" gây fork thật

**Bối cảnh**: phát hiện khi đào sâu tiếp mục 17 (Phương án A) — sau khi vá xong
bug "STARTUP-SYNC vòng lặp vô hạn" (đã fix, đã deploy, xem mục 8), cluster test
tiếp tục chạy và lộ ra một **fork thật, đang diễn ra trực tiếp**, không tự hồi
phục, khác hẳn và sâu hơn mọi giả thuyết trước đó trong mục 17. Tài liệu này là
phân tích kiến trúc đầy đủ theo yêu cầu của user ("xem xét lại tổng thể... thử
tìm hiểu xem Sui hay blockchain khác có thiết kế nào tốt cho vấn đề này
không"), không phải chỉ vá 1 dòng code.

Đọc cùng với `note/consensus_local_dag_trust_gap_design_2026-09.md` (Phương án
A gốc) và `note/known_bugs/commit_info_sync_fork.md` +
`note/known_bugs/startup_sync_persistence_fork.md` — đây là **bug thứ 3** cùng
một họ ("STARTUP-SYNC bỏ sót/nhập sai một mẩu metadata mà đường consensus bình
thường xử lý đúng, gây fork sau khi node bắt kịp"). Xem mục 7 để biết vì sao
lần này cần một sửa **kiến trúc**, không phải vá triệu chứng thứ 3.

---

## 1. Tóm tắt vấn đề bằng 1 câu

`STARTUP-SYNC` (đường catch-up khi Validator fetch block trực tiếp từ 1 peer
để phục hồi nhanh) **gán thẳng trường `CommitIndex` lấy từ block header của
peer** làm `lastHandledCommitIndex` của chính mình — nhưng `CommitIndex` là sổ
sách đếm **nội bộ, không ký, không quorum-verify**, không đảm bảo giống nhau
giữa 2 validator trung thực cho cùng 1 nội dung — nên sau khi "mượn" xong, node
tính sai `next_expected_index` và dispatch nhầm sang một commit khác hẳn trong
DAG của chính nó, tạo ra block khác nội dung ở cùng 1 GlobalExecIndex — một
fork thật.

---

## 2. Bằng chứng trực tiếp (live-reproduced trên local 232, không suy đoán)

### 2.1. So sánh nội dung block thật giữa 2 phe bị fork

Cùng 1 `globalExecIndex=490`, cùng `parentHash`, cùng `leaderAddress`:

| Trường | node-0 (+node-1) | node-2 (+node-3) |
|---|---|---|
| `transactions` | `[]` (rỗng) | `[1 tx]` (hash `0xf8d6d2d9...`) |
| `commitIndex` | `0x241e` (9246) | `0x24f2` (9458) — **lệch 212** |
| `aggregateSignature` | `0x` (rỗng!) | chữ ký BLS hợp lệ |
| `stateRoot` | `0x721ed0ac...` | `0x66d3a52e...` |

Node-0 và node-2 **đều tin đây là "cùng 1 block"** (cùng GEI, cùng cha) nhưng
nội dung thực sự khác nhau hoàn toàn — không phải sai lệch thoáng qua, đã xác
minh lặp lại nhiều lần ổn định.

### 2.2. Nhị phân tìm điểm rẽ nhánh đầu tiên

Block #489 khớp tuyệt đối giữa mọi node. Block **#490 là điểm rẽ nhánh đầu
tiên** — trùng khớp chính xác với thời điểm node-0 **vừa hoàn tất STARTUP-SYNC**
(catch-up 404→489 sau khi tự sửa xong bug vòng lặp vô hạn ở mục 8) và chuyển
sang dispatch bình thường qua consensus.

### 2.3. Đoạn code gây lỗi (Go, `execution/executor/unix_socket_handler_sync.go:458-470`)

```go
// CRITICAL FIX: Restore lastHandledCommitIndex from the synced block!
// This prevents Go from double-executing these commits when Rust resumes consensus.
if header.CommitIndex() > 0 {
    commitIdx32 := uint32(header.CommitIndex())
    currentEpoch := header.Epoch()
    lastEpoch := storage.GetLastHandledCommitEpoch()

    if currentEpoch > lastEpoch || (currentEpoch == lastEpoch && commitIdx32 > storage.GetLastHandledCommitIndex()) {
        storage.ForceSetLastHandledCommitIndex(commitIdx32)   // <-- lấy CommitIndex TỪ BLOCK CỦA PEER
        storage.UpdateLastHandledCommitEpoch(currentEpoch)
    }
}
```

`header.CommitIndex()` ở đây là trường **nhúng trong block do PEER gửi qua**
`fetch_blocks_from_peer` (STARTUP-SYNC fetch trực tiếp từ 1 peer, không qua
CommitSyncer/DIGEST-GATE). Giá trị này phản ánh **bộ đếm DAG nội bộ của
PEER**, không phải của node đang catch-up.

Sau đó, `startup_sync.rs:526` dùng giá trị này để tính bước tiếp theo:

```rust
commit_processor.update_next_expected_index(new_handled + 1);
```

`new_handled` tới từ `barrier_client.get_last_handled_commit_index()` — tức
chính giá trị Go vừa "mượn" từ peer ở trên. Node-0 sau đó dispatch **commit
nằm ở đúng index đó trong DAG của CHÍNH NÓ** — một commit hoàn toàn khác về
nội dung (vì DAG 2 node có thể đánh số khác nhau cho cùng nội dung, xem mục
2.4) — ra một block khác hẳn cho cùng GEI.

### 2.4. Vì sao `CommitIndex` được phép khác nhau giữa 2 validator trung thực — bằng chứng NGAY TRONG CHÍNH CODEBASE

`fork_guard.rs` (dòng 13-25, sửa từ 2026-09-05) đã tự ghi rõ:

> "Two honest validators can commit the identical GEI/state via different
> local commit-round numbers (e.g. one observes an extra empty/skipped round
> the other doesn't), so their raw bytes can legitimately differ while
> `state_root` (and `block_hash`) match exactly."

Đây chính là lý do LAYER-6 **cố tình không** so raw bytes/CommitIndex, chỉ so
`block_hash`+`state_root`. Nhưng `unix_socket_handler_sync.go` (STARTUP-SYNC,
phía Go) lại **không biết luật này** — nó coi `CommitIndex` như một giá trị
phổ quát, dùng chung được giữa các node. Hai chỗ trong CÙNG hệ thống đang giả
định 2 điều mâu thuẫn nhau về cùng 1 trường dữ liệu.

### 2.5. Vì sao LAYER-6 không tự chặn được lỗi này

`fork_guard.rs::runtime_fork_guard` mỗi lần re-verify chỉ hỏi **1 peer** (xoay
vòng peer ở mỗi lần retry để tránh hỏi lại đúng 1 nguồn — nhưng vẫn là 1 peer
mỗi lần, không phải đa số). Với fork 2-phe-thật (node0+1 vs node2+3), khi vòng
xoay tình cờ hỏi trúng peer **cùng phe** với mình, nó báo "NOW MATCHES" — dù
vẫn đang fork với phe kia. Quan sát trực tiếp: dao động phát-hiện/tự-huỷ suốt
400+ block liên tục mà không bao giờ đạt "CONFIRMED FORK" (3/3 thất bại) lẫn
"ổn định khớp" — đúng như dự đoán từ cách logic hoạt động.

---

## 3. Chuỗi nguyên nhân đầy đủ (root cause chain, không phải 1 bug đơn lẻ)

```
[Fork gốc, chưa rõ nguyên nhân sâu nhất — xem mục 9]
        │  node-0 tự tính ra block #397 sai (LAYER-6 bắt đúng, abort() đúng thiết kế)
        ▼
[BUG #1 — ĐÃ SỬA] STARTUP-SYNC vòng lặp vô hạn khi resync sau abort
        │  SYNC-FORK-GUARD (Go) rollback counter về 404, nhưng Rust's `local_block`
        │  không được báo lại → fetch lại đúng range cũ mãi mãi → node treo vĩnh viễn,
        │  kéo cả cluster mất quorum (khi có thêm 1 node khác đang down)
        ▼
[BUG #2 — CHƯA SỬA, tài liệu này] Sau khi bug #1 được vá, STARTUP-SYNC
        │  hoàn tất fetch 404→489 từ peer — NHƯNG khi phục hồi
        │  lastHandledCommitIndex, mượn nhầm số đếm nội bộ CỦA PEER
        ▼
node-0 tính next_expected_index SAI → dispatch nhầm commit trong DAG
của chính nó → block #490 nội dung khác hẳn mạng lưới → FORK THẬT
        ▼
[BUG #3 — thiết kế yếu, cùng họ] LAYER-6 chỉ so 1 peer/lần → không bao giờ
kết luận dứt điểm khi fork là fork-2-phe-thật, không phải 1 peer lỗi thoáng qua
```

Cả 3 đều là các biểu hiện khác nhau của **cùng một lỗ hổng kiến trúc**: đường
STARTUP-SYNC (vốn thiết kế cho SyncOnly — node không có DAG riêng, "tin 1
peer" là hợp lý) đang bị **Validator** (có DAG riêng, cần bảo toàn tính nhất
quán nội bộ) dùng lại nguyên xi, không điều chỉnh cho đúng bối cảnh khác nhau
giữa 2 loại node.

---

## 4. Đối chiếu thiết kế thật: Sui và Aptos xử lý lớp vấn đề này thế nào

*(Nghiên cứu trực tiếp từ tài liệu chính thức, không suy đoán — xem nguồn cuối mục)*

### 4.1. Sui (chính là gốc mà `consensus_core` trong repo này fork/mô phỏng theo)

- **Thứ tự commit deterministic tuyệt đối giữa mọi validator trung thực**:
  "Mysticeti takes Narwhal's DAG and determines the final execution order
  using deterministic rules... ensuring all validators reach the same
  conclusions." Với cùng 1 trạng thái DAG, mọi validator PHẢI suy ra cùng 1
  chuỗi commit, cùng index — không có khái niệm "mỗi node đếm một kiểu".
- **Đơn vị trao đổi khi đồng bộ/catch-up KHÔNG BAO GIỜ là một con số đếm thô
  từ 1 peer** — mà là **checkpoint**: chứa hash của checkpoint trước, danh
  sách digest giao dịch/hiệu ứng, và **chữ ký của một quorum validator**. Full
  node đồng bộ bằng cách xác minh chữ ký + xác minh hash-chain nối tiếp — không
  bao giờ "tin lời 1 peer nói con số này là X".
- Checkpoint được suy ra từ consensus commit bằng **1 luật deterministic mà
  bất kỳ validator nào cũng tự tính lại được độc lập** — không "mượn" giá trị
  của validator khác.

### 4.2. Aptos (cùng gốc học thuật HotStuff/DiemBFT, có ranh giới
consensus/execution rất rõ ràng)

- "Execution... output must be deterministic: honest validators that start
  from the same state and the same block must produce the same write set. **A
  supermajority of voting power (more than two-thirds) must agree on that
  result before the block commits**."
- State sync chỉ tin **LedgerInfo có quorum certificate (>2/3 stake ký)**,
  không bao giờ tin số đếm nội bộ chưa ký của 1 peer đơn lẻ.

### 4.3. Nguyên lý chung (2 hệ thống lớn, độc lập nhau, cùng hội tụ về 1 luật)

> **Đơn vị dùng để đồng bộ/catch-up phải là một artifact tự-xác-thực-được: hoặc
> (a) suy ra được bằng luật deterministic thuần từ nội dung, hoặc (b) có chữ
> ký/chứng thực quorum đi kèm. Không bao giờ được import thẳng một trường
> bookkeeping nội bộ, chưa ký, từ MỘT peer, rồi coi như của chính mình.**

Bug ở mục 2.3 vi phạm chính xác luật này: `header.CommitIndex()` không thuộc
cả 2 loại (a) hay (b) — nó không tự suy ra được từ nội dung (đã tự thừa nhận ở
mục 2.4 là node-local), và cũng không đi kèm chữ ký/quorum nào.

**Nguồn:**
- [Sui Consensus Docs](https://docs.sui.io/develop/sui-architecture/consensus)
- [Mysticeti paper (arXiv)](https://arxiv.org/pdf/2310.14821)
- [Sui Data Management](https://docs.sui.io/learn/architecture/sui-full-node-data)
- [Sui Checkpoint Verification](https://docs.sui.io/develop/sui-architecture/checkpoint-verification)
- [Aptos Execution](https://aptos.dev/network/blockchain/execution)
- [Aptos State Synchronization](https://aptos.dev/network/nodes/configure/state-sync)

---

## 5. Điều quan trọng: codebase này ĐÃ CÓ đúng công cụ cần dùng, chỉ chưa dùng nhất quán

Không cần phát minh cơ chế mới. Hệ thống đã có sẵn, đã kiểm chứng qua thực
chiến (mục 11-14 của `project_consensus_halt_not_guess_phuong_an_a`):

- **`CertifiedCommit`** (`meta-consensus/core/src/commit.rs`) — commit đi kèm
  bằng chứng quorum, dùng cho catch-up bình thường qua `CommitSyncer`. Đúng
  loại (b) ở mục 4.3.
- **DIGEST-GATE / `peer_commit_attestation`** — cơ chế đếm phiếu quorum
  (`CommitVoteMonitor::vote_count_for_index`) đã dùng để verify 1 commit-index
  là đáng tin trước khi dispatch, hoàn toàn tương đương "quorum certificate"
  của Aptos.
- **`reset_to_network_baseline`** (`dag_state_impl.rs`) — cơ chế NHẬN state từ
  mạng và tự đặt lại baseline **có kiểm soát**, đã từng được vá đúng hướng này
  1 lần (mục "commit_info_sync_fork.md": thêm `reputation_scores` vào luồng
  fetch để STARTUP-SYNC không còn tự đoán/tự tính thiếu dữ liệu).

Vấn đề KHÔNG phải là "thiếu công cụ" — mà là STARTUP-SYNC cho Validator đang
**đi vòng qua** những công cụ này, dùng đường tắt Go-level (`SyncBlocksRequest`
+ header.CommitIndex()) vốn được thiết kế cho SyncOnly.

---

## 6. Phương án thiết kế đề xuất

### 6.1. Nguyên tắc chỉ đạo

> **Với một node CÓ DAG riêng (Validator), `commit-index` không bao giờ được
> gán từ bên ngoài — nó chỉ được phép sinh ra từ chính DAG nội bộ của node đó
> (deterministic), hoặc đi kèm bằng chứng quorum tường minh.**

Điều này gộp 3 lỗi rời rạc (mục 3) thành **một bất biến kiến trúc được bảo vệ
ở một chỗ**, thay vì vá từng triệu chứng khi nó lộ ra lần nữa ở một đường khác
(đã xảy ra 3 lần: `recovery.rs`, `executor.rs::commit_is_empty_for_gei`, và
giờ là `startup_sync.rs` — không có lý do để tin đây là lần cuối nếu không sửa
gốc).

### 6.2. Thiết kế cụ thể — Option A (khuyến nghị)

**Ý tưởng**: tách STARTUP-SYNC (cho Validator) thành 2 bước rõ ràng thay vì 1
bước gộp như hiện tại:

1. **Bước NẠP DỮ LIỆU (data-only)**: giữ nguyên cơ chế fetch-từ-peer +
   `sync_and_execute_blocks` để nạp nhanh block content + state (đã có sẵn,
   nhanh, đã qua parent-hash/state-root verify — phần này KHÔNG sai, cứ giữ).
   Nhưng **loại bỏ hoàn toàn** đoạn `ForceSetLastHandledCommitIndex(commitIdx32)`
   lấy từ `header.CommitIndex()` của peer (mục 2.3) — Go **không được phép**
   tự quyết định commit-index nữa trong luồng này.

2. **Bước ĐỐI CHIẾU COMMIT-INDEX (Rust tự làm, không tin Go/peer)**: sau khi
   data đã nạp xong tới block N, Rust hỏi lại **CHÍNH DAG của mình**
   (`DagState`, đã được STARTUP-SYNC's cryptographic-stream-validation nạp
   qua `fetch_blocks_from_peer`/`CertifiedCommit` ở một nhánh khác của cùng
   file) — "DAG của tôi nói block/GEI N tương ứng với commit-index nào?". Nếu
   DAG cục bộ ĐÃ CÓ câu trả lời (đã từng quyết định/nhận CertifiedCommit cho
   range này) → dùng giá trị đó, KHÔNG BAO GIỜ dùng giá trị Go/peer báo.
   Nếu DAG cục bộ **chưa có** dữ liệu cho range đó (trường hợp thực tế đang
   gặp) → đây chính là dấu hiệu cần **để CommitSyncer catch-up bình thường**
   (qua `CertifiedCommit`, đã quorum-verify) tiếp quản việc xác định
   commit-index, thay vì STARTUP-SYNC tự đoán.

3. Cụ thể hoá bước 2 bằng cách **không gọi
   `commit_processor.update_next_expected_index(new_handled + 1)` ngay sau
   STARTUP-SYNC** như hiện tại (`startup_sync.rs:526`) nếu Go's report không
   khớp với bất kỳ mốc nào Rust tự biết. Thay vào đó: giữ
   `next_expected_index` ở giá trị AN TOÀN cuối cùng Rust tự xác nhận được
   (ví dụ: mốc trước khi SYNC-FORK-GUARD phải can thiệp lần đầu), và để
   CommitSyncer tự fetch+verify range còn thiếu qua đường `CertifiedCommit`
   bình thường (đường này ĐÃ an toàn, đã test rất kỹ suốt cả dự án).

**Vì sao đây là fix đúng tầng, không phải vá thêm lần 4**: nó biến "STARTUP-
SYNC" từ chỗ tự-quyết-định-commit-index (rủi ro) thành chỗ **chỉ tăng tốc nạp
dữ liệu**, còn quyền quyết định "commit nào ứng với index nào" **luôn thuộc về
1 chỗ duy nhất** (DAG nội bộ + CommitSyncer's quorum-verified path) — đúng bất
biến ở mục 6.1, và đúng mô hình Sui/Aptos ở mục 4.

### 6.3. Option B — vá tối thiểu (đã cân nhắc, KHÔNG khuyến nghị làm chính, có thể làm tạm trong lúc chờ Option A)

Chỉ đơn giản: đổi điều kiện ở mục 2.3 để **không bao giờ** chấp nhận
`header.CommitIndex()` từ 1 block **fetch từ peer** (chỉ cho phép nguồn này
khi phục hồi từ **snapshot/backup của chính mình**, nơi node-local
CommitIndex vẫn nhất quán vì cùng 1 node). Ưu điểm: sửa nhanh, rủi ro thấp,
chặn được đúng lỗi đang thấy. Nhược điểm: không giải quyết tận gốc — nếu
`next_expected_index` không có nguồn nào để lấy đúng nữa, node sẽ cần dựa hẳn
vào CommitSyncer catch-up từ đầu (chậm hơn, nhưng AN TOÀN hơn hẳn so với đoán
sai). **Có thể triển khai ngay như một "circuit breaker" trong lúc thiết kế
Option A đầy đủ**, vì tự nó đã loại bỏ hoàn toàn khả năng gây fork (chỉ đổi
"nhanh nhưng có thể sai" thành "chậm hơn nhưng luôn đúng").

### 6.4. LAYER-6: chuyển từ "hỏi 1 peer, xoay vòng" sang "hỏi đa số"

Đổi `runtime_fork_guard` để mỗi lần verify (kể cả lần đầu, không chỉ retry)
hỏi **nhiều peer cùng lúc** (ví dụ toàn bộ `barrier_peers`) và yêu cầu
**đa số phản hồi phải đồng nhất với nhau** trước khi so với local — nếu bản
thân các peer trả lời KHÔNG đồng nhất với nhau (dấu hiệu fork-2-phe-thật, như
đang gặp), phải log riêng một cảnh báo mới ("QUORUM ITSELF IS SPLIT") thay vì
lặp vô hạn "MISMATCH → NOW MATCHES → MISMATCH" như hiện tại — đúng tinh thần
"đòi quorum, không đòi 1 nguồn" ở mục 4.3.

---

## 7. Vì sao đây là lần thứ 3 của cùng 1 họ bug, và vì sao lần này cần sửa khác đi

| # | File ghi nhận | Metadata bị bỏ sót/nhập sai |
|---|---|---|
| 1 | `known_bugs/startup_sync_persistence_fork.md` | `lastBlockHashKey` chưa flush xuống đĩa |
| 2 | `known_bugs/commit_info_sync_fork.md` | `CommitInfo`/`reputation_scores` không được fetch kèm commit |
| 3 | tài liệu này | `CommitIndex` bị mượn thẳng từ peer, không quorum-verify |

Cả 3 lần đều được sửa bằng cách **thêm đúng 1 trường/đúng 1 lệnh flush còn
thiếu** — vá đúng, nhưng KHÔNG đóng được lớp lỗ hổng chung: "STARTUP-SYNC là 1
đường tắt riêng, dễ quên đồng bộ 1 mẩu state nào đó mà đường consensus chính
thống đã xử lý đúng từ lâu". Mục 6.1-6.2 đề xuất đóng lớp này bằng nguyên tắc
(không đường tắt nào được tự quyết định commit-index), thay vì tiếp tục dò
từng trường bị thiếu mỗi khi lộ ra.

---

## 8. Đã làm trong phiên điều tra này (tách biệt với thiết kế ở mục 6, đã deploy+verify)

**Bug vòng lặp vô hạn STARTUP-SYNC** (mục 3, bug #1) — ĐÃ SỬA, ĐÃ DEPLOY, ĐÃ
VERIFY TRỰC TIẾP trên local 232 (node thoát vòng lặp, bắt kịp cluster):

`consensus/metanode/src/node/setup_consensus/startup_sync.rs`, trong nhánh
`if chunk_sync_failed`: thêm truy vấn lại `barrier_client.get_last_block_number()`
và **luôn chấp nhận giá trị mới kể cả khi thấp hơn** (khác với guard "ignore
lower value" ở đầu hàm — ở đây giá trị thấp hơn CHÍNH LÀ điều đúng, vì đó là
kết quả rollback của SYNC-FORK-GUARD) trước khi retry vòng tiếp theo. Chưa
commit vào git (đang nằm trên working tree `dev`, cần gộp chung với quyết
định ở mục 6 trước khi tạo branch/PR, vì 2 bug liên quan chặt tới nhau).

**Lưu ý quan trọng**: bug #1 tự nó AN TOÀN (chỉ sửa việc "đọc lại đúng số",
không đụng gì tới việc "commit-index đến từ đâu") — nhưng chính vì nó cho
phép node THOÁT được vòng lặp và tiến tới bước STARTUP-SYNC hoàn tất, nó mới
làm lộ ra bug #2 (vốn nằm ở bước SAU khi STARTUP-SYNC thành công) — bug #2 rất
có thể đã tồn tại từ trước, chỉ chưa có cơ hội biểu hiện vì node luôn bị kẹt ở
bug #1 trước khi chạm tới nó.

---

## 9. Câu hỏi mở, chưa trả lời được trong phiên này

**Vì sao node-0 có block #397 sai NGAY TỪ ĐẦU** (trước cả khi STARTUP-SYNC
tham gia) — nguyên nhân gốc của CHÍNH cú fork đầu tiên kích hoạt toàn bộ chuỗi
sự kiện ở mục 3 — chưa được điều tra trong phiên này. LAYER-6 đã bắt và
`abort()` đúng thiết kế tại thời điểm đó, nên hệ quả trực tiếp không phải là
fork chưa bị phát hiện — nhưng nếu nguyên nhân gốc của bug #397 vẫn còn đó,
sự cố sẽ tái diễn định kỳ ngay cả sau khi mục 6 được triển khai đầy đủ. Nên
điều tra riêng, độc lập với thiết kế ở tài liệu này.

---

## 10. Việc cần làm tiếp (chưa code, chờ xác nhận hướng từ mục 6)

1. Xác nhận chọn Option A (kiến trúc đầy đủ) hay B (vá tạm) trước, hay làm B
   ngay lập tức như circuit-breaker rồi làm A sau — cả 2 đều hợp lý, khác nhau
   về thời gian/rủi ro.
2. Thiết kế chi tiết interface giữa STARTUP-SYNC và CommitSyncer cho Option A
   (hàm nào gọi hàm nào, DagState cần thêm API gì để "hỏi lại DAG nội bộ biết
   gì về range N" một cách rẻ/nhanh).
3. Viết lại `runtime_fork_guard` theo mục 6.4 (đa số peer, không xoay vòng 1).
4. Test: cần 1 kịch bản test tái tạo được đúng "STARTUP-SYNC catch-up trên
   Validator, không phải SyncOnly" — kịch bản hiện tại (`run_restart_test.sh`)
   đã tình cờ tái tạo được 1 lần, nhưng không xác định — cần làm nó tái tạo
   được theo yêu cầu để có test hồi quy thật sự (regression test), không chỉ
   dựa vào may mắn khi test thủ công.
5. Điều tra riêng câu hỏi mở ở mục 9.
