# 🚀 Angel Operations Guide — Metanode Core

This guide integrates the **Angel Protocol** (Rules) and **Angel Workflow** (Processes) to manage complexity in asynchronous systems and prevent state drift or unauthorized forks.

---

## 📜 PART 1: ANGEL PROTOCOL (THE RULES)
*Non-negotiable constraints. Violation of these rules directly threatens system stability.*

### 🟢 1. Always Do
*   **Multi-Directional Impact Analysis:** Before modifying any symbol, you MUST run:
    *   `gitnexus_impact({target: "symbolName", direction: "upstream"})` to identify callers.
    *   `gitnexus_impact({target: "symbolName", direction: "downstream"})` to predict side effects in async flows.
*   **Guarantee Idempotency:** Ensure all state-changing logic can be retried safely without altering the final result.
*   **Identify State Owner:** Verify which component holds the **Single Source of Truth** before modifying any write logic.
*   **Maintain Correlation IDs:** Every new asynchronous process must inherit or generate a trace ID for cross-node debugging.
*   **Verify Blast Radius:** Always run `gitnexus_detect_changes()` before committing.
*   **🇻🇳 Post-Process Summary:** Upon completing any task, the Agent MUST provide a concise summary of the changes, impacts, and status **in Vietnamese** to ensure clear communication with the user.

### 🔴 2. Never Do
*   **NEVER** modify code without understanding the downstream impact (async consequences).
*   **NEVER** use regex or "find-and-replace" for renames. Use `gitnexus_rename` to maintain call-graph integrity.
*   **NEVER** ignore Race Conditions. Always assume events or packets may arrive out of chronological order.
*   **NEVER** create redundant local states that lead to **State Forking**.
*   **NEVER** commit changes if `gitnexus_detect_changes()` shows unexpected impact on unrelated execution flows.

---

## 🔄 PART 2: ANGEL WORKFLOW (THE PROCESSES)
*Operational steps to execute the protocol.*

### 2.1. Feature Development (The Safe-Change Loop)
1.  **Discovery:** Use `gitnexus_query` to find execution flows and `gitnexus_impact` to map affected nodes.
2.  **Conflict-Free Design:** Implement idempotent mechanisms (version checks/request IDs) if modifying the State Owner.
3.  **Implementation & Verification:** Insert logs with `Correlation ID`. Run `gitnexus_detect_changes()`.
4.  **Completion:** The Agent generates a **Vietnamese Summary** of the implementation.

### 2.2. Conflict Resolution Workflow
| Step | Action | Tool/Objective |
| :--- | :--- | :--- |
| **S1: Isolation** | Suspend write operations on the conflicting data segment. | Prevent further contamination. |
| **S2: Trace** | Use `Correlation ID` to trace the event sequence. | Identify "Node Zero". |
| **S3: Compare** | Compare state snapshots between forked nodes. | Determine the Source of Truth. |
| **S4: Reconcile** | Apply Overwrite or Manual Merge logic. | Restore a unified state. |
| **S5: Summary** | Provide a **Vietnamese Report** on how the conflict was resolved. | Communication & Records. |

---

## ⚡ PART 3: ASYNC & STATE MANAGEMENT (TECHNICAL)

| Scenario | Handling Protocol |
| :--- | :--- |
| **Race Condition** | Use **Logic Clocks (Lamport)** or **State Machines** to block invalid transitions. |
| **State Forking** | Apply conflict resolution strategies like *Last Write Wins* or *Deterministic Merging*. |
| **Event Callbacks** | Always perform an **"Is Still Valid"** check at the start of the callback execution. |

---

## 📝 PART 4: ARCHITECTURE DECISION RECORDS (ADR)
All changes to State Management architecture must document:
*   **Context:** Why is the change necessary?
*   **Decision:** What is the chosen solution?
*   **Consequences:** Trade-offs (e.g., increased latency for guaranteed consistency).

---

## 🛠 PART 5: GITNEXUS QUICK COMMANDS
*   **System Overview:** `gitnexus://repo/metanode/context`
*   **Execution Flows:** `gitnexus://repo/metanode/processes`
*   **Flow Trace:** `gitnexus://repo/metanode/process/{name}`
*   **Re-index Index:** `npx gitnexus analyze`

---
> **Operational Philosophy:** "Better late than wrong." Prioritize **Consistency** over **Availability** in the Metanode core modules.