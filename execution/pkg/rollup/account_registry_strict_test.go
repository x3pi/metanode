package rollup

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
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

func payloadWithKey(user common.Address, keyJSON string) []byte {
	return []byte(fmt.Sprintf(`{"kind":"account_registered","user":"%s","cluster_key":%s,"parent_seq":1}`, user.Hex(), keyJSON))
}

// A cluster key of the wrong length must never be zero-padded or truncated into a different key.
func TestAccountRegistry_ClusterKeyMustBeExactly48Bytes(t *testing.T) {
	key := clusterKeyOf(0x11)
	h := NewAccountRegistryHandler(key)
	user := common.HexToAddress("0x00000000000000000000000000000000000000b0")

	full := hex.EncodeToString(key[:])
	bad := map[string]string{
		"47 bytes hex":       `"` + full[:94] + `"`,
		"49 bytes hex":       `"` + full + `11"`,
		"empty string":       `""`,
		"odd length hex":     `"` + full[:95] + `"`,
		"not hex":            `"zz` + full[2:] + `"`,
		"short int array":    `[1,2,3]`,
		"long int array":     "[" + strings.Repeat("17,", 48) + "17]",
		"out of range array": "[" + strings.Repeat("17,", 47) + "300]",
		"object":             `{"a":1}`,
	}
	for name, js := range bad {
		t.Run(name, func(t *testing.T) {
			db := newFlagDB()
			err := h.Apply(db, payloadWithKey(user, js))
			assert.Error(t, err)
			assert.False(t, db.GetParentRegistered(user), "a malformed cluster key must never register anyone")
		})
	}

	good := map[string]string{
		"hex":       `"` + full + `"`,
		"0x hex":    `"0x` + full + `"`,
		"int array": "[" + strings.Repeat("17,", 47) + "17]",
	}
	for name, js := range good {
		t.Run("ok "+name, func(t *testing.T) {
			db := newFlagDB()
			require.NoError(t, h.Apply(db, payloadWithKey(user, js)))
			assert.True(t, db.GetParentRegistered(user))
		})
	}
}

// A different (valid, 48-byte) cluster key is rejected, and a zero key never matches a real cluster.
func TestAccountRegistry_WrongOrZeroClusterKeyRejected(t *testing.T) {
	h := NewAccountRegistryHandler(clusterKeyOf(0x11))
	user := common.HexToAddress("0x00000000000000000000000000000000000000b1")
	for name, k := range map[string]cm.PublicKey{"other cluster": clusterKeyOf(0x22), "zero key": {}} {
		db := newFlagDB()
		err := h.Apply(db, payloadWithKey(user, `"`+hex.EncodeToString(k[:])+`"`))
		assert.Error(t, err, name)
		assert.False(t, db.GetParentRegistered(user), name)
	}
}

func TestRegistrationWorker_StopIsIdempotent(t *testing.T) {
	w := NewRegistrationWorker(newFlagDB(), nil, nil, clusterKeyOf(1))
	w.Start()
	w.Stop()
	assert.NotPanics(t, func() { w.Stop() })
}
