# Thiết kế: đóng lỗ hổng "tin dữ liệu DAG cục bộ không có bằng chứng mật mã"

**Trạng thái (CẬP NHẬT 2026-09-10): user đã chọn Phương án A rõ ràng** ("Phương
án A, ưu tiên sửa 2 bug gốc trước và cần tạo cảnh bảo rõ ràng cho operater vào
telegram nhé"). 2 bug gốc (mục 7 tài liệu incident chính) đã fix, test, deploy,
verify trực tiếp trên cụm thật, và push thẳng `dev` (`d5cbbdc9`) — xong trước,
đúng thứ tự user yêu cầu. Phần lõi của Phương án A (halt + log marker rõ ràng
thay vì tự "ân xá" không bằng chứng, cộng cảnh báo Telegram qua Health Monitor
có sẵn — KHÔNG qua tích hợp Telegram của tầng deploy/CI, theo đúng yêu cầu rõ
ràng của user) **đã code xong, compile sạch, test xanh (193 consensus-core +
185 metanode, 0 fail), nhưng CHƯA commit/deploy lên cụm thật** — xem mục 7 mới
bên dưới để biết chi tiết trạng thái implementation và các việc còn lại trước
khi coi là sẵn sàng merge `dev`. Phương án B (mục 5) được giữ lại làm phương án
dự phòng đã ghi chép đầy đủ, không triển khai.

Tài liệu này phân tích kỹ nguyên nhân gốc thật sự của sự cố fork ngày
2026-09-10 (xem `note/deploy_hardening_and_incident_drills_2026-09.md` mục
7 để biết bối cảnh sự cố gốc), đối chiếu với cách hệ thống Sui thật (gốc
mà codebase này fork từ đó) đã xử lý đúng loại sự cố này trong sản xuất
thật (mục 4.5), và đề xuất **2 phương án kiến trúc thay thế** — không phải
chỉnh 1 hằng số thời gian.

---

## 1. Tóm tắt vấn đề bằng 1 câu

Khi 1 node đọc lại dữ liệu DAG cũ từ đĩa (RocksDB) thay vì nhận trực tiếp
qua mạng, hệ thống **không xác minh lại chữ ký** của block đó — chỉ so khớp
digest với chính nó (tự tham chiếu, không có giá trị chứng minh) — nên nếu
dữ liệu trên đĩa từng bị ghi sai (do 1 bug/crash khác, không liên quan gì
tới logic đồng thuận), node sẽ **tự tin dùng dữ liệu sai đó như thể đã được
xác minh**, và không có tầng phòng thủ nào bắt được cho tới khi `LAYER-6
fork_guard` (lớp kiểm tra định kỳ, độc lập, so sánh với peer) tình cờ chạy
tới đúng block đó — có thể sau hàng chục phút.

---

## 2. Bằng chứng từ code thật (đã đọc trực tiếp, không suy đoán)

### 2.1. Đường đi CÓ xác minh chữ ký (khi nhận block MỚI qua mạng)

`block_verifier.rs`:
```rust
fn verify_block_inner(&self, block: &SignedBlock, skip_epoch_check: bool) -> ConsensusResult<()> {
    ...
    // Verify the block's signature.
    block.verify_signature(&self.context)?;
    ...
}
```
Được gọi bởi `verify_and_vote()` (block mới tự mình đề xuất/nhận qua gossip)
và `verify_for_commit_sync()` (block nhận qua `CommitSyncer::fetch_commits`
từ peer) — CẢ HAI đều gọi `block.verify_signature()` **TRƯỚC KHI** tạo
`VerifiedBlock::new_verified(...)`. Đây là đường đi AN TOÀN.

### 2.2. Đường đi KHÔNG xác minh chữ ký (khi đọc lại block CŨ từ đĩa)

`storage/rocksdb_store.rs::read_blocks()`:
```rust
fn read_blocks(&self, refs: &[BlockRef]) -> ConsensusResult<Vec<Option<VerifiedBlock>>> {
    ...
    let signed_block: SignedBlock = bcs::from_bytes(&serialized)...;
    // Only accepted blocks should have been written to storage.
    let block = VerifiedBlock::new_verified(signed_block, serialized);
    // Makes sure block data is not corrupted, by comparing digests.
    assert_eq!(*key, block.reference());
    ...
}
```
**Không có `verify_signature()` ở đây.** Comment "Only accepted blocks
should have been written to storage" là NIỀM TIN (assumption), không phải
kiểm tra thật. Dòng `assert_eq!` chỉ so khớp `(round, author, digest)` —
digest được TÍNH TỪ CHÍNH bytes vừa đọc, nên đây là kiểm tra **tự tham
chiếu** (nếu bytes sai nhưng tự nhất quán, digest tính ra từ bytes sai đó
vẫn "khớp" với chính nó) — **không chứng minh được bytes đó thật sự là cái
mà validator gốc đã ký.**

### 2.3. `VerifiedBlock` là 1 type, nhưng 2 mức độ tin cậy khác nhau lại
dùng chung

`block.rs`:
```rust
/// Creates VerifiedBlock from a verified SignedBlock and its serialized bytes.
pub fn new_verified(signed_block: SignedBlock, serialized: Bytes) -> Self { ... }
```
Tên hàm ngụ ý "đầu vào đã được verify" — nhưng bản thân hàm **không hề kiểm
tra** gì cả, chỉ đóng gói. Cả 2 đường đi (2.1 xác minh chữ ký thật, 2.2 chỉ
tin đĩa) đều tạo ra CÙNG 1 type `VerifiedBlock`, không có cách nào ở tầng
type-system hay runtime để phân biệt "cái này thật sự đã qua verify chữ ký"
với "cái này chỉ được tin vì đọc từ đĩa". Code tiêu thụ `VerifiedBlock` ở
mọi nơi khác (linearizer, commit_processor, digest-gate...) không có cách
nào biết để cẩn trọng hơn với loại thứ 2.

### 2.4. Đường đi thực tế dẫn tới amnesty ĐI QUA đúng lỗ hổng này

`dag_state/read.rs::get_blocks()`:
```rust
/// Gets blocks by checking genesis, cached recent blocks in memory, then storage.
pub(crate) fn get_blocks(&self, block_refs: &[BlockRef]) -> Vec<Option<VerifiedBlock>> {
    ...
    if let Some(block_info) = self.recent_blocks.get(block_ref) {
        blocks[index] = Some(block_info.block.clone());   // cache trong RAM — AN TOÀN (đã verify lúc nhận)
        continue;
    }
    missing.push((index, block_ref));
    ...
    let store_results = self.store.read_blocks(&missing_refs)...  // rơi xuống ĐĨA — KHÔNG AN TOÀN (mục 2.2)
```
`recent_blocks` (RAM) chỉ giữ "uncommitted and last committed blocks" —
**bị giới hạn/eviction** theo `cached_rounds`. Ngay sau khi 1 node RESTART,
cache RAM này gần như RỖNG — mọi lần `linearize_sub_dag()` cần dữ liệu DAG
cũ (đúng thứ mà `decided_with_local_blocks` và sau đó là cơ chế "ân xá" cần
dùng) đều buộc phải rơi xuống đường đọc-đĩa-không-xác-minh ở mục 2.2. **Đây
chính xác là lúc "co-located validators restarting" — kịch bản đã gây fork
thật ngày 2026-09-10.**

### 2.5. Không có kiểm tra toàn vẹn khi khởi động cho tầng consensus-core

Tầng EXECUTION (Go) có "🔍 [INTEGRITY] Starting data integrity check
(depth=10)" chạy mỗi lần khởi động (đã thấy log này nhiều lần trong phiên
làm việc). Tầng CONSENSUS-CORE (Rust) **không có gì tương đương** — đã grep
toàn bộ `consensus/metanode/meta-consensus/core/src/`, chỉ có
`recover_last_commit_info()` (khôi phục điểm reputation, không phải kiểm
tra nội dung block).

---

## 3. Vì sao đây LÀ nguyên nhân hợp lý nhất cho sự cố ngày 2026-09-10

Chuỗi nhân quả đầy đủ, khớp với mọi bằng chứng đã quan sát:

1. Trong phiên làm việc đó, node-0 đã trải qua 1 chuỗi crash-loop RocksDB
   thật (panic tại `rocksdb_store.rs:31`, "outer restart #6") — tức đã có
   NHIỀU LẦN dừng đột ngột/không sạch ngay trước sự cố.
2. node-3 (cùng host `234` với node-0) cũng trải qua restart dồn dập trong
   cùng khung thời gian.
3. Sau mỗi lần restart, cache RAM (`recent_blocks`) rỗng → mọi truy vấn DAG
   gần đây đều rơi xuống đường đọc-đĩa (mục 2.2/2.4) — không xác minh chữ
   ký.
4. `next_expected_index` kẹt (đúng bug gốc, xem tài liệu incident chính) →
   cơ chế "ân xá" kích hoạt sau 121s, tin tưởng "commit 856...", dữ liệu
   này lấy từ `decided_with_local_blocks`, mà bản thân input của nó có thể
   đã đi qua đường đọc-đĩa không xác minh ở bước (3).
5. 38 phút sau, `LAYER-6 fork_guard` (kiểm tra định kỳ mỗi 10 block, độc
   lập với toàn bộ luồng trên) phát hiện Block #500 sai lệch thật so với
   peer.

**Chưa chứng minh được 100% đây là CƠ CHẾ DUY NHẤT** (chưa dựng lại được
byte-for-byte cách dữ liệu trên đĩa bị sai từ đầu — có thể là 1 write bị
ngắt giữa chừng lúc crash, có thể là 1 bug logic khác hoàn toàn tạo ra
`WriteBatch` sai từ đầu) — nhưng bất kể CƠ CHẾ SINH RA dữ liệu sai là gì,
**lỗ hổng "không xác minh lại khi đọc từ đĩa" là điều kiện CẦN để dữ liệu
sai đó len lỏi vào được 1 quyết định cục bộ mà không bị chặn** — đóng lỗ
hổng này giúp ích bất kể nguyên nhân sinh ra corruption ban đầu là gì.

---

## 4. Vì sao chỉnh `RECOVERY_STUCK_TIMEOUT_SECS` không đóng được lỗ hổng
này (nhắc lại, đã giải thích, ghi lại ở đây cho đầy đủ tài liệu thiết kế)

Ngưỡng thời gian chỉ trả lời "mạng có còn bỏ phiếu cho chỉ số này không" —
không liên quan gì tới "dữ liệu cục bộ của TÔI có còn đúng không". Nếu dữ
liệu đã sai từ bước (3) ở mục 3, chờ bao lâu cũng sai y hệt. Số liệu 900s
"an toàn hơn" 120s trong thực nghiệm chỉ vì cửa sổ dài hơn tình cờ cho hệ
thống nhiều thời gian hơn để tự ổn định TRƯỚC KHI đồng hồ đếm hết — không
phải một chứng minh toán học.

---

## 4.5. 🌍 Đối chiếu với thực tế: Sui thật (hệ thống gốc mà codebase này
fork từ đó) đã gặp gần như đúng sự cố này — và xử lý HOÀN TOÀN KHÁC

