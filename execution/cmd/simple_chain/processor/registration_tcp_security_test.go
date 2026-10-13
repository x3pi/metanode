package processor

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
)

// signedRegistration returns a funded-less test user with a VALID registration signature for the relay in sp.
func signedRegistration(t *testing.T, relay *rollup.RegistrationRelay) (common.Address, []byte) {
	t.Helper()
	k, err := crypto.GenerateKey()
	require.NoError(t, err)
	addr := crypto.PubkeyToAddress(k.PublicKey)
	sig, err := crypto.Sign(crypto.Keccak256(relay.RegistrationDigest(addr)), k)
	require.NoError(t, err)
	return addr, sig
}

func rawBody(addr common.Address, sig []byte) []byte { return append(addr.Bytes(), sig...) }

// Old behaviour: the connection was stored BEFORE the relay verified the signature, and deleted on failure. A client
// sending a bad signature for a victim's address therefore removed (or replaced) the victim's push subscription.
func TestRegisterAccount_BadSignatureCannotCancelOrReplaceAnotherClientsSubscription(t *testing.T) {
	sp, relay, tracker, _ := setupRegistrationTest(t)
	defer tracker.Stop()

	victim, goodSig := signedRegistration(t, relay)
	connA := newRecordingConn(victim)
	require.NoError(t, sp.ProcessRegisterAccount(NewMockRequest(connA, NewMockMessage(command.RegisterAccount, rawBody(victim, goodSig)))))
	_, ok := tracker.Get(victim)
	require.True(t, ok, "the legitimate client must be subscribed")

	// An attacker on another connection sends a garbage signature for the victim's address.
	connB := newRecordingConn(common.HexToAddress("0xbad"))
	badSig := make([]byte, 65)
	badSig[0] = 1
	err := sp.ProcessRegisterAccount(NewMockRequest(connB, NewMockMessage(command.RegisterAccount, rawBody(victim, badSig))))
	require.Error(t, err)

	entry, ok := tracker.Get(victim)
	require.True(t, ok, "the victim's subscription must survive a failed request from someone else")
	assert.Equal(t, connA.MockConnection, entry.Conn.(*recordingConn).MockConnection, "the entry must still belong to the legitimate connection")

	tracker.PushResult(victim, rollup.RegistrationResult{Status: rollup.RegStatusConfirmed})
	time.Sleep(50 * time.Millisecond)
	var got bool
	for _, m := range connA.AllMessages() {
		if m.Command() == command.RegistrationResult {
			got = true
		}
	}
	assert.True(t, got, "the legitimate client must still receive the push")
	for _, m := range connB.AllMessages() {
		assert.NotEqual(t, command.RegistrationResult, m.Command(), "the attacker must not receive the victim's push")
	}
}

// Old behaviour: every request allocated a tracker entry before any validation, so one connection could grow the map
// with arbitrary addresses (until the 15 minute TTL).
func TestRegisterAccount_InvalidRequestsAllocateNothing(t *testing.T) {
	sp, _, tracker, _ := setupRegistrationTest(t)
	defer tracker.Stop()

	conn := newRecordingConn(common.Address{})
	for i := 0; i < 500; i++ {
		body := make([]byte, registerAccountRawLen)
		body[0], body[1], body[19], body[20] = byte(i), byte(i>>8), 1, 1 // distinct addresses, garbage signature
		_ = sp.ProcessRegisterAccount(NewMockRequest(conn, NewMockMessage(command.RegisterAccount, body)))
	}
	assert.Equal(t, 0, tracker.Len(), "requests that fail signature verification must not create tracker entries")
}

func TestTracker_CapsAreEnforced(t *testing.T) {
	tracker := NewRegistrationTracker()
	defer tracker.Stop()

	// per-address subscriber limit, distinct live connections
	addr := common.HexToAddress("0x01")
	for i := 0; i < maxSubscribersPerAddress; i++ {
		c := newRecordingConn(common.BigToAddress(common.Big1))
		require.True(t, tracker.Store(addr, RegConnEntry{Conn: c, CreatedAt: time.Now()}))
	}
	extra := newRecordingConn(common.BigToAddress(common.Big2))
	assert.False(t, tracker.Store(addr, RegConnEntry{Conn: extra, CreatedAt: time.Now()}), "subscriber limit per address")

	// the same connection storing again only refreshes its entry
	first, _ := tracker.Get(addr)
	assert.True(t, tracker.Store(addr, RegConnEntry{Conn: first.Conn, MsgID: "again", CreatedAt: time.Now()}))
	got, _ := tracker.Get(addr)
	assert.Equal(t, "again", got.MsgID)

	// global limit on distinct addresses
	tr2 := NewRegistrationTracker()
	defer tr2.Stop()
	c := newRecordingConn(common.Address{})
	for i := 0; i < maxTrackedRegistrations; i++ {
		var a common.Address
		a[0], a[1], a[2] = byte(i), byte(i>>8), byte(i>>16)
		a[19] = 0xAA
		require.True(t, tr2.Store(a, RegConnEntry{Conn: c, CreatedAt: time.Now()}), "entry %d", i)
	}
	assert.False(t, tr2.Store(common.HexToAddress("0xfffffffffffffffffffffffffffffffffffffff0"), RegConnEntry{Conn: c, CreatedAt: time.Now()}), "global tracker limit")
}

