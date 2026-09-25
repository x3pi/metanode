# Khiếu nại ERC20 với lưu trữ tối thiểu (mô hình optimistic)

> **Trạng thái:** DRAFT phân tích (2026-09-25). Đây là **phương án B** cho bài toán "người dùng report được nếu node thực thi làm sai ERC20", thay cho phương án A "Sổ holder ERC20 neo mọi block" trong `SEQUENCER_PARENT_ANCHORING.md` mục 3.2. Đọc kèm tài liệu đó.
>
> **Ý tưởng gốc (của bạn):** Parent Chain chỉ lưu hash header. Khi người dùng khiếu nại, họ đưa toàn bộ lịch sử họ có; node thực thi xác thực đúng/thiếu giao dịch nào; nếu cả hai xác minh đúng thì chứng minh biến động ERC20 từ các input. Parent lưu tối thiểu; người dùng tự lưu dữ liệu của mình; node có đầy đủ.
>
> **⚠️ Đã được rà lại và hợp nhất (2026-09-25):** thiết kế khuyến nghị hiện hành là **`SEQUENCER_FRAUD_PROOF_DESIGN.md`** (mô hình đe doạ "không ai giám sát"). Điểm yếu chính của tài liệu này: Tầng 1 dựa vào **lời khai node ký theo yêu cầu** mà node có thể từ chối; đã được thay bằng cam kết chủ động trên Parent Chain (R1–R12 trong tài liệu hợp nhất).
>
> **Ràng buộc bổ sung (2026-09-25, quan trọng):** **người dùng chỉ tự nhớ giao dịch của chính mình**; không có watcher, không ai khác giữ dữ liệu đầy đủ. Ràng buộc này làm **mục 4–7 (giao thức Merkle-sum, giả định "có watcher") chỉ còn là Tầng 2 tuỳ chọn**. Thiết kế mặc định là **Tầng 1 ở mục 11** — thứ duy nhất chứng minh được chỉ bằng dữ liệu của chính người dùng. **Đọc mục 11 trước.**
>
> **Quy ước:** **[XÁC MINH]** đã đối chiếu code · **[ĐỀ XUẤT]** thiết kế mới · **[CHƯA XÁC MINH]** phải kiểm trước khi làm.

---

## 1. Kết luận nhanh

1. **Hướng này đúng và rẻ nhất khi không có tranh chấp** (Parent chỉ lưu vài chục byte/chain). Nó phù hợp mục đích "đủ để report nếu node sai", không phải "chứng minh số dư cho bên thứ ba ngay lập tức".
2. **Có 4 chỗ phải sửa để nó an toàn:**
   1. **Node không được làm trọng tài.** Nếu node tự "xác thực" bằng chứng của người dùng rồi tuyên bố đúng/sai thì nó chỉ cần nói "bằng chứng của bạn sai". Việc node kiểm tra chỉ là **bước hỗ trợ** (chỉ ra thiếu gì); **phán quyết duy nhất là Parent Chain**, kiểm bằng bằng chứng Merkle xác định. Node im lặng quá hạn (đo bằng `blockTime` của Parent Chain) ⇒ **người dùng thắng mặc định**.
   2. **Phải có một "lời khai" của node để làm đối tượng khiếu nại.** Chỉ có hash header thì Parent không biết node đã *khẳng định* điều gì về ERC20. Lời khai là **câu trả lời truy vấn có chữ ký** (ví dụ `balanceOf(X)` tại block `B`), người dùng tự lưu.
   3. **Tính đầy đủ của lịch sử không tự có.** Danh sách người dùng có thể thiếu giao dịch (đặc biệt **tiền vào do người khác gửi**), và node có thể giấu giao dịch. Phải có cơ chế **phát hiện bỏ sót bằng thử thách** (mục 4.3), không chỉ "node bảo thiếu gì".
   4. **Phạm vi phát hiện được có giới hạn:** chỉ bắt được **mâu thuẫn theo ngữ nghĩa ERC20** (mục 5), không bắt được mọi lỗi thực thi EVM.
3. **Thu hẹp chi phí bằng cách khiếu nại một đoạn giữa hai lời khai của chính node** (`V1` tại `B1`, `V2` tại `B2`): chỉ cần chứng minh `V2 − V1 ≠ Σ biến động thật trong (B1, B2]`. Không cần lịch sử từ genesis, và lấy luôn chữ ký của node làm mốc.
4. **Parent Chain lưu tối thiểu:** một accumulator của hash block (`MMR root` + `size`) mỗi chain, cộng ký quỹ (`SecurityBond`) đã có; mỗi khiếu nại chiếm một bản ghi O(1) và bị xoá khi xong.

---

## 2. So sánh với phương án A (Sổ holder neo mọi block)

| | **A. Sổ holder neo mọi block** | **B. Khiếu nại (tài liệu này)** |
|---|---|---|
| Parent lưu | mỗi anchor: `holder_ledger_root` + MMR + các gốc | **chỉ MMR root + size** mỗi chain (+ bản ghi khiếu nại tạm thời) |
| Node làm thêm mỗi block | dựng/cập nhật cây `(token, holder)`, đối chiếu giữa replica | chỉ duy trì MMR; ký câu trả lời truy vấn khi được hỏi |
| Chi phí khi không có tranh chấp | có (mỗi anchor) | **gần 0** |
| Chi phí khi có tranh chấp | thấp (1 bằng chứng Merkle) | cao hơn, tỉ lệ độ dài đoạn tranh chấp |
| Đối tượng dùng được | bên thứ ba **tin** số dư ngay (chứng minh chủ động) | **chỉ để report node sai** (bị động, có cửa sổ thời gian) |
| Niềm tin | tin sổ do node dựng (replica đối chiếu) | tin **ít nhất một người quan sát trung thực có đầy đủ dữ liệu** (giả định optimistic chuẩn) |
| Độ phức tạp | vừa | **cao** (giao thức thử thách, cửa sổ thời gian, ký quỹ) |
| Mục đích của bạn ("đủ để report") | thừa | **khớp** |

