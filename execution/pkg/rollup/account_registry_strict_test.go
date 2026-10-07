package rollup

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

type flagDB struct {
	mu sync.Mutex
	m  map[common.Address]bool
}

func newFlagDB() *flagDB { return &flagDB{m: map[common.Address]bool{}} }
func (f *flagDB) GetParentRegistered(a common.Address) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[a]
}
func (f *flagDB) SetParentRegistered(a common.Address, v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[a] = v
}

func clusterKeyOf(b byte) cm.PublicKey {
	var k cm.PublicKey
	for i := range k {
		k[i] = b
	}
	return k
}

func payloadWithKey(user common.Address, keyBytes []byte) []byte {
	p := &pb.AccountRegistrationPayloadProto{
		Kind:       SystemPayloadKindAccountRegistered,
		User:       user.Bytes(),
		ClusterKey: keyBytes,
		ParentSeq:  1,
	}
	b, _ := deterministicMarshal.Marshal(p)
	return b
}

// A cluster key of the wrong length must never be zero-padded or truncated into a different key.
func TestAccountRegistry_ClusterKeyMustBeExactly48Bytes(t *testing.T) {
	key := clusterKeyOf(0x11)
	h := NewAccountRegistryHandler(key)
	user := common.HexToAddress("0x00000000000000000000000000000000000000b0")

	bad := map[string][]byte{
		"47 bytes":      key[:47],
		"49 bytes":      append(key[:], 0x11),
		"empty bytes":   nil,
		"zero bytes":    []byte{},
		"short 3 bytes": []byte{1, 2, 3},
		"32 bytes":      make([]byte, 32),
		"64 bytes":      make([]byte, 64),
	}
	for name, k := range bad {
		t.Run(name, func(t *testing.T) {
			db := newFlagDB()
			err := h.Apply(db, payloadWithKey(user, k))
			assert.Error(t, err)
			assert.False(t, db.GetParentRegistered(user), "a malformed cluster key must never register anyone")
		})
	}

	good := map[string][]byte{
		"exact 48 bytes": key[:],
	}
	for name, k := range good {
		t.Run("ok "+name, func(t *testing.T) {
			db := newFlagDB()
			require.NoError(t, h.Apply(db, payloadWithKey(user, k)))
			assert.True(t, db.GetParentRegistered(user))
		})
	}
}

// A different (valid, 48-byte) cluster key is rejected, and a zero key never matches a real cluster.
func TestAccountRegistry_WrongOrZeroClusterKeyRejected(t *testing.T) {
	h := NewAccountRegistryHandler(clusterKeyOf(0x11))
	user := common.HexToAddress("0x00000000000000000000000000000000000000b1")
	for name, k := range map[string]cm.PublicKey{"other cluster": clusterKeyOf(0x22), "zero key": {}} {
		t.Run(name, func(t *testing.T) {
			db := newFlagDB()
			err := h.Apply(db, payloadWithKey(user, k[:]))
			assert.Error(t, err, name)
			assert.False(t, db.GetParentRegistered(user), name)
		})
	}
}

func TestRegistrationWorker_StopIsIdempotent(t *testing.T) {
	w := NewRegistrationWorker(newFlagDB(), nil, nil, clusterKeyOf(1))
	w.Start()
	w.Stop()
	assert.NotPanics(t, func() { w.Stop() })
}
