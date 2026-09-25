# Thiết kế bằng chứng gian lận cho node thực thi (không ai giám sát)

> **Trạng thái:** DRAFT — thiết kế **hợp nhất và khuyến nghị hiện hành** (2026-09-25). Rà lại kỹ `SEQUENCER_PARENT_ANCHORING.md` (phương án A) và `SEQUENCER_ERC20_DISPUTE.md` (phương án B) dưới mô hình đe doạ chặt hơn. Khi mâu thuẫn, **tài liệu này thắng**; hai tài liệu kia giữ làm tham chiếu chi tiết.
>
> ✅ **ĐÃ CHỐT (2026-09-25): đường chính là chế độ giao dịch chuẩn có chữ ký — xem `SEQUENCER_ERC20_STANDARD_TX_PROOF.md` (nguồn sự thật cho triển khai).** Trong tài liệu này, phần sổ sự kiện theo log (`EventLeaf`, `incl_root`, `events_root`), `demandInclusion`/`demandOpen`, kiểm toán block mức 0/2 là **chế độ tổng quát chưa làm**; còn giữ nguyên: mô hình đe doạ (mục 2), bảng tấn công (mục 3), rà soát R1–R12 (mục 4), `AnchorState`/MMR các anchor (5.1), `reportRewrite` (5.5), ràng buộc ký quỹ (5.7).
>
> **Phạm vi (2026-09-25): chỉ chứng minh khi số dư bắt đầu bằng 0** (toàn bộ lịch sử từ sự kiện đầu, không baseline, không chứng minh theo đoạn) — xem mục 0 của `SEQUENCER_ERC20_USER_HISTORY_PROOF.md`. Kéo theo: cây sự kiện của holder là **Merkle-sum-min** (kiểm tiền tố không âm, P7) thay cho Merkle-sum; các đoạn nói về "khiếu nại theo đoạn giữa hai lời khai" và "cận dưới" ở `SEQUENCER_ERC20_DISPUTE.md` **không còn áp dụng**.
>
> **Quyền riêng tư (2026-09-25): người ngoài không thấy lịch sử cho tới khi người dùng báo cáo** — xem mục 13 của `SEQUENCER_ERC20_USER_HISTORY_PROOF.md`: RPC đọc hiện **công khai hoàn toàn** nên cần cổng đọc H7 (`privacy_mode`); `HolderLeaf` tách thành `incl_root` (Merkle thường) và `sum_root_hash` để bằng chứng bao gồm không lộ số dư người khác; `demandOpen`/P4/P7 chỉ holder được nộp; bỏ `tx_count_total` khỏi anchor; kiểm toán viên chỉ theo sự đồng ý. Điều này **thay đổi** định nghĩa `HolderLeaf` ở mục 5.2 bên dưới.
>
> **Luồng chứng minh ERC20 cụ thể, chi tiết từng bước (chỉ dựa trên lịch sử người dùng):** xem **`SEQUENCER_ERC20_USER_HISTORY_PROOF.md`**. Tài liệu đó cũng **sửa R13–R21** (bỏ phép tính cận dưới làm căn cứ kết tội vì vướng hạn mức/`permit`; kiểm theo **sự kiện** thay thế).
>
> **Quy ước:** **[XÁC MINH]** đã đối chiếu code · **[ĐỀ XUẤT]** thiết kế mới · **[CHƯA XÁC MINH]** phải kiểm tra trước khi làm.

---

## 1. Kết luận