**Khuyến nghị:** dùng **B** làm mặc định vì đúng mục đích và rẻ; giữ **A** là phần mở rộng nếu sau này cần "chứng minh số dư chủ động cho bên thứ ba".

---

## 3. Ai lưu gì

| Bên | Lưu gì | Ghi chú |
|---|---|---|
| **Parent Chain** | `MmrAnchor{chain_id, size, root, last_block, signature}` mỗi chain (ghi đè bản mới, ~100 byte). `SecurityBond` (đã có). **Bản ghi khiếu nại tạm thời** (mục 4) O(1)/khiếu nại. | Không lưu hash từng block, không lưu danh sách giao dịch, không lưu sổ holder. |
| **Người dùng** | (1) **Lời khai có chữ ký của node** (`SignedStatement`, ~vài trăm byte mỗi cái; tối thiểu 2 cái để khiếu nại một đoạn). (2) Bytes **giao dịch của mình** + **receipt** + **header preimage** của các block chứa chúng + **bằng chứng MMR** (~`log₂(n)×32` byte). | Nên lưu **ngay lúc giao dịch** vì node có thể ngừng phục vụ sau này. Bằng chứng MMR cũ phải còn dùng được (mục 6.2). |
| **Node thực thi** | Toàn bộ block, receipt, chỉ mục theo `(token, holder)` **ngoài chuỗi** (để dựng danh sách khi bị khiếu nại), khoá ký, MMR đầy đủ. | Không thêm gì lên chuỗi trừ `MmrAnchor`. |
| **Người quan sát độc lập (watcher)** | bản sao dữ liệu đầy đủ của chuỗi, tuỳ chọn | Giả định an toàn: có ≥ 1 watcher trung thực (mục 7). |

---

## 4. Giao thức khiếu nại  **[ĐỀ XUẤT]**

### 4.1. Chuẩn bị (không có tranh chấp)

**(a) Node neo MMR.** Mỗi K block (đã commit Raft, bền DB — quy tắc thu gọn log, schema mục 1.4), node đăng `MmrAnchor` lên Parent Chain: `{chain_id, size, root, last_block}` ký bằng khoá dùng chung. Parent **chỉ giữ bản mới nhất** và kiểm `size` tăng đơn điệu. Hai `MmrAnchor` khác `root` cùng `size` là bằng chứng gian lận (tái dùng cơ chế `SlashOnEquivocation`, `#16`).

**(b) Node ký lời khai truy vấn.** RPC (chế độ có ký) trả thêm `SignedStatement` cho các truy vấn liên quan ERC20 (`balanceOf`, `totalSupply`, và receipt của giao dịch ERC20):

```proto
message SignedStatement {
  uint32 schema_version = 1;  // = 1
  uint64 chain_id       = 2;
  uint64 block_number   = 3;
  bytes  block_hash     = 4;  // 32 byte: ràng buộc vào một block cụ thể
  bytes  to             = 5;  // 20 byte: contract token
  bytes  calldata       = 6;  // ví dụ balanceOf(holder)
  bytes  result         = 7;  // ABI-encoded kết quả
  bytes  signature      = 8;  // trên keccak256("ROLLUP_STMT_V1:" ‖ be64(chain) ‖ be64(block) ‖ block_hash ‖ to ‖ keccak256(calldata) ‖ keccak256(result))
}
```

Chữ ký dùng khoá dùng chung (địa chỉ = `sequencer_address`); cùng khoá ⇒ mọi replica ký ra cùng lời khai (chữ ký xác định: BLS, hoặc ECDSA RFC 6979).

### 4.2. Mở khiếu nại

Người dùng thấy hai lời khai của node về cùng `(token, holder)`: `S1` tại `B1` với `V1`, `S2` tại `B2 > B1` với `V2`, hoặc một lời khai đơn lẻ mâu thuẫn với lịch sử của họ. Họ gọi:

`openDispute(chainId, S1, S2, bond)` — Parent kiểm:
1. Chữ ký của `S1`, `S2` hợp lệ theo khoá đã đăng ký của `chainId`; cùng `to` và cùng `holder` (giải từ `calldata`), `B1 < B2`.
2. `block_hash` của cả hai nằm trong MMR hiện hành (bằng chứng MMR đi kèm).
3. Người dùng đặt **ký quỹ** (chống khiếu nại bừa).

Khởi tạo bản ghi `Dispute{id, chain, token, holder, B1, V1, B2, V2, opener, bond, state=AWAIT_CLAIM, deadline = now + W_claim}`. `now` là **`blockTime` của Parent Chain** (không dùng đồng hồ cục bộ; cùng nguyên tắc Reclaim, `SEQUENCER_DESIGN.md` mục 3.6).

### 4.3. Node phải đưa ra "bản khai lịch sử" và bị thử thách

**Node khai** (`claimLedger`) trong `W_claim`:

