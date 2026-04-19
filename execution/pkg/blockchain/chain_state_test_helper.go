package blockchain

// NewTestChainState creates a minimal ChainState for integration testing.
// It has no storage manager, no block database, no trie — just epoch
// tracking fields initialised to genesis state.
//
// This is intentionally in the blockchain package so it can set the
// unexported fields of ChainState.
func NewTestChainState() *ChainState {
	return &ChainState{
		currentEpoch:          0,
		epochStartTimestampMs: 0,
		epochStartTimestamps:  make(map[uint64]uint64),
		epochBoundaryBlocks:   make(map[uint64]uint64),
	}
}
