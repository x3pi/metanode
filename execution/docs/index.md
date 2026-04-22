# Meta Node Architecture & Technical Documentation

Welcome to the **Meta Node** technical documentation. This site serves as the central knowledge base for running, debugging, and understanding the architecture of the `mtn-simple-2025` project.

## 📌 Getting Started

If you are new to the Meta Node ecosystem, begin with these introductory documents:
- 📖 **[Application Overview](01-introduction/app-overview.md)**: A high-level perspective of the blockchain's components.
- 🚀 **[Getting Started Guide](01-introduction/getting-started.md)**: Steps to spin up and run the chain nodes.

---

## 🏗️ Technical Resources

The documentation is organized into logical categories to help you navigate:

### 1. Architecture
Understand the core mechanisms that power the network:
- [Account State Architecture](02-architecture/account-state-architecture.md)
- [QUIC Stream Implementation](02-architecture/quic--stream--architecture.md)
- [Socket Protocol](02-architecture/socket-protocol.md)

### 2. Core Concepts
Deep dives into the technical foundation of Meta Node:
- [Transaction Flow](03-core-concepts/transaction-flow.md)
- [Block Signing & Consensus](03-core-concepts/block-signing.md)
- [Merkle Tree vs Flat DB](03-core-concepts/flat-vs-mpt-comparison.md)

### 3. Performance & Scaling
Read about how we optimized the node to handle high TPS workloads:
- [High Throughput Optimizations](04-performance-scaling/high-throughput-optimization.md)
- [Scaling to Hundreds of Thousands](04-performance-scaling/scaling-to-hundreds-of-thousands.md)

### 4. Operations & Diagnostics
Guides tailored for devops, node operators, and testers debugging on the network:
- [Snapshot & Backup Management](05-operations/snapshot-guide.md)
- [Testing & TPS Blast](07-tooling/tps-blast-test-guide-vn.md)
- [Realtime Debugging Guide](06-diagnostics-guides/realtime-debug-guide.md)

---
*Generated via MkDocs Material Theme*
