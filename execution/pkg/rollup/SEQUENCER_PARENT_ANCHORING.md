# Neo (anchor) block lên Parent Chain và chứng minh bằng chứng (ERC20)

> **Trạng thái:** DRAFT phân tích (2026-09-24). Bổ sung cho `SEQUENCER_STEP_BY_STEP_PLAN.md` (Giai đoạn F mới) và `SEQUENCER_SCHEMAS_AND_TEST_PLAN.md`.
> **Câu hỏi gốc:** node thực thi nên đồng bộ gì lên Parent Chain (hash giao dịch theo `from` + `nonce`, hash block…) để người dùng có thể nộp một loại giao dịch chứng minh "tôi đã thực hiện tất cả các input này và thành công", đủ để chứng minh số dư ERC20?
>
> **⚠️ Đã được rà lại và hợp nhất (2026-09-25):** phương án A trong tài liệu này (sổ holder neo trong anchor) trở thành **nền** của thiết kế khuyến nghị **`SEQUENCER_FRAUD_PROOF_DESIGN.md`**, với hai sửa đổi: `HolderLeaf` dùng **cây sự kiện Merkle-sum** (`events_root`) thay cho `event_chain_hash`, và Parent giữ **một MMR các anchor** thay cho từng anchor rời. Xem R1–R12 ở đó.
>
> **Phạm vi thu hẹp (2026-09-25):** chỉ cần chứng minh **số dư của 1 người trên 1 contract ERC20**, khi đã có **toàn bộ lịch sử giao dịch (vào và ra) của người đó**. Thiết kế chọn nằm ở **mục 3.2** và thay thế các phần "bằng chứng state" ở mục 5.2 trên đường chính; các phần khác của tài liệu vẫn đúng.
>
> **Quy ước:** **[XÁC MINH]** = đã đối chiếu code (có vị trí). **[ĐỀ XUẤT]** = thiết kế mới. **[CHƯA XÁC MINH]** = phải kiểm tra trước khi làm.

---

## 1. Kết luận nhanh

1. **Không nên đồng bộ danh sách hash từng giao dịch lên Parent Chain.** Chi phí tỉ lệ với số giao dịch (gas + dung lượng) và không cần thiết: chỉ cần neo **một bộ gốc băm cho mỗi block** (hash block + 3 gốc Merkle). Danh sách hash giao dịch đầy đủ nằm ở node/Archival (ngoài chuỗi) và **kiểm chứng được** bằng bằng chứng Merkle so với gốc đã neo.
2. **"Mọi input của tôi đều thành công" KHÔNG đủ để chứng minh số dư ERC20.** Số dư còn phụ thuộc vào giao dịch **của người khác** (nhận tiền, `transferFrom`, mint, rebase), vào **nội dung** giao dịch (calldata, số lượng), và vào logic contract (phí chuyển, proxy, hook). Danh sách input của riêng X chỉ chứng minh được phía *đi ra* trong điều kiện lý tưởng.
3. **Cách chứng minh số dư ERC20 gọn và đúng là bằng chứng state** (Merkle proof của ô lưu `balances[X]` dưới gốc state đã neo), không phải cộng dồn lịch sử. Lịch sử giao dịch chỉ dùng cho loại chứng minh khác: **"giao dịch T đã thực hiện thành công trong block B"** (chứng minh hành động), ví dụ để mở khoá một khoản trên Parent Chain.
4. Vì vậy mình đề xuất **hai loại giao dịch chứng minh** trên Parent Chain: **`ProveTxIncluded`** (hành động) và **`ProveStorageSlot`** (số dư/trạng thái), cùng một nền là **`BlockAnchor`**.
5. **Cập nhật theo phạm vi thu hẹp (mục 3.2):** khi chỉ cần ERC20 và "biết hết lịch sử của 1 người", thiết kế chọn là **Sổ holder ERC20** (`HolderLedger`): node dựng, từ các log `Transfer` chuẩn, một cây `(token, holder) → {balance, eventCount, eventChainHash}` và neo gốc của nó. Có 2 loại chứng minh: **`proveErc20Balance`** (1 bằng chứng Merkle, nhanh) và **`proveErc20History`** (người dùng nộp **toàn bộ** sự kiện của holder, Parent kiểm từng sự kiện với receipt và đối chiếu số lượng + chuỗi băm → **không thể bỏ sót** giao dịch nào). Cách này **tránh được hai vướng mắc lớn** của bằng chứng state (không có bằng chứng ô lưu trữ contract, không sinh được bằng chứng theo block cũ).

---

## 2. Hiện trạng trong code

