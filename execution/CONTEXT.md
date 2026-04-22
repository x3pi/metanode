# `mtn-simple-2025` Context (Go Execution Engine)

## Overview
This is the **Go Exec 层 / Layer** of the MetaNode architecture. It handles transaction execution, State Database (MPT/Nomt), and Smart Contracts.

## The Dual Go-Node Architecture (Master & Sub)
This Go project is designed to be run in two distinct modes per logical validator node:

1. **Go Master Node** (`is_master=true`):
   - Receives strictly ordered blocks from the **Rust Consensus** (`mtn-consensus`) via UDS.
   - Executes transactions and calculates state roots.
   - Sends state updates (`AccountBatch`) down to the Sub Node.
   
2. **Go Sub Node** (`is_master=false`):
   - Dedicated process for handling client RPCs/WebSockets.
   - Receives state updates from the Master. It does not run transactions locally to save CPU.
   - Forwards newly submitted TXs to the Rust Consensus for ordering.

## Core Responsibilities
- **BlockProcessor**: The central orchestrator for transaction execution, syncing IPC messages from Rust, and Master-Sub synchronization.
- **StateDB**: Manages account balances, nonces, and smart contract states.

## Context Management Tip
Whenever you (Antigravity AI) work in this repository, always remember that timestamps and transaction order come strictly from Rust. Do not attempt to implement separate ordering logic in Go.

Please refer to `../META_NODE_ARCHITECTURE.md` for the overarching 3-part node topology.
