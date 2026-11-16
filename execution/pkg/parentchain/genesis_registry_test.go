package parentchain

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

const testClusterKeyHex = "0x8235afcbc534265b024e8fdbf74807b2444d3e32f8fc1c9067c6146f26add2b6b7b7641fe48c27d2a6dbc63a4ed25e17"

func TestGenesisAccountRegistrations_Validation(t *testing.T) {
	ok := &Genesis{AccountRegistry: []GenesisAccountRegistration{
		{Address: "0xbf374559569f3f22c38166ffa2a97648677a65aa", ClusterKey: testClusterKeyHex},
		{Address: "0xb8f0a0c31acba71ccda060ae1a79916f5eb271ba", ClusterKey: testClusterKeyHex},
	}}
	regs, err := ok.AccountRegistrations()
	if err != nil {
		t.Fatalf("valid registry rejected: %v", err)
	}
	if len(regs) != 2 || regs[0].User != common.HexToAddress("0xbf374559569f3f22c38166ffa2a97648677a65aa") {
		t.Fatalf("unexpected registrations: %+v", regs)
	}

	bad := []GenesisAccountRegistration{
		{Address: "not-an-address", ClusterKey: testClusterKeyHex},
		{Address: "0x0000000000000000000000000000000000000000", ClusterKey: testClusterKeyHex},
		{Address: "0xbf374559569f3f22c38166ffa2a97648677a65aa", ClusterKey: "0x1234"},
	}
	for i, b := range bad {
		g := &Genesis{AccountRegistry: []GenesisAccountRegistration{b}}
		if _, err := g.AccountRegistrations(); !errors.Is(err, ErrGenesisInvalid) {
			t.Fatalf("case %d: expected ErrGenesisInvalid, got %v", i, err)
		}
	}

	dup := &Genesis{AccountRegistry: []GenesisAccountRegistration{
		{Address: "0xbf374559569f3f22c38166ffa2a97648677a65aa", ClusterKey: testClusterKeyHex},
		{Address: "0xBF374559569F3F22C38166FFA2A97648677A65AA", ClusterKey: testClusterKeyHex},
	}}
	if _, err := dup.AccountRegistrations(); !errors.Is(err, ErrGenesisInvalid) {
		t.Fatalf("duplicate address must be rejected, got %v", err)
	}
}

func TestApplyGenesisAccountRegistry(t *testing.T) {
	g := &Genesis{AccountRegistry: []GenesisAccountRegistration{
		{Address: "0xbf374559569f3f22c38166ffa2a97648677a65aa", ClusterKey: testClusterKeyHex},
		{Address: "0xb8f0a0c31acba71ccda060ae1a79916f5eb271ba", ClusterKey: testClusterKeyHex},
	}}
	regs, err := g.AccountRegistrations()
	if err != nil {
		t.Fatal(err)
	}
	var key cm.PublicKey = regs[0].ClusterKey

	store := NewMemoryStore()
	// An address already in the registry must be left untouched (no second event).
	pre := regs[1].User
	if err := store.SetAccountRegistry(pre, key); err != nil {
		t.Fatal(err)
	}

	if err := ApplyGenesisAccountRegistry(store, regs); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	for _, r := range regs {
		got, found, err := store.GetAccountRegistry(r.User)
		if err != nil || !found || got != key {
			t.Fatalf("registry for %s: found=%v err=%v", r.User.Hex(), found, err)
		}
	}

	events, _, err := store.GetAccountRegistrations(crypto.Keccak256Hash(key.Bytes()), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].UserAddress != regs[0].User {
		t.Fatalf("expected exactly one inbound event for %s, got %+v", regs[0].User.Hex(), events)
	}
}