```proto
message LedgerClaim {
  bytes  dispute_id = 1;
  uint64 count      = 2;   // số sự kiện Transfer liên quan holder trong (B1, B2]
  bytes  total_delta = 3;  // int256 có dấu: tổng biến động theo khai
  bytes  root       = 4;   // gốc Merkle-SUM của danh sách sự kiện đã sắp; mỗi nút chứa (hash, tổng delta của cây con)
}
```

Danh sách sự kiện nằm **ngoài chuỗi** (node phải công bố cho ai hỏi); Parent chỉ lưu `LedgerClaim`. Ngay khi nhận, Parent kiểm được điều rẻ nhất: **`V2 − V1 == total_delta`**?
- **Không bằng:** hai lời khai của chính node mâu thuẫn với bản khai lịch sử của nó ⇒ **node có lỗi** (`NODE_AT_FAULT`), không cần thử thách thêm.
- **Bằng:** node buộc phải đúng với cây của nó; ai cũng có thể thử thách trong `W_challenge`:

| Thử thách | Người nộp | Chứng minh | Kết luận nếu thành công |
|---|---|---|---|
| **Bỏ sót** | ai có một sự kiện thật chưa có trong danh sách | sự kiện thật `e'` (kèm bằng chứng MMR + receipt) có vị trí `(block, txIndex, logIndex)` **nằm giữa hai lá liền kề** `i, i+1` của cây (bằng chứng Merkle hai lá liền kề), hoặc trước lá đầu / sau lá cuối trong `(B1, B2]` | `NODE_AT_FAULT` |
| **Bịa** | ai đó | yêu cầu node **mở lá `i`**; node phải nộp sự kiện + bằng chứng trong `W_open`; sự kiện không hợp lệ hoặc node im lặng | `NODE_AT_FAULT` |
| **Tổng sai** | ai đó | một nút trong cây có tổng khác tổng hai con (bằng chứng O(log n)) | `NODE_AT_FAULT` |
| **Vi phạm ngữ nghĩa** | ai đó | mở lá(s) chứng minh vi phạm ở mục 5 (ví dụ số dư chạy âm) | `NODE_AT_FAULT` |

Vì lá có **thứ tự tăng ngặt** theo `(block, txIndex, logIndex)` và cây cam kết cả **tổng**, node không thể vừa giữ `total_delta = V2 − V1` vừa giấu một giao dịch thật hoặc bịa một giao dịch mà không bị phát hiện, miễn là **có ít nhất một bên có đủ dữ liệu để thử thách**.

### 4.4. Phán quyết

| Tình huống (theo `blockTime` Parent Chain) | Kết quả |
|---|---|
| Node **không** `claimLedger` trong `W_claim` | `DEFAULT_USER_WIN` |
| `V2 − V1 ≠ total_delta` | `NODE_AT_FAULT` |
| Có thử thách thành công trong `W_challenge` | `NODE_AT_FAULT` |
| Node không mở lá khi bị yêu cầu trong `W_open` | `NODE_AT_FAULT` |
| Hết `W_challenge`, không thử thách nào thành công | `NODE_CLEARED`: ký quỹ của người khiếu nại bị **tịch thu** (một phần trả cho node để bù chi phí công bố) |

Khi `NODE_AT_FAULT`/`DEFAULT_USER_WIN`: **forfeit một phần `SecurityBond`** của chain (theo tham số) để **bồi thường** người khiếu nại và người thử thách; hoàn ký quỹ cho người khiếu nại. Việc slash là **kết quả xác định của bằng chứng** (không cần cơ quan phán xử), phù hợp việc đã gỡ `RecoveryCommittee`.

### 4.5. Vai trò của "node xác thực bằng chứng của người dùng" (ý gốc của bạn)

Giữ lại nhưng **chỉ là bước hỗ trợ ngoài chuỗi** (`prepare`), không có hiệu lực pháp lý:

```
POST /dispute/prepare  { statements: [S1, S2], events: [ …bằng chứng người dùng có… ] }
→ { valid: [...], invalid: [{index, reason}], missing: [ …các sự kiện node có mà người dùng thiếu, kèm bằng chứng… ],
    suggestedClaim: {count, total_delta, root} }
```

Người dùng dùng kết quả này để biết mình thiếu gì và tự kiểm bằng bằng chứng Merkle trước khi mở khiếu nại. **Phán quyết vẫn do Parent Chain.**

---

## 5. Loại sai sót phát hiện được (và không)

**Phát hiện được (mâu thuẫn theo ngữ nghĩa ERC20, kiểm được bằng số học + bằng chứng, không cần chạy EVM):**

| # | Sai sót | Cách phát hiện |
|---|---|---|
| 1 | Số dư khai ≠ Σ biến động thật giữa hai lời khai của node | `V2 − V1 ≠ total_delta` hoặc thử thách bỏ sót/bịa |
| 2 | **Chi vượt số dư**: một `Transfer` thành công khi số dư chạy (running balance) của người gửi < số lượng | duyệt các lá, số dư chạy âm |
| 3 | Calldata nói `transfer(to, amt)` thành công nhưng log là số lượng/người nhận khác, hoặc **không có log** | đối chiếu `txBytes` với log (chỉ với lời gọi trực tiếp, ABI chuẩn) |
| 4 | `totalSupply` khai ≠ Σ mint − Σ burn | mở lá của địa chỉ `0x0` (cần lời khai `totalSupply` và mở rộng phạm vi holder = `0x0`) |
| 5 | Node ký hai lời khai **mâu thuẫn** về cùng `(token, holder, block)` | so trực tiếp hai chữ ký |
| 6 | Node ký lời khai về block mà `block_hash` **không thuộc** MMR đã neo | bằng chứng MMR thất bại |

