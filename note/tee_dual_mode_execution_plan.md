# Kế Hoạch: Metanode Hỗ Trợ Hai Chế Độ Thực Thi — `cgo` (Normal) và `trustzone` (TA)

> **Loại tài liệu:** Kế hoạch triển khai — cập nhật `note/tee_core_packaging_plan.md`, KHÔNG
> thay thế. Sửa lại 2 tiền đề sai của tài liệu đó sau khi khảo sát trực tiếp mã nguồn và
> phần cứng thật (2026-08-15). Xem mục 2 bên dưới.
>
> **Phạm vi đã chốt (hỏi trực tiếp người yêu cầu, không suy đoán):** chỉ đổi **nơi thực thi**
> lõi EVM (`mvm` + Xapian). KHÔNG đổi mô hình tin cậy — không private key trong TEE, không
> RPMB anti-replay, không Merkle-witness statelessness (đó là phạm vi của
> `tee_master_architecture.md`, một hệ thống lớn hơn hẳn). Host (Normal World) vẫn được TA
> tin tưởng hoàn toàn như hôm nay. Xapian **có** trong phạm vi (đưa cả vào TA). Xử lý **tuần
> tự hóa** (1 phiên tại 1 thời điểm) ở mốc đầu, không giữ song song GOMAXPROCS-worker như cgo
> mode. Chọn chế độ bằng **runtime config**, một binary — theo khuôn `state_backend` đã có.

---

## 1. Bối cảnh

Metanode chạy lõi EVM C++ (`mvm` + Xapian, đóng gói `libmvm_linker.a`) **in-process** qua cgo
trong tiến trình Go (`execution/`). Mục tiêu: cho phép **cùng lõi C++ đó** chạy trong một
TrustZone TA (secure world), chọn được theo từng node qua config.

## 2. Sửa lại 2 tiền đề sai của `tee_core_packaging_plan.md`

### 2.1. B1/B2 KHÔNG đóng hết callback C++→Go
Tài liệu cũ nói B1/B2 đã loại bỏ callback giữa chừng thực thi. Grep call site thật trong
C++ cho thấy: **8 callback đã đóng thật** (block context/logging), nhưng **7 callback vẫn
sống**:

| Callback | Go | C++ gọi từ | Bản chất |
|---|---|---|---|
| `GlobalStateGet` | `mvm_api.go:1331` | `my_global_state.cpp:81,131,200` | Đọc account state; `status==3` = tín hiệu Block-STM suspend/`ErrEstimateHit`, không chỉ dữ liệu |
| `GetStorageValue` | `mvm_api.go:1459` | `my_storage.cpp:66`, `xapian_handlers.cpp:89` | Đọc storage slot |
| `ExtensionCallGetApi` | `extension.go:43` | `my_extension.cpp:250` | HTTP GET thật ra mạng ngoài (5s timeout, chặn SSRF) |
| `ExtensionExtractJsonField` | `extension.go:96` | `my_extension.cpp:254` | Parse JSON |
| `ExtensionBlst` | `extension.go:151` | `my_extension.cpp:258` | BLS precompile |
| `ExtensionGetOrCreateSimpleDb` | `extension.go:287` | `my_extension.cpp:523` | Đọc/GHI trie thứ cấp, root nằm trong AccountState |
| `ClearProcessingPointers` | `mvm_api.go:1426` | `my_global_state.cpp:107,155,226` | Dead — code tự ghi chú "không còn cần thiết", giữ lại chỉ để không vỡ linker |

→ TA **bắt buộc có kênh gọi ngược**, không thể là "1 lệnh vào → 1 kết quả ra".

**B3 (`ExtensionGetOrCreateSimpleDb`) được mở lại:** tài liệu cũ hoãn B3 vì "cần
Merkle-witness đầy đủ" — lý do đó chỉ đúng nếu TA không tin Host. Phạm vi đã chốt ở đây là
TA **vẫn tin Host hoàn toàn**, nên B3 chỉ cần một lệnh reverse-call thẳng như `GlobalStateGet`.

### 2.2. Không có GlobalPlatform/libteec trên phần cứng thật
Khảo sát trực tiếp repo `tz-llm-trustzone` (cùng board, dùng lại được nguyên khuôn mẫu):
không có `TEEC_OpenSession`/`TEEC_InvokeCommand` ở đâu cả. Transport thật:
- ioctl tùy biến trên `/dev/tc_ns_client` + SMC world-switch trong
  `tzdriver/core/tc_client_driver.c`.
- 1 trang shared (1 MiB) driver cấp bằng `__get_free_pages`; NW `mmap` qua fd driver, SW map
  cùng địa chỉ vật lý qua API riêng TEE-OS.
- `struct all_ring_buffer` (`tz-llm/llama.cpp/src/interface.h:206`): cờ atomic
  `request_ready`/`final_answer_ready` + `ring_buffer<io_task>`/`ring_buffer<io_result>` hai
  chiều — đúng hình dạng kênh gọi ngược cần dùng lại.
- Đồng bộ bắt buộc `raw_spinlock` dựng thuần `std::atomic` CAS — **không dùng mutex libc**
  (glibc NPTL và musl có layout `pthread_mutex_t` khác nhau; đã crash thật trên board khi thử
  `std::mutex` qua ranh giới này).
