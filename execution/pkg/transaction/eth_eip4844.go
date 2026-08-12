package transaction

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/holiman/uint256"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// ToEthBlobTx converts an internal Transaction into a go-ethereum EIP-4844 blob
// transaction. The returned tx never carries a Sidecar: blob content lives in
// execution/pkg/blob_store, keyed by versioned hash, and is pruned independently
// of the tx itself — see the comment on Transaction.Sidecar in transaction.proto.
// Hash/signer derivation never need the sidecar (it is excluded from the signing
// preimage by go-ethereum's own BlobTx RLP tagging), so this is always safe.
func ToEthBlobTx(tx *pb.Transaction) *types.Transaction {
	if tx.Type != types.BlobTxType {
		return nil
	}
	if tx.ChainID == 0 {
		return nil
	}
	if len(tx.ToAddress) == 0 {
		// Blob txs can never be contract creation (EIP-4844).
		return nil
	}
	if tx.GasFeeCap == nil || tx.GasTipCap == nil || tx.MaxFeePerBlobGas == nil {
		return nil
	}

	toAddress := common.BytesToAddress(tx.ToAddress)

	var accessList types.AccessList
	if len(tx.AccessList) > 0 {
		accessList = toEthAccessList(tx.AccessList)
	}

	blobHashes := make([]common.Hash, len(tx.BlobVersionedHashes))
	for i, h := range tx.BlobVersionedHashes {
		blobHashes[i] = common.BytesToHash(h)
	}

	var nonceUint64 uint64
	if len(tx.Nonce) > 0 {
		nonceUint64 = new(big.Int).SetBytes(tx.Nonce).Uint64()
	}

	// tx.Data is the marshaled internal CallData proto (see FromEthBlobTx via
	// prepareTransactionSpecificData), not raw EVM calldata — must unwrap it the
	// same way (t *Transaction) ToEthTransaction()'s IsCallContract()/CallData()
	// branch does for the other tx types. Blob txs can never be contract
	// creation (enforced above/in FromEthBlobTx), so IsCallContract() here
	// reduces to "Data is non-empty".
	data := tx.Data
	if len(data) > 0 {
		callData := &CallData{}
		if err := callData.Unmarshal(data); err == nil {
			data = callData.Input()
		}
	}

	innerTxData := &types.BlobTx{
		ChainID:    uint256.NewInt(tx.ChainID),
		Nonce:      nonceUint64,
		GasTipCap:  uint256.MustFromBig(new(big.Int).SetBytes(tx.GasTipCap)),
		GasFeeCap:  uint256.MustFromBig(new(big.Int).SetBytes(tx.GasFeeCap)),
		Gas:        tx.MaxGas,
		To:         toAddress,
		Value:      uint256.MustFromBig(new(big.Int).SetBytes(tx.Amount)),
		Data:       data,
		AccessList: accessList,
		BlobFeeCap: uint256.MustFromBig(new(big.Int).SetBytes(tx.MaxFeePerBlobGas)),
		BlobHashes: blobHashes,
	}

	sigV, sigR, sigS := extractSignature(tx)
	if sigR != nil && sigS != nil && sigV != nil {
		innerTxData.V = uint256.MustFromBig(sigV)
		innerTxData.R = uint256.MustFromBig(sigR)
		innerTxData.S = uint256.MustFromBig(sigS)
	}

	return types.NewTx(innerTxData)
}

// FromEthBlobTx converts a go-ethereum Blob transaction into the internal protobuf
// Transaction. It only does structural conversion plus the shape rules the EIP-4844
// spec fixes unconditionally (no contract creation, at least one blob hash) — KZG
// proof verification, persisting the sidecar to blob_store, and clearing pTx.Sidecar
// before the tx is propagated/stored are the ingestion boundary's job (see
// buildMetaTxFromEthTx in cmd/simple_chain), since they require I/O this package
// doesn't otherwise perform.
func FromEthBlobTx(ethTx *types.Transaction, pTx *pb.Transaction) error {
	if ethTx.Type() != types.BlobTxType {
		return errors.New("not an EIP-4844 transaction")
	}

	to := ethTx.To()
	if to == nil || *to == (common.Address{}) {
		return errors.New("EIP-4844 transaction cannot be a contract creation")
	}
	blobHashes := ethTx.BlobHashes()
	if len(blobHashes) == 0 {
		return errors.New("EIP-4844 transaction must carry at least one blob hash")
	}

	pTx.Type = types.BlobTxType
	pTx.ToAddress = to.Bytes()
	pTx.Amount = ethTx.Value().Bytes()
	pTx.MaxGas = ethTx.Gas()

	nonceValue := ethTx.Nonce()
	nonceBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(nonceBytes, nonceValue)
	pTx.Nonce = nonceBytes

	// Blob txs always have a real recipient (checked above), so this always takes
	// the CallData branch — the deploy-address argument is unreachable and unused.
	data, err := prepareTransactionSpecificData(ethTx, common.Address{})
	if err != nil {
		return err
	}
	pTx.Data = data

	txChainID := ethTx.ChainId()
	if txChainID == nil || txChainID.Sign() <= 0 {
		return errors.New("EIP-4844 transaction is missing ChainID")
	}
	pTx.ChainID = txChainID.Uint64()

	if ethTx.GasTipCap() != nil {
		pTx.GasTipCap = ethTx.GasTipCap().Bytes()
	}
	if ethTx.GasFeeCap() != nil {
		pTx.GasFeeCap = ethTx.GasFeeCap().Bytes()
	}
	if ethTx.BlobGasFeeCap() != nil {
		pTx.MaxFeePerBlobGas = ethTx.BlobGasFeeCap().Bytes()
	}
	pTx.MaxGasPrice = 0

	pTx.BlobVersionedHashes = make([][]byte, len(blobHashes))
	for i, h := range blobHashes {
		pTx.BlobVersionedHashes[i] = h.Bytes()
	}

	// Carry the sidecar through structurally for now; the ingestion boundary
	// verifies it against BlobVersionedHashes via KZG, persists it to blob_store,
	// and clears this field before the tx goes any further.
	if sidecar := ethTx.BlobTxSidecar(); sidecar != nil {
		pTx.Sidecar = &pb.BlobSidecar{
			Blobs:       blobsToBytes(sidecar.Blobs),
			Commitments: commitmentsToBytes(sidecar.Commitments),
			Proofs:      proofsToBytes(sidecar.Proofs),
		}
	}

	if len(ethTx.AccessList()) > 0 {
		pTx.AccessList = make([]*pb.AccessTuple, len(ethTx.AccessList()))
		for i, item := range ethTx.AccessList() {
			storageKeys := make([][]byte, len(item.StorageKeys))
			for j, key := range item.StorageKeys {
				storageKeys[j] = key.Bytes()
			}
			pTx.AccessList[i] = &pb.AccessTuple{
				Address:     item.Address.Bytes(),
				StorageKeys: storageKeys,
			}
		}
	} else {
		pTx.AccessList = nil
	}

	v, r, s := ethTx.RawSignatureValues()
	if v != nil && r != nil && s != nil {
		pTx.R = r.Bytes()
		pTx.S = s.Bytes()
		pTx.V = v.Bytes()

		signer := types.NewCancunSigner(txChainID)
		fromAddress, err := types.Sender(signer, ethTx)
		if err != nil {
			return fmt.Errorf("error deriving sender for EIP-4844 transaction: %w", err)
		}
		pTx.FromAddress = fromAddress.Bytes()

		var sig []byte
		sig = append(sig, r.Bytes()...)
		sig = append(sig, s.Bytes()...)
		if len(v.Bytes()) > 0 {
			sig = append(sig, v.Bytes()[len(v.Bytes())-1])
		} else {
			sig = append(sig, 0)
		}
		pTx.Sign = sig
	} else {
		pTx.R = nil
		pTx.S = nil
		pTx.V = nil
		pTx.Sign = nil
	}
	return nil
}