| Điều | Kết quả | Vị trí |
|---|---|---|
| Header block đã cam kết những gốc nào | `lastBlockHash`, `blockNumber`, `accountStatesRoot`, `stakeStatesRoot`, `receiptRoot`, `transactionsRoot`, `logsBloom`, `timestamp`, `globalExecIndex`, `commitIndex`… **[XÁC MINH]** | `pkg/block/block_header.go:28` |
| Cây giao dịch | `transactionsRoot` là gốc của trie lưu giao dịch, **khoá là hash giao dịch** (không khoá theo `from`/`nonce`) **[XÁC MINH]** | `pkg/transaction_state_db` (`GetTransaction(hash)`) |
| Receipt | Có `Status()` và `EventLogs()`; gốc receipt được tính khi lắp block **[XÁC MINH]** | `types/receipt.go`, `calculateReceiptsRoot` |
| State tài khoản | `AccountState` chứa `smartContractState.storageRoot` → gốc state cam kết cả gốc lưu trữ của contract (ERC20) **[XÁC MINH]** | `state/account_state.go:29`, `state/smart_contract_state.go:18` |
| Bằng chứng state | RPC `GetProof(address, block)` chỉ cho **tài khoản** (không cho ô lưu trữ contract) **[XÁC MINH]**; NOMT chỉ sinh bằng chứng theo **gốc hiện tại**, không theo block cũ (kết luận từ các lần kiểm trước, nomt `nomt_generate_proof` không nhận gốc/độ cao) | `cmd/simple_chain/rpc_state.go:433`, `pkg/nomt_ffi/bridge.go:989` |
| Đã có gì trên Parent Chain | `SubmitCheckpoint(chainID, epoch, blockHeight, stateRoot, validatorSetHash, cert)`: **chỉ giữ checkpoint mới nhất mỗi chain**, chỉ có `stateRoot`, không có hash block, không có gốc giao dịch/receipt **[XÁC MINH]** | `cross_chain/gateway.go` (`ChainCheckpoint`, `SubmitCheckpoint`) |
| `AccountState.lastHash` | có trường, ý nghĩa (hash giao dịch cuối hay hash block) **[CHƯA XÁC MINH]** | `state/account_state.go:31` |

---

## 3. Phân tích: cái gì đủ để chứng minh số dư ERC20?

| # | Cách | Chứng minh được gì | Vì sao đủ / thiếu |
|---|---|---|---|
| A | Danh sách input thành công của X (đề xuất gốc) | Các giao dịch **X gửi** đã được đưa vào và thành công | **Thiếu:** không thấy dòng tiền vào từ người khác; không cho biết số lượng nếu chưa có calldata; không thấy tác động của contract khác gọi vào token; tx thành công vẫn có thể chuyển ít hơn `amount` (token có phí). |
| B | Cộng dồn sự kiện `Transfer` (receipt logs) | Số dư = Σ vào − Σ ra, với token chuẩn | **Thiếu tính đầy đủ:** phải thấy **mọi** log liên quan X trong mọi block từ đầu. `logsBloom` chỉ chứng minh được "không có" (không có âm tính giả), còn block có bit bật phải lộ toàn bộ receipt. Chi phí tỉ lệ độ dài chuỗi → không phù hợp chứng minh on-chain. |
| C | **Bằng chứng state của ô lưu trữ** (`balances[X]`) dưới `accountStatesRoot` đã neo | **Số dư đúng tại block B**, không cần lịch sử | **Đủ và gọn (kích thước logarit).** Cần: gốc state đã neo, sinh được bằng chứng 2 tầng (tài khoản → `storageRoot` → ô lưu trữ), bộ kiểm chứng phi trạng thái. |
| D | Phát lại toàn bộ (công bố mọi giao dịch + tái thực thi xác định) | Mọi thứ, kể cả nội bộ | Đúng nhưng nặng (Hướng B của `SEQUENCER_DESIGN.md` mục 15.4); dùng cho kiểm toán/giải quyết tranh chấp, không cho người dùng thường. |
| E | Operator ký xác nhận số dư | Chỉ là lời của operator | Không trustless; chỉ hợp cho mô hình custodial thử nghiệm. |

**Kết luận phân tích:** với ERC20 dùng **C**. Dùng **A** khi mục đích là *chứng minh hành động* (ví dụ "tôi đã đốt/khoá V" để nhận V ở chỗ khác), không phải chứng minh số dư.

### 3.1. Bằng chứng đầy đủ cho "toàn bộ input của X" nếu vẫn muốn

Chỉ cần khi cần chứng minh *X không có giao dịch nào khác ngoài danh sách*:
- Giao thức đã bắt buộc `nonce = nonce hiện tại + 1`, nên các giao dịch của X có nonce liên tiếp, mỗi nonce đúng một giao dịch.
- Bằng chứng: **(i)** bằng chứng tài khoản của X tại block B cho thấy `nonce = n` (từ `accountStatesRoot`), **(ii)** với mỗi `k = 0..n-1` một bằng chứng đưa-vào của giao dịch nonce `k` (kèm block chứa nó).
- Chi phí O(n) bằng chứng. **Không có ánh xạ (from, nonce) → hash trong cây giao dịch hiện có** (cây khoá theo hash) nên nút/Archival phải giữ **chỉ mục ngoài chuỗi** `(from, nonce) → (block, txHash)`; Parent Chain chỉ kiểm tra bằng chứng.
- Nếu Parent Chain cần tự kiểm **tính liên tiếp theo block** mà không cần O(n) bằng chứng, có thể thêm một gốc riêng `txIndexRoot` (mục 4.2). Lưu ý: gốc này **không nằm trong header** (thêm vào header là đổi băm block, tức sửa code cũ) nên chỉ do node ký, kiểm chứng được bằng Archival/replay.

