package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// register_chains bootstraps a fresh Root Anchor's ChainRegistry with a set of founding chains
// via registerChainViaStake() -- the vote-free, real-native-coin-gated registration path, one
// transaction per chain. This replaced bootstrapFoundingChains() (a single-transaction, >=
// MinFoundingChains BATCH call, retired 2026-08-28 -- see note/cross_chain_stake_and_value_flow.md)
// for the same reason bootstrapFoundingChains itself replaced the propose()/vote() governance flow
// before it: propose()/vote() require the voter to already be an ActiveChains member, and a fresh
// Root Anchor's ChainRegistry starts empty -- there is no committee that could ever vote those
// per-chain proposals through. registerChainViaStake() solves this identically for chain #1 and
// every chain after it, gated instead by a REAL, liquid native-coin deposit from -key's own
// wallet (config.CrossChain.MinNativeStakeToRegisterWei on the target chain) -- NOT any
// ERC-20-style token, and NOT the old PerChainAllocation/MinFoundingChains-count gate. -key's
// wallet is debited by that deposit amount ONCE PER CHAIN registered in this run (N chains needs
// N * MinNativeStakeToRegisterWei of real balance up front) -- fund it accordingly before running
// this tool against a real target.
//
// Each founding chain's real committee entry is built from ITS OWN node's Databases.BLSPrivateKey
// (config.json) -- not the genesis validator's consensus authority_key/PubkeyBls, and not the
// gateway_bls_key devnet fallback gen_single_chain.py writes identically to every chain. This
// matters for more than correctness of THIS one transaction: CommitAttestationWorker and
// CommitteeAttestationWorker (the real background workers that later sign attestCommit/
// committeeUpdate quorum certs) sign with Databases.BLSPrivateKey specifically -- registering a
// different key here would mean their real signature shares never match the committee this
// registers, and every later attestCommit()/committeeUpdate() would silently never reach quorum.

// committeeMember pairs a founding chain's ID with its own committee BLS private key hex --
// needed to cast a real, individually-signed governance vote() on that chain's behalf (see
// fundGenesis below). Mirrors live_asset_bridge's own committeeMember/proposeVoteExecute, which
// already live-verified this exact propose->vote->timelock->execute sequence
// (note/cross_chain_production_readiness_plan.md Phase 0.9).
type committeeMember struct {
	ChainID uint64
	PrivHex string
}