1. **Không thể chứng minh mọi gian lận khi không ai giám sát.** Cái chứng minh được là gian lận mà **chính người dùng có dấu vết trong tay** (giao dịch của họ, receipt, lời khai đã ký, cam kết công khai của node trên Parent Chain).
2. **Cách tốt nhất là biến "giám sát" thành một phần của giao thức, phân tán cho từng người dùng:** mỗi ứng dụng khách của người dùng **tự kiểm toán các giao dịch của chính mình** ngay sau khi giao dịch xong, dựa trên **các cam kết công khai, bất biến của node trên Parent Chain**. Không cần watcher chuyên dụng.
3. **Bằng chứng theo yêu cầu (node ký lời khai khi được hỏi) là điểm yếu nhất** vì node có thể từ chối ký. Phải **chuyển sang cam kết chủ động** (node đăng cam kết theo chu kỳ lên Parent Chain, không hỏi thì cũng đã có) và **cơ chế yêu cầu – phản hồi, im lặng = thua** để ép node xuất trình bằng chứng.
4. **Ba bộ phận cần có** (mục 5): (a) **`AnchorMMR`** trên Parent Chain cam kết hash block **và** gốc sổ holder ERC20 của từng anchor; (b) **Sổ holder 2 tầng** (mỗi holder có cây sự kiện Merkle-sum) để người dùng buộc node **xuất trình sự kiện của mình**; (c) **Tự kiểm toán của khách hàng** + ba loại báo cáo: **`reportRewrite`** (viết lại lịch sử/chia nhìn), **`reportTxMismatch`** (thực thi sai giao dịch của tôi), **`demandInclusion`/`demandOpen`** (ép xuất trình, im lặng = thua).
5. **Giới hạn thẳng thắn** (mục 8): không chứng minh được lỗi EVM tự nhất quán, tiền vào mà không ai kiểm tra, kiểm duyệt (node từ chối nhận giao dịch), và không chứng minh **số dư chính xác** chỉ bằng trí nhớ người dùng.

---

## 2. Mô hình đe doạ và giả định

| # | Giả định | Hệ quả |
|---|---|---|
| T1 | **Operator không đáng tin.** Hắn điều khiển mọi replica (Raft) và **khoá ký dùng chung**. | Chữ ký của node là **bằng chứng chống lại node**, không phải bằng chứng sự thật. Raft chỉ chịu lỗi dừng, **không** ngăn operator gian lận. |
| T2 | **Không ai giám sát dữ liệu trong node.** Không watcher, không kiểm toán viên mặc định. | Không được dựa vào bên thứ ba có dữ liệu đầy đủ. |
| T3 | **Người dùng chỉ nhớ giao dịch của chính mình** (và bằng chứng thanh toán người khác đưa). | Bằng chứng phải xây từ dữ liệu này + cam kết công khai on-chain. |
| T4 | Parent Chain đáng tin (đồng thuận riêng) và là **nơi duy nhất bất biến**. | Mọi cam kết của node phải nằm ở đây; mọi phán quyết là hàm xác định của bằng chứng. |
| T5 | Lưu trữ on-chain **tối thiểu**. | Mỗi chain chỉ O(1) trạng thái lâu dài. |
| T6 | Xử lý block Go **giữ nguyên** (không đổi header/receipt). | Cam kết bổ sung (sổ holder, MMR) do node dựng **ngoài** đường xử lý block. |
| T7 | Phạm vi **ERC20 chuẩn** (phát đủ `Transfer`). | Loại token rebasing/không sự kiện/có hàm quản trị làm giảm số dư. |

---

## 3. Tấn công nào, ai chứng minh được, bằng gì

Cột **"1 người dùng"** = một người dùng đơn lẻ, chỉ có dữ liệu của mình, tự chứng minh được không.

