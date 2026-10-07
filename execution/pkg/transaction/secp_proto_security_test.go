package transaction

import (
	"crypto/ecdsa"
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

func fullyPopulatedSecpTx(t *testing.T, key *ecdsa.PrivateKey) *Transaction {
	t.Helper()
	tx := &Transaction{proto: &pb.Transaction{
		FromAddress:         crypto.PubkeyToAddress(key.PublicKey).Bytes(),
		ToAddress:           common.HexToAddress("0x2222222222222222222222222222222222222222").Bytes(),
		Amount:              []byte{0x03, 0xe8},
		MaxGas:              21000,
		MaxGasPrice:         1000,
		MaxTimeUse:          5,
		Data:                []byte{0xde, 0xad},
		LastDeviceKey:       common.HexToHash("0x01").Bytes(),
		NewDeviceKey:        common.HexToHash("0x02").Bytes(),
		Nonce:               []byte{0, 0, 0, 0, 0, 0, 0, 7},
		ChainID:             991,
		GasTipCap:           []byte{1},
		GasFeeCap:           []byte{2},
		AccessList:          []*pb.AccessTuple{{Address: common.HexToAddress("0x33").Bytes(), StorageKeys: [][]byte{common.HexToHash("0x44").Bytes()}}},
		BlobVersionedHashes: [][]byte{common.HexToHash("0x55").Bytes()},
		MaxFeePerBlobGas:    []byte{3},
		Type:                0xFF,
	}}
	require.NoError(t, tx.SignSecpProto(key))
	require.True(t, tx.ValidSecpProtoSign())
	return tx
}

// Every field committed by TransactionHashData is covered by the signature: changing ANY of them (even ones an
// Ethereum sighash would not cover, e.g. device keys / MaxTimeUse) invalidates the tx, so an in-flight tx cannot be
// altered by a relayer or a Byzantine proposer.
func TestSecpProtoSecurity_EveryCommittedFieldIsSigned(t *testing.T) {
	key, _ := crypto.GenerateKey()
	mutators := map[string]func(p *pb.Transaction){
		"ToAddress":     func(p *pb.Transaction) { p.ToAddress[0] ^= 1 },
		"Amount":        func(p *pb.Transaction) { p.Amount = []byte{0x03, 0xe9} },
		"MaxGas":        func(p *pb.Transaction) { p.MaxGas++ },
		"MaxGasPrice":   func(p *pb.Transaction) { p.MaxGasPrice++ },
		"MaxTimeUse":    func(p *pb.Transaction) { p.MaxTimeUse++ },
		"Data":          func(p *pb.Transaction) { p.Data = []byte{0xde, 0xae} },
		"LastDeviceKey": func(p *pb.Transaction) { p.LastDeviceKey[0] ^= 1 },
		"NewDeviceKey":  func(p *pb.Transaction) { p.NewDeviceKey[0] ^= 1 },
		"Nonce":         func(p *pb.Transaction) { p.Nonce[7]++ },
		"ChainID":       func(p *pb.Transaction) { p.ChainID++ },
		"GasTipCap":     func(p *pb.Transaction) { p.GasTipCap = []byte{9} },
		"GasFeeCap":     func(p *pb.Transaction) { p.GasFeeCap = []byte{9} },
		"AccessList":    func(p *pb.Transaction) { p.AccessList[0].StorageKeys[0][0] ^= 1 },
		"AccessListAdd": func(p *pb.Transaction) { p.AccessList = append(p.AccessList, &pb.AccessTuple{Address: []byte{1}}) },
		"BlobHashes":    func(p *pb.Transaction) { p.BlobVersionedHashes[0][0] ^= 1 },
		"MaxFeePerBlob": func(p *pb.Transaction) { p.MaxFeePerBlobGas = []byte{9} },
	}
	for name, mutate := range mutators {
		t.Run(name, func(t *testing.T) {
			tx := fullyPopulatedSecpTx(t, key)
			cp := proto.Clone(tx.proto).(*pb.Transaction)
			mutate(cp)
			tampered := &Transaction{proto: cp}
			assert.False(t, tampered.ValidSecpProtoSign(), "mutating %s must invalidate the signature", name)
			assert.False(t, tampered.ValidSecpSign())
		})
	}
}

// A signature made for one transaction type can never be replayed as another: a 0xFF signature relabelled as an
// Ethereum type is not a valid Ethereum signature, and an Ethereum-signed tx relabelled 0xFF is not a valid proto
// signature (different hash domain, different R/S/V encoding).
func TestSecpProtoSecurity_NoCrossTypeConfusion(t *testing.T) {
	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)

	proto0xFF := fullyPopulatedSecpTx(t, key)
	for _, typ := range []uint64{0, 1, 2, 3, 4} {
		cp := proto.Clone(proto0xFF.proto).(*pb.Transaction)
		cp.Type = typ
		relabelled := &Transaction{proto: cp}
		assert.False(t, relabelled.ValidSecpSign(), "0xFF signature must not validate as type %d", typ)
		assert.False(t, relabelled.ValidEthSign(), "0xFF signature must not validate as Ethereum type %d", typ)
	}

	// A genuine EIP-1559 signature, imported the way eth_sendRawTransaction does, then relabelled 0xFF.
	signer := e_types.LatestSignerForChainID(big.NewInt(991))
	ethTx, err := e_types.SignNewTx(key, signer, &e_types.DynamicFeeTx{
		ChainID: big.NewInt(991), Nonce: 7, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(1000), Gas: 21000,
		To: &common.Address{0x22}, Value: big.NewInt(1000),
	})
	require.NoError(t, err)
	imported, err := NewTransactionFromEth(ethTx)
	require.NoError(t, err)
	importedTx := imported.(*Transaction)
	require.True(t, importedTx.ValidEthSign(), "sanity: the imported ETH tx is valid as ETH")
	require.Equal(t, from, importedTx.FromAddress())

	cp := proto.Clone(importedTx.proto).(*pb.Transaction)
	cp.Type = 0xFF
	cp.Sign = nil
	relabelled := &Transaction{proto: cp}
	assert.False(t, relabelled.ValidSecpProtoSign(), "an ETH signature relabelled 0xFF must be rejected")
}

