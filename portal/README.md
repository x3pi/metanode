# 🌐 Metanode Portal — Account Gate & Ecosystem Gateway

A sleek, responsive, real-time Web3 Portal designed for **Metanode Core**. It provides seamless wallet onboarding for the **Parent Chain Account Registration Gate**, interactive transaction dispatch, cross-cluster rollup bridging, and multi-node zero-fork telemetry compatible with **Chain ID 991 Cutover**, **Protobuf Wire**, and **BLS f+1 Co-Attestation**.

---

## ✨ Core Features

1. **🛡️ Parent Chain Account Gate Onboarding:**
   - Detects whether your secp256k1 wallet (MetaMask / Rabby) is registered on the Parent Chain.
   - Shows live gate status: 🟢 `PASS (Gate Open)` vs 🔴 `BLOCKED (Code 69: AccountNotRegistered)`.
   - **1-Click Onboarding:** Signs ECDSA registration intent and relays to Parent Chain `AccountRegistry` (Gasless via Execution Node Relay).
   - **Registry Lookup:** Real-time query tool to verify any `0x` address registration on the Parent Chain.

2. **💸 Transaction Dispatcher:**
   - Transfer funds natively on execution clusters using EIP-155 replay-protected transactions (Chain ID 991).
   - Pre-flight Account Gate Check prevents sending from unregistered wallets to avoid Code 69 failures.
   - Live transaction receipt inspection.

3. **🌉 Cross-Cluster Rollup Transfers:**
   - Bridge assets between Execution Clusters via Parent Chain Float Model &amp; BLS f+1 Co-Attestation.
   - Real RPC transfer execution via `mtn_sendCrossChainTransfer` or interactive simulation.
   - 3-Stage visual timeline: `(1) Source Locked` &rarr; `(2) Parent Relayed (f+1)` &rarr; `(3) Dest Claimed`.

4. **📊 Multi-Node Health & Zero-Fork Telemetry:**
   - Real-time side-by-side status of Parent Chain (`:18601`, `:8547`), Exec 1 (`:8646`), and Exec 2 (`:8647`).
   - Verifies block heights, state roots, and zero-fork determinism.
   - **Readiness Probes (`/readiness`):** Real-time HTTP 200 Ready vs 503 Service Unavailable detection.
   - **Committee Key Status (`/health`):** Verifies if validator key matches genesis committee or alerts on key mismatch.
   - **Prometheus Metrics Inspector:** Live dashboard for `master_validator_committee_key_valid`, `master_account_registration_pending_total`, and more.
   - **Custom Node Connect:** Add and probe any custom local or remote RPC endpoint with auto-detection.

---

## 🚀 Quick Start

### 1. Install Dependencies
```bash
cd portal
npm install
```

### 2. Start Local Development Server
```bash
npm run dev
```
Open [http://localhost:5173](http://localhost:5173) in your browser.

### 3. Build for Production
```bash
npm run build
npm run preview
```

---

## 🔗 Preset Cluster Endpoints

| Node / Cluster | Chain ID | RPC URL | Role |
| :--- | :--- | :--- | :--- |
| **Execution Cluster 1** | `991` | `http://127.0.0.1:8646` | Sharded EVM execution (Gate Enabled) |
| **Execution Cluster 2** | `991` | `http://127.0.0.1:8647` | Sharded EVM execution (Gate Enabled) |
| **Parent Chain L1** | `991` | `http://127.0.0.1:18601` | L1 Consensus & Account Registry |
| **Node-0 RPC** | `991` | `http://127.0.0.1:8545` | Local Single Node / Test RPC |
| **Ansible Exec 1** | `991` | `http://127.0.0.1:8747` | Multi-node Ansible Cluster Node |
| **Ansible Parent** | `991` | `http://127.0.0.1:8547` | Multi-node Ansible Parent Chain |
