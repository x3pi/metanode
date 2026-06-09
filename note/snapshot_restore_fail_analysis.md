# 📸 Đánh Giá Kiến Trúc: Sự Cố Mismatch Khi Restore Snapshot NOMT

Tài liệu này phân tích chi tiết nguyên nhân lỗi pipeline khôi phục trạng thái (`Snapshot Restore Mismatch!`), luồng phản ứng chuỗi (chain reaction) dẫn đến crash fatal, và giải pháp khắc phục triệt để ở cả tầng vận hành (script) lẫn tầng mã nguồn Go Bridge.

---

## 1. Hiện Tượng & Triệu Chứng Lỗi

Khi một node khôi phục (restore) từ bản snapshot ở block cao (ví dụ: block #1600), hệ thống báo lỗi mismatch nghiêm trọng tại pha khởi động:

```
[WARN]  ⚠️ [STARTUP] stake_db NOMT is EMPTY (root=0x0) but header expects 0x7f2bffd5f0464f5a.... STARTUP-SYNC will reconcile.
[ERROR] 🚨 [STARTUP] NOMT stake_db handle root (0x00000000...) differs from header StakeStatesRoot (0x7f2bffd5f0464f5a...)!
[INFO]  📸 [SNAPSHOT FIX] Loaded metadata.json: Block=1600, GEI=1600, StateRoot=0x794d2ece...
[ERROR] ❌ [FATAL] Snapshot Restore Mismatch! NOMT root=0x5a3d995e..., but metadata.json claims StateRoot=0x794d2ece...
[ERROR] 🚨 [FATAL EXIT] FATAL: Snapshot restore failed. NOMT state corrupted or mismatched with metadata.
```

---

## 2. Phân Tích Nguyên Nhân Gốc (Root Cause Analysis)

Sự cố này xảy ra do sự kết hợp của hai lỗi độc lập: lỗi ánh xạ thư mục trong script khôi phục trạng thái và lỗi thiếu bước flush dữ liệu stake tại genesis khi tái khởi tạo khẩn cấp.

### Lỗi 1: Ánh xạ sai thư mục Snapshot trong `restore_node.sh`
Trong cấu trúc thư mục snapshot của Metanode, các CSDL NOMT (`account_state`, `stake_db`, `smart_contract_storage`) được đóng gói bên trong thư mục `consensus/nomt_db/`.

Khi chạy script `restore_node.sh` để giải nén và di chuyển dữ liệu vào thư mục chạy thực tế của node (`sample/node1/data/data/`), script sử dụng vòng lặp duyệt qua các thư mục con ở cấp cao nhất của thư mục giải nén:

```bash
for folder in "$SNAP_DIR"/*; do
  folder_name=$(basename "$folder")
  if [ "$folder_name" = "back_up" ]; then
      ...
  elif [ "$folder_name" = "history" ]; then
      cp -a "$folder"/* "$NODE_DATA/data/data/history/"
  ...
```

Tuy nhiên, script **hoàn toàn không có nhánh xử lý cho thư mục `consensus`**. Khi gặp thư mục `consensus`, script rơi vào nhánh `else`:
```bash
  else
      echo "    📦 Mapping other data folder: $folder_name -> data/data/$folder_name..."
      cp -a "$folder" "$NODE_DATA/data/data/"
  fi
```
Vì trước đó script đã tạo thư mục `$NODE_DATA/data/data/consensus/`, lệnh `cp -a` sẽ copy thư mục `consensus` từ snapshot vào **trong** thư mục `consensus` đã có sẵn, tạo thành cấu trúc thư mục lồng nhau:
`$NODE_DATA/data/data/consensus/consensus/nomt_db/...`

Hậu quả: 
- Thư mục CSDL NOMT chuẩn tại `$NODE_DATA/data/data/consensus/nomt_db/` bị bỏ trống.
- Khi Go Bridge khởi động, FFI Rust không tìm thấy database NOMT cũ nên đã tự động khởi tạo các database trống rỗng (root hash = `0x0`).

---

### Lỗi 2: Phản Ứng Chuỗi (Chain Reaction) Gây Reset Block Tip Về Genesis
Khi Go Bridge phát hiện CSDL NOMT trống rỗng (root hash = `0x0` hoặc trùng khớp với empty root hash), nó kích hoạt cơ chế tự phục hồi (Self-Healing) tại `app_blockchain.go`:

```go
isEmptyAccountNomt := (nomtAccountRoot == (e_common.Hash{}) || nomtAccountRoot == emptyAccountRoot || nomtAccountRoot == emptyStakeRoot)
isEmptyStakeNomt := (nomtStakeRoot == (e_common.Hash{}) || nomtStakeRoot == emptyAccountRoot || nomtStakeRoot == emptyStakeRoot)
isEmptyNomt := isEmptyAccountNomt || isEmptyStakeNomt

if isEmptyNomt && (headerAccountRoot != (e_common.Hash{}) || headerStakeRoot != (e_common.Hash{})) {
    // Reset block tip về genesis (block #0) để đồng bộ/thực thi lại
    app.startLastBlock = blk0
    storage.ResetAllBlockCounters(0)
}
```

Do `nomtStakeRoot == 0x0`, biến `isEmptyStakeNomt` và `isEmptyNomt` chuyển sang `true`, kéo theo việc reset block tip của Go Bridge về Genesis (Block #0). 

Sau khi reset block tip về 0, Go Bridge tải Account State DB từ block #0 (có root `0x5a3d995e...`).
Cuối cùng, pha kiểm tra Snapshot đối chứng active trie root hiện tại (`0x5a3d995e...`) với StateRoot lưu trong `metadata.json` (expect `0x794d2ece...` ở block #1600), dẫn đến crash fatal do sai lệch trạng thái.

---

### Lỗi 3: Thiếu `IntermediateRoot(true)` Trong `repopulateGenesisState`
Khi node bị reset về block #0 do phát hiện NOMT trống, nó sẽ chạy cơ chế `repopulateGenesisState()` để khôi phục lại dữ liệu phân bổ ban đầu (genesis allocations) và danh sách validators từ `genesis.json` nhằm đưa database về trạng thái block 0 chuẩn.

Trong `repopulateGenesisState()` của `app_blockchain.go`:
- Đối với **Account State**, hệ thống gọi đúng quy trình:
  ```go
  app.chainState.GetAccountStateDB().IntermediateRoot(true)
  app.chainState.GetAccountStateDB().Commit()
  ```
- Đối với **Stake State**, hệ thống chỉ gọi:
  ```go
  cs.Commit()
  ```
Do thiếu lệnh `cs.IntermediateRoot(true)`, các thay đổi về validator và stake (đang nằm ở cache `dirtyValidators`) **không được đẩy vào trie và session của NOMT**.
Khi `cs.Commit()` chạy, nó thấy trie trống (`wDirty` rỗng) nên lập tức bỏ qua việc commit payload xuống đĩa. CSDL `stake_db` trên đĩa tiếp tục duy trì trạng thái trống rỗng (root = `0x0`), trong khi trie cached root trong RAM vẫn giữ giá trị logic `0x7f2bffd5f0464f5a...`. Điều này dẫn đến sự mất nhất quán giữa bộ nhớ đệm và dữ liệu trên đĩa.

---

## 3. Giải Pháp Khắc Phục Triệt Để (Architectural Remediation)

Để sửa lỗi này một cách toàn diện, chúng ta triển khai 3 chỉnh sửa kiến trúc:

### Giải pháp 1: Sửa ánh xạ thư mục trong `restore_node.sh`
Thêm nhánh kiểm tra cụ thể cho thư mục `consensus` để giải nén trực tiếp nội dung bên trong nó vào thư mục đích, tương tự cách xử lý thư mục `history`:

```bash
elif [ "$folder_name" = "consensus" ]; then
    echo "    📦 Mapping consensus directory directly..."
    cp -a "$folder"/* "$NODE_DATA/data/data/consensus/" 2>/dev/null || true
```

### Giải pháp 2: Gọi `IntermediateRoot(true)` cho Stake DB tại Genesis Repopulate
Đảm bảo các cấu hình validator từ genesis được ghi nhận hoàn tất xuống đĩa bằng cách đồng nhất quy trình commit của Stake DB trong `repopulateGenesisState()`:

```go
// repopulateGenesisState
logger.Info("Committing stake state...")
if _, err := cs.IntermediateRoot(true); err != nil {
    logger.Error("Failed to calculate intermediate root for stake state: %v", err)
    return err
}
_, commitErr := cs.Commit()
```

### Giải pháp 3: Tách Biệt Kiểm Tra Empty Trạng Thái Khi Khởi Động
Nếu database khối đã tiến triển lên cao (`block_number > 0`) và dữ liệu tài khoản vẫn đầy đủ (`isEmptyAccountNomt == false`), việc thiếu hụt `stake_db` không được phép kéo lùi toàn bộ block tip về genesis block #0. 
Chúng ta chỉ reset block tip về 0 khi thực sự mất dữ liệu tài khoản (`isEmptyAccountNomt == true`), giúp hệ thống khởi động an toàn và kích hoạt cơ chế `STARTUP-SYNC` tự động hòa mạng để sửa lỗi `stake_db`.

---

## 4. Kiểm Chứng Trạng Thái (Verification Steps)

1. **Khôi phục snapshot và khởi động**: Khi chạy `./restore_node.sh`, thư mục `consensus/nomt_db` sẽ được đặt chuẩn xác tại `$NODE_DATA/data/data/consensus/nomt_db/`.
2. **Khớp StateRoot**: Pha khởi động sẽ nạp trực tiếp block #1600 từ LevelDB và đọc chính xác account root `0x794d2ece...` từ NOMT trên đĩa, khớp hoàn toàn với `metadata.json` mà không kích hoạt chuỗi reset về genesis.
3. **Persist Validator**: Nếu xảy ra trường hợp reset sạch sẽ, `repopulateGenesisState()` sẽ commit validator và ghi nhận root `0x7f2bffd5f0464f5a...` trực tiếp xuống CSDL `stake_db` trên đĩa.