func main() {
	var (
		submitterKeyHex     string
		rootAnchorRPC       string
		chainsDir           string
		chainIDsFlag        string
		targetRPCsFlag      string
		fundGenesisFlag     bool
		genesisSupplyFlag   string
		perChainAllocFlag   string
		timelockWaitSeconds int
		rootAnchorKeysDir   string
	)

	flag.StringVar(&submitterKeyHex, "key", "0xd3ae7482f46f11cee2447bc711e9eb0fb79d4f2549781554cb962f54604e50f8", "Sender ECDSA private key hex (must hold real gas balance on Root Anchor; the default is a public devnet-only key, see start_relayer_daemon.sh's own warning)")
	flag.StringVar(&rootAnchorRPC, "root-anchor", "http://127.0.0.1:9099", "Root Anchor JSON-RPC endpoint")
	flag.StringVar(&chainsDir, "chains-dir", "deploy/systemd/private_chains_data", "Directory containing private chain data (chain_<id>/node-0/config.json for each) -- pass an ABSOLUTE path unless running this from the repo root")
	flag.StringVar(&rootAnchorKeysDir, "root-anchor-keys-dir", "", "Directory containing Root Anchor's node-*_keys (or config) to register Root Anchor's own committee into ChainRegistry on Root Anchor and all target RPCs")
	flag.StringVar(&chainIDsFlag, "chains", "101,102,103,104", "Comma-separated list of founding chain IDs to register (no minimum count -- registerChainViaStake works from chain #1 onward; -key's wallet needs real balance >= len(chains) * MinNativeStakeToRegisterWei)")
	flag.StringVar(&targetRPCsFlag, "target-rpcs", "", "Comma-separated chainID=rpcURL pairs (e.g. \"101=http://127.0.0.1:8546,102=http://127.0.0.1:8547\") -- ChainRegistry is PER-CHAIN local state, not shared: attestCommit() on chain 102 needs ITS OWN copy of chain 101's committee, not just Root Anchor's. Without this flag, only Root Anchor's registry is seeded and every attestCommit() from one private chain to another fails with \"unknown source chain ID\" (found + fixed 2026-08-26 via live E2E testing). Pass every founding chain's own RPC here in addition to -root-anchor.")
	flag.BoolVar(&fundGenesisFlag, "fund-genesis", false, "After bootstrapping, also mint the one-time genesis supply on Root Anchor (ProposalAllocateSupply, Reserve-only) and distribute it to each founding chain (ProposalTransferAllocation) -- see note/threat_matrix... PR #84 review. Off by default: bootstrapFoundingChains alone never touches SupplyLedger (by design, C7 fix), so this is opt-in.")
	flag.StringVar(&genesisSupplyFlag, "genesis-supply", "", "Total one-time genesis supply to mint on Root Anchor, base-10 wei string (required if -fund-genesis; e.g. a devnet default like \"400000000000000000000000000\" for 400,000,000 tokens at 18 decimals -- this is an operational/ceremony decision, not a protocol constant, hence a flag rather than a hardcoded literal)")
	flag.StringVar(&perChainAllocFlag, "per-chain-allocation", "", "Amount transferred from Root Anchor's Reserve to EACH founding chain in -chains, base-10 wei string (required if -fund-genesis). Must satisfy len(chains)*per-chain-allocation <= genesis-supply.")
	flag.IntVar(&timelockWaitSeconds, "timelock-wait", 12, "Seconds to sleep after voting, before executeProposal -- must exceed the target chain's cross_chain.devnet_governance_timelock_seconds_override (gen_root_anchor_chain.py's default is 10s). NEVER use this against a real production Root Anchor with the real 72h timelock -- -fund-genesis is a devnet/ceremony-rehearsal convenience, not a production automation path.")
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
	var committee []committeeMember
	var chainIDs []uint64

	// 1. Discover and register Root Anchor's own committee (Reserve Chain)
	rootClient, err := ethclient.Dial(rootAnchorRPC)
	if err == nil {
		if onChainID, err := rootClient.ChainID(ctx); err == nil {
			reserveChainID := onChainID.Uint64()
			rootEntries, rootMembers, err := discoverRootAnchorCommittee(rootAnchorKeysDir, chainsDir, reserveChainID)
			if err == nil && len(rootEntries) > 0 {
				rootRegistry := cross_chain.ChainRegistry{
					ChainID:         reserveChainID,
					Epoch:           0,
					Committee:       rootEntries,
					QuorumThreshold: 6667,
					GatewayContract: p_common.GATEWAY_CONTRACT_ADDRESS,
				}
				rootPayloadBytes, err := json.Marshal(rootRegistry)
				if err == nil {
					payloads = append(payloads, rootPayloadBytes)
					committee = append(committee, rootMembers...)
					chainIDs = append(chainIDs, reserveChainID)
					logger.Info("Prepared founding entry for Root Anchor (chain %d) with %d validator(s) (real BLS pubkey, real PoP)", reserveChainID, len(rootEntries))
				}
			} else {
				logger.Warn("Could not discover Root Anchor node keys (%v) -- skipping Root Anchor self-registration", err)
			}
		}
		rootClient.Close()
	}

	// 2. Discover and register all private founding chains
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
		// Deliberately NOT config.LoadConfig(): it caches into the package-level ConfigApp
		// singleton behind a sync.Once, so calling it more than once per process (exactly what
		// this loop does, once per founding chain) silently returns the FIRST chain's config for
		// every subsequent chain ID, ignoring configPath entirely. Confirmed live: every chain
		// registered after the first ended up with chain 101's BLSPrivateKey/committee entry,
		// so only chain 101 could ever pass its own committee-membership check anywhere in the
		// mesh. Found + fixed 2026-08-26 via live governance-vote testing. Read+unmarshal each
		// file directly into its own struct instead.
		nodeCfg, err := loadConfigFresh(configPath)
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
		committee = append(committee, committeeMember{ChainID: targetChainID, PrivHex: nodeCfg.Databases.BLSPrivateKey})
		chainIDs = append(chainIDs, targetChainID)
		logger.Info("Prepared founding entry for chain %s (real BLS pubkey from Databases.BLSPrivateKey, real PoP)", cidStr)
	}

	if len(payloads) == 0 {
		logger.Error("No chains to register (-chains was empty)")
		os.Exit(1)
	}

	var registerCalldatas [][]byte
	for i, payloadBytes := range payloads {
		calldata, err := parsedABI.Pack("registerChainViaStake", payloadBytes)
		if err != nil {
			logger.Error("Failed to pack registerChainViaStake call for chain %d: %v", chainIDs[i], err)
			os.Exit(1)
		}
		registerCalldatas = append(registerCalldatas, calldata)
	}

	registerChains(ctx, privKey, fromAddress, rootAnchorRPC, "Root Anchor", chainIDs, registerCalldatas)

	if fundGenesisFlag {
		fundGenesis(ctx, privKey, fromAddress, rootAnchorRPC, committee, chainIDs, genesisSupplyFlag, perChainAllocFlag, timelockWaitSeconds)
	}

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
			registerChains(ctx, privKey, fromAddress, rpcURL, label, chainIDs, registerCalldatas)
		}
	}
}