Đã tra cứu (2026-09-10, sau khi user yêu cầu kiểm tra xem blockchain thực
tế có giải pháp hay hơn không). Toàn bộ `consensus-core` trong repo này ghi
`Copyright (c) Mysten Labs, Inc.` — đây là fork/derivative thật của
`consensus-core` dùng trong Sui mainnet (giao thức Mysticeti). Tra được 1
sự cố CÓ THẬT, đã công bố chính thức: **"Sui Mainnet Network Stall
Resolution"** (tháng 3/2026,
[blog.sui.io](https://www.sui.io/blog/sui-mainnet-network-stall-resolution)).

**Tóm tắt sự cố thật của Sui:** 1 bug logic trong xử lý commit (edge-case
liên quan garbage collection) khiến các validator tính ra kết quả đồng
thuận KHÁC NHAU từ cùng đầu vào — về bản chất là cùng 1 lớp vấn đề với sự
cố của chúng ta (dữ liệu/tính toán cục bộ lệch khỏi những gì mạng thật sự
đồng thuận).

**Cách Sui thật xử lý — đối lập hoàn toàn với cơ chế "ân xá" trong codebase
này:**
- *"they halted rather than proceed unilaterally"* — validator **DỪNG
  LẠI**, không tự ý tiến tới khi không đạt quorum (>1/3 stake ký digest
  khác nhau → không thể certification).
- Câu trích dẫn quan trọng nhất: **"Critically, no validator trusted its
  own unconfirmed decision locally."**
- Cơ chế "quarantine" (khu vực tạm giữ effect chưa finalize) chặn TUYỆT ĐỐI
  việc hoàn tất cho tới khi đạt chứng thực (certification) bằng quorum
  stake thật — không có đường "tự tin sau khi chờ đủ lâu" ở bất kỳ đâu
  trong kiến trúc gốc.
- Phục hồi: kỹ sư tìm nguyên nhân gốc → viết bản vá → **validator của
  Mysten Labs tự kiểm thử, xác nhận đúng** → validator khác nâng cấp binary
  đã vá, "replay consensus AN TOÀN", nối lại ký khi quorum khôi phục. Đây
  là quy trình CÓ NGƯỜI, CÓ KIỂM CHỨNG, không phải tự động hoá âm thầm.

**Ý nghĩa cho thiết kế:** cơ chế `PERMANENT-GAP-RECOVERY` (ân xá theo thời
gian) trong file `commit_processor/processor.rs` **là 1 bổ sung riêng của
MetaNode, KHÔNG tồn tại trong triết lý an toàn của hệ thống Sui gốc** mà nó
kế thừa phần lõi consensus-core. Điều này gợi ý mạnh mẽ: thay vì cố làm cho
cơ chế "ân xá" AN TOÀN HƠN (Lớp 1/Lớp 2 ở mục 5), có thể hướng đúng đắn hơn
— đã được CHỨNG MINH BẰNG SẢN XUẤT THẬT — là:

1. **Hạn chế/loại bỏ khả năng "tự tin dữ liệu cục bộ chưa xác nhận"** thay
   vì cố bọc thêm lớp bảo vệ cho nó. Khi thực sự bí (không có bằng chứng
   nào cho thấy mạng còn đồng thuận chỉ số này), hành vi AN TOÀN là **dừng
   node đó lại + báo động rõ ràng cho operator** (chấp nhận mất khả dụng
   cục bộ tại 1 node, giống Sui thật chấp nhận mất khả dụng TOÀN MẠNG khi
   cần) — không phải âm thầm đoán và tiếp tục chạy.
2. **Ưu tiên sửa 2 bug gốc đã tìm thấy trong cùng phiên điều tra** (crash-
   loop RocksDB không tự hồi phục — mục 7 tài liệu incident chính; lỗi bàn
   giao peer_rpc sau restart dồn dập) — rất có khả năng tình huống "kẹt
   thật, không thể xác nhận được" vốn dĩ CỰC HIẾM trong vận hành bình
   thường (Sui thật cũng cần 1 bug hiếm, edge-case GC, mới gây ra sự cố
   tương tự) — sửa đúng 2 bug gốc có thể loại bỏ phần lớn nhu cầu phải có
   "ân xá" tự động ngay từ đầu, thay vì tốn công làm cho việc đoán mò trở
   nên "an toàn hơn".

**Tham khảo thêm 1 mẫu thiết kế production khác, cùng họ BFT** (tra cứu
song song): kiến trúc `SafetyRules` của DiemBFT/HotStuff — thay vì cố bảo
vệ TOÀN BỘ dữ liệu DAG cục bộ, hệ thống này chỉ lưu bền vững (đồng bộ,
fsync ngay khi cập nhật) **1 lượng cực nhỏ dữ liệu "an toàn-quan-trọng"**
(vd: round cao nhất đã bỏ phiếu) — đủ để KHÔNG BAO GIỜ tự mâu thuẫn
(equivocate) dù mất điện/crash bất cứ lúc nào — còn lại TOÀN BỘ dữ liệu DAG
khác được coi là "cache", có thể mất/hỏng sau crash và **luôn được phép
re-fetch lại từ peer mà không có rủi ro an toàn nào**, vì bản thân nó không
phải thứ quyết định an toàn. Đối chiếu với codebase này: `next_expected_index`/
`gap_recovery_bypass_ceiling` hiện đang suy luận dựa trên TOÀN BỘ kho DAG
cục bộ (mục 2.4) thay vì dựa trên 1 "điểm neo an toàn" hẹp, tối giản kiểu
`SafetyRules` — đây là 1 hướng thiết kế thay thế đáng cân nhắc song song
với mục 5, có thể còn triệt để hơn nhưng cũng là thay đổi kiến trúc lớn
hơn nhiều.

---

## 5. Hai phương án — trình bày cả hai, chưa chốt 1 phương án duy nhất

### Phương án A — Bỏ khả năng "tự tin dữ liệu cục bộ chưa xác nhận", theo
đúng mô hình Sui thật đã dùng trong sản xuất (khuyến nghị xem xét trước)

Cụ thể: khi `gap_recovery_bypass_ceiling` sắp được set (tức là hệ thống
sắp sửa "ân xá" 1 commit chưa xác nhận được), THAY VÌ cấp ân xá và tiếp tục
dispatch, node:
1. Log lỗi nghiêm trọng, đầy đủ bối cảnh (index bị kẹt, đã kẹt bao lâu,
   bao nhiêu commit đang chờ phía sau) — dùng đúng mức độ chi tiết mà
   `PERMANENT-GAP-RECOVERY` hiện tại đã ghi log.
2. Chuyển sang trạng thái "chờ can thiệp" tương đương `is_terminally_failed`
   mà `LAYER-6 fork_guard` đã dùng khi phát hiện fork — **không** tự
   `abort()` ngay (khác LAYER-6 — ở đây chưa CHẮC CHẮN có gì sai, chỉ là
   CHƯA XÁC NHẬN được, nên dừng dispatch nhưng vẫn giữ node sống để dễ chẩn
   đoán/can thiệp, không cần khởi động lại).
3. Gửi cảnh báo operator thật (Telegram, đã có sẵn hạ tầng cảnh báo trong
   `deploy/ci/incident_drills/drill_telegram_alert.sh` và hệ thống alert
   hiện có) — không chỉ ghi log im lặng.
4. Việc "gỡ kẹt" trở thành 1 hành động CÓ KIỂM SOÁT (vận hành thủ công gọi
   `--restore-node`/`--reset-all` cho đúng node đó, hoặc 1 công cụ tự động
   riêng chạy NGOÀI đường găng chính, có thể re-sync xác minh chữ ký thật
   từ peer trước khi cho node tham gia lại) — không phải tự động âm thầm
   trong vòng lặp dispatch.

**Đánh đổi:** chấp nhận 1 node có thể "đứng yên" lâu hơn (mất khả dụng cục
bộ) thay vì tự đoán — đúng triết lý Sui thật đã áp dụng thành công trong sự
cố sản xuất thật của họ. Nếu bug gốc (crash-loop RocksDB, peer_rpc handover)
được sửa trước, tình huống thực sự cần dùng tới đường này sẽ RẤT HIẾM.

**Việc cần làm thêm nếu chọn hướng này:** xác nhận việc 1 node "đứng yên
chờ can thiệp" không tự nó kéo cả cụm xuống dưới ngưỡng quorum một cách
không cần thiết — cần review kỹ có bao nhiêu node có thể rơi vào trạng thái
này cùng lúc trong kịch bản xấu nhất, và liệu ngưỡng BFT (2f+1) còn đứng
vững hay không khi đó.

### Phương án B — Làm an toàn hơn cơ chế "ân xá" hiện có (2 lớp, phương án
dự phòng nếu vẫn cần khả năng tự động hồi phục mà không có operator)

