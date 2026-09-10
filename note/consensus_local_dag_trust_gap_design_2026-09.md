# Thiết kế: đóng lỗ hổng "tin dữ liệu DAG cục bộ không có bằng chứng mật mã"

**Trạng thái: CHỈ LÀ THIẾT KẾ, CHƯA CODE.** Tài liệu này phân tích kỹ nguyên
nhân gốc thật sự của sự cố fork ngày 2026-09-10 (xem
`note/deploy_hardening_and_incident_drills_2026-09.md` mục 7 để biết bối
cảnh sự cố gốc) và đề xuất hướng sửa kiến trúc — **không phải chỉnh 1 hằng
số thời gian**. Không triển khai gì từ tài liệu này cho tới khi được review
và đồng ý rõ ràng.

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

## 5. Đề xuất kiến trúc (2 lớp, độc lập, bổ trợ nhau)

### Lớp 1 — Xác minh lại chữ ký khi tin dữ liệu đọc từ đĩa cho quyết định an
toàn-quan-trọng (ưu tiên cao, phạm vi hẹp)

**Không** xác minh lại MỌI lần đọc đĩa (quá tốn kém, phá vỡ lý do thiết kế
cache RAM ngay từ đầu). Chỉ xác minh lại chữ ký cho đúng những block nằm
trong chuỗi nhân quả dẫn tới 1 quyết định **"ân xá"** (`gap_recovery_bypass_ceiling`)
— đây vốn đã là đường XỬ LÝ HIẾM, có sẵn ngân sách thời gian/CPU (bài test
thật cho thấy vài phút tới hàng chục phút mới xảy ra 1 lần), nên trả thêm
chi phí xác minh chữ ký ở đúng thời điểm này là hoàn toàn chấp nhận được.

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
  operator ngay, không chỉ âm thầm bỏ qua), và áp dụng lớp 2 dưới đây.

### Lớp 2 — Coi "vừa khởi động lại sau khi dừng không sạch" là điều kiện
kích hoạt bắt buộc phải re-sync từ peer, không được tự quyết định cục bộ

Mở rộng khái niệm hiện có `COLD-START-BYPASS`/`startup_sync_active` (đã
dùng cho node hoàn toàn mới) sang thêm 1 trường hợp: **node vừa phục hồi
sau 1 lần dừng KHÔNG SẠCH** (process bị SIGKILL/panic/crash, không phải
`systemctl stop` bình thường qua SIGTERM). Cách phát hiện: ghi 1 "cờ bẩn"
(dirty flag) xuống đĩa NGAY KHI khởi động, xóa cờ đó CHỈ KHI dừng sạch qua
đường tắt bình thường (SIGTERM handler) — giống hệt filesystem journal
dùng "dirty bit". Nếu khởi động lên mà thấy cờ vẫn còn (nghĩa là lần trước
dừng không qua đường sạch), coi node đó như `is_catching_up()=true` bắt
buộc, chặn hoàn toàn `decided_with_local_blocks`/amnesty cho tới khi hoàn
tất 1 vòng re-sync xác minh chữ ký thật với ít nhất 1 peer — đúng tinh thần
`COLD-START-BYPASS` đã có, chỉ mở rộng điều kiện kích hoạt.

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

1. **Dựng lại chính xác cách dữ liệu trên đĩa của node-3 bị sai** (nếu còn
   truy được — dữ liệu gốc của sự cố đã bị xóa khi `--reset-all` sau đó, xem
   mục "Đã xử lý" trong tài liệu incident chính; cần 1 lần tái hiện MỚI, có
   kiểm soát, KHÔNG xóa dữ liệu ngay khi phát hiện, để bắt tận tay byte nào
   sai so với block gốc mà peer có).
2. Đo chi phí thật của việc xác minh chữ ký lại cho 1 chuỗi nhân quả (có
   thể vài trăm tới vài nghìn block) — xác nhận không tạo ra 1 nút thắt cổ
   chai MỚI ở đúng con đường vốn đã hiếm/khẩn cấp này.
3. Thiết kế chi tiết cơ chế "dirty flag" ở Lớp 2 sao cho **chính bản thân
   flag đó không trở thành 1 điểm kẹt/fork mới** (vd: flag bị mất/hỏng
   cũng phải fail-safe về phía "coi như bẩn", không phải "coi như sạch").
4. Review bởi người có domain knowledge sâu về `consensus-core`
   (Mysticeti/Sui gốc) — đây là thay đổi chạm vào đúng ranh giới tin cậy
   (trust boundary) cốt lõi của toàn bộ hệ thống BFT, rủi ro cao nếu làm
   sai theo 1 hướng khác.
