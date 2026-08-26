package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// register_chains bootstraps a fresh Root Anchor's ChainRegistry with a set of founding chains
// via bootstrapFoundingChains() -- the real, self-closing genesis path (PR #61), NOT the normal
// propose()/vote() governance flow this tool used before 2026-08-26. That was a real bug, not
// just a devnet convenience gap: propose()/vote() require the voter to already be an
// ActiveChains member, and a fresh Root Anchor's ChainRegistry starts empty -- there is no
// committee that could ever vote those per-chain proposals through, so the old code always sent
// N proposals that would sit pending forever (confirmed live: getChainRegistry still returned
// exists=false after running it against a real Root Anchor).
//
// Each founding chain's real committee entry is built from ITS OWN node's Databases.BLSPrivateKey
// (config.json) -- not the genesis validator's consensus authority_key/PubkeyBls, and not the
// gateway_bls_key devnet fallback gen_single_chain.py writes identically to every chain. This
// matters for more than correctness of THIS one transaction: CommitAttestationWorker and
// CommitteeAttestationWorker (the real background workers that later sign attestCommit/
// committeeUpdate quorum certs) sign with Databases.BLSPrivateKey specifically -- registering a
// different key here would mean their real signature shares never match the committee this
// registers, and every later attestCommit()/committeeUpdate() would silently never reach quorum.
func main() {
	var (
		submitterKeyHex string
		rootAnchorRPC   string
		chainsDir       string
		chainIDsFlag    string
		targetRPCsFlag  string
	)

	flag.StringVar(&submitterKeyHex, "key", "0xd3ae7482f46f11cee2447bc711e9eb0fb79d4f2549781554cb962f54604e50f8", "Sender ECDSA private key hex (must hold real gas balance on Root Anchor; the default is a public devnet-only key, see start_relayer_daemon.sh's own warning)")
	flag.StringVar(&rootAnchorRPC, "root-anchor", "http://127.0.0.1:9099", "Root Anchor JSON-RPC endpoint")
	flag.StringVar(&chainsDir, "chains-dir", "deploy/systemd/private_chains_data", "Directory containing private chain data (chain_<id>/node-0/config.json for each) -- pass an ABSOLUTE path unless running this from the repo root")
	flag.StringVar(&chainIDsFlag, "chains", "101,102,103,104", "Comma-separated list of founding chain IDs to register (>= MinFoundingChains)")
	flag.StringVar(&targetRPCsFlag, "target-rpcs", "", "Comma-separated chainID=rpcURL pairs (e.g. \"101=http://127.0.0.1:8546,102=http://127.0.0.1:8547\") -- ChainRegistry is PER-CHAIN local state, not shared: attestCommit() on chain 102 needs ITS OWN copy of chain 101's committee, not just Root Anchor's. Without this flag, only Root Anchor's registry is seeded and every attestCommit() from one private chain to another fails with \"unknown source chain ID\" (found + fixed 2026-08-26 via live E2E testing). Pass every founding chain's own RPC here in addition to -root-anchor.")
	flag.Parse()

	submitterKeyHex = strings.TrimPrefix(submitterKeyHex, "0x")
	privKey, err := crypto.HexToECDSA(submitterKeyHex)
	if err != nil {
		logger.Error("Invalid private key: %v", err)
		os.Exit(1)
	}
	fromAddress := crypto.PubkeyToAddress(privKey.PublicKey)
	logger.Info("Using submitter address: %s", fromAddress.Hex())

	ctx := context.Background()

	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	if err != nil {
		logger.Error("Failed to parse Gateway ABI: %v", err)
		os.Exit(1)
	}

	var payloads [][]byte
	for _, cidStr := range strings.Split(chainIDsFlag, ",") {
		cidStr = strings.TrimSpace(cidStr)
		if cidStr == "" {
			continue
		}
		var targetChainID uint64
		if _, err := fmt.Sscanf(cidStr, "%d", &targetChainID); err != nil {
			logger.Error("Invalid chain ID %q: %v", cidStr, err)
			os.Exit(1)
		}

		configPath := filepath.Join(chainsDir, fmt.Sprintf("chain_%s", cidStr), "node-0", "config.json")
		nodeCfg, err := config.LoadConfig(configPath)
		if err != nil {
			logger.Error("Failed to load node config for chain %s at %s: %v", cidStr, configPath, err)
			os.Exit(1)
		}
		if nodeCfg.Databases.BLSPrivateKey == "" {
			logger.Error("Chain %s's config at %s has no Databases.BLSPrivateKey configured", cidStr, configPath)
			os.Exit(1)
		}

		blsPriv, blsPub, _ := bls.GenerateKeyPairFromSecretKey(nodeCfg.Databases.BLSPrivateKey)
		popSig := cross_chain.PopSign(blsPriv, blsPub)

		registry := cross_chain.ChainRegistry{
			ChainID: targetChainID,
			Epoch:   0,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: blsPub.Bytes(), Stake: 1000, PopSignature: popSig.Bytes()},
			},
			QuorumThreshold: 6667,
			GatewayContract: p_common.GATEWAY_CONTRACT_ADDRESS,
		}
		payloadBytes, err := json.Marshal(registry)
		if err != nil {
			logger.Error("Failed to marshal registry for chain %s: %v", cidStr, err)
			os.Exit(1)
		}
		payloads = append(payloads, payloadBytes)
		logger.Info("Prepared founding entry for chain %s (real BLS pubkey from Databases.BLSPrivateKey, real PoP)", cidStr)
	}

	if len(payloads) == 0 {
		logger.Error("No chains to register (-chains was empty)")
		os.Exit(1)
	}

	calldata, err := parsedABI.Pack("bootstrapFoundingChains", payloads)
	if err != nil {
		logger.Error("Failed to pack bootstrapFoundingChains call: %v", err)
		os.Exit(1)
	}

	bootstrap(ctx, privKey, fromAddress, rootAnchorRPC, "Root Anchor", calldata, len(payloads), chainIDsFlag)

	// ChainRegistry is PER-CHAIN local state (see gateway.go's g.ChainRegistry map) -- Root
	// Anchor bootstrapping its own registry does NOT give any private chain knowledge of its
	// siblings' committees. Without this, attestCommit() on chain 102 for a commit produced by
	// chain 101 always reverted with "unknown source chain ID: chain 101", because chain 102's
	// OWN GatewayEngine.ChainRegistry had never heard of chain 101 at all. Found + fixed
	// 2026-08-26 via live E2E testing of the P4 relayer's WatchChainPair loop.
	if targetRPCsFlag != "" {
		for _, pair := range strings.Split(targetRPCsFlag, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) != 2 {
				logger.Error("Invalid -target-rpcs entry %q (expected chainID=url)", pair)
				os.Exit(1)
			}
			label, rpcURL := "chain "+parts[0], parts[1]
			bootstrap(ctx, privKey, fromAddress, rpcURL, label, calldata, len(payloads), chainIDsFlag)
		}
	}
}

