package trie

import (
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/sha3"

	"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
)

func Test_hasher_hash(t *testing.T) {
	tests := []struct {
		name        string
		node        node.Node
		force       bool
		parallel    bool
		wantNilHash bool
	}{
		{
			name: "hash FullNode with children - parallel",
			node: &node.FullNode{
				Children: [17]node.Node{
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					nil,
					nil,
				},
			},
			force:    true,
			parallel: true,
		},
		{
			name: "hash FullNode with children - sequential",
			node: &node.FullNode{
				Children: [17]node.Node{
					node.HashNode(make([]byte, 32)),
					node.HashNode(make([]byte, 32)),
					nil, nil, nil, nil, nil, nil, nil, nil,
					nil, nil, nil, nil, nil, nil, nil,
				},
			},
			force:    true,
			parallel: false,
		},
		{
			name:     "hash empty FullNode",
			node:     &node.FullNode{},
			force:    true,
			parallel: false,
		},
		{
			name:     "hash ValueNode returns itself",
			node:     node.ValueNode([]byte{0x01, 0x02, 0x03}),
			force:    false,
			parallel: false,
		},
		{
			name:     "hash HashNode returns itself",
			node:     node.HashNode([]byte{0x01, 0x02, 0x03}),
			force:    false,
			parallel: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &hasher{
				sha: sha3.NewLegacyKeccak256().(crypto.KeccakState),
				tmp: make([]byte, 0, 550),
			}
			if tt.parallel {
				h.parallelDepth = 2
			}
			gotHashed, gotCached := h.hash(tt.node, tt.force)
			if gotHashed == nil {
				t.Error("hashed result should not be nil")
			}
			if gotCached == nil {
				t.Error("cached result should not be nil")
			}

			// For FullNode, the hash result should be a HashNode (32 bytes)
			if _, ok := tt.node.(*node.FullNode); ok {
				if hn, ok := gotHashed.(node.HashNode); ok {
					if len(hn) != 32 {
						t.Errorf("hash should be 32 bytes, got %d", len(hn))
					}
					t.Logf("gotHashed: %v", hex.EncodeToString(hn))
				}
			}
		})
	}
}

func Test_hasher_hashData(t *testing.T) {
	h := &hasher{
		sha: sha3.NewLegacyKeccak256().(crypto.KeccakState),
		tmp: make([]byte, 0, 550),
	}

	// Hash of empty data should be deterministic
	hash1 := h.hashData([]byte{})
	hash2 := h.hashData([]byte{})
	if hex.EncodeToString(hash1) != hex.EncodeToString(hash2) {
		t.Error("hashData should be deterministic")
	}

	// Hash of different data should differ
	hash3 := h.hashData([]byte{0x01})
	if hex.EncodeToString(hash1) == hex.EncodeToString(hash3) {
		t.Error("hashData of different data should produce different hashes")
	}

	// Hash should be 32 bytes (Keccak256)
	if len(hash1) != 32 {
		t.Errorf("hash should be 32 bytes, got %d", len(hash1))
	}
}

func Test_hasher_deterministic(t *testing.T) {
	// Hashing the same FullNode twice should give the same hash
	makeNode := func() *node.FullNode {
		return &node.FullNode{
			Children: [17]node.Node{
				node.HashNode(make([]byte, 32)),
				nil, nil, nil, nil, nil, nil, nil, nil,
				nil, nil, nil, nil, nil, nil, nil, nil,
			},
		}
	}

	h1 := newHasher(false)
	hashed1, _ := h1.hash(makeNode(), true)
	returnHasherToPool(h1)

	h2 := newHasher(false)
	hashed2, _ := h2.hash(makeNode(), true)
	returnHasherToPool(h2)

	hn1 := hex.EncodeToString(hashed1.(node.HashNode))
	hn2 := hex.EncodeToString(hashed2.(node.HashNode))
	if hn1 != hn2 {
		t.Errorf("deterministic hash failed: %s != %s", hn1, hn2)
	}
}

