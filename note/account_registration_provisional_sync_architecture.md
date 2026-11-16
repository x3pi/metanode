# Kiến trúc: Đăng ký tài khoản tạm thời (Provisional) và đồng bộ lên Parent Chain

- **Ngày cập nhật:** 2026-11-16 (bản 2: ngưỡng 1 chữ ký cho provisional, gửi batch lên parent, giãn nhịp đề xuất, rate limit, e2e)
- **Nhánh:** `test-deploy`
- **Trạng thái:** Đã triển khai trong code. Unit test, integration test và e2e trong tiến trình đều PASS; build PASS (xem mục 12). **Chưa chạy e2e trên cluster thật** (cần deploy binary mới, xem mục 11).
- **Tài liệu liên quan:** `note/plan_recipient_gate_and_offline_provisional_20261012.md` (chính sách Recipient Gate / Offline Provisional, khóa cụm thua).

---

## 1. Mục tiêu

1. User đăng ký **chỉ với child chain** (execution node). Nếu chữ ký hợp lệ và địa chỉ chưa thuộc cluster nào khác, child chain **tự kích hoạt tạm (PROVISIONAL)** ngay ở lượt relay đầu tiên, không đợi Parent Chain xác nhận.
   - `mtn_registerAccount` **chỉ tiếp nhận** request và trả `PENDING`. Client phải poll `mtn_getRegistrationStatus` tới khi thấy `PROVISIONAL` (cờ provisional đã commit on-chain, thường sau 1–2 block) hoặc `CONFIRMED`, rồi mới bắt đầu giao dịch. Giao dịch gửi trước lúc đó sẽ bị account gate từ chối.
2. Child chain **tự đồng bộ lên Parent Chain ở nền**: gửi lại khi lỗi hoặc timeout, cho tới khi parent xác nhận hoặc phân xử xong. Có thể **gom nhiều đăng ký vào một tx** (batch).
3. Request được **ghi DB (fsync) trước khi trả kết quả cho user**. Child chain crash hay restart cũng không mất request, và các request đó được gửi tiếp. Địa chỉ đã có trên parent thì bỏ qua, không gửi lại.
4. Sự cố đồng bộ được **báo qua Telegram, có gom tin để không spam**.

Trước thay đổi này, tài khoản chỉ được kích hoạt tạm sau khi gọi parent **lỗi ít nhất 2 lần** (`entry.attempts < 2`), tức là chỉ khi parent đã "chết hẳn". Mọi lỗi đồng bộ chỉ ghi vào `log.Printf`.

---

## 2. Thành phần và vị trí code

