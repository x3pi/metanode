package trie

import (
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/sha3"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
)

func Test_hasher_hash(t *testing.T) {
	type fields struct {
		sha      crypto.KeccakState
		tmp      []byte
		parallel bool
	}
	type args struct {
		n     node.Node
		force bool
	}
	tests := []struct {
		name       string
		fields     fields
		args       args
		wantHashed node.Node
		wantCached node.Node
	}{
		// TODO: Add test cases.
		{
			name: "Test 1",
			fields: fields{
				sha:      sha3.NewLegacyKeccak256().(crypto.KeccakState),
				tmp:      []byte{},
				parallel: true,
			},
			args: args{
				n: &node.FullNode{
					Children: [17]node.Node{
						&node.HashNode{0x01},
						&node.HashNode{0x02},
						&node.HashNode{0x03},
						&node.HashNode{0x04},
						&node.HashNode{0x05},
						&node.HashNode{0x06},
						&node.HashNode{0x07},
						&node.HashNode{0x08},
						&node.HashNode{0x09},
						&node.HashNode{0x0a},
						&node.HashNode{0x0b},
						&node.HashNode{0x0c},
						&node.HashNode{0x0d},
						&node.HashNode{0x0e},
						&node.HashNode{0x0f},
					},
				},
				force: true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &hasher{
				sha:      tt.fields.sha,
				tmp:      tt.fields.tmp,
				parallel: tt.fields.parallel,
			}
			gotHashed, _ := h.hash(tt.args.n, tt.args.force)
			logger.Info("gotHashed: %v", hex.EncodeToString(gotHashed.(node.HashNode)))
			// if !reflect.DeepEqual(gotHashed, tt.wantHashed) {
			// 	t.Errorf("hasher.hash() gotHashed = %v, want %v", gotHashed, tt.wantHashed)
			// }
			// if !reflect.DeepEqual(gotCached, tt.wantCached) {
			// 	t.Errorf("hasher.hash() gotCached = %v, want %v", gotCached, tt.wantCached)
			// }
		})
	}
}