// parseInventoryActiveNodeIDs searches for ansible/inventory.yml to determine which Root Anchor
// validator nodes are currently active in the cluster, preventing stale/unused key directories from
// inflating the total committee size and blocking QuorumCert accumulation.
func parseInventoryActiveNodeIDs(deployDir string) map[int]bool {
	active := make(map[int]bool)
	inventoryPaths := []string{
		filepath.Join(deployDir, "inventory.yml"),
		filepath.Join(deployDir, "../ansible/inventory.yml"),
		filepath.Join(deployDir, "ansible/inventory.yml"),
		"deploy/ansible/inventory.yml",
		"../ansible/inventory.yml",
	}
	re := regexp.MustCompile(`node_ids:\s*\[([^\]]+)\]`)
	for _, p := range inventoryPaths {
		data, err := os.ReadFile(p)
		if err == nil {
			matches := re.FindAllStringSubmatch(string(data), -1)
			for _, m := range matches {
				if len(m) >= 2 {
					for _, part := range strings.Split(m[1], ",") {
						part = strings.TrimSpace(part)
						if id, err := strconv.Atoi(part); err == nil {
							active[id] = true
						}
					}
				}
			}
			if len(active) > 0 {
				break
			}
		}
	}
	return active
}

// discoverRootAnchorCommittee inspects directories to discover Root Anchor's validator BLS keys
// and construct its ValidatorEntry list with real BLS public keys and real PoP signatures.
func discoverRootAnchorCommittee(keysDir, chainsDir string, reserveChainID uint64) ([]cross_chain.ValidatorEntry, []committeeMember, error) {
	var entries []cross_chain.ValidatorEntry
	var members []committeeMember

	activeNodeIDs := parseInventoryActiveNodeIDs(keysDir)

	dirsToTry := []string{
		keysDir,
		"deploy/systemd",
		"../systemd",
		"../../systemd",
		filepath.Join(chainsDir, "..", "..", "systemd"),
		filepath.Join(chainsDir, "..", "systemd"),
	}

	seenKeys := make(map[string]bool)

	for _, d := range dirsToTry {
		if d == "" {
			continue
		}
		absD, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		entriesFound, err := os.ReadDir(absD)
		if err != nil {
			continue
		}
		for _, entry := range entriesFound {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if strings.HasPrefix(name, "node-") {
				if len(activeNodeIDs) > 0 {
					var nodeIdx int
					if _, err := fmt.Sscanf(name, "node-%d", &nodeIdx); err == nil {
						if !activeNodeIDs[nodeIdx] {
							continue
						}
					}
				}
				for _, cfgName := range []string{"execution.json", "config.json"} {
					cfgPath := filepath.Join(absD, name, cfgName)
					if _, err := os.Stat(cfgPath); err == nil {
						cfg, err := loadConfigFresh(cfgPath)
						if err == nil && cfg.Databases.BLSPrivateKey != "" {
							blsKeyHex := strings.TrimPrefix(cfg.Databases.BLSPrivateKey, "0x")
							if !seenKeys[blsKeyHex] {
								seenKeys[blsKeyHex] = true
								blsPriv, blsPub, _ := bls.GenerateKeyPairFromSecretKey(blsKeyHex)
								popSig := cross_chain.PopSign(blsPriv, blsPub)
								entries = append(entries, cross_chain.ValidatorEntry{
									PubkeyBLS:    blsPub.Bytes(),
									Stake:        1000,
									PopSignature: popSig.Bytes(),
								})
								members = append(members, committeeMember{
									ChainID: reserveChainID,
									PrivHex: blsKeyHex,
								})
							}
						}
					}
				}
			}
		}
		if len(entries) > 0 {
			break
		}
	}

	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("no Root Anchor node keys found in inspected directories")
	}
	return entries, members, nil
}