**⚠️ CẬP NHẬT (sau khi user yêu cầu xem xét kỹ rủi ro deadlock/race —
2026-09-10, cùng ngày):** bản thiết kế Lớp 2 ban đầu (bên dưới, đã sửa) có
lỗi thật: "bắt buộc chờ xác nhận từ peer mới được quyết định cục bộ" không
có giới hạn thời gian và không có đường thoát khi TẤT CẢ peer cũng đang
trong tình trạng tương tự (vd toàn cụm cùng restart) — đúng loại
**self-locking cycle** mà chính file `commit_syncer/mod.rs` đã ghi nhận
từng gặp thật ("a self-locking cycle that, once entered..., never recovers
on its own. Reproduced directly: a single-validator devnet stuck >1
minute"). Đã tìm thấy 1 mẫu ĐÚNG ĐẮN có sẵn trong chính codebase cho đúng
tình huống này — xem mục 5.3 — và sửa lại Lớp 2 theo đúng mẫu đó.

### Lớp 1 — Xác minh lại chữ ký khi tin dữ liệu đọc từ đĩa cho quyết định an
toàn-quan-trọng (ưu tiên cao, phạm vi hẹp, ĐÃ XÁC NHẬN không có rủi ro
deadlock/race)

**Không** xác minh lại MỌI lần đọc đĩa (quá tốn kém, phá vỡ lý do thiết kế
cache RAM ngay từ đầu). Chỉ xác minh lại chữ ký cho đúng những block nằm
trong chuỗi nhân quả dẫn tới 1 quyết định **"ân xá"** (`gap_recovery_bypass_ceiling`)
— đây vốn đã là đường XỬ LÝ HIẾM, có sẵn ngân sách thời gian/CPU (bài test
thật cho thấy vài phút tới hàng chục phút mới xảy ra 1 lần), nên trả thêm
chi phí xác minh chữ ký ở đúng thời điểm này là hoàn toàn chấp nhận được.

**Vì sao không có rủi ro deadlock/race (đã kiểm tra code thật):**
`block.verify_signature(&context)` chỉ cần khóa công khai từ
`context.committee` — `Context` là **immutable theo từng epoch**
(`context.rs`, field `committee: Committee` không bọc `RwLock`/`Mutex`;
chuyển epoch tạo hẳn `Context` mới, không sửa đè cái cũ) — đây là tính toán
CPU thuần túy, không khóa (lock), không I/O mạng, không phụ thuộc bất kỳ
tiến trình/thread nào khác. Không có đường nào dẫn tới deadlock hay race
condition ở lớp này.

Việc cần làm (mức thiết kế, chưa code):
- Thêm 1 hàm mới, ví dụ `verify_causal_chain_signatures(commit_index) ->
  Result<(), ...>` trong `linearizer` hoặc `commit_processor`, duyệt lại
  toàn bộ block trong `to_commit` (và tổ tiên của chúng nếu cần) của đúng
  commit sắp được "ân xá", gọi `block.verify_signature(&context)` cho từng
  cái.
- Chỉ gọi hàm này ở ĐÚNG 1 chỗ: ngay trước khi `gap_recovery_bypass_ceiling`
  được set / trước khi 1 commit cụ thể được chấp nhận dưới amnesty trong
  vòng lặp DIGEST-GATE POLL (`commit_processor/processor.rs`).
- Nếu xác minh THẤT BẠI: KHÔNG cấp ân xá cho chỉ số đó — log lỗi nghiêm
  trọng (đây là bằng chứng dữ liệu cục bộ đã hỏng thật, cần cảnh báo
  operator ngay, không chỉ âm thầm bỏ qua). **Không** tự ý xóa/reset dữ liệu
  cục bộ ở đây (đó là quyết định vận hành, không phải quyết định tự động) —
  chỉ từ chối ân xá và tiếp tục chờ đường xác minh bình thường + Lớp 2.

### 5.3. Mẫu ĐÚNG ĐẮN đã có sẵn trong codebase — dùng làm khuôn mẫu cho Lớp 2

`node/setup_consensus/startup_sync.rs`, cơ chế "ANTI-FORK" lúc khởi động:
so `block_hash`/`state_root` của block cục bộ với peer, **giới hạn 10 lần
thử × 3 giây = tối đa 30 giây**; nếu hết giới hạn mà vẫn chưa xác nhận
được (vd peer cũng đang restart) thì **KHÔNG chặn cứng** — log rõ ràng rồi
chủ động "DEFERRING to POST-GATE-VERIFY" và tiếp tục chạy, giao phó việc
bắt lỗi cho 1 lớp kiểm tra ĐỘC LẬP, chạy SAU, không nằm trên đường găng
(critical path). Đây chính là triết lý thật sự của toàn bộ hệ thống Zero-
Fork Invariant, thể hiện rõ nhất qua `LAYER-6 fork_guard`: **không phải
"không bao giờ để tính sai xảy ra"**, mà là **"phát hiện đủ nhanh + tự
dừng sạch trước khi cái sai kịp lan ra ngoài"**. `LAYER-6` chạy như 1
observer độc lập (kiểm tra định kỳ mỗi 10 block, không chặn luồng chính),
và khi phát hiện sai thì `abort()` — 1 node tự dừng KHÔNG làm hỏng phần
còn lại của mạng (BFT không phụ thuộc vào đầu ra của riêng 1 node), nên
đây là hành vi an toàn thật sự, không phải một sự thỏa hiệp.

### Lớp 2 (ĐÃ SỬA) — Kiểm tra chéo NHANH, KHÔNG CHẶN LUỒNG, riêng cho các
commit vừa được ân xá — theo đúng mẫu 5.3, không phải "chặn trước"

~~(Bản cũ: coi "vừa khởi động lại sau khi dừng không sạch" là điều kiện bắt
buộc phải re-sync từ peer TRƯỚC KHI được quyết định cục bộ — ĐÃ LOẠI BỎ vì
rủi ro tự khóa khi toàn cụm cùng restart, xem cảnh báo đầu mục 5.)~~

Thiết kế thay thế: khi 1 commit được dispatch dưới "ân xá" (dù đã qua Lớp 1
— Lớp 1 chỉ bắt được corruption ở tầng CHỮ KÝ block, không bắt được mọi
dạng lỗi khác có thể xảy ra), đăng ký nó vào 1 hàng đợi kiểm tra chéo
NGẮN HẠN, xử lý bởi 1 task nền độc lập (giống hệt cách `LAYER-6` đã chạy
độc lập với luồng chính):
- Trong vòng ví dụ 60 giây SAU KHI dispatch (không phải trước — không chặn
  gì cả), chủ động hỏi ít nhất 1 peer "bạn có commit_index này chưa, digest
  của bạn là gì".
- Nếu peer xác nhận KHỚP → xóa khỏi hàng đợi, không làm gì thêm.
- Nếu peer xác nhận SAI (mismatch) → kích hoạt đúng đường xử lý `LAYER-6`
  đã có (log nghiêm trọng, tự `abort()` sạch) — CÀNG SỚM CÀNG TỐT so với
  hiện tại (tới 38 phút mới bắt được nhờ chu kỳ "mỗi 10 block" chung chung,
  không ưu tiên riêng các commit đến từ đường ân xá — vốn là đường RỦI RO
  CAO hơn hẳn đường bình thường).
- Nếu KHÔNG peer nào trả lời trong 60 giây (peer cũng đang restart, mạng
  gián đoạn...) → **KHÔNG chặn, KHÔNG coi là lỗi** — ghi log mức WARN
  "chưa xác minh chéo được", tiếp tục theo dõi ở lần kiểm tra định kỳ tiếp
  theo (tận dụng lại đúng chu kỳ "mỗi 10 block" của `LAYER-6` sẵn có làm
  lưới an toàn cuối cùng) — **không có bất kỳ đường nào trong thiết kế này
  chặn luồng dispatch chính**, nên không thể tự khóa dù TẤT CẢ peer cùng
  im lặng cùng lúc.

**Vì sao thiết kế này không có rủi ro deadlock:** khác bản cũ, Lớp 2 mới
KHÔNG NẰM TRÊN đường găng — nó là 1 task quan sát chạy song song, không có
ai chờ nó, không ai bị nó chặn. Trường hợp xấu nhất (không peer nào phản
hồi) chỉ dẫn tới "chưa xác minh chéo được nhanh, phải chờ chu kỳ định kỳ
bắt sau" — giống hệt mức độ an toàn/rủi ro hệ thống ĐANG CÓ hôm nay (chỉ
nhanh hơn trong trường hợp tốt, không tệ hơn trong trường hợp xấu nhất).

### (Không ưu tiên ngay) Củng cố đường `CommitSyncer` fetch `CertifiedCommit`
làm cơ chế phục hồi CHÍNH

Đã cân nhắc ở vòng thiết kế trước — đây là đường AN TOÀN TUYỆT ĐỐI (có chữ
ký/chứng chỉ tổng hợp thật, không phụ thuộc thời gian chờ) nhưng đã có lịch
sử bị revert ("ACTIVE SYNC RECOVERY: Removed... fetch commit cũ có thể fail
vì block đã bị peer dọn") — nên xếp sau Lớp 1+2, chỉ xem xét lại sau khi có
người hiểu rõ vì sao đường đó từng bị revert và điều kiện gì sẽ không lặp
lại y hệt lỗi đó.

---

## 6. Việc cần làm TRƯỚC KHI code bất cứ gì ở mục 5

0. **[MỚI, quan trọng nhất] Chọn giữa Phương án A và B ở mục 5** — đây là
   quyết định SẢN PHẨM/VẬN HÀNH (đánh đổi giữa "an toàn tuyệt đối, chấp
   nhận mất khả dụng cục bộ nhiều hơn" theo đúng mô hình Sui thật, và "vẫn
   giữ khả năng tự hồi phục không cần người can thiệp, nhưng phức tạp hơn
   và còn 1 chút rủi ro dù đã giảm nhiều") — không phải quyết định kỹ
   thuật thuần túy, cần người có thẩm quyền quyết định (không nên để 1 mình
   AI tự chọn). Nếu chọn Phương án A: xác nhận thêm câu hỏi ở cuối mục đó
   (nhiều node cùng "đứng yên" có kéo cả cụm dưới ngưỡng quorum không).
1. **Dựng lại chính xác cách dữ liệu trên đĩa của node-3 bị sai** (nếu còn
   truy được — dữ liệu gốc của sự cố đã bị xóa khi `--reset-all` sau đó, xem
   mục "Đã xử lý" trong tài liệu incident chính; cần 1 lần tái hiện MỚI, có
   kiểm soát, KHÔNG xóa dữ liệu ngay khi phát hiện, để bắt tận tay byte nào
   sai so với block gốc mà peer có).
2. Đo chi phí thật của việc xác minh chữ ký lại cho 1 chuỗi nhân quả (có
   thể vài trăm tới vài nghìn block) — xác nhận không tạo ra 1 nút thắt cổ
   chai MỚI ở đúng con đường vốn đã hiếm/khẩn cấp này.
3. ~~Thiết kế chi tiết cơ chế "dirty flag"~~ — **đã bỏ hướng này** sau khi
   xem xét kỹ rủi ro deadlock (mục 5, cập nhật) — Lớp 2 giờ dùng mô hình
   observer bất đồng bộ (mục 5.3/Lớp 2 mới), không cần "dirty flag" nữa.
4. **[MỚI] Xác nhận task nền (background task) của Lớp 2 mới không tự nó
   trở thành 1 nguồn rò rỉ tài nguyên** nếu chạy liên tục qua nhiều epoch/
   nhiều giờ (hàng đợi kiểm tra chéo phải có giới hạn kích thước + tự dọn
   dẹp mục quá hạn, giống cách `pending_local_commits`/`DIGEST_HISTORY_RETAIN`
   đã giới hạn kích thước ở những nơi khác trong cùng file) — đây không phải
   rủi ro deadlock/fork, nhưng là rủi ro vận hành cần tính tới trước khi
   code.
5. **[MỚI] Xác nhận việc gọi peer để kiểm tra chéo (Lớp 2 mới) dùng đúng
   cơ chế mạng đã có** (`peer_rpc`/`CommitSyncer` fetch) — KHÔNG tự mở thêm
   1 đường kết nối mạng mới — để không lặp lại đúng lớp bug peer_rpc
   "early→full server handover" đã phát hiện hôm nay (mục 7 tài liệu
   incident chính) ở 1 chỗ mới.
6. Review bởi người có domain knowledge sâu về `consensus-core`
   (Mysticeti/Sui gốc) — đây là thay đổi chạm vào đúng ranh giới tin cậy
   (trust boundary) cốt lõi của toàn bộ hệ thống BFT, rủi ro cao nếu làm
   sai theo 1 hướng khác. **Đặc biệt xin review kỹ phần "không chặn luồng
   chính" của Lớp 2 mới** — đây là thuộc tính AN TOÀN QUAN TRỌNG NHẤT của
   thiết kế (không deadlock), cần người khác xác nhận độc lập, không chỉ
   dựa vào 1 lượt tự-rà-soát.

---

## 7. Phương án A — trạng thái implementation thật (2026-09-10)

Sau khi user chọn Phương án A rõ ràng, đây là những gì ĐÃ code (trên nhánh
`feat/phuong-an-a-halt-not-guess`, KHÔNG merge `dev` cho tới khi có sign-off
riêng, đúng cách làm việc suốt phiên này với vùng code an toàn-quan-trọng này):

### 7.1. Thay đổi thật trong `commit_processor/processor.rs`

Xóa bỏ hoàn toàn đường cấp "ân xá" (`gap_recovery_bypass_ceiling = Some(ceiling)`)
— biến này và MỌI điểm đọc nó ở vòng lặp DIGEST-GATE POLL được giữ nguyên
không đổi 1 dòng nào (để giảm diện thay đổi/rủi ro tối đa), nhưng giờ luôn
chỉ thấy `None` vì không còn nơi nào gán `Some(...)` nữa — tức là bị vô hiệu
hóa hoàn toàn về mặt hành vi mà không cần sửa/hiểu lại toàn bộ vòng lặp đó.

Thay vào chỗ đó: khi `next_expected_index` đứng yên quá
`RECOVERY_STUCK_TIMEOUT_SECS` (900s, KHÔNG đổi hằng số này nữa — bài học
từ vụ fork thật khi thử rút xuống 120s) mà vẫn còn commit cục bộ đang chờ
(`pending_local_commits` không rỗng), thay vì tự tin dùng dữ liệu đó, hệ
thống chỉ ghi 1 dòng `error!()` cấp cao, dễ grep, đúng 1 lần cho mỗi lần
"đứng yên" liên tục (cờ `halt_alert_sent_for_current_stall`, tự reset khi
`next_expected_index` thật sự tiến lên) với marker cố định
`[CONSENSUS-HALT-SUSPECTED-DIVERGENCE]` — **không tự gọi mạng, không tự gọi
Telegram trong code an toàn-quan-trọng này** — theo đúng nguyên tắc tách
biệt: tầng Rust chỉ có trách nhiệm dừng lại + để lại bằng chứng rõ ràng,
tầng giám sát bên ngoài (bash, đã có sẵn, đã chạy, đã test) có trách nhiệm
phát hiện + cảnh báo người.

Hệ quả: node KHÔNG dispatch commit thêm nữa khi rơi vào tình huống này (đúng
tinh thần Phương án A / Sui thật — mục 4.5) — nhưng cũng KHÔNG tự crash/
panic/abort (RPC, service vẫn "sống" để operator còn kiểm tra/so sánh được
với peer trước khi quyết định phục hồi thế nào), và KHÔNG có vòng lặp chờ/
khóa nào mới được thêm vào (không đổi flow điều khiển hiện có ngoài việc
không gán `Some(...)` nữa) — nên không có rủi ro deadlock/race mới so với
code hiện tại.

### 7.2. Cảnh báo Telegram — qua Health Monitor có sẵn, không qua tầng deploy/CI

Theo đúng 2 lần làm rõ của user ("Không telegram trong deploy tích hợp ấy"
+ chọn "Dùng lại đúng bot/chat đã có trong inventory.yml"): thêm 1 kiểm tra
mới trong `deploy/ansible/monitors/start_monitors.sh`, WORKER 1 HEALTH
MONITOR — trong đúng nhánh node "còn sống" (RPC vẫn phản hồi, không phải
crash/down), quét 200KB cuối của `logs/execution/<ngày mới nhất>/execution.log`
(local đọc file trực tiếp, remote qua `ssh_remote` — đúng 2 cách truy cập
log per-node đã có sẵn trong script này) tìm marker
`CONSENSUS-HALT-SUSPECTED-DIVERGENCE`. Nếu thấy và chưa từng cảnh báo cho
lần "đứng yên" này (`consensus_halt_alerted[$node_key]`, chỉ reset khi
`dead_nodes[]` ghi nhận 1 lần crash/restart thật sự — KHÔNG tự "phục hồi"
chỉ vì dòng log trôi khỏi cửa sổ 200KB, vì thiếu bằng chứng không phải là
bằng chứng đã hết treo), gọi `send_tele()` — **dùng đúng `TELEGRAM_BOT_TOKEN`/
`TELEGRAM_CHAT_ID` đọc từ `inventory.yml` mà Health Monitor đã dùng cho mọi
cảnh báo khác** (SERVER_DOWN/REBOOT/NODE_CRASH) — với nội dung định dạng
HTML khác biệt rõ ràng (tiêu đề "CONSENSUS TỰ DỪNG — NGHI NGỜ PHÂN NHÁNH",
nhấn mạnh RPC vẫn sống nên KHÔNG PHẢI crash, và runbook: xác minh block
hash với peer TRƯỚC KHI quyết định restart hay restore-snapshot) — không
lẫn với các cảnh báo CI/deploy thường ngày khác cùng bot.

### 7.3. Việc còn lại trước khi coi là sẵn sàng (chưa xong tại thời điểm viết)

- [ ] Commit thay đổi `processor.rs` + `start_monitors.sh` + cập nhật 2 file
  note này trên nhánh `feat/phuong-an-a-halt-not-guess`.
- [ ] `cargo test -p consensus-core` + `-p metanode` sau khi commit (đã chạy
  xanh trước khi viết mục này: 193 + 185 pass, 0 fail) — nhắc lại 1 lần nữa
  sau build release trước khi deploy thật.
- [ ] Build + deploy nhánh này lên cụm thật (234/230), xác nhận: (a) hoạt
  động bình thường không bị ảnh hưởng khi KHÔNG rơi vào tình huống stuck,
  (b) cố gắng xác nhận marker log + cảnh báo Telegram thật sự bắn ra đúng
  khi tái hiện lại tình huống "đứng yên" (khó ép xảy ra 100% theo ý muốn,
  nhưng `node_chaos_restart` từng tái hiện được tình huống gốc tương đối
  ổn định — ưu tiên thử lại kịch bản đó).
- [ ] **KHÔNG merge `dev`** cho tới khi có xác nhận thêm từ user sau khi xem
  kết quả deploy/verify thật — đúng cách làm đã thống nhất suốt phiên này
  cho vùng code đã từng gây fork thật 1 lần.

---

## 8. 🔴 PHÁT HIỆN MỚI, RIÊNG BIỆT (2026-09-10, trong lúc verify Phương án A
   trên cụm thật): `leader_address` không nhất quán giữa đường LIVE và đường
   CATCH-UP SYNC — gây lệch hash block tạm thời, tự sửa được, nhưng là lỗ
   hổng thật, KHÁC với lỗ hổng gốc ở mục 1-7

### 8.1. Triệu chứng quan sát trực tiếp trên cụm thật

Trong lúc verify Phương án A (xem `note/deploy_hardening_and_incident_drills_2026-09.md`
để biết bối cảnh đầy đủ: 3 node bị halt đúng theo thiết kế, được restart theo
đúng runbook), user báo cáo 🔴 CRITICAL: block #339 lệch hash giữa node-0 (m0)
và node 1/2/3 (m1-m3) trong vài chục giây:

```
m0: hash: 0x2d3ab1...ba37, miner: 0x4a61f5...4979 (= địa chỉ validator node-0)
m1/m2/m3: hash: 0xb60a5c...142b, miner: 0x000...000 (địa chỉ rỗng)
```

Mọi trường KHÁC (transactions, tx order) đều khớp. Vài phút sau, TẤT CẢ 4 node
tự hội tụ về CÙNG 1 phiên bản (`0xb60a5c...`, miner rỗng) — xác nhận qua
`eth_getBlockByNumber` trực tiếp trên cả 4 RPC, KHÔNG phải suy đoán. Không có
`fork_guard`/LAYER-6 abort nào xảy ra.

### 8.2. Đã loại trừ (điều tra qua code thật, không suy đoán)

- **KHÔNG phải panic/crash-loop**: panic `typed-store/src/metrics.rs:70` xuất
  hiện đúng 1 lần/mỗi lần khởi động tiến trình trên cả 4 node — vô hại, không
  liên quan, tự phục hồi ngay ("no reactor running" lúc Tokio runtime chưa kịp
  sẵn sàng cho 1 thread phụ).
- **KHÔNG phải Phương án A gây ra** — cơ chế mới ở mục 1-7 chỉ liên quan
  `next_expected_index` có tiến hay không, không đụng tới field `leader_address`.
- **KHÔNG phải bug ở tầng Go `block_processor_processing.go`'s fallback
  `bp.validatorAddress`** — đã đọc kỹ: comment của hàm này tự mâu thuẫn với
  chính nó ("Falling back to bp.validatorAddress would cause a fork!" nhưng
  code lại lấy `bp.validatorAddress` làm giá trị mặc định) — NHƯNG đã rà soát
  toàn bộ 5 call site thật (`speculative_executor.go` x2,
  `block_processor_sync.go` x3): **cả 5 đều LUÔN truyền 1 override tường
  minh**, nên nhánh mặc định (`bp.validatorAddress`) hiện tại KHÔNG THỂ chạm
  tới được trong production — không phải nguyên nhân của sự cố này (dù vẫn là
  1 "quả mìn" nguy hiểm nên dọn, xem mục 8.4). Phát hiện phụ: file test
  `block_processor_processing_test.go`'s `TestGetLeaderAddress_LeaderOverride`
  KHÔNG hề gọi hàm thật — nó tự chép lại 1 bản logic riêng, và bản chép đó
  SAI khác với code thật (thêm điều kiện loại trừ địa chỉ rỗng mà code thật
  không có) — nên test này xanh (PASS) không chứng minh được gì về hàm thật.
  Đã sửa (mục 8.4).
- Cả `speculative_executor.go` (đường LIVE) và `block_processor_sync.go`
  (đường CATCH-UP SYNC) đều gọi ĐÚNG 1 helper an toàn, dùng chung:
  `bp.GetLeaderAddress(leaderAddress []byte, leaderAuthorIndex uint32)`
  (`block_processor_core.go`) — hàm này xử lý input giống hệt nhau ở cả 2 nơi
  (20 byte hợp lệ → dùng thẳng; khác 20 byte → fallback về địa chỉ 0 tất định,
  KHÔNG bao giờ tự đoán). Vậy 2 đường Go này tự nó **nhất quán và an toàn** —
  bằng chứng cho thấy input mà mỗi đường NHẬN ĐƯỢC từ Rust khác nhau, không
  phải cách Go xử lý input khác nhau.

### 8.3. Nguyên nhân gốc thật (đã xác nhận qua code, còn 1 mắt xích cuối chưa
   chốt 100% — xem "còn mở" bên dưới)