**Không phát hiện được:**
- Lỗi thực thi EVM mà kết quả vẫn **tự nhất quán** với log (ví dụ node ghi sai cả trạng thái lẫn log theo cùng một cách). Cần **tái thực thi** một giao dịch trên Parent Chain với bằng chứng trạng thái trước (Hướng B của `SEQUENCER_DESIGN.md` mục 15.4) — nặng và cần các phần state-proof chưa có (`SEQUENCER_PARENT_ANCHORING.md` mục 6).
- Token **không phát sự kiện `Transfer` đầy đủ** (rebasing, đổi số dư âm thầm).
- **Kiểm duyệt** (node từ chối xử lý giao dịch): giới hạn nền tảng đã ghi ở `SEQUENCER_DESIGN.md` mục 15.6.
- Người dùng không có bằng chứng nào và **không có watcher nào** có dữ liệu: không ai thử thách được ⇒ node gian lận tinh vi không bị phát hiện.

---

## 6. Chi tiết kỹ thuật cần chốt

### 6.1. Cây Merkle-SUM và vị trí sự kiện

- Lá: `H("EVLEAF_V1:" ‖ be64(block) ‖ be32(txIndex) ‖ be32(logIndex) ‖ deltaSigned33 ‖ keccak256(receiptBytes‖logIndex))`; nút trong: `H(left.hash ‖ right.hash ‖ (left.sum + right.sum))`. Thứ tự lá = thứ tự tăng ngặt theo `(block, txIndex, logIndex)`. **[CHƯA XÁC MINH]** `txIndex` và `logIndex` xác định và chứng minh được trong receipt trie hiện có (`pkg/receipt`, `pkg/proto` `EventLog`).
- Dùng lại `BuildMerkleTree`/`GenerateMerkleProof` (`cross_chain/relayer.go`, RFC-6962) làm khung, mở rộng thêm trường tổng.

### 6.2. Bằng chứng MMR và tính bền của bằng chứng cũ

- Parent chỉ giữ **MMR mới nhất**. Bằng chứng MMR người dùng lưu ở thời điểm cũ có thể **lỗi thời** so với `root` mới. MMR cho phép nâng bằng chứng cũ lên gốc mới **nếu có các nút mới** (ai có bản sao MMR đều dựng được: node, watcher). **Rủi ro:** người dùng chỉ có bằng chứng cũ và node/watcher ngừng phục vụ.
- Giảm thiểu: người dùng lưu **`MmrAnchor` có chữ ký** tại thời điểm giao dịch; Parent chấp nhận bằng chứng dưới `MmrAnchor` **cũ** nếu (i) nó do đúng khoá ký và (ii) `size` của nó ≤ `size` hiện hành và hai MMR **nhất quán** (bằng chứng nhất quán MMR — Parent kiểm được). Hoặc Parent giữ thêm một vòng nhỏ các root cũ (đánh đổi lưu trữ tối thiểu).
- **Quyết định mở:** giữ 1 root hay vòng R root.

### 6.3. Thời gian và tham số  **[ĐỀ XUẤT, chốt theo số đo]**

`W_claim`, `W_challenge`, `W_open`, mức ký quỹ người khiếu nại, tỉ lệ forfeit `SecurityBond`, giới hạn `count` tối đa mỗi khiếu nại (chia đoạn), `K` neo MMR. **Không đặt số trước khi đo** chi phí Parent Chain. Mọi mốc thời gian dùng `blockTime` của Parent Chain.

### 6.4. Ràng buộc kích thước và chống lạm dụng

- `count` tối đa mỗi khiếu nại có trần; đoạn lớn hơn phải chia thành nhiều khiếu nại nối tiếp (dùng lời khai trung gian của node làm mốc).
- Bản ghi khiếu nại **có trần số lượng đồng thời** trên mỗi chain và bị xoá khi xong (Bounded).
- Ký quỹ người khiếu nại ≥ chi phí công bố dự kiến của node, để khiếu nại bừa không có lợi.

---

## 7. Mô hình tin cậy và rủi ro

| Rủi ro | Mô tả | Giảm thiểu |
|---|---|---|
| Không ai có dữ liệu đầy đủ để thử thách | **Đây là tình huống mặc định** (người dùng chỉ nhớ giao dịch của mình; tiền vào do người khác gửi họ không có) | Không được dựa vào watcher. Dùng **Tầng 1 (mục 11)**, chỉ cần dữ liệu của chính người dùng; Tầng 2 chỉ hiệu lực khi có bên giữ dữ liệu |
| Node giấu dữ liệu (data withholding) | Chỉ đăng `LedgerClaim`, không công bố lá | Quy tắc "yêu cầu mở lá, im lặng = thua" ép công bố theo yêu cầu |
| Khoá dùng chung bị lộ | Kẻ tấn công ký lời khai/MMR giả | Equivocation slash cho hai `MmrAnchor` mâu thuẫn; tối thiểu hoá thời gian sống của quyền dùng khoá; velocity-limit (`#11`) |
| Khiếu nại bừa/DoS | Mở nhiều khiếu nại | Ký quỹ + trần số khiếu nại đồng thời |
| Đua thời gian / sai đồng hồ | | Mọi mốc dùng `blockTime` Parent Chain, xác định |
| Bằng chứng MMR cũ hết dùng được | mục 6.2 | Lưu `MmrAnchor` ký + bằng chứng nhất quán, hoặc vòng R root |
| Đường không phát hiện được | mục 5 | Nêu rõ trong tài liệu người dùng; Hướng B (tái thực thi) cho giá trị lớn |