| # | Tấn công của node | 1 người dùng chứng minh được? | Bằng chứng | Cần thêm |
|---|---|---|---|---|
| 1 | **Viết lại lịch sử / chia nhìn** (đưa lịch sử khác nhau cho người khác nhau, hoặc đổi block sau khi đã ký) | **Có** | lời khai/receipt có chữ ký chứa `block_hash` của block `b` **mâu thuẫn** với hash tại vị trí `b` trong `AnchorMMR` (bằng chứng MMR) | node phải **ký** phản hồi có `block_hash` |
| 2 | **Thực thi sai giao dịch của tôi** (`transfer` thành công nhưng log/số lượng khác hoặc thiếu) | **Có** | receipt đã được header cam kết (`receiptRoot`) + calldata của tôi | không cần lời khai nào |
| 3 | **Bỏ sót sự kiện của tôi khỏi sổ holder** đã neo | **Có** (nhờ ép xuất trình) | `demandInclusion`: node phải nộp bằng chứng sự kiện ∈ sổ của tôi, im lặng/sai = thua | sổ holder 2 tầng đã neo |
| 4 | **Bịa sự kiện làm giảm số dư tôi** | **Có** (nhờ ép mở) | `demandOpen`: node phải chứng minh từng sự kiện thật bằng receipt + MMR; sự kiện bịa không có receipt | sổ holder 2 tầng |
| 5 | Sổ nội bộ **không nhất quán** (`balance ≠ Σ sự kiện`) | **Có** | kiểm ngay lúc xuất trình (Merkle-sum) | cây sự kiện Merkle-sum |
| 6 | Khai số dư **thấp hơn** mức tôi chắc chắn có | **Có, nhưng yếu hơn** | cận dưới từ sổ đã neo + sự kiện tôi biết | mục 5.6 |
| 7 | **Bỏ sót tiền vào do người khác gửi** | **Không** (tôi không biết) — **người gửi có thể** | người gửi tự kiểm toán giao dịch của họ (`demandInclusion` cho sổ của **người nhận**) | client của người gửi kiểm 2 phía |
| 8 | Khai số dư **cao hơn** thực tế | Không (gây hại người khác) | người bị hại tự chứng minh (như dòng 3–4) | — |
| 9 | **Giấu dữ liệu / ngừng phục vụ** | Chỉ bằng chứng **đã lưu sẵn** | dữ liệu người dùng lưu ngay lúc giao dịch + `demand*` (im lặng = thua) | client tự lưu ngay |
| 10 | **Kiểm duyệt** (từ chối nhận giao dịch) | **Không** | — | forced-inclusion qua Parent (ngoài phạm vi) |
| 11 | Lỗi EVM mà kết quả **vẫn tự nhất quán** với log | **Không** | cần tái thực thi 1 bước trên Parent + bằng chứng state | dài hạn (mục 8) |
| 12 | Operator **rút ký quỹ rồi gian lận** | phụ thuộc thời gian | ký quỹ phải bị khoá lâu hơn cửa sổ báo cáo | mục 5.7 |
| 13 | **Lộ/mất khoá ký** | như dòng 1 (hai lịch sử ký bởi cùng khoá) | equivocation | — |

**Nhận xét quan trọng:** dòng **7** là chỗ tự nhiên biến mọi người dùng thành người giám sát: **mỗi khoản tiền có một người gửi**, và người gửi đang giữ giao dịch đó. Nếu client của người gửi kiểm tra rằng sự kiện của họ có mặt trong sổ của **cả hai** phía (gửi và nhận), thì việc bỏ sót tiền vào bị phát hiện bởi người gửi, không cần người nhận biết gì.

---

## 4. Rà soát thiết kế cũ: lỗ hổng tìm thấy và cách sửa

