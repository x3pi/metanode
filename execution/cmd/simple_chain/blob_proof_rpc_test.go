package main

import (
	"crypto/sha256"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/holiman/uint256"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

// A blob tx whose KZG proof is corrupt must be rejected through the REAL ingress path
// (ConvertRawEthTxToMetaTx -> formatGethError), with the typed error and the geth-style RPC code/message.
func TestConvertRawEthTx_CorruptBlobProofRejected(t *testing.T) {
	var blob kzg4844.Blob
	commitment, err := kzg4844.BlobToCommitment(&blob)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := kzg4844.ComputeBlobProof(&blob, commitment)
	if err != nil {
		t.Fatal(err)
	}
	vh := common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &commitment))
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	build := func(p kzg4844.Proof) []byte {
		tx, err := types.SignTx(types.NewTx(&types.BlobTx{
			ChainID: uint256.NewInt(991), To: common.Address{19: 1},
			Gas: 21000, GasFeeCap: uint256.NewInt(20_000_000_000),
			GasTipCap: uint256.NewInt(1_000_000_000), Value: uint256.NewInt(0),
			BlobFeeCap: uint256.NewInt(1_000_000_000), BlobHashes: []common.Hash{vh},
			Sidecar: &types.BlobTxSidecar{Blobs: []kzg4844.Blob{blob}, Commitments: []kzg4844.Commitment{commitment}, Proofs: []kzg4844.Proof{p}},
		}), types.NewCancunSigner(big.NewInt(991)), key)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := tx.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	app := &App{config: &config.SimpleChainConfig{ChainId: big.NewInt(991)}}

	bad := proof
	bad[0] ^= 0xff
	_, _, convErr := app.ConvertRawEthTxToMetaTx(build(bad))
	if !errors.Is(convErr, mt_transaction.ErrInvalidBlobProof) {
		t.Fatalf("expected ErrInvalidBlobProof from the real converter, got %v", convErr)
	}
	rpcErr, ok := formatGethError(convErr).(*jsonrpcError)
	if !ok || rpcErr.ErrorCode() != -32000 || rpcErr.Error() != "KZG proof verification failed" {
		t.Fatalf("unexpected RPC error: %#v", formatGethError(convErr))
	}
}