**Cửa sổ thời gian là đánh đổi cố hữu của optimistic:** kết quả khiếu nại chỉ chắc chắn sau `W_claim + W_challenge`; **không** phù hợp nếu cần nhả tiền ngay dựa trên số dư ERC20 (khi đó dùng phương án A hoặc bắt buộc chờ hết cửa sổ).

---

## 8. Kế hoạch triển khai (Giai đoạn F-min, thay cho F2b–F4 nếu chọn phương án B)

| Bước | Việc | Phụ thuộc | Kích thước |
|---|---|---|---|
| F0 | Xác minh: `txIndex`/`logIndex` và bằng chứng receipt trong `pkg/receipt`; cách dựng MMR/Merkle-sum; ký xác định (BLS hay ECDSA) | — | S–M |
| G1 | `MmrAnchor` trên Parent Chain (đơn điệu, equivocation slash); bộ duy trì MMR ở node; bằng chứng nhất quán MMR | A0, F0, C2 | M |
| G2 | RPC chế độ có ký: `SignedStatement` cho `balanceOf`/`totalSupply`/receipt ERC20 (một điểm móc thêm ở RPC, mặc định-tắt) | C1 | M |
| G3 | Chỉ mục ngoài chuỗi `(token, holder) → sự kiện` ở node; dịch vụ `prepare` và công bố danh sách/lá | F0, C2 | L |
| G4 | Hợp đồng khiếu nại trên Parent Chain: `openDispute`, `claimLedger`, các thử thách (bỏ sót, bịa, tổng sai, ngữ nghĩa), phán quyết, forfeit bond, dọn dẹp | G1, G3 | L |
| G5 | Công cụ người dùng: lưu lời khai + bằng chứng, dựng khiếu nại; watcher tham chiếu | G2, G4 | M |
| G6 | Chaos/đối kháng: node giấu dữ liệu, node im lặng, node bỏ sót/bịa, khiếu nại bừa | G4 | L |

**Test (`T-DP-*`, bổ sung vào `SEQUENCER_SCHEMAS_AND_TEST_PLAN.md` mục 2.3):**

| ID | Test | Đạt khi |
|---|---|---|
| T-DP-01 | Chữ ký `SignedStatement` | đúng khoá → hợp lệ; sai 1 bit → từ chối; các replica ký ra cùng chữ ký |
| T-DP-02 | `MmrAnchor` đơn điệu; hai root khác cùng `size` | tăng mới nhận; mâu thuẫn → slash |
| T-DP-03 | Bằng chứng MMR | block bất kỳ O(log n) hợp lệ; sửa 1 bit → từ chối; bằng chứng cũ + nhất quán → chấp nhận |
| T-DP-04 | `openDispute` thiếu ký quỹ / hai lời khai khác holder hoặc token / `B1 ≥ B2` | từ chối |
| T-DP-05 | Node im lặng quá `W_claim` | `DEFAULT_USER_WIN`, forfeit đúng, hoàn ký quỹ |
| T-DP-06 | `V2 − V1 ≠ total_delta` | `NODE_AT_FAULT` ngay |
| T-DP-07 | Node đúng, không ai thử thách | `NODE_CLEARED`, ký quỹ người khiếu nại bị tịch thu một phần |
| T-DP-08 | **Bỏ sót** một sự kiện (giữa hai lá, trước lá đầu, sau lá cuối) | thử thách thành công cho cả 3 vị trí |
| T-DP-09 | **Bịa** một sự kiện | yêu cầu mở lá → node không chứng minh được → thua |
| T-DP-10 | Node im lặng khi bị yêu cầu mở lá | thua sau `W_open` |
| T-DP-11 | Cây có tổng sai ở nút trong | thử thách O(log n) thành công |
| T-DP-12 | Số dư chạy âm ở một lá | vi phạm ngữ nghĩa → `NODE_AT_FAULT` |
| T-DP-13 | Calldata `transfer` thành công nhưng log khác/thiếu | phát hiện (lời gọi trực tiếp) |
| T-DP-14 | Chuyển cho chính mình; mint/burn; nhiều `Transfer` trong một tx | tổng và thứ tự đúng |
| T-DP-15 | Chia đoạn `count` vượt trần | khiếu nại nối tiếp dùng lời khai trung gian, kết quả cộng dồn đúng |
| T-DP-16 | Trần số khiếu nại đồng thời | vượt trần → từ chối; bộ nhớ không phình |
| T-DP-17 | Mọi mốc thời gian | chỉ phụ thuộc `blockTime` Parent Chain; đổi đồng hồ node không đổi kết quả |
| T-DP-18 | `prepare` (bước hỗ trợ) | trả đúng thiếu/sai; **không** có hiệu lực phán quyết |
| T-DP-19 | Khoá bị lộ, ký lời khai giả | hai lời khai mâu thuẫn bị phát hiện; slash |

---

## 9. Điểm chưa xác minh

| Điểm | Ảnh hưởng | Cách xác minh |
|---|---|---|
| `txIndex`/`logIndex` xác định và chứng minh được trong receipt trie | vị trí sự kiện, bằng chứng receipt | đọc `pkg/receipt`, `EventLog` |
| MMR/Merkle-sum: dùng lại `relayer.go` được đến đâu | khối lượng code mới | đọc `BuildMerkleTree` |
| Ký xác định giữa replica (BLS vs ECDSA) | mọi replica ra cùng lời khai | thử thật |
| Chi phí xác minh bằng chứng trong handler Gateway (barrier tuần tự, blob `GatewayEngine`) | tham số cửa sổ/trần | benchmark |
| Watcher: ai vận hành, khuyến khích thế nào | giả định an toàn | quyết định vận hành |