// VerifyBlobSidecar KZG-verifies every blob in sidecar against pTx's committed
// BlobVersionedHashes: the versioned hash must equal 0x01 || sha256(commitment)[1:]
// (EIP-4844's "version 1" hash), and the commitment/proof pair must actually attest
// to the blob's content. Called once at the ingestion boundary, before the sidecar
// is persisted to blob_store and stripped from the propagated transaction.
func VerifyBlobSidecar(pTx *pb.Transaction) error {
	if pTx.Type != types.BlobTxType {
		return nil
	}
	sidecar := pTx.Sidecar
	if sidecar == nil {
		return errors.New("EIP-4844 transaction is missing its blob sidecar")
	}
	n := len(pTx.BlobVersionedHashes)
	if n == 0 {
		return errors.New("EIP-4844 transaction has no blob versioned hashes")
	}
	if len(sidecar.Blobs) != n || len(sidecar.Commitments) != n || len(sidecar.Proofs) != n {
		return fmt.Errorf("blob sidecar shape mismatch: %d versioned hashes, %d blobs, %d commitments, %d proofs",
			n, len(sidecar.Blobs), len(sidecar.Commitments), len(sidecar.Proofs))
	}

	for i := 0; i < n; i++ {
		var blob kzg4844.Blob
		if len(sidecar.Blobs[i]) != len(blob) {
			return fmt.Errorf("blob %d: invalid length %d, want %d", i, len(sidecar.Blobs[i]), len(blob))
		}
		copy(blob[:], sidecar.Blobs[i])

		var commitment kzg4844.Commitment
		if len(sidecar.Commitments[i]) != len(commitment) {
			return fmt.Errorf("blob %d: invalid commitment length %d, want %d", i, len(sidecar.Commitments[i]), len(commitment))
		}
		copy(commitment[:], sidecar.Commitments[i])

		var proof kzg4844.Proof
		if len(sidecar.Proofs[i]) != len(proof) {
			return fmt.Errorf("blob %d: invalid proof length %d, want %d", i, len(sidecar.Proofs[i]), len(proof))
		}
		copy(proof[:], sidecar.Proofs[i])

		wantHash := common.BytesToHash(pTx.BlobVersionedHashes[i])
		gotHash := common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &commitment))
		if gotHash != wantHash {
			return fmt.Errorf("blob %d: versioned hash mismatch: commitment hashes to %#x, tx commits to %#x", i, gotHash, wantHash)
		}

		if err := kzg4844.VerifyBlobProof(&blob, commitment, proof); err != nil {
			return fmt.Errorf("blob %d: KZG proof verification failed: %w", i, err)
		}
	}
	return nil
}

func blobsToBytes(blobs []kzg4844.Blob) [][]byte {
	out := make([][]byte, len(blobs))
	for i := range blobs {
		b := make([]byte, len(blobs[i]))
		copy(b, blobs[i][:])
		out[i] = b
	}
	return out
}

func commitmentsToBytes(commitments []kzg4844.Commitment) [][]byte {
	out := make([][]byte, len(commitments))
	for i := range commitments {
		b := make([]byte, len(commitments[i]))
		copy(b, commitments[i][:])
		out[i] = b
	}
	return out
}

func proofsToBytes(proofs []kzg4844.Proof) [][]byte {
	out := make([][]byte, len(proofs))
	for i := range proofs {
		b := make([]byte, len(proofs[i]))
		copy(b, proofs[i][:])
		out[i] = b
	}
	return out
}
