package transaction

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/holiman/uint256"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// makeValidBlobSidecar builds one real, KZG-valid blob + commitment + proof + its
// versioned hash, exercising the actual go-ethereum KZG library rather than fakes.
func makeValidBlobSidecar(t *testing.T) (blob kzg4844.Blob, commitment kzg4844.Commitment, proof kzg4844.Proof, versionedHash common.Hash) {
	t.Helper()
	// A well-formed (all-zero) blob is valid input to BlobToCommitment — each
	// 32-byte field element just needs to be < the BLS modulus, and zero always is.
	commitment, err := kzg4844.BlobToCommitment(&blob)
	if err != nil {
		t.Fatalf("BlobToCommitment: %v", err)
	}
	proof, err = kzg4844.ComputeBlobProof(&blob, commitment)
	if err != nil {
		t.Fatalf("ComputeBlobProof: %v", err)
	}
	versionedHash = common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &commitment))
	return blob, commitment, proof, versionedHash
}

func TestVerifyBlobSidecar_Valid(t *testing.T) {
	blob, commitment, proof, vh := makeValidBlobSidecar(t)

	pTx := &pb.Transaction{
		Type:                types.BlobTxType,
		BlobVersionedHashes: [][]byte{vh.Bytes()},
		Sidecar: &pb.BlobSidecar{
			Blobs:       [][]byte{blob[:]},
			Commitments: [][]byte{commitment[:]},
			Proofs:      [][]byte{proof[:]},
		},
	}

	if err := VerifyBlobSidecar(pTx); err != nil {
		t.Fatalf("VerifyBlobSidecar: expected valid sidecar to pass, got: %v", err)
	}
}

func TestVerifyBlobSidecar_WrongVersionedHash(t *testing.T) {
	blob, commitment, proof, _ := makeValidBlobSidecar(t)

	pTx := &pb.Transaction{
		Type:                types.BlobTxType,
		BlobVersionedHashes: [][]byte{common.Hash{0xDE, 0xAD}.Bytes()}, // doesn't match commitment
		Sidecar: &pb.BlobSidecar{
			Blobs:       [][]byte{blob[:]},
			Commitments: [][]byte{commitment[:]},
			Proofs:      [][]byte{proof[:]},
		},
	}

	if err := VerifyBlobSidecar(pTx); err == nil {
		t.Fatalf("VerifyBlobSidecar: expected mismatched versioned hash to fail")
	}
}

func TestVerifyBlobSidecar_TamperedBlob(t *testing.T) {
	blob, commitment, proof, vh := makeValidBlobSidecar(t)
	tamperedBlob := blob
	tamperedBlob[0] ^= 0xFF // corrupt the blob content after commitment/proof were computed

	pTx := &pb.Transaction{
		Type:                types.BlobTxType,
		BlobVersionedHashes: [][]byte{vh.Bytes()},
		Sidecar: &pb.BlobSidecar{
			Blobs:       [][]byte{tamperedBlob[:]},
			Commitments: [][]byte{commitment[:]},
			Proofs:      [][]byte{proof[:]},
		},
	}

	if err := VerifyBlobSidecar(pTx); err == nil {
		t.Fatalf("VerifyBlobSidecar: expected tampered blob to fail proof verification")
	}
}

func TestVerifyBlobSidecar_MissingSidecar(t *testing.T) {
	pTx := &pb.Transaction{
		Type:                types.BlobTxType,
		BlobVersionedHashes: [][]byte{common.Hash{0x01}.Bytes()},
	}
	if err := VerifyBlobSidecar(pTx); err == nil {
		t.Fatalf("VerifyBlobSidecar: expected missing sidecar to fail")
	}
}

func TestVerifyBlobSidecar_NonBlobTypeIsNoop(t *testing.T) {
	pTx := &pb.Transaction{Type: types.DynamicFeeTxType}
	if err := VerifyBlobSidecar(pTx); err != nil {
		t.Fatalf("VerifyBlobSidecar: expected non-blob tx type to be a no-op, got: %v", err)
	}
}

// TestFromEthBlobTx_RejectsContractCreation guards the EIP-4844 rule that blob
// txs can never deploy a contract.
func TestFromEthBlobTx_RejectsContractCreation(t *testing.T) {
	key, _ := crypto.GenerateKey()
	blob, commitment, proof, vh := makeValidBlobSidecar(t)

	innerTx := &types.BlobTx{
		ChainID:    uint256.NewInt(1337),
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(1),
		Gas:        21000,
		To:         common.Address{}, // zero address: not allowed for blob txs
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: []common.Hash{vh},
		Sidecar: &types.BlobTxSidecar{
			Blobs:       []kzg4844.Blob{blob},
			Commitments: []kzg4844.Commitment{commitment},
			Proofs:      []kzg4844.Proof{proof},
		},
	}
	ethTx, err := types.SignNewTx(key, types.NewCancunSigner(big.NewInt(1337)), innerTx)
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}

	pTx := &pb.Transaction{}
	if err := FromEthBlobTx(ethTx, pTx); err == nil {
		t.Fatalf("FromEthBlobTx: expected zero-address (contract creation) blob tx to be rejected")
	}
}