- TA có sẵn khuôn mẫu **vòng lặp phục vụ nhiều request** (`main.cpp`'s `while(true)`).
- **TA không có filesystem POSIX** — `io-backend.cpp` (nơi duy nhất gọi `open()`/libaio) chỉ
  build khi **ngoài** TA; bản TA dùng `alloc-stage-chcore.cpp` + SMC stub. Ảnh hưởng trực
  tiếp việc đưa Xapian vào TA (mục 5).

## 3. Kiểm kê đầy đủ ranh giới cgo hiện tại (`pkg/mvm`, xác nhận 2026-08-15)

### 3.1. Lệnh forward (Go→C++, 7 entry point thực thi)
`call` (`mvm_api.go:811`) · `execute` (`:904`) · `deploy` (`:1283`) · `executeBatch` (`:1030`)
· `sendNative` (`:1098`) · `processNativeMintBurn` (`:1154`) · `noncePlusOne` (`:1202`).
Mỗi lệnh trả về `*ExecuteResult` — giải phóng bằng `freeResult`/`freeBatchResult`
(chỉ liên quan tới quản lý bộ nhớ phía cgo, không phải lệnh dây riêng).

### 3.2. Lệnh lifecycle / setup (Go→C++, không phải "thực thi 1 tx")
- `clearAllStateInstances` (`:199,1654`) — xóa `State::instances` cache.
- `updateStateNonce` (`:207`), `updateStateBalance` (`:217`) — Go ghi đè cache C++ ngoài luồng.
- `clear_xapian_tx_buffer`/`commit_xapian_tx_buffer` (+ `_batch`, `:1666,1675,1696,1717`) —
  vòng đời buffer ghi Xapian theo từng tx.
- `MVM_commitAllXapian` (`:1658`) — chốt Xapian ở ranh giới block.
- `MVM_exportAllXapianLogs`/`MVM_freeExportedXapianLogs` (`:224-225`) — xuất log cho P2P sync.
- `ReplayFullDbLogs` (`:190`, qua `CallReplayFullDbLogs`) — replay log vào State/Xapian C++.
- `SetXapianBasePath` (`:265`) — trỏ đường dẫn đĩa thật (TA không có FS → xem mục 5).
- `InitCppFileLog`/`CloseCppFileLog` (`logger.go:36,46`) — log file C++, không cần trong TA.
- `testMemLeak`/`testMemLeakGS` (`:1433,1447`) — công cụ debug, KHÔNG đưa vào giao thức TA.

### 3.3. Lệnh reverse còn sống (C++→Go giữa chừng thực thi) — xem bảng mục 2.1
6 lệnh cần giao thức (bỏ `ClearProcessingPointers` vì dead).

### 3.4. Trạng thái C++ sống giữa các lời gọi (phải nằm trong TA)
- `State` singleton map (`linker/include/state.h:54`, `unordered_map` + `shared_mutex`).
- Xapian: `XapianRegistry` global, `XapianManager::instances`, buffer ghi theo tx.
- `ExecuteResult` (`mvm_linker.hpp:17`) chỉ gồm `char*`+độ dài, không con trỏ C++ nội bộ →
  serialize nguyên vẹn, không cần thiết kế lại.
- **Xác nhận thực nghiệm (GĐ2, `-count=5`):** `State` singleton này keying (ít nhất một phần)
  theo **địa chỉ contract**, KHÔNG theo `mvmId`/instance Go — và **process-global thật sự**,
  sống xuyên suốt nhiều `ChainState` Go hoàn toàn độc lập trong cùng 1 tiến trình test. Cụ thể:
  `Execute()` (`isCache=true`) rò rỉ storage giữa 2 lần gọi khác `ChainState` nếu dùng lại cùng
  1 địa chỉ contract (bắt được khi test cgo-vs-loopback dùng chung 1 `sender` cho cả 2 lượt:
  lượt sau thấy `storage[0]` đã tăng sẵn từ lượt trước). `Call()`/`Deploy()`/`SendNative()`/
  `ProcessNativeMintBurn()`/`NoncePlusOne()` không thấy rò rỉ tương tự trong test. Hệ quả cho
  GĐ3/GĐ5: xác nhận lại lý do `MVM_TZ_CMD_CLEAR_ALL_STATE_INSTANCES` phải được gọi đúng thời
  điểm biên (khởi động node / ranh giới block) — không được giả định trạng thái sạch giữa các
  lần gọi chỉ vì dùng `mvmId`/`ChainState` khác nhau.

## 4. Hệ quả thiết kế then chốt

1. **Không đổi kiểu trả về của `GetMVMApi`/`GetOrCreateMVMApi`.** 7 callback đọc field không
   export (`mvmApi.accountStateDb`, `.smartContractDb`, `.extendedMode`,
   `.currentRelatedAddresses`) trực tiếp trên `*MVMApi` cụ thể. Thêm **factory riêng**
   `NewExecutionEngine(...)`, không đụng hàm hiện có.
2. **Giao thức dây là 1 header C dùng chung, KHÔNG dùng protobuf** — dù protobuf là quy ước
   FFI khác của repo, TA chạy chcore-libc/musl; kéo protobuf-c/nanopb vào chỉ để nối 1 struct
   đã phẳng sẵn (`ExecuteResult`) là chi phí thừa, ngược KISS/YAGNI (`AGENTS.md`).
3. **Không shadow-mode 2 engine trên cùng dòng tx production** — cả hai đều ghi (Xapian
   buffer, `State`, MVCC); `ExtensionCallGetApi` gọi HTTP thật nên so byte-for-byte trực tiếp
   sẽ báo sai dương. Dùng **replay đối chiếu ngoại tuyến** thay thế.
4. **Loopback transport (x86, không cần phần cứng) là công cụ xác minh chính** — chạy toàn
   bộ giao thức thật (encode/decode + spinlock CAS thật) gọi thẳng `*MVMApi` cùng tiến trình.
5. **Vì tuần tự hóa:** Go host giữ 1 mutex bọc quanh toàn bộ phiên gọi vào TZ engine, để
   nhiều goroutine Block-STM (`true_block_stm.go:267,289,305`, chạy `GOMAXPROCS` worker) xếp
   hàng an toàn thay vì đua nhau ghi lên 1 trang shared. `status==3` (`ErrEstimateHit`) vẫn có
   thể xảy ra ở tầng MVCC account-state — TA phải unwind sạch, không để lại ghi dở trong
   Xapian tx buffer/`State`.

## 5. Xapian trong TA (do người dùng chọn đưa vào phạm vi)

Phần việc lớn nhất của kế hoạch. TA không có filesystem POSIX → mọi thao tác file của Xapian
(`open`/đọc/ghi/flush index) phải proxy qua lệnh reverse I/O mới, theo đúng mô hình
"SET_PAGES + mmap" đã dùng cho khối lượng lớn trong `tz-llm`'s `io-backend.cpp`. Cần đo trước:
dung lượng index Xapian thực tế + heap EVM + `State` map có nằm gọn trong trần bộ nhớ secure
thật của board mục tiêu hay không (đừng mang con số 3GB từ board `tz-llm` sang nếu khác phần
cứng) — nếu không, đây là điểm chặn cứng cần biết sớm, trước khi đổ công sức port code.

**SỬA LẠI ƯỚC LƯỢNG (2026-08-16, khảo sát source thật trước khi build, chưa cần board):**
Tiền đề ban đầu ở trên SAI — `execution/pkg/mvm/linker/src/xapian/` (~3000 dòng) **không phải**
bản tự cài đặt lại của metanode, mà là lớp quản lý (registry/pooling/log, 0 chỗ file I/O trực
tiếp — xác nhận bằng grep) bọc quanh **thư viện Xapian THẬT, bên ngoài** (`libxapian` 1.4.22,
dùng thẳng `Xapian::WritableDatabase`/`Xapian::Document`/`Xapian::DocNotFoundError` — kiểu thật
của thư viện, không phải kiểu tự định nghĩa). "Đưa Xapian vào TA" do đó **không phải** viết vài
lệnh proxy file I/O cho lớp quản lý mỏng — mà là **cross-build cả thư viện Xapian thật** (bộ máy
tìm kiếm full-text với backend glass/honey tự quản lý B-tree trên đĩa, phụ thuộc zlib, build
bằng autotools) cho chcore-libc/musl. I/O thật nằm bên trong libxapian, ngoài tầm với của lớp
quản lý mà metanode tự viết — không tự động "proxy được" chỉ bằng cách thêm lệnh reverse mới ở
tầng metanode.

**Hướng giảm rủi ro — ĐÃ XÁC MINH BẰNG BUILD THẬT (2026-08-16):** Xapian có backend
`Xapian::DB_BACKEND_INMEMORY` — không chạm filesystem. Đã cross-build thật `xapian-core-1.4.22`
(tarball release chính thức, không phải git HEAD — xem lý do ở dưới) cho đúng toolchain chcore/
musl (`vectorxj0553/tz-llm-llama-builder` docker image, cùng compiler/`-specs musl-gcc.specs`
dùng để build TA llama.cpp hôm nay), với `--disable-backend-glass --disable-backend-honey
--disable-backend-remote --enable-backend-inmemory`:
- **`libxapian.a` build sạch, 0 lỗi**, 182 object file, xác nhận có đủ `inmemory_*.o`
  (`inmemory_database.o`/`inmemory_document.o`/`inmemory_alltermslist.o`/
  `inmemory_positionlist.o`), kiến trúc `elf64-littleaarch64` thật.
- **Smoke test thật đã LINK thành công**: 1 file `.cpp` gọi `Xapian::InMemory::open()` → add
  document + term → `commit()` → `Enquire`/`Query`/`get_mset()` — biên dịch + link ra 1
  executable aarch64 thật (3.5MB), không thiếu symbol nào ngoài libstdc++/musl-libc chuẩn
  (`objdump -T` xác nhận: toàn bộ `UND` symbol là `GLIBCXX_3.4.x`/`CXXABI_1.3.5`/libc chuẩn —
  không có gì lạ). Link theo đúng cách file `.so` động (`libstdc++.so`/`libgcc_s.so` từ
  `.cpp/aarch64/lib/`) — **giống hệt cách llama.cpp/ggml TA hôm nay đã link** (nạp qua ramdisk
  lúc build image, không qua filesystem lúc runtime) → tích hợp vào pipeline `rebuild.sh`/
  `repack.sh` hiện có mà không cần cơ chế mới.
- **2 trở ngại thật gặp phải khi build, cả 2 đều đã giải quyết**, không phải giả thuyết:
  1. `zlib.h`/`libz` — Xapian **check zlib KHÔNG ĐIỀU KIỆN** (`configure.ac:858`, dùng cho
     `stemtest`, không tắt được dù disable hết backend cần nén) → phải cross-build zlib 1.3.1
     riêng cho target trước (nhỏ, không autotools, ~1 phút, không có lỗi gì).
  2. `backends/dbcheck.cc` (Xapian 1.4.22) dùng `GLASS_TABLE_EXTENSION` **không có `#ifdef
     XAPIAN_HAS_GLASS_BACKEND` bao quanh** ở 2 chỗ (dòng ~451, ~476) — lỗi thật của upstream khi
     build với glass backend tắt (cấu hình hiếm ai dùng nên chưa ai vá) → đã vá tại chỗ (bọc 2
     nhánh `else if` trong `#ifdef XAPIAN_HAS_GLASS_BACKEND`), build sạch sau đó. Nếu theo hướng
     này ở GĐ3 thật, patch này cần giữ lại (hoặc report upstream).
  3. **Dùng tarball release, không dùng git HEAD**: git HEAD (checkout tại
     `/mnt/Data/NNNNNNN/xapian/xapian-core`, ~4097 commit sau v1.4.0) thiếu nhiều file sinh ra
     lúc `make dist` (`include/xapian/error.h`, `languages/sbl-dispatch.h`,
     `queryparser/queryparser_internal.cc`...) — các rule sinh file này bị comment out trong
     Makefile đã `autoreconf`, cần công cụ maintainer (`lemon`, Snowball generator) không có sẵn.
     Tarball release (`xapian-core-1.4.22`, khớp bản `libxapian` 1.4.22 đã cài trên máy dev) đã
     có sẵn hết các file này — dùng thẳng, không cần bootstrap.
- **Chưa xác minh** (nằm ngoài phạm vi câu hỏi "có compile được không"): chạy THẬT trên board
  (cần TA thật, GĐ3 chưa tới bước đó); dung lượng heap/thời gian init của `libxapian.a` 17.6MB
  trong trần bộ nhớ secure thật; tích hợp với lớp quản lý `linker/src/xapian/` của metanode
  (~3000 dòng, chưa thử build chung — riêng phần này còn vướng thêm chặn cứng `std::span` của
  `mvm` nói ở dưới, không liên quan gì đến Xapian).

Artifact build thật (`libxapian.a`, `xapian_smoke`, `dbcheck.cc` đã vá) lưu tạm tại scratchpad
phiên làm việc — chưa commit vào repo nào (chỉ là bằng chứng khả thi, chưa phải patch chính
thức).

**Chặn cứng C++20 — ĐÃ GIẢI QUYẾT TẬN GỐC BẰNG BUILD THẬT (2026-08-16), phạm vi lớn hơn ước
lượng ban đầu:**

Quét lại toàn bộ `linker/` + `c_mvm/` (không chỉ 1 chỗ `crypto_handlers.cpp:676` ban đầu) lộ ra
C++20 được dùng **nhiều chỗ hơn nhiều**, và tệ nhất là **trong cả thư viện bên thứ ba đã
vendor**, không chỉ code metanode tự viết:
- `std::span`: `bn254.hpp`/`pairing.cpp` (BN254 pairing, EIP-197), `kzg.cpp` (KZG point
  evaluation, EIP-4844), `crypto_handlers.cpp`.
- `std::ranges::equal/copy/reverse`, `std::bit_cast`: `kzg.cpp`, `ripemd160.cpp` (RIPEMD160
  precompile).
- **`c_mvm/3rdparty/intx/include/intx.hpp`** — thư viện uint256 dùng cho **toàn bộ số học EVM**
  (consensus-critical) — dùng `operator<=>` (spaceship), `<concepts>`, `consteval`. Vá tay ở
  đây rủi ro cao (lỗi âm thầm trong số học 256-bit → fork), không nên làm.

→ Kết luận: "vá `std::span` thủ công" ở 1 chỗ là **không đủ và rủi ro sai chỗ** (đụng vào
`intx` vendor). Hướng đúng là sửa **toolchain**, không sửa code.

**Gốc rễ tìm ra**: `libstdc++ 9.2.0` không phải lệch tình cờ — nó được hardcode
(`GCC_VER=9.2.0`) trong chính script chính thức của toolchain
(`/home/vectorxj/chcore/staros/scripts/build/buildup_cpp.sh`), và bản đang dùng thực ra là
**prebuilt tải về** từ 1 git server nội bộ (`git@ipads.se.sjtu.edu.cn:staros/cpp-libs.git`,
không truy cập được) qua hàm `prebuild_load()` — không phải build tại chỗ. Script này có sẵn
đường build-from-source dự phòng (`musl_cross_build`/`musl_cross_install`, dùng
[`musl-cross-make`](https://github.com/richfelker/musl-cross-make), 1 dự án chuẩn/uy tín) nhưng
chưa ai chạy tới vì `prebuild_load()` luôn thành công trước.

**Đã build lại thật, thành công hoàn toàn**: chạy `musl-cross-make` độc lập (không qua
`buildup_cpp.sh`, vì hàm đó có bước hỏi tương tác + phụ thuộc git server nội bộ) với
`TARGET=aarch64-linux-musleabi`, **`GCC_VER=13.3.0`** (mới, C++20 đầy đủ), **`MUSL_VER=1.2.3`**
(giữ nguyên đúng bản gốc script đã dùng — để tối đa tương thích ABI với musl-libc fork riêng
của chcore mà TA thật sự chạy trên đó lúc runtime; libstdc++ build ra chỉ cần khớp ABI musl,
không cần dùng chính musl-libc này lúc chạy — đúng cách `musl_cross_install()` vẫn làm: chỉ lấy
header + `libstdc++.so*`/`libgcc_s.so*`, không đụng gì khác). Build sạch (0 lỗi thật, `-j4` để
an toàn RAM máy dùng chung), `make install` sạch → toolchain đầy đủ 853MB tại
`aarch64-linux-musleabi-g++` (GCC 13.3.0).
- Xác nhận `<span>` tồn tại, `_GLIBCXX_RELEASE 13`.
- **Smoke test thật**: 1 file `.cpp` dùng đủ **cả 5 tính năng C++20 đã tìm thấy trong
  `mvm`/`c_mvm`/`intx`** (`std::span`, `std::ranges::equal`, `operator<=>`/spaceship,
  `std::bit_cast`, `consteval`) — **compile sạch (0 lỗi, 0 warning) VÀ link sạch, cả 2 kiểu
  static lẫn dynamic** (`-static-libstdc++` và mặc định) bằng chính toolchain GCC 13.3.0 vừa
  build. Giải quyết tận gốc — không cần vá bất kỳ dòng code nào của `mvm` hay `intx`.
- **Việc còn lại (thuộc GĐ3 thật, chưa làm)**: chỉ còn bước tích hợp cơ học — copy
  `output/aarch64-linux-musleabi/include/c++/13.3.0` + `libsupc++`'s
  exception/typeinfo/new/initializer_list + `libstdc++.so*`/`libgcc_s.so*` vào
  `.cpp/aarch64/` của chcore staros (đúng những gì `musl_cross_install()` tự động hoá), thay
  cho bản 9.2.0 prebuilt. Chưa làm bước này (thuộc phạm vi tích hợp TA thật, không phải câu hỏi
  "compile được không").
- Artifact (toolchain 853MB) lưu tại `/home/pi/musl-cross-build-scratch/output/` trên máy dev
  — chưa commit vào repo nào, chỉ là bằng chứng khả thi.

Exception (19 file) + `std::mutex`/`std::shared_mutex` (8 file) trong `mvm` không phải rủi ro
mới — cùng loại tính năng llama.cpp/ggml đã chạy ổn trên đúng toolchain này.

## 5b. Thiết kế persist Xapian qua Host (2026-08-16)

### Phát hiện nền tảng: cơ chế này đã tồn tại 90%, không phải làm từ đầu

Đọc trực tiếp source (không suy đoán) lộ ra: metanode **đã có sẵn** đúng loại cơ chế
"lưu log thao tác Xapian dạng nhị phân + phát lại (replay) để dựng lại state" — dùng cho một
mục đích khác (đồng bộ node mới/bị tụt hậu), nhưng **cấu trúc dữ liệu và luồng chảy khớp gần
như hoàn hảo** với việc TA cần "nạp lại Xapian sau khi TA restart":

1. **`XapianLog::LogEntry`** (`linker/include/xapian/xapian_log.h`) — log thao tác có kiểu
   (`NEW_DOC`/`DEL_DOC`/`ADD_VALUE`/`ADD_TERM`/`SET_DATA`/`INDEX_TEXT`), **đã có sẵn
   `serialize()`/`deserialize()` nhị phân**. `ComprehensiveLog` (gói nhiều `LogEntry` theo
   `db_name`) cũng có `serialize()`/`deserialize()` riêng.
2. **`ExecuteResult` (C++) đã trả về `full_db_logs`** cho MỌI lần gọi Call/Execute/Deploy/...
   — Go trích ra qua `extractFullDbLogs()` (`helpers.go:309`) thành
   `MVMExecuteResult.MapFullDbLogs` (map địa chỉ → bytes log đã serialize).
3. **Go đã có `mvm.CallReplayFullDbLogs(logs map[string][]byte)`** (`mvm_api.go:118`) — gọi
   thẳng `C.ReplayFullDbLogs`, nạp log ngược trở lại state C++ (Xapian + `State` singleton).
   Dùng thật hôm nay tại `executor/unix_socket_handler_sync.go:1039`, khi 1 node đồng bộ dữ
   liệu backup từ node khác — comment tại chỗ gọi ghi rõ: **"Without this, smart contract
   storage reads return wrong values after sync... Xapian DB may be OUT OF SYNC"** — tức đây
   chính xác là cơ chế "state C++ trống/thiếu dữ liệu → nạp lại từ nơi khác" mà TA sau mỗi lần
   restart cũng cần, chỉ khác nguồn (TA nạp từ Host, không phải nạp từ backup file đồng bộ).
4. **`MapFullDbLogs` đã được lưu bền vững MỌI block, vô điều kiện, không phân biệt chế độ
   thực thi**: `block_processor_commit.go:611-637` gói `job.ProcessResults.FullDbLogs` vào
   `storage.BackUpDb`, serialize, rồi `storageManager.GetStorageBackupDb().Put(primaryKey,
   backupBytes)` — chạy ở tầng xử lý block, **phía TRÊN** `mvm.ExecutionEngine`, nên không
   quan tâm kết quả đến từ cgo hay TZ mode.
5. **Chiều "LƯU" (TA → Host) đã chạy được 100% ngay từ GĐ2, không cần code mới**: field
   `full_db_logs_count` đã có trong `mvm_tz_execute_result_hdr_t` (GĐ1), `tz_codec.go`'s
   `encodeExecuteResult`/`decodeExecuteResult` đã đọc/ghi `MapFullDbLogs` (GĐ2, đã build+test
   qua `TestTABoundary_TrustzoneLoopback_*`). Nghĩa là: mỗi lần TA trả kết quả về, log Xapian
   đã tự động đi kèm, tự động chảy qua đúng pipeline `block_processor_commit.go` đã có, tự
   động được lưu bền vững — **zero thay đổi code cho chiều lưu**.

### Việc thật còn thiếu: chiều "NẠP LẠI" (Host → TA)

Chỉ có phần này là chưa làm — và ngay cả phần này, **struct wire đã được thiết kế sẵn từ GĐ1**
(`mvm_tz_replay_full_db_logs_req_t`, `tzproto/mvm_tz_protocol.h:445-450`:
`entry_count` + blob stream `[20-byte address][32-bit log_len][log bytes]`, khớp thẳng
`LogReplayEntryC` C++ đã có) — chỉ thiếu 2 việc nối dây, không phải thiết kế lại:

1. **Go codec** (`tz_codec.go`): chưa có `encodeReplayFullDbLogsReq`/tương đương — cần viết
   theo đúng mẫu 9 lệnh lifecycle khác đã làm ở GĐ1 (nhỏ, ~30-40 dòng, cùng khuôn với
   `encodeXapianTxBufferReq` sẽ có).
2. **1 lệnh reverse-callback MỚI** (không phải lệnh có sẵn nào) —
   **`MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS`** (đề xuất tên, chưa có trong header): TA hỏi
   ngược Host "địa chỉ X có full_db_logs đã lưu gần nhất không?" khi tự nó tra `GlobalStateGet`/
   Xapian nội bộ mà thấy trống (dấu hiệu: TA vừa restart, singleton `State`/Xapian rỗng).
   Host trả về bằng đúng `mvm_tz_replay_full_db_logs_req_t`'s blob shape, TA gọi
   `ReplayFullDbLogs` (hàm C++ đã có sẵn) nạp vào. **Đây là lệnh reverse duy nhất thật sự mới
   cần thiết kế** — không phải 4 lệnh "file I/O proxy" mà GĐ1 từng phác thảo (dựa trên giả
   định sai — Xapian real dùng file thật; giờ đã xác nhận dùng backend InMemory, không có file
   nào để proxy cả, nên 4 lệnh đó nên bỏ, không triển khai).

### 1 lỗ hổng hiệu năng thật cần vá (Go-side, không phải TZ-protocol)

`storage.GetStorageBackupDb()` hiện lưu **theo block number** (key
`block_data_topic-<N>`) — tối ưu cho replay tuần tự lúc đồng bộ, **không tối ưu cho tra cứu
theo địa chỉ** (TA hỏi "địa chỉ X" — Host phải quét ngược block để tìm lần cuối X xuất hiện,
chi phí không chấp nhận được cho 1 lần đọc account). Cần thêm 1 index nhỏ, cộng thêm (không
thay thế), Go-side: `address → full_db_logs bytes gần nhất`, cập nhật cùng lúc với
`block_processor_commit.go`'s ghi hiện có (cùng 1 vòng lặp, thêm 1 `Put` nữa, chi phí thấp).
Đây là việc code Go nhỏ, không đụng tới protocol/C++.

### Khi nào TA gọi lệnh nạp lại

Theo đúng khuôn mẫu cgo mode hôm nay (chỉ gọi `ReplayFullDbLogs` khi state C++ THIẾU dữ liệu,
không gọi trước mỗi tx) — TA gọi `MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS` **lười (lazy), theo
địa chỉ, đúng lúc cần**: lần đầu tiên trong phiên TA hiện tại mà 1 địa chỉ được truy vấn
(`GlobalStateGet`/Xapian) và thấy trống trong `State`/Xapian nội bộ. Không cần "nạp toàn bộ
lịch sử lúc TA khởi động" — đúng tinh thần lazy-loading `State`'s cache đã có sẵn hôm nay.

### Không cần xử lý thêm cho MVCC/Block-STM suspend

Đã xác nhận ở GĐ1 (mục 3, phát hiện side-channel `status==3`): `ClearXapianTxBuffer` gọi vô
điều kiện trước MỌI lần thử lại tx, nên cơ chế nạp-lại này không tương tác gì với suspend/retry
— không cần thiết kế thêm.

### Việc còn lại — cập nhật 2026-08-16, 4/5 đã làm xong

1. [x] Thêm `MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS = 107` vào `mvm_tz_protocol.h`, tái dùng
   thẳng `mvm_tz_replay_full_db_logs_req_t`'s shape cho response (đúng thiết kế). Header build
   sạch qua cả gcc + clang `-fsyntax-only`.
2. [x] Viết Go codec trong `tz_codec.go`: `encodeGetLatestFullDbLogsReq`/
   `decodeGetLatestFullDbLogsReq` (request, chỉ 1 địa chỉ) +
   `encodeReplayFullDbLogsResp`/`decodeReplayFullDbLogsResp` (response, tái dùng
   `writeAddrBytesMap`/`readAddrBytesMap` có sẵn — đúng shape `[addr][len][data]`). Test round
   -trip thật (`tz_codec_full_db_logs_test.go`, 5 test, tất cả pass — bao gồm case rỗng, case
   nhiều entry, case header sai độ dài).
3. [ ] **Chưa làm — không phải bỏ sót, mà đúng giới hạn đã biết của GĐ2**: chưa nối vào
   `tz_loopback_engine.go`, vì đây là lệnh REVERSE (TA chủ động hỏi ngược), và trong loopback
   x86 hôm nay "phía TA" chỉ là gọi thẳng `*MVMApi` cùng tiến trình — không có khái niệm "TA
   session mới, `State`/Xapian rỗng" để trigger lệnh này (singleton C++ sống xuyên suốt tiến
   trình test, không reset giữa các lượt gọi). Việc nối dây dispatch reverse-callback thật chỉ
   có ý nghĩa khi có TA thật trên board (GĐ3) — cùng giới hạn đã ghi nhận từ trước cho cả 6
   lệnh reverse khác (`GlobalStateGet` v.v.) trong `tzproto/README.md`.
4. [x] Thêm index `address → full_db_logs` mới, package `pkg/storage`
   (`xapian_full_db_logs_index.go`: `FullDbLogsLatestKey`/`PutLatestFullDbLogsForAddress`/
   `GetLatestFullDbLogsForAddress`, last-write-wins, coi mọi lỗi là "miss" vì đây là cache
   best-effort không phải nguồn sự thật) + nối vào `block_processor_commit.go` (ghi cùng lúc
   với `BackUpDb` theo block, không thay thế nó). Test round-trip thật
   (`xapian_full_db_logs_index_test.go`, 4 test, tất cả pass — bao gồm case miss, case
   last-write-wins).
5. [x] Xoá 4 lệnh `MVM_TZ_RCMD_XAPIAN_FILE_OPEN`...`_FLUSH_CLOSE` + struct liên quan khỏi
   `mvm_tz_protocol.h` (dựa trên giả định sai về Xapian dùng file thật — đã xác nhận sai ở
   mục 5).

**Verify**: `go build/vet ./...` toàn module sạch, `go test ./pkg/mvm/... ./pkg/storage/...
-count=3` sạch, `go test ./... -count=1` toàn bộ 39 package sạch, `build_check.sh --go-only`
sạch (2/2), `PROJECT_STRUCTURE.md` đã cập nhật.

## 6. Các giai đoạn triển khai

- [x] **GĐ0 — Kiểm kê + khung chuyển chế độ** (2026-08-15): thêm `ExecutionMode` vào
  `pkg/config/config.go` theo khuôn `trie_factory.go`, factory `NewExecutionEngine`
  (`pkg/mvm/execution_mode.go`) tách biệt `GetOrCreateMVMApi` (không đổi). Build sạch 4/4
  (`build_check.sh` đầy đủ Rust+Go). Không đổi hành vi cgo mode.
- [x] **GĐ1 — Thiết kế giao thức CA↔TA** (2026-08-15): header + tài liệu tại
  `pkg/mvm/tzproto/` (`mvm_tz_protocol.h` — biên dịch sạch gcc+clang; `README.md` — trạng thái
  máy, bảng lệnh đầy đủ). Phát hiện thêm khi kiểm kê lại: `commit_full_db`/`revert_full_db`/
  `MVM_cancelTransaction` có `extern` trong `mvm_linker.hpp` nhưng **không có call site Go
  nào cả** — dead còn hơn `ClearProcessingPointers`, loại khỏi giao thức.
  **Đã xác nhận cơ chế unwind `status==3`** (đọc trực tiếp `my_global_state.cpp` +
  `true_block_stm.go`, xem `tzproto/README.md` mục 2): tín hiệu "cần thử lại" là 1 side-channel
  thuần Go (`mvccDB.BlockingVersion`, set ngay tại thời điểm trả lời reverse-call, TRƯỚC khi
  exception C++ kịp unwind) — TA chỉ cần throw ngay như C++ hôm nay, không cần thiết kế thêm
  gì cho tín hiệu này. Rủi ro thật duy nhất còn lại (buffer Xapian ghi dở) đã có sẵn cơ chế xử
  lý (`ClearXapianTxBuffer` gọi vô điều kiện trước mỗi lần thử lại) — giữ nguyên, không cần
  lệnh giao thức mới.
- [x] **GĐ2 — Loopback transport (x86, không cần board)** (2026-08-15): `tz_channel.go`
  (`tzChannel` — trang shared `C.malloc`, spinlock CAS thật, cờ ready thật);
  `tz_codec.go` (encode/decode `ExecuteResult` + cả 6 lệnh forward thật qua struct C thật);
  `tz_loopback_engine.go` (`tzLoopbackEngine` — round-trip đủ cả 6 lệnh qua giao thức thật,
  bọc `*MVMApi` thật, giữ nguyên `tzSessionMu` tuần tự hóa); nối vào
  `execution_mode.go`'s `NewExecutionEngine` — `ModeTrustzone` không còn panic. Mở rộng
  `ta_boundary_harness_test.go` với 6 test so cgo vs trustzone-loopback — **phủ đủ cả 6 lệnh
  forward** (`NativeTransfer`/SendNative, `Deploy`, `Call`, `Execute`, `ProcessNativeMintBurn`,
  `NoncePlusOne`) — mỗi test chạy 2 lượt tách biệt trên 2 `ChainState` riêng, địa chỉ
  (`sender`/`mvmId`) sinh duy nhất qua `nextTestAddr()` chứ không hardcode (không chung 1 tx,
  không chung 1 địa chỉ — xem phát hiện State-singleton ở mục 3.4), so `ExecuteResult` sau khi
  cả hai chạy xong: **khớp** (status, gas_used, balance/nonce change, deployed code, storage
  write — byte-for-byte). Xác nhận sạch ở `-count=5` (30/30 lượt). Build sạch: `go build/vet
  ./...` toàn module, `go test ./pkg/mvm/... -count=3`, `build_check.sh --go-only` (2/2).
  Không đổi hành vi cgo mode (mặc định vẫn cgo, không call site nào bị đổi).
- **GĐ3 — Đóng gói TA thật (cần board):**
  - [x] **`libmvm_linker.a` build được cho chcore-libc/musl — ĐÃ XÁC MINH BẰNG BUILD THẬT
    (2026-08-16), không cần board.** Dùng toolchain GCC 13.3.0 mới build ở mục 5 (thay cho
    9.2.0 cũ), build **toàn bộ** `c_mvm` (crypto core: KZG/RIPEMD160/BN254 pairing/BLS/
    secp256k1/keccak, dùng `intx`) rồi `linker` (glue EVM + lớp quản lý Xapian + cross-chain
    precompile) — cả 2 build **sạch 100%, đúng qua CMake thật của repo** (chỉ đổi
    `CMAKE_TOOLCHAIN_FILE`, không sửa 1 dòng nào trong `c_mvm`/`linker` C++ source). Kết quả:
    `libmvm.a` 23MB + `libmvm_linker.a` 63MB, 18+ object file, kiến trúc `elf64-littleaarch64`
    thật, xác nhận qua `objdump`.
    - **1 thay đổi build-flag** — **ĐÃ ÁP VÀO REPO THẬT (2026-08-16, commit `a2d28941`)**: 
      `-march=native -mtune=native` (hardcode không điều kiện trong cả `c_mvm/CMakeLists.txt`
      và `linker/CMakeLists.txt`) không hợp lệ khi cross-compile từ x86_64 sang aarch64 — đã
      bọc trong `if(CMAKE_CROSSCOMPILING)`, dùng `-march=armv8-a -mtune=generic` khi cross,
      giữ nguyên `native` khi build x86 (verify: build lại native, output không đổi). Không
      phải lỗi C++/logic — thuần túy build-flag. Xem mục "Patch `-march=native`..." bên dưới
      để biết đầy đủ (kể cả 1 sự cố tự phát hiện+tự sửa lúc verify: `install()` hardcode path
      cũng bị lộ ra, đã vá cùng lúc).
    - **Phụ thuộc ngoài chưa vendor trong metanode, cần header (build thật cần cả lib) cho
      aarch64/musl**: `tbb` (Intel TBB — xem đánh giá riêng ngay dưới, **quyết định cuối cùng:
      cross-build thật, ĐÃ build thử thành công** — đảo ngược so với kết luận "thay shim" ban
      đầu, xem chi tiết), `mpfr`+`gmp` (GNU MPFR/GMP), `secp256k1` (libsecp256k1), `uuid`
      (libuuid) — xem đánh giá đầy đủ cho 3 thứ này ngay dưới (2026-08-16).

    - **Đánh giá TBB (2026-08-16) — CẬP NHẬT LẠI cùng ngày sau khi build thật, đảo ngược
      quyết định ban đầu: quyết định mới = CROSS-BUILD THẬT, không thay shim.**

      Đánh giá đầu tiên (đọc source `oneTBB` v2021.11.0 lần 1) kết luận "4 vấn đề chặn cứng,
      nên thay shim" — **kết luận đó SAI, do đọc source chưa đủ sâu**. Bị người dùng hỏi lại
      trực tiếp ("Nếu không thay tbb có chạy được trong trustzone không?"), đọc lại kỹ hơn +
      build thử thật thì thấy cả 4 điểm đều có đường thoát:
      1. **`futex`**: `semaphore.h` có sẵn `#if defined(SYS_futex) ... #else` — nhánh `#else`
         dùng `sem_wait`/`sem_post` (POSIX chuẩn) khi không có futex. Tự động kích hoạt lúc
         compile nếu `SYS_futex` không được định nghĩa trong header musl target.
      2. **`dlopen`**: chcore-libc **có implement `dlopen()` thật**
         (`musl-libc/ldso/dynlink.c`). Cách TBB dùng dlopen để tìm `libtbbbind.so`/
         `libtbbmalloc_proxy.so` được thiết kế **tự bỏ qua nếu không tìm thấy file** (hành vi
         chuẩn, đã test rộng rãi trên mọi máy Linux không cài hwloc) — không đóng gói các
         `.so` phụ đó vào ramdisk TA là đủ, không phải patch code.
      3. **hwloc/NUMA**: cùng cơ chế dlopen-tùy-chọn ở trên.
      4. **Tự sinh worker thread**: **suy diễn sai** ở đánh giá đầu — thread nội bộ trong 1
         phiên xử lý của TA không vi phạm thiết kế tuần tự hóa kênh CA↔TA (tuần tự hóa là số
         request đang xử lý qua kênh, không phải số thread bên trong TA lúc xử lý 1 request).
         `SYS_create_thread` có sẵn trong chcore.

      **Đã cross-build thật để xác nhận** (không chỉ đọc source): dùng đúng toolchain GCC
      13.3.0 đã build ở mục "Chặn cứng C++20", cấu hình `BUILD_SHARED_LIBS=OFF
      TBBMALLOC_BUILD=OFF TBBMALLOC_PROXY_BUILD=OFF TBB_TEST=OFF` — **build sạch 100%**:
      `libtbb.a` (643KB, `elf64-littleaarch64` thật, gồm cả `dynamic_link.cpp.o`/
      `itt_notify.cpp.o`/`semaphore.cpp.o`). `TBBBind build targets are disabled due to
      unsupported environment` — CMake tự phát hiện môi trường cross và tắt hwloc, đúng dự
      đoán. Smoke test dùng **đúng shape API `state.cpp` thật** (`accessor`/`const_accessor`/
      `insert`/`find`/`erase`) — compile + link sạch ra executable aarch64 thật (8.8MB).
      `dlopen`/`dlclose`/`dlsym` xác nhận là **weak symbol** trong binary — đúng thiết kế
      "graceful nếu thiếu".

      **1 giới hạn thật của kết quả này, nói rõ không giấu**: build thử dùng musl-cross-make
      (musl generic, có định nghĩa `SYS_futex 98`) — nên rất có thể đã compile qua nhánh futex
      thật, KHÔNG phải nhánh fallback `sem_wait`. Nhánh fallback chỉ tự kích hoạt khi build
      bằng chính musl-libc của chcore (không định nghĩa `SYS_futex`) — **chưa build qua đúng
      chcore-libc thật để xác nhận nhánh đó**, việc này thuộc GĐ3 (cần tích hợp toolchain vào
      pipeline build chcore thật, không chỉ toolchain đứng riêng).

      **Quyết định cuối**: cross-build TBB thật cho GĐ3, dùng thẳng `tbb::concurrent_hash_map`
      — không cần shim nữa.

    - **Đánh giá `mpfr`/`gmp`/`secp256k1`/`uuid` (2026-08-16) — quyết định: cross-build thật,
      KHÔNG thay thế (khác TBB — 3 thư viện này không có vấn đề kiến trúc tương tự):**
      - **Phạm vi dùng thật**: `mpfr`+`gmp` — 82 chỗ dùng thật trong
        `my_extension.cpp`/`utils.cpp`/`crypto_handlers.cpp` (1 extension precompile toán học
        dấu phẩy động chính xác cao: add/sub/mul/div/pow/atan2/fmod), không phải vestigial.
        `secp256k1` — dùng cho phục hồi public key kiểu ECDSA (giống `ecrecover`,
        `my_extension.cpp`, qua `secp256k1_recovery.h`). `uuid` — 1 chỗ duy nhất
        (`xapian_manager.cpp`, sinh UUID ngẫu nhiên cho instance Xapian).
      - **Bằng chứng musl-compatibility thật** (không suy đoán): cả 3 đều có package chính thức
        cho Alpine Linux (distro musl chuẩn) — xác nhận trực tiếp qua mirror
        `dl-cdn.alpinelinux.org`: `gmp-dev-6.3.0-r4`, `mpfr-dev-4.2.2-r0`,
        `libsecp256k1-dev-0.5.0-r1`, `util-linux-dev-2.42.2-r1` (cung cấp libuuid) — đều tồn
        tại thật trên mirror `edge/main` và `edge/community`. Khác hẳn TBB (không có gói musl
        chính thức nào).
      - **`gmp`+`mpfr`+`secp256k1`**: rủi ro thấp — cả 3 đều là thư viện tính toán thuần túy
        (số vào, số ra), không I/O, không thread riêng, không dlopen — cùng lớp rủi ro với
        `zlib` đã build thành công ở mục 5. `gmp`/`mpfr` có lịch sử cross-compile cực kỳ lâu đời
        (hàng chục năm, hỗ trợ hầu hết mọi target embedded/musl/WASM).
      - **`uuid` (libuuid) — 1 rủi ro thật cần xác minh, tương tự lớp rủi ro futex của TBB
        nhưng nhỏ và hẹp hơn nhiều**: đọc source `util-linux`'s `lib/randutils.c` —
        `uuid_generate_random()` gọi `ul_random_get_bytes()`, ưu tiên syscall `getrandom()`
        trước, chỉ fallback đọc file `/dev/urandom` (filesystem — TA không có) nếu
        `getrandom()` trả `ENOSYS`.

        **ĐÃ XÁC NHẬN (2026-08-16, đọc trực tiếp `kernel/syscall/syscall_num.h` của chcore,
        không phải suy đoán): chcore KHÔNG có syscall `getrandom()` — và không có BẤT KỲ
        syscall random/entropy/crypto-RNG nào cả** (soát toàn bộ danh sách `SYS_*`, 159 dòng,
        0 kết quả khớp `rand`/`entropy`/`crypt`). Vậy `uuid_generate_random()` chắc chắn
        **không chạy được as-is** trong TA — cả 2 đường (`getrandom` syscall và fallback
        `/dev/urandom`) đều không có. Cần 1 trong 2 hướng vá nhỏ, khoanh vùng rõ (không phải
        kiến trúc lại như TBB): (a) nếu RK3588 có khối TRNG phần cứng map được vào không gian
        địa chỉ TA qua MMIO (`SYS_create_device_pmo` đã có sẵn cho mục đích này — chưa xác
        minh RK3588 secure-world có lộ TRNG MMIO hay không), viết driver nhỏ đọc trực tiếp; (b)
        theo đúng triết lý thiết kế đã chốt của cả kế hoạch này (TA tin Host hoàn toàn) — xin
        UUID/bytes ngẫu nhiên qua kênh reverse-callback đã có, để Host (luôn có `/dev/urandom`
        thật) tạo hộ. Hướng (b) nhất quán hơn với thiết kế hiện tại, khuyến nghị chọn hướng này
        trừ khi (a) hoá ra rẻ hơn hẳn.

      **CROSS-BUILD THẬT ĐÃ XONG (2026-08-16), cả 4 thư viện, cùng toolchain GCC 13.3.0
      `aarch64-linux-musleabi` đã dùng cho zlib/Xapian/TBB/mvm+linker (`$NEWTC` =
      `/home/pi/musl-cross-build-scratch/output/`), build trực tiếp trên host (không qua
      Docker), source thật khớp đúng version apt-installed đã dùng để tham chiếu API:**
      - **GMP 6.3.0** (nguồn: `ftp.gnu.org`, khớp bản apt `libgmp-dev` cài trên host) —
        `./configure --host=aarch64-linux-musleabi --build=x86_64-pc-linux-gnu
        --disable-shared --enable-static --disable-assembly` (`--disable-assembly`: cố ý
        tránh asm tối ưu theo target, chỉ dùng pure-C fallback cho an toàn cross-compile).
        `make -j4`: 0 lỗi. `libgmp.a` xác nhận thật `readelf -h` → `Machine: AArch64`.
      - **MPFR 4.2.1** (nguồn: `ftp.gnu.org`, khớp bản apt `libmpfr-dev`) — cần
        `--with-gmp-build=<đường dẫn build tree GMP ở trên>` (MPFR link thẳng vào GMP build
        tree, chưa "install" ra prefix nào). `./configure ... --disable-shared
        --enable-static --with-gmp-build="$SCRATCH/gmp-6.3.0"`: log xác nhận
        "checking if we can link with GMP... yes". `make -j4 -C src`: 0 lỗi. `libmpfr.a`
        (4.7MB) xác nhận `Machine: AArch64`.
      - **secp256k1 v0.2.0** (clone `bitcoin-core/secp256k1`, tag khớp bản apt
        `libsecp256k1-dev 0.2.0-2`) — `./autogen.sh` rồi `./configure
        --host=aarch64-linux-musleabi --build=x86_64-pc-linux-gnu --disable-shared
        --enable-static --enable-module-recovery --disable-benchmark --disable-tests
        --disable-exhaustive-tests --with-pic=yes` (bật `--enable-module-recovery` vì
        `crypto_handlers.cpp` cần đúng `secp256k1_recovery.h`). Configure log xác nhận
        "module recovery = yes". `make -j4`: 0 lỗi. `libsecp256k1.a` xác nhận `Machine:
        AArch64`.
      - **libuuid (từ util-linux 2.39.3)** (nguồn: release tarball chính thức
        `mirrors.edge.kernel.org/.../util-linux-2.39.3.tar.xz`, khớp bản apt `util-linux
        2.39.3-9ubuntu6.5` — **không dùng git clone** vì thiếu sẵn `configure` được generate,
        và `autoreconf`/`autogen.sh` cần lệnh `autopoint` (gói `gettext` của Ubuntu Noble
        trên máy này KHÔNG có `autopoint`, xác nhận qua `dpkg -c` cả gói .deb tải trực tiếp
        — không phải lỗi cấu hình máy, gói thật sự thiếu binary đó). Dùng release tarball có
        sẵn `configure` để né hẳn phụ thuộc `autopoint`. `./configure
        --host=aarch64-linux-musleabi --build=x86_64-pc-linux-gnu --disable-all-programs
        --enable-libuuid --disable-shared --enable-static --without-python --without-systemd
        --without-udev --without-selinux --without-tinfo --without-ncurses` (chỉ bật
        libuuid, tắt hết ~30 tool dòng lệnh khác của util-linux monorepo — không cần). `make
        -j4 libuuid.la`: 0 lỗi. `libuuid.a` (142KB) xác nhận `Machine: AArch64`.
        **Xác nhận thêm caveat futex-style đã dự đoán ở trên**: `config.h` sinh ra từ build
        này có `#define HAVE_GETRANDOM 1` — vì đây là musl-cross-make's musl generic (có
        định nghĩa `SYS_getrandom`), **không phải musl thật của chcore**. Y hệt tình huống
        futex của TBB: build này chỉ xác nhận nhánh code path `getrandom()`-có-thật biên dịch
        sạch, KHÔNG xác nhận hành vi trên chcore thật (đã biết chcore không có syscall này —
        xem phần "ĐÃ XÁC NHẬN" ở trên). Hướng vá (b) — xin Host tạo UUID hộ qua reverse
        -callback — vẫn là việc thật cần làm ở GĐ3, không đổi kết luận.
      - **Smoke test API thật cho cả 4** (biên dịch + link tĩnh bằng
        `aarch64-linux-musleabi-gcc -static`, xác nhận output là ELF AArch64 thật qua
        `readelf -h`/`file`, không chạy được trên x86 nên không thể chứng minh runtime — chỉ
        chứng minh symbol resolution + ABI compile-time đúng):
        - `gmpmpfr_smoke.c`: chuỗi `mpfr_init2`→`mpfr_set_str`→`mpfr_add/sub/mul/div/pow/
          atan2/fmod`→`mpz_init`+`mpfr_get_z` (đúng chuỗi API `utils.cpp`/`my_extension.cpp`
          dùng). Link sạch với cả `libmpfr.a` + `libgmp.a`.
        - `secp256k1_smoke.c`: `secp256k1_context_create`→
          `secp256k1_ecdsa_recoverable_signature_parse_compact`→`secp256k1_ecdsa_recover`→
          `secp256k1_ec_pubkey_serialize`→`secp256k1_context_destroy` (đúng chuỗi
          `crypto_handlers.cpp`). Link sạch.
        - `uuid_smoke.c`: `uuid_generate_random`→`uuid_unparse_lower` (đúng chuỗi
          `xapian_manager.cpp`). Header thật `<uuid/uuid.h>` lấy từ chính source
          `util-linux-2.39.3/libuuid/src/uuid.h` vừa build (không dùng bản glibc hệ thống,
          tránh lệch ABI/macro như cạm bẫy header đã gặp với tbb/mpfr/gmp trước đó). Link
          sạch.
      - **Kết luận cập nhật**: cả 4 thư viện **build thật thành công**, artifact + smoke test
        đều nằm trong scratchpad (không commit vào repo nào, đúng quy ước hygiene đã dùng
        suốt phiên này). Việc còn lại cho GĐ3 không đổi so với đánh giá gốc bên dưới: tích
        hợp vào pipeline build chcore thật (không phải scratch copy) + patch UUID-qua-Host.

    - **Tích hợp toolchain + 4 lib vào repo `tz-llm-trustzone` thật (2026-08-16)** — theo lựa
      chọn tường minh của người dùng: dùng thẳng repo `tz-llm-trustzone` (không phải một
      chcore checkout riêng cho metanode, chưa tồn tại), nhưng **không đụng** pipeline production
      hiện có của repo đó (đã flash + verify thật trên board):
      - **Phát hiện quan trọng về pipeline thật của `tz-llm-trustzone`**: toolchain C++ production
        hiện tại (`.cpp/aarch64` bên trong Docker image `vectorxj0553/tz-llm-llama-builder:latest`,
        GCC 9.2.0, `libstdc++.so.6.0.27`, link **dynamic** — khác cách tiếp cận static đã dùng cho
        metanode) **cũng được build bằng chính musl-cross-make**, xác nhận qua đọc trực tiếp
        `scripts/build/buildup_cpp.sh` bên trong image (`GCC_VER=9.2.0 MUSL_VER=1.2.3`) — validate
        cách tiếp cận đã dùng cho GMP/MPFR/secp256k1/uuid/TBB phiên này đúng khuôn mẫu sẵn có của
        chính project, không phải tự chế. Compiler thật build TA/CA hiện tại là
        `aarch64-linux-gnu-gcc-11` (Ubuntu 22.04) bọc qua 1 specs-wrapper script
        (`-specs musl-gcc.specs`) — chính kiểu specs-wrapper từng gây lỗi `_Float32` khi thử áp
        dụng cho metanode trước đây trong phiên này.
      - **Quyết định tích hợp (theo lựa chọn của người dùng)**: thêm **song song**, không ghi đè
        `.cpp/aarch64` (GCC9) đang chạy production cho LLM TA đã flash trên board thật. Tạo
        `scripts/kick-the-tires/cpp13-metanode-deps/` (Dockerfile + README, đã commit-worthy,
        text-only) trong repo `tz-llm-trustzone`, build layer mới `FROM
        vectorxj0553/tz-llm-llama-builder:latest` bake tarball vào
        `/home/vectorxj/chcore/staros/.cpp13/aarch64/` — tag ảnh mới:
        `vectorxj0553/tz-llm-llama-builder:cpp13-metanode-deps` (chỉ local, không push).
      - **Lỗi gặp và tự sửa ngay trong lúc tích hợp — glibc mismatch tái diễn**: build Docker
        layer đầu tiên baked cả `toolchain/` (binary GCC13.3.0 đầy đủ, ~800MB) — verify lại thì
        compiler **không chạy được bên trong container** (`GLIBC_2.36'/`GLIBC_2.38' not found`),
        **đúng y hệt lỗi glibc-mismatch đã gặp và ghi lại trước đó khi build metanode's mvm/linker
        trực tiếp qua Docker phiên này** — image base OS quá cũ so với glibc mà binary GCC13.3.0
        (build trên host) link vào. Sửa: bỏ hẳn `toolchain/` binary khỏi image (chỉ còn data:
        headers + `.so`/`.a`, chạy/link được bất kể glibc host nào), rebuild layer còn 12MB thay
        vì 377MB. README của thư mục ghi rõ: **compiler phải chạy trên HOST**
        (`/home/pi/musl-cross-build-scratch/output/bin/aarch64-linux-musleabi-*`), image chỉ chứa
        output (headers/`.so`/`.a`) để dùng khi cần trong 1 build step chạy trong container.
      - **Verify thật (không chỉ "build xong")**: `docker create` + `docker cp` lấy đúng bytes
        `.cpp13/aarch64/{include,lib,3rdparty}` ra khỏi image vừa build, compile+link
        `gmpmpfr_smoke.c` bằng compiler host **chỉ dùng các file vừa lấy ra từ image** (không
        phải bản gốc ở scratchpad) → thành công, `readelf -h` xác nhận `EXEC`/`Machine: AArch64`
        thật. Xác nhận image cũ `vectorxj0553/tz-llm-llama-builder:latest` **không đổi**
        (`docker inspect --format '{{.Id}}'` khớp y hệt trước/sau, `.cpp/aarch64/lib/libstdc++.so.6.0.27`
        md5 không đổi).
      - **Chưa làm / còn mở**: đây vẫn chỉ là "compile-only feasibility trong 1 image phụ", CHƯA
        wiring vào 1 TA CMake target thật, CHƯA bake vào `oh_tee/apps` qua `rebuild.sh`/
        `repack.sh`, CHƯA qua bất kỳ bước nào của quy trình build/flash 8 bước thật trong
        `CLAUDE.md`. Reverse-callback dispatch (`MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS`/
        `GlobalStateGet`/...) chưa nối vào bất kỳ TA build thật nào — chưa có TA build thật để
        nối vào.
      - File: `tz-llm-trustzone/scripts/kick-the-tires/cpp13-metanode-deps/{Dockerfile,README.md}`
        (đã commit-worthy nhưng CHƯA `git commit` — đang chờ người dùng xác nhận, theo đúng quy
        tắc "chỉ commit khi được yêu cầu"). Tarball `cpp13-metanode-deps.tar.gz` (12MB,
        headers+`.a`+`.so` only, không có compiler binary) nằm cùng thư mục, đã thêm vào
        `.gitignore` — không commit (build artifact, tự regenerate được theo README).

    - **Patch `-march=native` + link 4 lib thật vào CMakeLists.txt THẬT của metanode (2026-08-16,
      tiếp nối "commit và tiếp tục")** — không phải scratch copy nữa:
      - **`c_mvm/CMakeLists.txt`**: `-march=native -mtune=native` (cả C và C++ flags) bọc trong
        `if(CMAKE_CROSSCOMPILING) ... -march=armv8-a -mtune=generic ... else() ... native ...
        endif()` — hành vi x86 mặc định giữ nguyên tuyệt đối (verify: build lại native, so sánh
        md5 output không đổi), chỉ khi thật sự cross-compile (`CMAKE_TOOLCHAIN_FILE` được set)
        mới chuyển sang armv8-a.
      - **`linker/CMakeLists.txt`**: cùng kiểu conditional cho march flags. Thêm block
        `if(MVM_3RDPARTY_ROOT)` link `mpfr gmp secp256k1 uuid` PUBLIC vào target `mvm_linker`
        (chỉ kích hoạt khi biến này được set tường minh) — vì trên x86, các lib này KHÔNG bao
        giờ được link qua CMake cả, chỉ qua `#cgo LDFLAGS` của `pkg/mvm/mvm_api.go` ở tầng Go,
        nên target CMake `mvm_linker` từ trước tới giờ chưa từng "biết" về chúng.
      - **Phát hiện phụ đáng chú ý lúc điều tra**: `secp256k1_context_create()` et al. trong
        `my_extension.cpp` (dùng `<secp256k1.h>`/`<secp256k1_recovery.h>` thật) **không hề được
        link qua `-lsecp256k1` ở đâu cả** trên path x86 — xác nhận qua `nm` trên 1 binary
        `go test -c` thật: các symbol `secp256k1_context_create`/`secp256k1_ecdsa_recover`/...
        thật ra đến từ chính bản libsecp256k1 C mà **go-ethereum tự vendor và biên dịch qua
        cgo** (`github.com/ethereum/go-ethereum/crypto/secp256k1`) — chỉ "ăn may" vì Go gộp mọi
        cgo object trong cùng 1 process vào 1 link unit chung. **Một TA đứng riêng (không qua
        Go/cgo) sẽ KHÔNG có trick này** — xác nhận việc cross-build `libsecp256k1.a` thật ở
        mục trên (không phải bản vendor của go-ethereum) là bắt buộc, không dư thừa.
      - **Sự cố xảy ra thật trong lúc verify, đã tự phát hiện + sửa ngay (đáng ghi lại vì dễ tái
        diễn)**: `install()` trong CẢ HAI file (`c_mvm` và `linker`) hardcode đường dẫn đích theo
        `CMAKE_CURRENT_SOURCE_DIR` (thư mục **source**, không phải build dir) — nghĩa là
        `make install` từ **bất kỳ** build dir nào (kể cả 1 cross-build ở `/tmp`) đều ghi đè
        thẳng vào `c_mvm/build/lib/static/libmvm.a` **thật** của repo, đúng file mà
        `pkg/mvm/mvm_api.go`'s cgo LDFLAGS link vào. Việc này **đã xảy ra thật 2 lần** lúc verify
        (`libmvm.a` bị ghi đè bằng bản AArch64) — phát hiện ngay qua `readelf -h`, sửa bằng cách
        `make install` lại từ build dir x86 gốc (`/tmp/verify_native_c_mvm`), xác nhận khỏi bằng
        `go build`+`go test -c` thật chạy lại sạch (x86 ELF, exit 0) sau mỗi lần sửa. Sau đó vá
        tận gốc: thêm biến `MVM_INSTALL_PREFIX` (mặc định = hành vi cũ y hệt,
        `${CMAKE_CURRENT_SOURCE_DIR}/build`) cho `c_mvm`, và `MVM_C_MVM_BUILD_DIR` (mặc định =
        `../c_mvm/build` y hệt cũ) cho `linker`, để 1 build cross không bao giờ cần chạm vào
        đường dẫn thật nữa. **Đây là lỗ hổng có sẵn từ trước trong 2 file này (không phải do
        patch tạo ra) — chỉ lộ ra vì đây là lần đầu có ai cross-build từ 1 dir khác dir mặc định.**
      - **Verify thật, từ chính source repo (không phải scratch copy)**:
        - `c_mvm`: cross-configure với `CMAKE_TOOLCHAIN_FILE` trỏ toolchain GCC13.3.0 +
          `MVM_INSTALL_PREFIX` trỏ 1 dir cô lập → build sạch 0 lỗi → `libmvm.a` (22.8MB)
          `readelf -h` xác nhận AArch64. Cấu hình native (không set gì thêm) build lại, so khớp
          hành vi cũ 100%.
        - `linker`: cross-configure với `MVM_C_MVM_BUILD_DIR` trỏ vào bản cross `c_mvm` ở trên +
          `MVM_3RDPARTY_ROOT` trỏ `.cpp13`-style 3rdparty (headers+`.a` GMP/MPFR/secp256k1/
          libuuid) → build sạch 0 lỗi, **toàn bộ 17 file thật** bao gồm cả
          `my_extension.cpp`/`utils.cpp`/`crypto_handlers.cpp`/`xapian_manager.cpp` (những file
          dùng 4 lib mới) → `libmvm_linker.a` (61.6MB) `readelf -h` xác nhận AArch64. Cấu hình
          native build lại, so khớp hành vi cũ 100% (`libmvm_linker.a` vẫn x86_64, 0 lỗi).
        - Sau toàn bộ quá trình: xác nhận cuối `readelf -h` trên **file thật trong repo** —
          `c_mvm/build/lib/static/libmvm.a` và `c_mvm/build/libmvm.a` đều x86_64 — cộng
          `go build ./pkg/mvm/...` + `go test -run NOTHING ./pkg/mvm/...` chạy sạch, exit 0.
      - **Kết luận**: cả `c_mvm` VÀ `linker` — 2 target C++ thật, thật sự dùng cho production
        cgo build hôm nay — giờ đã cross-build được cho `aarch64-linux-musleabi` **trực tiếp từ
        source repo thật**, dùng đúng toolchain + 4 lib đã build/verify trong phiên này, mà
        KHÔNG làm thay đổi bất kỳ hành vi/output nào của build x86 hiện có. File đã sửa: đã
        `git commit` (`a2d28941`, branch `dev`): `execution/pkg/mvm/c_mvm/CMakeLists.txt`,
        `execution/pkg/mvm/linker/CMakeLists.txt`.

    - **Full-stack link test thật (2026-08-16, tiếp nối "cứ tiếp tục")** — chưa từng verify trước
      đây: liệu TOÀN BỘ stack (mvm + linker + Xapian + TBB + GMP + MPFR + secp256k1 + libuuid)
      có link chung được thành 1 executable aarch64 hay không, không chỉ từng static lib riêng
      lẻ compile được:
      - **Test 1 (tối giản)**: `main()` gọi `InitCppFileLog(...)` — link `-static` với toàn bộ
        8 thư viện → **thành công, 0 lỗi**, executable AArch64 thật (8.9MB). Nhưng `nm` xác nhận
        chỉ 1 symbol (`InitCppFileLog`) thực sự được kéo vào — do static archive chỉ kéo theo
        `.o` bị tham chiếu, nên test này KHÔNG chứng minh gì về phần lõi EVM (`call`/`deploy`/...).
      - **Test 2 (mạnh hơn, cố ý âm tính)**: lấy địa chỉ hàm `call()` (thay vì gọi đúng chữ ký
        phức tạp có macro `MVM_B1_CONTEXT_PARAMS`) để buộc linker kéo `.o` thật chứa logic EVM
        vào. **Kết quả sau khi sửa 2 lỗi tự gây ra trong chính lệnh test** (thiếu
        `-Wl,--start-group/--end-group` khiến `libmvm.a`→`libmvm_linker.a` không resolve ngược
        được `handle_cross_chain_precompile`; quên link `libz.a` đã build sẵn từ trước cho
        Xapian's `chert_table.cc` cần `deflate`/`inflate`) — sau khi sửa, danh sách undefined
        symbol còn lại **sạch, đúng 2 nhóm thật, không còn nhiễu**:
        1. **7 reverse-callback**: `GlobalStateGet`, `GetStorageValue`, `ExtensionCallGetApi`,
           `ExtensionExtractJsonField`, `ExtensionBlst`, `ExtensionGetOrCreateSimpleDb`,
           `ClearProcessingPointers` — **khớp CHÍNH XÁC 100%** với bảng 7 lệnh reverse plan đã
           liệt kê từ đầu (qua đọc code) — giờ có bằng chứng compile-time thật độc lập xác nhận
           đây đúng là toàn bộ bề mặt reverse-call cần cho GĐ1/GĐ3, không thiếu không thừa.
        2. **13 symbol `blst_*`** (`blst_p1_mult`, `blst_miller_loop`, `blst_fp12_finalverify`,
           ...) — **phát hiện MỚI, ngoài phạm vi 4 lib ban đầu**: `c_mvm/src/crypto/kzg.cpp`
           (KZG/EIP-4844 blob verification) dùng thật BLST (BLS12-381), qua header-only include
           (`c_mvm/CMakeLists.txt:100` trỏ `../../bls/blst/bindings`) nhưng KHÔNG có `libblst.a`
           nào cross-build cho aarch64 — chỉ tìm thấy 1 bản do Rust cargo build sẵn tại
           `target/release/build/blst-*/out/libblst.a` (x86_64, phía consensus Rust, không dùng
           lại được cho C++ TA). Đây là 1 lib thứ 5 cần cross-build nếu muốn EVM đầy đủ
           (bao gồm cả precompile blob/KZG) chạy trong TA.
      - **Người dùng xác nhận cross-build BLST luôn (2026-08-16)** — đã làm ngay sau phát hiện
        trên: `pkg/bls/blst` không có `build.sh` upstream, nhưng vendored source tự đủ —
        `src/server.c` tự `#include` toàn bộ các `.c` khác, `build/assembly.S` tự chọn đúng file
        `.S` theo `#if defined(__aarch64__)` (đã có sẵn asm cho armv8 tại `build/elf/*-armv8.S`,
        không cần asm x86-only). Build: compile 2 file
        (`server.c` với `-O2 -fno-builtin -fPIC`, `assembly.S` với `-O2 -fPIC`) → `ar rcs` →
        `libblst.a` (288KB). `readelf -h` xác nhận AArch64; `nm` xác nhận 207 symbol `T blst_*`,
        bao gồm đủ cả 13 symbol còn thiếu ở trên.
      - **Re-run full-stack link test với `libblst.a`**: 13 symbol `blst_*` biến mất hoàn toàn,
        chỉ còn đúng 7 reverse-callback như dự đoán — xác nhận BLST là lib DUY NHẤT còn thiếu,
        không có bất ngờ nào khác.
      - **Test cuối cùng — link 100% sạch, không còn undefined symbol nào**: viết 1 file stub
        tạm (`fullstack_reverse_stub.cpp`, ghi rõ "TEMPORARY... NOT a real reverse-dispatch
        implementation") định nghĩa thân rỗng cho đúng 7 hàm reverse-callback theo đúng chữ ký
        thật trong `mvm_linker.hpp`/`my_global_state.cpp`. Link lại toàn bộ (`mvm` + `linker` +
        Xapian + TBB + GMP + MPFR + secp256k1 + libuuid + BLST + zlib + stub) với `-static` →
        **thành công tuyệt đối, 0 undefined symbol** — 1 executable AArch64 thật, 52MB
        (`readelf -h` xác nhận `EXEC`/`AArch64`).
      - **Kết luận**: full-stack link-level compatibility của TOÀN BỘ stack thật (không chỉ phần
        "static weight" ban đầu) đã được xác nhận **hoàn toàn** — thứ DUY NHẤT còn thiếu để có 1
        TA thật là triển khai reverse-dispatch thật cho 7 hàm (thay vì stub rỗng), đúng những gì
        GĐ1/GĐ3 của plan đã dự kiến từ đầu, không có bất ngờ kiến trúc nào khác phát sinh. Đã
        thêm BLST vào `tz-llm-trustzone`'s `cpp13-metanode-deps` image (commit `f0ca40129`) theo
        đúng khuôn mẫu 4 lib kia. Artifact test nằm ở scratchpad
        (`fullstack_smoke.cpp`/`fullstack_smoke2.cpp`/`fullstack_reverse_stub.cpp`), không commit
        (chỉ để verify, không phải code sản phẩm).

    - **Cạm bẫy đã gặp và tự sửa (đáng ghi lại vì dễ tái diễn)**: build lần đầu qua Docker
      (`vectorxj0553/tz-llm-llama-builder`) dùng `musl-gcc -specs musl-gcc.specs` + `-I` thủ
      công gặp lỗi `_Float32`/`_Float64`/`_Float128` "not declared" bên trong `<format>` (qua
      `<chrono>`) — tưởng là bug thật của toolchain/libstdc++13, nhưng **không phải**: dùng
      thẳng driver riêng của toolchain mới (`aarch64-linux-musleabi-g++`, không qua
      `-specs`/`-nostdinc` thủ công) thì sạch ngay. Sau đó chuyển hẳn build sang chạy trên
      **host trực tiếp** (không qua Docker) vì binary toolchain mới cần glibc mới hơn glibc
      trong base image Docker (`GLIBC_2.36`/`2.38` not found). Và khi thêm header hệ thống
      (tbb/mpfr/gmp) bằng cách trỏ thẳng `/usr/include`, dính lỗi `__gnuc_va_list`/`_Noreturn`/
      `uintptr_t` 32-bit — do glibc host lẫn vào trước musl target; sửa bằng cách copy **chỉ
      đúng header cần** (`tbb/`, `oneapi/`, `mpfr.h`, `gmp.h`, `secp256k1*.h`, `uuid/uuid.h`)
      vào 1 thư mục cô lập riêng, không trỏ `/usr/include` bừa bãi. Không có lỗi nào trong nhóm
      này là lỗi thật của mvm/toolchain — toàn bộ là do cách tôi tự ghép flags, đã sửa xong.
    - **Việc thật còn lại cho GĐ3 lúc viết đoạn này (2026-08-16 sáng)**: cross-build
      tbb/mpfr/gmp/secp256k1/uuid cho chcore/musl (không chỉ header), tích hợp vào
      `tz-llm-trustzone`, và giải quyết `-march=native`. **CẬP NHẬT — tất cả đã xong trong
      cùng ngày** (xem các mục bên dưới theo thứ tự thời gian): 5 lib cross-build thật (mục
      "CROSS-BUILD THẬT ĐÃ XONG" + BLST sau đó), tích hợp vào `tz-llm-trustzone` qua image
      `cpp13-metanode-deps` (song song, không đụng `.cpp/aarch64` production), `-march=native`
      đã patch vào CMakeLists.txt thật (commit `a2d28941`). **Vẫn CHƯA làm**: chưa "thay
      `.cpp/aarch64` bằng toolchain 13.3.0" — quyết định cuối (người dùng chọn) là KHÔNG thay,
      giữ song song; và chưa tích hợp vào `rebuild.sh`/`repack.sh` thật (cần 1 TA CMake target
      thật trước, hiện chưa có target đó — xem mục 8 và phần "Chưa làm / còn mở").
  - [x] **Xapian qua proxy I/O — THIẾT KẾ persist-qua-Host (2026-08-16)**: xem mục 5b ngay
    dưới. Kết luận chính: **phần "lưu" đã chạy sẵn 100%, không cần code mới** — chỉ "nạp lại"
    (load) là việc thật cần làm, và chỉ cần nối dây thêm (struct wire đã có sẵn từ GĐ1).
  - [x] **Xác nhận trần bộ nhớ secure thật — ĐÃ LÀM (2026-08-16), số liệu thật từ board:**
    board ban đầu hoàn toàn trống (chưa từng flash) → phục hồi qua
    `flash/recover-golden-image.sh` (ghi `checkpoints/golden-image/idbloader_through_vendor.img`
    raw, ~3.6GB, không đụng userdata) rồi `flash/flash.sh` (uboot/boot_linux đã xác nhận theo
    `checkpoints/SHA256SUMS`) — cả 2 bước chạy sạch, không lỗi/retry nào (xem chi tiết quy
    trình + 1 lần dính đúng bug `cs 2` không timeout đã biết trong README này, tự khắc phục
    bằng cách vào lại MaskROM). Sau khi board boot thật (power-cycle vật lý), bắt log UART
    ngay từ đầu boot (dmesg ring buffer bình thường đã xoay vòng mất log boot sớm sau ~1000s
    uptime — phải bắt tại chỗ lúc reboot, không đọc `dmesg` trên hệ thống đã chạy lâu):
    ```
    zzh: tzasc_cma[0] (tzasc0) reserved 768MiB OK
    zzh: tzasc_cma[1] (tzasc1) reserved 768MiB OK
    zzh: tzasc_cma[2] (tzasc2) reserved 768MiB OK
    zzh: tzasc_cma[3] (tzasc3) reserved 768MiB OK
    ```
    4 vùng × 768MiB = **3072MiB = 3GB chẵn**, không FAILED/NULL — khớp hoàn toàn con số "3GB
    reliability ceiling" đã ghi trong CLAUDE.md. Board đang ở trạng thái khỏe mạnh, xác nhận
    đúng baseline tài liệu mô tả.
    - **Phát hiện phụ, chưa xử lý (không thuộc phạm vi hôm nay)**: SSD NVMe của board hiện
      **không có bảng phân vùng nào** (`/proc/partitions` chỉ thấy `nvme0n1` nguyên đĩa, không
      có `nvme0n1p1`) — khả năng là SSD trống thật (khớp việc board hoàn toàn trống trước đó).
      Chưa động vào (phân vùng/format SSD là việc phá hủy dữ liệu, ngoài phạm vi hôm nay, cần
      hỏi trước khi làm).
  - Theo đúng quy trình build→flash→boot đã có tiền lệ đau — **đã áp dụng đúng trong lần flash
    này**: dùng checkpoints đã xác nhận (không tự build lại), gặp đúng bug `cs 2` không timeout
    đã biết trước và tự khắc phục bằng MaskROM lại (không phải lỗi mới), verify không có
    dòng lỗi/corrupt nào trong log flash.
- **GĐ4 — Kiểm chứng đối chiếu ngoại tuyến:**
  - [x] **Phần loopback (2026-08-15, không cần board):** `tz_differential_replay_test.go` —
    replay 1 chuỗi 6 tx (đủ cả 6 lệnh forward, tx sau phụ thuộc state tx trước, đúng như 1
    block thật) 2 lần độc lập (cgo, rồi trustzone-loopback), cùng 1 tập địa chỉ cho cả 2 lượt
    (khác với các test đối chiếu đơn lẻ ở GĐ2, vốn cố tình dùng địa chỉ khác nhau) — so từng
    bước `ExecuteResult` VÀ hash trạng thái cuối cùng (`AccountStateDB.DeterministicDirtyHash`,
    cùng primitive `true_block_stm.go` dùng cho fork-debugging): **khớp**. Vì dùng chung địa
    chỉ giữa 2 lượt, phải gọi `mvm.ClearAllStateInstances()` giữa 2 lượt để tránh rò rỉ
    State-singleton (xem mục 3.4) — xác nhận log `[State] Clearing all N cached State
    instances` xuất hiện đúng chỗ. Xác nhận sạch ở `-count=5`.
  - [ ] **Phần TA thật:** replay cùng dải trên TA thật (cần GĐ3 xong trước) — chưa làm, cần
    board. Loại block gọi `ExtensionCallGetApi` khỏi so byte-for-byte (nguồn không-determinism
    có từ trước, không phải lỗi riêng TZ mode).
- **GĐ5 — Bring-up 1 node thật:** chạy `execution_mode=trustzone` cạnh cluster `normal`,
  benchmark TPS thực tế, cập nhật `PROJECT_STRUCTURE.md`.

## 7. Không tuyên bố hoàn thành khi

- "Không crash" — một `GGML_ASSERT`/lỗi logic trong TA không abort sạch, có thể treo vô hạn,
  trông giống hệt đang tính toán chậm từ bên ngoài.
- Chưa có dải block replay khớp 100% byte-for-byte (trừ tập gọi HTTP) trên cả loopback và
  TA thật — đúng tinh thần Zero-Fork Invariant của `AGENTS.md`.

## 8. Wire codec cho 6 reverse-callback thật (2026-08-16, tiếp nối GĐ3)

Phát hiện qua full-stack link test (mục GĐ3 ở trên): GĐ2's loopback engine gọi thẳng
`e.real.Call()` (dùng cgo callback có sẵn), nên **6 reverse-callback thật
(`GlobalStateGet`/`GetStorageValue`/`ExtensionCallGetApi`/`ExtensionExtractJsonField`/
`ExtensionBlst`/`ExtensionGetOrCreateSimpleDb`) chưa từng đi qua wire protocol** — kể cả ở
loopback. ID + struct wire (`mvm_tz_protocol.h`, id 101-106) đã có sẵn từ GĐ1, nhưng chưa có
codec Go tương ứng (khác `GET_LATEST_FULL_DB_LOGS`, id 107, đã có codec từ §5b).

- **Đã thêm 6 cặp encode/decode vào `tz_codec.go`** (theo đúng field layout thật, đối chiếu
  trực tiếp implementation Go hiện tại — `mvm_api.go`'s `GlobalStateGet`/`GetStorageValue`,
  `extension.go`'s `Extension*` — không suy đoán từ header comment):
  - `GlobalStateGet`: req = `mvm_id`+`address` (2x20 byte); resp header = `status int32`
    (0=not found/1=found/3=Block-STM suspend), blob (chỉ khi status==1) = balance(32)+nonce(32)+
    code (length-prefixed).
  - `GetStorageValue`: req = `mvm_id`+`address`+`key` (20+20+32); resp header = `status int32`
    (0=STORAGE_SUCCESS/1=NOT_FOUND/2=SUSPEND), blob (chỉ khi status==0) = value(32).
  - `ExtensionCallGetApi`/`ExtensionExtractJsonField`/`ExtensionBlst`: cùng 1 shape bytes-in/
    bytes-out, không header cố định (đúng theo comment gốc trong header) — viết 1 cặp hàm
    generic dùng chung cho cả 3 (identity passthrough, vì blob_len của kênh đã tự đóng khung).
  - `ExtensionGetOrCreateSimpleDb`: req header = `address`+`mvm_id` (2x20 byte) + blob input;
    resp = blob output, không header (khớp comment gốc).
  - `ClearProcessingPointers` **cố tình KHÔNG có codec** — dead code (chính comment trong
    `mvm_api.go` ghi "HÀM NÀY KHÔNG CÒN CẦN THIẾT"), giữ lại chỉ để không vỡ linker C++; TA
    thật chỉ cần 1 stub C nội bộ, không cần qua wire.
- **Test**: `tz_codec_reverse_callbacks_test.go`, 12 test round-trip (đúng khuôn mẫu
  `tz_codec_full_db_logs_test.go`) — status thành công/thất bại cho cả `GlobalStateGet`/
  `GetStorageValue`, shape bytes-in/out cho Extension, header-length-guard cho cả 2 request có
  header cố định. Tất cả pass. `go build`/`go vet`/`go test -count=1` toàn bộ `pkg/mvm`+
  `pkg/storage` sạch, không phá vỡ gì đã có.
- **Chưa làm — cố ý, không phải thiếu sót**: những codec này CHƯA nối vào
  `tz_loopback_engine.go` — làm vậy cần 1 bản `libmvm_linker.a` thứ hai mà `GlobalStateGet`
  et al. tự route qua wire protocol thay vì cgo export thẳng (tức là cần sửa **C++**, không
  chỉ Go) — việc này chỉ thực sự cần thiết khi có TA thật (GĐ3 phần cứng), vì GĐ2's loopback
  cheat bằng cách dùng `*MVMApi` trực tiếp trong cùng process, nơi cgo export vẫn hoạt động
  bình thường bất kể `execution_mode`. Giá trị của việc làm trước: khi có TA thật, codec wire
  đã sẵn sàng + unit-tested, không cần viết lại từ đầu lúc đó.

## 9. Kiến trúc TA thật trên board — điều tra sâu 2026-08-17, TÁCH RIÊNG khỏi LLM TA

Theo yêu cầu tường minh của người dùng: **TA metanode phải là 1 process hoàn toàn độc lập**,
không chia sẻ code path/state/struct nào với LLM TA (`tz-llm-trustzone`'s `llama.cpp`). Mục
này ghi lại kiến trúc thật đã xác nhận qua đọc trực tiếp kernel + driver source (không suy
đoán), làm nền cho lần implement tiếp theo.

### 9.1 Đường dẫn generic driver — có tồn tại nhưng KHÔNG dùng được (đã tự sửa sai)

Ban đầu tưởng `tzdriver` có sẵn đường dẫn generic chuẩn GlobalPlatform (`TC_NS_CLIENT_IOCTL_
SES_OPEN_REQ`/`SEND_CMD_REQ`/`LOAD_APP_REQ`, nhận `uuid` bất kỳ — struct `tc_ns_client_context`
thật có field `uuid[UUID_LEN]`). **Đọc kỹ hơn phát hiện đường này chết**: `tc_init()`
(`tzdriver/core/tc_client_driver.c`) gọi `llm_client_init()` (đăng ký `g_llm_ns_client_fops`,
chỉ xử lý 7 ioctl `LLM_CLIENT_IOCTL_*`, id 24-30) rồi **`return` sớm** — code generic
(`tc_ns_client_init()`, đăng ký `g_tc_ns_client_fops` + gọi `tc_teeos_init()`) không bao giờ
chạy. **Đề xuất ban đầu "bỏ return sớm" là SAI** — `tc_ns_client_init()` gọi lại
`load_hw_info()`/`load_reserved_mem()`/`tc_teeos_init()` (SMC context, reserved mempool...) —
chạy cả 2 sẽ **double-init phần cứng secure-world**, đúng loại lỗi CLAUDE.md cảnh báo gây
corruption. Tự phát hiện và dừng lại trước khi viết patch nào — không đề xuất lại hướng này.

### 9.2 Cơ chế shared-memory thật, ĐÃ CHỨNG MINH hoạt động — tái dùng được, không cần sửa kernel

Đọc `alloc-stage-chcore.cpp` (TA-side, dùng để stream model weight — chạy ổn định trên board
này) xác nhận chuỗi API thật để TA tự xin + map 1 vùng CMA vào không gian địa chỉ của chính
nó, không cần ioctl phía CA:
```cpp
vaddr_t vaddr = chcore_alloc_vaddr(size);              // reserve 1 dải vaddr trống trong TA
int entry_index = push_pages_ex(size, cma_index, soft); // SMC xin vùng CMA vật lý (đã proven)
unsigned long paddr = tzasc_cma_meta_arr[cma_index].entry[entry_index].paddr;
usys_map_tzasc_cma_pmo(vaddr, size, paddr);              // map paddr vào vaddr — giờ dùng được như con trỏ thường
```
`push_pages_ex` dùng `usys_tee_switch_req` (KHÁC `usys_tee_wait_switch_req`) — 1 request/reply
đơn, không phải vòng lặp chờ sự kiện. Phía CA muốn cùng vùng vật lý thì `LLM_CLIENT_IOCTL_
SET_PAGES` (đã có, đã proven — `alloc-stage.cpp`/`io-backend.cpp` dùng hàng ngày cho weight
streaming) mmap đúng `cma_index`+`entry_index`. **Toàn bộ chuỗi này không đụng 1 dòng kernel
nào cả** — thuần túy gọi lại API TA/CA userspace đã có sẵn.

### 9.3 `chanmgr` đã chạy nhiều process độc lập thật — xác nhận qua source

`chanmgr/main.c` dùng `create_process()` cho **cả `/rknpu.srv` VÀ `llama-cli`** — 2 process
chcore riêng biệt, xác nhận chcore là multi-process OS thật, không giới hạn 1 TA/boot như giả
định ban đầu. **Nhưng**: launch `llama-cli` xong, `chanmgr`'s main thread gọi
`waitpid(pid, NULL, 0)` — **block vĩnh viễn** vì `llama-cli` chạy `while(true)` vô hạn (đúng
thiết kế phục vụ nhiều request). Hệ quả: launch TA metanode phải xảy ra **trước** đoạn
`llama-cli`, hoặc từ 1 pthread riêng bên trong `chanmgr` (spawn trước, `waitpid` riêng, không
đụng logic launch `llama-cli` hiện có) — chưa viết patch, chỉ xác nhận đúng chỗ cần sửa.

### 9.4 Rủi ro thật còn mở, CHƯA giải quyết được chỉ bằng đọc code

`sys_tee_wait_switch_req` (kernel, `spd/teed/smc.c`) dùng state **per-CPU**
(`smc_percpu_structs[cpu_id]`), có guard `-EINVAL` nếu 2 thread cùng CPU tranh làm
`waiting_thread` — không phải race ngầm, lỗi sạch. Nhưng **bug lịch sử thật** (STATUS.md mục
4) là lỗi **thứ tự**: nhiều lời gọi `wait_switch_req` từ các nguồn khác nhau tiêu thụ nhầm SMC
event của nhau, gây `paddr=0` → **crash kernel thật đã xảy ra trên board này**. TA metanode có
vòng lặp `wait_switch_req` RIÊNG (không dùng chung với `main.cpp`'s) — an toàn ở mức "không
tranh trực tiếp" (per-CPU + guard), nhưng **chưa xác nhận được** hành vi khi 2 TA cùng lúc gọi
`wait_switch_req` trên các CPU khác nhau có genuinely độc lập hay không (cần đọc thêm
`smc_smp.c`/lịch sử schedule, hoặc — thực tế hơn — test trên board thật, đúng cách bug gốc
từng được phát hiện: không đọc code trước mà lường được).

### 9.5 CA khám phá địa chỉ shared-page của TA metanode bằng cách nào — ĐÃ XÁC NHẬN (2026-08-17)

Vì tách riêng hoàn toàn khỏi `all_ring_buffer` (theo yêu cầu người dùng), CA không có kênh nào
sẵn để đọc `cma_index`/`entry_index` TA metanode vừa `push_pages()`. Đọc trực tiếp
`tc_client_driver.c`'s `tzasc_cma_push_pages_with_index()` (kernel-side, service thật cho
`push_pages_ex`'s SMC) xác nhận:
```c
struct tzasc_cma_meta *g_tzasc_cma_meta = g_tzasc_cma_meta_arr + cma_index; // 1 meta/count RIÊNG mỗi bank
...
entry = &g_tzasc_cma_meta->entry[g_tzasc_cma_meta->count];
ret = g_tzasc_cma_meta->count++;   // entry_index = count TRƯỚC khi tăng
```
`entry_index` cấp **tuần tự tuyệt đối từ 0** cho mỗi `cma_index` riêng — deterministic thật,
không suy đoán. Cũng phát hiện đoạn này có `spin_lock_irqsave(&tzasc_cma_lock, ...)` bảo vệ
`count++` — comment ghi rõ đây là fix cho 1 bug thật (race CA-ioctl vs TA-SMC-relay từng gây
corruption dữ liệu tensor) — bookkeeping này đã an toàn với gọi đồng thời từ nhiều nguồn.

**Hệ quả**: `entry_index=0` chỉ deterministic nếu TA metanode là người gọi `push_pages()`
**đầu tiên** trên `cma_index` nó chọn — vì tensor pool LLM có thể tràn vào bất kỳ bank nào
trong 4 bank khi model đủ lớn (không bank nào chắc chắn "sạch"). **Giải pháp chốt**: TA
metanode phải launch + tự `push_pages()` xong **TRƯỚC KHI `llama-cli` launch** trong
`chanmgr` — khớp đúng ràng buộc §9.3 (launch metanode phải xảy ra trước đoạn `llama-cli` do
`waitpid()` block vĩnh viễn sau đó). Lúc đó chưa có gì đụng TZASC, `entry_index=0` chắc chắn
đúng suốt phiên boot — CA không cần handshake sống, chỉ cần biết trước cặp
`(cma_index, entry_index)=(N, 0)` cố định để `SET_PAGES` mmap thẳng, N là bank dành riêng cho
metanode (đề xuất: bank không phải `TZASC_NR_MODEL`/`TZASC_NR_NPU_SCRATCH` để giảm khả năng
tràn vào, dù không tuyệt đối loại trừ nếu model rất lớn).

### 9.6 Implement thật lần đầu (2026-08-17) — tách biệt hoàn toàn khỏi LLM TA

Theo yêu cầu tường minh của người dùng ("tách việc chạy xử lý block metanode trong trustzone
và việc llm trong trustzone ra 2 phần rõ ràng"): viết `execution/pkg/mvm/ta/mvm_ta_main.cpp`
(metanode repo) — **zero dependency** vào `tz-llm-trustzone/tz-llm/llama.cpp` (không include,
không link, không chung struct/process). Chỉ dùng chung header SDK chcore-libc (API công khai
của hệ điều hành) + tái hiện độc lập cơ chế `push_pages`/`usys_map_tzasc_cma_pmo` (không gọi
lại code của project kia).

- **Channel setup**: `push_pages_ex` (tự viết, không phụ thuộc `alloc-stage-chcore.cpp`) +
  `usys_map_tzasc_cma_pmo` — đúng chuỗi API đã xác nhận ở §9.2, dùng `MVM_TZASC_CMA_INDEX=1`
  (né `TZASC_NR_MODEL`/`TZASC_NR_NPU_SCRATCH`).
- **Dispatch loop**: busy-poll thuần túy trên cờ atomic (`usys_yield()` giữa các lần check) —
  **cố tình không dùng `usys_tee_wait_switch_req` ở bất kỳ đâu**, né hẳn rủi ro §9.4 thay vì cố
  chứng minh nó an toàn.
- **6 reverse-callback thật** (`GlobalStateGet`/`GetStorageValue`/`ExtensionCallGetApi`/
  `ExtensionExtractJsonField`/`ExtensionBlst`/`ExtensionGetOrCreateSimpleDb`): implement đầy đủ,
  round-trip qua cùng channel (nested round-trip trong lúc xử lý forward command).
- **`MVM_TZ_CMD_CALL`**: round-trip đầy đủ, gọi `call()` thật (chữ ký xác nhận trực tiếp từ
  `mvm_linker.hpp`, phát hiện + tự sửa 1 lỗi thật lúc viết: thiếu tham số `b_block_number`
  32-byte big-endian, đã convert từ `req.block_number` uint64 đúng theo cách Go làm
  `big.NewInt(...).FillBytes(...)`). `ExecuteResult` encode đầy đủ cho status/exception/
  gas_used/output/exmsg + 3 mảng state-change đơn giản nhất (`add_balance_change`/
  `sub_balance_change`/`nonce_change`, dạng `[32B addr left-pad][32B value]`, xác nhận qua đọc
  `extractAddBalance`/`extractSubBalance` thật trong `mvm_api.go`).
- **CHƯA làm, ghi rõ TODO trong code**: encode đầy đủ cho `code_change`/`storage_change`/
  `storage_read`/`full_db_hash`/`full_db_logs`/`event_logs`/`native_logs` (đếm đúng số lượng,
  chưa ghi entry — không sai dữ liệu, chỉ trả rỗng). 5 forward command còn lại (Execute/Deploy/
  SendNative/ProcessNativeMintBurn/NoncePlusOne) chưa wire (cơ chế giống hệt Call, hoãn lại).

**Verify thật đã làm được (không cần board)**: compile sạch bằng toolchain cross GCC13.3.0
thật (0 lỗi, AArch64 ELF). Link full-stack (mvm+linker+5 lib+Xapian+TBB+zlib+`libc.a` thật của
chcore-libc) → **0 undefined symbol** — xác nhận toàn bộ thiết kế logic/symbol-graph đúng.
**Lưu ý trung thực**: bản link này KHÔNG phải build deploy được (trộn runtime musl-cross-make
với `libc.a` thật của chcore trong cùng 1 binary — sai ABI cho phần cứng thật) — chỉ để verify
symbol graph. Build thật cần đúng toolchain GCC9+specs-wrapper qua Docker pipeline như LLM TA
(`build-llama-docker.sh`'s `build-chcore`), **chưa làm**.

**Patch `chanmgr/main.c`** (tz-llm-trustzone repo): thêm launch `/mvm_ta` qua `create_process()`
**đồng bộ, ngay trước** đoạn launch `llama-cli` (chỉ `waitpid()` đẩy sang thread riêng, không
block `main()`) — đúng ràng buộc thứ tự §9.5.

**BUILD THẬT ĐÃ XÁC NHẬN (2026-08-17)** — chạy trực tiếp `tee_os_kernel/build/build_tee.sh`
qua Docker image `vectorxj0553/tz-llm-oh-builder:latest` (đúng target build TEE-OS thật, không
phải toàn bộ `rebuild.sh` — bỏ qua `linux.sh`/uboot vì chỉ đổi 1 file `tee_os_kernel`, không có
gì phía llama.cpp/kernel Linux cần build lại, đúng theo hướng dẫn CLAUDE.md "Changed only
tz-llm/tee_os_kernel... skip step 1"). Script này tự chạy `make clean && make -j$(nproc)` —
build **toàn bộ** TEE-OS, không chỉ `chanmgr`. Kết quả: `[exited with code 0]`, **0 dòng
`error:`** trong toàn bộ log — `chanmgr` build sạch (thấy rõ dòng code mới
`if (__ipc_tls.chanmgr_ipc_struct == NULL) {` chạy qua compiler không lỗi), rồi build tiếp
`procmgr`, `kernel/` (nhiều warning nhưng toàn bộ đều **có sẵn từ trước**, ở các file tôi không
đụng vào — `object/memory.c`, `opteed/smc.c`, `mmparse.c`, `irq.c` — không phải do patch này
gây ra). **Patch `chanmgr/main.c` build thật thành công, không còn là "chưa compile được".**

**Việc CHƯA làm, rõ ràng (vẫn còn thật)**:
- Build thành công ở đây là build TEE-OS **đứng riêng** — chưa chạy `rebuild.sh` đầy đủ (cần
  cả `linux.sh` bracket theo đúng thứ tự FIT-hash, xem `build-oh-docker.sh`'s comment), nên
  **chưa có `boot.img`/`uboot_repacked.img` mới** để flash. Đây là bước tiếp theo, không phải
  đã xong.
- `mvm_ta` binary **chưa tồn tại** — `create_process(1, {"/mvm_ta"}, NULL)` trong patch sẽ thất
  bại (log rõ ràng, không fatal, theo đúng code đã viết) cho đến khi có 1 target build thật tạo
  ra binary đó và đặt vào `oh_tee/apps/mvm_ta` (chưa tích hợp vào pipeline Docker, xem mục dưới
  — file `mvm_ta_main.cpp` mới chỉ verify link-sạch trên x86, chưa build bằng đúng toolchain
  GCC9+specs-wrapper của chcore thật).
- Chưa tích hợp `mvm_ta` vào pipeline Docker thật (`build-chcore`/`build-llama-docker.sh`'s
  tương tự) — chưa có target build thật tạo ra binary `mvm_ta` cho chcore.
- Chưa test trên board thật bất kỳ phần nào ở mục này (kể cả launch-order/`entry_index=0`
  giả định — chỉ verify được khi có binary `mvm_ta` thật để boot thử).
- Encode đầy đủ cho 7/10 loại mảng state-change còn lại + 5 forward command còn lại.
- §9.4's rủi ro wait_switch_req — nay đã NÉ HẲN bằng thiết kế busy-poll, không còn áp dụng cho
  cơ chế của metanode nữa (nhưng vẫn là rủi ro thật cho `llama-cli`'s riêng, không phải việc
  của metanode giải quyết).

File: `metanode/execution/pkg/mvm/ta/{mvm_ta_main.cpp,README.md}` (mới, verify link x86),
`tz-llm-trustzone/tz-llm/tee_os_kernel/user/system-services/system-servers/chanmgr/main.c`
(sửa, **build TEE-OS thật thành công, xác nhận 2026-08-17**).

### 9.7 `mvm_ta` — build DEPLOYABLE thật thành công (2026-08-17, tiếp nối "có tiếp tục")

Phần link x86 ở mục 9.6 (trộn runtime musl-cross-make với `libc.a` thật của chcore) chỉ để
verify symbol graph, KHÔNG phải build deploy được — tự ghi rõ điều này. Tiếp tục điều tra để
có bản build thật, tìm ra và giải quyết đúng gốc rễ:

- **Nguyên nhân gốc thật của lỗi `_Float32`** (đã gặp từ trước trong lịch sử dự án, tưởng đã
  hiểu — hoá ra chưa đúng): `_Float32`/`_Float64`/`_Float128` là C++ TYPE (không chỉ macro giới
  hạn `__FLT32_DIG__`) chỉ được GCC's C++ frontend hỗ trợ **từ GCC 13 trở lên**. Compiler thật
  trong Docker (`musl-gcc` bọc `aarch64-linux-gnu-gcc-11`) là **GCC 11.4.0** — predefine macro
  `__FLT32_DIG__` (kích hoạt nhánh code dùng `_Float32`) nhưng KHÔNG có type đó → lỗi
  "not declared". Xác nhận qua test tối giản (`#include <chrono>` alone) trước khi đụng build
  thật — không suy đoán.
- **Fix thật**: build 1 toolchain musl-cross-make THỨ HAI, ghim `GCC_VER=11.5.0` (bản gần nhất
  có sẵn hash trong musl-cross-make, khớp GCC 11.4.0 thật — cùng major.minor, ABI/header
  libstdc++ tương thích). Stage header/lib giống hệt cách cũ, TÁI DÙNG nguyên `3rdparty/` (5 lib
  C thuần, ABI ổn định qua các version GCC) — **trừ GMP/MPFR phải build lại với `--with-pic`**
  (bản gốc không position-independent, link thật của chcore là PIE binary — lộ ra qua lỗi
  `relocation R_AARCH64_ADR_PREL_PG_HI21 ... can not be used when making a shared object`).
- **Build command thật**: `c_mvm`+`linker` qua CMake với `CMAKE_C_COMPILER`/`CXX_COMPILER` =
  `musl-gcc` thật (`chcore-libc/bin/musl-gcc`, đúng ABI musl syscall/struct), `CMAKE_AR`/
  `RANLIB` = `aarch64-linux-gnu-ar`/`-ranlib` hệ thống (`musl-ar` có sẵn nhưng không có
  `musl-ranlib` tương ứng). `mvm_ta_main.cpp` compile trực tiếp bằng `musl-gcc`, cần `-I` đúng
  `tz-llm/tee_os_kernel/.../chcore-libc/musl-libc/install/include` thật của repo host (bản baked
  sẵn trong Docker image `chcore-libc/include/chcore/` là snapshot CŨ, thiếu `llm.h`/
  `TZASC_NR`). Link cuối cùng cũng qua `musl-gcc`, `--start-group/--end-group` toàn bộ
  `libmvm_linker.a`+`libmvm.a`+5 lib 3rdparty+Xapian+TBB+zlib, rồi `-L.../lib libstdc++.so
  libgcc_s.so` — đúng y hệt cách repo này tự link `llama-cli` (chỉ khác version libstdc++).
- **Kết quả xác nhận thật**: `mvm_ta` — executable AArch64 PIE thật (`readelf -h`: `Type: DYN`,
  `Machine: AArch64`), 42.9MB, **0 undefined symbol, `FINAL_LINK_EXIT=0`**. Không còn trộn
  runtime, không còn hack verify-only — đây là tooling hình dạng production thật. Cần
  `libstdc++.so.6.0.29`/`libgcc_s.so.1` đi kèm lúc runtime (giống hệt `llama-cli` cần bản `.so`
  riêng trong ramdisk).
- **1 rủi ro còn mở, chưa kiểm chứng**: `libxapian.a`/`libtbb.a` link vào bản thật này **vẫn là
  bản build với toolchain GCC13.3.0 cũ** (không rebuild lại với GCC11.5.0) — chưa gặp lỗi
  `_Float32` vì chúng không transitively include `<chrono>`'s C++20 format/stream integration,
  nhưng đây là 1 giả định chưa kiểm chứng đầy đủ về ABI libstdc++ ổn định qua các version GCC.
  Đáng làm rõ trước khi tin tưởng hoàn toàn (không chỉ "link không lỗi" mà "đúng runtime").
- **Vẫn CHƯA test trên board thật** — đây là mốc "build/link đúng", không phải "đúng runtime".
  File thật: `tz-llm-trustzone/scripts/kick-the-tires/cpp13-metanode-deps/{README.md,
  mvm_toolchain_chcore_real.cmake}` — đã commit (`d339fa2bb`).

### 9.8 `mvm_ta` staged vào `oh_tee/apps` — xác nhận qua build thật (2026-08-17, "tích hợp mvm_ta vào oh_tee/apps đi")

Bước tiếp theo bắt buộc để `chanmgr`'s `create_process("/mvm_ta")` (patch §9.6) thật sự tìm
thấy binary: `mvm_ta` phải nằm trong `oh_tee/apps/` trước khi `build_tee.sh` chạy, vì
`build_tee.sh` làm `cp oh_tee/apps/* ramdisk-dir` **trước** `make` (không điều kiện).

- **2 file sửa trong `tz-llm-trustzone`** (giữ đúng nguyên tắc tách biệt — chỉ tái dùng cơ chế
  staging có sẵn, không đụng code/struct LLM):
  - `scripts/kick-the-tires/oh-builder-hdf.sh`: thêm 1 bind mount
    `cpp13-metanode-deps/mvm_ta_output:/home/vectorxj/mvm_ta_build:ro`.
  - `scripts/kick-the-tires/chcore-extracted.sh`: thêm 1 khối `if [ -f
    "$MVM_TA_BUILD/mvm_ta" ]; then cp ... ../oh_tee/apps/; fi` — copy `mvm_ta` +
    `libstdc++.so.6.0.29` + symlink `libstdc++.so.6` + `libgcc_s.so.1`, đặt ngay sau khối
    re-copy `llama-cli` có sẵn, trước `./build_tee.sh`. Y hệt pattern cũ, chỉ khác tên file.
- **Verify bằng build thật, không chỉ đọc code**: chạy `chcore.sh` trực tiếp qua
  `oh-builder-hdf.sh` (image `vectorxj0553/tz-llm-oh-builder:latest`), bỏ qua
  `linux.sh`/uboot vì chỉ 2 script staging đổi (không phải TA/kernel source) — xác nhận
  `mvm_ta` (42,955,336 bytes, khớp md5 bản build ở §9.7) xuất hiện đúng trong CẢ
  `oh_tee/apps/` lẫn `ramdisk-dir/`.
- **1 lỗi build không liên quan gặp trong lần chạy verify này**: `make` (bên trong
  `build_tee.sh`, sau bước copy ramdisk-dir) báo `clang: Command not found` khi build
  `procmgr`'s `read_procmgr_elf_tool` — do script verify tự viết gọi thẳng `chcore.sh` mà
  không export `PATH` trỏ `/home/tools/clang_linux-x86_64-36cd05-20221030/bin` (việc
  `build-oh-docker.sh` thật luôn làm trước khi gọi `chcore.sh`). Xác nhận `clang` binary có
  thật trong image (`ls /home/tools/clang_.../bin/clang` OK) — chỉ là thiếu PATH trong lần
  gọi tắt, không phải regression từ 2 file vừa sửa. Full pipeline thật (qua `rebuild.sh` →
  `build-oh-docker.sh`) không có lỗi này vì nó tự export PATH đúng.
- **Sự cố phụ trong lúc verify, đã xử lý**: `build_tee.sh` chạy `make clean` trên
  `tee_os_kernel` — vì thư mục này bind-mount THẲNG vào repo host (không phải bản copy), lệnh
  này xoá mất 4 file đã track trong git (`kernel/incbin_promgr_bin.S`, `kernel/linker.ld`,
  `kernel/linker.ld.S`, `procmgr/read_procmgr_elf_tool` — build artifact được commit sẵn để
  tiện dùng). Phục hồi ngay bằng `git checkout --` trước khi commit gì khác — không phải lỗi
  do 2 file sửa ở đây, là hệ quả biết trước của cách `oh-builder-hdf.sh` mount thư mục.
- **Đã commit**: `861a2f08d` (`tz-llm-trustzone`).
- **Còn lại, CHƯA làm**: chạy full pipeline thật (`rebuild.sh`) để ra `boot.img` mới có
  `mvm_ta`, rồi đi hết quy trình build/flash/test 8 bước của `CLAUDE.md` trên phần cứng thật
  — chưa được yêu cầu, sẽ cần định hướng rõ ràng từ người dùng trước khi bắt đầu (rủi ro/thời
  gian đáng kể, đụng tới flash board thật).

### 9.9 Chạy thật lần đầu trên phần cứng — TEXTREL bug, đã fix (2026-08-17→18)

`mvm_ta` chạy thật lần đầu trên board bị secure-world loader từ chối với lỗi mơ hồ "Not a
valid dynamic program". Sau khi loại trừ 2 giả thuyết sai (TLS, kích thước BSS — cả 2 đều
được "sửa" nhưng không phải nguyên nhân thật), dùng `-Wl,-z,text` làm cờ chẩn đoán tại link
time tìm ra nguyên nhân thật: `libxapian.a`/`libz.a` (build từ phiên trước, dùng GCC13.3.0)
không được build với `-fPIC`, gây `DT_TEXTREL` — loader của chcore từ chối bất kỳ ELF nào cần
`mprotect(RWX)` để áp text relocation. Fix: build lại cả 2 thư viện với `-fPIC` thật từ
source gốc (`scripts/kick-the-tires/cpp13-metanode-deps/xapian_zlib_pic_rebuild/build_pic.sh`).
Xác nhận qua UART: `[mvm_ta] starting` xuất hiện lần đầu tiên trên phần cứng thật.

### 9.10 Bug thứ nhất, đã fix + xác nhận: race điều kiện khởi động (`g_tzasc_cma_meta_paddr`)

Sau TEXTREL fix, `mvm_ta` crash ngay với `BUG_ON(g_tzasc_cma_meta_paddr == 0)`
(`kernel/object/memory.c`'s `sys_map_tzasc_cma_meta()`). Bốn giả thuyết sai liên tiếp (reorder
gọi hàm, `smp_wmb`/`smp_rmb`, `arch_flush_cache` CLEAN/INVALIDATE, ghim CPU affinity) đều coi
đây là vấn đề visibility/cache giữa 2 core — sai. **Nguyên nhân thật**: `chanmgr` launch
`mvm_ta` đồng bộ, ngay lập tức, **trước khi Normal-World Linux tồn tại** — trong khi
`g_tzasc_cma_meta_paddr` chỉ được ghi bởi `tzasc_cma_meta_init()`, chỉ đạt tới được từ SMC
yield **đầu tiên do Normal World khởi tạo** (`handle_yield_smc()`) — về mặt cấu trúc không
thể xảy ra trước khi Linux boot đủ xa để gửi 1 SMC như vậy. Bằng chứng UART: dòng
`[mvm_ta] starting` và lần đọc thất bại luôn nằm **trước** banner `Booting Linux on physical
CPU`, còn lần ghi thật luôn nằm **sau**, cách nhau 14–50s tùy lần boot (không cố định).

**Fix (đã xác nhận đúng trên phần cứng)**: kernel trả `-EAGAIN` thay vì `BUG_ON` khi
`g_tzasc_cma_meta_paddr==0`; `mvm_ta` tự retry. Bản thân cơ chế retry đã trải qua 3 lần sửa
lỗi tự gây ra trong lúc tìm cơ chế chờ đúng (xem 9.11) trước khi ổn định — nhưng **kết quả
cuối**: `BUG_ON` không còn xảy ra, xác nhận nhiều lần qua UART.

### 9.11 3 lần tự sửa lỗi trong lúc tìm cơ chế "chờ" đúng cho retry loop

1. **`nanosleep()`** giữa các lần retry: không tin cậy ở giai đoạn boot cực sớm này (chưa
   từng được dùng ở đây trong toàn bộ codebase) — live trên hardware: loop im lặng hoàn toàn
   sau lần in đầu tiên, không thành công, không FATAL sau 60s — chỉ ra loop bị kẹt bên trong
   chính `nanosleep()`. Bỏ.
2. **`usys_yield()` spin thuần với cap theo SỐ LẦN LẶP** (20 triệu lần): số lần lặp không
   phải proxy an toàn cho thời gian thực — live: 20 triệu lần chạy hết trong vài giây (nhanh
   hơn dự đoán rất nhiều), FATAL abort trước khi write kịp đến. Bỏ.
3. **`usys_yield()` spin thuần với cap theo THỜI GIAN THỰC** (`clock_gettime`, 90s): tự nó lại
   gây ra 1 lỗi mới nghiêm trọng hơn — cả `usys_map_tzasc_cma_meta()` lẫn `usys_yield()` đều
   là syscall THUẦN nội bộ secure-world, không bao giờ world-switch sang Normal World. Spin
   nhanh trên 1 CPU **có thể tự chặn Normal World chạy trên đúng core đó** — live: chờ đủ 90s
   mà write chưa từng đến (lâu hơn hẳn mọi lần quan sát trước 14–50s), một dạng bế tắc tự gây
   ra. Bỏ.
4. **Lỗi tự gây thêm, phát hiện giữa các lần trên**: dòng `kinfo` debug thêm vào
   `sys_map_tzasc_cma_meta()` (không throttle) khi kết hợp với spin nhanh → flood UART hàng
   chục nghìn dòng/giây → rủi ro soft-lockup thật (đã dừng kịp bằng power-cycle, chưa gây hư
   hại). Fix: throttle 1/200000. Riêng biệt: `%#lx` trong `kinfo` không được `kinfo()` của
   chcore hỗ trợ đúng (in ra literal `lx`) — dùng `%lx` (không `#`) thay thế.

**Fix cuối cùng, đúng**: quay lại thứ tự `push_pages()` TRƯỚC `usys_map_tzasc_cma_meta()`
(giống fix attempt #1 ban đầu, nhưng lý do khác hẳn — xem 9.12) — vì `usys_tee_switch_req()`
(cơ chế `push_pages()` dùng) là round-trip THẬT, block đúng cách (TS_INTER, không tốn CPU),
world-switch sang Normal World thật — không có rủi ro tự chặn như spin thuần.

### 9.12 Bug thứ hai, đã fix + xác nhận: thứ tự `push_pages()`/`usys_map_tzasc_cma_meta()`

Lý thuyết ban đầu ("map trước, push sau", mirror `alloc-stage-chcore.cpp`'s `tzasc_cma_init()`
gọi trong `push_pages_ex()`) hoá ra sai cho trường hợp `mvm_ta`: cơ chế đó chỉ đúng cho
`llama-cli` vì nó launch SAU KHI Normal World đã boot xong — lúc nó gọi `push_pages_ex()` lần
đầu, `tzasc_cma_meta_init()` đã chạy từ trước (do hoạt động khác). `mvm_ta` không có điều kiện
đó. **Fix đúng, đã xác nhận**: gọi `push_pages()` TRƯỚC — round trip thật của nó tự đảm bảo
ít nhất 1 SMC yield do Normal World khởi tạo đã xảy ra trước khi nó return, nên
`usys_map_tzasc_cma_meta()` gọi ngay sau đó chắc chắn thành công (không cần retry loop phức
tạp — chỉ giữ 1 safety net nhỏ, có giới hạn).

### 9.13 Bug thứ ba, ĐÃ TÌM RA NHƯNG CHƯA FIX ĐƯỢC AN TOÀN: SMC response bị "nuốt"

Sau 2 fix trên, `mvm_ca_test` (tool test CA tự viết, có relay-thread mirror đúng
`fake_ca.cpp`'s `ca_thread`/`LLM_CLIENT_IOCTL_RUN`) vẫn không mmap được kênh — `dmesg` báo
`entry->size=0`. Đào sâu tìm ra **2 nguyên nhân độc lập, cả 2 đều là race điều kiện khởi động
sớm, và cả 2 đều nằm trong code kernel/driver DÙNG CHUNG với `llama-cli`**:

1. **`not_first_smc[cpu]` (kernel, `opteed/smc.c`'s `sys_tee_switch_req()`)**: gate mỗi-CPU,
   coi lệnh `sys_tee_switch_req` ĐẦU TIÊN trên 1 CPU là handshake "CPU entry done" (do
   `chanmgr`'s 16 idle-thread mồi từ đầu `main()`), bỏ qua payload thật nếu có. `mvm_ta`
   launch đủ sớm để đôi khi CHÍNH request `push_pages()` thật của nó là lệnh đầu tiên trên
   CPU đó — bị nuốt thành handshake, mất payload thật. Đã thử fix bằng cách "mồi" trước
   (`mvm_prime_smc()`) — nhưng thread `mvm_ta` **di chuyển CPU giữa các round-trip** (xác nhận
   qua scheduler đang dùng là `pbrr`, không phải `rr` — `pbrr`'s hàng đợi ready là 1 hàng đợi
   TOÀN CỤC dùng chung cho mọi CPU, `find_runnable_thread()` không hề đọc `affinity` khi chọn
   thread tiếp theo — `usys_set_affinity()` **không có tác dụng thực tế** dưới scheduler này).
   Sửa bằng cách mồi nhiều lần (24 lần, > 3×`PLAT_CPU_NUM`) để phủ hết khả năng di chuyển —
   xác nhận qua UART: request thật cuối cùng đạt `not_first_smc=1` (không còn bị nuốt kiểu
   này).
2. **`llm_tee_os_init()` (tzdriver, `tc_client_driver.c`, Linux kernel module_init time)**:
   vòng lặp probe SHM-handshake RIÊNG của chính driver, độc lập hoàn toàn với cơ chế relay
   (`smc_call_cpu_resume()`). Vòng lặp này gọi `do_smc_transport()` với payload riêng
   (paddr SHM), chỉ kiểm tra `out.ret == SMC_EXIT_PREEMPTED` hay không — **không hề đọc
   `out.target`/`exit_reason`** — bất kỳ response nào khác PREEMPTED (kể cả `SMC_EXIT_SHADOW`
   mang theo con trỏ thread thật của `mvm_ta`) đều bị coi là "chưa khớp marker, thử lại",
   **nuốt mất payload thật vĩnh viễn** — không ai còn cách nào đánh thức lại thread đó. Đây
   là race đã có tiền lệ với chính `llama-cli` (comment "BUG FIX 2026-08-08" trong code driver
   xác nhận 1 dạng tương tự đã từng gây hang dài hạn cho LLM TA).

**Vì sao chưa fix**: fix đúng đắn cho #2 đòi hỏi sửa `tc_client_driver.c` — code driver dùng
chung, đang phục vụ `llama-cli` production. Theo đúng nguyên tắc tách biệt dự án (đã chốt với
người dùng), không đụng vào code này mà không có kế hoạch test kỹ trên cả 2 đường
(`-s 0`/`-s 1`) của LLM TA.

**Thử "mồi thêm nhiều lần + chờ đủ thời gian thực (25s) trước khi push_pages() thật" — THẤT
BẠI, và quan trọng hơn: PHÁT HIỆN RA MỘT NGUYÊN LÝ KIẾN TRÚC**: mỗi lệnh SMC riêng lẻ (kể cả
lệnh "mồi" vô hại) đều có rủi ro bị nuốt bởi #1 hoặc #2. Xác nhận live: chính vòng lặp mồi
(24+ lần, chạy tới >300 lần thực tế trước khi kẹt) cuối cùng cũng bị kẹt vĩnh viễn ở 1 lệnh
mồi nào đó — **gọi càng nhiều lệnh SMC càng làm TĂNG xác suất tích luỹ bị kẹt, không giảm**.
Không thể giải quyết bằng "thử thêm" trong phạm vi `mvm_ta`.

### 9.14 Hướng đi tiếp theo (chưa làm, cần quyết định của người dùng ở phiên sau)

1. **Chấp nhận sửa code driver dùng chung** (`llm_tee_os_init()` forward đúng
   `SMC_EXIT_SHADOW` thay vì bỏ qua, lý tưởng nhất là hợp nhất với dispatch table của
   `smc_call_cpu_resume()`), kèm test đầy đủ `llama-cli` trên cả `-s 0` và `-s 1` sau đó
   (đúng golden rule của `CLAUDE.md`).
2. **Redesign multi-thread trong `mvm_ta`**: pattern retry-with-fresh-thread — nếu 1 luồng
   gọi SMC bị kẹt quá lâu (watchdog), spawn luồng mới thử lại, chấp nhận rò rỉ luồng cũ bị
   kẹt. Không cần đụng driver chung, nhưng phức tạp hơn nhiều, tự nó cũng không loại bỏ hoàn
   toàn rủi ro (chỉ giảm ảnh hưởng khi rủi ro xảy ra).
3. **Không launch `mvm_ta` sớm như hiện tại** — cần `chanmgr`'s launch order đổi (cũng là
   code dùng chung, dù nhỏ hơn tzdriver).

### 9.15 Trạng thái board hiện tại (cuối phiên 2026-08-18)

Board đang chạy bản build có **fix #1 (`not_first_smc` priming 24 lần) + fix #2 (chờ 25s thực)
— bản này ĐÃ XÁC NHẬN BỊ KẸT** (do chính hiện tượng mô tả ở 9.13) trong lần test cuối. Cần
power-cycle + xác nhận lại trạng thái trước khi tiếp tục bất kỳ thay đổi nào ở phiên sau —
đừng giả định từ tên file, đọc kỹ UART thật.

Backup các bản build `mvm_ta` trước đó (theo timestamp fix) còn lưu ở
`scripts/kick-the-tires/cpp13-metanode-deps/mvm_ta_output_2026-08-18-*-old/` — hữu ích nếu
cần so sánh/rollback.

**Đa nền tảng (yêu cầu người dùng, 2026-08-18, chưa triển khai)**: xem
`[[metanode-multi-hardware-design-goal]]` trong memory — cần trừu tượng hoá các hằng số
đặc thù board (`PLAT_CPU_NUM`, layout TZASC, ioctl number của `tzdriver`) trước khi tính tới
hỗ trợ phần cứng khác, sau khi vấn đề correctness ở 9.13 được giải quyết.

### 9.20 Đánh giá lại kiến trúc + thiết kế mới, đơn giản hoá (2026-08-19)

Sau khi thử watchdog+retry-with-fresh-thread (§9.18/9.19) và tự nó phát sinh 1 bug mới không
rõ nguyên nhân (watchdog không fire đúng thiết kế sau khi tích lũy ~10 thread) — người dùng
yêu cầu dừng lại, đánh giá tổng thể hướng đi thay vì tiếp tục vá triệu chứng.

**Phát hiện chiến lược quan trọng khi đọc lại code `llama-cli` (chỉ đọc, không share)**:
`examples/main/main.cpp`'s `main()` gọi `usys_tee_wait_switch_req()` 2 lần liên tiếp NGAY ĐẦU,
chờ CA gửi task thật (`task_queue->inner_model_path`) rồi mới biết load model nào — **load
tensor (chạm TZASC) chỉ xảy ra khi có request inference thật, không tự động lúc boot**. Điều
này chứng minh ràng buộc "`mvm_ta` phải launch trước `llama-cli`" (§9.5) trên thực tế chỉ cần
đúng "trước khi `llama-cli` bắt đầu load tensor" — một cửa sổ thời gian RẤT rộng (yêu cầu
trigger bên ngoài thật), không phải "phải là SMC đầu tiên của toàn hệ thống" như thiết kế cũ
giả định.

Cũng xác nhận: không có TZASC index nào "rảnh" cho metanode — `TZASC_NR=4`, index 0-2 dùng
cho model tensor (`cma_index_counter % TZASC_NR_MODEL`), index 3 dành cho NPU scratch. Tăng
`TZASC_NR` để có index riêng là rủi ro cao (đụng 4 vị trí đồng bộ theo CLAUDE.md) — không
chọn hướng này.

**Thiết kế mới (thay thế hoàn toàn §9.18/9.19)**: bỏ hết vòng lặp mồi + 20 lần retry + verify
lồng nhau. Thay bằng 2 bước rõ ràng:
1. `mvm_wait_for_boot_settled()`: chờ AN TOÀN, không gọi bất kỳ SMC nào (chỉ poll
   `usys_map_tzasc_cma_meta()` — syscall thuần nội bộ, không rủi ro bị "nuốt") tới khi write
   toàn cục `g_tzasc_cma_meta_paddr` xuất hiện (bằng chứng Normal World đã tiến đủ xa), cộng
   thêm biên độ an toàn cố định (25s) v để chắc chắn vượt qua cửa sổ probe tối đa ~15s của
   `llm_tee_os_init()`.
2. `mvm_push_pages_resilient()`: CHỈ 1 lần gọi `push_pages()` thật, giám sát bởi ĐÚNG 1 luồng
   phụ (không phải retry loop) — nếu timeout hoặc "fake success" (đã biết 2 dạng: entry_index
   trùng 0 do handshake ON_DONE; map mảng toàn cục thành công nhưng entry riêng vẫn size=0) thì
   FATAL rõ ràng thay vì lặp lại — sau bước chờ ở trên, việc này được kỳ vọng hiếm khi xảy ra;
   nếu vẫn xảy ra thường xuyên, đó là tín hiệu cần điều tra tiếp, không phải che giấu bằng
   retry vô hạn.

Giữ nguyên tuyệt đối nguyên tắc tách biệt dự án: toàn bộ thay đổi chỉ nằm trong
`metanode/execution/pkg/mvm/ta/mvm_ta_main.cpp`, không đụng `chanmgr.c`/`tc_client_driver.c`/
`opteed/smc.c`. Việc đọc `llama-cli`'s `main.cpp` chỉ để hiểu cơ chế OS dùng chung, không
link/share code.

**Test trên phần cứng của thiết kế §9.20 (kMaxWaitSec cố định)**: THẤT BẠI 2 lần liên tiếp
cùng ngày — `kMaxWaitSec=90` hết giờ trước khi meta write kịp landed; tăng lên `240` build lại
thì bị người dùng chặn lại trước khi kịp flash/test, đúng lúc đó nhận phản hồi trực tiếp:
"cần xem xét thiết kế không phụ thuộc hardcode thời gian nào cả hệ thống đâu lương trước việc
delay bao lâu đâu" — tức: hệ thống không hề biết trước độ trễ thật là bao lâu, nên mọi hằng số
thời gian dùng để QUYẾT ĐỊNH THẤT BẠI (không phải để nhịp lại/log) đều là đoán mò về điều kiện
board/tải lúc chạy, không phải sự thật — đoán số lớn hơn sau khi đoán nhỏ thất bại chỉ trì hoãn
đúng lỗi đó, không sửa nó.

## §9.21 — Thiết kế lại: không dùng elapsed time làm căn cứ từ bỏ

Nguyên tắc sửa lại: elapsed wall-clock time chỉ được dùng để quyết định "thử lại" / "in
heartbeat cho dễ chẩn đoán" — KHÔNG BAO GIỜ dùng để quyết định "từ bỏ, FATAL abort". Chỉ tin
hai loại tín hiệu: (a) một kết quả DƯƠNG đã được xác minh độc lập (entry cụ thể của push này
thật sự có đúng size — §9.19), hoặc (b) một mã lỗi thật từ platform (không phải timeout).

Áp dụng cho 2 hàm trong `mvm_ta_main.cpp`:
- **`mvm_wait_for_boot_settled()`**: bỏ hẳn `kMaxWaitSec`/nhánh FATAL. Vòng lặp này chỉ poll
  `usys_map_tzasc_cma_meta()` — một syscall thuần secure-world-internal, ĐÃ XÁC NHẬN không hề
  phát SMC nên không có rủi ro bị swallow — nên hoàn toàn an toàn để chờ vô hạn định. Chỉ còn
  heartbeat log throttle mỗi 20s để chẩn đoán qua UART, không còn cap.
- **`mvm_push_pages_resilient()`**: đổi từ "1 lần thử + FATAL nếu watchdog hết giờ" sang vòng
  lặp thử lại vô hạn định với thread mới mỗi lần watchdog "hết giờ" (đổi tên biến từ
  `kWatchdogTimeoutSec` thành `kRetryPaceSec` để phản ánh đúng ngữ nghĩa: hết giờ = "thử lại",
  không phải "từ bỏ"). An toàn để retry vô hạn vì (§9.13): một request bị swallow thật sự
  KHÔNG có side-effect phía server (bị coi nhầm là boot handshake, không bao giờ chạm tới logic
  push_pages thật) — nên retry không bao giờ double-count hay hỏng dữ liệu; trường hợp xấu nhất
  là 1 attempt chậm (không thật sự bị swallow) cũng landed muộn sau đó, chỉ tốn 1 slot
  meta-array vô hại. Đồng thời bỏ luôn yêu cầu cứng `entry_index==0` (best-effort, không phải
  bất biến đúng-sai) — chỉ cảnh báo (WARNING, không abort) nếu khác 0, miễn slot đó được xác
  minh độc lập là genuine.

Các hằng số còn lại trong code (`kRetryPaceSec=15`, `kMetaVerifyAttempts=20`,
`kHeartbeatIntervalSec=20`) không phải là ngưỡng thất bại — chỉ là nhịp polling/log, sai số
ước lượng chỉ gây thử lại dư thừa vô hại, không bao giờ gây abort sai.

**Build**: biên dịch sạch qua `docker run` thủ công (image
`vectorxj0553/tz-llm-llama-builder:cpp13-metanode-deps`, mount thẳng `metanode/execution/pkg`
+ `tz-llm/tee_os_kernel/.../chcore-libc/musl-libc/install/include`), không TEXTREL, md5 khác
bản trước — promote vào `mvm_ta_output/`, đang chạy `rebuild.sh` → `repack.sh` → flash → test
trên phần cứng.

**Chưa test trên phần cứng** — đang build/flash lần đầu của thiết kế §9.21 này.

## §9.22 — §9.21 test thật: root-cause KHÁC hoàn toàn dự đoán (priority scheduling, không phải swallow race)

Test đầu tiên của §9.21: `mvm_wait_for_boot_settled()` (không cap thời gian) hit `>1800s` mà
`g_tzasc_cma_meta_paddr` **chưa từng landed** — vượt xa mọi lần quan sát trước (14-50s bình
thường). Kiểm tra kỹ: **hoàn toàn không có dòng log Linux/Normal World nào xuất hiện** suốt
thời gian đó. Ban đầu nghi UART/baud rate (đúng một phần — ModemManager re-enumerate reset
baud về 9600 sau mỗi lần board mất nguồn, xem memory `host-uart-baud-reset-gotcha` — đã tốn
nhiều thời gian điều tra nhầm hướng này trước khi tìm ra vấn đề thật).

**Root cause thật, xác nhận qua đọc kernel source + live evidence**: scheduler `pbrr`
(Priority-Based Round-Robin) là **ưu tiên tuyệt đối** — luồng priority cao hơn LUÔN thắng
luồng priority thấp hơn khi cả hai ready. `chanmgr/main.c`'s 16 luồng `idle()` tự hạ priority
xuống 1 (`usys_set_prio(0, 1)`) rồi lặp gọi `usys_tee_switch_req()` thật — **đây chính là cơ
chế duy nhất nhường CPU cho Normal World boot**. `mvm_wait_for_boot_settled()`'s vòng lặp
`usys_yield()` chạy ở priority mặc định (10, không set) — **triệt tiêu hoàn toàn** 16 luồng
priority-1 đó trên bất kỳ CPU nào nó rơi vào → Normal World không bao giờ được nhường CPU để
boot. Đây CHÍNH LÀ hiện tượng đã tự phát hiện và bỏ 1 lần trước đó (§9.11 mục 3: cap-90s spin
cũng tự gây bế tắc) — thiết kế §9.21 vô tình tái tạo lại đúng bug đó khi bỏ cap mà quên khắc
phục nguyên nhân gốc.

**Fix vòng 1 (thành công)**: `usys_set_prio(0, 1)` một lần ở đầu `mvm_wait_for_boot_settled()`,
khớp priority của `chanmgr`'s idle threads. Kết quả: Linux boot **13 giây**, driver handshake
(`llm_tee_os_init`) thành công **ngay lần thử đầu tiên** — cải thiện triệt để, xác nhận 5-6 lần
liên tiếp.

**Vấn đề mới lộ ra sau fix**: `mvm_ta` bản thân KHÔNG tiến triển được (không cả heartbeat) sau
20-30 phút — vì ở priority 1 nó cạnh tranh trực tiếp với chính 16 luồng idle đang bận đó. Thử
3 vòng sửa thêm, TẤT CẢ đều thất bại/không chứng minh được:
- **Toggle priority quanh mỗi lần check**: lỗi logic — hạ priority *trước khi* `usys_yield()`
  nên coi như không đổi gì (điều quyết định là priority *lúc yield*, không phải lúc đang chạy).
- **Toggle theo counter (1/1000 vòng ở priority thường)**: counter tự nó không đếm nổi vì
  luồng đói quá, vô dụng — không có cả heartbeat sau 5+ phút.
- **`nanosleep()`** (đã bỏ 1 lần rất sớm trong session, nghi treo): đọc lại kỹ kernel source
  (`kernel/irq/timer.c`: `sys_clock_nanosleep`/`enqueue_sleeper`/`sleep_timer_cb`) thấy cơ chế
  trông hợp lệ (per-CPU timer list, IRQ-driven, có lock chống race đã biết) — thử lại có căn
  cứ, nhưng **vẫn không chứng minh được hoạt động** (0 tiến triển sau 6+ phút, kể cả bản chẩn
  đoán in từng bước trước/sau lệnh gọi).

**Fix cuối (round 6, do người dùng chỉ định trực tiếp): `usys_tee_wait_switch_req()`** — cơ
chế SMC-based blocking mà `llama-cli` đã dùng thành công (proven). **CRASH KERNEL THẬT ngay
lần test đầu tiên**: `BUG: sys_tee_wait_switch_req:142 on (expr) percpu->waiting_thread` —
primitive này chỉ cho **đúng 1 luồng chờ mỗi CPU**; `mvm_ta` và `llama-cli` cùng chờ trên đó
→ `BUG_ON`, board treo cứng hoàn toàn (UART ngừng hẳn). Hazard này **đã được biết và tránh sẵn**
ở chỗ khác trong chính `mvm_ta_main.cpp` (`mvm_reverse_round_trip`/`mvm_ta_run`'s comment gọi
đây là "previously-hardware-crash-causing hazard") — chỉ là quên đối chiếu trước khi thử lại.
Xem memory `usys-tee-wait-switch-req-percpu-crash`. **Kết luận: không bao giờ dùng lại
primitive này từ `mvm_ta`.** Revert về fix vòng 1 (`usys_set_prio(0,1)` + `usys_yield()` thuần).

**Phát hiện kiến trúc quan trọng nhất, giải thích toàn bộ bí ẩn "chờ mãi không tiến triển"**:
đọc `mvm_ca_test.cpp`'s comment có sẵn (thêm 2026-08-18, trước cả session đang bàn) tiết lộ:
`push_pages()` (SMC do TA tự khởi tạo qua `usys_tee_switch_req(SMC_EXIT_SHADOW)`) **về mặt cấu
trúc không thể hoàn tất được cho tới khi có một luồng Normal-World đang chủ động lặp gọi ioctl
`LLM_CLIENT_IOCTL_RUN`** (qua `smc_call_cpu_resume()` trong `tzdriver`) — đây chính là job của
`mvm_ca_test.cpp`'s `ca_relay_thread()`. Nghĩa là: **chờ đợi bao lâu ở phía TA cũng vô ích nếu
không có ai phục vụ phía CA** — không phải vấn đề priority/scheduling như tưởng suốt session.
`llama-cli` có cơ chế tương đương riêng (`fake_ca.cpp`'s `ca_thread`) — **cùng dùng chung ioctl
`LLM_CLIENT_IOCTL_RUN`** — đây chính là nguồn gốc thật của MỌI xung đột quan sát được (kể cả
crash `usys_tee_wait_switch_req` ở trên): chạy `mvm_ca_test` trong khi `llama-cli` sống sẽ luôn
có nguy cơ SMC bị giao nhầm cho `llama-cli`, crash nó (`invalid prompt` → page fault → thoát).

## §9.23 — Fix kiến trúc triệt để: `mvm_launcher.srv` hoàn toàn độc lập khỏi `chanmgr`/`llama-cli`

Theo yêu cầu người dùng ("tạo hẳn riêng đi") — thay vì #ifdef bên trong `chanmgr/main.c`, tạo
**binary hoàn toàn mới**, launch **độc lập** ở tầng cao hơn:

1. **`chanmgr/main.c` revert 100% về nguyên trạng** — xoá sạch mọi code metanode từng thêm
   (launch `/mvm_ta`, `mvm_ta_waiter`). Không còn dấu vết nào của metanode trong file này nữa.
2. **`mvm_launcher.srv`** — binary mới, thư mục riêng
   (`tee_os_kernel/user/system-services/system-servers/mvm_launcher/`), chỉ làm 2 việc: spawn
   16 luồng idle priority-1 (copy y hệt pattern đã chứng minh của `chanmgr`, không link/share
   code) + `create_process("/mvm_ta")` + `waitpid()`. Không có logic channel/IPC nào của
   `chanmgr`, không biết gì về `llama-cli`.
3. **`procmgr.c`'s `boot_default_apps()`** — điểm launch tiến trình cấp cao nhất toàn hệ
   thống (nơi `chanmgr.srv` vốn được launch từ đó). Thêm cờ `#define METANODE_ONLY_BOOT`
   (hiện đang bật): khi bật, launch `mvm_launcher.srv` THAY VÌ `chanmgr.srv` — 2 deployment
   loại trừ lẫn nhau hoàn toàn, không còn ai cùng dùng `LLM_CLIENT_IOCTL_RUN` nữa. Đổi lại
   (comment `#define` đó) để build về chế độ `tz-llm` gốc (chanmgr+llama-cli) khi cần — pipeline
   build chưa có cơ chế truyền CFLAGS riêng theo target nên đây tạm thời là toggle thủ công tại
   1 chỗ duy nhất, có thể thay bằng build-flag thật sau.
4. `tee_os_kernel/Makefile`: đăng ký `mvm_launcher.srv` vào `USER_TARGETS`/
   `USER_TARGET_DIR_MAP`/`ramdisk:` dependency, theo đúng mẫu `chanmgr.srv`.

**Kết quả test trên phần cứng — THÀNH CÔNG HOÀN TOÀN, round-trip `MVM_TZ_CMD_EXECUTE` đầu
tiên trong lịch sử project**:
```
[procmgr] Launching mvm_launcher...
main 56: mvm_launcher main entry
main 74: launched metanode TA, pid=4
[mvm_ta] push_pages succeeded on attempt #3 (entry_index=0, entry.size verified)
[mvm_ta] channel ready: cma_index=1 entry_index=0 paddr=0x150000000 vaddr=0x300002aac000 size=0x401000
```
```
[mvm_ca_test] mapped OK. protocol_version=1 (want 1)
[mvm_ca_test] sending MVM_TZ_CMD_EXECUTE: sender=0x11..11 recipient=0x22..22 amount=100wei
[mvm_ca_test] reverse call cmd=2 header_len=136 blob_len=136
[mvm_ca_test] FATAL: unhandled reverse cmd=2 -- aborting cleanly instead of hanging mvm_ta forever
```
`push_pages()` mất 3 lần thử (2 lần đầu chưa có relay servicer, lần 3 mới có `mvm_ca_test`'s
relay chạy — khớp đúng phát hiện §9.22). Reverse cmd=2 chưa xử lý là giới hạn của
`mvm_ca_test` (công cụ test, không phải `mvm_ta`) — thoát sạch, không crash/treo `mvm_ta`.
Linux boot vẫn ổn định 13s, WiFi kết nối bình thường — `mvm_launcher.srv`'s launch không ảnh
hưởng gì tới boot flow chung.

## §9.24 — "reverse cmd=2" thực ra là bug race condition trong `mvm_ca_test.cpp`, không phải TA

Điều tra dòng `reverse call cmd=2` ở §9.23: **KHÔNG phải reverse call thật từ `mvm_ta`** — là
self-race trong chính `mvm_ca_test.cpp`'s dispatch loop. Cả `request_ready` VÀ `response_ready`
đều là flag DÙNG CHUNG 2 chiều (CA→TA và TA→CA) — nhưng vòng poll của CA (`for (round...) {
while (which<0) { ... } }`) tự CAS-consume các flag này ngay sau khi CHÍNH NÓ vừa set, KHÔNG
kiểm tra `direction` trước, nên tự đọc lại tín hiệu của chính mình:

1. **`request_ready` self-race**: CA set `request_ready=1` để gửi `MVM_TZ_CMD_EXECUTE` (dòng
   ~293) → vòng poll NGAY SAU đó tự CAS-consume flag đó trước khi `mvm_ta` kịp thấy → đọc
   `g_channel->cmd` vẫn là `MVM_TZ_CMD_EXECUTE=2` (giá trị CA vừa ghi, chưa bị `mvm_ta` ghi đè
   bằng ID reverse thật) → in nhầm "reverse call cmd=2". Hậu quả nghiêm trọng hơn cả log sai:
   `mvm_ta`'s dispatch loop (cũng CAS-consume `request_ready`) **không bao giờ thấy được** tín
   hiệu đó nữa (đã bị CA "đánh cắp") → `mvm_ta` treo vĩnh viễn chờ 1 request không bao giờ tới.
2. **`response_ready` self-race** (đối xứng, lộ ra sau khi fix #1): sau khi `handle_reverse_call()`
   trả lời 1 reverse call thật (set `response_ready=1`, `direction` vẫn là `TA_TO_HOST` vì
   không ai flip lại), vòng poll kế tiếp tự CAS-consume flag đó, thấy `direction==TA_TO_HOST`
   (không phải `HOST_TO_TA`) → `FATAL: response_ready set but direction=TA_TO_HOST -- protocol
   confusion`.

**Fix (cả 2, cùng pattern)**: PEEK `g_channel->direction` bằng atomic load **trước khi** thử
CAS-consume flag — chỉ tiêu thụ nếu `direction` xác nhận đây thật sự là tín hiệu từ phía bên
kia (`request_ready`: chỉ consume khi `direction==TA_TO_HOST`; `response_ready`: chỉ consume
khi `direction==HOST_TO_TA`). Nếu peek thấy `direction` không khớp — đó là tín hiệu CỦA CHÍNH
MÌNH chưa được bên kia tiêu thụ, để nguyên, tiếp tục poll.

**Kết quả sau fix — round-trip EVM execution HOÀN CHỈNH VÀ ĐÚNG kết quả** (build lại
`mvm_ca_test.cpp` bằng `aarch64-linux-gnu-g++ -std=c++17 -static -O2`, binary mới push lên
board qua `hdc file send`, test trên fresh boot):
```
=== ExecuteResult ===
status=1 exception=0 gas_used=0
add_balance_change: 0x2222...2222 += 100   (recipient)
sub_balance_change: 0x1111...1111 -= 100   (sender)
nonce_change:       0x1111...1111 nonce=1  (sender)
```
Đúng chuẩn native transfer EVM: trừ sender, cộng recipient, tăng nonce — 2 vòng reverse-call
`GLOBAL_STATE_GET` (cmd=101, cho sender lẫn recipient) xử lý sạch sẽ, không lỗi. **Đây là bằng
chứng đầu tiên cho thấy EVM interpreter thật của metanode chạy đúng bên trong TrustZone secure
world, qua toàn bộ pipeline CA↔TA.**

**CẬP NHẬT (cùng ngày): đủ 6/6 reverse-call đã có handler trong `mvm_ca_test.cpp`**

Thêm handler cho 4 lệnh còn thiếu (`EXTENSION_CALL_GET_API`/`EXTENSION_EXTRACT_JSON_FIELD`/
`EXTENSION_BLST` — chung 1 shape blob-only [output bytes]; `EXTENSION_GET_OR_CREATE_SIMPLE_DB`
— cũng blob-only theo `mvm_tz_protocol.h`) — mỗi lệnh trả về kết quả "rỗng nhưng hợp lệ" (4-byte
length-prefix = 0, đúng convention `Extension_return{nullptr,0}` = thất bại/không có dữ liệu)
thay vì fabricate dữ liệu giả. Test lại thành công round-trip **lần thứ 2 liên tiếp, KHÔNG cần
reboot board** — `mvm_ta`'s dispatch loop chính bền vững qua nhiều lệnh (`seq` tăng đúng 7→9→11),
kết quả giống hệt lần đầu.

**ĐÍNH CHÍNH (cùng ngày, sau khi đọc lại kỹ)**: đánh giá "Xapian-trong-TA chưa triển khai, rủi
ro cao nhất" ở trên **SAI** — đã bị lẫn với 1 phần khác của kế hoạch chưa đọc kỹ. Thực tế (đã
xác minh bằng build thật 2026-08-16, xem mục "Xapian: DB_BACKEND_INMEMORY" phía trên):
- Xapian dùng backend `InMemory` — không chạm filesystem — nên **không cần lệnh reverse-call
  proxy file I/O nào cả** (giả định ban đầu của GĐ1 là sai, đã tự sửa từ trước).
- `libxapian.a` đã cross-build sạch cho đúng toolchain chcore/musl, smoke test link thành công
  thật (không phải giả định).
- Việc còn thiếu thật sự CHỈ có: 1 lệnh reverse-callback mới
  (`MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS`, struct wire `mvm_tz_replay_full_db_logs_req_t` đã
  thiết kế sẵn) cho chiều "TA restart → nạp lại dữ liệu từ Host", cộng 1 hàm Go codec nhỏ
  (~30-40 dòng, theo đúng mẫu 9 lệnh lifecycle khác đã có). Chiều "lưu" (TA→Host) đã tự động
  hoạt động qua `encodeExecuteResult`/`block_processor_commit.go` có sẵn, không cần code mới.
- Rủi ro thật còn lại (chưa đo): 1 lỗ hổng hiệu năng Go-side (`GetStorageBackupDb()` tối ưu
  tra cứu theo block number, không tối ưu theo địa chỉ — cần thêm 1 index nhỏ).

## §9.25 — Test contract code thật: NULL pointer crash thật trong EVM interpreter

Viết test EXECUTE gọi contract thật (không cần `MVM_TZ_CMD_DEPLOY`, chưa wire trong `mvm_ta` —
giả lập "địa chỉ đã có code" ngay từ phía CA: `GlobalStateGet`'s response cho MỘT địa chỉ cụ
thể (`g_contract_addr`) trả `status=1` kèm bytecode thật trong blob, thay vì luôn trả
`status=0`). Bytecode dùng: `PUSH1 0x2a PUSH1 0x00 SSTORE PUSH1 0x00 SLOAD PUSH1 0x00 MSTORE
PUSH1 0x20 PUSH1 0x00 RETURN` (lưu 42 vào slot 0, đọc lại, trả về — chuẩn EVM đơn giản, không
đặc thù metanode).

**Kết quả**: `mvm_ca_test` gửi thành công code thật (`GlobalStateGet: returning REAL code (16
bytes)`), nhưng `mvm_ta` **crash NULL pointer thật** ngay sau đó:
```
handle_trans_fault: no vmr found for va 0x0!
do_page_fault: faulting ip is 0x3000005ed38c (real IP), faulting address is 0x0, fsc is trans_fault
Thread ... CMD: /mvm_ta
```
`mvm_launcher.srv` **chưa in "metanode TA exited"** — `waitpid()` chưa trả về, chưa rõ toàn bộ
process có bị kernel kill hẳn hay chỉ 1 luồng chết còn luồng khác vẫn "sống" ở mức OS (khớp
CLAUDE.md's cảnh báo: lỗi bên trong TA không luôn abort sạch).

**Chưa điều tra sâu** (cần session riêng, đụng core EVM interpreter C++ của metanode, không
nên vá vội): có thể do (a) bytecode/test tự viết thiếu field nào đó interpreter cần (ví dụ:
context/environment object chưa init đầy đủ khi không qua đường DEPLOY thật), (b) bug thật
trong interpreter khi xử lý SSTORE/SLOAD lần đầu trên 1 địa chỉ "giả lập có code", hoặc (c) vấn
đề trong cách `GlobalStateGet`'s response được `mvm_ta` parse (blob layout). Test 1 (native
transfer, không chạm contract code) vẫn chạy hoàn hảo trên CÙNG bản build — xác nhận bug chỉ
liên quan tới đường thực thi contract code, không phải hạ tầng channel/protocol.

**Việc tiếp theo**:
0. ~~Điều tra crash NULL pointer~~ **ĐÃ GIẢI QUYẾT — xem §9.27.**
1. ~~Viết `encodeReplayFullDbLogsReq`/handler tương ứng cho `MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS`~~
   **ĐÃ LÀM (§9.28)** — cơ chế reverse-call viết xong đầy đủ; auto-trigger vào interpreter vẫn
   là việc riêng, chưa làm (xem §9.28's ghi chú).
2. Thêm index địa chỉ nhỏ cho `GetStorageBackupDb()` (lỗ hổng hiệu năng đã biết).
3. ~~Viết test EXECUTE gọi contract code thật~~ **ĐÃ LÀM (§9.25)** — chạy đúng, xem §9.27.
4. ~~Tích hợp `libxapian.a`~~ **ĐÃ XONG TỪ TRƯỚC** (xác nhận qua `strings`/symbol check trên
   binary thật) — xác minh RUNTIME giờ không còn bị chặn bởi crash, có thể làm tiếp.
5. exercise storage/extension reverse-calls với dữ liệu THẬT (hiện tất cả đang trả về "empty
   nhưng hợp lệ" cho 4 lệnh extension) — có thể bắt đầu vì contract execution đã chạy được thật
   (bao gồm cả `GET_STORAGE_VALUE` cho SLOAD, xem §9.27).
6. **MỚI (§9.28)**: thiết kế + wire auto-trigger thật cho `mvm_fetch_and_replay_full_db_logs()`
   vào đúng điểm trong `xapian_handlers.cpp` (khi 1 địa chỉ được Xapian tra lần đầu trong phiên
   TA, chưa có dữ liệu cục bộ) + dedupe per-session (tránh round-trip lặp lại mỗi lần chạm cùng
   1 địa chỉ).

## §9.27 — Root cause thật của §9.25/§9.26: `saveDebugInfo()` ghi file khi TA không có filesystem

Tiếp tục thu hẹp bằng bracketing `fprintf(stderr,...)` trực tiếp trên hardware (không phải qua
rebuild-để-symbolicate, đã xác nhận là ngõ cụt ở §9.26) — 4 vòng build→flash→reboot liên tiếp,
mỗi vòng thu hẹp phạm vi crash xuống một tầng:

1. Bracket trong `mvm_ta_main.cpp` (`GlobalStateGet`/`round_trip`): crash xảy ra NGAY SAU khi
   `GlobalStateGet` trả về thành công `status=1 code_length=16` với pointer hợp lệ — tức là toàn
   bộ wire-marshal (`BlobWriter`/`BlobReader`) hoạt động đúng, loại được giả thuyết (b) của §9.26.
2. Bracket trong `linker/src/my_global_state.cpp`'s `MyGlobalState::get()` (nhánh `status==1`):
   TOÀN BỘ nhánh này chạy xong sạch (in tới tận `status==1 branch RETURN`) — bug KHÔNG nằm ở đây,
   nằm ở bước SAU khi hàm này return, bên trong `run()` (`mvm_linker.cpp`) gọi vào
   `_Processor::run()`.
3. Bracket trong `c_mvm/src/processor.cpp`'s `_Processor::run()`: in "about to enter dispatch
   loop, sm_size=16" thành công, nhưng KHÔNG bao giờ thấy "dispatch loop EXITED normally" —
   crash nằm bên trong vòng lặp dispatch, ngay ở lần gọi `dispatch()` ĐẦU TIÊN (opcode PUSH1).
   Vòng này bị nhiễu UART nặng do 1 print debug rác từ session khác (`[MVMDBG] sys_tee_switch_req
   entry`, thêm 2026-08-18 cho 1 điều tra khác đã đóng từ lâu, in ra ở MỌI lần SMC world-switch) —
   silence dòng này (comment, không xoá) trong `smc.c` mới đọc được log sạch ở vòng tiếp theo.
   Xem thêm memory `uart-diagnostic-noise-garbles-capture`.
4. Đọc `dispatch()` (`processor.cpp`): TRƯỚC switch-case xử lý opcode, có gọi
   `saveDebugInfo(tx, op, ctxt)` khi `tx.is_debug == true` — **đây chính là lệnh EVM đầu tiên,
   `mvm_ca_test.cpp` luôn set `req.is_debug = 1`**. Đọc `saveDebugInfo()`: làm I/O file THẬT —
   `std::filesystem::exists()`/`create_directories()`, `std::ofstream outFile(...)` ghi trace mỗi
   opcode vào `./tx_debug/*.log`. **Đây chính là bug**: TA không có filesystem POSIX (đã ghi rõ
   trong CLAUDE.md từ trước) — và đường code này CHƯA TỪNG được thực thi ở bất kỳ test nào trước
   đó, vì native-transfer-only `EXECUTE` không bao giờ vào tới vòng lặp `dispatch()` (exec_code
   rỗng, điều kiện vòng lặp `false` ngay từ đầu).

**Fix**: thêm `MVM_SetDebugFileLoggingEnabled(bool)` (khai báo `mvm_linker.hpp`, định nghĩa
`processor.cpp` bằng `static std::atomic<bool>` module-level, mặc định `true` — không đổi hành vi
đường cgo/Go hiện có). `saveDebugInfo()` early-return nếu bị tắt. `mvm_ta_main.cpp`'s `main()`
gọi hàm này với `false` ngay đầu, trước `mvm_channel_init()`/`mvm_ta_run()`.

**Xác nhận trên hardware, 2 lần liên tiếp** (build→flash→reboot riêng mỗi lần): lần 1 với các
print chẩn đoán còn nguyên, lần 2 sau khi dọn sạch print (giữ lại các print nhẹ hơn trong
`mvm_ta_main.cpp`, xoá hết print per-opcode/per-statement trong `processor.cpp`/
`my_global_state.cpp`) — cả 2 lần cho kết quả GIỐNG HỆT nhau:
```
=== ExecuteResult (contract call (SSTORE/SLOAD)) ===
status=0 exception=0 gas_used=20229
nonce_change_count=1 storage_change_count=1
storage_change: addr=3333...33 key=0x00 value=0x2a
[mvm_ca_test] DONE
```
`value=0x2a` (=42 decimal) khớp chính xác với bytecode test (`PUSH1 0x2a ... SSTORE`) — semantics
đúng, không chỉ "không crash". `cmd=102` (`GET_STORAGE_VALUE`, cho SLOAD) cũng lần đầu tiên xuất
hiện và trả lời đúng trong round-trip này. Mốc GĐ3 cốt lõi — thực thi contract code EVM thật qua
toàn bộ pipeline TrustZone TA — **đã đạt được và xác nhận**.

Chi tiết đầy đủ: memory `mvm-ta-evm-interpreter-nullptr-crash` (đã cập nhật thành RESOLVED).

## §9.26 — Thu hẹp root cause của §9.25: dựng lại harness x86 in-process, loại 4 giả thuyết

Thử symbolicate `faulting IP 0x3000005ed38c` bằng cách rebuild `mvm_ta` không strip trong cùng
container — **ngõ cụt thật sự**: dù giữ nguyên `-DCMAKE_BUILD_TYPE=Release` (chỉ thêm `-g`),
`.text` của bản rebuild lệch bản đã deploy ngay từ offset `0x24` (khác byte đầu tiên) — rebuild
không reproducible bit-for-bit (nghi do thứ tự member trong static archive không determinist),
nên không thể tra ngược địa chỉ crash qua rebuild. Bài học: muốn symbolicate thật, phải thêm
`-g`/bỏ strip **ngay trong lần build gốc** (sửa `build_mvm_ta.sh`), không thể làm sau.

**Hướng nhanh hơn nhiều**: dựng harness x86 in-process gọi thẳng `execute()` — không cần TA,
không cần board, không cần cross-toolchain, vì bug nằm trong C++ thuần
(`libmvm_linker.a`/`c_mvm`), không phải logic riêng cho chcore. Link thẳng vào
`linker/build/lib/static/libmvm_linker.a` + `c_mvm/build/lib/static/libmvm.a` (bản x86 in-tree,
build Release+`-g` mặc định của cgo path — không cần rebuild) + `libsecp256k1`/`libblst` x86 có
sẵn trên máy. File: `metanode/execution/pkg/mvm/ta/host_repro/host_repro.cpp` (README riêng
trong cùng thư mục).

**Kết quả — KHÔNG tái hiện được crash**: gọi `execute()` với đúng bytecode + `GlobalStateGet`
fabrication y hệt `mvm_ca_test.cpp`, kể cả đúng thứ tự 2 lần gọi (native transfer trước, contract
call sau, cùng process/mvm_id) — chạy sạch dưới gdb, `exitReason=0 exception=0`. Loại được, là
nguyên nhân DUY NHẤT:
- Bug logic chung trong interpreter khi xử lý SSTORE/SLOAD.
- Corruption state singleton giữa 2 lần gọi `execute()` liên tiếp cùng process.
- Xapian (xác nhận thêm: `my_storage.cpp`'s đường SSTORE/SLOAD zero reference tới Xapian —
  chưa từng là giả thuyết sống được sau khi kiểm tra).
- Stack size nhỏ trên TA: `chcore/defs.h`'s `MAIN_THREAD_STACK_SIZE`/
  `CHCORE_PTHREAD_DEFAULT_STACK_SIZE` đều resolve 8MB trên build 64-bit — cùng bậc với default
  của glibc, không phải "TA có stack nhỏ hơn" như nghi ngờ ban đầu.

**Còn lại, CHƯA test**: (a) codegen riêng của musl-gcc (GCC11 cross) tại đúng điểm crash — harness
x86 không bắt được bug riêng của compiler cross; (b) chính code wire-marshal
(`BlobWriter`/`BlobReader`) trong `mvm_ta_main.cpp` — harness dùng stub trả dữ liệu trực tiếp,
bỏ qua hoàn toàn đường marshal thật đó (dù code này abort() rõ ràng khi lệch size, không phải
kiểu lỗi im lặng NULL-deref, nên xác suất thấp hơn). Bước tiếp theo cần: hoặc thêm `printf` chẩn
đoán quanh reverse-round-trip trong `mvm_ta_main.cpp` rồi build→flash→reboot 1 lần nữa trên board
thật, hoặc disassemble trực tiếp object cross-build tại điểm crash — không việc nào làm tiếp
được chỉ bằng x86.

## §9.28 — Viết cơ chế reverse-call cho `MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS` (mục 1 cũ)

Go-side codec cho cmd này (`encodeGetLatestFullDbLogsReq`/`decodeGetLatestFullDbLogsReq` +
`encodeReplayFullDbLogsResp`/`decodeReplayFullDbLogsResp`, `tz_codec.go`) hoá ra **đã viết sẵn
từ trước** (2026-08-16), kèm test round-trip (`tz_codec_full_db_logs_test.go`) — không cần làm
lại. Phần thật sự thiếu: **TA-side handler** (`mvm_ta_main.cpp`) chưa hề gọi tới cmd này (0 kết
quả grep) và `mvm_ca_test.cpp` cũng chưa xử lý (sẽ rơi vào nhánh FATAL nếu có ai gửi).

**Đã viết**: `mvm_fetch_and_replay_full_db_logs(const unsigned char address[20])` trong
`mvm_ta_main.cpp` — build request (`mvm_tz_get_latest_full_db_logs_req_t`, chỉ 20-byte address),
gửi round trip, decode response (tái dùng shape `mvm_tz_replay_full_db_logs_req_t`: header
`entry_count` + blob `entry_count` bản ghi `[20-byte address RAW, không length-prefix][log_len
u32][log bytes]` — khớp chính xác `tz_codec.go`'s `writeAddrBytesMap`, KHÔNG dùng quy ước
length-prefix-cho-mọi-field thông thường của `BlobReader::readBytes`), dựng mảng
`LogReplayEntryC[]`, gọi `ReplayFullDbLogs()` — y hệt cách cgo mode dùng `mvm.CallReplayFullDbLogs`
lúc node sync. Trả về giá trị `ReplayFullDbLogs` (khác 0 = thành công), hoặc `1` (thành công,
không có gì để replay) nếu `entry_count==0` — KHÔNG phải lỗi.

Thêm case tương ứng trong `mvm_ca_test.cpp`'s `handle_reverse_call()` cho cmd=107: trả về
`entry_count=0` (hợp lệ, giống style 4 case extension stub trước đó).

**Cố ý CHƯA làm** (khác các reverse-call khác): auto-trigger tự động vào interpreter. Comment
protocol header mô tả điều kiện gọi là "lần đầu trong phiên TA mà 1 tra cứu Xapian cho địa chỉ
này ra rỗng" — cần lần theo đúng chỗ trong `xapian_handlers.cpp` (2096 dòng, DB Xapian
per-address được mở/tra ở đâu) + thêm dedupe theo phiên (tránh gọi lại mỗi lần chạm cùng địa
chỉ) — đây là quyết định thiết kế thật, không nên đoán bừa. Hàm viết xong hoàn toàn ĐÚNG giao
thức và sẵn sàng gọi, chỉ chưa có nơi gọi.

**Xác nhận build**: compile sạch cả 2 phía (musl-gcc cross cho `mvm_ta_main.cpp`, thật trong
container; `aarch64-linux-gnu-g++` cho `mvm_ca_test.cpp`, local). Link đầy đủ `mvm_ta` (dùng lại
`libmvm_linker.a`/`libmvm.a` không đổi từ build trước) ra **md5 giống hệt bit-for-bit** bản đang
chạy trên board (`d76124f4f8878c17bd21ea43dd5b9f47`) — vì hàm mới là `static`, chưa ai gọi, GCC
`-O2` loại bỏ hoàn toàn (dead code elimination thật, không phải suy đoán). Do đó **không cần
flash lại** — board hiện tại đã chạy code tương đương tuyệt đối. Không cần vòng build→flash→
reboot nào cho thay đổi này.

### §9.28 follow-up (cùng ngày) — Storage reverse-call với dữ liệu THẬT (mục 5 "Việc tiếp theo")

Được hỏi phạm vi trước khi làm (5 lệnh storage/extension đòi hỏi mức công sức rất khác nhau —
extension cần bytecode CALL + ABI encode/decode thật + `CALL_GET_API` cần gọi HTTP thật ra
ngoài) — chọn làm `GET_STORAGE_VALUE` trước (an toàn, không network, không ABI), 4 lệnh
extension để riêng cho vòng sau khi đã rõ `argument_encode`'s format thật.

Thêm địa chỉ test thứ 3, `g_storage_test_addr` (0x44 lặp 20 lần), bytecode bare SLOAD (không
SSTORE trước — khác hẳn test 2's SSTORE-rồi-SLOAD-trong-cùng-tx, vốn không bao giờ rời khỏi
cache in-process của `mvm_ta`, không thật sự exercise round-trip). `GetStorageValue` case trong
`mvm_ca_test.cpp` giờ check `(address, key)`: khớp `(g_storage_test_addr, slot 0)` → trả
`status=0` (SUCCESS) kèm giá trị thật `0x1337`; mọi `(address,key)` khác giữ nguyên `NOT_FOUND`
(không phá 2 test cũ). Cũng sửa `run_execute_and_print()` để in luôn RETURN output bytes (trước
đây parse rồi bỏ, `(void)output_len`) — cần thiết để verify bằng mắt giá trị trả về đúng.

**Xác nhận trên hardware**: `GetStorageValue: returning REAL value 0x1337` → RETURN output =
`...1337` khớp chính xác. 2 test cũ (native transfer, SSTORE/SLOAD) vẫn `status=0 exception=0`
không đổi — không regression. Chỉ cần build lại `mvm_ca_test` (aarch64-linux-gnu-g++, không đụng
`mvm_ta`) + push qua `hdc` + chạy trên board đang chạy sẵn — **không cần flash/reboot** vì thay
đổi hoàn toàn nằm ở phía test tool, không phải TA.

## §9.29 — Fix Xapian InMemory backend cho TA (chuẩn bị cho auto-trigger §9.28 mục 6)

Trước khi wire auto-trigger cho `mvm_fetch_and_replay_full_db_logs()`, phát hiện `XapianManager`
chưa từng thực sự hỗ trợ chạy trong TA: constructor luôn mở DB qua path thật trên đĩa
(`Xapian::WritableDatabase(path, DB_CREATE_OR_OPEN)`), không có nhánh InMemory nào trong code
ứng dụng thật (`grep DB_BACKEND_INMEMORY` ra 0 kết quả trong `execution/`) — "InMemory đã proven"
ghi nhận 2026-08-16 chỉ là smoke test độc lập, chưa nối vào `XapianManager` thật.

**Fix 3 lớp** (`xapian_manager.cpp`/`.h`, `my_extension/utils.h`/`.cpp`):
1. Constructor: `openXapianDb()` chọn `Xapian::WritableDatabase(std::string(),
   DB_BACKEND_INMEMORY)` khi `IsXapianBasePathEmpty()` (TA không bao giờ gọi `SetXapianBasePath`),
   giữ nguyên nhánh path-based khi có base path (cgo/Linux thường).
2. `acquireSearchDb()`/`acquireSimpleReadDb()`: pool nhiều handle đọc đồng thời (mở lại cùng
   path) không có ý nghĩa với InMemory (không có path) và cũng không cần (TA tuần tự hoá, không
   bao giờ đọc đồng thời) — bypass thẳng `&db` trong chế độ InMemory.
3. `revertUncommittedChanges()`: **phát hiện kiến trúc cứng, xác nhận qua đọc source Xapian thật**
   — `InMemoryDatabase::commit()`/`::cancel()` đều là no-op tuyệt đối, `close()` xoá sạch dữ liệu
   vĩnh viễn — cách revert cũ (đóng+mở lại từ đĩa) KHÔNG THỂ áp dụng cho InMemory bằng API Xapian.
   Thêm `undo_snapshot_` (pre-image từng docid, capture lười lần đầu chạm trong `replay_log()`),
   revert InMemory tự tay replay ngược lại thay vì đóng/mở.

**Xác nhận trên x86 rất kỹ** (cả 2 chế độ trong 1 harness, `xapian_inmemory_test.cpp`,
scratchpad-only): InMemory mode (open/ghi/đọc-trước-commit/revert-xoá-đúng/commit-rồi-revert-vẫn-
còn — pass hết) VÀ disk mode (xác nhận, bằng cách so với code GỐC qua `git stash`, rằng 1 hành vi
"get_data() trước commit không thấy gì qua reader pool" là đặc điểm CÓ SẴN từ trước của Glass
backend, không phải regression).

**Trên hardware thật: THẤT BẠI, chưa root-cause, đã gỡ bỏ**. Thêm self-test tạm
(`mvm_ta_xapian_inmemory_selftest()`, cùng logic x86) chạy lúc TA khởi động — **crash `mvm_ta`
NGAY LẬP TỨC mỗi lần boot** (`faulting address 0x82`, IP nằm ở vùng nhớ `0x400000062000-
4000000d0000` — KHÁC hẳn mọi crash khác trong session này, vốn luôn `0x300...`). Ban đầu tưởng
nghiêm trọng hơn nhiều (board im lặng hoàn toàn UART/HDMI đen sau lần flash đầu) — chạy
`recover-golden-image.sh` + reflash lại ĐÚNG bản đó tái hiện crash y hệt, sạch sẽ (xác nhận đây
là bug thật, xác định, không phải hỏng flash/idbloader; lần "im lặng hoàn toàn" trước đó hoá ra
là sự cố rời rạc không liên quan — lần power-cycle NGAY SAU đó boot U-Boot/kernel/Linux bình
thường, chỉ riêng `mvm_ta` crash). **Chưa xác định được root cause** — gỡ bỏ hẳn self-test (hàm +
call site + include tạm) để đưa board về trạng thái ổn định đã biết thay vì tiếp tục điều tra
trong cùng phiên; xác nhận 3 test cũ (native transfer, SSTORE/SLOAD, GET_STORAGE_VALUE thật) vẫn
chạy đúng với fix 3-lớp còn nguyên, chỉ bỏ self-test.

**Phát hiện phụ**: 6 tiến trình `cat /dev/ttyUSB0` cũ chồng chéo từ các lần capture trước (chưa
bao giờ bị kill) từng làm 1 lần capture tưởng "board im lặng hoàn toàn" — thật ra chỉ là các
process cũ tranh nhau đọc byte. `pkill -f "cat /dev/ttyUSB0"` trước MỌI lần bắt log mới. Xem
memory `zombie-uart-readers-steal-bytes`.

Chi tiết đầy đủ: memory `xapian-inmemory-ta-backend-fix`.

**Việc tiếp theo cho fix Xapian**: KHÔNG coi fix này là "đã xác nhận trên hardware" — core fix
(constructor/pool/revert) tin là đúng (khớp x86 tuyệt đối) nhưng CHƯA có bằng chứng thật trên
musl/aarch64. Trước khi thử lại self-test: bracket TỪNG lệnh gọi Xapian riêng lẻ (không chỉ
trước/sau cả hàm), và cân nhắc khả năng lỗi nằm ở chính `mvm::Address`/`mvm::from_big_endian`
(chưa từng dùng đúng shape này trên TA trước đây) chứ không phải Xapian.

## §9.30 — Root cause thật của crash §9.29: lệch phiên bản GCC giữa `libxapian.a` và phần còn lại của TA (2026-08-20)

Thêm lại self-test (round 2) với bracket `fprintf`/`fflush` quanh TỪNG lệnh gọi Xapian riêng lẻ
(không chỉ quanh cả hàm như round 1) — tái hiện đúng lớp crash cũ, nhưng lần này định vị chính
xác được: `terminate called after throwing an instance of 'Xapian::DocNotFoundError'` rồi
`Unsupported syscall 130, bye.` (chcore/musl chưa cài đặt đầy đủ đường `terminate`/`abort`, nên
1 exception C++ không bắt được lộ ra ngoài trông y hệt crash NULL-deref). Định vị chính xác về
`xapian_manager.cpp`'s `get_overlayed_document()` (~dòng 679): `throw
Xapian::DocNotFoundError("Document not found");` — throw trong chính source của metanode, được
bọc ngay 1 tầng trên bởi `catch (const Xapian::DocNotFoundError&)` GIỐNG HỆT về mặt chữ trong
`get_data()` — vậy mà vẫn thoát ra không bắt được.

**Root cause**: `libxapian.a` (+`libtbb.a`/`libz.a`) được cross-build bằng toolchain GCC
13.3.0-era (đã cảnh báo là rủi ro mở, chưa xử lý, trong build note 2026-08-17 của chính project
này), trong khi toàn bộ phần còn lại link vào `mvm_ta` (c_mvm, linker, `mvm_ta_main.cpp`) dùng
GCC 11.5.0 qua `musl-gcc`. Dựng `Xapian::DocNotFoundError` gọi vào class cơ sở `Xapian::Error`
được biên dịch bởi GCC13 của chính Xapian; so khớp RTTI/`type_info` của 1 `catch` biên dịch bởi
GCC11 với object được throw đó có thể thất bại qua ranh giới ABI xử lý exception khác major
version GCC như vậy, dù kiểu ở mức source giống hệt nhau cả 2 phía. Đây KHÔNG phải bug logic của
fix 3-lớp — là lệch toolchain ảnh hưởng MỌI exception do Xapian ném ra, không riêng gì lệnh này.

**Fix thật** (chưa làm, xác định là việc riêng, lớn, phạm vi tách biệt): rebuild
`libxapian.a`/`libtbb.a`/`libz.a` bằng đúng thế hệ GCC 11.5.0 dùng cho phần còn lại của TA. Cho
tới khi đó, không wire bất kỳ đường đi nào để Xapian tự throw và trông đợi catch hoạt động — điều
này cũng chặn auto-trigger `MVM_TZ_RCMD_GET_LATEST_FULL_DB_LOGS` (§9.28 mục 6), vì phân biệt
"found"/"not found" qua Xapian dựa đúng vào cơ chế này.

Gỡ self-test lần 2 sau khi root-cause xong, rebuild+reflash board về lại trạng thái ổn định (fix
3-lớp còn nguyên, không có self-test) — xem `DEPLOYED_STATE.md` cho checkpoint xác nhận sạch.
Chi tiết đầy đủ + cách áp dụng: memory `xapian-inmemory-ta-backend-fix` (đã cập nhật).

## §9.32 — Round 3 (thử fix thật): rebuild `libtbb.a` bằng GCC 11.5.0 — BẤT ỔN trên hardware, REVERT, chưa kết luận

Thử làm fix thật cho §9.30. Phát hiện quan trọng khi kiểm tra lại: đọc `.comment` section thật
(`readelf -p .comment`) của `libxapian.a`/`libz.a` đang dùng cho thấy chúng **ĐÃ LÀ GCC 11.4.0**
từ trước (round-1 TEXTREL fix 2026-08-17, `xapian_zlib_pic_rebuild/build_pic.sh`, dùng đúng
musl-gcc thật) — đính chính lại §9.30's kết luận (dựa vào doc cũ, không tự kiểm tra) rằng cả 3 lib
đều GCC13. **Chỉ `libtbb.a` còn lệch GCC 13.3.0.** Vì `xapian_manager.h`/`xapian_registry.h`/
`state.h` đều dùng thẳng `tbb::concurrent_hash_map`, TBB lệch version là ứng viên hợp lý hơn cho
crash exception-ABI đã thấy, không nhất thiết phải là Xapian tự nó.

Rebuild `libtbb.a` (oneTBB 2021.11.0, source vẫn còn nguyên trong scratchpad phiên trước) bằng
đúng toolchain GCC 11.5.0 (`/home/pi/musl-cross-build-scratch-gcc11`, cùng toolchain đã stage
header libstdc++ cho build này) — build sạch, `.comment` xác nhận GCC 11.5.0, PIC/no-TEXTREL xác
nhận qua `-Wl,-z,text` shared-object link, `mvm_ta` relink thành công. Thêm self-test round 3
(bracket per-call như round 2) tái hiện đúng kịch bản x86-proven (getInstance/new_document/
commitBufferForTxHash/get_data-trước-revert/revertUncommittedChanges/get_data-sau-revert — lệnh
cuối chính là lệnh crash ở round 2) để kiểm chứng thật.

**Kết quả: BẤT ỔN trên hardware, KHÔNG kết luận được, đã REVERT.** Flash round-3 lần đầu → board
im lặng hoàn toàn → hoá ra idbloader/GPT hỏng (`[Vendor ERROR]: Boot device type is invalid!`) →
`recover-golden-image.sh` sửa xong → power-cycle lần 1: boot thật sự tiến xa (DDR → U-Boot → ATF →
OP-TEE → `[ChCore] create initial thread done`) rồi KẸT tại `SYS_rt_sigprocmask` → 2 lần cắm
nguồn tiếp theo: im lặng hoàn toàn (tệ hơn, không cả tới DDR banner). So sánh FIT sub-image xác
nhận U-Boot/ATF/fdt giống hệt bản trước, chỉ optee (chứa mvm_ta+libtbb.a mới) khác.

**Đánh giá nguyên nhân**: điểm kẹt xảy ra RẤT SỚM trong ChCore kernel — trước cả khi `chanmgr`
launch `mvm_ta`. Về cơ chế, `libtbb.a`/`mvm_ta`/self-test round 3 không thể là nguyên nhân trực
tiếp ở giai đoạn này (chưa chạy tới). Khớp hơn với rủi ro kênh flash USB flaky đã biết từ trước
(tz-llm-trustzone's CLAUDE.md: ~10-20% lỗi mỗi lần ghi; majority-vote-3 chỉ bảo vệ phần
uboot/boot_linux, không bảo vệ idbloader/GPT). Revert checkpoints về bản round-2 ổn định trước đó,
flash lại qua đúng kênh đó → boot sạch ngay lần đầu, uptime 7+ phút xác nhận qua `hdc` — củng cố
(không chứng minh tuyệt đối, mới 1 lần thử) giả thuyết kênh-flash hơn lỗi nội dung.

**Theo yêu cầu người dùng: dừng lại, không thử lại round-3 trong phiên này.** `libtbb.a` GCC11.5.0
đã build sẵn, giữ làm artifact cho lần thử sau
(`scripts/kick-the-tires/cpp13-metanode-deps/libtbb.a` hiện là bản GCC11.5.0, backup GCC13.3.0 ở
`libtbb.a.gcc13-backup` cùng thư mục) — nhưng board hiện tại đang chạy bản round-2 (không có fix
TBB, không có self-test). Xem `tz-llm-trustzone/DEPLOYED_STATE.md` cho trạng thái chính xác.

**Việc tiếp theo nếu quay lại**: thử lại round-3 với biện pháp an toàn hơn — verify md5 ngay sau
flash trước khi power-cycle (loại trừ ghi lỗi độc lập với hành vi boot), và/hoặc thử flash round-3
nhiều lần để xem tỷ lệ thất bại có khớp với ~10-20% kênh-flash đã biết hay cao bất thường (nếu cao
bất thường mới nên nghi ngờ lại nội dung).

## §9.31 — Extension reverse-call đầu tiên với dữ liệu THẬT: `GET_OR_CREATE_SIMPLE_DB` (2026-08-20)

Trong 4 lệnh extension reverse-call còn lại (`CALL_GET_API`/`EXTRACT_JSON_FIELD`/`BLST`/
`GET_OR_CREATE_SIMPLE_DB` — wire protocol + handler TA-side đã có sẵn từ round "6/6 reverse-call"
trước đây, nhưng luôn trả stub rỗng vì chưa có test contract nào thực sự gọi tới), chọn
`GET_OR_CREATE_SIMPLE_DB` làm cái đầu tiên: đơn giản nhất (chỉ ABI decode/encode + 1 map giả lập
phía Host, không crypto/JSON, và — quan trọng — **không đụng HTTP thật** (theo yêu cầu người
dùng: "chưa cần làm call http thật đâu nhé", để dành `CALL_GET_API` cho sau) lẫn không đụng
Xapian của TA (đây là reverse-call thuần, Host tự lo lưu trữ; `SIMPLE_DATABASE_ADDRESS` khác hẳn
`FULL_DATABASE_ADDRESS`/`xapian_handlers.cpp` — không bị chặn bởi bug ABI GCC ở §9.30).

**Cách làm** (toàn bộ nằm ở `mvm_ca_test.cpp`, phía CA-test — KHÔNG đụng `mvm_ta`, nên không cần
rebuild/flash):
1. Contract address thứ 3 (`g_simpledb_test_addr`, 0x55×20) với bytecode "calldata forwarder"
   generic (CALLDATACOPY → CALL tới precompile 261 = `SIMPLE_DATABASE_ADDRESS` với chính calldata
   của tx → RETURNDATACOPY → RETURN) — không cần Solidity/`solc`, hand-assemble 22 opcode, tái
   dùng được cho cả `set(...)` lẫn `get(...)` chỉ bằng cách đổi calldata mỗi lần EXECUTE (test
   harness đã hỗ trợ input/input_len thật từ trước, chỉ 2 test cũ chưa dùng tới).
2. Mini ABI codec tự viết (không dùng thư viện ngoài) cho `set(string,string,string) returns
   (bool)` / `get(string,string) returns (string)` — đúng chuẩn go-ethereum ABI mà
   `extension.go`'s `ExtensionGetOrCreateSimpleDb` (bản cgo thật) cũng decode/encode qua
   `abi.MethodById`/`Inputs.Unpack`/`Outputs.Pack`. Selector lấy qua `cast sig` (foundry) thật,
   không đoán: `set(string,string,string)` = `0xda465d74`, `get(string,string)` = `0x3e10510b`.
   Giới hạn: chỉ string ≤32 byte (đủ cho test, không phải codec ABI tổng quát).
3. `handle_reverse_call()`'s case `MVM_TZ_RCMD_EXTENSION_GET_OR_CREATE_SIMPLE_DB`: decode selector
   + args thật từ blob (raw ABI calldata, không có length-prefix riêng — khác với
   `writeBytes`-style của các lệnh khác, xác nhận qua đọc `mvm_reverse_round_trip`'s code thật
   thay vì đoán theo doc comment cũ của header, vốn mô tả sai là "length-prefixed"), lưu vào
   `g_fake_simpledb` (map trong tiến trình, đứng vai Host thật), trả response ABI-encode thật.
4. Phát hiện phụ: precompile 261 có write-protection check
   (`ctxt->read_only || !gs.is_cache()`) áp dụng cho MỌI lời gọi (kể cả `get()`, không chỉ
   `set()`) — `run_execute_and_print()` trước đó luôn hardcode `is_cache=0`; thêm tham số
   `is_cache` (mặc định `false`, không đổi 3 test cũ) để test mới truyền `true`.

**Xác nhận trên hardware, không cần reboot/flash** (chỉ build lại `mvm_ca_test` bằng
`aarch64-linux-gnu-g++ -static`, push qua `hdc`, chạy trực tiếp trên board đang chạy sẵn):
- SET: reverse call cmd=106 blob_len=292 (khớp đúng tính toán ABI head+tail), lưu
  `db1/key1/hello_ta`, RETURN = 32 byte ABI `bool(true)`.
- GET: reverse call cmd=106 blob_len=196 (khớp tính toán), tìm thấy, RETURN = 96 byte ABI
  `string` đúng offset(0x20)+length(8)+`"hello_ta"` — decode hex xác nhận khớp chính xác giá trị
  vừa SET.

**Việc tiếp theo**: BLST (deterministic, có `libblst.a` sẵn) hoặc EXTRACT_JSON_FIELD tiếp theo;
CALL_GET_API (cần gọi HTTP thật) để lại sau cùng theo đúng yêu cầu người dùng.

### §9.31 follow-up (cùng ngày) — BLST extension reverse-call với dữ liệu THẬT

Lệnh thứ 2 trong 4 lệnh extension. Xác thực chữ ký BLS12-381 THẬT qua `libblst` (không phải giả
lập/mock) — khớp chính xác scheme metanode dùng thật (`pkg/bls/bls.go`'s `VerifySign`:
min-pubkey-size, pubkey G1 nén 48 byte, chữ ký G2 nén 96 byte, cùng DST
`BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_`).

**Cách làm**:
1. Địa chỉ contract thứ 4 (`g_blst_test_addr`, 0x66×20) — cùng mẫu "calldata forwarder" như
   SimpleDb, chỉ đổi target sang precompile 259 (BLST).
2. Test vector THẬT (không phải blob random): dùng chính source `pkg/bls/blst` đã vendor sẵn
   trong repo metanode, build 1 tool offline trên x86_64 host (`gen_vector.cpp`, scratchpad-only,
   không commit) để tạo sk/pk/sig/msg qua `blst_keygen`/`blst_sk_to_pk_in_g1`/`blst_hash_to_g2`/
   `blst_sign_pk_in_g1`, tự verify lại bằng chính `blst_core_verify_pk_in_g1` trước khi in ra —
   đảm bảo vector nhúng vào `mvm_ca_test.cpp` là chữ ký hợp lệ thật, không phải đoán.
3. `abi_encode_bytes_args`/`abi_decode_bytes_args` mới (khác với
   `abi_encode_string_args`/`abi_decode_string_args` của SimpleDb) — hỗ trợ arg nhiều chunk
   (>32 byte, cần cho chữ ký 96 byte), cùng nguyên lý ABI offset+length+pad. Selector
   `verifySign(bytes,bytes,bytes)` = `0xee57fa59` (qua `cast sig` thật, khớp `extension.go`'s
   `blstAbiStr`).
4. `handle_reverse_call()`'s case `MVM_TZ_RCMD_EXTENSION_BLST`: decode ABI thật, gọi
   `blst_verify_sign()` (wrap `blst_p1_uncompress`/`blst_p2_uncompress`/`blst_p2_affine_in_g2`
   group-check/`blst_core_verify_pk_in_g1` — đúng thứ tự semantics của Go's
   `VerifyCompressed(sigGroupcheck=true, pkValidate=false)`), trả ABI `bool` thật.
5. **Phát hiện kỹ thuật đáng chú ý**: `libblst.a` cross-build cho aarch64/musl (dùng cho TA,
   `cpp11-stage-extracted/aarch64/3rdparty/lib/libblst.a`) **link sạch vào binary aarch64/glibc**
   (`aarch64-linux-gnu-g++ -static`) — xác nhận qua `nm -u` trước khi thử: blst hoàn toàn không
   phụ thuộc libc (chỉ gọi symbol nội bộ của chính nó), nên không bị vấn đề lệch ABI như Xapian ở
   §9.30 (Xapian ném C++ exception xuyên biên GCC-version; blst không dùng exception/RTTI C++ ở
   tầng C API này).

**Xác nhận trên hardware, không cần reboot/flash** (chỉ CA-test side): reverse call cmd=105
blob_len=420 (khớp tính toán ABI: 4+96+64+128+64), `BLST verifySign: pk=48B sig=96B msg=46B ->
VALID`, RETURN = 32 byte ABI `bool(true)`.

**Còn lại**: EXTRACT_JSON_FIELD (cần JSON parser thật phía CA-test); CALL_GET_API (để sau, chưa
cần HTTP thật theo yêu cầu người dùng).

### §9.31 follow-up 2 (cùng ngày) — EXTRACT_JSON_FIELD extension reverse-call với dữ liệu THẬT

Lệnh thứ 3 trong 4 lệnh extension. Khác 2 lệnh trước: `extension.go`'s `ExtensionExtractJsonField`
thật KHÔNG dùng go-ethereum's `abi` package (không verify selector qua `MethodById`) — chỉ tự
decode qua `argument_encode.DecodeStringInput(bCallData[4:], idx)`, nhưng encoding dây vẫn đúng
chuẩn ABI string (offset+length+pad) hệt các lệnh khác — xác nhận qua đọc source Go thật, không
đoán.

**Cách làm**:
1. Địa chỉ contract thứ 5 (`g_json_test_addr`, 0x77×20) — cùng mẫu forwarder, target precompile
   258 (`EXTRACT_JSON_FIELD_EXTENSION`).
2. `json_extract_flat_field()` — JSON extractor tối giản tự viết (không dùng thư viện ngoài,
   khớp chủ trương KISS/YAGNI của dự án cho 1 test tool chẩn đoán): chỉ parse object JSON phẳng
   1 tầng, format giá trị đúng quy tắc Go thật (`fmt.Sprintf("%v", ...)`: string trả nguyên văn
   không dấu ngoặc kép, bool → `"1"`/`"0"` theo đúng remap tường minh trong `extension.go`, số →
   literal text). Không phải parser JSON tổng quát (không hỗ trợ nested/array — đúng phạm vi test
   này cần).
3. `abi_encode_bytes_ret()` mới — giống `abi_encode_string_ret` của SimpleDb nhưng KHÔNG giới hạn
   32 byte (JSON blob có thể dài hơn 1 word) — khớp chính xác `argument_encode.EncodeSingleString`
   thật (`encoder.go`: offset=32, length, data).
4. `handle_reverse_call()`'s case `MVM_TZ_RCMD_EXTENSION_EXTRACT_JSON_FIELD`: decode 2 arg qua
   `abi_decode_bytes_args` (không phải `abi_decode_string_args` — JSON test string 40 byte, vượt
   giới hạn 32 byte của bản dành cho SimpleDb), KHÔNG gatekeep theo selector (khớp hành vi Go thật
   — hàm thật cũng không check).

**Xác nhận trên hardware, không cần reboot/flash**: reverse call cmd=104 blob_len=228, input
`json={"status":"ok","value":123,"flag":true} field=value`, trích đúng `"123"`, RETURN = 96 byte
ABI string offset(0x20)+length(3)+`"123"` (hex `313233`).

**Còn lại duy nhất**: CALL_GET_API — để sau theo đúng yêu cầu người dùng (chưa cần HTTP thật).
Cả 3/4 lệnh extension còn lại (GET_OR_CREATE_SIMPLE_DB, BLST, EXTRACT_JSON_FIELD) đã có dữ liệu
thật, xác nhận trên hardware.