## 10. Quyết định cần bạn xác nhận

| Quyết định | Đề xuất |
|---|---|
| Chọn **phương án B (khiếu nại)** làm mặc định, giữ A là mở rộng | Đồng ý |
| **Node không làm trọng tài**; `prepare` chỉ là hỗ trợ; Parent Chain phán quyết | Đồng ý |
| Đối tượng khiếu nại là **lời khai có chữ ký** của node (`SignedStatement`) | Đồng ý; thêm điểm móc RPC ký (mặc định-tắt) |
| Khiếu nại theo **đoạn giữa hai lời khai của node** thay vì từ genesis | Đồng ý |
| Node im lặng ⇒ người dùng thắng mặc định (theo `blockTime` Parent Chain) | Đồng ý |
| Parent giữ **1 MMR root** hay **vòng R root** | Chốt sau F0/G1 (đánh đổi lưu trữ vs độ bền bằng chứng cũ) |
| ~~Chấp nhận giả định "có ít nhất một watcher trung thực"~~ | **Loại bỏ** theo ràng buộc mới; Tầng 1 không cần watcher (mục 11) |

---

## 11. Ràng buộc: người dùng chỉ nhớ giao dịch của chính mình  **[ĐỀ XUẤT — thiết kế mặc định]**

### 11.1. Hệ quả

Người dùng `X` chỉ có: (a) các giao dịch **X gửi** (bytes + receipt + header + bằng chứng MMR), (b) các lời khai có chữ ký của node, (c) — nếu người trả chịu chia sẻ — **bằng chứng thanh toán** (proof-of-payment: tx + receipt + bằng chứng) của những khoản tiền **X nhận**. X **không** biết mọi khoản tiền vào và không biết các lần bị `transferFrom`.

Vì số dư `= Σ vào − Σ ra`, mà X không biết đủ chiều **vào**, nên:

| Điều X muốn chứng minh | Chỉ với dữ liệu của X? |
|---|---|
| Giao dịch của X đã vào block và **thành công**, số lượng/log đúng | **Được** (bằng chứng receipt) |
| **Toàn bộ giao dịch X gửi trong một đoạn** (không sót) | **Được** (nonce liên tiếp + lời khai nonce của node) |
| Node **khai số dư thấp hơn mức chắc chắn X phải có** (ví dụ mất tiền, bỏ khoản đã nhận) | **Được** (bằng chứng **cận dưới**, mục 11.3) |
| **Số dư chính xác** của X | **Không** (thiếu chiều vào của người khác) |
| Node khai số dư **cao hơn** thực tế | Không phải việc của X (gây hại cho người khác/tổng cung) |

Nói thẳng: với ràng buộc này, thứ chứng minh được là **"node đã làm sai theo hướng gây hại cho tôi"** (khai thấp, làm mất tiền, thực thi sai giao dịch của tôi), **không phải** "số dư của tôi chính xác là V".

### 11.2. Lời khai có chữ ký cần thêm (tối thiểu, người dùng tự lưu)

Ngoài `SignedStatement` cho `balanceOf` (mục 4.1), node ký thêm:

```proto
// Lời khai theo từng giao dịch của chính người gửi (trả kèm receipt của giao dịch ERC20).
message SignedTxStatement {
  uint32 schema_version = 1;  // = 1
  uint64 chain_id       = 2;
  uint64 block_number   = 3;
  bytes  block_hash     = 4;
  uint32 tx_index       = 5;
  bytes  tx_hash        = 6;
  bytes  token          = 7;   // 20 byte
  bytes  holder         = 8;   // người gửi (X)
  bytes  balance_before = 9;   // 32 byte: số dư holder NGAY TRƯỚC giao dịch trong block
  bytes  balance_after  = 10;  // 32 byte: NGAY SAU giao dịch
  bytes  signature      = 11;
}

// Lời khai nonce để chứng minh "đây là toàn bộ giao dịch của X".
message SignedNonceStatement {
  uint32 schema_version = 1;
  uint64 chain_id       = 2;
  uint64 block_number   = 3;
  bytes  block_hash     = 4;
  bytes  account        = 5;   // 20 byte
  uint64 nonce          = 6;   // nonce của tài khoản tại cuối block này (= số giao dịch đã gửi nếu bắt đầu từ 0)
  bytes  signature      = 7;
}
```

Cả hai ký bằng khoá dùng chung, cùng cơ chế chữ ký xác định (mục 4.1). Người dùng lưu chúng cùng giao dịch của mình.

### 11.3. Ba loại vi phạm chứng minh được chỉ bằng dữ liệu của X  (Tầng 1)