| Mã | Lỗ hổng trong thiết kế trước | Hậu quả | Sửa |
|---|---|---|---|
| **R1** | **Tầng 1 dựa vào lời khai node ký theo yêu cầu** (`SignedStatement`, `SignedTxStatement`, `SignedNonceStatement`). | Node **từ chối ký** thì người dùng không có bằng chứng; ở mô hình "không ai giám sát" đây là điểm chết. | Chuyển sang **cam kết chủ động trên Parent Chain** (sổ holder trong anchor); lời khai ký chỉ còn là **bằng chứng bổ trợ** cho lỗi viết lại lịch sử (dòng 1). |
| **R2** | **Baseline `S0` cũng là lời khai của node.** | Cận dưới yếu nếu node khai thấp từ đầu. | Baseline lấy từ **anchor đã neo** (công khai, bất biến), không từ lời khai riêng. |
| **R3** | **`MmrAnchor` chỉ giữ bản mới nhất** và bằng chứng cũ hết hạn. | Người dùng phải lưu bằng chứng MMR ở đúng thời điểm; node ngừng phục vụ thì mất khả năng nâng bằng chứng. | Parent giữ **một MMR các anchor** (mỗi lá = `(blockHash, ledgerRoot, …)` của một anchor). Bằng chứng nâng cấp được bằng dữ liệu công khai; người dùng lưu bằng chứng ngay lúc giao dịch. |
| **R4** | Chưa nêu **viết lại lịch sử/chia nhìn** là một tấn công chứng minh được bởi **một** người dùng. | Bỏ lỡ bằng chứng mạnh nhất, rẻ nhất. | `reportRewrite` (mục 5.5). |
| **R5** | Giao thức Merkle-sum (mục 4–7 tài liệu B) dựa vào **watcher có đủ dữ liệu**. | Vi phạm T2. | Thay bằng **yêu cầu – phản hồi, im lặng = thua** cho từng sự kiện của **chính người dùng** (mục 5.4). |
| **R6** | **`HolderLeaf` chỉ có `event_chain_hash`** (chuỗi băm tuyến tính). | Chứng minh một sự kiện thuộc sổ tốn O(số sự kiện). | Dùng **cây sự kiện Merkle-sum (MMR có tổng)** mỗi holder → bằng chứng O(log n) và kiểm `balance == Σ` tức thì. |
| **R7** | Sự kiện có **hai bên** (`from` và `to`) nhưng người dùng chỉ kiểm phía mình. | Node bỏ sót phía người nhận mà không ai để ý. | Client của **người gửi kiểm cả hai sổ** (dòng 7). |
| **R8** | **Ký quỹ có thể rút** (`UnregisterChainWithCert` → unbonding). | Operator rút ký quỹ rồi gian lận, báo cáo tới muộn. | Unbonding ≥ cửa sổ báo cáo + thời gian xử lý (mục 5.7). |
| **R9** | Tầng 1 giả định "không có hạn mức" cho cận dưới. | Hạn mức vô hạn/`permit` làm cận dưới vô nghĩa. | Giữ điều kiện, nhưng với **sổ đã neo** thì cận dưới **không còn là công cụ chính**; chỉ dùng cho dòng 6. |
| **R10** | **Chưa có kênh buộc node phục vụ bằng chứng.** | Node ngừng phục vụ = không gian lận cũng không bị bắt. | Cơ chế `demand*` với hạn theo `blockTime` của Parent; **im lặng = thua** (xác định, không cần node hợp tác). |
| **R11** | Mọi cam kết cùng **một khoá dùng chung**. | Lộ khoá = ký được mọi thứ. | Chấp nhận (T1); bảo vệ bằng equivocation + tối thiểu hoá cửa sổ giữa hai anchor + ký quỹ. |
| **R12** | Anchor thưa (K lớn) → cửa sổ viết lại dài. | Gian lận trong cửa sổ chưa neo khó bị bắt. | Anchor **dày** (K nhỏ hoặc theo thời gian); **hoãn nhả giá trị lớn** tới sau anchor kế tiếp. |

---

## 5. Thiết kế khuyến nghị  **[ĐỀ XUẤT]**

### 5.1. Cam kết công khai trên Parent Chain

Một **`AnchorMMR`** mỗi chain, cập nhật theo chu kỳ (mỗi K block hoặc T giây, **sau khi commit Raft và bền DB**, quy tắc thu gọn log ở schema mục 1.4):

```proto
message AnchorLeaf {           // một lá của MMR các anchor
  uint32 schema_version   = 1; // = 1
  uint64 block_number     = 2; // block cuối của anchor này
  bytes  block_hash       = 3; // 32 byte
  bytes  block_mmr_root   = 4; // MMR mọi block_hash tới block này (để chứng minh block bất kỳ)
  bytes  ledger_root      = 5; // gốc sổ holder ERC20 (mục 5.2)
  uint64 tx_count_total   = 6;
}

message AnchorState {          // trạng thái DUY NHẤT lâu dài mỗi chain trên Parent
  uint64 chain_id    = 1;
  uint64 anchors     = 2;      // số lá đã có trong MMR các anchor
  bytes  anchors_root = 3;     // gốc MMR các anchor
  uint64 last_block  = 4;
  bytes  signature   = 5;      // khoá dùng chung, miền "ROLLUP_ANCHOR_V1:"
}
```

- Parent **chỉ lưu `AnchorState`** (~100 byte); lá mới được **append** vào MMR các anchor (Parent tính lại đỉnh MMR; giao dịch neo mang lá mới).
- Bằng chứng một anchor cũ = bằng chứng MMR O(log n) **dưới `anchors_root` hiện hành**; ai có bản sao MMR đều nâng cấp được; người dùng lưu bằng chứng ngay lúc giao dịch (sửa R3).
- **Đơn điệu:** `block_number` tăng; hai `AnchorState` khác `anchors_root` cùng `anchors` là equivocation → slash (`#16`).

### 5.2. Sổ holder ERC20 2 tầng  (sửa R6)

Sau mỗi block (ngoài đường xử lý block, không ảnh hưởng băm block) node đọc receipt **thành công**, lấy log `Transfer` của token đã đăng ký và cập nhật **hai holder** (`from` −`v`, `to` +`v`; `0x0` không có sổ; chuyển cho chính mình vẫn là 1 sự kiện).

