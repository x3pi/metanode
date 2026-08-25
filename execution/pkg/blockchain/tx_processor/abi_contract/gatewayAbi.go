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
// bootstrapFoundingChains() was added while running a real T2 devnet smoke-test: a Root Anchor
// starts with zero ChainRegistry entries (NewGatewayEngine is always called with an empty
// registry — see gateway_handler.go's loadGatewayEngine), but GovernanceEngine.Vote requires the
// voter to already be a ChainRegistry member and ExecuteGovernanceProposal's
// ProposalRegisterChain case requires a proposal to have already passed a vote — a genuine
// circular dependency with no path to register the very first chains through governance alone.
// See GatewayEngine.BootstrapFoundingChains's doc comment (pkg/cross_chain/gateway.go) for the
// full design rationale (mirrors the >= MinFoundingChains requirement of the real
// founding_entry.json/assemble_root_anchor ceremony, and is self-closing after first use).
const GatewayABI = `[
	{
		"inputs": [
			{"internalType": "uint256", "name": "destChainId", "type": "uint256"},
			{"internalType": "address", "name": "target", "type": "address"},
			{"internalType": "bytes", "name": "payload", "type": "bytes"},
			{"internalType": "uint256", "name": "assetId", "type": "uint256"},
			{"internalType": "uint256", "name": "value", "type": "uint256"},
			{"internalType": "uint256", "name": "tip", "type": "uint256"},
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
			{"internalType": "uint64", "name": "registeredAt", "type": "uint64"}
		],
		"stateMutability": "view",
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
			{"internalType": "bytes[]", "name": "payloads", "type": "bytes[]"}
		],
		"name": "bootstrapFoundingChains",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "uint8", "name": "kind", "type": "uint8"},
			{"internalType": "bytes", "name": "payload", "type": "bytes"},
			{"internalType": "uint64", "name": "proposedAt", "type": "uint64"}
		],
		"name": "propose",
		"outputs": [{"internalType": "bytes32", "name": "proposalId", "type": "bytes32"}],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "bytes32", "name": "proposalId", "type": "bytes32"},
			{"internalType": "uint256", "name": "voterChainId", "type": "uint256"},
			{"internalType": "uint64", "name": "currentTimestamp", "type": "uint64"},
			{"internalType": "bytes", "name": "signerPubkeyBls", "type": "bytes"},
			{"internalType": "bytes", "name": "signature", "type": "bytes"}
		],
		"name": "vote",
		"outputs": [{"internalType": "uint8", "name": "status", "type": "uint8"}],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "bytes32", "name": "proposalId", "type": "bytes32"},
			{"internalType": "uint64", "name": "currentTimestamp", "type": "uint64"}
		],
		"name": "executeProposal",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{"internalType": "bytes32", "name": "proposalId", "type": "bytes32"},
			{"internalType": "uint256", "name": "totalSupply", "type": "uint256"}
		],
		"name": "registerAsset",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [{"internalType": "bytes32", "name": "proposalId", "type": "bytes32"}],
		"name": "getProposal",
		"outputs": [
			{"internalType": "bool", "name": "exists", "type": "bool"},
			{"internalType": "uint8", "name": "kind", "type": "uint8"},
			{"internalType": "bytes", "name": "payload", "type": "bytes"},
			{"internalType": "uint64", "name": "votesFor", "type": "uint64"},
			{"internalType": "uint64", "name": "proposedAt", "type": "uint64"},
			{"internalType": "uint64", "name": "effectiveAt", "type": "uint64"},
			{"internalType": "bool", "name": "executed", "type": "bool"},
			{"internalType": "uint8", "name": "status", "type": "uint8"}
		],
		"stateMutability": "view",
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
