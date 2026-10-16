package processor

import (
	"sync"
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
	"github.com/meta-node-blockchain/meta-node/types/network"
)

type testFakeParent struct {
	mu       sync.Mutex
	registry map[common.Address]cm.PublicKey
}

func (p *testFakeParent) SendRegisterAccount(common.Address, cm.PublicKey, []byte, cm.Sign) (common.Hash, error) {
	return common.Hash{1}, nil
}

func (p *testFakeParent) GetAccountRegistry(a common.Address) (cm.PublicKey, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	k, ok := p.registry[a]
	return k, ok, nil
}

type recordingConn struct {
	*MockConnection
	mu       sync.Mutex
	sentMsgs []network.Message
}

func newRecordingConn(addr common.Address) *recordingConn {
	return &recordingConn{
		MockConnection: NewMockConnection(addr),
	}
}

func (rc *recordingConn) SendMessage(msg network.Message) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.sentMsgs = append(rc.sentMsgs, msg)
	return rc.MockConnection.SendMessage(msg)
}

func (rc *recordingConn) LastMessage() network.Message {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.sentMsgs) == 0 {
		return nil
	}
	return rc.sentMsgs[len(rc.sentMsgs)-1]
}

func (rc *recordingConn) AllMessages() []network.Message {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := make([]network.Message, len(rc.sentMsgs))
	copy(out, rc.sentMsgs)
	return out
}

func setupRegistrationTest(t *testing.T) (*StateProcessor, *rollup.RegistrationRelay, *RegistrationTracker, *bls.KeyPair) {
	kp := bls.GenerateKeyPair()
	parent := &testFakeParent{registry: make(map[common.Address]cm.PublicKey)}
	relay := rollup.NewRegistrationRelay(parent, kp.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), d) },
		func(a common.Address) bool { return false })

	tracker := NewRegistrationTracker()
	relay.SetOnStatusChanged(tracker.PushResult)

	sp := NewStateProcessor(NewMockMessageSender(), nil, nil, nil)
	sp.SetRegistrationComponents(relay, tracker)

	return sp, relay, tracker, kp
}

func TestProcessGetRegistrationMessage(t *testing.T) {
	sp, relay, tracker, _ := setupRegistrationTest(t)
	defer tracker.Stop()

	testAddr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	conn := newRecordingConn(testAddr)
	msg := NewMockMessage(command.GetRegistrationMessage, testAddr.Bytes())
	req := NewMockRequest(conn, msg)

	err := sp.ProcessGetRegistrationMessage(req)
	require.NoError(t, err)

	lastMsg := conn.LastMessage()
	require.NotNil(t, lastMsg)
	assert.Equal(t, command.RegistrationMessage, lastMsg.Command())
	assert.Equal(t, 32, len(lastMsg.Body()))

	expectedHash := crypto.Keccak256(relay.RegistrationDigest(testAddr))
	assert.Equal(t, expectedHash, lastMsg.Body())
}

func TestProcessRegisterAccount_ProtoAndPush(t *testing.T) {
	sp, relay, tracker, _ := setupRegistrationTest(t)
	defer tracker.Stop()

	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	userAddr := crypto.PubkeyToAddress(privKey.PublicKey)

	digest := relay.RegistrationDigest(userAddr)
	hashToSign := crypto.Keccak256(digest)
	sig, err := crypto.Sign(hashToSign, privKey)
	require.NoError(t, err)

	// Test with pb.RegisterAccountRequest
	reqProto := &pb.RegisterAccountRequest{
		Address:   userAddr.Bytes(),
		Signature: sig,
	}
	reqBytes, err := proto.Marshal(reqProto)
	require.NoError(t, err)

	conn := newRecordingConn(userAddr)
	msg := NewMockMessage(command.RegisterAccount, reqBytes)
	req := NewMockRequest(conn, msg)

	err = sp.ProcessRegisterAccount(req)
	require.NoError(t, err)

	// Step 1: Client receives RegisterAccountAck as pb.RegistrationResult with "PENDING"
	lastMsg := conn.LastMessage()
	require.NotNil(t, lastMsg)
	assert.Equal(t, command.RegisterAccountAck, lastMsg.Command())

	var ackRes pb.RegistrationResult
	err = proto.Unmarshal(lastMsg.Body(), &ackRes)
	require.NoError(t, err)
	assert.Equal(t, "PENDING", ackRes.Status)
	assert.Equal(t, userAddr.Hex(), ackRes.Address)

	// Tracker should hold the entry
	entry, ok := tracker.Get(userAddr)
	assert.True(t, ok)
	assert.Equal(t, "mock-msg-id", entry.MsgID)

	// Step 2: Push outcome when L1 confirms
	tracker.PushResult(userAddr, rollup.RegistrationResult{Status: rollup.RegStatusConfirmed})

	// Wait briefly for goroutine push
	time.Sleep(50 * time.Millisecond)

	allMsgs := conn.AllMessages()
	require.GreaterOrEqual(t, len(allMsgs), 2)
	pushMsg := allMsgs[len(allMsgs)-1]
	assert.Equal(t, command.RegistrationResult, pushMsg.Command())
	assert.Equal(t, "mock-msg-id", pushMsg.ID())

	var pushRes pb.RegistrationResult
	err = proto.Unmarshal(pushMsg.Body(), &pushRes)
	require.NoError(t, err)
	assert.Equal(t, "CONFIRMED", pushRes.Status)
	assert.Equal(t, userAddr.Hex(), pushRes.Address)
}