```proto
message HolderLeaf {
  uint32 schema_version = 1;
  bytes  token          = 2;   // 20 byte
  bytes  holder         = 3;   // 20 byte
  bytes  balance        = 4;   // 32 byte, không âm
  uint64 event_count    = 5;
  bytes  events_root    = 6;   // gốc cây Merkle-sum-min theo BLOCK của holder: sum == balance, minPrefix >= 0 (số dư tích luỹ mỗi ranh giới block không âm; số dư bắt đầu từ 0; không dùng thứ tự trong block)
}

message EventLeaf {            // lá của cây sự kiện của holder
  uint64 block = 1; uint32 tx_index = 2; uint32 log_index = 3;
  bytes  delta_signed33 = 4;   // +/− giá trị (1 byte dấu + 32 byte)
  bytes  tx_hash = 5;
}
```

- `ledger_root` = gốc MPT khoá `keccak256(token ‖ holder)` → `HolderLeaf` (đề xuất dùng lại `pkg/trie` MPT **[CHƯA XÁC MINH]**).
- **Bất biến kiểm được tức thì:** `HolderLeaf.balance == tổng có dấu của events_root`. Node không thể cam kết số dư mâu thuẫn với chính các sự kiện của nó.
- Thứ tự sự kiện tăng ngặt theo `(block, tx_index, log_index)`.
- **Mọi replica dựng cùng một sổ** (hàm xác định) và đối chiếu `ledger_root` (thêm bất biến); lệch → replica lệch dừng. Với operator không đáng tin, bước này chỉ chống **lỗi**, không chống **gian lận**.

### 5.3. Tự kiểm toán của khách hàng  (thay cho watcher)

Sau **mỗi** giao dịch của người dùng có tác động ERC20, ứng dụng khách làm (tự động):

```
1. Lưu ngay: tx bytes, receipt, header, bằng chứng MMR block, lời khai đã ký (nếu có).
2. Khi anchor kế tiếp xuất hiện trên Parent Chain:
   a. TX_MISMATCH: đối chiếu calldata với log trong receipt đã được header cam kết.
   b. Kiểm SỰ KIỆN CỦA MÌNH có trong sổ CẢ HAI PHÍA (from và to):
        - Xin node bằng chứng (leaf ∈ ledger_root, event ∈ events_root); tự kiểm cục bộ.
        - Nếu node không trả lời / trả sai: gọi demandInclusion trên Parent (im lặng = thua).
   c. Kiểm chéo: block_hash trong mọi phản hồi đã ký == hash tại vị trí đó trong AnchorMMR
      (không khớp -> reportRewrite).
3. Định kỳ (tuỳ chọn): demandOpen sổ của chính mình để kiểm sự kiện bịa.
```

Đây là chỗ "giám sát" được **phân tán**: chi phí mỗi client rất nhỏ (chỉ giao dịch của chính nó), và **mỗi sự kiện có ít nhất một người kiểm** (người gửi).

### 5.4. Yêu cầu – phản hồi: im lặng = thua  (sửa R5, R10)

Mọi mốc thời gian dùng **`blockTime` của Parent Chain** (cùng nguyên tắc Reclaim, `SEQUENCER_DESIGN.md` mục 3.6).

| Giao dịch trên Parent | Người nộp | Node phải làm trong `W_resp` | Thua nếu |
|---|---|---|---|
| `demandInclusion(anchorIdx, token, holder, eventEvidence)` | người có sự kiện thật (người gửi hoặc người nhận có bằng chứng thanh toán) + ký quỹ nhỏ | nộp `(leafProof, eventProof)`: `leaf ∈ ledger_root`, `event ∈ events_root`, `balance == Σ` | im lặng, hoặc bằng chứng sai/thiếu |
| `demandOpen(anchorIdx, token, holder, indices)` | holder | với mỗi chỉ số nộp sự kiện + bằng chứng block (MMR) + receipt chứng minh log `Transfer` thật, receipt `SUCCESS` | im lặng, hoặc **bất kỳ** sự kiện không chứng minh được thật |
| `demandBalance(anchorIdx, token, holder)` | holder | nộp `leafProof` cho `balance` | im lặng |