### 3.2. Phạm vi thu hẹp: ERC20 + lịch sử đầy đủ của 1 holder  **[ĐỀ XUẤT]**

**Yêu cầu:** với một token ERC20 `T` và một holder `X`, chứng minh `balanceOf(X)` tại block `B` khi đã biết mọi giao dịch của `X` (chiều vào **và** chiều ra).

**Vấn đề thật sự không phải là "cộng các giao dịch" mà là chứng minh danh sách đã ĐẦY ĐỦ.** Header block không đánh chỉ mục theo holder, nên Parent Chain **không tự biết** người dùng có bỏ sót một giao dịch (đặc biệt là giao dịch *đi ra*) hay không; một người dùng có thể cố ý bỏ các giao dịch trừ tiền để phồng số dư. Vì vậy phải có một **cam kết về tính đầy đủ theo `(token, holder)`**.

**Thiết kế chọn — Sổ holder ERC20 (`HolderLedger`):**

1. Sau khi mỗi block được thực thi (không nằm trên đường xử lý block, không ảnh hưởng băm block), node đọc các receipt **thành công** của block và lấy các log `Transfer(address indexed from, address indexed to, uint256 value)` (topic0 = `keccak256("Transfer(address,address,uint256)")`) **của các token đã đăng ký**.
2. Với mỗi log, cập nhật **hai** holder (`from` trừ `value`, `to` cộng `value`); địa chỉ `0x0` (mint/burn) không có sổ; chuyển cho chính mình cộng trừ triệt tiêu nhưng **vẫn tính là 1 sự kiện**.
3. Sổ là một cây (đề xuất dùng lại trie MPT của `pkg/trie`, cập nhật tăng dần, khoá `keccak256(token ‖ holder)`):

```proto
message HolderLeaf {
  uint32 schema_version    = 1;  // = 1
  bytes  token             = 2;  // 20 byte
  bytes  holder            = 3;  // 20 byte
  bytes  balance           = 4;  // 32 byte big-endian (không âm)
  uint64 event_count       = 5;  // số sự kiện Transfer liên quan holder tới block này
  bytes  event_chain_hash  = 6;  // h_n; h_0 = 32 byte 0; h_i = keccak256(h_{i-1} ‖ be64(block) ‖ be32(txIndex) ‖ be32(logIndex) ‖ deltaSigned33)
  uint64 last_block        = 7;  // block gần nhất có sự kiện của holder
}
```
   `deltaSigned33` là số nguyên có dấu 33 byte (1 byte dấu + 32 byte giá trị). Thứ tự sự kiện là **tăng dần theo `(block, txIndex, logIndex)`**.
4. **Gốc của cây (`holder_ledger_root`) được neo cùng `BlockAnchor`** (mục 4.2), kèm một **accumulator các hash block** (`block_hash_mmr_root`, Merkle Mountain Range) để chứng minh một block cũ bất kỳ có thật với O(log n) thay vì phải nộp chuỗi header O(K).
5. **Mọi replica tự dựng cùng một cây** từ cùng receipt (hàm xác định) và **đối chiếu `holder_ledger_root` với nhau** (thêm bất biến): lệch → replica lệch dừng. Nhờ đó operator không thể neo sổ sai mà các replica không phát hiện.

**Hai loại chứng minh:**

| Loại | Người dùng nộp | Parent Chain kiểm | Tin vào | Chi phí |
|---|---|---|---|---|
| `proveErc20Balance(token, holder, B)` | `HolderLeaf` + bằng chứng Merkle dưới `holder_ledger_root` của anchor | bằng chứng Merkle hợp lệ; trả `balance` | sổ do node dựng (đã được các replica đối chiếu) | O(log n) |
| `proveErc20History(token, holder, B)` | **toàn bộ** `event_count` sự kiện của holder, theo thứ tự; mỗi sự kiện kèm bằng chứng block (MMR) và bằng chứng receipt | (a) mỗi sự kiện là log `Transfer` thật, của đúng `token`, trong receipt `SUCCESS` của một block thật; (b) số sự kiện nộp `== event_count` của lá; (c) chuỗi băm dựng lại `== event_chain_hash`; (d) `Σ delta == balance`; (e) thứ tự tăng ngặt | **chỉ** tin header/`receiptRoot` (đã neo) và `event_count` của lá | O(số sự kiện) — chia được thành các đoạn (mục 3.2.2) |

**Vì sao chặn được việc bỏ sót giao dịch:** lá đã cam kết `event_count` và `event_chain_hash` của **toàn bộ** lịch sử. Nộp thiếu → sai số lượng; nộp thừa/đổi chỗ → sai chuỗi băm; bịa sự kiện → không có bằng chứng receipt. Người dùng không thể chọn lọc.

**Giới hạn về niềm tin (nói thẳng):** tính đầy đủ dựa vào việc node dựng đúng sổ (`event_count` đúng). Header không cam kết chỉ mục theo holder, nên **không có cách nào tránh tin vào sổ** trừ khi (i) các replica đối chiếu, (ii) Archival độc lập tái dựng và có thể báo gian lận (cửa sổ thử thách, mục 7), hoặc (iii) nâng cấp thành bằng chứng hoàn toàn không tin cậy bằng bloom phân tầng: chứng minh "không có log liên quan trong đoạn block này" cho từng đoạn — O(số đoạn) bằng chứng, để dành cho giai đoạn sau.