| Thành phần | File | Vai trò |
|---|---|---|
| RPC cho user | `execution/cmd/simple_chain/mtn_registration_api.go` | `mtn_getRegistrationMessage`, `mtn_registerAccount` (có rate limit theo IP), `mtn_getRegistrationStatus` |
| `RegistrationRelay` | `execution/pkg/rollup/registration_relay.go` | Nhận request, lưu DB, gửi lên parent (lẻ hoặc batch), theo dõi trạng thái, cảnh báo |
| Store bền vững | `RegistrationRelayStore` / `kvRelayStore` (cùng file), key `reg_relay:<addr>` | Lưu `RelayRecord` dạng JSON |
| `durableKV` | `execution/cmd/simple_chain/app.go` (`type durableKV`) | Bọc storage, gọi `storage.SyncDurable` (fsync) sau mỗi Put/Delete |
| Khởi tạo relay | `app.go` (`NewRegistrationRelay`, `SetBatchSize`, `SetStore`, `SetProvisionalProposer`, `SetLockProposer`) | Gắn checker on-chain, proposer, store, kích thước batch |
| Áp dụng event on-chain | `execution/pkg/rollup/account_registry_handler.go` | System tx `account_provisional` / `account_registered` / `account_locked` đi qua đồng thuận |
| Committee | `execution/pkg/blockchain/tx_processor/rollup_committee.go` (`LiveCommitteeProvider`) | Khóa BLS phân biệt của các validator đang stake, không bị jail. Production luôn truyền vào handler (`rollup_system_handler.go`) |
| Worker đăng ký | `execution/pkg/rollup/worker_registration.go` | **Mọi validator** tự poll parent và ký `account_registered` bằng khóa committee của mình |
| Parent Chain | `execution/pkg/parentchain/tx.go`, `quorum_client.go`, `proto/parent_chain.proto` | `METHOD_REGISTER_ACCOUNT` (lẻ) và `METHOD_REGISTER_ACCOUNTS_BATCH` (batch) |
| Telegram | `execution/pkg/logger/logger.go` (`logger.Telegram`) | Gửi qua hàng đợi không chặn. Cấu hình `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, `TELEGRAM_THREAD_ID` |

**Nguyên tắc:** relay **không nằm trong đồng thuận**. Quyền giao dịch chỉ do **cờ on-chain** quyết định (Provisional / ParentRegistered / Locked). Các cờ này được set bằng system tx đi qua consensus. Relay chỉ *đề xuất* các tx đó và báo trạng thái cho user.

---

## 3. Trạng thái của một request

| Trạng thái | Ý nghĩa |
|---|---|
| `NONE` | Không có request nào cho địa chỉ |
| `PENDING` | Đã nhận và lưu DB, đang chuyển lên parent; **chưa dùng được** |
| `PROVISIONAL` | Cờ Provisional đã có on-chain ở cluster này: **user dùng được**, vẫn đang chờ parent xác nhận |
| `CONFIRMED` | Parent đã đăng ký địa chỉ cho cluster này, cờ ParentRegistered đã có on-chain |
| `REJECTED` | Parent đã đăng ký địa chỉ cho **cluster khác** trước (xung đột), địa chỉ bị khóa ở đây |
| `FAILED` | Không chuyển được lên parent và không phải tài khoản tạm. User có thể gửi lại |

`Status()` luôn ưu tiên cờ on-chain theo thứ tự: Locked, rồi ParentRegistered, rồi Provisional, sau đó mới tới bộ nhớ, cuối cùng là DB (nạp lười).

---

## 4. Luồng chính

```
User ──mtn_registerAccount(addr, sig)──▶ rate limit theo IP ──▶ Submit()
   1. addr locked → REJECTED;  registered → CONFIRMED;  provisional → PROVISIONAL   (idempotent)
   2. verify chữ ký secp256k1 trên RegistrationDigest(addr, clusterKey)  (sai → lỗi)
   3. ghi RelayRecord xuống DB + fsync        ◀── TRƯỚC KHI trả lời user
   4. đưa vào hàng đợi (queued=true) → trả PENDING
            │
            ▼  vòng relay: mỗi 1s hoặc khi Wake()
ProcessBatch()
   (1) lấy tối đa 64 request từ hàng đợi → prepareRelay() từng cái:
         ├─ on-chain registered → markConfirmed (xoá khỏi bộ nhớ và DB)
         ├─ GetAccountRegistry(addr) trên parent
         │    ├─ lỗi mạng/timeout → retryOrFail + đề xuất PROVISIONAL (trừ lỗi cấu hình cục bộ)
         │    ├─ đã thuộc cluster MÌNH → không gửi lại, chờ cờ on-chain
         │    └─ đã thuộc cluster KHÁC → đề xuất lock → REJECTED + cảnh báo CONFLICT
         └─ chưa ai sở hữu → đưa vào danh sách cần gửi
       sendRegistrations(danh sách):
         ├─ batch bật và ≥ 2 request → mỗi nhóm ≤ batchSize = 1 tx METHOD_REGISTER_ACCOUNTS_BATCH
         │    └─ parent từ chối batch ngay khi gửi → gửi lẻ từng cái ngay trong tick đó, đánh dấu noBatch
         └─ ngược lại → mỗi request 1 tx SendRegisterAccount
       gửi OK → submitted=true, lưu txHash → đề xuất PROVISIONAL
   (2) tối đa 64 request đã gửi, đang chờ parent → verifyOne()
   (3) requeueStalled(): đưa request bị kẹt vào lại hàng đợi, nạp thêm từ DB nếu còn
   (4) flushAlerts(false): gửi Telegram đã gom nếu đủ khoảng thời gian

verifyOne(addr)   (với request đã gửi)
   ├─ on-chain registered → markConfirmed
   ├─ receipt của txHash có Status != 1 → reset nonce (1 lần cho mỗi txHash) → retryOrFail (gửi lại)
   │     tx batch thất bại → các thành viên gửi lẻ từ đó về sau (noBatch)
   ├─ registry lỗi tạm thời → thử lại tick sau (không tính attempts), cảnh báo 1 lần
   ├─ registry có kết quả → resolveFound (của mình: chờ cờ; của cluster khác: lock)
   └─ quá maxVerifies (600 lần, khoảng 10 phút) mà parent chưa thấy
        ├─ tài khoản tạm → reset nonce, gửi lại tx, cảnh báo "confirmation timeout"
        └─ không phải tài khoản tạm → FAILED, cảnh báo
