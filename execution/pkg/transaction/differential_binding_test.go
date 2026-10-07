package transaction

import (
	"crypto/sha256"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// TestDifferential_ValidateEnvelopeBinding compares the direct validator against the canonical
// validator across all supported Ethereum transaction types (Legacy, EIP-2930, EIP-1559, EIP-4844,
// EIP-7702), verifying identical verdicts for both authentic transactions and all field-mutation cases.
func TestDifferential_ValidateEnvelopeBinding(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainID := big.NewInt(991)
	to := common.HexToAddress("0x1122334455667788990011223344556677889900")

	// 1. Generate diverse base transactions
	var ethTxs []*e_types.Transaction

	// 1a. Legacy Tx (EIP-155)
	legacyEip155, err := e_types.SignNewTx(key, e_types.NewEIP155Signer(chainID), &e_types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(20000000000),
		Gas:      21000,
		To:       &to,
		Value:    big.NewInt(1000000),
		Data:     []byte{0xaa, 0xbb},
	})
	require.NoError(t, err)
	ethTxs = append(ethTxs, legacyEip155)

	// 1b. Legacy Tx (Homestead / pre-EIP155)
	legacyHomestead, err := e_types.SignNewTx(key, e_types.HomesteadSigner{}, &e_types.LegacyTx{
		Nonce:    2,
		GasPrice: big.NewInt(15000000000),
		Gas:      25000,
		To:       &to,
		Value:    big.NewInt(500000),
		Data:     nil,
	})
	require.NoError(t, err)
	ethTxs = append(ethTxs, legacyHomestead)

	// 1c. EIP-2930
	eip2930, err := e_types.SignNewTx(key, e_types.NewLondonSigner(chainID), &e_types.AccessListTx{
		ChainID:  chainID,
		Nonce:    3,
		GasPrice: big.NewInt(20000000000),
		Gas:      30000,
		To:       &to,
		Value:    big.NewInt(2000000),
		Data:     []byte{0x12, 0x34},
		AccessList: e_types.AccessList{
			{Address: to, StorageKeys: []common.Hash{common.HexToHash("0x01")}},
		},
	})
	require.NoError(t, err)
	ethTxs = append(ethTxs, eip2930)

	// 1d. EIP-1559
	eip1559, err := e_types.SignNewTx(key, e_types.NewLondonSigner(chainID), &e_types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     4,
		GasTipCap: big.NewInt(1000000000),
		GasFeeCap: big.NewInt(30000000000),
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(3000000),
		Data:      nil,
	})
	require.NoError(t, err)
	ethTxs = append(ethTxs, eip1559)

	// 1e. EIP-4844 (Blob)
	var blob kzg4844.Blob
	commitment, err := kzg4844.BlobToCommitment(&blob)
	require.NoError(t, err)
	proof, err := kzg4844.ComputeBlobProof(&blob, commitment)
	require.NoError(t, err)
	vh := common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &commitment))

	eip4844, err := e_types.SignNewTx(key, e_types.NewCancunSigner(chainID), &e_types.BlobTx{
		ChainID:    uint256.MustFromBig(chainID),
		Nonce:      5,
		GasTipCap:  uint256.NewInt(1000000000),
		GasFeeCap:  uint256.NewInt(30000000000),
		Gas:        50000,
		To:         to,
		Value:      uint256.NewInt(4000000),
		BlobHashes: []common.Hash{vh},
		BlobFeeCap: uint256.NewInt(100),
		Sidecar: &e_types.BlobTxSidecar{
			Blobs:       []kzg4844.Blob{blob},
			Commitments: []kzg4844.Commitment{commitment},
			Proofs:      []kzg4844.Proof{proof},
		},
	})
	require.NoError(t, err)
	ethTxs = append(ethTxs, eip4844)

	// 1f. EIP-7702 (SetCode)
	authKey, _ := crypto.GenerateKey()
	auth, err := e_types.SignSetCode(authKey, e_types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(chainID),
		Address: to,
		Nonce:   0,
	})
	require.NoError(t, err)

	eip7702, err := e_types.SignNewTx(key, e_types.NewPragueSigner(chainID), &e_types.SetCodeTx{
		ChainID:   uint256.MustFromBig(chainID),
		Nonce:     6,
		GasTipCap: uint256.NewInt(1000000000),
		GasFeeCap: uint256.NewInt(30000000000),
		Gas:       60000,
		To:        to,
		Value:     uint256.NewInt(5000000),
		AuthList:  []e_types.SetCodeAuthorization{auth},
	})
	require.NoError(t, err)
	ethTxs = append(ethTxs, eip7702)

	// 1g. Contract Deployment (Legacy with To == nil)
	deployTx, err := e_types.SignNewTx(key, e_types.NewEIP155Signer(chainID), &e_types.LegacyTx{
		Nonce:    7,
		GasPrice: big.NewInt(20000000000),
		Gas:      100000,
		To:       nil,
		Value:    big.NewInt(0),
		Data:     []byte{0x60, 0x80, 0x60, 0x40},
	})
	require.NoError(t, err)
	ethTxs = append(ethTxs, deployTx)

	for idx, ethTx := range ethTxs {
		metaTx, err := NewTransactionFromEth(ethTx)
		require.NoError(t, err, "tx %d NewTransactionFromEth", idx)
		origPb := metaTx.Proto().(*pb.Transaction)

		// Test authentic tx: both must return nil
		directErr := validateDirectEnvelopeBinding(origPb, ethTx)
		canonErr := validateCanonicalEnvelopeBinding(origPb, ethTx)
		require.NoError(t, directErr, "direct on genuine tx %d (%s)", idx, txTypeDesc(ethTx.Type()))
		require.NoError(t, canonErr, "canonical on genuine tx %d (%s)", idx, txTypeDesc(ethTx.Type()))

		// Now run differential mutations
		mutators := []struct {
			name   string
			mutate func(p *pb.Transaction)
		}{
			{"tamper FromAddress", func(p *pb.Transaction) { p.FromAddress = common.HexToAddress("0xdead").Bytes() }},
			{"tamper ToAddress", func(p *pb.Transaction) { p.ToAddress = common.HexToAddress("0xdead").Bytes() }},
			{"tamper Amount", func(p *pb.Transaction) { p.Amount = []byte{0xff, 0xff} }},
			{"tamper Nonce", func(p *pb.Transaction) { p.Nonce = []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x99} }},
			{"tamper MaxGas", func(p *pb.Transaction) { p.MaxGas += 1 }},
			{"tamper MaxGasPrice", func(p *pb.Transaction) { p.MaxGasPrice += 100 }},
			{"tamper GasFeeCap", func(p *pb.Transaction) { p.GasFeeCap = []byte{0x99} }},
			{"tamper GasTipCap", func(p *pb.Transaction) { p.GasTipCap = []byte{0x88} }},
			{"tamper Data", func(p *pb.Transaction) { p.Data = append(p.Data, 0x99) }},
			{"tamper Type", func(p *pb.Transaction) { p.Type = 99 }},
			{"tamper ChainID", func(p *pb.Transaction) { p.ChainID += 1 }},
			{"tamper R", func(p *pb.Transaction) { p.R = []byte{0x01} }},
			{"tamper S", func(p *pb.Transaction) { p.S = []byte{0x02} }},
			{"tamper V", func(p *pb.Transaction) { p.V = []byte{0x03} }},
			{"tamper Sign", func(p *pb.Transaction) { p.Sign = []byte{0x04} }},
			{"inject MaxTimeUse", func(p *pb.Transaction) { p.MaxTimeUse = 500 }},
			{"inject LastDeviceKey", func(p *pb.Transaction) { p.LastDeviceKey = []byte{0x01} }},
			{"inject NewDeviceKey", func(p *pb.Transaction) { p.NewDeviceKey = []byte{0x02} }},
			{"inject ReadOnly", func(p *pb.Transaction) { p.ReadOnly = true }},
			{"inject AccessList", func(p *pb.Transaction) {
				p.AccessList = []*pb.AccessTuple{{Address: to.Bytes()}}
			}},
			{"inject BlobVersionedHashes", func(p *pb.Transaction) {
				p.BlobVersionedHashes = [][]byte{common.HexToHash("0x02").Bytes()}
			}},
			{"inject MaxFeePerBlobGas", func(p *pb.Transaction) {
				p.MaxFeePerBlobGas = []byte{0x05}
			}},
			{"inject AuthorizationList", func(p *pb.Transaction) {
				p.AuthorizationList = []*pb.SetCodeAuthorization{{Address: to.Bytes()}}
			}},
		}

		for _, m := range mutators {
			t.Run(m.name, func(t *testing.T) {
				clone := proto.Clone(origPb).(*pb.Transaction)
				m.mutate(clone)

				dErr := validateDirectEnvelopeBinding(clone, ethTx)
				cErr := validateCanonicalEnvelopeBinding(clone, ethTx)

				// Both must agree on validity verdict
				dValid := (dErr == nil)
				cValid := (cErr == nil)
				require.Equal(t, cValid, dValid,
					"verdict mismatch for tx %d (%s) mutation %s: direct=%v, canon=%v",
					idx, txTypeDesc(ethTx.Type()), m.name, dErr, cErr)
			})
		}
	}
}

func txTypeDesc(t byte) string {
	switch t {
	case e_types.LegacyTxType:
		return "Legacy"
	case e_types.AccessListTxType:
		return "EIP-2930"
	case e_types.DynamicFeeTxType:
		return "EIP-1559"
	case e_types.BlobTxType:
		return "EIP-4844"
	case e_types.SetCodeTxType:
		return "EIP-7702"
	default:
		return "Unknown"
	}
}
