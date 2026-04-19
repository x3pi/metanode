package bls

import (
	"encoding/hex"
	"fmt"
	"log"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

type KeyPair struct {
	publicKey  cm.PublicKey
	privateKey cm.PrivateKey
	address    common.Address
}

func NewKeyPair(privateKey []byte) *KeyPair {
	sec := new(blstSecretKey).Deserialize(privateKey)
	if sec == nil {
		log.Fatalf("NewKeyPair: invalid BLS private key: %x", privateKey)
	}
	pub := new(blstPublicKey).From(sec).Compress()
	hash := crypto.Keccak256([]byte(pub))
	return &KeyPair{
		privateKey: cm.PrivateKeyFromBytes(sec.Serialize()),
		publicKey:  cm.PubkeyFromBytes(pub),
		address:    common.BytesToAddress(hash[12:]),
	}
}

func (kp *KeyPair) PrivateKey() cm.PrivateKey {
	return kp.privateKey
}

func (kp *KeyPair) BytesPrivateKey() []byte {
	return kp.privateKey.Bytes()
}

func (kp *KeyPair) PublicKey() cm.PublicKey {
	return kp.publicKey
}

func (kp *KeyPair) BytesPublicKey() []byte {
	return kp.publicKey.Bytes()
}

func (kp *KeyPair) Address() common.Address {
	return kp.address
}

func (kp *KeyPair) String() string {
	str := fmt.Sprintf("Private key: %v\nPublic key: %v\nAddress: %v\n",
		hex.EncodeToString(kp.privateKey.Bytes()),
		hex.EncodeToString(kp.publicKey.Bytes()),
		hex.EncodeToString(kp.address.Bytes()),
	)
	return str
}

func GetAddressFromPublicKey(pubKey []byte) common.Address {
	if len(pubKey) == 0 {
		return common.Address{}
	}
	hash := crypto.Keccak256([]byte(pubKey))
	address := common.BytesToAddress(hash[12:])
	return address
}
