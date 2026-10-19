# MetaNode Execution Docs

Tài liệu kỹ thuật tầng Go execution (`execution/`). Điều hướng đầy đủ nằm trong `mkdocs.yml`; sơ đồ module tổng thể xem [`PROJECT_STRUCTURE.md`](../../PROJECT_STRUCTURE.md) và [`EXECUTION_GUIDE.md`](../EXECUTION_GUIDE.md).

- **Architecture:** [account state](architecture/account-state-architecture.md) · [NOMT storage](architecture/nomt-storage-architecture.md) · [socket protocol](architecture/socket-protocol.md) · [Xapian commit flow](architecture/xapian-db-commit-flow.md)
- **Core concepts:** [block signing](core-concepts/block-signing.md) · [flat vs MPT](core-concepts/flat-vs-mpt-comparison.md) · [fork safety](core-concepts/transaction-flow-fork-safety-analysis.md)
- **Performance:** [high throughput](performance-scaling/high-throughput-optimization.md) · [TPS analysis](performance-scaling/tps-analysis.md)
- **Operations:** [snapshot](operations/snapshot-guide.md) · [thêm node](operations/adding-new-node.md) · [explorer](operations/explorer.md)
- **Diagnostics / tooling:** [realtime debug](diagnostics-guides/realtime-debug-guide.md) · [debug RPC](diagnostics-guides/debug-rpc.md) · [TPS blast](tooling/tps-blast-test-guide-vn.md)