#### 3.2.1. Điều kiện để dùng được cho một token

- Token phải **phát đủ sự kiện `Transfer` cho mọi thay đổi số dư** (mint, burn, chuyển, chuyển có phí). **Không hỗ trợ:** token rebasing hoặc đổi số dư không phát sự kiện, token tự đổi hành vi qua nâng cấp proxy mà không giữ quy ước sự kiện.
- **Kiểm tra tuân thủ trước khi đăng ký token** (job ngoại tuyến): dựng sổ và so `balance` với `balanceOf` thật ở nhiều block/holder; chỉ đăng ký token nếu khớp 100%. Token đã đăng ký mà sau này lệch → tạm dừng cho phép chứng minh token đó.
- Chuyển cho chính mình, mint/burn (`0x0`), nhiều log `Transfer` trong một giao dịch, giao dịch **thất bại** (không tạo sự kiện): mỗi trường hợp có test riêng (mục 8).

#### 3.2.2. Lịch sử dài: chứng minh theo đoạn

Holder có hàng chục nghìn sự kiện không nộp nổi trong một giao dịch. Cho phép **chứng minh theo đoạn**: đoạn 1 chứng minh các sự kiện `1..k` khớp `event_chain_hash` và `balance` **tại anchor cũ** `B1` (lá đã neo); đoạn 2 nối tiếp từ trạng thái của `B1` tới anchor `B2` (`h_{k+1..m}`); Parent Chain **không lưu trạng thái tạm** — mỗi đoạn tự chứa và tham chiếu hai lá đã neo `(B1, B2)`: nếu lá `B1` đã được xác minh (bằng `proveErc20Balance` hoặc đoạn trước) thì đoạn chỉ cần chứng minh **hiệu** giữa hai lá.

#### 3.2.3. So với bằng chứng state (mục 5.2)

| | Sổ holder ERC20 | Bằng chứng state ô lưu trữ |
|---|---|---|
| Phụ thuộc layout lưu trữ token | Không | Có (slot khác nhau theo token/proxy) |
| Cần bằng chứng theo block cũ của NOMT | Không (dùng cây do node dựng + MMR) | Có (chưa có) |
| Cần bằng chứng ô lưu trữ 2 tầng | Không | Có (chưa có) |
| Cần kiểm chứng phi trạng thái NOMT trong Go | Không | Có (chưa có) |
| Token phát sự kiện đầy đủ | **Bắt buộc** | Không cần |
| Tin vào operator | Sổ do node dựng (replica đối chiếu, Archival kiểm) | Chỉ `accountStatesRoot` đã neo |
| Chi phí chứng minh số dư | O(log n) (balance) hoặc O(sự kiện) (history) | O(log n) |

Kết luận: với phạm vi **ERC20 + lịch sử holder**, Sổ holder ERC20 **đơn giản hơn để làm** (không phụ thuộc các phần chưa có của NOMT) nhưng **yếu hơn về niềm tin** (tin sổ do node dựng) và **hẹp hơn về loại token**. Bằng chứng state giữ làm **tuỳ chọn** (mục 5.2), không nằm trên đường chính.

---

## 4. Node thực thi nên đồng bộ gì lên Parent Chain

### 4.1. Ba mức

| Mức | Nội dung | Tần suất | Chi phí | Khuyến nghị |
|---|---|---|---|---|
| 0 | `SubmitCheckpoint` hiện có (`blockHeight`, `stateRoot`), chỉ giữ mới nhất | định kỳ | thấp | **Không đủ** (không có hash block/gốc giao dịch/receipt, không giữ lịch sử) |
| 1 | **`BlockAnchor`**: `blockHash` + `accountStatesRoot` + `transactionsRoot` + `receiptRoot` (+ chuỗi băm batch) | mỗi K block hoặc mỗi T giây | O(1)/lần | **Đề xuất** |
| 2 | Danh sách hash từng giao dịch `(from, nonce, txHash, status)` | mỗi block | O(số giao dịch)/block | **Không nên** trên chuỗi; giữ ở Archival, cam kết bằng gốc (mức 1 hoặc `txIndexRoot`) |

**Vì sao mức 1 đủ:** `blockHash` cam kết toàn bộ header, gồm cả ba gốc và `lastBlockHash` (chuỗi block). Người dùng nộp header đầy đủ (hoặc Parent lưu sẵn ba gốc) cộng bằng chứng Merkle thì Parent Chain xác minh được giao dịch/receipt/ô lưu trữ mà không cần biết các giao dịch khác.

### 4.2. `BlockAnchor` — schema  **[ĐỀ XUẤT]**

Lưu trên Parent Chain, **mỗi anchor một storage key** (bài học per-key), có **vòng giữ lịch sử có trần** để không phình vô hạn.

