# Production Security Hardening — bài học từ các cross-chain bridge khác

> Research doc, không phải audit report. Mục tiêu: đối chiếu từng lỗ hổng/kiến trúc phòng thủ đã
> biết trên các bridge thật (đã bị hack hoặc đã production nhiều năm) với `GatewayEngine`
> (`execution/pkg/cross_chain/gateway.go`) hiện tại, để biết cái gì ĐÃ có tương đương, cái gì THIẾU
> thật sự, và cái gì KHÔNG áp dụng (đừng copy pattern chỉ vì "bridge lớn nào cũng có").
>
> Bối cảnh đã biết khi viết doc này (2026-09-22, ngay sau commit `ef562e28` — 10 finding vừa vá):
> attest-then-claim với BLS 2f+1 quorum cert theo `(2*totalStake)/3+1` (vừa fix), ceiling
> `PerChainAllocation` per-commit (không phải theo thời gian), 2-hop qua Reserve chain, governance
> thuần cert-based (`GovernanceEngine` propose/vote/72h-timelock đã bị XOÁ theo yêu cầu người dùng
> — xem `project_root_anchor_min_founding_chains`/memory, đừng đề xuất lại DAO governance mà không
> nhắc điều này), `DeclareChainDeadWithCert` (gateway.go:622) là kill-switch per-chain đã có sẵn,
> cert-gated. Không có circuit breaker toàn Gateway, không có rate-limit theo thời gian.

---

## 1. Bài học từ các vụ hack lớn — root cause thật, không chỉ số tiền