// bootstrap submits the given (already-packed) bootstrapFoundingChains calldata to a single
// chain's RPC endpoint and blocks until it either confirms or the process exits on failure. It
// is deliberately fail-loud (os.Exit(1)) rather than fail-soft: a chain silently missing its
// siblings' committees would surface much later as a hard-to-diagnose "unknown source chain ID"
// revert during a real cross-chain attestation, not at registration time.
func bootstrap(ctx context.Context, privKey *ecdsa.PrivateKey, fromAddress common.Address, rpcURL, label string, calldata []byte, chainCount int, chainIDsFlag string) {
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		logger.Error("Failed to connect to %s at %s: %v", label, rpcURL, err)
		os.Exit(1)
	}
	defer client.Close()

	chainID, err := client.ChainID(ctx)
	if err != nil {
		logger.Error("Failed to fetch %s ChainID: %v", label, err)
		os.Exit(1)
	}
	logger.Info("Connected to %s (ChainID: %s)", label, chainID.String())

	nonce, err := client.PendingNonceAt(ctx, fromAddress)
	if err != nil {
		logger.Error("Failed to get nonce on %s: %v", label, err)
		os.Exit(1)
	}
	tx := types.NewTransaction(nonce, p_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), 5_000_000, big.NewInt(1_000_000_000), calldata)
	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), privKey)
	if err != nil {
		logger.Error("Failed to sign bootstrapFoundingChains transaction for %s: %v", label, err)
		os.Exit(1)
	}
	if err := client.SendTransaction(ctx, signedTx); err != nil {
		logger.Error("Failed to send bootstrapFoundingChains transaction to %s: %v", label, err)
		os.Exit(1)
	}
	logger.Info("bootstrapFoundingChains submitted to %s, tx=%s -- waiting for receipt...", label, signedTx.Hash().Hex())

	var receipt *types.Receipt
	for i := 0; i < 30; i++ {
		receipt, err = client.TransactionReceipt(ctx, signedTx.Hash())
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if receipt == nil {
		logger.Error("Timed out waiting for bootstrapFoundingChains receipt on %s (tx=%s): %v", label, signedTx.Hash().Hex(), err)
		os.Exit(1)
	}
	if receipt.Status != 1 {
		logger.Error("❌ bootstrapFoundingChains reverted on %s (tx=%s) -- ChainRegistry may already be bootstrapped, or a committee's PoP failed to verify", label, signedTx.Hash().Hex())
		os.Exit(1)
	}
	logger.Info("✅ bootstrapFoundingChains succeeded on %s! %d chain(s) registered: %s", label, chainCount, chainIDsFlag)
}
