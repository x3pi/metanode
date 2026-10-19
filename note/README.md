# MetaNode Engineering Notes, Architecture & Evidence Ledger (`note/`)

Thư mục này lưu trữ sổ cái kiểm chứng kỹ thuật (`CLAIMS_LEDGER.md`), các gói bằng chứng thực nghiệm (`evidence/`), tài liệu thiết kế kiến trúc cốt lõi, sổ tay vận hành chuyên biệt (Runbooks) và hồ sơ phân tích lỗi (Postmortems).

---

## 1. Sổ Cái Bằng Chứng & Báo Cáo Đo Đạc (`CLAIMS_LEDGER.md` & `evidence/`)

- **[`CLAIMS_LEDGER.md`](./CLAIMS_LEDGER.md):** Sổ cái tổng hợp toàn bộ các khẳng định kỹ thuật và số liệu hiệu năng đã được kiểm chứng thực nghiệm (`RE-VERIFIED`, `EXPLAINED`, `RESOLVED`, `OPEN`).
- **[`system_assessment_20261019.md`](./system_assessment_20261019.md):** Đánh giá tổng thể điểm mạnh và điểm yếu của hệ thống (gắn nhãn `[ĐO]`/`[ĐỌC CODE]`/`[GIẢ THUYẾT]`/`[CHƯA ĐO]`, trích từ `CLAIMS_LEDGER.md`).
- **[`evidence/`](./evidence/):** Các gói dữ liệu thô (`MANIFEST.json`, `PREREGISTERED.md`, log đo A/B, pprof profiles) được xác thực tự động bằng `execution/scripts/test/evidence/verify_evidence.py --strict`.
- **Các báo cáo đo đạc chính:**
  - [`readloop_gate_explorer_pacing_report_20261016.md`](./readloop_gate_explorer_pacing_report_20261016.md) — Backpressure lớp sớm tại `Connection.readLoop` (`ConnRequestChanSize = 16`), điều tiết `indexSingleBlock` -> `s.indexQueue` (`ExplorerQueueSize = 65536`, chờ tối đa `50ms`) và gom lô `explorerSvc.Commit()` mỗi 64 block.
  - [`cgocall_backpressure_explorer_report_20261015.md`](./cgocall_backpressure_explorer_report_20261015.md) — Phân rã `runtime.cgocall`, tối ưu 1-call CGO `goxapian`, JSON Explorer tĩnh và Backpressure lớp nhận Tx (`ReceiptTracker.InFlightTxCount`).
  - [`backlog_gc_followup_report_20261015.md`](./backlog_gc_followup_report_20261015.md) — Thu gọn đối tượng giữ trên heap Go (`ExecuteSCResult`, 64-shard caches) và đo A/B ở mức tải 1M & 4M tx.
  - [`node_profile_backlog_raft_report_20261014.md`](./node_profile_backlog_raft_report_20261014.md) & [`node_ab_old_vs_new_report_20261014.md`](./node_ab_old_vs_new_report_20261014.md) — Đối chiếu hiệu năng BFT/Raft và phân tích backlog.
  - [`tps_by_volume_report_20261014.md`](./tps_by_volume_report_20261014.md), [`tps_counted_report_20261013.md`](./tps_counted_report_20261013.md), [`tps_ingress_rootcause_report_20261013.md`](./tps_ingress_rootcause_report_20261013.md).

---

## 2. Kiến Trúc Cốt Lõi & Thiết Kế Hệ Thống

- **Đồng thuận & Khởi động/Đồng bộ:**
  - [`NODE_RESTART_ARCHITECTURE.md`](./NODE_RESTART_ARCHITECTURE.md) & [`startup_architecture.md`](./startup_architecture.md)
  - [`startup_sync_commit_index_import_fork_design_2026-09.md`](./startup_sync_commit_index_import_fork_design_2026-09.md)
  - [`consensus_local_dag_trust_gap_design_2026-09.md`](./consensus_local_dag_trust_gap_design_2026-09.md)
  - [`bft_consensus_delivery_guarantee.md`](./bft_consensus_delivery_guarantee.md) & [`bft_fault_tolerance_node_count.md`](./bft_fault_tolerance_node_count.md)
  - [`recover_sync_catchup_flow.md`](./recover_sync_catchup_flow.md)
- **Thực thi Giao dịch, Block-STM & Lưu trữ:**
  - [`TRANSACTION_PROCESSING_ARCHITECTURE.md`](./TRANSACTION_PROCESSING_ARCHITECTURE.md) & [`block_creation_architecture.md`](./block_creation_architecture.md)
  - [`block_stm_iterative_architecture.md`](./block_stm_iterative_architecture.md) & [`block_stm_architecture_review.md`](./block_stm_architecture_review.md)
  - [`real_vs_virtual_execution_isolation.md`](./real_vs_virtual_execution_isolation.md) & [`transaction_state_isolation.md`](./transaction_state_isolation.md)
  - [`design_backlog_bound_20261015.md`](./design_backlog_bound_20261015.md) & [`design_bounded_memory_indexes_20261007.md`](./design_bounded_memory_indexes_20261007.md)
  - [`explorer_history_sync_architecture.md`](./explorer_history_sync_architecture.md) & [`xapian/xapian_lifecycle_walkthrough.md`](./xapian/xapian_lifecycle_walkthrough.md)
- **Xuyên chuỗi (Cross-Chain), Parent Chain & Mạng:**
  - [`cross_chain_root_anchor_architecture.md`](./cross_chain_root_anchor_architecture.md) & [`cross_chain/`](./cross_chain/)
  - [`secp256k1_proto_tcp_architecture_design.md`](./secp256k1_proto_tcp_architecture_design.md) & [`tcp_eth_client_guide.md`](./tcp_eth_client_guide.md)
  - [`tee_master_architecture.md`](./tee_master_architecture.md) & [`tee_dual_mode_execution_plan.md`](./tee_dual_mode_execution_plan.md)

---

## 3. Sổ Tay Vận Hành Chuyên Biệt (Runbooks) & Postmortems

- [`runbook_parent_chain_multinode.md`](./runbook_parent_chain_multinode.md) — Vận hành cụm đa node Parent Chain.
- [`runbook_root_anchor_genesis_ceremony.md`](./runbook_root_anchor_genesis_ceremony.md) — Quy trình khởi tạo Genesis và Ủy ban Root Anchor.
- [`runbook_deploy_audit_rounds_and_stm_fix_20261019.md`](./runbook_deploy_audit_rounds_and_stm_fix_20261019.md) — Triển khai đợt sửa execution (audit round 2/3, Block-STM `c96640f4`) và thay đổi RPC: chọn quy trình, pre-flight, Go/No-Go, rollback.
- [`runbook_chain_death_recovery.md`](./runbook_chain_death_recovery.md) — Quy trình cứu hộ khẩn cấp khi một chuỗi ngừng hoạt động.
- [`known_bugs/`](./known_bugs/) — Hồ sơ phân tích nguyên nhân gốc (Root Cause Postmortems) của các lỗi đồng bộ/fork lịch sử đã được khắc phục.