// loadConfigFresh reads and unmarshals a node config.json directly, bypassing
// config.LoadConfig's process-wide sync.Once cache -- see the call site's comment for why that
// cache makes config.LoadConfig unsafe to call more than once per process.
func loadConfigFresh(configPath string) (*config.SimpleChainConfig, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}
	cfg := &config.SimpleChainConfig{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}
	return cfg, nil
}

func extractRevertReason(rpcURL string, txHash common.Hash) string {
	rpcClient, err := rpc.Dial(rpcURL)
	if err != nil {
		return ""
	}
	defer rpcClient.Close()

	var rawReceipt map[string]interface{}
	if err := rpcClient.Call(&rawReceipt, "eth_getTransactionReceipt", txHash.Hex()); err != nil {
		return ""
	}
	returnHex, ok := rawReceipt["return"].(string)
	if !ok || len(returnHex) < 10 {
		return ""
	}
	returnBytes, err := hex.DecodeString(strings.TrimPrefix(returnHex, "0x"))
	if err != nil || len(returnBytes) < 4 {
		return ""
	}
	// ABI Error(string) selector: 0x08c379a0
	if bytes.Equal(returnBytes[:4], []byte{0x08, 0xc3, 0x79, 0xa0}) && len(returnBytes) >= 68 {
		strLen := binary.BigEndian.Uint64(returnBytes[60:68])
		if uint64(len(returnBytes)) >= 68+strLen {
			return string(returnBytes[68 : 68+strLen])
		}
	}
	return string(returnBytes)
}