Rust gửi `leader_address` KHÔNG NHẤT QUÁN cho CÙNG 1 commit, tùy theo node
nhận biết commit đó qua đường nào:

- **`Commit::new_with_leader_address(...)`** (constructor CÓ tham số
  leader_address, `meta-consensus/core/src/commit.rs:91`) **không được gọi ở
  bất kỳ đâu trong toàn bộ codebase** (`grep -rn "new_with_leader_address"`
  chỉ khớp đúng định nghĩa của nó) — mọi commit được tạo qua
  `Commit::new(...)` (bare, không có leader_address).
- `linearizer/mod.rs`'s hàm tạo commit local (dòng ~242) dùng đúng
  `Commit::new(...)` bare này khi tạo commit MỚI (branch `final_commit ==
  None`) — nghĩa là field `leader_address` bên trong `Commit`/`TrustedCommit`
  (phần được SERIALIZE, HASH, và GỬI CHO PEER qua `commit_syncer`) LUÔN RỖNG
  tại thời điểm tạo, bất kể node nào tạo.
- Cơ chế BÙ ĐẮP thật sự nằm ở `CommitProcessor::resolve_leader_address`
  (`commit_processor/processor.rs:391`, 4 call site) — tra `leader_author_index`
  trong `epoch_eth_addresses` (map ETH-address theo epoch, tầng app `metanode`,
  KHÔNG có ở tầng `meta-consensus/core`) để gán `subdag.leader_address` — đây
  là field trên `CommittedSubDag` (struct KHÁC, tồn tại NGẮN HẠN, chỉ dùng cho
  lần dispatch NÀY, KHÔNG được serialize/lưu lại/gửi cho peer).
- Hệ quả: **`leader_address` không phải 1 phần của dữ liệu được đồng thuận
  (không nằm trong digest mà DIGEST-GATE so khớp) — nó được từng node tự tính
  lại, riêng lẻ, mỗi lần xử lý commit đó**, dựa trên trạng thái cache cục bộ
  (`epoch_eth_addresses`) của CHÍNH NODE ĐÓ tại đúng thời điểm xử lý. 2 node
  trung thực, xử lý ĐÚNG 1 commit ở 2 THỜI ĐIỂM khác nhau (node-0: xử lý live,
  ngay khi vừa quyết định; node 1/2/3: xử lý qua `commit_syncer` fetch lại
  hàng loạt commit lịch sử trong lúc catch-up dồn dập sau restart) hoàn toàn
  có thể cho ra 2 kết quả resolve khác nhau — ĐẶC BIỆT nếu `epoch_eth_addresses`
  của node đang catch-up chưa kịp có/đúng epoch tương ứng tại đúng lúc đó.

**Còn mở (chưa xác nhận 100%, cần điều tra có kiểm soát thay vì đoán tiếp)**:
lẽ ra nếu `resolve_leader_address` bị gọi và thất bại (nhánh "Committee index
OUT OF BOUNDS"/"Invalid address length"), phải thấy log `warn!`/`error!` có
tiền tố `[LEADER]` — nhưng `grep "\[LEADER\]"` trên CẢ 4 NODE, TOÀN BỘ lịch sử
log, cho ra 0 dòng. `RUST_LOG` mặc định là `info` (xác nhận qua
`ffi.rs`/`EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into())`)
nên `warn!`/`info!` (không phải `trace!`) đáng lẽ phải hiện. Có 2 khả năng
chưa phân biệt được dứt khoát: (a) `resolve_leader_address` LUÔN chỉ chạm
nhánh nhanh-im-lặng (dòng 400, `trace!`, bị lọc) vì `subdag.leader_address`
đã có sẵn 20 byte từ 1 nguồn khác chưa lần ra (mâu thuẫn với việc
`new_with_leader_address` là dead code — cần điều tra thêm xem `certified_commit`
lấy từ đâu ra 20 byte nếu có), hoặc (b) đường dispatch CHO COMMIT CATCH-UP
không hề đi qua 1 trong 4 call site của `resolve_leader_address` trong
`processor.rs` — bỏ qua bước resolve hoàn toàn, gửi thẳng field rỗng cho Go.
Cần thêm log tạm thời có kiểm soát (không phải đoán qua log có sẵn) để chốt
dứt điểm trước khi sửa Rust.

### 8.4. Đã sửa ngay (rủi ro thấp, không đụng consensus-core)

- `execution/cmd/simple_chain/processor/block_processor_processing_test.go`:
  sửa `TestGetLeaderAddress_LeaderOverride` để gọi ĐÚNG logic thật của
  `createBlockFromResults` (chỉ check `len(override) > 0`, không check giá trị
  0 — khớp với comment "CRITICAL FORK-SAFETY" ngay phía trên hàm thật) thay vì
  1 bản chép tay đã lệch. Test đã chạy PASS lại sau khi sửa.

### 8.5. Đề xuất hướng sửa gốc thật sự (CHƯA LÀM — cần quyết định riêng,
   giống mục 6 việc #0 ở trên, KHÔNG phải quyết định kỹ thuật thuần túy vì
   chạm `meta-consensus/core` dùng chung, rủi ro cao)

