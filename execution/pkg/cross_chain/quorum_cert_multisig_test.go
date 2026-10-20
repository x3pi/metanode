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

// A ten-member committee needs a two-byte signer bitmap: members 8 and 9 live in the second byte.
func TestVerifyQuorumCert_TenMembersTwoByteBitmap(t *testing.T) {
	reg := ChainRegistry{ChainID: 107, Epoch: 1} // 10 x 100 stake, default threshold 2*1000/3 + 1 = 667
	var kps []*bls.KeyPair
	for i := 0; i < 10; i++ {
		kp := bls.NewKeyPair(bytes.Repeat([]byte{0x07, byte(0x40 + i)}, 16))
		kps = append(kps, kp)
		reg.Committee = append(reg.Committee, ValidatorEntry{PubkeyBLS: kp.BytesPublicKey(), Stake: 100})
	}
	digest := []byte("ten-member-digest")
	cert := func(signers []int, bitmap []byte) QuorumCert {
		var sigs [][]byte
		for _, i := range signers {
			sigs = append(sigs, bls.Sign(kps[i].PrivateKey(), digest).Bytes())
		}
		return QuorumCert{Epoch: 1, AggregateSignature: bls.CreateAggregateSign(sigs), SignerBitmap: bitmap}
	}
	bitmapOf := func(signers []int, size int) []byte {
		bm := make([]byte, size)
		for _, i := range signers {
			if i/8 < size {
				bm[i/8] |= 1 << uint(i%8)
			}
		}
		return bm
	}
	seven := []int{0, 1, 2, 3, 7, 8, 9} // 700 >= 667, three of them in the second byte region
	six := []int{0, 1, 2, 3, 8, 9}      // 600 < 667
	assert.NoError(t, VerifyQuorumCertAgainstRegistry(reg, cert(seven, bitmapOf(seven, 2)), digest))
	assert.ErrorIs(t, VerifyQuorumCertAgainstRegistry(reg, cert(six, bitmapOf(six, 2)), digest), ErrQuorumNotReached)
	// a one-byte bitmap cannot name members 8 and 9: they are not counted, the stake falls short
	assert.ErrorIs(t, VerifyQuorumCertAgainstRegistry(reg, cert(seven, bitmapOf(seven, 1)), digest), ErrQuorumNotReached)
	// a longer bitmap with a stray bit beyond the committee does not add stake
	extra := bitmapOf(seven, 2)
	extra[1] |= 1 << 6 // member index 14 does not exist
	assert.NoError(t, VerifyQuorumCertAgainstRegistry(reg, cert(seven, extra), digest))
}
