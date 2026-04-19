package processor

import (
	"github.com/meta-node-blockchain/meta-node/pkg/block_signer"
)

// ConsensusContext groups the security and consensus-related fields required for
// block broadcasting and validation (e.g., BLS signatures, attestations).
// It was extracted from the monolithic BlockProcessor struct to clearly isolate
// consensus verification state from pure block execution state.
type ConsensusContext struct {
	// BLS block signer for signing block hashes before broadcast (Master only)
	blockSigner *block_signer.BlockSigner

	// Master's BLS public key for verifying block signatures (Sub only)
	masterBLSPubKey []byte

	// Skip signature verification for backward compatibility during rollout
	skipSigVerification bool

	// Attestation collector for 2/3 quorum fork detection
	attestationCollector *attestationCollector
}

// NewConsensusContext creates and initializes a new ConsensusContext.
// (Attestation collector initialization can be added here if needed to offload init logic)
func NewConsensusContext() *ConsensusContext {
	return &ConsensusContext{}
}
