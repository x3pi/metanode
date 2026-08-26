package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	eth_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

func main() {
	var (
		relayerKeyHex string
		rootAnchorRPC string
		chainsDir     string
		chainIDsFlag  string
	)

	flag.StringVar(&relayerKeyHex, "key", "0xd3ae7482f46f11cee2447bc711e9eb0fb79d4f2549781554cb962f54604e50f8", "Sender ECDSA private key hex")
	flag.StringVar(&rootAnchorRPC, "root-anchor", "http://127.0.0.1:9099", "Root Anchor JSON-RPC endpoint")
	flag.StringVar(&chainsDir, "chains-dir", "deploy/systemd/private_chains_data", "Directory containing private chain genesis files")
	flag.StringVar(&chainIDsFlag, "chains", "101,102,103,104", "Comma-separated list of chain IDs to register")
	flag.Parse()

	relayerKeyHex = strings.TrimPrefix(relayerKeyHex, "0x")
	privKey, err := crypto.HexToECDSA(relayerKeyHex)
	if err != nil {
		logger.Error("Invalid private key: %v", err)
		os.Exit(1)
	}

	fromAddress := crypto.PubkeyToAddress(privKey.PublicKey)
	logger.Info("Using submitter address: %s", fromAddress.Hex())

	ctx := context.Background()
	client, err := ethclient.Dial(rootAnchorRPC)
	if err != nil {
		logger.Error("Failed to connect to Root Anchor at %s: %v", rootAnchorRPC, err)
		os.Exit(1)
	}
	defer client.Close()

	chainID, err := client.ChainID(ctx)
	if err != nil {
		logger.Error("Failed to fetch Root Anchor ChainID: %v", err)
		os.Exit(1)
	}
	logger.Info("Connected to Root Anchor (ChainID: %s)", chainID.String())

	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	if err != nil {
		logger.Error("Failed to parse Gateway ABI: %v", err)
		os.Exit(1)
	}

	startNonce, err := client.PendingNonceAt(ctx, fromAddress)
	if err != nil {
		logger.Error("Failed to get initial nonce: %v", err)
		os.Exit(1)
	}
	currentNonce := startNonce

	chainIDs := strings.Split(chainIDsFlag, ",")
	for _, cidStr := range chainIDs {
		cidStr = strings.TrimSpace(cidStr)
		if cidStr == "" {
			continue
		}

		genesisPath := filepath.Join(chainsDir, fmt.Sprintf("chain_%s", cidStr), "genesis.json")
		genData, err := config.LoadGenesisData(genesisPath)
		if err != nil {
			logger.Error("Failed to load genesis for chain %s at %s: %v", cidStr, genesisPath, err)
			continue
		}

		var committee []cross_chain.ValidatorEntry
		for _, v := range genData.Validators {
			rawPub, _ := hex.DecodeString(strings.TrimPrefix(v.PubkeyBls, "0x"))
			if len(rawPub) == 0 && len(v.AuthorityKey) > 0 {
				rawPub = v.AuthorityKey
			}
			committee = append(committee, cross_chain.ValidatorEntry{
				PubkeyBLS: rawPub,
				Stake:     1000,
			})
		}

		var targetChainID uint64
		fmt.Sscanf(cidStr, "%d", &targetChainID)

		registry := cross_chain.ChainRegistry{
			ChainID:          targetChainID,
			Epoch:            0,
			Committee:        committee,
			QuorumThreshold:  6667,
			GatewayContract:  p_common.GATEWAY_CONTRACT_ADDRESS,
			StateRoot:        eth_common.Hash{},
			AccountTreeRoot:  eth_common.Hash{},
			ArchivalEndpoint: "",
			RegisteredAt:     0,
		}

		payloadBytes, err := json.Marshal(registry)
		if err != nil {
			logger.Error("Failed to marshal registry for chain %s: %v", cidStr, err)
			continue
		}

		now := uint64(time.Now().UnixMilli())
		proposeCalldata, err := parsedABI.Pack("propose", uint8(cross_chain.ProposalRegisterChain), payloadBytes, now)
		if err != nil {
			logger.Error("Failed to pack propose call for chain %s: %v", cidStr, err)
			continue
		}

		proposeFee := big.NewInt(100_000_000_000_000_000) // 0.1 MTN
		tx := types.NewTransaction(
			currentNonce,
			p_common.GATEWAY_CONTRACT_ADDRESS,
			proposeFee,
			2000000,
			big.NewInt(1000000000),
			proposeCalldata,
		)

		signer := types.LatestSignerForChainID(chainID)
		signedTx, err := types.SignTx(tx, signer, privKey)
		if err != nil {
			logger.Error("Failed to sign transaction for chain %s: %v", cidStr, err)
			continue
		}

		err = client.SendTransaction(ctx, signedTx)
		if err != nil {
			logger.Error("Failed to send transaction for chain %s: %v", cidStr, err)
			continue
		}

		logger.Info("✅ Chain %s registration proposed! Tx Hash: %s (nonce=%d)", cidStr, signedTx.Hash().Hex(), currentNonce)
		currentNonce++
		time.Sleep(500 * time.Millisecond)
	}
}

func mustSign(signer types.Signer, tx *types.Transaction, key *ecdsa.PrivateKey) *types.Transaction {
	signed, err := types.SignTx(tx, signer, key)
	if err != nil {
		panic(err)
	}
	return signed
}
