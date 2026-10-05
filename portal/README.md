# 🌐 Metanode Portal — Account Gate & Ecosystem Gateway

A sleek, responsive, real-time Web3 Portal designed for **Metanode Core**. It provides seamless wallet onboarding for the **Parent Chain Account Registration Gate**, interactive transaction dispatch, cross-cluster rollup bridging, and multi-node zero-fork telemetry.

---

## ✨ Core Features

1. **🛡️ Parent Chain Account Gate Onboarding:**
   - Detects whether your secp256k1 wallet (MetaMask / Rabby) is registered on the Parent Chain.
   - Shows live gate status: 🟢 `PASS (Gate Open)` vs 🔴 `BLOCKED (Code 69: AccountNotRegistered)`.
   - **1-Click Onboarding:** Signs ECDSA registration intent and relays to Parent Chain `AccountRegistry`.

2. **💸 Transaction Dispatcher:**
   - Transfer funds natively on execution clusters.
   - Informs users if their account is restricted by the Gate before broadcasting.
   - Live transaction receipt inspection.

3. **🌉 Cross-Cluster Rollup Transfers:**
   - Bridge assets between Execution Cluster 1 and Execution Cluster 2.
   - 3-Stage visual timeline: `(1) Source Locked` ➔ `(2) Parent Relayed` ➔ `(3) Dest Claimed`.

4. **📊 Multi-Node Health & Zero-Fork Monitor:**
   - Real-time side-by-side status of Parent Chain (`:8547`), Exec 1 (`:8646`), and Exec 2 (`:8647`).
   - Verifies block heights, state roots, and zero-fork determinism.

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
| **Parent Chain** | `990` | `http://127.0.0.1:8547` | L1 Consensus & Account Registry |
| **Standalone / Master** | `1000` | `http://127.0.0.1:8747` | Standalone single-node chain |