func Test_newHasher(t *testing.T) {
	h := newHasher(true)
	if h == nil {
		t.Fatal("newHasher returned nil")
	}
	if h.parallelDepth != 2 {
		t.Errorf("parallelDepth should be 2, got %d", h.parallelDepth)
	}
	returnHasherToPool(h)

	h2 := newHasher(false)
	if h2 == nil {
		t.Fatal("newHasher returned nil")
	}
	if h2.parallelDepth != 0 {
		t.Errorf("parallelDepth should be 0, got %d", h2.parallelDepth)
	}
	returnHasherToPool(h2)
}

func Test_hasher_proofHash(t *testing.T) {
	fn := &node.FullNode{
		Children: [17]node.Node{
			node.HashNode(make([]byte, 32)),
			node.HashNode(make([]byte, 32)),
			nil, nil, nil, nil, nil, nil, nil, nil,
			nil, nil, nil, nil, nil, nil, nil,
		},
	}

	h := newHasher(false)
	defer returnHasherToPool(h)

	collapsed, hashed := h.proofHash(fn)
	if collapsed == nil {
		t.Error("proofHash collapsed should not be nil")
	}
	if hashed == nil {
		t.Error("proofHash hashed should not be nil")
	}
}

// Test_parallel_vs_sequential_hash_equivalence is the CRITICAL fork-safety test.
// It proves that the parallel hasher (depth=2) produces an identical hash to
// the sequential hasher (depth=0) for the same FullNode structure.
// If this test ever fails, the parallel optimization MUST be reverted immediately
// because it means parallel hashing produces different state roots → FORK.
func Test_parallel_vs_sequential_hash_equivalence(t *testing.T) {
	// Build a 2-level deep FullNode (root → 16 children, some with sub-children)
	makeDeepNode := func() *node.FullNode {
		root := &node.FullNode{Flags: node.NewFlag()}
		for i := 0; i < 16; i++ {
			if i%3 == 0 {
				// Some children are FullNodes themselves (triggers 2nd level parallel)
				child := &node.FullNode{Flags: node.NewFlag()}
				for j := 0; j < 16; j++ {
					if j%2 == 0 {
						child.Children[j] = node.ValueNode([]byte{byte(i), byte(j), 0x42})
					}
				}
				root.Children[i] = child
			} else if i%3 == 1 {
				root.Children[i] = node.ValueNode([]byte{byte(i), 0xAB, 0xCD})
			}
			// i%3 == 2 → nil child
		}
		return root
	}

	// Hash with sequential hasher (depth=0)
	seqHasher := newHasherWithDepth(0)
	seqNode := makeDeepNode()
	seqHashed, _ := seqHasher.hash(seqNode, true)
	returnHasherToPool(seqHasher)

	// Hash with parallel hasher (depth=2 — same as production)
	parHasher := newHasherWithDepth(2)
	parNode := makeDeepNode()
	parHashed, _ := parHasher.hash(parNode, true)
	returnHasherToPool(parHasher)

	seqHex := hex.EncodeToString(seqHashed.(node.HashNode))
	parHex := hex.EncodeToString(parHashed.(node.HashNode))

	if seqHex != parHex {
		t.Fatalf("🚨 FORK-SAFETY VIOLATION: parallel hash != sequential hash\n  sequential: %s\n  parallel:   %s", seqHex, parHex)
	}
	t.Logf("✅ Fork-safety verified: parallel == sequential == %s", seqHex)
}

func Test_newHasherWithDepth(t *testing.T) {
	h := newHasherWithDepth(3)
	if h == nil {
		t.Fatal("newHasherWithDepth returned nil")
	}
	if h.parallelDepth != 3 {
		t.Errorf("parallelDepth should be 3, got %d", h.parallelDepth)
	}
	returnHasherToPool(h)

	h0 := newHasherWithDepth(0)
	if h0.parallelDepth != 0 {
		t.Errorf("parallelDepth should be 0, got %d", h0.parallelDepth)
	}
	returnHasherToPool(h0)
}