**(1) Cận dưới số dư bị vi phạm (`FLOOR_VIOLATION`).**
Cho một lời khai nền `S0` (số dư `V0` tại `B0`, hoặc `V0 = 0` từ lúc token ra đời) và một lời khai `S` (số dư `V` tại `B > B0`). Người dùng nộp:
- `O`: **mọi** giao dịch X gửi trong `(B0, B]` (đủ vì nonce liên tiếp, kiểm bằng hai `SignedNonceStatement` tại `B0` và `B` cho `n0` và `n`, cộng đúng `n − n0` giao dịch với nonce `n0..n−1`), mỗi giao dịch kèm receipt; **tổng chi** `out = Σ giá trị Transfer(X → *)` trong log của các receipt đó (kể cả chi qua DEX/router vì log nằm trong receipt của chính giao dịch của X);
- `I ⊆` các khoản **X biết đã nhận** trong `(B0, B]`, mỗi khoản có bằng chứng thanh toán (block trong MMR + receipt + log `Transfer(* → X)` trong receipt thành công); **tổng nhận đã biết** `in`;
- `A`: tổng hạn mức **đã cấp** (`Approval`/`permit` của X trong lịch sử của X) còn có thể bị rút bởi người khác — xem điều kiện dưới.

Parent Chain tính **`L = V0 + in − out − A`** (không âm hoá về 0). Nếu **`V < L`** thì node đã khai một số dư **thấp hơn mức X chắc chắn phải có** ⇒ `NODE_AT_FAULT` (dựa trên **chữ ký của chính node** ở `S0` và `S`).

**Điều kiện an toàn (nếu không thoả thì cận dưới yếu đi, không sai):**
- Chiều **ra** chỉ do giao dịch của X **hoặc** do người được cấp hạn mức. `A` là chặn trên tổng số tiền người khác có thể rút; hạn mức vô hạn ⇒ `L ≤ 0`, **không có bảo vệ**. Khuyến nghị: cấp hạn mức vừa đủ, thu hồi sau khi dùng.
- **`permit` (chữ ký hạn mức ngoài chuỗi):** X phải khai; chưa hỗ trợ là **rủi ro chưa xử lý**.
- Token **không có hàm quản trị làm giảm số dư** (blacklist, `burnFrom` bởi chủ, rebasing, thu hồi) — job kiểm tra tuân thủ (`SEQUENCER_PARENT_ANCHORING.md` mục 3.2.1) loại token như vậy.
- Baseline `S0` cũng là lời khai của node: nếu node đã khai thấp ngay tại `B0`, dùng baseline **cũ hơn**; tệ nhất là `V0 = 0` từ lúc token ra đời (khi đó `I` phải gồm mọi khoản đã nhận).

**(2) Mâu thuẫn nội bộ trong lời khai của node (`SELF_CONTRADICTION`).**
Không cần dữ liệu ngoài, kiểm bằng số học trên các lời khai đã ký:
- **Thực thi sai giao dịch của X:** `SignedTxStatement` cho giao dịch có log `Transfer(X → Y, v)` mà `balance_before − balance_after ≠ v` (cho token không phí), hoặc `balance_after` khác kỳ vọng theo log.
- **Chi vượt số dư:** giao dịch thành công với `balance_before < v`.
- **Số dư biến mất giữa hai giao dịch của X:** hai `SignedTxStatement` liên tiếp (nonce `k`, `k+1`) mà `balance_before(k+1) < balance_after(k)` và **không có** khoản chi nào khác của X ở giữa (nonce liên tiếp chứng minh không có giao dịch nào của X xen giữa) và `A = 0` (không có người khác rút được).
- **Hai lời khai mâu thuẫn** về cùng `(token, holder, block/txIndex)`.

**(3) Giao dịch của X bị xử lý khác điều X ký (`TX_MISMATCH`).**
`txBytes` (calldata `transfer(to, amt)` trực tiếp, ABI chuẩn) đã được node đưa vào block và ghi `SUCCESS`, nhưng log `Transfer` **thiếu** hoặc **khác** người nhận/số lượng. Chứng minh bằng receipt đã được header cam kết — **không cần lời khai nào**, chỉ cần MMR + bằng chứng receipt.

### 11.4. Luồng khiếu tối giản (Tầng 1)

```
1. Người dùng gom: SignedStatement(S0,S) và/hoặc SignedTxStatement, SignedNonceStatement,
   giao dịch của mình + receipt + bằng chứng MMR, (tuỳ chọn) bằng chứng thanh toán đã nhận.
2. Gọi trên Parent Chain: reportViolation(kind, evidence)  + ký quỹ nhỏ.
3. Parent kiểm MỌI thứ bằng bằng chứng xác định: chữ ký của node, MMR, receipt, nonce, số học.
4. Đúng   -> NODE_AT_FAULT: forfeit một phần SecurityBond, bồi thường người báo, hoàn ký quỹ.
   Sai    -> từ chối, ký quỹ bị tịch thu một phần (chống báo bừa).
```

Không có bản khai lịch sử của node, không có cây Merkle-sum, không cửa sổ thử thách dài, **không cần node trả lời** (mọi thứ nằm trong dữ liệu người dùng đã ký/lưu). Vì vậy **không có vấn đề "node im lặng / giấu dữ liệu"** ở Tầng 1.

### 11.5. Ai lưu gì (thay cho bảng ở mục 3)

| Bên | Lưu |
|---|---|
| **Parent Chain** | `MmrAnchor` mỗi chain (~100 byte) + `SecurityBond` có sẵn. Bản ghi báo cáo chỉ tồn tại trong lúc xử lý (một giao dịch, không cần lưu lâu). |
| **Người dùng** | (1) mọi giao dịch mình gửi + receipt + header + bằng chứng MMR; (2) các `SignedStatement`/`SignedTxStatement`/`SignedNonceStatement` node trả về **ngay lúc giao dịch**; (3) bằng chứng thanh toán **do người trả đưa** cho các khoản mình nhận (nếu muốn cận dưới chặt hơn); (4) danh sách hạn mức đã cấp (chính là các giao dịch `approve` của mình). |
| **Node** | như cũ; **phải** ký và trả `SignedTxStatement`/`SignedNonceStatement` cùng receipt (điểm móc RPC ký, mặc định-tắt). |

