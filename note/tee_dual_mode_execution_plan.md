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
block `main()`) — đúng ràng buộc thứ tự §9.5. **CHƯA compile được** — cần môi trường build
`chanmgr`/chcore đầy đủ, chưa dựng trong phiên này; lần build thật đầu tiên sẽ lộ ra bất kỳ lỗi
cú pháp/API nào.

**Việc CHƯA làm, rõ ràng**:
- Chưa build được `chanmgr/main.c`'s patch (chỉ viết theo đọc code, chưa compile).
- Chưa tích hợp `mvm_ta` vào pipeline Docker thật (`build-chcore`) — chưa có target build thật.
- Chưa test trên board thật bất kỳ phần nào ở mục này.
- Encode đầy đủ cho 7/10 loại mảng state-change còn lại + 5 forward command còn lại.
- §9.4's rủi ro wait_switch_req — nay đã NÉ HẲN bằng thiết kế busy-poll, không còn áp dụng cho
  cơ chế của metanode nữa (nhưng vẫn là rủi ro thật cho `llama-cli`'s riêng, không phải việc
  của metanode giải quyết).

File: `metanode/execution/pkg/mvm/ta/{mvm_ta_main.cpp,README.md}` (mới),
`tz-llm-trustzone/tz-llm/tee_os_kernel/user/system-services/system-servers/chanmgr/main.c`
(sửa, chưa compile).