**Hướng đúng**: làm cho `leader_address` trở thành 1 phần dữ liệu ĐƯỢC ĐỒNG
THUẬN (embed vào `Commit` lúc TẠO, qua `Commit::new_with_leader_address(...)`
đã có sẵn nhưng chưa dùng), thay vì để mỗi node tự tính lại rời rạc sau khi
digest đã chốt. Cách làm: tại `linearizer/mod.rs` dòng ~242 (nơi tạo commit
mới), tra `leader_author_index` → ETH address NGAY LÚC TẠO COMMIT (cần thread
`epoch_eth_addresses` xuống tới `Linearizer`, hiện chỉ có ở tầng app
`metanode`, không có ở tầng `meta-consensus/core` — đây là điểm cần thiết kế
cẩn thận, ranh giới 2 tầng). Một khi `leader_address` nằm TRONG `Commit` được
serialize+digest, `resolve_leader_address`'s nhánh "đã có sẵn 20 byte, bỏ qua"
(dòng 400) trở thành đường ĐI THƯỜNG, còn tra-cứu-theo-index chỉ còn là lưới
an toàn cho dữ liệu cũ (tương thích ngược) — loại bỏ hoàn toàn khả năng 2 node
tính ra 2 giá trị khác nhau cho CÙNG 1 commit.

**KHÔNG làm ngay** trong phiên này: đây là thay đổi ở `meta-consensus/core`
dùng chung (còn cơ bản/rủi ro cao hơn cả `commit_processor/processor.rs` mà
Phương án A vừa sửa), và mắt xích cuối ở mục 8.3 vẫn còn 1 phần chưa chốt
100%. Cần: (1) chốt dứt điểm mắt xích còn mở bằng log có kiểm soát, (2) thiết
kế lại ranh giới `epoch_eth_addresses` giữa `meta-consensus/core` và tầng app,
(3) áp dụng ĐÚNG quy trình đã thống nhất cho vùng code này: code cẩn thận,
test kỹ, nhánh riêng, review trước khi merge — không rush ngay sau khi vừa
tìm ra, đặc biệt khi cùng ngày đã có 1 lần fork thật từ 1 thay đổi vội ở khu
vực liền kề.

### 8.5b. ✅ Đã làm bước NHỎ, AN TOÀN trước (nhánh
    `fix/leader-address-retry-out-of-bounds`, commit `6e9b9acf`) — CHƯA phải
    hướng sửa gốc ở mục 8.5

Thay vì làm ngay bản redesign lớn ở mục 8.5 (đụng `meta-consensus/core` dùng
chung, 6+ call site kể cả test), làm 1 bước tăng dần, an toàn hơn, NẰM HOÀN
TOÀN trong file `processor.rs` (tầng app, đang sửa đổi tích cực sẵn):
2 nhánh "OUT OF BOUNDS"/"Invalid address length" trong `resolve_leader_address`
trước đây `return` NGAY ở lần quan sát ĐẦU TIÊN — khác hẳn nhánh "epoch chưa
có trong cache" ngay bên cạnh, vốn ĐÃ chờ vô thời hạn (mẫu hình đã được coi
là an toàn). Sửa: cho 2 nhánh kia CŨNG chờ có giới hạn (30s) trước khi mới
bỏ cuộc — thu hẹp đáng kể khung thời gian có thể xảy ra lệch (không loại bỏ
hoàn toàn về mặt lý thuyết, vì đây vẫn là tự tính lại RIÊNG LẺ mỗi node, chỉ
là ít có khả năng xảy ra hơn nhiều). Cố tình dùng cửa sổ CÓ GIỚI HẠN (không
vô thời hạn như nhánh "chưa cache") vì 1 cache entry SAI thật thì có thể
không bao giờ tự sửa đúng — chờ vô hạn ở đây có thể tạo ra rủi ro treo MỚI
cho toàn bộ pipeline dispatch, điều tuyệt đối không được phép.

`cargo test` (193 + 185) xanh. Hướng sửa gốc thật ở mục 8.5 (nhúng
`leader_address` vào `Commit` lúc tạo) **vẫn để đó, chưa làm**, cần quy trình
riêng như đã ghi.

**User yêu cầu thêm** (đúng nguyên văn): "check lại kỹ đừng để xung đột các
trường giữa go và rust nhé rust quyết định thì go chỉ cần tuần theo thôi
tránh tính toán độc lập dễ dẫn tới fork". Đã rà soát thêm ở tầng Go
(`block_processor_processing.go` và các file liên quan):
- `blockLeaderAddress` fallback `bp.validatorAddress`: ĐÃ đóng HẲN (không chỉ
  "hiện không ai gọi tới" như mục 8.4 ghi trước đó) — hàm giờ BẮT BUỘC đúng 1
  giá trị override, panic ngay nếu thiếu, thay vì âm thầm tự đoán.
  (commit `b59745e6`)
- `timestampMs`: comment cũ nói "0 → fallback time.Now()" nhưng code thật đã
  panic từ trước (test `ZeroTimestampPanic` xác nhận) — comment sai, đã sửa
  cho đúng thực tế, không phải lỗ hổng thật. (commit `af4d4bbb`)
- GEI (`is_authoritative_gei`/`gei_authority.go`): Go TỰ tính GEI bằng bộ đếm
  tăng dần — đây LÀ Go tính độc lập, NHƯNG an toàn vì bản chất khác hẳn
  leader_address: tăng theo ĐÚNG THỨ TỰ commit mà mọi node đã đồng thuận
  nhận được giống nhau (không phải "đoán" 1 thông tin cần tra cứu như định
  danh validator) — chính tài liệu trong file này còn ghi rõ đây là bản sửa
  CHỦ ĐÍCH để thay thế cách tính GEI cũ của RUST (nguồn gốc fork trước đó).
  Không sửa, không phải vi phạm nguyên tắc.
- `accountRoot`/`stakeStatesRoot`: đến từ `processResults` — Go tự thực thi
  (đúng vai trò execution engine), an toàn vì là hàm thuần từ (state cũ +
  danh sách tx đã được Rust sắp thứ tự) — mọi node thực thi ĐÚNG cùng đầu
  vào nên ra cùng kết quả, khác bản chất "đoán 1 identity".

**Test lại trực tiếp sau khi sửa** (nhánh `fix/leader-address-retry-out-of-bounds`):
`go build`/`go test ./...` sạch (trừ 1 lỗi flake CÓ SẴN, KHÔNG liên quan —
`executor` package's snapshot-manager periodic-trigger tests, xác nhận qua
chạy lại nhiều lần, không đụng file nào trong lần sửa này). Deploy lên cụm
thật, 6 vòng restart node-1 liên tục — cụm luôn khỏe, đồng bộ lại đúng. Cố
tạo tải giao dịch thật để stress-test đúng field `miner` lần này nhưng công
cụ test (`stall_probe_tool`) bị lỗi nonce 2 lần liên tiếp (giới hạn của công
cụ tạm dùng, không phải lỗi chain) — không ép được tải bền vững như lần test
NOMT. Verify được: sau các vòng restart, `hash`/`miner`/`stateRoot` khớp
TUYỆT ĐỐI ở cả 4 node cho block mới nhất — không có bằng chứng regression,
nhưng cũng chưa "bắt tận tay" được chính race gốc (vốn hiếm, cần đúng kịch
bản forward-jump catch-up mới lộ ra, như 2 lần trước đã xảy ra tự nhiên).

### 8.6. Đánh giá mức độ nghiêm trọng

- **Không phải fork vĩnh viễn**: `stateRoot`/`stakeStatesRoot` của TẤT CẢ 4
  node đã hội tụ khớp nhau hoàn toàn ở block #339 và các block sau đó — đã
  verify trực tiếp, không suy đoán.
- **Nhưng LÀ 1 lỗ hổng thật**: có 1 khoảng thời gian ngắn (quan sát được: vài
  chục giây tới vài phút) nơi các node trả lời RPC KHÁC NHAU cho CÙNG 1 số
  block — 1 client bên ngoài đọc đúng lúc đó có thể thấy dữ liệu KHÔNG NHẤT
  QUÁN giữa các node (dù cuối cùng cùng hội tụ đúng). Ngoài ra: nếu field
  `miner` có ý nghĩa kinh tế thật (chia thưởng gas-fee cho validator — xem
  commit `808fe371`), 1 block có `miner` rỗng thay vì địa chỉ thật nghĩa là
  validator đáng lẽ được thưởng cho block đó KHÔNG được ghi nhận đúng — cần
  người có domain knowledge về phần thưởng/kinh tế xác nhận đây có phải hành
  vi đúng ý định hay không.

### 8.7. ✅ ĐÃ LÀM hướng sửa GỐC THẬT (mục 8.5) — nhánh
    `fix/embed-leader-address-in-commit`, commit `47b90c88`

Theo yêu cầu "tiến hành fix luôn" của user: đã nhúng `leader_address` thẳng
vào `Commit` NGAY LÚC TẠO (dùng lại `Commit::new_with_leader_address` — có
sẵn nhưng chưa từng được gọi), thay vì để mỗi node tự tra cứu lại rời rạc
sau đó. Loại bỏ HẲN race ở tầng kiến trúc, không chỉ thu hẹp như mục 8.5b.

**Thiết kế** (ưu tiên giảm tối đa diện ảnh hưởng lên `meta-consensus/core`
dùng chung, đúng bài học từ vụ fork thật cùng ngày):
- `Linearizer`/`CommitObserver`: thêm field optional + hàm SETTER
  (`set_epoch_eth_addresses`), KHÔNG đổi chữ ký `new()` — 0 trong ~16 nơi gọi
  `Linearizer::new`/`CommitObserver::new` (kể cả test/bench) cần sửa.
- Tại đúng chỗ tạo `Commit::new(...)` trong `try_collect_sub_dag_and_commit`:
  tra cứu KHÔNG CHẶN (`try_read()`, không bao giờ chờ khoá vì đây là đường
  nóng tạo commit) `committee.epoch()` + author index của leader; có thì
  dùng `Commit::new_with_leader_address(...)`, không có thì rơi về ĐÚNG hành
  vi cũ (`resolve_leader_address` ở tầng downstream vẫn là lưới an toàn,
  không đổi).
- Chỉ 5 nơi THẬT cần sửa để nối dây `epoch_eth_addresses` xuống tới điểm gọi
  setter (`authority_node/mod.rs`, ngay sau khi tạo `CommitObserver`, trước
  khi nó bị bọc vào `Core`/`CoreThread` — cơ hội DUY NHẤT còn với tới được):
  `ConsensusAuthority::start`/`AuthorityNode::start` thêm đúng 1 tham số MỚI
  (`Option<...>`, `None` giữ nguyên hành vi cũ) → 2 nơi gọi trong test
  (truyền `None`) + 3 nơi gọi thật ở tầng app metanode (truyền
  `node.epoch_eth_addresses.clone()` — ĐÚNG Arc mà `CommitProcessor` đã và
  đang dùng cho việc y hệt này).

**Test mới** (`linearizer/tests.rs`): 2 test — (1) đặt địa chỉ RIÊNG cho
từng authority index (không phải 1 giá trị cố định — bắt được cả lỗi kiểu
"luôn lấy sai index 0"), dựng DAG 10 vòng 4 authority, xác nhận MỖI subdag
trả về đúng địa chỉ của authority THẬT SỰ làm leader vòng đó; (2) không gọi
`set_epoch_eth_addresses` — xác nhận `leader_address` vẫn rỗng y hệt hành vi
cũ (tương thích ngược, không phá bất kỳ caller nào chưa opt-in).

