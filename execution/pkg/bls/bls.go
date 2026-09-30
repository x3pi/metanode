package bls

import (
	"crypto/rand"
	"encoding/hex"
	"runtime"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	blst "github.com/meta-node-blockchain/meta-node/pkg/bls/blst/bindings/go"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

type blstPublicKey = blst.P1Affine
type blstSignature = blst.P2Affine
type blstAggregateSignature = blst.P2Aggregate
type blstAggregatePublicKey = blst.P1Aggregate
type blstSecretKey = blst.SecretKey

var dstMinPk = []byte("BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_")

func Init() {
	blst.SetMaxProcs(runtime.GOMAXPROCS(0))
}

func Sign(bPri cm.PrivateKey, bMessage []byte) cm.Sign {
	sk := new(blstSecretKey).Deserialize(bPri.Bytes())
	sign := new(blstSignature).Sign(sk, bMessage, dstMinPk)
	return cm.SignFromBytes(sign.Compress())
}

func GetByteAddress(pubkey []byte) []byte {
	hash := crypto.Keccak256(pubkey)
	address := hash[12:]
	return address
}

func VerifySign(bPub cm.PublicKey, bSig cm.Sign, bMsg []byte) bool {
	return new(blstSignature).VerifyCompressed(bSig.Bytes(), true, bPub.Bytes(), false, bMsg, dstMinPk)
}

// VerifyBatch reports whether EVERY (pub, sig, msg) triple is a valid signature. It uses blst's
// random-linear-combination batch verification (64-bit random scalars from crypto/rand), which is sound
// against forged signatures that only cancel out in an aggregate, and is markedly cheaper per signature
// than n independent verifications (one shared final exponentiation, no per-item final check).
// false means "at least one is invalid (or malformed)": callers must fall back to per-item VerifySign to
// find which. The verdict per item is therefore identical to calling VerifySign on each.
func VerifyBatch(bPubs, bSigs, bMsgs [][]byte) bool {
	n := len(bPubs)
	if n == 0 || len(bSigs) != n || len(bMsgs) != n {
		return false
	}
	pks := make([]*blstPublicKey, n)
	sigs := make([]*blstSignature, n)
	msgs := make([]blst.Message, n)
	for i := 0; i < n; i++ {
		pks[i] = new(blstPublicKey).Uncompress(bPubs[i])
		sigs[i] = new(blstSignature).Uncompress(bSigs[i])
		if pks[i] == nil || sigs[i] == nil {
			return false
		}
		msgs[i] = bMsgs[i]
	}
	randFn := func(s *blst.Scalar) {
		var buf [32]byte // Scalar is 256-bit; only the low randBits (64) are used by blst
		_, _ = rand.Read(buf[:8])
		buf[0] |= 1 // never a zero coefficient (would skip that item)
		s.FromLEndian(buf[:])
	}
	return new(blstSignature).MultipleAggregateVerify(sigs, true, pks, false, msgs, dstMinPk, randFn, 64)
}

func VerifyAggregateSign(bPubs [][]byte, bSig []byte, bMsgs [][]byte) bool {
	return new(blstSignature).AggregateVerifyCompressed(bSig, true, bPubs, false, bMsgs, dstMinPk)
}

func GenerateKeyPairFromSecretKey(hexSecretKey string) (cm.PrivateKey, cm.PublicKey, common.Address) {
	secByte, _ := hex.DecodeString(hexSecretKey)
	sec := new(blstSecretKey).Deserialize(secByte)
	pub := new(blstPublicKey).From(sec).Compress()
	hash := crypto.Keccak256([]byte(pub))
	return cm.PrivateKeyFromBytes(sec.Serialize()), cm.PubkeyFromBytes(pub), common.BytesToAddress(hash[12:])
}

func randBLSTSecretKey() *blstSecretKey {
	var t [32]byte
	_, _ = rand.Read(t[:])
	secretKey := blst.KeyGen(t[:])
	return secretKey
}

func GenerateKeyPair() *KeyPair {
	sec := randBLSTSecretKey()
	return NewKeyPair(sec.Serialize())
}

func CreateAggregateSign(bSignatures [][]byte) []byte {
	aggregatedSignature := new(blst.P2Aggregate)
	aggregatedSignature.AggregateCompressed(bSignatures, false)
	return aggregatedSignature.ToAffine().Compress()
}
