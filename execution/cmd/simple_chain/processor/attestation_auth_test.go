package processor

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// ProcessStateAttestation is routed for EVERY TCP connection and the connection type is self-declared, so any client can
// call it. An attestation feeds the fork detector that ends in fatal.Exit(), hence it must only count when a registered
// validator really signed it, and one validator must count once however many messages it sends.

func attestationFixture(t *testing.T) (*BlockProcessor, *storage.MemoryDB) {
	t.Helper()
	bp, _ := readRouteHarness(t)
	db := storage.NewMemoryDb()
	bp.attestationCollector = newAttestationCollector(db)
	return bp, db
}

func registerAttestingValidator(t *testing.T, bp *BlockProcessor, kp *bls.KeyPair) {
	t.Helper()
	pub := hex.EncodeToString(kp.BytesPublicKey())
	require.NoError(t, bp.chainState.GetStakeStateDB().CreateRegisterWithKeys(kp.Address(), "v", "", "", "", 0, big.NewInt(1),
		"", "", "", pub, []byte("p"), []byte("n"), "v", kp.BytesPublicKey()))
}

func sendAttestation(t *testing.T, bp *BlockProcessor, att StateAttestation) {
	t.Helper()
	body, err := json.Marshal(att)
	require.NoError(t, err)
	req := NewMockRequest(NewMockConnection(common.HexToAddress("0xabc")), NewMockMessage(p_common.StateAttestationTopic, body))
	require.NoError(t, bp.ProcessStateAttestation(req))
}

func attestationsFor(bp *BlockProcessor, block uint64) map[common.Hash][]string {
	bp.attestationCollector.mu.Lock()
	defer bp.attestationCollector.mu.Unlock()
	out := map[common.Hash][]string{}
	for h, n := range bp.attestationCollector.attestations[block] {
		out[h] = append([]string(nil), n...)
	}
	return out
}

func forgedHash(i int) common.Hash { return common.BigToHash(big.NewInt(int64(0xf00d00 + i))) }

func signedAttestation(kp *bls.KeyPair, block uint64, hash common.Hash, claimedAddr string) StateAttestation {
	return StateAttestation{
		BlockNumber: block, AttestationHash: hash, NodeAddress: claimedAddr,
		BLSSignature: bls.Sign(kp.PrivateKey(), hash.Bytes()).Bytes(), ValidatorPubKey: kp.BytesPublicKey(),
	}
}

func TestStateAttestation_UnsignedFromAnyClientIsIgnored(t *testing.T) {
	bp, db := attestationFixture(t)
	for i := 0; i < 50; i++ {
		sendAttestation(t, bp, StateAttestation{BlockNumber: 0, AttestationHash: forgedHash(i % 3), NodeAddress: fmt.Sprintf("fake-%d", i)})
	}
	require.Empty(t, attestationsFor(bp, 0), "unsigned attestations from an anonymous client must not reach the fork detector")
	require.Zero(t, db.Size(), "and must not be persisted (the key embeds the client-chosen NodeAddress)")
}

func TestStateAttestation_ValidSignatureFromNonValidatorIsIgnored(t *testing.T) {
	bp, _ := attestationFixture(t)
	outsider := bls.GenerateKeyPair()
	for i := 0; i < 10; i++ {
		sendAttestation(t, bp, signedAttestation(outsider, 0, forgedHash(i), outsider.Address().Hex()))
	}
	require.Empty(t, attestationsFor(bp, 0), "a correctly signed attestation from a key that is not a registered validator must not count")
}

func TestStateAttestation_RegisteredValidatorCountsOnceAndIdentityComesFromTheKey(t *testing.T) {
	bp, _ := attestationFixture(t)
	v := bls.GenerateKeyPair()
	registerAttestingValidator(t, bp, v)

	h := forgedHash(1)
	for i := 0; i < 5; i++ { // the same validator repeating itself, claiming someone else's address
		sendAttestation(t, bp, signedAttestation(v, 0, h, "0xSomeoneElse"))
	}
	// and then a conflicting second attestation for the same block: first wins, a validator is one vote
	sendAttestation(t, bp, signedAttestation(v, 0, forgedHash(2), v.Address().Hex()))

	got := attestationsFor(bp, 0)
	require.Len(t, got, 1, "one validator is one vote per block, whatever hash it sends")
	require.Equal(t, []string{v.Address().Hex()}, got[h], "identity is derived from the verified key, not from the NodeAddress field")
}

// An honest validator signs with block_signer; the receiver derives the identity from the public key alone. The two must
// agree, or the fix would silently reject every genuine attestation.
func TestStateAttestation_SignerAddressMatchesAddressDerivedFromItsPublicKey(t *testing.T) {
	kp := bls.GenerateKeyPair()
	require.Equal(t, kp.Address(), bls.GetAddressFromPublicKey(kp.BytesPublicKey()))

	_, pub, addr := bls.GenerateKeyPairFromSecretKey(hex.EncodeToString(kp.BytesPrivateKey()))
	require.Equal(t, addr, bls.GetAddressFromPublicKey(pub.Bytes()), "block_signer takes its address from GenerateKeyPairFromSecretKey")
}