// registerChains submits one registerChainViaStake calldata per chain (registerCalldatas[i] for
// chainIDs[i]) to a single chain's RPC endpoint, sequentially, blocking on each one's receipt
// before sending the next -- so an early chain's failure never leaves later chains' registration
// state ambiguous, and so -key's wallet nonce advances correctly between calls. It is deliberately
// fail-loud (os.Exit(1)) rather than fail-soft on any UNEXPECTED failure: a chain silently missing
// its siblings' committees would surface much later as a hard-to-diagnose "unknown source chain
// ID" revert during a real cross-chain attestation, not at registration time. "already registered"
// is tolerated per-chain (this run may be a safe re-run over a partially-registered set), matching
// the retired bootstrapFoundingChains's own self-closing tolerance.
func registerChains(ctx context.Context, privKey *ecdsa.PrivateKey, fromAddress common.Address, rpcURL, label string, chainIDs []uint64, registerCalldatas [][]byte) {
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		logger.Error("Failed to connect to %s at %s: %v", label, rpcURL, err)
		os.Exit(1)
	}
	defer client.Close()

	onChainID, err := client.ChainID(ctx)
	if err != nil {
		logger.Error("Failed to fetch %s ChainID: %v", label, err)
		os.Exit(1)
	}
	logger.Info("Connected to %s (ChainID: %s)", label, onChainID.String())

	for i, calldata := range registerCalldatas {
		targetChainID := chainIDs[i]

		nonce, err := client.PendingNonceAt(ctx, fromAddress)
		if err != nil {
			logger.Error("Failed to get nonce on %s: %v", label, err)
			os.Exit(1)
		}
		tx := types.NewTransaction(nonce, p_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), 5_000_000, big.NewInt(1_000_000_000), calldata)
		signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(onChainID), privKey)
		if err != nil {
			logger.Error("Failed to sign registerChainViaStake transaction (chain %d) for %s: %v", targetChainID, label, err)
			os.Exit(1)
		}
		if err := client.SendTransaction(ctx, signedTx); err != nil {
			logger.Error("Failed to send registerChainViaStake transaction (chain %d) to %s: %v", targetChainID, label, err)
			os.Exit(1)
		}
		logger.Info("registerChainViaStake(chain %d) submitted to %s, tx=%s -- waiting for receipt...", targetChainID, label, signedTx.Hash().Hex())

		var receipt *types.Receipt
		for j := 0; j < 30; j++ {
			receipt, err = client.TransactionReceipt(ctx, signedTx.Hash())
			if err == nil {
				break
			}
			time.Sleep(1 * time.Second)
		}
		if receipt == nil {
			logger.Error("Timed out waiting for registerChainViaStake receipt (chain %d) on %s (tx=%s): %v", targetChainID, label, signedTx.Hash().Hex(), err)
			os.Exit(1)
		}
		if receipt.Status != 1 {
			revertMsg := extractRevertReason(rpcURL, signedTx.Hash())
			if strings.Contains(revertMsg, "already in ChainRegistry") {
				logger.Info("ℹ️ %s: chain %d is already registered -- proceeding.", label, targetChainID)
				continue
			}
			logger.Error("❌ registerChainViaStake(chain %d) reverted on %s (tx=%s)", targetChainID, label, signedTx.Hash().Hex())
			if revertMsg != "" {
				logger.Error("   👉 Error ReturnData từ Receipt: \"%s\"", revertMsg)
				if strings.Contains(revertMsg, "min_native_stake_to_register_wei") {
					logger.Error("   👉 %s has no MinNativeStakeToRegisterWei configured -- registerChainViaStake fails closed until an operator sets it.", label)
				} else if strings.Contains(revertMsg, "insufficient balance") {
					logger.Error("   👉 -key's wallet (%s) does not hold enough real native balance on %s -- fund it, or reduce -chains.", fromAddress.Hex(), label)
				}
			} else {
				logger.Error("   👉 Error ReturnData: (không có hoặc mã lỗi rỗng)")
			}
			os.Exit(1)
		}
		logger.Info("✅ registerChainViaStake(chain %d) succeeded on %s!", targetChainID, label)
	}
}