**Quy tắc cho người dùng:** *lưu mọi giao dịch mình gửi, mọi lời khai node trả về, và mọi bằng chứng thanh toán người khác đưa cho mình.* Bằng chứng MMR cũ cần còn dùng được (mục 6.2): người dùng nên lưu kèm `MmrAnchor` có chữ ký tại thời điểm giao dịch.

### 11.6. Tầng 2 (mục 4–7) còn vai trò gì

Chỉ khi có bên **giữ dữ liệu đầy đủ** (watcher). Có một nguồn dữ liệu tự nhiên: **người gửi** một khoản tiền đang nhớ giao dịch đó; nên khi một khiếu nại về `(token, holder)` được mở, **mọi người dùng từng gửi cho holder** đó có thể thử thách việc bỏ sót sự kiện của mình — nhưng chỉ khi họ theo dõi các khiếu nại đang mở và còn giữ dữ liệu. Đây là bảo đảm **yếu, không nên dựa vào** ở giai đoạn đầu.

### 11.7. So sánh các phương án dưới ràng buộc mới

| Phương án | Cần người dùng nhớ gì | Chứng minh được | Còn thiếu |
|---|---|---|---|
| **B Tầng 1 (mục 11)** | giao dịch của mình + lời khai node | cận dưới; thực thi sai giao dịch của mình; mâu thuẫn lời khai | không chứng minh **số dư chính xác** |
| B Tầng 2 (mục 4–7) | như trên | số dư đúng khoảng giữa 2 lời khai | cần watcher, phức tạp |
| **A** — Sổ holder neo mọi block (`SEQUENCER_PARENT_ANCHORING.md` 3.2) | **không cần** nhớ gì | số dư chính xác (tin sổ do node dựng, replica đối chiếu) | node làm thêm mỗi block; cần token phát đủ sự kiện |
| **C** — Bằng chứng state ô lưu trữ | **không cần** nhớ gì | số dư chính xác dưới gốc state đã neo | cần các phần NOMT chưa có (ô lưu trữ 2 tầng, bằng chứng theo block cũ, kiểm chứng phi trạng thái) |

**Kết luận:** nếu người dùng thật sự chỉ nhớ giao dịch của mình, thì:
- **Bảo vệ tối thiểu, rẻ nhất:** Tầng 1 (chỉ cần dữ liệu người dùng, không cần watcher).
- **Muốn chứng minh số dư chính xác** thì **không thể** dựa vào trí nhớ của người dùng: phải dùng A hoặc C (dữ liệu do node/chuỗi cam kết).

### 11.8. Test bổ sung `T-DP-20..`

| ID | Test | Đạt khi |
|---|---|---|
| T-DP-20 | `FLOOR_VIOLATION`: node khai `V < L` | `NODE_AT_FAULT` |
| T-DP-21 | Node khai `V ≥ L` | từ chối báo cáo (không có vi phạm chứng minh được) |
| T-DP-22 | Thiếu 1 giao dịch của X trong `O` (nonce không liên tiếp) | từ chối: không chứng minh được tính đầy đủ |
| T-DP-23 | Nonce statement không khớp số giao dịch nộp | từ chối |
| T-DP-24 | X có `approve` vô hạn | `L ≤ 0`, không báo cáo được (không bị hiểu nhầm là an toàn) |
| T-DP-25 | Token có hàm quản trị làm giảm số dư | bị loại khỏi danh sách hỗ trợ |
| T-DP-26 | `SELF_CONTRADICTION`: `balance_before − balance_after ≠ v` | phát hiện |
| T-DP-27 | Chi vượt số dư theo chính lời khai của node | phát hiện |
| T-DP-28 | Số dư biến mất giữa hai giao dịch của X (không có `approve`) | phát hiện |
| T-DP-29 | `TX_MISMATCH`: `transfer` thành công nhưng thiếu/khác log | phát hiện, không cần lời khai |
| T-DP-30 | Bằng chứng thanh toán bị sửa 1 bit / thuộc block không có trong MMR | từ chối |
| T-DP-31 | Báo cáo bừa | ký quỹ bị tịch thu một phần |
| T-DP-32 | Chữ ký lời khai giả (khoá khác) | từ chối |
| T-DP-33 | Hai lời khai mâu thuẫn cùng `(token, holder, block)` | phát hiện, slash |

### 11.9. Quyết định cần bạn xác nhận

| Quyết định | Đề xuất |
|---|---|
| Mặc định **Tầng 1**; Tầng 2 chỉ khi có watcher | Đồng ý |
| Chấp nhận **không chứng minh được số dư chính xác** bằng trí nhớ người dùng; muốn số dư chính xác thì dùng A hoặc C | Cần bạn xác nhận (đây là điểm mấu chốt) |
| Node **phải ký** `SignedTxStatement`/`SignedNonceStatement` kèm receipt cho giao dịch ERC20 (một điểm móc RPC ký, mặc định-tắt) | Đồng ý |
| Người trả chia sẻ **bằng chứng thanh toán** cho người nhận (quy ước sản phẩm, ngoài giao thức) | Khuyến nghị |
| Xử lý `permit`/hạn mức vô hạn: chưa hỗ trợ và ghi rõ giới hạn | Đồng ý |