Kết quả xác định: nếu node thua ⇒ `NODE_AT_FAULT` (forfeit một phần ký quỹ, bồi thường người nộp, hoàn ký quỹ người nộp). Nếu node xuất trình đúng ⇒ ký quỹ người nộp bị tịch thu một phần (chống lạm dụng). Không cần node nào khác hợp tác.

### 5.5. `reportRewrite` — viết lại lịch sử / chia nhìn  (sửa R4)

Người dùng nộp: một **bản ghi có chữ ký của node** chứa `(chain_id, block_number, block_hash)` (receipt/lời khai) **và** bằng chứng MMR cho thấy tại vị trí `block_number` của `block_mmr_root` (trong anchor đã neo) có hash **khác**. Parent kiểm chữ ký (khoá đã đăng ký) + bằng chứng MMR ⇒ **equivocation** ⇒ slash ngay. **Chỉ cần một người dùng và chữ ký của node**; đây là bằng chứng mạnh nhất không cần dữ liệu nào ngoài phản hồi của node. Yêu cầu: **node phải ký mọi phản hồi mang `block_hash`** (receipt, truy vấn) — client từ chối phản hồi không ký (bắt buộc ở khách hàng).

### 5.6. `reportTxMismatch` và cận dưới

- **`reportTxMismatch`:** `txBytes` (calldata `transfer(to, amt)` trực tiếp) + receipt + bằng chứng MMR/receipt (không cần lời khai): receipt `SUCCESS` nhưng log thiếu/khác ⇒ node thực thi sai giao dịch của tôi.
- **Cận dưới** (dòng 6) giữ như tài liệu B mục 11.3 (1) nhưng baseline lấy từ **anchor** (R2); chỉ là bảo vệ phụ.

### 5.7. Ràng buộc với ký quỹ  (sửa R8)

- `UnbondingPeriodSeconds` ≥ `W_resp` + thời gian xử lý + **một chu kỳ anchor** để mọi báo cáo về anchor cuối còn kịp.
- Forfeit không được vượt số còn lại: cần **mức ký quỹ tối thiểu** tương ứng giá trị ERC20 được bảo vệ (tham số vận hành).
- Khoản giá trị **lớn** phải **chờ hết cửa sổ báo cáo** của anchor chứa nó trước khi nhả (R12).

### 5.8. Kiểm toán viên (tuỳ chọn, không phụ thuộc)

Một tiến trình độc lập (do bất kỳ ai chạy: nhà bảo hiểm, cộng đồng, người dùng lớn) tải block, **tái thực thi** và đối chiếu `ledger_root`/`block_hash` với anchor; có thể nộp `demandOpen`/`reportRewrite` thay mặt người dùng, và **được thưởng** từ ký quỹ bị forfeit. **Thiết kế không dựa vào nó**, nhưng nó là cách duy nhất phát hiện dòng 7–8 và 11 khi không ai kiểm tra.

---

## 6. Ai lưu gì (cuối cùng)

| Bên | Lưu | Ghi chú |
|---|---|---|
| **Parent Chain** | `AnchorState` mỗi chain (~100 byte, gồm `anchors_root` của MMR các anchor); `SecurityBond` (đã có); bản ghi `demand*` tạm thời | O(1) lâu dài |
| **Người dùng** | giao dịch của mình + receipt + header + bằng chứng MMR (block và anchor) + phản hồi có chữ ký node; các `approve` của mình; bằng chứng thanh toán người khác đưa | lưu **ngay** lúc giao dịch; client tự động |
| **Node** | toàn bộ block/receipt, sổ holder 2 tầng, MMR đầy đủ, khoá ký | phục vụ bằng chứng khi được hỏi; im lặng = thua |
| **Kiểm toán viên (nếu có)** | bản sao đầy đủ | không bắt buộc |

---

## 7. Test (`T-FP-*`, bổ sung vào `SEQUENCER_SCHEMAS_AND_TEST_PLAN.md`)

