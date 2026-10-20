package mvm

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

type fakeCodeDB struct{ byHash map[common.Hash][]byte }

func (f fakeCodeDB) Code(common.Address) []byte { return nil }
func (f fakeCodeDB) StorageValue(common.Address, []byte, ...*common.Hash) ([]byte, bool) {
	return nil, false
}
func (f fakeCodeDB) GetCodeByCodeHash(_ common.Address, h common.Hash) []byte { return f.byHash[h] }

type plainCodeDB struct{}

func (plainCodeDB) Code(common.Address) []byte { return nil }
func (plainCodeDB) StorageValue(common.Address, []byte, ...*common.Hash) ([]byte, bool) {
	return nil, false
}

// codeUnreadable must report only an account whose code hash is set, whose code is empty, whose hash is not the
// empty-code hash, and whose code cannot be found by hash either (so code written earlier in the same block is not flagged).
func TestCodeUnreadable_Decisions(t *testing.T) {
	addr := common.HexToAddress("0x01")
	h := common.HexToHash("0xabc")
	cases := []struct {
		name string
		db   SmartContractDB
		hash common.Hash
		code []byte
		want bool
	}{
		{"code present", fakeCodeDB{}, h, []byte{1}, false},
		{"no code hash", fakeCodeDB{}, common.Hash{}, nil, false},
		{"empty-code hash", fakeCodeDB{}, emptyCodeHash, nil, false},
		{"found by hash (pending in the same block)", fakeCodeDB{byHash: map[common.Hash][]byte{h: {1}}}, h, nil, false},
		{"hash set, code missing everywhere", fakeCodeDB{}, h, nil, true},
		{"hash set, db cannot look up by hash", plainCodeDB{}, h, nil, true},
	}
	for _, c := range cases {
		if got := codeUnreadable(c.db, addr, c.hash, c.code); got != c.want {
			t.Errorf("%s: codeUnreadable = %v, want %v", c.name, got, c.want)
		}
	}
}
