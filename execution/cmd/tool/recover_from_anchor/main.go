package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

func main() {
	rootAnchorRPC := flag.String("root", "http://127.0.0.1:10746", "Root Anchor RPC URL")
	privateChainRPC := flag.String("private", "http://127.0.0.1:8545", "Private Chain RPC URL")
	chainID := flag.Uint64("chainid", 0, "Private Chain ID to recover")
	flag.Parse()

	if *chainID == 0 {
		log.Fatal("Please provide a valid -chainid")
	}

	rootClient, err := ethclient.Dial(*rootAnchorRPC)
	if err != nil {
		log.Fatalf("Failed to connect to root anchor: %v", err)
	}

	privRpcClient, err := rpc.Dial(*privateChainRPC)
	if err != nil {
		log.Fatalf("Failed to connect to private chain RPC: %v", err)
	}

	// Gateway contract address on Root Anchor
	gatewayAddress := common.HexToAddress("0x0000000000000000000000000000000000001002")

	// Event signature
	eventSignature := []byte("PrivateChainBatchStored(uint256,uint64,bytes32,bytes)")
	eventHash := crypto.Keccak256Hash(eventSignature)

	query := ethereum.FilterQuery{
		FromBlock: big.NewInt(0),
		ToBlock:   nil,
		Addresses: []common.Address{gatewayAddress},
		Topics: [][]common.Hash{
			{eventHash},
			{common.BigToHash(new(big.Int).SetUint64(*chainID))}, // Filter by chainID
		},
	}

	logs, err := rootClient.FilterLogs(context.Background(), query)
	if err != nil {
		log.Fatalf("Failed to filter logs: %v", err)
	}

	log.Printf("Found %d batch events for chain ID %d", len(logs), *chainID)

	bytesType, _ := abi.NewType("bytes", "", nil)
	args := abi.Arguments{
		{Type: bytesType, Name: "compressedTxs"},
	}

	recoveredTxs := 0

	for _, vLog := range logs {
		unpacked, err := args.Unpack(vLog.Data)
		if err != nil || len(unpacked) == 0 {
			log.Printf("Failed to unpack event data: %v", err)
			continue
		}

		compressedTxs, ok := unpacked[0].([]byte)
		if !ok {
			log.Printf("Invalid data format")
			continue
		}

		// Decompress
		br := bytes.NewReader(compressedTxs)
		zr, err := zlib.NewReader(br)
		if err != nil {
			log.Printf("Failed to create zlib reader: %v", err)
			continue
		}
		
		decompressed, err := io.ReadAll(zr)
		zr.Close()
		if err != nil {
			log.Printf("Failed to decompress txs: %v", err)
			continue
		}

		var rawTxs [][]byte
		if err := json.Unmarshal(decompressed, &rawTxs); err != nil {
			log.Printf("Failed to unmarshal txs: %v", err)
			continue
		}

		log.Printf("Block %d: Recovered %d transactions", vLog.BlockNumber, len(rawTxs))

		for _, rawTx := range rawTxs {
			var txHash common.Hash
			err := privRpcClient.CallContext(context.Background(), &txHash, "eth_sendRawTransaction", hexutil.Encode(rawTx))
			if err != nil {
				// We ignore "already known" or "nonce too low" if it's already there
				if !strings.Contains(err.Error(), "already known") {
					log.Printf("Failed to send raw tx: %v", err)
				}
			} else {
				recoveredTxs++
				log.Printf("✅ Replayed tx: %s (waiting 1.5s for block mining...)", txHash.Hex())
				time.Sleep(1500 * time.Millisecond)
			}
		}
	}

	log.Printf("Successfully submitted %d recovered transactions to private chain", recoveredTxs)
}
