package abi_contract

// GatewayABI is the ABI surface for GatewayPrecompile (GATEWAY_CONTRACT_ADDRESS, 0x1002).
//
// This is a flattened encoding of the structs described in
// note/cross_chain_root_anchor_architecture.md mục 11.2/11.3 (CrossChainMessage, QuorumCert,
// MerkleProof) — each struct field is passed as its own parameter instead of a Solidity tuple.
// Per mục 11.1, the ABI shape in the design doc is descriptive, not a literal contract to
// replicate; flattening avoids go-ethereum's reflection-based tuple decoding (this codebase has
// no existing precedent for decoding tuple/struct ABI parameters — ValidatorHandler, the closest
// template, only ever decodes scalar parameters) for this first, wiring-focused milestone.
//
// Scope for Milestone A of the Root Anchor wiring plan: outbound(), attestCommit(),
// claimMessage(), refund(), and the 3 view methods. verifyAndExecute() and claimDeadChainBalance()
// are deferred — they follow the exact same pattern and can be added once this foundation is
// proven, without touching what's already wired.
//
// getChainRegistry() was added in Milestone B: it lets a remote chain read this chain's
// ChainRegistry entry for a given chainId over eth_call, the read half of the Go↔Root Anchor RPC
// channel (see execution/pkg/cross_chain/rootanchor). Same flattened-array convention as above —
// parallel arrays instead of a Solidity tuple/struct array for the committee.
//
// registerCommitteePop/getRegisteredPop/submitCommitteeAttestation/getCommitteeAttestationShares/
// committeeUpdate were added in Milestone C: a real multi-validator BLS quorum-cert production
// pipeline for CommitteeUpdate, using Root Anchor itself as the rendezvous point for individual
// signature shares (no new P2P, no Rust changes — see
// execution/pkg/blockchain/tx_processor/committee_attestation_worker.go and
// execution/pkg/cross_chain/epoch_sync.go's ComputeCommitteeUpdateDigest).
//
// batchOutboundCommit/getPendingOutboundCount/getCommitBatch/CommitBatched (2026-08-26, P4
// relayer automation) close a real gap found while building an automated RelayerDaemon watch
// loop: CommitAttestationWorker (Milestone F, committee_attestation_worker.go's sibling
// commit_attestation_worker.go) has always existed to BLS-sign a commit root and collect shares
// on Root Anchor, but its OnCommitFinalized() trigger was never called from anywhere in
// production -- there was no real mechanism that ever decided "these N pending outbound()
// messages now form a committed batch" in the first place. batchOutboundCommit(destChainId)
// (permissionless, like committeeUpdate()) takes every message currently queued for that one
// destination (scoped per-destination deliberately -- see GatewayEngine.PendingOutboundMessages'
// doc comment for why: batching multiple destinations under one AggregateValueLeaf would let
// each destination's own independent attestCommit() call debit the same source allocation
// against its own local ledger copy), builds a real commit tree, and both returns and durably
// records (getCommitBatch) the exact message list + signing epoch behind the resulting
// commitRoot -- letting the local CommitAttestationWorker sign it (wired in
// block_processor_core.go) and any relayer (including after a restart) deterministically
// rebuild the same Merkle proofs via BuildCommitTree(messages) without needing to also invent a
// separate proof-storage format.
//
// gasFee (outbound/claimMessage/refund/verifyAndExecute) was added per mục 2.6.5: a native-coin
// amount locked at outbound() time to pay for CONTRACT_CALL execution at the destination chain —
// closes the "unbounded/free gas for inbound CONTRACT_CALL -> DoS" risk (mục 5.3 risk #9).
// isContractCall()==true with gasFee==0 fails closed rather than running for free. Unused gas
// (gasFee minus real EVM gas actually consumed, priced at mt_common.MINIMUM_BASE_FEE) is minted
// back to msg.Sender on the destination chain in the same claim transaction — a deliberate
// simplification of the doc's literal "hoàn qua message refund" wording (which would require a
// brand-new B->A reverse-attestation message type); this keeps the supply invariant identical
// (nothing is minted beyond what was burned at outbound()) at the cost of the leftover landing on
// the destination chain rather than back on the source chain. See gateway_handler.go's
// executeContractCallForGateway call sites for the settlement logic.
//
// registerChainViaStake() solves the same "chain #1 has no vote path" circular dependency a
// deleted bootstrapFoundingChains() method (retired 2026-08-28) and a deleted vote-gated
// ProposalRegisterChain kind (retired 2026-09-04, along with the whole GovernanceEngine it and
// every other proposal kind used to run on — see cross_chain.GatewayEngine.RecoveryCommittee's own
// doc comment) used to solve, each in its own way: a Root Anchor starts with zero ChainRegistry
// entries (NewGatewayEngine is always called with an empty registry — see gateway_handler.go's
// loadGatewayEngine), so nothing can bootstrap the very first chain's own membership.
// registerChainViaStake() is vote-free and per-chain
// (not a batch), gated instead by a REAL native-coin deposit from the caller's own wallet
// (gateway_handler.go's "registerChainViaStake" case checks+burns it, see
// GatewayEngine.MinNativeStakeToRegister's own doc comment) — usable identically for chain #1 and
// every chain after it, which is what let
// bootstrapFoundingChains() (and its >= MinFoundingChains batch requirement) be retired entirely.
const GatewayABI = `[
	{
		"inputs": [
			{"internalType": "uint256", "name": "destChainId", "type": "uint256"},
			{"internalType": "address", "name": "target", "type": "address"},
			{"internalType": "bytes", "name": "payload", "type": "bytes"},
			{"internalType": "uint256", "name": "assetId", "type": "uint256"},
			{"internalType": "uint256", "name": "value", "type": "uint256"},
			{"internalType": "uint256", "name": "tip", "type": "uint256"},
			{"internalType": "uint256", "name": "gasFee", "type": "uint256"},
			{"internalType": "uint8", "name": "hopCount", "type": "uint8"},
			{"internalType": "bool", "name": "ordered", "type": "bool"}
		],
		"name": "outbound",
		"outputs": [{"internalType": "bytes32", "name": "messageId", "type": "bytes32"}],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "sourceChainId", "type": "uint256"},
			{"internalType": "bytes32", "name": "commitRoot", "type": "bytes32"},
			{"internalType": "uint256", "name": "aggregateAmount", "type": "uint256"},
			{"internalType": "uint256", "name": "assetId", "type": "uint256"},
			{"internalType": "uint256", "name": "proofLeafIndex", "type": "uint256"},
			{"internalType": "bytes32[]", "name": "proofSiblings", "type": "bytes32[]"},
			{"internalType": "uint64", "name": "certEpoch", "type": "uint64"},
			{"internalType": "bytes", "name": "certAggregateSignature", "type": "bytes"},
			{"internalType": "bytes", "name": "certSignerBitmap", "type": "bytes"}
		],
		"name": "attestCommit",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "reserveChainId", "type": "uint256"},
			{"internalType": "bytes32", "name": "commitRoot", "type": "bytes32"},
			{"internalType": "uint256", "name": "aggregateAmount", "type": "uint256"},
			{"internalType": "uint256", "name": "assetId", "type": "uint256"},
			{"internalType": "uint256", "name": "proofLeafIndex", "type": "uint256"},
			{"internalType": "bytes32[]", "name": "proofSiblings", "type": "bytes32[]"},
			{"internalType": "uint64", "name": "certEpoch", "type": "uint64"},
			{"internalType": "bytes", "name": "certAggregateSignature", "type": "bytes"},
			{"internalType": "bytes", "name": "certSignerBitmap", "type": "bytes"}
		],
		"name": "attestReserveIssuedCommit",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "bytes32", "name": "messageId", "type": "bytes32"},
			{"internalType": "uint256", "name": "sourceChainId", "type": "uint256"},
			{"internalType": "uint256", "name": "destChainId", "type": "uint256"},
			{"internalType": "uint256", "name": "sequence", "type": "uint256"},
			{"internalType": "uint8", "name": "hopCount", "type": "uint8"},
			{"internalType": "address", "name": "sender", "type": "address"},
			{"internalType": "address", "name": "target", "type": "address"},
			{"internalType": "uint256", "name": "assetId", "type": "uint256"},
			{"internalType": "uint256", "name": "value", "type": "uint256"},
			{"internalType": "bytes", "name": "payload", "type": "bytes"},
			{"internalType": "uint256", "name": "tip", "type": "uint256"},
			{"internalType": "uint256", "name": "gasFee", "type": "uint256"},
			{"internalType": "bool", "name": "ordered", "type": "bool"},
			{"internalType": "uint256", "name": "proofLeafIndex", "type": "uint256"},
			{"internalType": "bytes32[]", "name": "proofSiblings", "type": "bytes32[]"},
			{"internalType": "bytes32", "name": "commitRoot", "type": "bytes32"}
		],
		"name": "claimMessage",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "bytes32", "name": "messageId", "type": "bytes32"},
			{"internalType": "uint256", "name": "sourceChainId", "type": "uint256"},
			{"internalType": "uint256", "name": "destChainId", "type": "uint256"},
			{"internalType": "uint256", "name": "sequence", "type": "uint256"},
			{"internalType": "uint8", "name": "hopCount", "type": "uint8"},
			{"internalType": "address", "name": "sender", "type": "address"},
			{"internalType": "address", "name": "target", "type": "address"},
			{"internalType": "uint256", "name": "assetId", "type": "uint256"},
			{"internalType": "uint256", "name": "value", "type": "uint256"},
			{"internalType": "bytes", "name": "payload", "type": "bytes"},
			{"internalType": "uint256", "name": "tip", "type": "uint256"},
			{"internalType": "uint256", "name": "gasFee", "type": "uint256"},
			{"internalType": "bool", "name": "ordered", "type": "bool"},
			{"internalType": "uint256", "name": "proofLeafIndex", "type": "uint256"},
			{"internalType": "bytes32[]", "name": "proofSiblings", "type": "bytes32[]"},
			{"internalType": "bytes32", "name": "commitRoot", "type": "bytes32"}
		],
		"name": "creditReserveAllocation",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "bytes32", "name": "messageId", "type": "bytes32"},
			{"internalType": "uint256", "name": "sourceChainId", "type": "uint256"},
			{"internalType": "uint256", "name": "destChainId", "type": "uint256"},
			{"internalType": "uint256", "name": "sequence", "type": "uint256"},
			{"internalType": "uint8", "name": "hopCount", "type": "uint8"},
			{"internalType": "address", "name": "sender", "type": "address"},
			{"internalType": "address", "name": "target", "type": "address"},
			{"internalType": "uint256", "name": "assetId", "type": "uint256"},
			{"internalType": "uint256", "name": "value", "type": "uint256"},
			{"internalType": "bytes", "name": "payload", "type": "bytes"},
			{"internalType": "uint256", "name": "tip", "type": "uint256"},
			{"internalType": "uint256", "name": "gasFee", "type": "uint256"},
			{"internalType": "bool", "name": "ordered", "type": "bool"},
			{"internalType": "uint256", "name": "proofLeafIndex", "type": "uint256"},
			{"internalType": "bytes32[]", "name": "proofSiblings", "type": "bytes32[]"},
			{"internalType": "bytes32", "name": "commitRoot", "type": "bytes32"},
			{"internalType": "uint64", "name": "failEpoch", "type": "uint64"},
			{"internalType": "bytes", "name": "failAggregateSignature", "type": "bytes"},
			{"internalType": "bytes", "name": "failSignerBitmap", "type": "bytes"}
		],
		"name": "refund",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [{"internalType": "bytes32", "name": "messageId", "type": "bytes32"}],
		"name": "getMessageStatus",
		"outputs": [{"internalType": "uint8", "name": "status", "type": "uint8"}],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "getOriginalSender",
		"outputs": [
			{"internalType": "address", "name": "sender", "type": "address"},
			{"internalType": "uint256", "name": "sourceChainId", "type": "uint256"}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "isCalledByGateway",
		"outputs": [{"internalType": "bool", "name": "result", "type": "bool"}],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [{"internalType": "uint256", "name": "chainId", "type": "uint256"}],
		"name": "getChainRegistry",
		"outputs": [
			{"internalType": "bool", "name": "exists", "type": "bool"},
			{"internalType": "bytes[]", "name": "committeePubkeys", "type": "bytes[]"},
			{"internalType": "uint64[]", "name": "committeeStakes", "type": "uint64[]"},
			{"internalType": "bytes[]", "name": "committeePopSignatures", "type": "bytes[]"},
			{"internalType": "uint64", "name": "epoch", "type": "uint64"},
			{"internalType": "uint64", "name": "quorumThreshold", "type": "uint64"},
			{"internalType": "address", "name": "gatewayContract", "type": "address"},
			{"internalType": "bytes32", "name": "stateRoot", "type": "bytes32"},
			{"internalType": "bytes32", "name": "accountTreeRoot", "type": "bytes32"},
			{"internalType": "string", "name": "archivalEndpoint", "type": "string"},
			{"internalType": "uint64", "name": "registeredAt", "type": "uint64"},
			{"internalType": "address", "name": "genesisWallet", "type": "address"},
			{"internalType": "bytes32", "name": "genesisDigest", "type": "bytes32"}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [{"internalType": "uint256", "name": "chainId", "type": "uint256"}],
		"name": "getAllocation",
		"outputs": [{"internalType": "uint256", "name": "allocation", "type": "uint256"}],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "getMinNativeStakeToRegister",
		"outputs": [{"internalType": "uint256", "name": "minStake", "type": "uint256"}],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "chainId", "type": "uint256"},
			{"internalType": "bytes32", "name": "digest", "type": "bytes32"}
		],
		"name": "setGenesisDigest",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "bytes", "name": "pubkeyBls", "type": "bytes"},
			{"internalType": "bytes", "name": "popSignature", "type": "bytes"}
		],
		"name": "registerCommitteePop",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [{"internalType": "bytes", "name": "pubkeyBls", "type": "bytes"}],
		"name": "getRegisteredPop",
		"outputs": [{"internalType": "bytes", "name": "popSignature", "type": "bytes"}],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "sourceChainId", "type": "uint256"},
			{"internalType": "uint64", "name": "oldEpoch", "type": "uint64"},
			{"internalType": "bytes32", "name": "payloadHash", "type": "bytes32"},
			{"internalType": "bytes", "name": "signerPubkeyBls", "type": "bytes"},
			{"internalType": "bytes", "name": "signature", "type": "bytes"}
		],
		"name": "submitCommitteeAttestation",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "sourceChainId", "type": "uint256"},
			{"internalType": "uint64", "name": "oldEpoch", "type": "uint64"},
			{"internalType": "bytes32", "name": "payloadHash", "type": "bytes32"}
		],
		"name": "getCommitteeAttestationShares",
		"outputs": [
			{"internalType": "bytes[]", "name": "pubkeys", "type": "bytes[]"},
			{"internalType": "bytes[]", "name": "signatures", "type": "bytes[]"}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "sourceChainId", "type": "uint256"},
			{"internalType": "uint64", "name": "epoch", "type": "uint64"},
			{"internalType": "bytes32", "name": "commitRoot", "type": "bytes32"},
			{"internalType": "bytes", "name": "signerPubkeyBls", "type": "bytes"},
			{"internalType": "bytes", "name": "signature", "type": "bytes"}
		],
		"name": "submitCommitAttestation",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "sourceChainId", "type": "uint256"},
			{"internalType": "uint64", "name": "epoch", "type": "uint64"},
			{"internalType": "bytes32", "name": "commitRoot", "type": "bytes32"}
		],
		"name": "getCommitAttestationShares",
		"outputs": [
			{"internalType": "bytes[]", "name": "pubkeys", "type": "bytes[]"},
			{"internalType": "bytes[]", "name": "signatures", "type": "bytes[]"}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "destChainId", "type": "uint256"},
			{"internalType": "bytes32", "name": "messageId", "type": "bytes32"},
			{"internalType": "uint64", "name": "epoch", "type": "uint64"},
			{"internalType": "bytes", "name": "signerPubkeyBls", "type": "bytes"},
			{"internalType": "bytes", "name": "signature", "type": "bytes"}
		],
		"name": "submitMessageFailureAttestation",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "destChainId", "type": "uint256"},
			{"internalType": "bytes32", "name": "messageId", "type": "bytes32"},
			{"internalType": "uint64", "name": "epoch", "type": "uint64"}
		],
		"name": "getMessageFailureAttestationShares",
		"outputs": [
			{"internalType": "bytes[]", "name": "pubkeys", "type": "bytes[]"},
			{"internalType": "bytes[]", "name": "signatures", "type": "bytes[]"}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "sourceChainId", "type": "uint256"},
			{"internalType": "uint64", "name": "newEpoch", "type": "uint64"},
			{"internalType": "bytes[]", "name": "newCommitteePubkeys", "type": "bytes[]"},
			{"internalType": "uint64[]", "name": "newCommitteeStakes", "type": "uint64[]"},
			{"internalType": "bytes[]", "name": "newCommitteePopSignatures", "type": "bytes[]"},
			{"internalType": "uint64", "name": "quorumThreshold", "type": "uint64"},
			{"internalType": "bytes32", "name": "stateRoot", "type": "bytes32"},
			{"internalType": "bytes32", "name": "accountTreeRoot", "type": "bytes32"},
			{"internalType": "bytes32", "name": "payloadHash", "type": "bytes32"},
			{"internalType": "bytes[]", "name": "aggPubkeys", "type": "bytes[]"},
			{"internalType": "bytes", "name": "aggSignature", "type": "bytes"}
		],
		"name": "committeeUpdate",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "destChainId", "type": "uint256"}
		],
		"name": "batchOutboundCommit",
		"outputs": [
			{"internalType": "bytes32", "name": "commitRoot", "type": "bytes32"},
			{"internalType": "uint256", "name": "messageCount", "type": "uint256"}
		],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "destChainId", "type": "uint256"}
		],
		"name": "getPendingOutboundCount",
		"outputs": [
			{"internalType": "uint256", "name": "count", "type": "uint256"}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "bytes32", "name": "commitRoot", "type": "bytes32"}
		],
		"name": "getCommitBatch",
		"outputs": [
			{"internalType": "bool", "name": "exists", "type": "bool"},
			{"internalType": "uint64", "name": "epoch", "type": "uint64"},
			{"internalType": "bytes", "name": "messagesJson", "type": "bytes"}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "internalType": "bytes32", "name": "commitRoot", "type": "bytes32"},
			{"indexed": true, "internalType": "uint256", "name": "destChainId", "type": "uint256"},
			{"indexed": false, "internalType": "uint256", "name": "messageCount", "type": "uint256"},
			{"indexed": false, "internalType": "uint64", "name": "epoch", "type": "uint64"}
		],
		"name": "CommitBatched",
		"type": "event"
	},
	{
		"inputs": [
			{"internalType": "bytes", "name": "payload", "type": "bytes"},
			{"internalType": "uint256", "name": "amount", "type": "uint256"}
		],
		"name": "registerChainViaStake",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "fromChainId", "type": "uint256"},
			{"internalType": "uint256", "name": "toChainId", "type": "uint256"},
			{"internalType": "uint256", "name": "amount", "type": "uint256"},
			{"internalType": "uint64", "name": "nonce", "type": "uint64"},
			{"internalType": "uint64", "name": "certEpoch", "type": "uint64"},
			{"internalType": "bytes", "name": "certAggregateSignature", "type": "bytes"},
			{"internalType": "bytes", "name": "certSignerBitmap", "type": "bytes"}
		],
		"name": "transferAllocationWithCert",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [{"internalType": "uint256", "name": "chainId", "type": "uint256"}],
		"name": "getTransferAllocationNonce",
		"outputs": [{"internalType": "uint64", "name": "nonce", "type": "uint64"}],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "chainId", "type": "uint256"},
			{"internalType": "uint256", "name": "amount", "type": "uint256"},
			{"internalType": "uint64", "name": "certEpoch", "type": "uint64"},
			{"internalType": "bytes", "name": "certAggregateSignature", "type": "bytes"},
			{"internalType": "bytes", "name": "certSignerBitmap", "type": "bytes"}
		],
		"name": "allocateSupplyWithCert",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "chainId", "type": "uint256"},
			{"internalType": "uint64", "name": "certEpoch", "type": "uint64"},
			{"internalType": "bytes", "name": "certAggregateSignature", "type": "bytes"},
			{"internalType": "bytes", "name": "certSignerBitmap", "type": "bytes"}
		],
		"name": "declareChainDeadWithCert",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "chainId", "type": "uint256"},
			{"internalType": "uint64", "name": "certEpoch", "type": "uint64"},
			{"internalType": "bytes", "name": "certAggregateSignature", "type": "bytes"},
			{"internalType": "bytes", "name": "certSignerBitmap", "type": "bytes"}
		],
		"name": "unregisterChainWithCert",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "bytes", "name": "payload", "type": "bytes"},
			{"internalType": "uint64", "name": "certEpoch", "type": "uint64"},
			{"internalType": "bytes", "name": "certAggregateSignature", "type": "bytes"},
			{"internalType": "bytes", "name": "certSignerBitmap", "type": "bytes"}
		],
		"name": "updateCommitteeWithRecoveryCert",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "bytes", "name": "payload", "type": "bytes"},
			{"internalType": "uint256", "name": "totalSupply", "type": "uint256"},
			{"internalType": "uint64", "name": "certEpoch", "type": "uint64"},
			{"internalType": "bytes", "name": "certAggregateSignature", "type": "bytes"},
			{"internalType": "bytes", "name": "certSignerBitmap", "type": "bytes"}
		],
		"name": "registerAssetWithCert",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [{"internalType": "uint256", "name": "assetId", "type": "uint256"}],
		"name": "getAsset",
		"outputs": [
			{"internalType": "bool", "name": "exists", "type": "bool"},
			{"internalType": "uint256", "name": "homeChainId", "type": "uint256"},
			{"internalType": "address", "name": "canonicalContract", "type": "address"},
			{"internalType": "bool", "name": "active", "type": "bool"}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "bytes32", "name": "messageId", "type": "bytes32"},
			{"internalType": "uint256", "name": "sourceChainId", "type": "uint256"},
			{"internalType": "uint256", "name": "destChainId", "type": "uint256"},
			{"internalType": "uint256", "name": "sequence", "type": "uint256"},
			{"internalType": "uint8", "name": "hopCount", "type": "uint8"},
			{"internalType": "address", "name": "sender", "type": "address"},
			{"internalType": "address", "name": "target", "type": "address"},
			{"internalType": "uint256", "name": "assetId", "type": "uint256"},
			{"internalType": "uint256", "name": "value", "type": "uint256"},
			{"internalType": "bytes", "name": "payload", "type": "bytes"},
			{"internalType": "uint256", "name": "tip", "type": "uint256"},
			{"internalType": "uint256", "name": "gasFee", "type": "uint256"},
			{"internalType": "bool", "name": "ordered", "type": "bool"},
			{"internalType": "uint256", "name": "aggregateProofLeafIndex", "type": "uint256"},
			{"internalType": "bytes32[]", "name": "aggregateProofSiblings", "type": "bytes32[]"},
			{"internalType": "uint256", "name": "messageProofLeafIndex", "type": "uint256"},
			{"internalType": "bytes32[]", "name": "messageProofSiblings", "type": "bytes32[]"},
			{"internalType": "bytes32", "name": "commitRoot", "type": "bytes32"},
			{"internalType": "uint64", "name": "certEpoch", "type": "uint64"},
			{"internalType": "bytes", "name": "certAggregateSignature", "type": "bytes"},
			{"internalType": "bytes", "name": "certSignerBitmap", "type": "bytes"}
		],
		"name": "verifyAndExecute",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint256", "name": "deadChainId", "type": "uint256"},
			{"internalType": "address", "name": "account", "type": "address"},
			{"internalType": "uint256", "name": "amount", "type": "uint256"},
			{"internalType": "uint256", "name": "proofLeafIndex", "type": "uint256"},
			{"internalType": "bytes32[]", "name": "proofSiblings", "type": "bytes32[]"},
			{"internalType": "bytes32", "name": "accountLeafHash", "type": "bytes32"}
		],
		"name": "claimDeadChainBalance",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "withdrawRelayerTip",
		"outputs": [{"internalType": "uint256", "name": "amount", "type": "uint256"}],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "internalType": "bytes32", "name": "messageId", "type": "bytes32"},
			{"indexed": true, "internalType": "uint256", "name": "destChainId", "type": "uint256"},
			{"indexed": false, "internalType": "uint256", "name": "sequence", "type": "uint256"}
		],
		"name": "MessageSent",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "internalType": "bytes32", "name": "messageId", "type": "bytes32"},
			{"indexed": false, "internalType": "uint8", "name": "status", "type": "uint8"}
		],
		"name": "MessageStatusChanged",
		"type": "event"
	}
]`
