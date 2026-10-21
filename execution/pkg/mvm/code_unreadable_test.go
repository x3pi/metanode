package mvm

import (
	"strings"
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

// With METANODE_FAILSTOP_ON_UNREADABLE_CODE set, an unreadable-code account halts the node; without it, or when the code is
// readable, nothing halts.
func TestWarnIfCodeUnreadable_FailStopIsOptIn(t *testing.T) {
	var exits []string
	orig := exitOnUnreadableCode
	exitOnUnreadableCode = func(msg string) { exits = append(exits, msg) }
	defer func() { exitOnUnreadableCode = orig }()
	addr := common.HexToAddress("0x01")
	h := common.HexToHash("0xabc")

	t.Setenv(failStopOnUnreadableCodeEnv, "")
	warnIfCodeUnreadable(fakeCodeDB{}, addr, h, nil)
	if len(exits) != 0 {
		t.Fatalf("without the opt-in the node must only log, got %d exit(s)", len(exits))
	}

	for _, v := range []string{"1", "true", "TRUE", " yes "} {
		exits = nil
		t.Setenv(failStopOnUnreadableCodeEnv, v)
		warnIfCodeUnreadable(fakeCodeDB{}, addr, h, nil)
		if len(exits) != 1 {
			t.Fatalf("%q: want exactly one fail-stop, got %d", v, len(exits))
		}
		if !strings.Contains(exits[0], addr.Hex()) || !strings.Contains(exits[0], h.Hex()) {
			t.Fatalf("%q: the halt message must name the account and the code hash: %s", v, exits[0])
		}
	}

	exits = nil
	t.Setenv(failStopOnUnreadableCodeEnv, "true")
	warnIfCodeUnreadable(fakeCodeDB{}, addr, h, []byte{1})                                 // code present
	warnIfCodeUnreadable(fakeCodeDB{byHash: map[common.Hash][]byte{h: {1}}}, addr, h, nil) // pending in the same block
	warnIfCodeUnreadable(fakeCodeDB{}, addr, emptyCodeHash, nil)                           // contract with empty runtime code
	warnIfCodeUnreadable(fakeCodeDB{}, addr, common.Hash{}, nil)                           // not a contract
	if len(exits) != 0 {
		t.Fatalf("readable or legitimately empty code must never halt, got %d exit(s)", len(exits))
	}

	exits = nil
	t.Setenv(failStopOnUnreadableCodeEnv, "false")
	warnIfCodeUnreadable(fakeCodeDB{}, addr, h, nil)
	if len(exits) != 0 {
		t.Fatalf("an explicit false must not halt")
	}
}