```proto
message BlockAnchor {
  uint32 schema_version      = 1;  // = 1
  uint64 chain_id            = 2;
  uint64 block_number        = 3;
  bytes  block_hash          = 4;  // 32 byte
  bytes  prev_anchor_hash    = 5;  // block_hash của anchor liền trước (liên kết chuỗi anchor)
  bytes  account_states_root = 6;  // 32 byte
  bytes  transactions_root   = 7;  // 32 byte
  bytes  receipt_root        = 8;  // 32 byte
  bytes  batch_commit_hash   = 9;  // chuỗi băm batch tại Apply (schema mục 1.3), liên kết với log Raft
  uint64 tx_count_total      = 10; // tổng số giao dịch tích luỹ tới block này
  bytes  tx_index_root       = 11; // TUỲ CHỌN: gốc Merkle của các lá (from,nonce,txHash,status) đã sắp — do node tính, không nằm trong header
  bytes  holder_ledger_root  = 12; // gốc Sổ holder ERC20 (mục 3.2); do node dựng, các replica đối chiếu
  bytes  block_hash_mmr_root = 13; // accumulator (Merkle Mountain Range) của mọi block_hash tới block này
  uint64 block_hash_mmr_size = 14; // số lá đã có trong MMR (= block_number + 1 nếu bắt đầu từ genesis)
}
```

- Key: `keccak256("rollup_anchor_v1" ‖ be64(chainID) ‖ be64(blockNumber))`; cộng key trỏ tới anchor mới nhất và anchor cũ nhất còn giữ.
- **Chỉ neo block đã commit Raft và đã bền xuống DB** (cùng quy tắc thu gọn log, schema mục 1.4): finality của Raft chính là finality của anchor; không có reorg.
- Chữ ký: cùng cơ chế `SubmitCheckpoint` (`QuorumCert` của committee = 1 khoá dùng chung), với digest gắn miền `"ROLLUP_ANCHOR_V1:"` để không dùng lại chữ ký ở nơi khác.
- **Đơn điệu:** `block_number` mới phải lớn hơn anchor mới nhất; `prev_anchor_hash` phải khớp. Hai anchor **khác `block_hash` cho cùng `block_number`** là bằng chứng gian lận (mục 7).
- **Cadence K:** đánh đổi giữa độ trễ có-thể-chứng-minh và phí Parent Chain. Với block giữa hai anchor, bằng chứng phải kèm chuỗi header từ block đó tới anchor kế tiếp (O(K) header); để giảm về O(log) có thể neo **gốc Merkle Mountain Range của các `block_hash`** mỗi K block (tối ưu, giai đoạn sau).

### 4.3. `txIndexRoot` (tuỳ chọn) — chi tiết

Lá: `H("TXLEAF_V1:" ‖ from(20) ‖ be64(nonce) ‖ txHash(32) ‖ status(1))`, sắp tăng dần theo `(from, nonce)`; gốc Merkle nhị phân RFC-6962 (tái dùng `BuildMerkleTree`/`GenerateMerkleProof` của `relayer.go`, đã có). Nhờ sắp xếp, có bằng chứng **liền kề** (nonce `k` và `k+1` của cùng `from` nằm sát nhau) và bằng chứng **không tồn tại** của một `(from, nonce)` trong block. **Đánh đổi:** thêm việc tính mỗi block và không được header cam kết.

---

## 5. Hai loại giao dịch chứng minh trên Parent Chain  **[ĐỀ XUẤT]**

Cả hai nằm ở Gateway (cùng phong cách `verifyAndExecute`), là tx barrier tuần tự. Người gọi trả gas; Parent Chain **không lưu state ứng dụng**, chỉ xác minh.

### 5.1. `proveTxIncluded` — "giao dịch T đã thành công trong block B"

Đầu vào: `chainId, blockNumber, headerBytes, txBytes, txProof, receiptBytes, receiptProof, claimType, claimData`.

Kiểm tra theo thứ tự (dừng ở lỗi đầu tiên, không đoán):
1. Có `BlockAnchor(chainId, blockNumber)` **hoặc** chuỗi header hợp lệ tới một anchor sau đó.
2. `keccak256(headerBytes) == anchor.block_hash` (hoặc nối chuỗi `lastBlockHash`), và các gốc trong header khớp anchor.
3. `txProof` chứng minh `txBytes` nằm trong `transactions_root`; `keccak256(txBytes)` là `txHash`.
4. `receiptProof` chứng minh `receiptBytes` nằm trong `receipt_root`, và receipt tương ứng đúng `txHash`, `status == SUCCESS`.
5. **Chống phát lại:** `(chainId, txHash, claimType)` chưa được dùng (`ClaimedProofs`, tương tự `ClaimedMessages`); ghi nhận lần dùng.
6. Diễn giải `claimType` (ví dụ `ERC20_BURN`): giải `txBytes`/log theo ABI chuẩn để rút ra `(from, amount)`.

Dùng cho: mở khoá/nhả tiền khi đã chứng minh một hành động (đốt, khoá, nạp), cầu nối, huy hiệu, v.v.

### 5.2. `proveStorageSlot` — "ô lưu trữ S của contract C có giá trị V tại block B"

Đầu vào: `chainId, blockNumber, headerBytes, contract, slot, value, accountProof, storageProof`.

1. Xác minh header/anchor như trên → lấy `accountStatesRoot`.
2. `accountProof`: chứng minh tài khoản `contract` nằm trong `accountStatesRoot`, lấy `storageRoot` từ trạng thái contract.
3. `storageProof`: chứng minh ô `slot` có `value` dưới `storageRoot`.
4. Trả về/ghi sự kiện `(chainId, blockNumber, contract, slot, value)`; **diễn giải là việc của contract gọi**.

