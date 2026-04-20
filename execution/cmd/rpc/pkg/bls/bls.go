package bls

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
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

// blsCurveOrder is the order r of the BLS12-381 curve.
// Valid private key must satisfy: 0 < key < r.
var blsCurveOrder, _ = new(big.Int).SetString(
	"73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff00000001", 16,
)

// ===== PUBLIC API FUNCTIONS (NO LOCK VERSION) =====

// Sign performs BLS signing without lock (direct CGO call)
func Sign(bPri cm.PrivateKey, bMessage []byte) (result cm.Sign) {
	// Panic recovery
	defer func() {
		if r := recover(); r != nil {
			result = cm.Sign{}
		}
	}()

	// Validate input
	if !ValidateBlsPrivateKey(bPri.Bytes()) {
		return cm.Sign{}
	}

	// Direct CGO call - NO LOCK
	sk := new(blstSecretKey).Deserialize(bPri.Bytes())
	sign := new(blstSignature).Sign(sk, bMessage, dstMinPk)
	result = cm.SignFromBytes(sign.Compress())
	return
}

// SignConcurrent is alias for Sign (for backward compatibility)
func SignConcurrent(bPri cm.PrivateKey, bMessage []byte) cm.Sign {
	return Sign(bPri, bMessage)
}

// VerifySign performs BLS verification without lock (direct CGO call)
func VerifySign(bPub cm.PublicKey, bSig cm.Sign, bMsg []byte) (result bool) {
	// Panic recovery
	defer func() {
		if r := recover(); r != nil {
			result = false
		}
	}()

	// Direct CGO call - NO LOCK
	result = new(blstSignature).VerifyCompressed(bSig.Bytes(), true, bPub.Bytes(), false, bMsg, dstMinPk)
	return
}

// VerifySignConcurrent is alias for VerifySign (for backward compatibility)
func VerifySignConcurrent(bPub cm.PublicKey, bSig cm.Sign, bMsg []byte) bool {
	return VerifySign(bPub, bSig, bMsg)
}

// ValidateBlsPrivateKey validates a BLS private key before use
func ValidateBlsPrivateKey(keyBytes []byte) bool {
	if len(keyBytes) != 32 {
		return false
	}

	// Check if key is all zeros
	isZero := true
	for _, b := range keyBytes {
		if b != 0 {
			isZero = false
			break
		}
	}
	if isZero {
		return false
	}

	// Check key < curve order
	keyInt := new(big.Int).SetBytes(keyBytes)
	if keyInt.Cmp(blsCurveOrder) >= 0 {
		return false
	}

	return true
}

func Init() {
	blst.SetMaxProcs(runtime.GOMAXPROCS(0))
}

func GetByteAddress(pubkey []byte) []byte {
	hash := crypto.Keccak256(pubkey)
	address := hash[12:]
	return address
}

// ===== LEGACY COMPATIBILITY FUNCTIONS (NO LOCK) =====

func VerifyAggregateSign(bPubs [][]byte, bSig []byte, bMsgs [][]byte) bool {
	// Direct CGO call - NO LOCK
	return new(blstSignature).AggregateVerifyCompressed(bSig, true, bPubs, false, bMsgs, dstMinPk)
}

func GenerateKeyPairFromSecretKey(hexSecretKey string) (cm.PrivateKey, cm.PublicKey, common.Address) {
	// Validate input before CGO calls
	secByte, err := hex.DecodeString(hexSecretKey)
	if err != nil || len(secByte) != 32 {
		return cm.PrivateKey{}, cm.PublicKey{}, common.Address{}
	}

	if !ValidateBlsPrivateKey(secByte) {
		return cm.PrivateKey{}, cm.PublicKey{}, common.Address{}
	}

	// Direct CGO call - NO LOCK
	sec := new(blstSecretKey).Deserialize(secByte)
	pk := new(blstPublicKey).From(sec).Compress()
	hash := crypto.Keccak256([]byte(pk))
	return cm.PrivateKeyFromBytes(sec.Serialize()), cm.PubkeyFromBytes(pk), common.BytesToAddress(hash[12:])
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
	// Direct CGO call - NO LOCK
	aggregatedSignature := new(blst.P2Aggregate)
	aggregatedSignature.AggregateCompressed(bSignatures, false)
	return aggregatedSignature.ToAffine().Compress()
}
