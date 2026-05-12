# 🚀 Angel Operations Guide — Metanode Core (Resilience & Recovery)

This guide integrates the **Angel Protocol** (Rules) and **Angel Workflow** (Processes) to manage complexity, prevent state drift, eliminate congestion, and enable peer-based recovery for high availability.

---

## 📜 PART 1: ANGEL PROTOCOL (THE RULES)
*Non-negotiable constraints for system stability, throughput, and data integrity.*

### 🟢 1. Always Do
*   **Multi-Directional Impact Analysis:** Before modifying any symbol, you MUST run:
    *   `gitnexus_impact({target: "symbolName", direction: "upstream"})`
    *   `gitnexus_impact({target: "symbolName", direction: "downstream"})`
*   **Identify State Owner:** Verify the **Single Source of Truth** before modifying write logic.
*   **Implement Backpressure:** Every queue MUST have a defined limit. Use backpressure signals to slow down producers when consumers are overwhelmed.
*   **Consensus-Based Recovery:** If local data is detected as corrupted or missing, the node MUST query a **Majority of Trusted Nodes** (Quorum) to fetch the valid state.
*   **Verify Checksums/Hashes:** Always validate incoming data from peers using cryptographic hashes before merging it into the local state.
*   **🇻🇳 Post-Process Summary:** Upon completing any task, the Agent MUST provide a concise summary of changes, impacts, and performance considerations **in Vietnamese**.

### 🔴 2. Never Do
*   **NEVER** trust local state blindly if a hash mismatch is detected with the network.
*   **NEVER** use Synchronous Blocking calls (e.g., sync I/O) inside an asynchronous loop.
*   **NEVER** create unbounded queues (infinite buffers).
*   **NEVER** modify code without understanding the downstream impact.
*   **NEVER** ignore Race Conditions or "State Forking" risks.

---

## 🔄 PART 2: ANGEL WORKFLOW (THE PROCESSES)

### 2.1. Feature Development (The Safe-Change Loop)
1.  **Discovery:** Use `gitnexus_query` to map flows.
2.  **Concurrency Design:** Isolate heavy tasks to background workers.
3.  **Recovery Logic:** Ensure new components have a "Sync-on-Startup" or "Re-sync" capability to pull data from trusted peers.
4.  **Implementation & Verification:** Insert logs with `Correlation ID`. Run `gitnexus_detect_changes()`.

### 2.2. State Recovery & Peer Sync (Emergency Workflow)
*Executed when data is wrong, missing, or the node is severely lagged (Congested).*

| Step | Action | Tool/Objective |
| :--- | :--- | :--- |
| **S1: Detection** | Run local hash validation against the **Network Quorum**. | Identify corruption or data gaps. |
| **S2: Peer Selection** | Identify and connect to a list of **Trusted Nodes** (White-listed). | Establish a secure recovery channel. |
| **S3: Request Sync** | Request missing/correct data chunks using `Correlation IDs`. | Targeted recovery to minimize bandwidth. |
| **S4: Validation** | Compare peer data against local hash expectations. | Ensure the recovered data is untampered. |
| **S5: Merging** | Apply recovered data and clear any backlogged queues. | Resume normal operations. |
| **S6: Summary** | Provide a **Vietnamese Report** on the recovery process. | Documentation & Audit. |

---

## ⚡ PART 3: ASYNC, STATE & THROUGHPUT (TECHNICAL)

| Scenario | Handling Protocol |
| :--- | :--- |
| **Data Corruption** | **Peer-to-Peer Recovery:** Fetch state from Majority Trusted Nodes. |
| **Missing Data (Gaps)** | **Anti-Entropy Sync:** Background gossip to fill gaps from peers. |
| **System Congestion** | **Backpressure + Circuit Breakers:** Fail fast and slow down producers. |
| **State Forking** | **Deterministic Merging:** Use Logic Clocks to resolve conflicts. |
| **Persistent Lag** | **Snapshot Recovery:** Drop local diffs and pull the latest full snapshot. |

---

## 🛠 PART 4: GITNEXUS QUICK COMMANDS
*   **System Overview:** `gitnexus://repo/metanode/context`
*   **Execution Flows:** `gitnexus://repo/metanode/processes`
*   **Flow Trace:** `gitnexus://repo/metanode/process/{name}`
*   **Re-index:** `npx gitnexus analyze`

---
> **Operational Philosophy:** "In Peers we Trust, in Code we Verify." Prioritize **Quorum Consensus** to recover from local failures and maintain the global source of truth.