| ID | Test | Đạt khi |
|---|---|---|
| T-FP-01 | `AnchorState` đơn điệu; hai `anchors_root` khác nhau cùng `anchors` | tăng mới nhận; mâu thuẫn → slash |
| T-FP-02 | Bằng chứng anchor O(log n) dưới `anchors_root` hiện hành cho anchor cũ | hợp lệ; sửa 1 bit → từ chối |
| T-FP-03 | **`reportRewrite`**: phản hồi ký về block `b` có hash H, MMR có H′ ≠ H | slash; người báo được thưởng |
| T-FP-04 | Phản hồi ký khớp MMR | báo cáo bị từ chối, ký quỹ bị phạt |
| T-FP-05 | **`reportTxMismatch`**: `transfer` thành công nhưng thiếu/khác log | phát hiện, không cần lời khai |
| T-FP-06 | `demandInclusion` khi node trả bằng chứng đúng | ký quỹ người nộp bị tịch thu một phần |
| T-FP-07 | `demandInclusion` khi node **bỏ sót** sự kiện | node im lặng/không chứng minh được → thua |
| T-FP-08 | `demandInclusion` **cả hai phía** của một `Transfer` | bỏ sót phía nào cũng bị phát hiện (client người gửi kiểm 2 sổ) |
| T-FP-09 | `demandOpen` với sự kiện **bịa** | không có receipt → thua |
| T-FP-10 | `balance ≠ Σ events_root` trong `HolderLeaf` | phát hiện ngay lúc xuất trình |
| T-FP-11 | Node im lặng mọi `demand*` (ngừng phục vụ) | thua theo `blockTime`; không phụ thuộc đồng hồ node |
| T-FP-12 | Hai replica dựng sổ từ cùng receipt | `ledger_root` giống hệt; lệch → replica dừng |
| T-FP-13 | Chuyển cho chính mình; mint/burn; nhiều `Transfer` trong 1 tx; giao dịch thất bại; token có phí | sổ đúng quy tắc mục 5.2 |
| T-FP-14 | Token rebasing/quản trị giảm số dư | job tuân thủ loại; không cho đăng ký |
| T-FP-15 | Rút ký quỹ (`UnregisterChainWithCert`) rồi báo cáo trong unbonding | forfeit được; báo cáo sau unbonding → không |
| T-FP-16 | Khoản giá trị lớn nhả trước khi hết cửa sổ báo cáo | bị chặn |
| T-FP-17 | Khoá bị lộ: hai lịch sử ký bởi cùng khoá | `reportRewrite` phát hiện |
| T-FP-18 | Client tự kiểm toán tự động sau mỗi giao dịch | phát hiện mọi bất nhất trong test đối kháng (node bỏ sót/bịa/đổi hash) |
| T-FP-19 | Node từ chối ký phản hồi | client từ chối phản hồi, cảnh báo; không thể báo cáo (ghi rõ giới hạn) |
| T-FP-20 | Bằng chứng người dùng lưu từ lâu (đã qua nhiều anchor) | vẫn kiểm được dưới `anchors_root` mới nhờ MMR |

---

## 8. Giới hạn (nói thẳng)

1. **Không có giám sát ⇒ gian lận nào không để lại dấu vết cho ai thì không bị phát hiện.** Đặc biệt **tiền vào mà người gửi không kiểm tra** (dòng 7) và **khai số dư cao** (dòng 8).
2. **Không chứng minh được số dư chính xác bằng trí nhớ người dùng** — nay thu hẹp: chỉ còn **khoản nhận bị bỏ sót mà chưa bị chi và người gửi không kiểm** (nhờ P7 với số dư bắt đầu bằng 0, mọi khoản nhận bị bỏ sót rồi bị chi tiếp đều làm sổ tự mâu thuẫn). **Và chỉ áp dụng khi số dư bắt đầu bằng 0.** Người dùng chứng minh được: sự kiện của mình có/không có trong sổ, sự kiện nào bịa, số dư nhất quán với sự kiện đã xuất trình. **Số dư đúng** phụ thuộc việc các sự kiện tiền vào của người khác được liệt kê đủ — điều này dựa vào **người gửi tự kiểm** (dòng 7) hoặc kiểm toán viên.
3. **Không bắt được lỗi EVM tự nhất quán** (dòng 11). Muốn bắt cần **tái thực thi một bước** trên Parent Chain với bằng chứng state (cần phần NOMT chưa có: ô lưu trữ 2 tầng, bằng chứng theo block cũ, kiểm chứng phi trạng thái) **hoặc** bằng chứng hiệu lực (ZK) — ngoài phạm vi.
4. **Kiểm duyệt** không chứng minh được; cần cơ chế **chèn giao dịch bắt buộc qua Parent Chain** (ngoài phạm vi).
5. **Node từ chối ký** phản hồi: client không dùng được phản hồi nhưng **không có gì để báo cáo**. Giảm thiểu: `reportTxMismatch` và `demand*` **không cần chữ ký theo yêu cầu**; chỉ `reportRewrite` cần phản hồi ký.
6. **Cửa sổ giữa hai anchor**: gian lận trong đó chỉ bị bắt sau anchor kế tiếp; giảm bằng anchor dày và hoãn nhả giá trị lớn.
7. **Tin vào token chuẩn** (T7) và vào việc node dựng sổ từ receipt đúng (chỉ kiểm được theo dòng 3–5, không toàn cục).