**Test:** `cargo build`/`cargo test` sạch cả 2 crate (`consensus-core`
195/195 — 193 cũ + 2 mới, `metanode` 185/185).

**Verify trực tiếp trên cụm thật:** deploy lên cả 4 node, 5 vòng restart
node-1 liên tục — cụm luôn khỏe, `hash`/`miner`/`stateRoot` khớp TUYỆT ĐỐI cả
4 node. Quan trọng nhất: đọc trực tiếp bộ đếm chẩn đoán `DIAG_LEADER_*` (mục
8.3, đã sửa lỗi log vỡ chữ ở mục 8.5b) trên node thật sau deploy:

```
[DIAG leader-addr] preembedded=39 resolved_ok=76944 waiting_iters=0 out_of_bounds=0 invalid_len=0
```

`resolved_ok=76944` là lịch sử CŨ (tạo từ TRƯỚC khi có bản vá, tất nhiên vẫn
rỗng, phải tra cứu lại theo đường cũ khi replay — đúng như kỳ vọng, dữ liệu
cũ không thể "hồi tố"). `preembedded=39` là các commit MỚI, tạo SAU khi bản
vá đã chạy — đi thẳng qua đường nhanh, không cần tra cứu gì nữa — **bằng
chứng trực tiếp, không suy đoán, rằng cơ chế nhúng đang hoạt động đúng trên
cụm thật**. `out_of_bounds=0`, `invalid_len=0` — chưa gặp lại đúng race gốc
lần nữa (dù đây là race hiếm, không phải bằng chứng tuyệt đối, nhưng cùng
với bằng chứng preembedded ở trên là đủ tự tin).

**Kết luận:** mục 8 (leader_address non-determinism) coi như ĐÃ ĐÓNG ở cấp độ
kiến trúc. `Commit::new_with_leader_address` không còn là dead code.

---

## 9. 🔴 SỰ CỐ THẬT THỨ 2 (2026-09-10, trong lúc đang deploy build chẩn đoán
   cho mục 8): node-0 tự chặn khởi động vì "NOMT root MISMATCH" — HOÀN TOÀN
   KHÁC, KHÔNG LIÊN QUAN tới mục 7-8, nhưng đủ nghiêm trọng để ghi lại đầy đủ,
   kể cả 1 lần tôi SUÝT sửa sai

### 9.1. Triệu chứng

Redeploy build chẩn đoán (mục 8.3, chỉ thêm counter, KHÔNG đổi logic) lên cả
4 node → node-0 (không phải 1/2/3) từ chối khởi động, lặp lại đúng 3 lần rồi
dừng hẳn (circuit breaker `StartLimitBurst=5` hoạt động đúng, không crash-loop
vô hạn):

```
🚨 [INTEGRITY] NOMT account_state root MISMATCH: header=0x71b031408fe4fa25...,
   NOMT=0x0959d5af96915538... — state is corrupted
🛑 Exiting with code 78 (EX_CONFIG) — restore from snapshot to fix.
```

node 1/2/3 khởi động lại hoàn toàn bình thường CÙNG lúc, CÙNG build — xác
nhận đây KHÔNG phải do thay đổi Rust của tôi (chỉ thêm counter, không đổi
control flow) và KHÔNG phải lỗi hệ thống của cả cụm.

### 9.2. Tôi đã SUÝT sửa sai — ghi lại để không ai lặp lại

Đọc code Go (`app_blockchain.go`) thấy có sẵn 1 cơ chế "catch-up bypass":
khi NOMT root không khớp header và không tìm được block khớp nào trong
LevelDB (cả tìm lùi lẫn tìm tới), code này KHÔNG coi là lỗi — nó gọi
`ChainState.SetFutureNomtRoot(...)` và log "Preserving NOMT state... registering
future unaligned root for catch-up bypass", ngụ ý sẽ tự khớp lại sau. Tôi đã
VIẾT 1 bản sửa cho `startup_integrity_check.go` để CHECK 4 công nhận
`futureNomtRoot` này là ngoại lệ hợp lệ (giống hệt cách nó đã công nhận
`isSnapshotRecovery`), tức là cho phép node-0 khởi động dù mismatch.

**Trước khi build/deploy bản sửa đó, kiểm tra thêm 1 bước (đúng tinh thần
"test kỹ" user yêu cầu) thì phát hiện**: `ChainState.GetFutureNomtRoot()`
**không được gọi ở BẤT KỲ ĐÂU khác trong toàn bộ codebase** (`grep -rn
"GetFutureNomtRoot"` chỉ khớp định nghĩa + đúng đoạn sửa của tôi). Nghĩa là
cờ "catch-up bypass" này được ĐẶT nhưng KHÔNG BAO GIỜ ĐƯỢC ĐỌC LẠI bởi bất kỳ
logic catch-up/execution thật nào — lời hứa "sẽ tự khớp lại khi có block mới"
trong comment KHÔNG hề được nối dây tới bất cứ thứ gì thật sự kiểm tra hay xoá
cờ đó. Nếu tôi merge bản sửa, node-0 sẽ khởi động lại với state có khả năng
THẬT SỰ sai, và KHÔNG có bất kỳ cơ chế nào trong hệ thống sẽ phát hiện hay bắt
lại sai lầm đó sau này — chính xác kiểu lỗi mà toàn bộ mục 1-8 của tài liệu
này đang cảnh báo (tin 1 "cờ tạm hoãn" chưa được xác minh thật). **Đã revert
ngay (`git checkout --`) trước khi build/deploy, không commit.**

Kết luận: `startup_integrity_check.go` đang làm ĐÚNG — chặn khởi động là hành
vi AN TOÀN, không phải bug cần "nới lỏng". Vấn đề thật nằm ở CHỖ KHÁC (mục
9.3).

### 9.3. Nguyên nhân gốc thật (chưa chốt hoàn toàn — cần điều tra sâu về NOMT,
   ngoài phạm vi phiên này)

Truy log qua `journalctl` (KHÔNG có trong `execution.log` — phát hiện phụ:
các dòng log CHECK 1-5/`[STARTUP] NOMT Root MISMATCH` chỉ xuất hiện qua
`journalctl -u metanode-execution-N`, không hề nằm trong file `execution.log`
dù cùng 1 tiến trình — đáng chú ý cho lần điều tra sau, đừng chỉ grep
execution.log):

- `11:25:33`: process CŨ của node-0 dừng — log cho thấy chuỗi shutdown đầy đủ,
  "Clean shutdown sentinel written", systemd xác nhận "Deactivated successfully"
  (không phải SIGKILL).
- `11:26:35`: process MỚI khởi động lần 1, ĐỌC ĐƯỢC sentinel ("✅ Clean shutdown
  detected — skipping expensive integrity checks") — nhưng **VẪN exit code 78
  giống hệt** các lần sau. Xác nhận: CHECK 4 (so khớp NOMT-vs-header) chạy
  KHÔNG ĐIỀU KIỆN, không phụ thuộc "clean shutdown" (cờ đó chỉ giảm ĐỘ SÂU của
  CHECK 2 — dò block chain — từ 100 xuống 10 block, không tắt CHECK 4). Sentinel
  bị dùng 1 lần rồi tự xoá (đúng thiết kế), nên các lần khởi động SAU đó (2, 3)
  hiển nhiên báo "No clean shutdown indicator" — không phải bằng chứng của 1
  lần crash thật khác, chỉ là hệ quả của lần đầu đã tiêu thụ sentinel.
- Tìm ngược từ block #360 xuống tới genesis, và tìm xuôi #361-#460, trong
  LevelDB đều KHÔNG có block nào có header khớp với NOMT root hiện tại
  (0x0959d5af...) — root này chưa từng khớp với bất kỳ block cụ thể nào trong
  toàn bộ lịch sử LƯU TRỮ của chính node-0.
- **Điều này xảy ra SAU 1 lần shutdown sạch (không SIGKILL)** — nghĩa là: hoặc
  (a) NOMT (cây trie ghi đè theo trang, page cache 4GB) có 1 khoảng không đồng
  bộ thật giữa việc flush cây trie và việc ghi header block, ngay cả khi chuỗi
  shutdown báo "hoàn tất" thành công, hoặc (b) có 1 lỗi tính header
  `AccountStatesRoot` không khớp với root NOMT thực tế lưu, độc lập với việc
  shutdown sạch hay không.
- node-0 đã chạy liên tục ~28 phút CPU time, khối lượng ghi rất lớn (hàng
  triệu commit) trước lần restart này — khác biệt lớn nhất so với node 1/2/3
  (vừa được tôi restart cách đây không lâu, ít "tích luỹ" hơn) — có thể là
  yếu tố liên quan (tải ghi lớn/lâu dài) nhưng CHƯA xác nhận là nguyên nhân
  thật, chỉ là tương quan quan sát được.

### 9.4. Hiện trạng & khuyến nghị

- Cụm vẫn khỏe: 3/4 node (đúng ngưỡng chịu lỗi BFT f=1 cho n=4) đồng thuận
  bình thường. node-0 đã tự dừng AN TOÀN (`systemctl status` = `failed`,
  KHÔNG crash-loop, không tốn tài nguyên) sau đúng 3 lần thử — không phải sự
  cố khẩn cấp cho vận hành cụm ngay bây giờ.
- **CHƯA đụng dữ liệu node-0** — không reset, không xoá, đúng nguyên tắc đã
  thống nhất suốt phiên này.
- Node-0 tự đề xuất 2 hướng phục hồi AN TOÀN, KHÔNG PHÁ DỮ LIỆU 3 node còn
  lại: restore từ snapshot, hoặc resync từ peer — cần quyết định cụ thể + xác
  nhận từ user trước khi thực hiện (không tự ý làm).
- Nguyên nhân gốc thật của việc NOMT root lệch header sau shutdown sạch là 1
  câu hỏi RIÊNG, SÂU hơn, thuộc tầng Go/NOMT — KHÔNG cùng phạm vi với mục 1-8
  (leader_address, Phương án A) — nên điều tra như 1 việc độc lập, không rush
  chung với các việc đang mở khác trong tài liệu này.

### 9.5. ✅ Đã tìm ra nguyên nhân gốc THẬT (đọc code, không suy đoán) và ĐÃ SỬA
   (commit `e30a0239`)

`NomtStateTrie.Commit()` (`execution/pkg/trie/nomt_state_trie.go`) tính và
TRẢ VỀ root mới của 1 block NGAY LẬP TỨC (đồng bộ) — giá trị này đi thẳng vào
header block (ghi LevelDB riêng, đồng bộ) — nhưng chỉ LƯU TẠM việc ghi
xuống đĩa thật sự vào `n.pendingFinishedSession`, KHÔNG ghi ngay. Việc ghi
thật (`CommitPayload`) được hoãn lại, chỉ chạy khi: (a) block TIẾP THEO gọi
`Commit()` (cơ chế "NOMT-SYNC-DRAIN" có sẵn, tối ưu hiệu năng hợp lý — chồng
lấp ghi đĩa chậm của block N với việc CPU xử lý block N+1), hoặc (b) gọi tay
`CommitPayload()`.

**Lỗ hổng**: BLOCK CUỐI CÙNG trước bất kỳ lần shutdown nào không có "block
tiếp theo" để tự kích hoạt việc ghi hoãn đó. `WaitForPersistence()` (hàm MỌI
đường shutdown thật đều gọi) có sẵn bước `WaitCommitPayload()` nhưng hàm đó
CHỈ chờ những commit ĐÃ được giao cho goroutine ghi bất đồng bộ — 1 session
CHƯA TỪNG được giao (vì chưa có block tiếp theo kích hoạt) thì
`WaitCommitPayload()` không hề biết tới. Sau đó, đường shutdown thật
(`nomt_ffi.Handle.Close()`, gọi qua `CloseNomtDB()` — đã xác nhận qua
`globalNomtHandles` đúng là kiểu `*nomt_ffi.Handle`, không phải wrapper Go
`*NomtStateTrie`) **`.Abort()` tất cả session đang chờ "để giải phóng bộ nhớ
an toàn"** — âm thầm VỨT BỎ đúng phần ghi đĩa thật của block cuối cùng, trên
MỌI lần shutdown (kể cả shutdown sạch), bất cứ khi nào thời điểm dừng rơi
đúng vào khoảng trống đó — trong khi header (đã ghi trước đó) vẫn khẳng định
root như thể đã commit xong.