**Ví dụ ERC20 (Solidity/OpenZeppelin):** `slot = keccak256(abi.encode(holder, mappingSlot))`, `mappingSlot` là chỉ số của biến `_balances` trong layout của contract (thường `0` nhưng **khác nhau theo từng token**, proxy dùng vùng lưu trữ có tên riêng). Vì vậy nên có **sổ đăng ký mô tả token** (`contract → {balanceMappingSlot, layoutKind, decimals}`) do bên tích hợp quản lý; primitive `proveStorageSlot` **không** tự đoán layout.

Dùng cho: chứng minh số dư ERC20 tại block B, quyền sở hữu NFT, allowance, v.v.

### 5.3. Cái người dùng phải làm

```
1. Lấy anchor mới nhất ≥ block quan tâm (đọc Parent Chain).
2. Xin Archival/RPC của node: header, bằng chứng (tx/receipt/state) cho block đó.
3. Gửi giao dịch proveTxIncluded hoặc proveStorageSlot lên Parent Chain.
```

### 5.4. `proveErc20Balance`  **[ĐỀ XUẤT — đường chính cho ERC20]**

Đầu vào: `chainId, anchorBlock, token, holderLeaf (HolderLeaf), merkleProof`.
Kiểm: (1) có `BlockAnchor(chainId, anchorBlock)`; (2) `token` nằm trong sổ đăng ký token; (3) `merkleProof` chứng minh `keccak256(token ‖ holder) → holderLeaf` dưới `anchor.holder_ledger_root`. Kết quả: `(chainId, anchorBlock, token, holder, balance)`; **số dư là số dư tại `anchorBlock`**, không phải số dư hiện tại.

### 5.5. `proveErc20History`  **[ĐỀ XUẤT]**

Đầu vào: `chainId, anchorBlock, token, holder, holderLeaf, leafProof, events[]`, mỗi `event = {block, txIndex, logIndex, txBytes/receiptBytes, blockMmrProof, receiptProof}`.
Kiểm theo thứ tự (dừng ở lỗi đầu tiên):
1. Lá hợp lệ dưới `holder_ledger_root` của anchor (như 5.4).
2. `len(events) == holderLeaf.event_count` (hoặc đúng đoạn, mục 3.2.2).
3. Với mỗi sự kiện: `blockMmrProof` chứng minh `block_hash` nằm trong `anchor.block_hash_mmr_root`; header → `receiptRoot`; `receiptProof` chứng minh receipt trong `receiptRoot`; receipt `status == SUCCESS`; log tại `logIndex` do đúng `token` phát, topic0 là `Transfer`, một trong `from`/`to` bằng `holder`.
4. Thứ tự `(block, txIndex, logIndex)` tăng ngặt; dựng lại chuỗi băm `h_n`; so với `holderLeaf.event_chain_hash`.
5. `Σ delta (đã dấu) == holderLeaf.balance` và không bao giờ âm giữa chừng.
6. Chống phát lại **không cần** (chứng minh này không nhả tiền); nếu bên gọi dùng kết quả để nhả tiền thì bên đó tự chống phát lại.

---

## 6. Những điều còn thiếu để làm được (cần bổ sung ở node, không chỉ ở Parent Chain)

| Thiếu | Chi tiết | Trạng thái |
|---|---|---|
| Bằng chứng ô lưu trữ contract | `GetProof` hiện chỉ cho tài khoản; cần bằng chứng 2 tầng (tài khoản → `storageRoot` → ô) | **[XÁC MINH]** chưa có |
| Bằng chứng theo block cũ | NOMT chỉ sinh theo gốc hiện tại. Cần: (a) sinh và **lưu bằng chứng ngay lúc neo** cho tập ô đã đăng ký, hoặc (b) giữ được state cũ (backend MPT dựng lại từ gốc, `NewTransactionStateDBFromRoot`-style cho tài khoản), hoặc (c) chỉ hỗ trợ bằng chứng tại **anchor mới nhất** | **[CHƯA XÁC MINH]** phương án khả thi |
| Bộ kiểm chứng phi trạng thái | Cần kiểm chứng bằng chứng NOMT/MPT **không cần handle DB** trong Go (chưa có wrapper phi trạng thái, `PathProof::verify` chưa có FFI Go) | **[CHƯA XÁC MINH]** |
| Bằng chứng giao dịch/receipt | Khoá cây giao dịch là hash; cần định dạng bằng chứng MPT cho `transactionsRoot` và `receiptRoot`; khoá cây receipt **[CHƯA XÁC MINH]** | cần đọc `pkg/receipt` |
| Chỉ mục `(from, nonce)` | Ngoài chuỗi, ở node/Archival | cần xây (chỉ khi dùng mục 3.1) |
| Dịch vụ Archival | Lưu giao dịch, receipt, header, bằng chứng; phục vụ người dùng | thuộc `SEQUENCER_DESIGN.md` mục 6.3 (đã hạ ưu tiên) — **nay có lý do quay lại** |
| Sổ đăng ký mô tả token | layout `balances` cho từng token (đường bằng chứng state); **danh sách token đã qua kiểm tra tuân thủ sự kiện** (đường sổ holder) | thiết kế mới |
| **Bộ dựng Sổ holder ERC20** | đọc receipt sau mỗi block, cập nhật cây `(token, holder)`, xác định 100%, lưu bền cục bộ, đối chiếu gốc giữa replica | thiết kế mới; cây tăng dần dùng lại `pkg/trie` MPT **[CHƯA XÁC MINH]** hỗ trợ bằng chứng và cập nhật tăng dần đủ dùng |
| **Accumulator hash block (MMR)** | duy trì tăng dần, neo gốc; sinh bằng chứng O(log n) | thiết kế mới |
| **Vị trí sự kiện trong receipt** | cần `txIndex`, `logIndex` xác định và bằng chứng receipt kèm log | **[CHƯA XÁC MINH]** — đọc `pkg/receipt`, `pkg/proto` (`EventLog`) |
| Job kiểm tra tuân thủ token | so sổ với `balanceOf` trước khi đăng ký | thiết kế mới |