// TestFromEthBlobTx_RejectsTooManyBlobs guards the per-tx blob-count cap
// (mt_common.MAX_BLOBS_PER_TX) added alongside the per-block budget in
// tx_processor.computeBlobBudgetRejections — a tx over the per-tx limit is
// unsatisfiable regardless of the per-block cap, so it's rejected here at
// admission rather than ever reaching block-building.
func TestFromEthBlobTx_RejectsTooManyBlobs(t *testing.T) {
	key, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x00000000000000000000000000000000001234")

	hashes := make([]common.Hash, mt_common.MAX_BLOBS_PER_TX+1)
	for i := range hashes {
		hashes[i] = common.Hash{0x01, byte(i)}
	}

	innerTx := &types.BlobTx{
		ChainID:    uint256.NewInt(1337),
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(1),
		Gas:        21000,
		To:         to,
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: hashes,
		// No Sidecar needed: FromEthBlobTx checks the blob count before any
		// sidecar/KZG verification.
	}
	ethTx, err := types.SignNewTx(key, types.NewCancunSigner(big.NewInt(1337)), innerTx)
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}

	pTx := &pb.Transaction{}
	if err := FromEthBlobTx(ethTx, pTx); err == nil {
		t.Fatalf("FromEthBlobTx: expected a tx with %d blobs (limit %d) to be rejected",
			len(hashes), mt_common.MAX_BLOBS_PER_TX)
	}
}

// TestFromEthBlobTx_RoundTrip exercises the full FromEthBlobTx -> NewTransactionFromEth
// -> ToEthBlobTx path with a real, KZG-valid signed blob tx.
func TestFromEthBlobTx_RoundTrip(t *testing.T) {
	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(*key.Public().(*ecdsa.PublicKey))
	to := common.HexToAddress("0x00000000000000000000000000000000001234")
	blob, commitment, proof, vh := makeValidBlobSidecar(t)

	innerTx := &types.BlobTx{
		ChainID:    uint256.NewInt(1337),
		Nonce:      0,
		GasTipCap:  uint256.NewInt(100),
		GasFeeCap:  uint256.NewInt(1000),
		Gas:        21000,
		To:         to,
		Value:      uint256.NewInt(5000),
		BlobFeeCap: uint256.NewInt(10),
		BlobHashes: []common.Hash{vh},
		Sidecar: &types.BlobTxSidecar{
			Blobs:       []kzg4844.Blob{blob},
			Commitments: []kzg4844.Commitment{commitment},
			Proofs:      []kzg4844.Proof{proof},
		},
	}
	ethTx, err := types.SignNewTx(key, types.NewCancunSigner(big.NewInt(1337)), innerTx)
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}

	txIface, err := NewTransactionFromEth(ethTx)
	if err != nil {
		t.Fatalf("NewTransactionFromEth: %v", err)
	}
	tx := txIface.(*Transaction)

	if tx.FromAddress() != from {
		t.Fatalf("FromAddress mismatch: got %s, want %s", tx.FromAddress().Hex(), from.Hex())
	}
	if tx.ToAddress() != to {
		t.Fatalf("ToAddress mismatch: got %s, want %s", tx.ToAddress().Hex(), to.Hex())
	}

	rebuilt := tx.ToEthTransaction()
	if rebuilt == nil {
		t.Fatalf("ToEthTransaction returned nil for a valid blob tx")
	}
	if rebuilt.Type() != types.BlobTxType {
		t.Fatalf("rebuilt tx type = %d, want BlobTxType", rebuilt.Type())
	}
	if len(rebuilt.BlobHashes()) != 1 || rebuilt.BlobHashes()[0] != vh {
		t.Fatalf("rebuilt BlobHashes mismatch: got %v, want [%s]", rebuilt.BlobHashes(), vh.Hex())
	}
	if rebuilt.BlobTxSidecar() != nil {
		t.Fatalf("rebuilt tx must never carry a sidecar (see ToEthBlobTx doc)")
	}
}

// TestFromEthBlobTx_CallDataRoundTrip guards against a real bug found while
// live-testing on a 3-node cluster: internally, calldata is stored as a
// marshaled CallData proto message (see prepareTransactionSpecificData), not
// raw EVM calldata — ToEthBlobTx must unwrap it the same way
// (t *Transaction).ToEthTransaction()'s IsCallContract()/CallData() branch
// does for the other tx types, or the reconstructed tx's Data (and therefore
// its hash and EVM execution) is wrong for any blob tx that calls a contract.
func TestFromEthBlobTx_CallDataRoundTrip(t *testing.T) {
	key, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x00000000000000000000000000000000005678")
	blob, commitment, proof, vh := makeValidBlobSidecar(t)
	calldata := []byte{0xAA, 0xBB, 0xCC, 0xDD}

	innerTx := &types.BlobTx{
		ChainID: uint256.NewInt(1337), Nonce: 0,
		GasTipCap: uint256.NewInt(100), GasFeeCap: uint256.NewInt(1000), Gas: 50000,
		To: to, Value: uint256.NewInt(0), Data: calldata,
		BlobFeeCap: uint256.NewInt(10),
		BlobHashes: []common.Hash{vh},
		Sidecar: &types.BlobTxSidecar{
			Blobs:       []kzg4844.Blob{blob},
			Commitments: []kzg4844.Commitment{commitment},
			Proofs:      []kzg4844.Proof{proof},
		},
	}
	ethTx, err := types.SignNewTx(key, types.NewCancunSigner(big.NewInt(1337)), innerTx)
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}
	originalHash := ethTx.Hash()

	txIface, err := NewTransactionFromEth(ethTx)
	if err != nil {
		t.Fatalf("NewTransactionFromEth: %v", err)
	}
	tx := txIface.(*Transaction)
	tx.proto.Sidecar = nil // exactly what the ingestion boundary does before propagation

	rebuilt := tx.ToEthTransaction()
	if rebuilt == nil {
		t.Fatalf("ToEthTransaction returned nil")
	}
	if !bytes.Equal(rebuilt.Data(), calldata) {
		t.Fatalf("calldata mismatch: got %x, want %x", rebuilt.Data(), calldata)
	}
	if got := tx.EthHash(); got != originalHash {
		t.Fatalf("EthHash after sidecar strip = %s, want %s (calldata unwrap bug)", got.Hex(), originalHash.Hex())
	}
}