// fundGenesis mints the one-time genesis supply on Root Anchor and distributes it to every
// founding chain, using the real propose->vote->timelock->execute governance flow -- NOT a
// direct SupplyLedger.GrantAllocation() call from inside BootstrapFoundingChains, which is
// exactly the bug this replaces (an earlier version of gateway.go auto-minted a hardcoded
// allocation to every founding chain from inside bootstrap itself, completely bypassing the C7
// fix's Reserve-only/one-time-mint gate -- see PR #84's review comment and
// note/cross_chain_attack_scenario_catalog.md item C7).
//
// Scope: this ONLY ever needs to run against Root Anchor's own RPC, not every founding chain's.
// GlobalSupplyLedger is per-chain-local state, but the C8 fix restricts any ceiling-enforced
// (nonzero-value) attestCommit() to the chain whose OWN LocalChainID equals its OWN configured
// ReserveChainID -- so as long as every founding chain's config points reserve_chain_id at Root
// Anchor's real chain ID (see gen_root_anchor_chain.py/gen_single_chain.py and PR #84's
// deploy.yml fix), only Root Anchor's own GatewayEngine will ever pass that check. A private
// chain's own local SupplyLedger copy is therefore never consulted for the ceiling check, and
// does not need its own mint/transfer sequence run against it.
//
// reserveChainID is NOT taken from a flag -- it's read directly off Root Anchor's own RPC via
// eth_chainId, so this can never target the wrong chain by a typo'd/stale config value.
func fundGenesis(ctx context.Context, privKey *ecdsa.PrivateKey, fromAddress common.Address, rootAnchorRPC string, committee []committeeMember, chainIDs []uint64, genesisSupplyStr, perChainAllocStr string, timelockWaitSeconds int) {
	if genesisSupplyStr == "" || perChainAllocStr == "" {
		logger.Error("-fund-genesis requires both -genesis-supply and -per-chain-allocation")
		os.Exit(1)
	}
	genesisSupply, ok := new(big.Int).SetString(genesisSupplyStr, 10)
	if !ok || genesisSupply.Sign() <= 0 {
		logger.Error("Invalid -genesis-supply %q (must be a positive base-10 integer)", genesisSupplyStr)
		os.Exit(1)
	}
	perChainAlloc, ok := new(big.Int).SetString(perChainAllocStr, 10)
	if !ok || perChainAlloc.Sign() <= 0 {
		logger.Error("Invalid -per-chain-allocation %q (must be a positive base-10 integer)", perChainAllocStr)
		os.Exit(1)
	}

	client, err := ethclient.Dial(rootAnchorRPC)
	if err != nil {
		logger.Error("fundGenesis: failed to connect to Root Anchor at %s: %v", rootAnchorRPC, err)
		os.Exit(1)
	}
	defer client.Close()
	onChainID, err := client.ChainID(ctx)
	if err != nil {
		logger.Error("fundGenesis: failed to fetch Root Anchor's real ChainID: %v", err)
		os.Exit(1)
	}
	reserveChainID := onChainID.Uint64()
	logger.Info("fundGenesis: Root Anchor's real chain ID (used as ReserveChainID) is %d", reserveChainID)

	foundingChainsCount := 0
	for _, cid := range chainIDs {
		if cid != reserveChainID {
			foundingChainsCount++
		}
	}
	totalDistributed := new(big.Int).Mul(perChainAlloc, big.NewInt(int64(foundingChainsCount)))
	if totalDistributed.Cmp(genesisSupply) > 0 {
		logger.Error("len(founding_chains)=%d * -per-chain-allocation=%s = %s exceeds -genesis-supply=%s",
			foundingChainsCount, perChainAlloc.String(), totalDistributed.String(), genesisSupply.String())
		os.Exit(1)
	}

	grant := cross_chain.AllocationGrantPayload{ChainID: reserveChainID, Amount: genesisSupply}
	grantPayload, err := json.Marshal(grant)
	if err != nil {
		logger.Error("fundGenesis: marshal AllocationGrantPayload: %v", err)
		os.Exit(1)
	}
	mintErr := proposeVoteExecute(ctx, client, privKey, fromAddress, rootAnchorRPC,
		5 /* ProposalAllocateSupply */, grantPayload, committee, timelockWaitSeconds,
		"mint genesis supply")
	if mintErr != nil {
		if strings.Contains(mintErr.Error(), "already been minted") {
			logger.Info("ℹ️ fundGenesis: genesis supply already minted on Root Anchor -- proceeding to distribute.")
		} else {
			logger.Error("❌ fundGenesis: genesis mint failed: %v", mintErr)
			os.Exit(1)
		}
	} else {
		logger.Info("✅ fundGenesis: minted %s to Reserve (chain %d) on Root Anchor", genesisSupply.String(), reserveChainID)
	}

	for _, cid := range chainIDs {
		if cid == reserveChainID {
			continue // Skip Reserve itself -- only distribute to private chains
		}
		transfer := cross_chain.AllocationTransferPayload{FromChainID: reserveChainID, ToChainID: cid, Amount: perChainAlloc}
		transferPayload, err := json.Marshal(transfer)
		if err != nil {
			logger.Error("fundGenesis: marshal AllocationTransferPayload for chain %d: %v", cid, err)
			os.Exit(1)
		}
		if err := proposeVoteExecute(ctx, client, privKey, fromAddress, rootAnchorRPC,
			6 /* ProposalTransferAllocation */, transferPayload, committee, timelockWaitSeconds,
			fmt.Sprintf("transfer allocation to chain %d", cid)); err != nil {
			logger.Error("❌ fundGenesis: allocation transfer to chain %d failed: %v", cid, err)
			os.Exit(1)
		}
		logger.Info("✅ fundGenesis: transferred %s from Reserve (chain %d) to chain %d", perChainAlloc.String(), reserveChainID, cid)
	}
}