// Remove must only drop the given connection's entry.
func TestTracker_RemoveOnlyDropsThatConnection(t *testing.T) {
	tracker := NewRegistrationTracker()
	defer tracker.Stop()
	addr := common.HexToAddress("0x02")
	a, b := newRecordingConn(common.Address{}), newRecordingConn(common.Address{})
	require.True(t, tracker.Store(addr, RegConnEntry{Conn: a, CreatedAt: time.Now()}))
	require.True(t, tracker.Store(addr, RegConnEntry{Conn: b, CreatedAt: time.Now()}))
	tracker.Remove(addr, a)
	left, ok := tracker.Get(addr)
	require.True(t, ok)
	assert.True(t, left.Conn == b)
}

// Old behaviour: any body that happened to parse as a pb.RegisterAccountRequest was treated as proto even when it was 85
// raw bytes. This 85-byte body is a syntactically valid proto (address field = bytes 2..21, signature field = bytes 24..84)
// and used to be rejected with "invalid signature length: 61"; by length it is the raw form and must reach the relay, which
// rejects the (unsigned) signature instead.
func TestRegisterAccount_85BytesIsAlwaysTheRawForm(t *testing.T) {
	sp, _, tracker, _ := setupRegistrationTest(t)
	defer tracker.Stop()

	body := make([]byte, registerAccountRawLen)
	body[0], body[1] = 0x0a, 0x14   // proto: field 1, length 20 (the address field spans bytes 2..21)
	body[22], body[23] = 0x12, 0x3d // proto: field 2, length 61 (the signature field spans bytes 24..84)
	var probe pb.RegisterAccountRequest
	require.NoError(t, proto.Unmarshal(body, &probe), "test premise: this body must also parse as proto")
	require.Len(t, probe.Signature, 61)

	conn := newRecordingConn(common.Address{})
	err := sp.ProcessRegisterAccount(NewMockRequest(conn, NewMockMessage(command.RegisterAccount, body)))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "invalid signature length", "an 85-byte body must be parsed as raw (20+65), not as proto")
}

// An already-terminal registration is pushed AFTER the ACK, and only to a connection the relay accepted.
func TestRegisterAccount_TerminalStateIsAckedThenPushed(t *testing.T) {
	kp := bls.GenerateKeyPair()
	var user common.Address
	parent := &testFakeParent{registry: make(map[common.Address]cm.PublicKey)}
	relay := rollup.NewRegistrationRelay(parent, kp.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), d) },
		func(a common.Address) bool { return a == user }) // on-chain flag already set for `user`
	tracker := NewRegistrationTracker()
	defer tracker.Stop()
	relay.SetOnStatusChanged(tracker.PushResult)
	sp := NewStateProcessor(NewMockMessageSender(), nil, nil, nil)
	sp.SetRegistrationComponents(relay, tracker)

	k, err := crypto.GenerateKey()
	require.NoError(t, err)
	user = crypto.PubkeyToAddress(k.PublicKey)
	sig, err := crypto.Sign(crypto.Keccak256(relay.RegistrationDigest(user)), k)
	require.NoError(t, err)

	conn := newRecordingConn(user)
	require.NoError(t, sp.ProcessRegisterAccount(NewMockRequest(conn, NewMockMessage(command.RegisterAccount, rawBody(user, sig)))))
	time.Sleep(80 * time.Millisecond)
	msgs := conn.AllMessages()
	require.Len(t, msgs, 2)
	assert.Equal(t, command.RegisterAccountAck, msgs[0].Command())
	assert.Equal(t, command.RegistrationResult, msgs[1].Command())
}