### 1.1 Wormhole (2/2022, $326M) — bypass verify signature qua input account giả
Root cause: chương trình Solana dùng `load_instruction_at` (deprecated) để xác nhận
`Secp256k1Program` đã được gọi trước đó, nhưng không kiểm tra địa chỉ của Instructions sysvar được
truyền vào — attacker giả 1 account khác đóng vai sysvar, khiến check "signature đã verify" pass
dù thực tế signature chưa từng được verify. [Halborn](https://www.halborn.com/blog/post/explained-the-wormhole-hack-february-2022)

**Đối chiếu:** lớp lỗi này là "verify sai đối tượng, không sai thuật toán ký". `VerifyQuorumCertAgainstRegistry`
(gateway.go:1673+) verify trực tiếp trên `digest []byte` được compute tại chỗ bởi caller
(`ComputeCommitRootAttestMessage`, `ComputeMessageLeafHash`, ...), không nhận digest từ bên ngoài
qua một "context" gián tiếp nào — nên lớp lỗi Wormhole cụ thể (input account giả mạo) không áp dụng
trực tiếp. Nhưng NGUYÊN TẮC đáng giữ: **mọi digest phải được compute lại từ dữ liệu đã verify, không
bao giờ nhận digest làm tham số từ bên gọi**. Đã kiểm tra: đúng là design hiện tại luôn tự compute
digest nội bộ (`ComputeMessageSuccessAttestMessage`, `ComputeMessageFailureAttestMessage`, ...) —
không có hàm public nào nhận `digest []byte` trực tiếp từ ABI calldata untrusted. **Không có gap ở
đây, chỉ ghi nhận để không vô tình phá invariant này trong tương lai.**

### 1.2 Ronin/Axie (3/2022, $625M) — compromise khoá validator, quyền uỷ quyền quên thu hồi
Root cause KHÔNG phải lỗi smart contract: 5/9 khoá validator bị lộ (4 khoá Sky Mavis + 1 khoá Axie
DAO mà Sky Mavis được **uỷ quyền ký tạm thời** hồi 11/2021 để giảm tải, nhưng **quyền đó không bao
giờ bị thu hồi**). [Cointelegraph](https://cointelegraph.com/news/the-aftermath-of-axie-infinity-s-650m-ronin-bridge-hack)

**Đối chiếu:** dự án này dùng BLS quorum cert theo stake-weight (không phải fixed N-of-M khoá đơn
giản kiểu Ronin), và committee rotation đi qua `UpdateCommitteeWithRecoveryCert` (gateway.go:665,
cert-gated bởi `RecoveryCommittee`) — không có khái niệm "uỷ quyền tạm thời rồi quên thu hồi" kiểu
Ronin. **Nhưng bài học thật sự áp dụng được:** `RecoveryCommittee` tự nó CHÍNH LÀ một trust root cố
định, non-growable (theo memory). Nếu 1 khoá trong `RecoveryCommittee` bị lộ, kẻ tấn công có thể ký
`UpdateCommitteeWithRecoveryCert`/`DeclareChainDeadWithCert` cho BẤT KỲ chain nào — đây là điểm tập
trung rủi ro tương đương "5/9 khoá Ronin" về mặt hệ quả (single trust anchor, không theo dõi được
qua on-chain data-driven attestation như `CommitVoteMonitor`). **Câu hỏi cần trả lời (không phải
code fix):** `RecoveryCommittee` hiện có bao nhiêu thành viên, threshold bao nhiêu, và khoá được
lưu ở đâu (HSM? hot wallet?) — đây là câu hỏi vận hành/key-management, không phải logic code, nên
nằm ngoài phạm vi sửa code của agent, nhưng LÀ điểm rủi ro cao nhất theo đúng pattern Ronin nếu câu
trả lời là "ít người giữ khoá, lưu plaintext".

### 1.3 Nomad (8/2022, $190M) — trusted root mặc định 0x00, message nào cũng "hợp lệ"
Root cause: 1 bản upgrade khởi tạo `committedRoot` bằng `0x00`. Do storage Solidity trả về `0x00`
cho key chưa tồn tại, `acceptableRoot(0x00)` luôn `true` — bất kỳ message nào (kể cả chưa từng được
prove) cũng được coi là hợp lệ. Hack "crowdsourced": sau khi 1 người tìm ra, hàng trăm địa chỉ khác
copy-paste lại transaction để rút tiền. [Nomad post-mortem](https://medium.com/nomad-xyz-blog/nomad-bridge-hack-root-cause-analysis-875ad2e5aacd)

**Đối chiếu — đây là lớp lỗi có liên quan thật sự:** nguyên nhân gốc là 1 initialization/migration
path ghi 1 giá trị "rỗng" vào đúng chỗ mà logic verify coi "rỗng = đã verify". Trong gateway.go,
tương đương gần nhất là `g.AttestedCommits` map (`key := fmt.Sprintf("%d:%s:%s", ...)`, gateway.go
~1213/1308) — nếu BẤT KỲ đường nào ghi 1 `AttestedCommit{}` rỗng/zero-value vào map này mà không đi
qua `attestCommitInternal`'s verify thật, thì `ClaimMessage`'s lookup (`attested, exists :=
g.AttestedCommits[key]`) sẽ coi nó "đã attest". Đã kiểm tra lúc vá finding #2/#3 tuần này: **không
tìm thấy đường ghi trực tiếp nào ngoài `attestCommitInternal`** (không có default zero-value nào bị
serialize ra thành "đã attest" — `exists` map lookup của Go trả `false` đúng cho key chưa tồn tại,
khác hẳn Solidity mapping trả `0x00`). **Không phải gap hiện tại**, nhưng đây là lớp lỗi cần luôn
kiểm tra lại mỗi khi thêm 1 map/field verify mới: hỏi "giá trị zero/rỗng của field này có vô tình
== 'đã pass verify' không?".

### 1.4 Poly Network (8/2021, $611M) — access control lẫn lộn giữa 2 contract, decode dữ liệu lỏng lẻo
Root cause: `EthCrossChainManager` có quyền owner trên `EthCrossChainData` (nơi lưu keeper hợp lệ).
Attacker forge 1 message gọi `putCurEpochConPubKeyBytes` để thay keeper bằng public key của chính
mình — hợp lệ vì `EthCrossChainManager` là caller được trust tuyệt đối, còn scheme decode dữ liệu
đầu vào quá dễ dãi. [SlowMist](https://slowmist.medium.com/the-root-cause-of-poly-network-being-hacked-ec2ee1b0c68f)

**Đối chiếu:** lớp lỗi "contract A được trust tuyệt đối để ghi vào state an ninh của contract B,
nhưng A tự nó nhận input untrusted và forward gần như nguyên vẹn" — đây CHÍNH LÀ hình dạng
finding #3 vừa vá tuần này (destination-binding bypass: `ClaimMessage` nhận `message` trực tiếp từ
calldata, quyết định set `ActiveContext{OriginalSender, SourceChainID}` dựa trên nó). Đã vá đúng
lớp lỗi này (destChainId phải luôn khớp `g.LocalChainID`). **Điểm cần double-check thêm (chưa audit
sâu trong phiên vá vừa rồi):** `UpdateCommitteeWithRecoveryCert`'s `payload []byte` decode — có
dùng 1 scheme decode tuỳ biến ("lenient" như Poly Network) hay ABI decode chuẩn? Nếu là ABI decode
chuẩn (`abi.Method.Inputs.Unpack`, đã thấy dùng ở gateway_handler.go cho các method khác) thì an
toàn; nếu có 1 custom byte-offset parser tương tự kiểu cũ của `EthCrossChainManager` thì cần audit
riêng. **Chưa xác minh trong doc này — cần 1 lượt đọc riêng `UpdateCommitteePayload`'s decode path
trước khi coi là an toàn.**

### 1.5 Harmony Horizon (6/2022, $100M) — hạ tầng, không phải smart contract
Root cause: 2-of-5 multisig, nhưng cả 2 khoá bị lộ vì server chứa hot wallet bị compromise (khoá
lưu ở dạng ký được trực tiếp trên server, không phải HSM/air-gapped). [Halborn](https://www.halborn.com/blog/post/explained-the-harmony-horizon-bridge-hack)

**Đối chiếu:** cùng nhóm rủi ro với 1.2 (Ronin) — đây là câu hỏi key-management vận hành, không
phải code. `RecoveryCommittee` và mọi validator committee trong `ChainRegistry` đều ký bằng BLS
private key ở đâu đó ngoài phạm vi `gateway.go`. **Không sửa được bằng code, chỉ ghi nhận: threshold
càng thấp (2-of-5 kiểu Harmony) thì 1 lần compromise hạ tầng càng dễ đủ quorum — dự án này đã tốt
hơn về mặt LÝ THUYẾT vì dùng stake-weighted 2f+1 thay vì N-of-M cố định, nhưng nếu trong thực tế chỉ
có vài validator nắm phần lớn stake thì rủi ro tương đương.**

### 1.6 BNB Token Hub (10/2022, $586M) — lỗi implementation của thư viện Merkle proof (IAVL), không phải logic nghiệp vụ
Root cause: code verify IAVL range proof quên kiểm tra "node không được có right child" — attacker
gắn 1 leaf giả vào right child của 1 node thật (dùng lại root hash của 1 block cũ, hợp lệ), proof
vẫn pass vì code chỉ tính root từ left child. [QuillAudits](https://www.quillaudits.com/blog/hack-analysis/bsc-token-hub-bridge-hack)

**Đối chiếu — đây là gap thật sự đáng kiểm tra:** dự án dùng Merkle proof riêng
(`VerifyMerkleProof`, `BuildCommitTree`, `GetMerkleProof` trong gateway.go) — một implementation
tự viết, không phải thư viện bên thứ 3 như IAVL, nhưng CHÍNH VÌ VẬY rủi ro "tự viết sai 1 edge case
của cây Merkle" là có thật và chưa được audit riêng trong phiên vừa rồi (phiên trước chỉ audit
gateway.go's business logic, không audit thuật toán Merkle tree tự thân). **Khuyến nghị cụ thể:**
1 audit/fuzz riêng cho `BuildCommitTree`/`VerifyMerkleProof`/`GetMerkleProof` — cụ thể: (a) proof
cho 1 cây có số leaf lẻ (padding leaf cuối được xử lý sao?), (b) proof có index nằm ngoài range có
bị reject đúng không, (c) 2 leaf khác nhau có thể tạo cùng 1 proof path hợp lệ không (second-preimage
kiểu Merkle cổ điển — dự án có domain-separation prefix `0x00` cho message leaf theo
`ComputeMessageLeafHash`, cần xác nhận aggregate-value leaf và commit-root cũng có domain
separation tương tự để không nhầm lẫn 2 loại leaf khác nhau). **Đây là hạng mục ưu tiên cao nhất
tìm được trong doc này — chưa có bằng chứng là có bug, nhưng chưa từng được audit riêng và đây là
đúng lớp lỗi đã từng gây mất $586M ở dự án khác.**

### 1.7 Multichain/Anyswap (7/2023, $126M+ rồi sập hẳn) — tập trung hoá hạ tầng MPC vào 1 cá nhân
Root cause: server MPC chạy dưới tài khoản cloud CÁ NHÂN của CEO; CEO bị bắt, quyền truy cập biến
mất, và ngay sau đó có 1 vụ rút tiền bất thường (nghi là chính MPC key bị lấy). Thiết kế
"decentralized" trên giấy nhưng vận hành tập trung hoàn toàn. [Chainalysis](https://www.chainalysis.com/blog/multichain-exploit-july-2023/)

**Đối chiếu:** dự án không dùng MPC custody (không giữ tài sản tập trung ở 1 địa chỉ) — mô hình là
lock-and-mint qua từng chain riêng với ceiling ledger, không có 1 "vault" trung tâm nào giữ toàn bộ
tài sản liên chain. **Lớp rủi ro Multichain không áp dụng trực tiếp**, nhưng bài học vận hành vẫn
đúng: bất kỳ hạ tầng nào ký hộ `RecoveryCommittee`/validator committee mà phụ thuộc 1 cá nhân/1 tài
khoản cloud duy nhất đều có cùng rủi ro "1 điểm chết là sập cả hệ thống".

---

## 2. Kiến trúc phòng thủ đã production nhiều năm — học được gì

### 2.1 IBC (Cosmos) — light client + packet timeout, KHÔNG trust relayer
IBC verify bằng light client on-chain (relayer chỉ là "người đưa thư", không được trust) và mọi
packet có timeout tuyệt đối (theo timestamp, vì block height không tương thích giữa các chain khác
nhau) — nếu quá timeout mà chưa nhận, chain gửi tự động timeout packet lại bằng non-membership
proof. [IBC v2 spec](https://ibcprotocol.dev/blog/ibc-v2-announcement)

**Đối chiếu:** dự án ĐÃ đúng tinh thần "không trust relayer" — relayer chỉ submit calldata,
`VerifyQuorumCertAgainstRegistry`/`VerifyMerkleProof` mới là nguồn sự thật. Về packet timeout: dự
án **cố tình KHÔNG có timeout quyết định outcome** (đúng theo Zero-Fork Invariant của AGENTS.md —
"thà pending chứ không fork", không dùng `Duration`/`timeout` để quyết định dispatch). Đây là khác
biệt triết lý có chủ đích, không phải thiếu sót: IBC's timeout là về tính khả dụng ở TẦNG ứng dụng
(packet timeout ra quyết định hoàn tiền), còn Zero-Fork Invariant là về tính an toàn ở TẦNG đồng
thuận (không dispatch commit chưa verified). **2 khái niệm khác nhau, không mâu thuẫn** — dự án vẫn
có thể (và nên) học IBC's packet-timeout Ở TẦNG ỨNG DỤNG (ví dụ: 1 message pending quá lâu không ai
claim thì cho phép sender tự huỷ + hoàn tiền trên chain nguồn, dùng bằng chứng "không có commitment
nào khớp" tương tự non-membership proof) mà KHÔNG cần đụng đến tầng đồng thuận/dispatch — đây là 1
tính năng UX/availability, chưa thấy tồn tại trong gateway.go hiện tại (`MessageStatusPending` có
thể tồn tại vĩnh viễn nếu không ai bao giờ claim, không có cách nào cho sender tự rút lại). **Gap
thật, nhưng KHÔNG khẩn cấp về bảo mật (không mất tiền, chỉ là tiền có thể kẹt vô thời hạn nếu không
ai claim) — độ ưu tiên: trung bình, cần quyết định thiết kế (không phải bug fix nhỏ).**

### 2.2 Chainlink CCIP — Risk Management Network độc lập, "curse" để pause
CCIP có 1 mạng RMN tách biệt hoàn toàn: viết bằng ngôn ngữ khác, team khác, tập validator không
trùng với DON chính, tự tính lại toàn bộ batch message và "bless" nó; nếu phát hiện bất thường, đủ
số node RMN "curse" thì hợp đồng bị pause tự động — không cần chờ multisig admin. (Ghi chú: theo
docs 2026, vai trò tự động off-chain của RMN hiện KHÔNG còn active mặc định, chỉ còn contract RMN
on-chain như 1 emergency safeguard — tức là ngay cả Chainlink cũng đang thu hẹp phạm vi cơ chế này
theo thời gian, không mở rộng.) [Chainlink docs](https://docs.chain.link/ccip/concepts/architecture/offchain/risk-management-network)

**Đối chiếu:** đây là pattern **KHÔNG nên copy nguyên xi** vào dự án này. Lý do cụ thể: (a) CCIP là
hệ thống permissionless, nhiều enterprise dùng chung — cần 1 lớp veto độc lập vì không kiểm soát
được ai deploy contract gì lên nó; dự án này là mạng riêng (private chains + Root Anchor), người
vận hành đã kiểm soát toàn bộ validator set qua `ChainRegistry`/`RecoveryCommittee`. (b) Chính
Chainlink cũng đang thu hẹp phạm vi tự động của RMN, không mở rộng — không phải "best practice đang
lên", mà là 1 cơ chế đắt đỏ đang được đánh giá lại. (c) Việc này đúng như AGENTS.md's guardrail
"Scope Gating for Resilience" cảnh báo: đừng nhét circuit breaker vào business logic chỉ vì bridge
lớn có nó. **Điều ĐÁNG học, phạm vi nhỏ hơn nhiều:** `DeclareChainDeadWithCert` (gateway.go:622) đã
CHÍNH LÀ 1 dạng "curse" per-chain, cert-gated — đúng tinh thần RMN nhưng gọn hơn nhiều và không cần
hạ tầng riêng. Việc cần làm (nếu có) là mở rộng ĐIỀU KIỆN kích hoạt (hiện chỉ chắc là do 1 cert thủ
công), không phải xây 1 network riêng.

### 2.3 Axelar — PoS + threshold signature + quadratic voting chống tập trung stake
Axelar dùng Tendermint PoS, validator tự stake AXL, quorum ~67% voting power, và có thêm quadratic
voting (số phiếu = căn bậc 2 của stake) để 1 validator càng lớn càng khó thao túng thêm quyền lực
tuyến tính theo stake. [Axelar blog](https://www.axelar.network/blog/accounting-for-stake-in-threshold-signature-schemes)

**Đối chiếu:** dự án dùng stake-weighted quorum tuyến tính (`totalStake += val.Stake`,
`accumulatedStake += val.Stake` — gateway.go trong `VerifyQuorumCertAgainstRegistry`), KHÔNG có
quadratic dampening. **Gap có thật nhưng rủi ro thấp trong bối cảnh hiện tại:** quadratic voting chỉ
thật sự cần thiết khi validator set là permissionless/mở (ai cũng stake được, cá voi có thể thao
túng) — với `ChainRegistry` hiện tại (mỗi chain tự đăng ký committee riêng qua
`RegisterChainViaStake`/`UpdateCommitteeWithRecoveryCert`, không phải 1 pool stake chung toàn mạng),
mô hình đe doạ khác hẳn Axelar. **Không khuyến nghị thêm quadratic voting trừ khi validator set này
dự kiến trở thành permissionless/mở cho public trong tương lai — nếu có dự định đó, đây là quyết
định kiến trúc cần bàn riêng, không phải fix nhỏ.**

### 2.4 LayerZero v1 — bài học tiêu cực: "độc lập" chỉ đúng nếu ép được bằng cấu trúc, không phải quy ước
Điểm yếu LayerZero v1 bị chỉ trích nhiều nhất: Oracle và Relayer PHẢI độc lập để an toàn, nhưng
LayerZero không có cơ chế nào BẮT BUỘC sự độc lập đó ở tầng protocol — ứng dụng tự chọn Oracle/
Relayer của mình, nên 2 thứ có thể là cùng 1 bên vận hành mà protocol không biết/không ngăn được.
[L2BEAT](https://medium.com/l2beat/circumventing-layer-zero-5e9f652a5d3e)

**Đối chiếu — bài học áp dụng trực tiếp:** dự án có pattern tương tự cần soi lại — relayer
(`RelayerEngine`/`relayer_daemon`) là bên submit cả `attestCommit` LẪN sau đó gọi `claimMessage`.
Về mặt cryptographic, relayer không thể forge gì (BLS cert + Merkle proof đã chặn hết) — khác hẳn
LayerZero v1 (nơi Oracle/Relayer TỰ LÀ nguồn sự thật). Đây KHÔNG phải cùng lớp lỗi. **Nhưng nguyên
tắc chung đáng giữ:** bất kỳ thiết kế tương lai nào dựa vào "2 bên độc lập" (ví dụ nếu sau này thêm
1 lớp giám sát/watcher độc lập) phải ép được sự độc lập bằng cấu trúc (ví dụ: node operator khác
nhau, key khác nhau, không cho phép cùng 1 bên vừa vận hành watcher vừa vận hành validator) — không
chỉ dừng ở "khuyến nghị vận hành nên tách ra".

### 2.5 Circle CCTP — burn-and-mint loại bỏ hẳn "kho tài sản" để trộm
CCTP không khoá tài sản vào 1 pool/vault nào — burn ở nguồn, mint ở đích, dựa trên 1 attestation
service tập trung (Circle) ký xác nhận đã burn thật. Đánh đổi rõ ràng: không có pool để hack, nhưng
có 1 điểm trust tập trung duy nhất (Circle's Iris service) có thể trì hoãn/từ chối ký (không thể mint
bậy, nhưng có thể "đóng băng" hệ thống). [LI.FI deep dive](https://li.fi/knowledge-hub/circles-cross-chain-transfer-protocol-cctp-a-deep-dive)

**Đối chiếu:** dự án ĐÃ theo đúng mô hình burn-and-mint (không phải lock-and-mint kiểu pool), qua
`processNativeMintBurnForGateway`/`ProcessNativeMintBurn` — đúng nguyên tắc "không có kho để hack"
CCTP đề cao. Khác biệt tốt hơn CCTP: dự án dùng BLS quorum cert phi tập trung theo từng chain thay vì
1 attestation service tập trung duy nhất — không có single point of censorship kiểu Circle. **Không
có gap ở đây, đây là điểm dự án đã làm đúng ngay từ đầu, đáng ghi nhận.**

---

## 3. Danh sách ưu tiên hoá cụ thể

### 3.1 Nên làm sớm (rẻ, rõ ràng, không đổi kiến trúc)
1. **✅ ĐÃ LÀM (2026-09-22)** — Audit/fuzz riêng cho Merkle tree tự viết (`BuildMerkleTree`,
   `BuildCommitTree` trong relayer.go; `VerifyMerkleProof`, `hashPair`, `ComputeMessageLeafHash`
   trong gateway.go; `HashAggregateValueLeaf` trong epoch_sync.go) — theo đúng lớp lỗi đã gây ra vụ
   BNB Token Hub $586M (mục 1.6). Thêm vào `execution/pkg/cross_chain/security_audit_test.go`:
   6 test property + 1 native Go fuzz target (`FuzzMerkleProof_TamperedSiblingsNeverForgeVerification`,
   chạy sạch ~14.5M exec/30s không tìm được forge nào). Kết quả: **không tìm thấy bug** — nhưng phát
   hiện 1 điều đáng ghi lại: `BuildCommitTree` LUÔN thêm ít nhất 1 `AggregateValueLeaf` cho mỗi
   assetId khác nhau, nên 1 commit dù chỉ có 1 message cũng KHÔNG BAO GIỜ là cây 1-leaf thật sự (luôn
   ≥2 leaf) — trường hợp cây 1-leaf thật (`BuildMerkleTree` gọi trực tiếp) chỉ test được bằng cách
   bỏ qua `BuildCommitTree`. Domain separation (0x00 message / 0x01 internal / 0x02 aggregate) xác
   nhận chặn đứng mọi khả năng nhầm lẫn leaf-type; "promote unpaired node unchanged" (thay vì
   duplicate-and-hash kiểu Bitcoin) xác nhận an toàn ở mọi parity từ n=1 đến n=17 lá; proof của 1 leaf
   xác nhận không thể tái sử dụng cho leaf khác trong cùng cây. Xem chi tiết trong file test, không
   lặp lại ở đây.
2. **✅ ĐÃ KIỂM TRA (2026-09-22) — AN TOÀN, không phải fix.** `gateway_handler.go`'s
   `"updateCommitteeWithRecoveryCert"` case (dòng ~1779): outer call decode bằng ABI chuẩn
   (`h.abi.MethodById`/`method.Inputs.Unpack`, giống mọi method khác), `args[0]` là 1 tham số
   `bytes` ABI-decoded chuẩn. Payload BÊN TRONG `bytes` đó decode bằng `json.Unmarshal` thẳng vào
   `cross_chain.UpdateCommitteePayload` (`gateway_handler.go:1785`) — thoạt nhìn giống rủi ro
   "decode lỏng lẻo" kiểu Poly Network, nhưng khác biệt cốt lõi: `UpdateCommitteeWithRecoveryCert`
   (gateway.go:665-721) tính digest ký (`ComputeRecoveryUpdateCommitteeMessage`, dòng 702) trực
   tiếp từ CÁC FIELD ĐÃ DECODE (`update.ChainID/NewEpoch/NewCommittee/QuorumThreshold/StateRoot/
   AccountTreeRoot`), và CHÍNH CÁC FIELD ĐÓ (không parse lại lần nào khác) được ghi thẳng vào
   `g.ChainRegistry[update.ChainID]` (dòng 706-719) — decode-once, dùng cho cả verify lẫn state
   write, không có đường decode thứ 2 nào để lệch nhau. Đây CHÍNH LÀ điều làm Poly Network bị hack
   (2 contract tin 2 cách hiểu khác nhau của cùng 1 input) — ở đây không tồn tại 2 cách hiểu.
   `UpdateCommitteePayload` (types.go:365) chỉ có field `uint64`/`common.Hash`/`[]ValidatorEntry`,
   không có `interface{}`/map/custom `UnmarshalJSON` nào gây mập mờ ngữ nghĩa. Root fix khác đã có
   sẵn: `ChainID==0` bị reject (dòng 672-674, kể cả sau khi alias từ `SourceChainID`), epoch phải
   tăng nghiêm ngặt (dòng 688-690, có test `RejectsReplayAsRollback`), `ValidateQuorumThreshold`
   chặn floor 2/3 (dòng 698-699). **Kết luận: không có gap, không cần sửa gì.**
3. **Xác nhận vận hành `RecoveryCommittee`**: bao nhiêu member, threshold bao nhiêu, khoá lưu ở đâu
   (mục 1.2/1.5) — đây là câu hỏi cho người vận hành, không phải code, nhưng là điểm tập trung rủi ro
   cao nhất nếu câu trả lời xấu.

### 3.2 Cần quyết định thiết kế trước khi làm (không tự ý code)
4. **✅ ĐÃ LÀM (2026-09-22, commit `5ab51458`)** — Packet-timeout ở tầng ứng dụng (mục 2.1, học từ
   IBC). Dùng `CrossChainMessage.TimeoutTimestamp` (mới) so với `blockTime` THẬT của chain đích
   (tham số đã có sẵn trong `handleWrite`, lấy từ block header đã consensus-hoá — không phải wall-
   clock/`Duration`, đúng tinh thần Zero-Fork Invariant) — `ClaimMessage`/`VerifyAndExecute` finalize
   `MessageStatusFailedTimeout` thay vì `Success` một khi đã quá hạn, kích hoạt lại đúng pipeline
   failure-cert đã có sẵn (`MessageFailedCallback`→`MessageFailureAttestationWorker`) để nguồn
   `Refund()` được — tái dùng nguyên vẹn cơ chế đã audit cho payload-revert, không phải primitive
   mới. Việc này bắt đầu từ 1 bản WIP của agent khác có 3 bug thật (đã sửa): (a) `status` trả về từ
   `ClaimMessage` không được check trước khi mint/relay — 1 message hết hạn vẫn bị giao tiền thật
   trong khi ledger credit bị bỏ qua, tạo lệch sổ cái; (b) `FailedTimeout` early-return bỏ qua
   `saveGatewayEngine` + không emit `MessageStatusChanged` — trạng thái bị âm thầm mất, observer
   không thấy; (c) tính năng rate-limit đi kèm (xem mục 5 dưới) có lỗi kiến trúc gốc, đã bỏ hẳn. Chi
   tiết đầy đủ trong commit message `5ab51458`, không lặp lại ở đây.
5. **⚠️ ĐÃ THỬ, ĐÃ BỎ (2026-09-22) — phát hiện lỗi kiến trúc gốc, cần thiết kế lại chứ không phải
   chỉnh tham số.** Bản WIP ban đầu implement rate-limit bằng cách check
   `PerChainAllocation[destChainID]` (sai chain) ngay tại `outbound()` handler trên chain NGUỒN. Sửa
   thành `PerChainAllocation[engine.LocalChainID]` (đúng chain) vẫn KHÔNG chạy được — xác nhận bằng
   chính test suite hiện có: entry đó trên bản sao LOCAL của 1 chain thường (không phải Reserve)
   thường là 0, vì theo đúng kiến trúc đã audit trước đó (`CreditReserveAllocation`'s doc comment),
   ceiling thật của 1 chain X chỉ có ý nghĩa authoritative trên bản sao của RESERVE, không phải trên
   bản sao của chính X — check tại `outbound()` (chạy trên X, không phải Reserve) về bản chất luôn so
   với 1 con số gần như luôn = 0 → **chặn cứng gần như MỌI giao dịch outbound() có Value > 0** (xác
   nhận bằng hàng chục test thật FAIL ngay khi thêm check này). Đã gỡ bỏ hoàn toàn (không chỉ tắt) để
   không vô tình bị bật lại. **Thiết kế đúng cho tính năng này:** check phải chạy Ở RESERVE, bên
   trong `attestCommitInternal` (nơi ceiling thật `PerChainAllocation[sourceChainID]` đã được
   debit/check — mục 5.5.B của `shard_design_ton_real.md` mô tả đúng vị trí này cho thiết kế shard
   tương lai), không phải tại `outbound()` trên chain nguồn. Vẫn là quyết định kinh tế/vận hành
   (ngưỡng bao nhiêu %, theo cửa sổ thời gian nào) CẦN user chốt trước, cộng thêm giờ còn cần 1
   quyết định kiến trúc (đặt check ở attestCommitInternal thay vì outbound) — độ ưu tiên không đổi,
   nhưng phạm vi thực hiện lớn hơn ước tính ban đầu.

### 3.3 Không khuyến nghị (đã cân nhắc, không áp dụng)
- **Risk Management Network kiểu CCIP riêng biệt** (mục 2.2) — quá nặng so với quy mô mạng riêng
  hiện tại, và chính Chainlink cũng đang thu hẹp phạm vi tự động của nó.
- **Quadratic voting kiểu Axelar** (mục 2.3) — chỉ cần thiết nếu validator set trở thành
  permissionless; hiện tại mỗi chain tự quản committee riêng, mô hình đe doạ khác.
- **DAO governance/propose-vote-timelock** — đã từng có (`GovernanceEngine`), đã bị xoá theo yêu cầu
  explicit của người dùng trước đây. Không đề xuất lại trừ khi người dùng chủ động nêu lại.
- **Multisig admin pause kiểu Harmony's post-hack fix (4-of-5)** — dự án đã có cơ chế tốt hơn
  (`DeclareChainDeadWithCert`, cert-gated theo BLS quorum thay vì multisig cố định) — không cần lùi
  về pattern cũ hơn.

---

## Nguồn tham khảo

- [Wormhole hack — Halborn](https://www.halborn.com/blog/post/explained-the-wormhole-hack-february-2022)
- [Ronin bridge hack — Cointelegraph](https://cointelegraph.com/news/the-aftermath-of-axie-infinity-s-650m-ronin-bridge-hack)
- [Nomad bridge hack root cause — Nomad post-mortem](https://medium.com/nomad-xyz-blog/nomad-bridge-hack-root-cause-analysis-875ad2e5aacd)
- [Poly Network hack root cause — SlowMist](https://slowmist.medium.com/the-root-cause-of-poly-network-being-hacked-ec2ee1b0c68f)
- [Harmony Horizon bridge hack — Halborn](https://www.halborn.com/blog/post/explained-the-harmony-horizon-bridge-hack)
- [BNB Token Hub hack — QuillAudits](https://www.quillaudits.com/blog/hack-analysis/bsc-token-hub-bridge-hack)
- [Multichain collapse — Chainalysis](https://www.chainalysis.com/blog/multichain-exploit-july-2023/)
- [IBC v2 announcement](https://ibcprotocol.dev/blog/ibc-v2-announcement)
- [Chainlink CCIP Risk Management Network docs](https://docs.chain.link/ccip/concepts/architecture/offchain/risk-management-network)
- [Axelar threshold signature / stake accounting](https://www.axelar.network/blog/accounting-for-stake-in-threshold-signature-schemes)
- [LayerZero v1 Oracle/Relayer independence criticism — L2BEAT](https://medium.com/l2beat/circumventing-layer-zero-5e9f652a5d3e)
- [Circle CCTP deep dive — LI.FI](https://li.fi/knowledge-hub/circles-cross-chain-transfer-protocol-cctp-a-deep-dive)
- [Cross-chain bridge rate limiting / circuit breaker general pattern](https://cryptodaily.co.uk/2026/05/defi-risk-controls-circuit-breakers)