func TestProcessRegisterAccount_RelayCallbackPushesToSameConnection(t *testing.T) {
	kp := bls.GenerateKeyPair()
	parent := &testFakeParent{registry: make(map[common.Address]cm.PublicKey)}
	registered := false
	relay := rollup.NewRegistrationRelay(parent, kp.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), d) },
		func(common.Address) bool { return registered })
	tracker := NewRegistrationTracker()
	defer tracker.Stop()
	relay.SetOnStatusChanged(tracker.PushResult)

	sp := NewStateProcessor(NewMockMessageSender(), nil, nil, nil)
	sp.SetRegistrationComponents(relay, tracker)

	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	userAddr := crypto.PubkeyToAddress(privKey.PublicKey)
	sig, err := crypto.Sign(crypto.Keccak256(relay.RegistrationDigest(userAddr)), privKey)
	require.NoError(t, err)

	conn := newRecordingConn(userAddr)
	req := NewMockRequest(conn, NewMockMessage(command.RegisterAccount, rawBody(userAddr, sig)))
	require.NoError(t, sp.ProcessRegisterAccount(req))
	require.Len(t, conn.AllMessages(), 1)
	assert.Equal(t, command.RegisterAccountAck, conn.AllMessages()[0].Command())

	registered = true
	relay.ProcessBatch()

	require.Eventually(t, func() bool { return len(conn.AllMessages()) == 2 }, time.Second, 10*time.Millisecond)
	push := conn.AllMessages()[1]
	assert.Equal(t, command.RegistrationResult, push.Command())
	assert.Equal(t, req.Message().ID(), push.ID())

	var result pb.RegistrationResult
	require.NoError(t, proto.Unmarshal(push.Body(), &result))
	assert.Equal(t, string(rollup.RegStatusConfirmed), result.Status)
	assert.Equal(t, userAddr.Hex(), result.Address)
}

func TestProcessRegisterAccount_ProvisionalPushesToSameConnection(t *testing.T) {
	kp := bls.GenerateKeyPair()
	parent := &testFakeParent{registry: make(map[common.Address]cm.PublicKey)}
	provisional := false
	relay := rollup.NewRegistrationRelay(parent, kp.PublicKey(),
		func(d []byte) cm.Sign { return bls.Sign(kp.PrivateKey(), d) },
		func(common.Address) bool { return false })
	relay.SetProvisionalChecker(func(common.Address) bool { return provisional })
	tracker := NewRegistrationTracker()
	defer tracker.Stop()
	relay.SetOnStatusChanged(tracker.PushResult)

	sp := NewStateProcessor(NewMockMessageSender(), nil, nil, nil)
	sp.SetRegistrationComponents(relay, tracker)

	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	userAddr := crypto.PubkeyToAddress(privKey.PublicKey)
	sig, err := crypto.Sign(crypto.Keccak256(relay.RegistrationDigest(userAddr)), privKey)
	require.NoError(t, err)

	conn := newRecordingConn(userAddr)
	req := NewMockRequest(conn, NewMockMessage(command.RegisterAccount, rawBody(userAddr, sig)))
	require.NoError(t, sp.ProcessRegisterAccount(req))
	require.Len(t, conn.AllMessages(), 1)

	provisional = true
	relay.ProcessBatch()

	require.Eventually(t, func() bool { return len(conn.AllMessages()) == 2 }, time.Second, 10*time.Millisecond)
	push := conn.AllMessages()[1]
	assert.Equal(t, command.RegistrationResult, push.Command())
	assert.Equal(t, req.Message().ID(), push.ID())

	var result pb.RegistrationResult
	require.NoError(t, proto.Unmarshal(push.Body(), &result))
	assert.Equal(t, string(rollup.RegStatusProvisional), result.Status)
	assert.Equal(t, userAddr.Hex(), result.Address)
}

func TestProcessGetRegistrationStatus(t *testing.T) {
	sp, _, tracker, _ := setupRegistrationTest(t)
	defer tracker.Stop()

	testAddr := common.HexToAddress("0x2222222222222222222222222222222222222222")
	conn := newRecordingConn(testAddr)
	msg := NewMockMessage(command.GetRegistrationStatus, testAddr.Bytes())
	req := NewMockRequest(conn, msg)

	err := sp.ProcessGetRegistrationStatus(req)
	require.NoError(t, err)

	lastMsg := conn.LastMessage()
	require.NotNil(t, lastMsg)
	assert.Equal(t, command.RegistrationStatus, lastMsg.Command())

	var res pb.RegistrationResult
	err = proto.Unmarshal(lastMsg.Body(), &res)
	require.NoError(t, err)
	assert.Equal(t, "NONE", res.Status)
	assert.Equal(t, testAddr.Hex(), res.Address)
}

func TestProcessRegisterAccount_InvalidBody(t *testing.T) {
	sp, _, tracker, _ := setupRegistrationTest(t)
	defer tracker.Stop()

	conn := newRecordingConn(common.Address{})
	msg := NewMockMessage(command.RegisterAccount, []byte("short"))
	req := NewMockRequest(conn, msg)

	err := sp.ProcessRegisterAccount(req)
	assert.Error(t, err)
}