```

---

## 5. Kích hoạt tạm (`account_provisional`)

### 5.1 Khi nào relay đề xuất

| Tình huống | Đề xuất? | Lý do |
|---|---|---|
| Parent sống, chưa ai sở hữu, gửi tx lên parent thành công | **Có, ngay tick đó** | Đã gửi lên parent, chỉ chờ parent thực thi |
| Parent lỗi mạng hoặc timeout (lookup hay send) | **Có, ngay lần lỗi đầu** | Không bắt user đợi parent sống lại |
| Parent báo địa chỉ thuộc cluster khác | **Không** | Đã biết chắc là xung đột |
| Lỗi cấu hình cục bộ (không có node parent, thiếu private key) | **Không** | Node này không bao giờ đồng bộ được |
| Đề xuất bị lỗi (ví dụ tx pool bận) | Thử lại ở lượt sau | Có ghi log |

### 5.2 Ngưỡng chữ ký: 1 chữ ký committee (hướng A)

- `account_registered` và `account_locked`: cần **f+1** chữ ký committee, với `required = (N-1)/3 + 1` và N là số khóa BLS phân biệt. Mọi validator đều tự poll parent và tự ký, nên luôn đạt được.
- `account_provisional`: cần **1** chữ ký hợp lệ của **một thành viên committee** (`verifyAttestations(..., minRequired=1)`).
  - Lý do: chỉ node nhận RPC của user biết về request, nên trên committee từ 4 khóa trở lên thì f+1 (≥ 2) **không bao giờ đạt được**, và tài khoản tạm sẽ kẹt mãi. Mọi điều một người ký thêm có thể kiểm tra thì mọi replica **đã kiểm tra tất định** trong handler: chữ ký user gắn với đúng cluster key, chưa locked, chưa registered. Chữ ký validator chỉ chứng minh tx do một thành viên committee gửi.
  - Chữ ký từ ngoài committee, hoặc ký sai digest, vẫn bị từ chối.
  - Rủi ro chấp nhận (giống chính sách offline-provisional): trong lúc chờ parent, địa chỉ có thể đã thuộc cluster khác. Khi parent trả lời thì địa chỉ bị khóa ở đây.

| Số khóa BLS phân biệt (N) | provisional cần | registered/locked cần |
|---|---|---|
| 1–3 (kể cả Raft dùng chung 1 cluster key như devnet hiện tại) | 1 | 1 |
| 4–6 | **1** (trước đây là 2, nên bị kẹt) | 2 |
| 7 | **1** (trước đây là 3) | 3 |

### 5.3 Không spam đề xuất

Sau mỗi lần đề xuất thành công, request phải chờ `relayProvisionalRetryTicks = 30` lượt relay mới được đề xuất lại (`provWait`). Vì ngưỡng là 1, đề xuất đầu tiên áp dụng ngay khi commit, và cờ on-chain chặn mọi lần đề xuất tiếp theo. Việc đề xuất lại chỉ xảy ra khi lần trước không được áp dụng. Trước đây, khi parent chết, mỗi user đang chờ sinh **1 system tx mỗi tick**.

---

## 6. Gửi lên Parent Chain: lẻ và batch

### 6.1 Method batch phía parent
- Proto: `METHOD_REGISTER_ACCOUNTS_BATCH = 9`, `RegisterAccountsBatchArgs { repeated RegisterAccountArgs entries }` (field `register_accounts_batch = 10`).
- Tối đa `MaxRegisterAccountsBatch = 64` entry. Batch rỗng hoặc vượt giới hạn → `ErrorCode 225`.
- **Mọi entry phải đăng ký vào chính cluster gửi tx** (`FloatIdentityKey == senderKey`), nếu không thì cả tx bị từ chối (`ErrorCode 226`).
- **Mỗi entry độc lập**: entry lỗi (đã thuộc cluster khác, chữ ký user sai) bị hoàn tác bằng snapshot lồng nhau và bỏ qua, các entry khác vẫn áp dụng. Receipt `Status = 1`. Kết quả từng địa chỉ thể hiện trong AccountRegistry, và relay vốn đọc kết quả từ đó.
- Cả batch dùng **1 nonce**. Seq inbound vẫn liên tục.
- Bước admission (`VerifyTxSignature`) chấp nhận cluster chưa có trong AccountRegistry nếu khóa của entry đầu tiên suy ra đúng địa chỉ người gửi, giống tx đăng ký lẻ.

### 6.2 Phía relay
- Bật bằng `PARENT_REGISTRATION_BATCH_SIZE` > 1 (tối đa 64). **Mặc định tắt (0).**
- Các request sẵn sàng gửi trong cùng một tick được chia thành nhóm ≤ batchSize, mỗi nhóm 1 tx.
- Dự phòng:
  - Parent từ chối batch ngay khi gửi (ví dụ node parent bản cũ): gửi lẻ từng request ngay trong tick đó, đánh dấu `noBatch`.
  - Tx batch được thực thi nhưng thất bại (`Status != 1`): reset nonce **1 lần** cho txHash đó (`reportTxFailureOnce`), các thành viên gửi lẻ từ đó về sau.
- Batch thành công một phần: thành viên nào bị parent từ chối sẽ tự đi theo luồng `resolveFound` (lock) hoặc timeout xác nhận.

### 6.3 Gửi lại khi lỗi/timeout
| Sự cố | Cơ chế |
|---|---|
| Lookup hoặc send lên parent lỗi | `retryOrFail`: `attempts++`, đưa vào lại hàng đợi, tick sau gửi lại |
| Tài khoản tạm và parent chết lâu | **Không bao giờ FAILED**, thử lại mãi |
| Không phải tài khoản tạm | FAILED sau `defaultRelayMaxAttempts = 20` lần |
| Parent thực thi nhưng từ chối tx (ví dụ errCode 103) | Reset nonce rồi gửi lại |
| Parent nhận tx nhưng mãi không đăng ký | Sau `defaultRelayMaxVerifies = 600` lần kiểm tra: reset nonce, gửi lại (tài khoản tạm) hoặc FAILED |
| Hàng đợi đầy khi retry | Bước quét `requeueStalled` đưa lại vào hàng đợi khi có chỗ |

---

## 7. Bền vững dữ liệu: crash, restart, bỏ qua nếu đã tồn tại

- `Submit` ghi `RelayRecord` (gồm **userSig** và **txHash**) với fsync **trước khi** trả lời user. Ghi DB lỗi thì `Submit` trả lỗi. Bị từ chối vì busy thì xoá record vừa ghi, không để lại record mồ côi.
- Khi restart, `SetStore` đọc lại toàn bộ record:
  - Record đã registered hoặc locked on-chain thì bị xoá.
  - Tối đa 4096 record nạp vào bộ nhớ; phần dư được nạp dần khi có chỗ (`storeHasMore`).
  - Request chưa gửi được đưa vào hàng đợi. Nếu hàng đợi đầy thì bước quét mỗi tick sẽ xử lý.
- Đã tồn tại thì bỏ qua: `Submit` idempotent. `prepareRelay` hỏi `GetAccountRegistry` trước khi gửi. Cờ `queued` chặn đưa trùng vào hàng đợi.
- Giải phóng bộ nhớ:
  - CONFIRMED: xoá khỏi bộ nhớ và DB.
  - FAILED/REJECTED: xoá khỏi bộ nhớ khi bảng đầy, nhưng vẫn giữ trong DB.

---

## 8. Chống lạm dụng: rate limit `mtn_registerAccount`

- Mỗi IP client: `REGISTRATION_RATE_PER_MIN` lượt mỗi phút (**mặc định 20**, burst bằng số đó; `0` là tắt). Vượt giới hạn thì trả lỗi `too many registration requests from this address, retry later`.
- Lý do: tạo khóa thì miễn phí, nhưng mỗi request tốn 1 fsync, 1 chỗ trong bảng 4096, 1 tx lên parent, và có thể 1 cờ provisional on-chain vĩnh viễn.
- Bảng limiter tối đa 10.000 IP. Đầy thì xoá toàn bộ và đếm lại từ đầu.
- Lưu ý: nếu RPC nằm sau reverse proxy thì mọi user chung một IP. Khi đó cần tăng giới hạn hoặc đặt 0.
- Cộng thêm rate limiter chung của RPC server (`rpc_rate_limiter.go`).

---

## 9. Cảnh báo Telegram (chống spam)

| Loại (tiêu đề trong tin) | Khi nào được ghi nhận |
|---|---|
| `⚠️ Parent Chain sync error` | Lỗi đồng bộ **đầu tiên** của một tài khoản trong một đợt lỗi (cờ `alerted`) |
| `⏱️ Parent Chain confirmation timeout, resubmitting` | Quá `maxVerifies`, phải gửi lại |
| `❌ registration FAILED (...)` | Hết số lần thử, hoặc hết thời gian chờ xác nhận (không phải tài khoản tạm) |
| `🚨 registration CONFLICT (...)` | Parent đã gán địa chỉ cho cluster khác |
| `⚠️ relay retry queue full` | Hàng đợi retry đầy |
| `✅ CONFIRMED after sync error` | Tài khoản từng bị báo lỗi nay đã được xác nhận |

Cảnh báo được gom và gửi tối đa **1 tin mỗi phút** (`defaultRelayAlertInterval`). Mỗi tin có số lượng theo loại, tối đa 3 địa chỉ mẫu, và dòng `Tracked now: pending=… provisional=… failed=…`. Lỗi đầu tiên sau một khoảng yên lặng thì báo ngay. `Stop()` gửi nốt phần còn lại. Wall-clock ở đây chỉ dùng để giãn nhịp thông báo, không quyết định gì on-chain.

---

## 10. Hằng số và biến môi trường

| Tên | Giá trị | Ý nghĩa |
|---|---|---|
| `defaultRelayQueueSize` | 1024 | Sức chứa hàng đợi relay |
| `defaultRelayMaxTracked` | 4096 | Số request tối đa trong bộ nhớ |
| `defaultRelayMaxAttempts` | 20 | Số lần thử trước khi FAILED (không áp dụng cho tài khoản tạm) |
| `defaultRelayMaxVerifies` | 600 | Số lần kiểm tra trước khi coi là timeout xác nhận |
| `defaultRelayInterval` | 1s | Chu kỳ tick của relay |
| `relayBatchPerTick` | 64 | Số request tối đa mỗi bước trong một tick |
| `relayProvisionalRetryTicks` | 30 | Số lượt chờ trước khi đề xuất provisional lại |
| `MaxRegisterAccountsBatch` | 64 | Số entry tối đa trong 1 tx batch |
| `defaultRelayAlertInterval` | 1 phút | Khoảng cách tối thiểu giữa 2 tin Telegram |
| env `PARENT_REGISTRATION_BATCH_SIZE` | mặc định 0 (tắt) | > 1 thì bật batch. Ansible: `parent_registration_batch_size` |
| env `REGISTRATION_RATE_PER_MIN` | mặc định 20 | Giới hạn theo IP. Ansible: `registration_rate_per_min` |

---

## 11. Triển khai (BẮT BUỘC đọc trước khi deploy)

Cả hai thay đổi dưới đây là **thay đổi luật thực thi** và repo **không có cơ chế kích hoạt theo block height**:

1. **Ngưỡng provisional = 1** (`account_registry_handler.go`): trên committee từ 4 khóa trở lên, node bản cũ giữ tx ở trạng thái pending, còn node bản mới set cờ provisional, dẫn tới state lệch nhau. ⇒ **Mọi replica của một execution cluster phải chạy cùng bản**: dừng tất cả, nâng cấp, rồi khởi động lại; không rolling upgrade. Với committee 1–3 khóa (như devnet Raft hiện tại, các replica dùng chung 1 cluster key) thì luật cũ và mới cho cùng kết quả.
2. **Method batch** (`parentchain/tx.go`): node parent bản cũ thực thi tx batch thành `ErrorCode 299`, còn bản mới thì thực thi thành công. ⇒ **Nâng cấp toàn bộ node Parent Chain cùng lúc trước**, rồi mới đặt `parent_registration_batch_size > 1` cho các execution cluster. Thứ tự ngược lại vẫn an toàn về dữ liệu (relay tự rơi về gửi lẻ), nhưng phí công.

---

## 12. Kiểm thử

### 12.1 Unit test
| File | Nội dung |
|---|---|
| `pkg/rollup/registration_relay_test.go` | Test relay gốc (đã chỉnh cho hành vi mới) |
| `pkg/rollup/registration_relay_provisional_alert_test.go` | Kích hoạt tạm, retry/timeout, gom cảnh báo, bền vững/restart (17 test) |
| `pkg/rollup/registration_relay_batch_test.go` | 1 tx cho nhiều user, chia nhóm theo batchSize, tắt mặc định, dự phòng gửi lẻ, reset nonce 1 lần, batch thành công một phần, giãn nhịp đề xuất |
| `pkg/parentchain/register_accounts_batch_test.go` | Encode/decode và giới hạn, từng entry độc lập (MemoryStore + TreeStore), chặn entry của cluster khác (226), batch sai định dạng (225) |
| `pkg/blockchain/tx_processor/provisional_integration_test.go` (P10) | Committee 4 khóa: 1 chữ ký kích hoạt provisional; chữ ký ngoài committee hoặc sai digest bị từ chối; `account_registered` vẫn cần 2 |
| `cmd/simple_chain/mtn_registration_ratelimit_test.go` | Ngân sách theo IP, tắt khi 0, giới hạn bộ nhớ |

### 12.2 E2E trong tiến trình (`pkg/blockchain/tx_processor/registration_e2e_inprocess_test.go`)
Parent **thật** (`ExecuteTx` trên `MemoryStore`, `HTTPServer.Handler()` thật, gọi qua `QuorumClient` bằng HTTP thật) và cluster **thật** (chain state, committee 4 khóa trong stake DB, `RollupSystemHandler`, account gate). Chỉ phần sản xuất block là giả lập.

| Test | Kiểm tra |
|---|---|
| `TestRegistrationE2E_BatchProvisionalAndConfirmation` | 5 user, đúng **1 tx parent** (nonce +1); cả 5 PROVISIONAL với 1 chữ ký và giao dịch được; 1 worker chưa đủ để CONFIRMED, 2 worker thì đủ |
| `TestRegistrationE2E_ParentOutageThenRecovery` | Parent sập (HTTP 503): PROVISIONAL và giao dịch được; 50 tick không bị FAILED; parent sống lại thì CONFIRMED |
| `TestRegistrationE2E_CrashRestartDurability` | Crash sau khi đã nhận 3 request lúc parent sập; relay mới trên cùng DB gửi đủ cả 3; DB rỗng sau khi CONFIRMED |
| `TestRegistrationE2E_TwoClustersRace` | Hai cluster cùng một địa chỉ: A thắng và CONFIRMED; B khóa địa chỉ, báo REJECTED với home = A, không bao giờ mở tạm |

Phép thử đột biến: trả ngưỡng provisional về f+1 thì `BatchProvisionalAndConfirmation` FAIL ("provisional with a single committee attestation on a 4-key committee"). Vậy test thực sự kiểm tra hướng A.

### 12.3 E2E trên cluster thật (`execution/cmd/tool/e2e_registration_cluster`)
Mặc định trỏ vào devnet của `deploy/ansible_clusters/inventory.yml` (exec1 `:8747`, exec2 `:8750`, parent `:18601`).

| Bước | Nội dung | Mặc định |
|---|---|---|
| S1 | Identity hai cluster, gate bật, key khác nhau | chạy |
| S2 | N user đăng ký đồng thời trên exec1: đo thời gian tới khi dùng được và tới CONFIRMED, số tx parent (nonce delta, < N nếu batch bật) | chạy (`-users 10`) |
| S3 | Cùng một địa chỉ đăng ký đồng thời ở exec1 và exec2: đúng 1 CONFIRMED, 1 REJECTED đúng home | chạy |
| S4 | Parent sập: PROVISIONAL trong lúc sập, CONFIRMED sau khi khởi động lại | cần `-parent-stop-cmd` và `-parent-start-cmd` |
| S5 | Rate limit theo IP trên exec2 (chặn IP này khoảng 1 phút) | cần `-rate-check` |

```
cd execution
go run ./cmd/tool/e2e_registration_cluster -users 20 -report /tmp/reg_e2e.md \
  -parent-stop-cmd  'sudo systemctl stop  metanode-parent_node_{0,1,2,3}' \
  -parent-start-cmd 'sudo systemctl start metanode-parent_node_{0,1,2,3}' -rate-check
