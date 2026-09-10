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