**Bản sửa**: trong `WaitForPersistence()`, gọi `trie.CommitPayload()` (hàm
CÓ SẴN, đã dùng ở 13+ nơi khác trong codebase cho đúng mục đích "xả session
đang chờ", an toàn khi gọi dù không có gì đang chờ) để XẢ (ghi thật) bất cứ
gì còn treo, TRƯỚC KHI gọi `WaitCommitPayload()` như cũ — cho cả
AccountStateDB lẫn StakeStateDB. Đây chính là nửa còn thiếu của đúng cơ chế
mà comment gốc của hàm này đã từng cảnh báo (nguyên văn: "otherwise the
snapshot captures an incomplete NOMT state, causing a StateRoot mismatch!")
— nghĩa là lỗ hổng này VỐN ĐÃ ảnh hưởng cả đường SNAPSHOT, không chỉ shutdown,
chỉ là chưa ai nối trọn vẹn.

**Đã test**: `go build ./...` sạch, `go test ./...` (toàn bộ execution
module) xanh, không có test nào fail. **Đã verify TRỰC TIẾP trên cụm thật**:
build + deploy bản sửa lên cả 4 node, sau đó chạy **17 vòng restart liên tục**
trên node 1/2/3 (`systemctl restart` lặp lại, mỗi lần đợi RPC sống lại rồi
kiểm tra log CHECK 4/5) — **TẤT CẢ 17/17 lần đều PASS integrity check sạch,
0 lần NOMT-vs-header lệch**. Hạn chế của bài test này: chain lúc test khá
rảnh (thử gửi thêm giao dịch qua `stall_probe_tool` nhưng bị lệch nonce cục
bộ ở phía tool, không phải lỗi chain, nên chưa tạo được tải liên tục thật khi
restart) — nghĩa là chưa ép được đúng kịch bản "vừa xử lý xong 1 block, restart
ngay tắp lự" ở tần suất cao như lúc node-0 gặp sự cố gốc (28 phút CPU liên
tục, hàng triệu commit). Cơ chế sửa vẫn đúng về mặt logic (dùng lại hàm đã
được kiểm chứng qua 13+ nơi khác), nhưng nếu muốn có bằng chứng THẬT SỰ dưới
tải cao, cần 1 bài test riêng có tạo giao dịch thật liên tục trong lúc restart.

**CHƯA sửa được**: dữ liệu ĐÃ MẤT của node-0 (từ TRƯỚC khi có bản vá này) —
bản vá chỉ ngăn KHÔNG TÁI DIỄN trong tương lai, không phục hồi được commit đã
mất. node-0 vẫn cần quyết định riêng: resync từ peer, hay restore snapshot.

### 9.7. ✅ Test lại LẦN 2, dưới TẢI THẬT — đóng đúng lỗ hổng đã ghi ở mục 9.5

User xác nhận đang trong giai đoạn test, dữ liệu không quan trọng, có thể xoá
làm lại — nên đã `ansible_deploy.sh --reset-all --open-ports` (genesis mới
sạch, cả 4 node khỏe, có bản vá NOMT), rồi tạo TẢI GIAO DỊCH THẬT LIÊN TỤC
bằng `stall_probe_tool` (1 tài khoản đã cấp vốn sẵn trong genesis, gửi tuần
tự đúng nonce, ~500 tx, mỗi tx đợi xác nhận trước khi gửi tiếp) trong lúc
restart node-1 lặp lại **8 lần liên tục** — lần này block height THẬT SỰ
đang tăng liên tục trong lúc restart (0xb → 0x1a), đúng sát kịch bản sự cố
gốc của node-0 (đang xử lý liên tục, không rảnh).

**Kết quả: 8/8 lần restart dưới tải thật đều PASS integrity check sạch, 0
lần lệch NOMT-vs-header.** Cộng dồn với 17 lần test trước (chain rảnh) =
**25/25 lần restart, 0 lỗi**. Hạn chế đã ghi ở mục 9.5 (chưa test dưới tải
thật) coi như đã được lấp — bản vá đứng vững dưới cả điều kiện gần với sự cố
gốc nhất mà phiên này tạo được.

Cụm cuối phiên: 4/4 node khỏe, cùng chiều cao (chênh lệch 1 block do độ trễ
lan truyền bình thường), đồng thuận đúng.

### 9.6. ✅ node-0 đã phục hồi (resync từ peer, không đụng genesis/3 node kia)

Trước khi chạy `ansible_deploy.sh --reset-all --only-node 0` (theo đúng gợi ý
trong log lỗi của chính node), kiểm tra kỹ code thì phát hiện 1 rủi ro thật
nghiêm trọng: bước tạo genesis/keys trong role `local_build` **không hề lọc
theo `target_node`** — nghĩa là `--reset-all` (dù có `--only-node`) sẽ **tạo
lại genesis cho TOÀN CỤM**, có nguy cơ làm hỏng luôn node 1/2/3 đang khỏe.
**KHÔNG dùng lệnh đó.**

Thay vào đó: xác nhận `config/genesis.json` + `keys/` của node-0 nằm TÁCH
BIỆT khỏi `data/` (nơi chứa state hỏng) — chỉ **đổi tên (không xoá)**
`/opt/metanode/node-0/data` → `data.corrupted-backup-20260910` (có thể khôi
phục lại nếu cần), khởi động lại service. Node-0 tự resync từ genesis, bắt
kịp cả cụm (block #362, hash/stateRoot khớp node-2/node-3) chỉ trong vài
giây, qua đúng cơ chế catch-up sync đã có sẵn (cùng cơ chế node 1/2/3 dùng cả
phiên nay) — không đụng gì tới genesis, keys, hay 3 node còn lại.

**Cụm hiện tại: 4/4 node khỏe, đồng thuận đúng.**

## 10. 🔴 SỰ CỐ THẬT THỨ 3 (2026-09-10/11, phát hiện khi deploy `dev` mới nhất lên cụm thật của đồng nghiệp và re-run `node_chaos_restart`)

### 10.1. Triệu chứng

Deploy `8e9c1814` (đã gộp cả 3 fix ở mục 7-9) lên cụm thật 5-node của "nhat"
(192.168.1.234/230) rồi chạy `node_chaos_restart` — chặng đầu (restart node
0) PASS sạch, nhưng chặng 2 (restart node 1) thì **toàn bộ cụm ngừng tạo
block mới vĩnh viễn**: `eth_blockNumber` đứng im ở #120 hơn 20 phút không tự
phục hồi, kể cả các node **chưa hề bị restart trong bài test này** (node-3).
RPC vẫn trả lời bình thường, service vẫn "active" — không phải crash.

**Rollback về commit cũ (`d5cbbdc9`, trước cả 3 fix hôm nay) cũng KHÔNG hết
treo** — cùng hiện tượng lặp lại y hệt dưới code cũ, với dữ liệu được giữ
nguyên (`Keep Data: true`). Điều này loại trừ hoàn toàn khả năng đây là do 3
fix hôm nay gây ra — bug đã có sẵn từ trước, chỉ là hôm nay mới tạo đúng điều
kiện để lộ ra.

### 10.2. Nguyên nhân gốc — xác nhận bằng log thật, không suy đoán

Tái hiện được y hệt trên cụm test cục bộ (232) của chính phiên này. Dump
goroutine Go xác nhận `commitWorker`/`processRustEpochData`
(`block_processor_commit.go`/`block_processor_network.go`) đứng im từ lúc
process khởi động, trong khi log Rust của cùng node cho thấy DAG/commit-
syncer vẫn tiến triển bình thường (`vote.index`, `CommitRange` vẫn tăng) —
chỉ riêng cầu nối Rust→Go bị đứt.

Log gốc tìm được (`node-2`, xuất hiện lặp đi lặp lại đúng 1 commit):
```
🚨 [FATAL] DeliveryManager closed response channel without replying.
🚨 [FATAL] Failed to send commit to DeliveryManager: channel closed
Execution failure during block delivery. Cannot recover. Error: Missing
transaction payload for digest ... — TxPayloadCache has no entry (most
likely a post-restart cache-miss). Refusing to build a block with a
silently-dropped transaction.
```

Chuỗi nguyên nhân đầy đủ:

1. `TxPayloadCache` (`consensus_core::get_global_tx_cache()`) chỉ lưu RAM,
   mất sạch khi process restart (đã có comment giải thích rõ trong code từ
   một fix ngày 2026-09-08, sau một sự cố fork thật khi xử lý này còn silent-
   drop giao dịch thay vì bail).
2. Có sẵn cơ chế "hỏi peer trước khi bó tay"
   (`ensure_tx_payloads_cached` → `coordination_hub`'s `TxFetcherFn`, được
   wire đúng trong `authority_node/mod.rs` — **đã xác nhận trực tiếp bằng
   diag logging thêm tạm thời (`TX-PAYLOAD-RECOVERY-DIAG`) là cơ chế này CÓ
   chạy, CÓ gọi peer, nhưng genuinely không peer nào còn giữ payload này**
   (`1/1 digest(s) still missing` sau mỗi lần thử). Không phải bug ở khâu
   này — đúng như doc comment của `TxFetcherFn` đã cảnh báo trước: "nếu cả
   cụm restart cùng lúc, mất luôn ở mọi peer, cơ chế này không cứu được".
3. Lỗi ở bước 1 lan tới `BlockDeliveryManager::run()` (`block_delivery.rs`),
   nơi cũ `panic!()` ngay khi gặp Err bất kỳ.
4. Task này được `tokio::spawn` riêng (không nằm trong call-stack chính) nên
   panic của nó **không hề bị bắt** bởi outer resilience loop đã thêm ở
   commit `08780279` (chính commit đó mô tả một triệu chứng gần như y hệt:
   "Rust consensus im lặng vĩnh viễn, Go vẫn chạy, RPC vẫn trả lời, zero
   further restart attempts" — nhưng cơ chế sửa ở đó chỉ bọc call-stack
   chính, không bọc được task nền này).

Kết quả: node "sống" nhưng pipeline chuyển giao commit Rust→Go chết vĩnh
viễn, không có gì giám sát hay tự phục hồi — đúng y hệt hiện tượng quan sát
được trên cả 2 cụm.

### 10.3. Vì sao dễ xảy ra đúng lúc deploy code mới

Kịch bản kích hoạt phổ biến nhất: **restart toàn cụm cùng lúc** (chính là
những gì `ansible_deploy.sh --start` không có `--only-node` làm — thao tác
deploy code mới thường quy!). Nếu đúng lúc đó có 1 giao dịch đang "bay" giữa
các node (đã được DAG order nhưng chưa kịp lan truyền payload đầy đủ tới mọi
nơi), restart đồng loạt xóa sạch cache ở MỌI node cùng lúc → không còn ai
giữ payload đó → treo vĩnh viễn, không do lỗi code mới nào cả.

### 10.4. Fix (nhánh `fix/block-delivery-halt-not-panic-on-tx-payload-loss`)

**Không sửa bước 1-2** (bail + peer-recovery đã đúng, đã xác nhận hoạt động
đúng thiết kế). Chỉ sửa bước 3: `BlockDeliveryManager::run()` không còn
`panic!()` nữa mà gọi `deliver_with_halt_retry()` — log một marker riêng,
dễ grep (`🛑🚨 [CONSENSUS-HALT-TX-PAYLOAD-LOST]`, khác với
`CONSENSUS-HALT-SUSPECTED-DIVERGENCE` của mục 8 vì đây là mất dữ liệu ĐÃ XÁC
NHẬN chứ không phải nghi ngờ phân nhánh — runbook xử lý khác hẳn), rồi retry
gọi lại `send_committed_subdag` (bao gồm cả bước peer-recovery) mỗi 10s, vô
thời hạn, thay vì chết hẳn — đúng tinh thần "thà pending chứ tuyệt đối không
fork" đã áp dụng ở mục 8. Nếu một peer từng tạm thời mất payload sau đó lấy
lại được (trường hợp phổ biến: chỉ 1 node restart, các node khác vẫn sống),
node sẽ **tự phục hồi** và log `CONSENSUS-HALT-TX-PAYLOAD-LOST-RECOVERED`.

Mở rộng `start_monitors.sh` (Health Monitor) để gửi cảnh báo Telegram riêng
cho marker mới này, kèm runbook riêng (không khuyên "kiểm tra fork trước khi
restart" như mục 8's marker, vì ở đây không có nghi ngờ fork — mọi node đều
đồng ý dữ liệu đã mất thật) và tự gửi thông báo "đã phục hồi" khi thấy dòng
RECOVERED.

### 10.5. Xác minh live

- Tái hiện được lỗi gốc trên cụm test 232 (cùng đúng commit/digest bị kẹt).
- Deploy bản vá lên cụm 232 đang kẹt sẵn: panic cũ (`Execution failure
  during block delivery`) ngừng xuất hiện hoàn toàn (dừng ở đúng 33 lần từ
  trước khi vá, không tăng thêm), thay vào đó marker halt mới xuất hiện đều
  đặn mỗi ~2 phút, **process không hề crash, RPC vẫn sống bình thường suốt**
  — đúng như thiết kế.
- `--reset-all` để lấy baseline sạch, chạy lại tải giao dịch thật liên tục
  qua 6 lần restart xen kẽ node-0/node-1 — cụm khỏe mạnh xuyên suốt, block
  height tăng đều, 4/4 node đồng bộ, không crash, không kẹt lần nào (đợt
  test này không tình cờ tái hiện đúng race mất-payload — vốn dĩ có tính
  xác suất theo thời điểm restart — nhưng hành vi bình thường không hề bị
  ảnh hưởng bởi bản vá).
- `cargo test -p metanode --release`: 185/185. `cargo test -p consensus-core
  --release`: 195/195 (không đụng crate này, chạy lại để chắc chắn không có
  tác dụng phụ).

**Chưa merge vào `dev`, chưa đụng lại máy 234** (cụm của nhat) — theo đúng
nguyên tắc "không tự ý quyết định trên môi trường của người khác", chờ sau
khi merge `dev` xong và có xác nhận từ user mới quay lại xử lý.

## 11. 🟡 THIẾT KẾ MỚI (2026-09-11, theo yêu cầu user): Quorum-Certified Skip cho tx-payload mất vĩnh viễn TOÀN CỤM

### 11.1. Vấn đề mục 10's fix CHƯA giải quyết được

`deliver_with_halt_retry` (mục 10) retry vô thời hạn khi payload mất — an
toàn (không fork) nhưng nếu **mất thật ở MỌI node cùng lúc** (kịch bản thực
tế: mất điện cả cụm, không chỉ deploy code), cụm **treo vĩnh viễn**, không
có cách tự phục hồi nào khác ngoài `--reset-all` (mất sạch dữ liệu).

User hỏi đúng trọng tâm: giao dịch đó **chưa từng được áp dụng vào state**
(build block thất bại trước khi apply) — nên về logic, nếu THẬT SỰ không ai
còn giữ, bỏ qua nó là an toàn (không có gì để so sánh/fork, vì chưa ai áp
dụng gì cả). Vấn đề chỉ là: làm sao BIẾT CHẮC "không ai còn giữ" mà không
tự đoán một mình (rủi ro: 1 node khác chỉ tạm mất kết nối, không phải mất
dữ liệu thật — nếu tự ý bỏ qua rồi node đó nối lại, 2 bên có state khác
nhau thật).

Đối chiếu với Sui thật (đã dẫn trong mục 4.5): "Network Stall Resolution"
(2026-03) — Sui **cũng không tự động bỏ qua**, mà "halted rather than
proceed unilaterally", phục hồi qua xác minh CÓ CON NGƯỜI, không tự đoán.

### 11.2. Thiết kế: Quorum-Certified Payload-Loss Attestation

Nguyên tắc: quyết định "bỏ qua giao dịch X" phải **tự nó đi qua một vòng
đồng thuận nhỏ** (thu thập đủ 2f+1 stake xác nhận), để MỌI node honest đi
đến đúng 1 kết luận giống hệt nhau — an toàn structurally, không phải vì tin
tưởng 1 node tự phán đoán.

**Các thành phần:**

1. **`PayloadLossAttestation`** — thông điệp mới, ký bằng `protocol_keypair`
   sẵn có (tái dùng đúng pattern `compute_block_signature`/`IntentMessage`
   trong `block.rs`, thêm 1 `IntentScope` mới để domain-separate, không đụng
   chữ ký hiện có). Nội dung: `{ commit_index, tx_digest, authority_index }`
   — nghĩa là "tôi (authority N) xác nhận: đã tự kiểm tra cache CỤC BỘ +
   hỏi hết các peer tôi liên lạc được, không ai (kể cả tôi) còn giữ payload
   của digest này cho commit này."

2. **RPC mới** (mở rộng `authority_service`, cùng tầng với `fetch_transactions`
   sẵn có): `attest_payload_loss(commit_index, tx_digest) -> AttestationOrPayload`
   — nếu node được hỏi CÓ payload, trả về payload luôn (giúp cả trường hợp
   phục hồi bình thường nhanh hơn); nếu KHÔNG có, trả về `PayloadLossAttestation`
   đã ký.

3. **Thu thập quorum** — tái dùng `StakeAggregator<QuorumThreshold>`
   (`stake_aggregator.rs`, cơ chế CÓ SẴN, đang dùng cho fastpath transaction
   certification trong `transaction_certifier.rs` — không viết lại từ đầu).
   Khi đủ 2f+1 stake cùng ký xác nhận "mất", node gộp thành 1
   **QuorumCertificate** (danh sách chữ ký + stake đã đạt ngưỡng).

4. **Lan truyền chứng thực** — QuorumCertificate được broadcast cho mọi
   node khác; bất kỳ node nào NHẬN được (tự verify chữ ký, không cần tự thu
   thập lại) đều có thể tin và áp dụng — giống hệt cách `CertifiedBlock` đã
   lan truyền trong hệ thống hiện tại.

5. **Áp dụng an toàn (fork-safety cốt lõi)** — mọi node có QuorumCertificate
   hợp lệ cho đúng `(commit_index, tx_digest)` sẽ loại bỏ CHÍNH XÁC giao
   dịch đó khỏi `build_sorted_transactions`'s danh sách theo CÙNG một quy
   tắc xác định (deterministic) → mọi node tính ra CÙNG 1 danh sách giao
   dịch còn lại → CÙNG 1 block, CÙNG 1 hash. Không có gì để đoán.

6. **Kích hoạt: CÓ CON NGƯỜI, không tự động** (v1, đúng tiền lệ Sui) — cơ
   chế thu thập attestation CHỈ bắt đầu khi operator chủ động gọi (CLI/FFI
   admin command), SAU KHI đã xác nhận qua alert `CONSENSUS-HALT-TX-PAYLOAD-LOST`
   (mục 10) rằng đây là tình trạng kẹt lâu dài thật, không phải thoáng qua.
   Không tự động trigger sau vài phút — tránh đúng rủi ro "quá vội kết luận
   mất thật trong khi chỉ là mất kết nối tạm thời".

### 11.3. Blast radius & kế hoạch test

Đụng `meta-consensus/core` (rủi ro cao nhất, như mọi lần trong tài liệu
này) — cần: unit test cho `StakeAggregator` reuse, unit test ký/verify
attestation, test tích hợp mô phỏng "toàn cụm mất payload cùng lúc" trên
cụm 232 (tái hiện lại đúng kịch bản mục 10 nhưng KHÔNG có node nào còn giữ
payload — hiện tại kịch bản test đã có sẵn cách tái hiện), xác nhận: (a)
quorum thu đúng, (b) sau khi certified, mọi node ra cùng 1 block/hash, (c)
KHÔNG certified nếu chưa đủ quorum (một số node chưa trả lời) — cụm vẫn
đúng đắn chờ tiếp, không tự ý đoán non.

**Trạng thái: THIẾT KẾ, CHƯA IMPLEMENT.** Việc lớn, cần làm cẩn thận qua
nhiều bước, không vội trong 1 lần — implement + test từng phần một, đúng
tinh thần đã áp dụng suốt các mục 7-10.

### 11.4. CẬP NHẬT (2026-09-11, cuối ngày): đã implement, đã tìm thấy 2 lỗ hổng fork thật, đã vá cả 2

Đã implement đầy đủ (6 increment), merge vào `dev` (`899acf35`), và xác
minh sống trên cả cụm local 232 lẫn cụm thật 234/230 qua CI chính thức —
chi tiết đầy đủ trong memory `project_payload_loss_live_test_status.md` và
`project_consensus_halt_not_guess_phuong_an_a.md` mục 12-13, không lặp lại
ở đây. Tóm tắt 2 điểm quan trọng nhất cho ai đọc thiết kế này sau:

**Lỗ hổng #1 (tìm thấy TRƯỚC KHI merge, qua test sống)**: bản đầu tiên chỉ
kiểm tra `TxPayloadCache` (RAM, có LRU-evict) để quyết định "mất" — một node
đã THỰC SỰ thực thi+commit giao dịch, rồi cache bị evict sau đó, sẽ
"trung thực nhưng sai" xác nhận "mất". Tái hiện fork thật. Vá bằng 2 phần:
(a) registry `STUCK_CLAIMS` — chỉ node ĐANG THỰC SỰ kẹt lại đúng claim này
mới được ký "mất"; (b) cơ chế phục hồi thật — tái dùng hạ tầng
`ExecutableBlock` đã lưu sẵn trên đĩa của peer + RPC sync đã có sẵn cho
SyncOnly node, tự động lấy nguyên khối đã build từ peer trước khi bao giờ
cần đến quorum-skip.

**Lỗ hổng #2 (tìm thấy SAU KHI merge, dùng thật lần đầu trên local 232)**:
mục 11.3.c ở trên đã tiên đoán đúng nguyên tắc cần có ("KHÔNG certified nếu
chưa đủ quorum — một số node chưa trả lời — cụm vẫn đúng đắn chờ tiếp,
không tự ý đoán non") nhưng bản implement ban đầu KHÔNG tuân theo đúng
nguyên tắc này: 1 peer bị timeout khi hỏi (do đang gặp sự cố khác, không
phải do nó thực sự "abstain") bị âm thầm loại khỏi phép tính quorum, y hệt
cách xử lý 1 chữ ký sai hỏng — trong khi 2 trường hợp khác hẳn nhau (peer
KHÔNG trả lời = chưa biết gì, không phải "đã trả lời và không có ý kiến").
Tái hiện fork thật lần 2 y hệt kịch bản 11.3.c cảnh báo trước. Vá bằng 2
phần: (a) thử lại rộng rãi (5 lần, cách nhau 10s) trước khi coi 1 peer là
"không liên lạc được"; (b) chỉ sau khi hết thử, mới cho phép tiếp tục theo
đa số NẾU tổng stake của các peer vẫn im lặng không vượt quá ngưỡng chịu
lỗi f có sẵn của committee (`total_stake - quorum_threshold`) — giữ đúng
khả năng chịu lỗi BFT bình thường của cả hệ thống thay vì đòi hỏi TẤT CẢ
peer luôn phải trả lời (bản vá đầu tiên định làm vậy, bị user chỉ ra đúng
là sẽ phá vỡ khả năng chịu lỗi của cả blockchain, sửa lại ngay).

Vá lỗ hổng #2: branch `fix/payload-loss-attestation-unresponsive-peer-fork`
(commit `eb562482`), **CHƯA merge vào `dev`**, đã xác minh sống fork-free
qua 3 kịch bản trên local 232 (bình thường / 1 peer ngừng-rồi-thử-lại-thành-công /
1 peer ngừng-và-vẫn-tiếp-tục-đúng-theo-đa-số).
