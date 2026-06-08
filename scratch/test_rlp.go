package main

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
)

func main() {
	fmt.Println("Testing RLP encoding with nil V, R, S...")

	// Direct RLP encoding of nil *big.Int
	var nilBigInt *big.Int = nil
	nilBigBytes, err := rlp.EncodeToBytes(nilBigInt)
	if err != nil {
		fmt.Printf("Direct nil *big.Int RLP encoding failed: %v\n", err)
	} else {
		fmt.Printf("Direct nil *big.Int RLP encoding success: %x\n", nilBigBytes)
	}

	// 1. DynamicFeeTx with nil V, R, S
	tx1 := &e_types.DynamicFeeTx{
		ChainID:   big.NewInt(991),
		Nonce:     0,
		GasTipCap: big.NewInt(100),
		GasFeeCap: big.NewInt(100),
		Gas:       21000,
		To:        &common.Address{1},
		Value:     big.NewInt(1000),
		V:         nil,
		R:         nil,
		S:         nil,
	}

	res1 := e_types.NewTx(tx1)
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("DynamicFeeTx.Hash() PANICKED: %v\n", r)
		}
	}()
	h1 := res1.Hash()
	fmt.Printf("DynamicFeeTx.Hash() success: %s\n", h1.Hex())

	b1, err := res1.MarshalBinary()
	if err != nil {
		fmt.Printf("DynamicFeeTx.MarshalBinary() failed: %v\n", err)
	} else {
		fmt.Printf("DynamicFeeTx.MarshalBinary() success, len: %d\n", len(b1))
	}

	j1, err := res1.MarshalJSON()
	if err != nil {
		fmt.Printf("DynamicFeeTx.MarshalJSON() failed: %v\n", err)
	} else {
		fmt.Printf("DynamicFeeTx.MarshalJSON() success: %s\n", string(j1))
	}

	// Test Sender call on DynamicFeeTx
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("DynamicFeeTx Sender PANICKED: %v\n", r)
			}
		}()
		signer := e_types.NewLondonSigner(res1.ChainId())
		from, err := e_types.Sender(signer, res1)
		if err != nil {
			fmt.Printf("DynamicFeeTx Sender returned error: %v\n", err)
		} else {
			fmt.Printf("DynamicFeeTx Sender: %s\n", from.Hex())
		}
	}()

	// 2. LegacyTx with nil V, R, S
	tx2 := &e_types.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(100),
		Gas:      21000,
		To:       &common.Address{1},
		Value:    big.NewInt(1000),
		V:        nil,
		R:        nil,
		S:        nil,
	}

	res2 := e_types.NewTx(tx2)
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("LegacyTx.Hash() PANICKED: %v\n", r)
		}
	}()
	h2 := res2.Hash()
	fmt.Printf("LegacyTx.Hash() success: %s\n", h2.Hex())

	b2, err := res2.MarshalBinary()
	if err != nil {
		fmt.Printf("LegacyTx.MarshalBinary() failed: %v\n", err)
	} else {
		fmt.Printf("LegacyTx.MarshalBinary() success, len: %d\n", len(b2))
	}

	j2, err := res2.MarshalJSON()
	if err != nil {
		fmt.Printf("LegacyTx.MarshalJSON() failed: %v\n", err)
	} else {
		fmt.Printf("LegacyTx.MarshalJSON() success: %s\n", string(j2))
	}

	// Test Sender call on LegacyTx
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("LegacyTx Sender PANICKED: %v\n", r)
			}
		}()
		signer := e_types.NewEIP155Signer(big.NewInt(991))
		from, err := e_types.Sender(signer, res2)
		if err != nil {
			fmt.Printf("LegacyTx Sender returned error: %v\n", err)
		} else {
			fmt.Printf("LegacyTx Sender: %s\n", from.Hex())
		}
	}()

	// 3. AccessListTx with nil V, R, S
	tx3 := &e_types.AccessListTx{
		ChainID:  big.NewInt(991),
		Nonce:    0,
		GasPrice: big.NewInt(100),
		Gas:      21000,
		To:       &common.Address{1},
		Value:    big.NewInt(1000),
		V:        nil,
		R:        nil,
		S:        nil,
	}

	res3 := e_types.NewTx(tx3)
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("AccessListTx.Hash() PANICKED: %v\n", r)
		}
	}()
	h3 := res3.Hash()
	fmt.Printf("AccessListTx.Hash() success: %s\n", h3.Hex())

	b3, err := res3.MarshalBinary()
	if err != nil {
		fmt.Printf("AccessListTx.MarshalBinary() failed: %v\n", err)
	} else {
		fmt.Printf("AccessListTx.MarshalBinary() success, len: %d\n", len(b3))
	}

	j3, err := res3.MarshalJSON()
	if err != nil {
		fmt.Printf("AccessListTx.MarshalJSON() failed: %v\n", err)
	} else {
		fmt.Printf("AccessListTx.MarshalJSON() success: %s\n", string(j3))
	}

	// Test Sender call on AccessListTx
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("AccessListTx Sender PANICKED: %v\n", r)
			}
		}()
		signer := e_types.NewEIP2930Signer(res3.ChainId())
		from, err := e_types.Sender(signer, res3)
		if err != nil {
			fmt.Printf("AccessListTx Sender returned error: %v\n", err)
		} else {
			fmt.Printf("AccessListTx Sender: %s\n", from.Hex())
		}
	}()
}