---

## 7. Mô hình tin cậy và rủi ro

- **Anchor là lời của operator (khoá dùng chung).** Nếu khoá bị lộ hoặc operator gian lận, kẻ đó có thể **neo một `accountStatesRoot` bịa** rồi dùng `proveStorageSlot` để "chứng minh" số dư tuỳ ý → **rút cạn mọi thứ đang được mở khoá bằng bằng chứng này**. Bằng chứng chỉ an toàn hơn lời operator ở chỗ *nhất quán với root đã neo*; nó **không** chứng minh root đúng với việc thực thi.
- **Giảm thiểu (nên có trước khi bằng chứng mở khoá tiền thật):**
  1. **Cửa sổ thử thách:** anchor chỉ có hiệu lực dùng cho khoản có giá trị sau `D` block/giây trên Parent Chain; trong thời gian đó Archival độc lập tái thực thi và có thể báo gian lận.
  2. **Chống hai anchor mâu thuẫn:** ký anchor bằng message miền riêng; hai anchor khác `block_hash` cùng `block_number` là bằng chứng equivocation — tái dùng `SlashOnEquivocation` (cơ chế đã xác nhận cho `AccountTreeRoot`, `#16`) với `ComputeCommitRootAttestMessage`-tương-tự, **không** cần code phát hiện riêng.
  3. **Giới hạn giá trị nhả theo cửa sổ** (velocity-limit `#11`).
  4. **Tái thực thi độc lập** (Hướng B) cho giá trị lớn.
- **Không có reorg:** anchor chỉ neo block đã commit Raft + bền DB.
- **Riêng tư:** bằng chứng lộ nội dung giao dịch (calldata) của các giao dịch được chứng minh; chấp nhận được (chuỗi không riêng tư).
- **Chi phí Parent Chain:** mỗi anchor một lần ghi + một lần kiểm chữ ký; mỗi bằng chứng là kiểm Merkle bằng Go thuần trong handler (không cần sửa MVM).

---

## 8. Tích hợp vào kế hoạch: Giai đoạn F (sau C5)

| Bước | Việc | Phụ thuộc | Kích thước |
|---|---|---|---|
| F0 | **Xác minh:** định dạng bằng chứng MPT của `receiptRoot` (vị trí `txIndex`/`logIndex`, log trong receipt); cây MPT của `pkg/trie` có dùng làm sổ holder tăng dần + bằng chứng được không; (chỉ nếu giữ đường bằng chứng state) sinh bằng chứng theo block cũ NOMT vs MPT và kiểm chứng phi trạng thái | — | S–M |
| F1 | `BlockAnchor` trên Parent Chain: schema (gồm `holder_ledger_root`, `block_hash_mmr_root`), `submitAnchor`, đơn điệu, vòng giữ lịch sử có trần, chống anchor mâu thuẫn; **gap analysis với `SubmitCheckpoint`** | A0, F0 | M |
| F2 | Worker neo (chỉ leader, sau commit Raft + bền DB), cadence K, retry idempotent theo `(chainID, blockNumber)` | C2, F1 | M |
| F2b | **Bộ dựng Sổ holder ERC20 + MMR hash block** trên mọi replica (xác định), đối chiếu gốc giữa replica, job kiểm tra tuân thủ token | F0, C2 | L |
| F3 | Dịch vụ bằng chứng: header, receipt, bằng chứng sổ holder, bằng chứng MMR, danh sách sự kiện đầy đủ của 1 holder; (tuỳ chọn) chỉ mục `(from, nonce)` và ô lưu trữ 2 tầng | F0, F2b | L |
| F4 | **`proveErc20Balance`, `proveErc20History` (+ theo đoạn)** và `proveTxIncluded` trên Parent Chain + `ClaimedProofs` + sổ đăng ký token; `proveStorageSlot` là tuỳ chọn | F1, F3 | L |
| F5 | Cửa sổ thử thách + equivocation slash cho anchor + tái thực thi độc lập | F4 | L |

**Test (ID mới `T-AN-*`):**