---

## 9. Kế hoạch hợp nhất (Giai đoạn F, thay cho F-min và F0–F5 khi chọn thiết kế này)

| Bước | Việc | Phụ thuộc | Kích thước |
|---|---|---|---|
| F0 | **Xác minh:** định dạng bằng chứng MPT của `receiptRoot` (`txIndex`, `logIndex`, log trong receipt); dùng `pkg/trie` MPT làm sổ; cài MMR có tổng; ký xác định giữa replica | — | S–M |
| F1 | `AnchorState` + MMR các anchor trên Parent Chain (append lá, đơn điệu, equivocation slash); gap analysis với `SubmitCheckpoint` | A0, F0 | M |
| F2 | Bộ dựng **sổ holder 2 tầng + MMR block** trên mọi replica (xác định, đối chiếu gốc); job tuân thủ token | F0, C2 | L |
| F3 | Worker neo (leader, sau commit Raft + bền DB); cadence dày; RPC **ký mọi phản hồi mang `block_hash`** (điểm móc RPC ký, mặc định-tắt); dịch vụ bằng chứng (leaf, event, block, receipt) | C1, F2 | L |
| F4 | Hợp đồng Parent Chain: `reportRewrite`, `reportTxMismatch`, `demandInclusion`, `demandOpen`, `demandBalance`; phán quyết theo `blockTime`; forfeit/bồi thường; ràng buộc unbonding | F1, F3 | L |
| F5 | **SDK tự kiểm toán cho khách hàng** (lưu bằng chứng, kiểm cả hai sổ, tự nộp `demand*`); công cụ dòng lệnh | F3, F4 | M |
| F6 | Đối kháng/chaos: node bỏ sót/bịa/đổi hash/im lặng/ngừng phục vụ/rút ký quỹ; kiểm toán viên tham chiếu (tuỳ chọn) | F4, F5 | L |

---

## 10. Quyết định cần bạn xác nhận

| Quyết định | Đề xuất |
|---|---|
| Chuyển từ **lời khai theo yêu cầu** sang **cam kết chủ động trên Parent Chain** (sổ holder trong anchor) — tức phương án A trở thành **nền**, không còn tuỳ chọn | Đồng ý (sửa R1) |
| **Tự kiểm toán của khách hàng** là cơ chế giám sát chính; kiểm toán viên chuyên dụng chỉ tuỳ chọn | Đồng ý |
| **Node phải ký mọi phản hồi mang `block_hash`**; client từ chối phản hồi không ký | Đồng ý (một điểm móc RPC ký, mặc định-tắt) |
| **Im lặng = thua** với mọi `demand*`, tính theo `blockTime` Parent Chain | Đồng ý |
| **Người gửi kiểm cả hai sổ** (gửi và nhận) làm quy ước bắt buộc của SDK | Đồng ý |
| Anchor **dày** và **hoãn nhả giá trị lớn** tới hết cửa sổ báo cáo | Đồng ý; K và ngưỡng "lớn" chốt theo số đo |
| `UnbondingPeriodSeconds` ≥ cửa sổ báo cáo + xử lý + 1 chu kỳ anchor; mức ký quỹ tối thiểu theo giá trị được bảo vệ | Đồng ý; số cụ thể sau |
| Chấp nhận các giới hạn ở mục 8 (đặc biệt: không chứng minh số dư chính xác bằng trí nhớ người dùng; không bắt lỗi EVM tự nhất quán; không chống kiểm duyệt) | Cần bạn xác nhận |
| Có đầu tư **tái thực thi một bước trên Parent Chain** (dài hạn) để bắt lỗi EVM tự nhất quán | Để sau; cần state-proof của NOMT |
