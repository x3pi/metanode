package transaction

import (
	"math/big"
	"testing"

	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

// FuzzUnmarshalBinary tests that unmarshaling arbitrary bytes into an Ethereum transaction
// never panics or causes uncontrolled crashes.
func FuzzUnmarshalBinary(f *testing.F) {
	// Seed corpus
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0x01, 0xc0})
	f.Add([]byte{0x02, 0xc0})
	f.Add([]byte{0x7f})
	f.Add([]byte{0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, data []byte) {
		tx := new(e_types.Transaction)
		_ = tx.UnmarshalBinary(data)
	})
}

// FuzzValidateEthTxEnvelope tests that arbitrary byte inputs, even if successfully
// unmarshaled, can be safely validated by ValidateEthTxEnvelope without panics.
func FuzzValidateEthTxEnvelope(f *testing.F) {
	expectedChainID := big.NewInt(991)

	// Seed corpus
	f.Add([]byte{})
	f.Add([]byte{0x02, 0xc0})
	f.Add([]byte{0x01, 0x80})

	f.Fuzz(func(t *testing.T, data []byte) {
		tx := new(e_types.Transaction)
		if err := tx.UnmarshalBinary(data); err != nil {
			return
		}
		// If unmarshal succeeded, envelope validation must return an error or nil, never panic
		_ = ValidateEthTxEnvelope(tx, expectedChainID)
	})
}

// FuzzRLPBatchEnvelopes tests decoding and validating arbitrary RLP batch envelope payloads.
func FuzzRLPBatchEnvelopes(f *testing.F) {
	// Seed corpus
	f.Add([]byte{})
	f.Add([]byte{0xc0}) // Empty list
	f.Add([]byte{0xc1, 0xc0})
	f.Add([]byte{0xf8, 0x40})

	f.Fuzz(func(t *testing.T, data []byte) {
		var rawEnvelopes [][]byte
		if err := rlp.DecodeBytes(data, &rawEnvelopes); err != nil {
			return
		}
		if len(rawEnvelopes) > MaxBatchTxCount {
			return
		}
		for idx, env := range rawEnvelopes {
			if len(env) > MaxRawEthTxEnvelopeSize {
				continue
			}
			tx := new(e_types.Transaction)
			if err := tx.UnmarshalBinary(env); err == nil {
				_ = ValidateEthTxEnvelope(tx, big.NewInt(991))
			}
			if idx > 50 {
				// Prevent long execution per iteration during fuzz
				break
			}
		}
	})
}