| ID | Test | Đạt khi |
|---|---|---|
| T-AN-01 | Anchor đơn điệu | `block_number` không tăng → từ chối; `prev_anchor_hash` sai → từ chối |
| T-AN-02 | Hai anchor khác hash cùng độ cao | bị phát hiện, kích hoạt slash |
| T-AN-03 | Chỉ neo block đã commit + bền | không bao giờ neo block chưa commit (kill giữa chừng) |
| T-AN-04 | Vòng giữ lịch sử có trần | anchor cũ bị loại đúng thứ tự, không phình |
| T-AN-05 | `proveTxIncluded` đúng | thành công, `ClaimedProofs` ghi nhận |
| T-AN-06 | Cùng `(chainId, txHash, claimType)` lần 2 | bị từ chối (chống phát lại) |
| T-AN-07 | Giao dịch **thất bại** | không được chứng minh là thành công |
| T-AN-08 | Header/bằng chứng bị sửa 1 bit | từ chối |
| T-AN-09 | `proveStorageSlot` cho số dư ERC20 tại block B | giá trị khớp `balanceOf` tại B (so với RPC) |
| T-AN-10 | Số dư thay đổi sau B | bằng chứng cũ vẫn đúng **tại B**, không bị hiểu là số dư hiện tại |
| T-AN-11 | Token có phí chuyển / proxy | `proveStorageSlot` vẫn đúng (khác với cộng dồn sự kiện) |
| T-AN-12 | Nonce liên tiếp (mục 3.1) | bằng chứng tài khoản `nonce=n` + `n` bằng chứng giao dịch → xác nhận đủ |
| T-AN-13 | Anchor bịa (khoá bị lộ) trong cửa sổ thử thách | khoản có giá trị lớn không được nhả trước khi hết cửa sổ |
| T-AN-14 | Dựng Sổ holder trên 2 replica từ cùng receipt | `holder_ledger_root` giống hệt; lệch → replica dừng |
| T-AN-15 | `proveErc20Balance` | `balance` khớp `balanceOf` thật tại block đó (so RPC) |
| T-AN-16 | `proveErc20History` đầy đủ | thành công; `Σ delta == balance` |
| T-AN-17 | **Bỏ sót 1 sự kiện** (đặc biệt sự kiện trừ tiền) | từ chối: sai `event_count` |
| T-AN-18 | Thêm sự kiện giả / đổi thứ tự hai sự kiện | từ chối: thiếu bằng chứng receipt / sai chuỗi băm |
| T-AN-19 | Sự kiện từ receipt **thất bại** hoặc từ token khác | từ chối |
| T-AN-20 | Chuyển cho chính mình; mint/burn (`0x0`); nhiều `Transfer` trong 1 giao dịch | số dư và `event_count` đúng theo quy tắc mục 3.2 |
| T-AN-21 | Token có phí chuyển (2 log `Transfer` mỗi lần) | sổ khớp `balanceOf`; job tuân thủ cho qua |
| T-AN-22 | Token rebasing / không phát sự kiện | job tuân thủ **loại** token, không cho đăng ký |
| T-AN-23 | Lịch sử dài chia đoạn | các đoạn nối tiếp đúng; đoạn không liền kề bị từ chối |
| T-AN-24 | Số dư về 0 rồi tăng lại; số dư tối đa `2^256-1` | không tràn, không âm |
| T-AN-25 | MMR: block cũ bất kỳ | bằng chứng O(log n) hợp lệ; sửa 1 bit → từ chối |

---

## 9. Quyết định cần bạn xác nhận

| Quyết định | Đề xuất |
|---|---|
| Chỉ neo gốc block (mức 1), **không** đưa danh sách hash từng giao dịch lên Parent Chain | Đồng ý với mức 1; danh sách hash ở Archival |
| Chứng minh số dư ERC20 bằng **bằng chứng state** (`proveStorageSlot`), còn danh sách input dùng cho `proveTxIncluded` | Như mục 3 |
| Có thêm `txIndexRoot` (gốc theo `(from, nonce)`, không nằm trong header) | Không ở giai đoạn đầu; thêm nếu cần chứng minh liên tiếp mà không phải O(n) bằng chứng |
| Cadence neo K | Chốt sau khi đo chi phí Parent Chain |
| Có cần cửa sổ thử thách + tái thực thi độc lập trước khi bằng chứng mở khoá tiền thật | **Có** với giá trị lớn (mục 7) |
| **Phạm vi ERC20 chỉ gồm token phát đủ sự kiện `Transfer`** (loại token rebasing/không sự kiện) | Đồng ý; có job kiểm tra tuân thủ trước khi đăng ký |
| **Đường chính = Sổ holder ERC20** (mục 3.2); bằng chứng state ô lưu trữ chỉ là tuỳ chọn | Đồng ý |
| Chấp nhận **tin vào sổ do node dựng** (replica đối chiếu + Archival kiểm) thay vì bằng chứng hoàn toàn không tin cậy bằng bloom phân tầng | Chấp nhận ở giai đoạn đầu; bloom phân tầng để giai đoạn sau |
| "Toàn bộ lịch sử" nghĩa là gồm cả **chiều vào** (do người khác gửi) | Có (nếu chỉ chiều ra thì không đủ tính số dư) |
| Mở rộng `SubmitCheckpoint` hay tạo `BlockAnchor` mới | Chốt sau F1 (gap analysis) |