```
(Tên service theo devnet hiện tại: `metanode-parent_node_0..3`.)

### 12.4 Kết quả chạy
Xem mục 13.

---

## 13. Kết quả đo

(Chỉ ghi số có log thật. Log nằm ở thư mục scratchpad của phiên, không commit; chạy lại các lệnh dưới đây để tái hiện.)

**2026-11-16, bản 2:**
- [ĐO] `CGO_ENABLED=1 go test -race -p 1 -count=1 ./pkg/rollup/ ./pkg/parentchain/`: `ok` cả hai, exit 0, không có `DATA RACE`.
- [ĐO] `CGO_ENABLED=1 go test -p 1 -count=1 ./pkg/blockchain/tx_processor/ ./cmd/simple_chain/ ./cmd/simple_chain/processor/`: `ok` cả ba (tx_processor 404.9s), exit 0. Gồm 4 test `RegistrationE2E_*` và P10 mới.
- [ĐO] `consensus/metanode/scripts/build_check.sh` (đầy đủ, kể cả Rust): "ALL BUILDS PASSED (5/5)", exit 0.
- [ĐO] Phép thử đột biến (ngưỡng provisional về f+1): `TestRegistrationE2E_BatchProvisionalAndConfirmation` FAIL như mong đợi, sau đó code đã được khôi phục.
- [ĐO] `protoc` (libprotoc 3.21.12, protoc-gen-go v1.31.0, vtproto v0.6.0) tạo lại `parent_chain*.pb.go` giống hệt bản đã commit (kiểm tra trước khi sửa proto), nên các file sinh lại sau khi sửa proto là do cùng bộ công cụ tạo ra.
- [CHƯA ĐO] `e2e_registration_cluster` trên devnet thật: cần deploy bản mới (`./deploy_clusters.sh --deploy`). Chưa có số đo độ trễ hay thông lượng thật.

---

## 14. RegistrationWorker tự phục hồi khi Parent Chain bị reset

`execution/pkg/rollup/worker_registration.go`. Cursor của worker chỉ nằm trong bộ nhớ, nên restart child node cũng quét lại từ 0. Cơ chế dưới đây áp dụng khi child **vẫn đang chạy** trong lúc parent bị reset.

| Tín hiệu | Cách phát hiện | Hành động |
|---|---|---|
| **Lịch sử parent đổi** (wipe rồi mọc lại, kể cả khi dài hơn cursor cũ) | Worker poll từ `cursor-1` và so event đầu với **fingerprint** (seq + user của event đã xác nhận cuối cùng) | Lệch: **reset ngay** về 0, gửi 🚨 Telegram |
| **Parent ngắn hơn cursor** (`nextCursor < cursor`, cần parent trả `total` khi `cursor >= total`) | Đếm số lần poll liên tiếp thấy thiếu | Chỉ reset khi thấy liên tiếp `registrationShrinkConfirmPolls = 30` lần; 1 lần poll bình thường là bộ đếm về 0 |

- Reset về **0** (không phải về `nextCursor`), vì sau khi reset, seq 0..total-1 có thể là user mới. Quét lại rẻ: user đã `ParentRegistered` thì bỏ qua, `AlreadyAttested` chặn ký trùng.
- Lý do cần đếm K lần: khi restart đồng loạt, các node parent đang replay commit tạm thời trả log ngắn hơn. Quorum 2/4 node chậm như nhau trước đây sẽ gây reset nhầm và 🚨 giả. Node chậm không bao giờ trả user khác ở cùng một seq, nên nhánh fingerprint vẫn reset ngay.
- `HTTPServer` trả 503 cho `/inbound_registrations` và `/inbound` khi `syncing`, **nhưng hiện `SetSyncing()` chưa được gọi ở đâu trong `cmd/parent_chain`**, nên chặn này chưa có tác dụng. Bước đếm K lần ở trên là thứ thực sự chống reset nhầm.
- Rủi ro chấp nhận: parent mất dữ liệu nghĩa là mất bản ghi quyền sở hữu, nên child có thể đang giữ `ParentRegistered` cho user mà parent mới không còn biết. Tin 🚨 là để người vận hành xử lý, không tự sửa trong im lặng.
- Test: `pkg/rollup/worker_registration_test.go` (`W1_SelfHealing_ParentReset`: ngắn lại, xoá trắng, mọc lại dài hơn; `W1_LaggingParent_NoFalseResetOrAlert`). Phép thử đột biến: tắt bước đếm thì `W1_LaggingParent` FAIL.
- Token Telegram: `security.env` (0600) được render từ `telegram_bot_token` / `telegram_chat_id`, nạp qua `EnvironmentFile`. Template ép kiểu `| string` để chat_id dạng số không làm vỡ deploy. **Token nên được mã hoá bằng `ansible-vault encrypt_string`** thay vì để plaintext trong `inventory.yml`.