// A signature by one key can never authenticate a tx claiming another sender, and degenerate r/s values are rejected.
func TestSecpProtoSecurity_SenderBindingAndDegenerateSignatures(t *testing.T) {
	victimKey, _ := crypto.GenerateKey()
	attackerKey, _ := crypto.GenerateKey()

	// Attacker signs a tx that names the victim as sender.
	forged := fullyPopulatedSecpTx(t, attackerKey)
	forged.proto.FromAddress = crypto.PubkeyToAddress(victimKey.PublicKey).Bytes()
	forged.ClearCacheHash()
	assert.False(t, forged.ValidSecpProtoSign(), "signature of another key must not authenticate the claimed sender")

	n := crypto.S256().Params().N
	pad := func(v *big.Int) []byte { return common.LeftPadBytes(v.Bytes(), 32) }
	good := fullyPopulatedSecpTx(t, victimKey)
	for name, rs := range map[string][2][]byte{
		"r=0":   {pad(big.NewInt(0)), good.proto.S},
		"s=0":   {good.proto.R, pad(big.NewInt(0))},
		"r=N":   {pad(n), good.proto.S},
		"s=N":   {good.proto.R, pad(n)},
		"r=N+1": {pad(new(big.Int).Add(n, big.NewInt(1))), good.proto.S},
	} {
		cp := proto.Clone(good.proto).(*pb.Transaction)
		cp.R, cp.S = rs[0], rs[1]
		assert.False(t, (&Transaction{proto: cp}).ValidSecpProtoSign(), name)
	}
	for _, v := range []byte{2, 27, 28, 0xFF} {
		cp := proto.Clone(good.proto).(*pb.Transaction)
		cp.V = []byte{v}
		assert.False(t, (&Transaction{proto: cp}).ValidSecpProtoSign(), "V=%d must be rejected (only 0/1 allowed)", v)
	}
	// Non-canonical (unpadded / over-long) encodings are rejected, so one signature has exactly one encoding.
	cp := proto.Clone(good.proto).(*pb.Transaction)
	cp.R = append([]byte{0}, cp.R...)
	assert.False(t, (&Transaction{proto: cp}).ValidSecpProtoSign(), "33-byte R")
	cp = proto.Clone(good.proto).(*pb.Transaction)
	cp.S = cp.S[1:]
	assert.False(t, (&Transaction{proto: cp}).ValidSecpProtoSign(), "31-byte S")
}

// Random garbage in R/S/V (and random field contents) must never panic and never validate.
func TestSecpProtoSecurity_RandomGarbageNeverValidates(t *testing.T) {
	key, _ := crypto.GenerateKey()
	good := fullyPopulatedSecpTx(t, key)
	rng := rand.New(rand.NewSource(1))
	rnd := func(n int) []byte { b := make([]byte, n); rng.Read(b); return b }

	for i := 0; i < 2000; i++ {
		cp := proto.Clone(good.proto).(*pb.Transaction)
		switch i % 4 {
		case 0:
			cp.R, cp.S, cp.V = rnd(32), rnd(32), []byte{byte(rng.Intn(2))}
		case 1:
			cp.R, cp.S, cp.V = rnd(rng.Intn(70)), rnd(rng.Intn(70)), rnd(rng.Intn(4))
		case 2:
			cp.FromAddress, cp.ToAddress, cp.Nonce, cp.Amount = rnd(rng.Intn(40)), rnd(rng.Intn(40)), rnd(rng.Intn(12)), rnd(rng.Intn(40))
		case 3:
			cp.R, cp.S, cp.V, cp.Sign = nil, nil, nil, rnd(65)
		}
		tx := &Transaction{proto: cp}
		assert.NotPanics(t, func() {
			assert.False(t, tx.ValidSecpProtoSign(), "iteration %d", i)
		})
	}
}
