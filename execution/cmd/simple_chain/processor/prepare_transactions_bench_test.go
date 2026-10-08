package processor

import (
	"bytes"
	"math/big"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
	"google.golang.org/protobuf/proto"
)

func generateBenchmarkBlock(numTxs int, dupRate float64, invalidBindingRate float64) *pb.ExecutableBlock {
	key, _ := crypto.GenerateKey()
	recipient := common.HexToAddress("0x1234567890123456789012345678901234567890")
	chainID := big.NewInt(991)
	signer := e_types.NewLondonSigner(chainID)

	txExes := make([]*pb.TransactionExe, 0, numTxs)
	validDigests := make([][]byte, 0, numTxs)

	for i := 0; i < numTxs; i++ {
		// Inject duplicate
		if i > 0 && len(validDigests) > 0 && float64(i%100)/100.0 < dupRate {
			dupIdx := (i * 3) % len(validDigests)
			txExes = append(txExes, &pb.TransactionExe{Digest: validDigests[dupIdx]})
			continue
		}

		ethTx := e_types.NewTx(&e_types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     uint64(i),
			GasTipCap: big.NewInt(1000000000),
			GasFeeCap: big.NewInt(2000000000),
			Gas:       21000,
			To:        &recipient,
			Value:     big.NewInt(int64(1000 + i)),
			Data:      nil,
		})

		signedTx, err := e_types.SignTx(ethTx, signer, key)
		if err != nil {
			panic(err)
		}

		pTx := &pb.Transaction{}
		if err := transaction.FromEthEIP1559Tx(signedTx, pTx); err != nil {
			panic(err)
		}
		rawEnv, err := signedTx.MarshalBinary()
		if err != nil {
			panic(err)
		}
		pTx.RawEnvelope = rawEnv

		// Inject invalid envelope binding
		if float64(i%100)/100.0 < invalidBindingRate {
			// Tamper proto field to mismatch envelope
			pTx.Amount = []byte{0xFF, 0xEE, 0xDD}
		}

		digest, err := proto.Marshal(pTx)
		if err != nil {
			panic(err)
		}

		validDigests = append(validDigests, digest)
		txExes = append(txExes, &pb.TransactionExe{Digest: digest})
	}

	return &pb.ExecutableBlock{
		GlobalExecIndex:   100,
		CommitIndex:       100,
		Epoch:             1,
		Transactions:      txExes,
		CommitTimestampMs: 1791450000000,
	}
}

// prepareTransactionsSequential is the baseline legacy implementation used for bit-for-bit verification
func prepareTransactionsSequential(epochData *pb.ExecutableBlock) []types.Transaction {
	if epochData == nil {
		return nil
	}
	rawTxs := ParallelUnmarshalTransactions(epochData.Transactions)
	seenTxs := make(map[common.Hash]bool, len(rawTxs))
	dedupedTxs := make([]types.Transaction, 0, len(rawTxs))

	for _, tx := range rawTxs {
		if tx == nil {
			continue
		}
		if len(tx.RawEnvelope()) > 0 {
			err := transaction.ValidateEnvelopeBinding(tx)
			if err != nil {
				continue
			}
		}
		hash := tx.Hash()
		if seenTxs[hash] {
			continue
		}
		seenTxs[hash] = true
		dedupedTxs = append(dedupedTxs, tx)
	}

	sort.Slice(dedupedTxs, func(i, j int) bool {
		hashI := dedupedTxs[i].Hash()
		hashJ := dedupedTxs[j].Hash()
		return bytes.Compare(hashI.Bytes(), hashJ.Bytes()) < 0
	})
	return dedupedTxs
}

// TestPrepareTransactions_PropertyIdentical verifies 100% bit-for-bit equality
// between parallel PrepareTransactions and the sequential legacy baseline.
func TestPrepareTransactions_PropertyIdentical(t *testing.T) {
	testCases := []struct {
		name        string
		numTxs      int
		dupRate     float64
		invalidRate float64
	}{
		{"EmptyBlock", 0, 0.0, 0.0},
		{"SingleTx", 1, 0.0, 0.0},
		{"SmallBlock_50", 50, 0.1, 0.05},
		{"Threshold_199", 199, 0.15, 0.05},
		{"ParallelThreshold_200", 200, 0.15, 0.05},
		{"MediumBlock_1000", 1000, 0.20, 0.10},
		{"LargeBlock_4000", 4000, 0.10, 0.05},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			block := generateBenchmarkBlock(tc.numTxs, tc.dupRate, tc.invalidRate)

			want := prepareTransactionsSequential(block)
			got := PrepareTransactions(block)

			if len(got) != len(want) {
				t.Fatalf("Length mismatch: got %d, want %d", len(got), len(want))
			}

			for i := 0; i < len(got); i++ {
				if got[i].Hash() != want[i].Hash() {
					t.Fatalf("Tx %d Hash mismatch: got %s, want %s", i, got[i].Hash().Hex(), want[i].Hash().Hex())
				}
				if !bytes.Equal(got[i].RawEnvelope(), want[i].RawEnvelope()) {
					t.Fatalf("Tx %d RawEnvelope mismatch", i)
				}
				if got[i].FromAddress() != want[i].FromAddress() {
					t.Fatalf("Tx %d FromAddress mismatch: got %s, want %s", i, got[i].FromAddress().Hex(), want[i].FromAddress().Hex())
				}
				if got[i].GetNonce() != want[i].GetNonce() {
					t.Fatalf("Tx %d GetNonce mismatch: got %d, want %d", i, got[i].GetNonce(), want[i].GetNonce())
				}
				if got[i].Amount().Cmp(want[i].Amount()) != 0 {
					t.Fatalf("Tx %d Amount mismatch: got %v, want %v", i, got[i].Amount(), want[i].Amount())
				}
			}
		})
	}
}

func BenchmarkPrepareTransactions_Sequential_4000(b *testing.B) {
	block := generateBenchmarkBlock(4000, 0.05, 0.01)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res := prepareTransactionsSequential(block)
		if len(res) == 0 {
			b.Fatal("prepareTransactionsSequential returned empty result")
		}
	}
}

func BenchmarkPrepareTransactions_Parallel_4000(b *testing.B) {
	block := generateBenchmarkBlock(4000, 0.05, 0.01)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res := PrepareTransactions(block)
		if len(res) == 0 {
			b.Fatal("PrepareTransactions returned empty result")
		}
	}
}

func BenchmarkPrepareTransactions_Sequential_8000(b *testing.B) {
	block := generateBenchmarkBlock(8000, 0.05, 0.01)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res := prepareTransactionsSequential(block)
		if len(res) == 0 {
			b.Fatal("prepareTransactionsSequential returned empty result")
		}
	}
}

func BenchmarkPrepareTransactions_Parallel_8000(b *testing.B) {
	block := generateBenchmarkBlock(8000, 0.05, 0.01)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res := PrepareTransactions(block)
		if len(res) == 0 {
			b.Fatal("PrepareTransactions returned empty result")
		}
	}
}
