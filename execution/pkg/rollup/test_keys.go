package main

import (
"context"
"fmt"
"github.com/ethereum/go-ethereum/core/types"
"github.com/ethereum/go-ethereum/ethclient"
"github.com/ethereum/go-ethereum/common"
"github.com/ethereum/go-ethereum/crypto"
"math/big"
"log"
)

func main() {
client, err := ethclient.Dial("http://127.0.0.1:10746")
if err != nil {
log.Fatal(err)
}
chainID, _ := client.ChainID(context.Background())
keys := []string{
"0x9f61a687fbeac9e11d5cfce0fe2dcec035cb2b21eb9c584d8cf90696ce2fc370",
"0x54e654bf0d419120418ae30ec27f947025f007c78b87cf09b6a81a1f201e6ccd",
"0x09a65b80a867a64d0b408d21412101a1c9c2a5cd9b94d5c3120a6d915c5efc73",
"0x98fa07a2c60f1ba1649593970c208745c9c831b29c6ed4e3e9950f2e63eab66d",
"0xd0d328c5d1b8ca9d3a25f2c1e7c66687a3f33d5ddb0f469eb6e43eb71e9308c8",
"0x91b6f94dc13b8e166a322a97668051e4a19f1ce02b2b10242c174a4f56541634",
}
to := common.HexToAddress("0x47Bc6bCBAeDC731Fd2832df68CEd61053aCC02B0")

for _, hexKey := range keys {
privateKey, _ := crypto.HexToECDSA(hexKey[2:])
from := crypto.PubkeyToAddress(privateKey.PublicKey)
nonce, _ := client.PendingNonceAt(context.Background(), from)

tx := types.NewTx(&types.LegacyTx{
Nonce:    nonce,
Gas:      21000,
GasPrice: big.NewInt(1000000000),
To:       &to,
Value:    big.NewInt(1),
})
signedTx, _ := types.SignTx(tx, types.LatestSignerForChainID(chainID), privateKey)
err := client.SendTransaction(context.Background(), signedTx)
if err == nil {
fmt.Printf("Account %s sent successfully!\n", from.Hex())
return
} else {
fmt.Printf("Account %s failed: %v\n", from.Hex(), err)
}
}
}