// proposeVoteExecute submits propose(), then a real BLS-signed vote() from every committee
// member, then (after the devnet timelock override) executeProposal() -- the same real,
// live-verified sequence as live_asset_bridge's own proposeVoteExecute
// (note/cross_chain_production_readiness_plan.md Phase 0.9), adapted to reuse this tool's own
// sendTxAndWait/extractRevertReason helpers instead of duplicating a second transaction-sending
// implementation.
func proposeVoteExecute(ctx context.Context, client *ethclient.Client, privKey *ecdsa.PrivateKey, fromAddress common.Address, rpcURL string, kind uint8, payload []byte, committee []committeeMember, timelockWaitSeconds int, label string) error {
	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	if err != nil {
		return fmt.Errorf("parse Gateway ABI: %w", err)
	}

	now := uint64(time.Now().Unix())
	proposeCalldata, err := parsedABI.Pack("propose", kind, payload, now)
	if err != nil {
		return fmt.Errorf("pack propose(kind=%d): %w", kind, err)
	}
	// Anti-spam fee enforced by gateway_handler.go's "propose" case (0.1 native token) -- see
	// its own comment for why this exists.
	proposeFee := new(big.Int).Mul(big.NewInt(1_000_000_000), big.NewInt(100_000_000))
	proposeReceipt, err := sendTxAndWait(ctx, client, privKey, fromAddress, rpcURL, proposeFee, 2_000_000, proposeCalldata,
		fmt.Sprintf("%s: propose", label))
	if err != nil {
		return err
	}

	var proposalID common.Hash
	returnHex := extractRawReturnHex(rpcURL, proposeReceipt.TxHash)
	if returnBytes, err := hex.DecodeString(strings.TrimPrefix(returnHex, "0x")); err == nil && len(returnBytes) >= 32 {
		proposalID = common.BytesToHash(returnBytes[len(returnBytes)-32:])
	} else {
		propTs := now
		if block, err := client.BlockByNumber(ctx, proposeReceipt.BlockNumber); err == nil && block != nil {
			propTs = block.Time()
		}
		var buf []byte
		buf = append(buf, kind)
		var tsBytes [8]byte
		binary.BigEndian.PutUint64(tsBytes[:], propTs)
		buf = append(buf, tsBytes[:]...)
		buf = append(buf, payload...)
		proposalID = crypto.Keccak256Hash(buf)
	}
	logger.Info("%s: on-chain proposalID=%s", label, proposalID.Hex())

	voteNow := uint64(time.Now().Unix())
	for _, m := range committee {
		kp := bls.NewKeyPair(common.FromHex(m.PrivHex))
		voteMsg := cross_chain.ComputeGovernanceVoteMessage(proposalID, m.ChainID)
		sig := bls.Sign(kp.PrivateKey(), voteMsg)
		voteCalldata, err := parsedABI.Pack("vote", proposalID, new(big.Int).SetUint64(m.ChainID), voteNow, kp.BytesPublicKey(), sig.Bytes())
		if err != nil {
			return fmt.Errorf("pack vote(chain=%d): %w", m.ChainID, err)
		}
		// Soft: tolerate "already voted"/"already timelocked"/"quorum already reached" reverts
		// past the real threshold as expected, not fatal -- mirrors live_asset_bridge's own
		// sendCalldataSoft rationale (quorum is computed off the CURRENT ActiveChains size, not
		// a size this tool tracks externally).
		if _, err := sendTxAndWait(ctx, client, privKey, fromAddress, rpcURL, nil, 1_000_000, voteCalldata,
			fmt.Sprintf("%s: vote(chain=%d)", label, m.ChainID)); err != nil {
			logger.Info("ℹ️ %s: vote(chain=%d) did not succeed (likely already past quorum/timelock): %v", label, m.ChainID, err)
		}
	}

	logger.Info("%s: waiting %ds for devnet timelock before executeProposal...", label, timelockWaitSeconds)
	time.Sleep(time.Duration(timelockWaitSeconds) * time.Second)
	execNow := uint64(time.Now().Unix())
	execCalldata, err := parsedABI.Pack("executeProposal", proposalID, execNow)
	if err != nil {
		return fmt.Errorf("pack executeProposal(kind=%d): %w", kind, err)
	}
	_, err = sendTxAndWait(ctx, client, privKey, fromAddress, rpcURL, nil, 1_000_000, execCalldata,
		fmt.Sprintf("%s: executeProposal", label))
	return err
}

