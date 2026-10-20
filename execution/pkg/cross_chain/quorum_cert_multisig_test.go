package cross_chain

import (
	"bytes"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Quorum-cert verification over a committee with uneven stakes and a custom threshold: which signer subsets pass.
func TestVerifyQuorumCert_UnevenStakesAndCustomThreshold(t *testing.T) {
	stakes := []uint64{1000, 2000, 3000}
	var kps []*bls.KeyPair
	reg := ChainRegistry{ChainID: 106, Epoch: 1, QuorumThreshold: 7500} // needs ceil(6000*0.75) = 4500 stake
	for i, s := range stakes {
		kp := bls.NewKeyPair(bytes.Repeat([]byte{0x06, byte(0x30 + i)}, 16))
		kps = append(kps, kp)
		reg.Committee = append(reg.Committee, ValidatorEntry{PubkeyBLS: kp.BytesPublicKey(), Stake: s})
	}
	digest := []byte("digest-under-test")
	cert := func(signers []int, bitmapBits []int) QuorumCert {
		var sigs [][]byte
		for _, i := range signers {
			sigs = append(sigs, bls.Sign(kps[i].PrivateKey(), digest).Bytes())
		}
		bm := []byte{0}
		for _, i := range bitmapBits {
			bm[0] |= 1 << uint(i)
		}
		return QuorumCert{Epoch: 1, AggregateSignature: bls.CreateAggregateSign(sigs), SignerBitmap: bm}
	}
	cases := []struct {
		name    string
		signers []int
		bitmap  []int
		wantErr error
		ok      bool
	}{
		{"all three", []int{0, 1, 2}, []int{0, 1, 2}, nil, true},
		{"2000+3000 = 5000 >= 4500", []int{1, 2}, []int{1, 2}, nil, true},
		{"1000+3000 = 4000 < 4500", []int{0, 2}, []int{0, 2}, ErrQuorumNotReached, false},
		{"1000+2000 = 3000 < 4500", []int{0, 1}, []int{0, 1}, ErrQuorumNotReached, false},
		{"3000 alone", []int{2}, []int{2}, ErrQuorumNotReached, false},
		{"bitmap claims a member who did not sign", []int{1, 2}, []int{0, 1, 2}, ErrInvalidBLSSignature, false},
		{"bitmap omits a member who did sign", []int{0, 1, 2}, []int{1, 2}, ErrInvalidBLSSignature, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := VerifyQuorumCertAgainstRegistry(reg, cert(c.signers, c.bitmap), digest)
			if c.ok {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, c.wantErr)
		})
	}
	// default threshold (2*total/3 + 1 = 4001): 1000+3000 = 4000 is one short, 2000+3000 passes
	reg.QuorumThreshold = 0
	assert.ErrorIs(t, VerifyQuorumCertAgainstRegistry(reg, cert([]int{0, 2}, []int{0, 2}), digest), ErrQuorumNotReached)
	assert.NoError(t, VerifyQuorumCertAgainstRegistry(reg, cert([]int{1, 2}, []int{1, 2}), digest))
}
