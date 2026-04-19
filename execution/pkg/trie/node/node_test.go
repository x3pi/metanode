package node

import (
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestDecodeNode(t *testing.T) {
	tests := []struct {
		name        string
		hash        []byte
		buf         []byte
		wantType    string
		wantNilNode bool
		wantErr     bool
	}{
		{
			name: "decode full node from protobuf",
			hash: []byte("test-hash"),
			buf: common.FromHex(
				"12c2040a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a20eeeae2329c18b56cb8d3bf14d5c215f265ba5e56d08ee6d31f625b4bc9bfc6210a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a2000000000000000000000000000000000000000000000000000000000000000000a200000000000000000000000000000000000000000000000000000000000000000",
			),
			wantType: "full",
		},
		{
			name:     "decode invalid protobuf returns hash node",
			hash:     []byte("test"),
			buf:      []byte{0x01, 0x02, 0x03},
			wantType: "hash",
		},
		{
			name:     "decode empty buf returns full node (default protobuf type)",
			hash:     []byte{},
			buf:      []byte{},
			wantType: "full",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeNode(tt.hash, tt.buf)
			if tt.wantErr && err == nil {
				t.Errorf("DecodeNode() expected error but got none")
				return
			}
			if got == nil && !tt.wantNilNode {
				// Some cases may return nil, check based on wantType
				if tt.wantType != "" {
					t.Errorf("DecodeNode() returned nil node, wanted type %s", tt.wantType)
				}
				return
			}
			switch tt.wantType {
			case "full":
				if _, ok := got.(*FullNode); !ok {
					t.Errorf("DecodeNode() expected FullNode, got %T", got)
				}
			case "hash":
				if _, ok := got.(HashNode); !ok {
					t.Errorf("DecodeNode() expected HashNode, got %T", got)
				}
			}
		})
	}
}

func TestHashEmptyFullNode(t *testing.T) {
	fullNode := &FullNode{}
	bData, err := fullNode.Marshal()
	if err != nil {
		t.Fatalf("Failed to marshal empty FullNode: %v", err)
	}
	if len(bData) == 0 {
		t.Error("Marshal returned empty bytes for FullNode")
	}
	hash := crypto.Keccak256Hash(bData)
	if hash == (common.Hash{}) {
		t.Error("Hash of FullNode should not be zero")
	}
}

func TestFullNodeMarshalUnmarshal(t *testing.T) {
	// Create a FullNode with one non-nil child
	original := &FullNode{}
	original.Children[8] = HashNode(common.FromHex("eeeae2329c18b56cb8d3bf14d5c215f265ba5e56d08ee6d31f625b4bc9bfc621"))

	// Marshal
	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Decode back
	decoded, err := DecodeNode([]byte("test-hash"), data)
	if err != nil {
		t.Fatalf("DecodeNode failed: %v", err)
	}

	fn, ok := decoded.(*FullNode)
	if !ok {
		t.Fatalf("Expected FullNode, got %T", decoded)
	}

	// Check child at index 8 is set
	if fn.Children[8] == nil {
		t.Error("Child at index 8 should not be nil")
	} else {
		childHash, ok := fn.Children[8].(HashNode)
		if !ok {
			t.Errorf("Child at index 8 should be HashNode, got %T", fn.Children[8])
		} else {
			want := "eeeae2329c18b56cb8d3bf14d5c215f265ba5e56d08ee6d31f625b4bc9bfc621"
			got := hex.EncodeToString(childHash)
			if got != want {
				t.Errorf("Child hash mismatch: got %s, want %s", got, want)
			}
		}
	}
}

func TestNodeToBytes(t *testing.T) {
	// Test with a FullNode
	fn := &FullNode{}
	b := NodeToBytes(fn)
	if len(b) == 0 {
		t.Error("NodeToBytes returned empty for FullNode")
	}

	// Test with HashNode
	hn := HashNode([]byte{0x01, 0x02, 0x03})
	b = NodeToBytes(hn)
	if len(b) == 0 {
		t.Error("NodeToBytes returned empty for HashNode")
	}
}

func TestNewFlag(t *testing.T) {
	flag := NewFlag()
	if !flag.Dirty {
		t.Error("NewFlag should have Dirty = true")
	}
	if flag.Hash != nil {
		t.Error("NewFlag should have nil Hash")
	}
}

func TestValueNodeNil(t *testing.T) {
	if NilValueNode != nil {
		t.Error("NilValueNode should be nil")
	}
}

func TestFullNodeCopy(t *testing.T) {
	original := &FullNode{}
	original.Children[0] = HashNode([]byte{0x01})
	original.Flags = NodeFlag{Hash: []byte{0xab}, Dirty: true}

	copied := original.Copy()
	if copied == original {
		t.Error("Copy should return a different pointer")
	}
	if copied.Flags.Dirty != original.Flags.Dirty {
		t.Error("Copied Dirty flag should match original")
	}
}

func TestShortNodeCopy(t *testing.T) {
	original := &ShortNode{
		Key:   []byte{0x01, 0x02},
		Val:   HashNode([]byte{0x03}),
		Flags: NodeFlag{Dirty: true},
	}

	copied := original.Copy()
	if copied == original {
		t.Error("Copy should return a different pointer")
	}
}