func extractRawReturnHex(rpcURL string, txHash common.Hash) string {
	rpcClient, err := rpc.Dial(rpcURL)
	if err != nil {
		return ""
	}
	defer rpcClient.Close()

	var rawReceipt map[string]interface{}
	if err := rpcClient.Call(&rawReceipt, "eth_getTransactionReceipt", txHash.Hex()); err != nil {
		return ""
	}
	returnHex, ok := rawReceipt["return"].(string)
	if !ok {
		return ""
	}
	return returnHex
}

// sendTxAndWait signs, sends, and waits for a single transaction's receipt, returning an error
// (with decoded revert reason where available) on any failure instead of os.Exit(1) -- unlike
// bootstrap()'s deliberately fail-loud style, propose/vote/executeProposal callers need to
// distinguish real failures from expected/tolerable reverts (already-minted, already-voted,
// quorum-already-reached) themselves.
func sendTxAndWait(ctx context.Context, client *ethclient.Client, privKey *ecdsa.PrivateKey, fromAddress common.Address, rpcURL string, value *big.Int, gasLimit uint64, calldata []byte, label string) (*types.Receipt, error) {
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: fetch ChainID: %w", label, err)
	}
	nonce, err := client.PendingNonceAt(ctx, fromAddress)
	if err != nil {
		return nil, fmt.Errorf("%s: get nonce: %w", label, err)
	}
	if value == nil {
		value = big.NewInt(0)
	}
	tx := types.NewTransaction(nonce, p_common.GATEWAY_CONTRACT_ADDRESS, value, gasLimit, big.NewInt(1_000_000_000), calldata)
	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), privKey)
	if err != nil {
		return nil, fmt.Errorf("%s: sign tx: %w", label, err)
	}
	if err := client.SendTransaction(ctx, signedTx); err != nil {
		return nil, fmt.Errorf("%s: send tx: %w", label, err)
	}

	var receipt *types.Receipt
	for i := 0; i < 30; i++ {
		receipt, err = client.TransactionReceipt(ctx, signedTx.Hash())
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if receipt == nil {
		return nil, fmt.Errorf("%s: timed out waiting for receipt (tx=%s): %w", label, signedTx.Hash().Hex(), err)
	}
	if receipt.Status != 1 {
		revertMsg := extractRevertReason(rpcURL, signedTx.Hash())
		if revertMsg != "" {
			return receipt, fmt.Errorf("%s: reverted (tx=%s): %s", label, signedTx.Hash().Hex(), revertMsg)
		}
		return receipt, fmt.Errorf("%s: reverted (tx=%s), no revert reason decoded", label, signedTx.Hash().Hex())
	}
	logger.Info("✅ %s succeeded (tx=%s)", label, signedTx.Hash().Hex())
	return receipt, nil
}
