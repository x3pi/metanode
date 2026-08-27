// live_asset_bridge is a multi-step devnet tool for a full real end-to-end custom-asset
// bridge verification against 2 real running private-chain nodes, using the devnet governance
// timelock override (config.CrossChainConfig.DevnetGovernanceTimelockSecondsOverride) instead
// of exploiting the caller-supplied-timestamp gap in vote()/executeProposal() (a separate, real
// finding — see note/cross_chain_production_readiness_plan.md — deliberately not used here so
// this test proves the intended devnet accommodation, not an unrelated bug).
//
// This is what live-verified the full outbound()->attestCommit()->claimMessage() round trip
// (note/cross_chain_production_readiness_plan.md Phase 0.9) and the ProposalAllocateSupply /
// GenesisCoordinator fixes — kept as a real tool (not deleted after use) since re-running the
// same live round trip is the most direct way to re-verify this flow after future changes,
// e.g. for Phase 2's T2 multi-machine testnet run.
//
// Run against ONE chain's RPC at a time; -state is a small per-chain JSON scratch file this
// tool reads/writes across steps. Steps, in order:
//
//	deploy          deploy a real token + approve the Gateway
//	bootstrap       bootstrapFoundingChains with 4 fresh fake committee entries (governance quorum)
//	register-chain  propose+vote+execute ProposalRegisterChain for the OTHER chain ID, with a
//	                fresh committee keypair this tool generates and keeps (so it can later sign
//	                a real attestCommit on that chain's behalf)
//	register-asset  propose+vote+execute ProposalRegisterAsset, then registerAsset()
//	outbound        submit outbound() moving a custom-asset value to the other chain
//	attest          build a real 1-message commit tree for a given MessageID and call
//	                attestCommit() on this chain, signed with the "OTHER chain's committee" key
//	                register-chain saved
//	claim           submit claimMessage() on this chain, completing the bridge
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	eth_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

const testWrappedAssetBytecode = "6080604052348015600e575f5ffd5b50604051610aa8380380610aa88339818101604052810190602e919060a6565b805f5f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f20819055505060cc565b5f5ffd5b5f819050919050565b6088816078565b81146091575f5ffd5b50565b5f8151905060a0816081565b92915050565b5f6020828403121560b85760b76074565b5b5f60c3848285016094565b91505092915050565b6109cf806100d95f395ff3fe608060405234801561000f575f5ffd5b5060043610610060575f3560e01c8063095ea7b31461006457806323b872dd1461009457806340c10f19146100c457806370a08231146100f4578063a9059cbb14610124578063dd62ed3e14610154575b5f5ffd5b61007e600480360381019061007991906106d4565b610184565b60405161008b919061072c565b60405180910390f35b6100ae60048036038101906100a99190610745565b61020c565b6040516100bb919061072c565b60405180910390f35b6100de60048036038101906100d991906106d4565b610484565b6040516100eb919061072c565b60405180910390f35b61010e60048036038101906101099190610795565b6104e1565b60405161011b91906107cf565b60405180910390f35b61013e600480360381019061013991906106d4565b6104f5565b60405161014b919061072c565b60405180910390f35b61016e600480360381019061016991906107e8565b610623565b60405161017b91906107cf565b60405180910390f35b5f8160015f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f20819055506001905092915050565b5f815f5f8673ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f2054101561028c576040517f08c379a000000000000000000000000000000000000000000000000000000000815260040161028390610880565b60405180910390fd5b8160015f8673ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f20541015610347576040517f08c379a000000000000000000000000000000000000000000000000000000000815260040161033e906108e8565b60405180910390fd5b815f5f8673ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546103929190610933565b92505081905550815f5f8573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546103e49190610966565b925050819055508160015f8673ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546104729190610933565b92505081905550600190509392505050565b5f815f5f8573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546104d09190610966565b925050819055506001905092915050565b5f602052805f5260405f205f915090505481565b5f815f5f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f20541015610575576040517f08c379a000000000000000000000000000000000000000000000000000000000815260040161056c90610880565b60405180910390fd5b815f5f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546105c09190610933565b92505081905550815f5f8573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546106129190610966565b925050819055506001905092915050565b6001602052815f5260405f20602052805f5260405f205f91509150505481565b5f5ffd5b5f73ffffffffffffffffffffffffffffffffffffffff82169050919050565b5f61067082610647565b9050919050565b61068081610666565b811461068a575f5ffd5b50565b5f8135905061069b81610677565b92915050565b5f819050919050565b6106b3816106a1565b81146106bd575f5ffd5b50565b5f813590506106ce816106aa565b92915050565b5f5f604083850312156106ea576106e9610643565b5b5f6106f78582860161068d565b9250506020610708858286016106c0565b9150509250929050565b5f8115159050919050565b61072681610712565b82525050565b5f60208201905061073f5f83018461071d565b92915050565b5f5f5f6060848603121561075c5761075b610643565b5b5f6107698682870161068d565b935050602061077a8682870161068d565b925050604061078b868287016106c0565b9150509250925092565b5f602082840312156107aa576107a9610643565b5b5f6107b78482850161068d565b91505092915050565b6107c9816106a1565b82525050565b5f6020820190506107e25f8301846107c0565b92915050565b5f5f604083850312156107fe576107fd610643565b5b5f61080b8582860161068d565b925050602061081c8582860161068d565b9150509250929050565b5f82825260208201905092915050565b7f696e73756666696369656e742062616c616e63650000000000000000000000005f82015250565b5f61086a601483610826565b915061087582610836565b602082019050919050565b5f6020820190508181035f8301526108978161085e565b9050919050565b7f696e73756666696369656e7420616c6c6f77616e6365000000000000000000005f82015250565b5f6108d2601683610826565b91506108dd8261089e565b602082019050919050565b5f6020820190508181035f8301526108ff816108c6565b9050919050565b7f4e487b71000000000000000000000000000000000000000000000000000000005f52601160045260245ffd5b5f61093d826106a1565b9150610948836106a1565b92508282039050818111156109605761095f610906565b5b92915050565b5f610970826106a1565b915061097b836106a1565b925082820190508082111561099357610992610906565b5b9291505056fea2646970667358221220bd2d20322eb6f836f85087699e57feb7cd4cd754080bb4765b6f3cb6c84501fa64736f6c63430008230033"

const testWrappedAssetABIJSON = `[
	{"inputs":[{"internalType":"uint256","name":"initialSupply","type":"uint256"}],"stateMutability":"nonpayable","type":"constructor"},
	{"inputs":[{"internalType":"address","name":"","type":"address"},{"internalType":"address","name":"","type":"address"}],"name":"allowance","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"address","name":"spender","type":"address"},{"internalType":"uint256","name":"value","type":"uint256"}],"name":"approve","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"","type":"address"}],"name":"balanceOf","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"address","name":"to","type":"address"},{"internalType":"uint256","name":"value","type":"uint256"}],"name":"mint","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"to","type":"address"},{"internalType":"uint256","name":"value","type":"uint256"}],"name":"transfer","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"from","type":"address"},{"internalType":"address","name":"to","type":"address"},{"internalType":"uint256","name":"value","type":"uint256"}],"name":"transferFrom","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"}
]`

type committeeMember struct {
	ChainID uint64 `json:"chain_id"`
	PrivHex string `json:"priv_hex"`
}

type stateFile struct {
	Committee    []committeeMember `json:"committee"` // governance quorum (fake founding chains)
	TokenAddress string            `json:"token_address"`
	// RemoteCommitteePrivByChain: this chain's own registered committee entry for each OTHER
	// chain ID it has registered via register-chain, keyed by that chain's ID (as a decimal
	// string) — a single overwritable field here was a real bug: running register-chain twice
	// (once for the real other-chain ID, once more for self-registration so registerAsset's
	// HomeChainID check passes) silently overwrote the first key with the second, so `attest`
	// ended up signing with the wrong keypair for the wrong "chain" without any error until the
	// on-chain BLS verification failed.
	RemoteCommitteePrivByChain map[string]string `json:"remote_committee_priv_by_chain"`
}

func loadState(path string) *stateFile {
	s := &stateFile{}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, s)
	}
	return s
}

func saveState(path string, s *stateFile) {
	data, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(path, data, 0644); err != nil {
		fmt.Println("save state:", err)
		os.Exit(1)
	}
}

var (
	ctx        = context.Background()
	client     *ethclient.Client
	gatewayABI abi.ABI
	tokenABI   abi.ABI
)

func sendCalldata(privKeyHex string, to eth_common.Address, calldata []byte, amount *big.Int, gasLimit uint64, label string) *types.Receipt {
	return sendCalldataSoft(privKeyHex, to, calldata, amount, gasLimit, label, false)
}

func sendCalldataSoft(privKeyHex string, to eth_common.Address, calldata []byte, amount *big.Int, gasLimit uint64, label string, soft bool) *types.Receipt {
	privKey, err := crypto.HexToECDSA(strings.TrimPrefix(privKeyHex, "0x"))
	if err != nil {
		fmt.Println(label, "bad key:", err)
		os.Exit(1)
	}
	from := crypto.PubkeyToAddress(privKey.PublicKey)
	chainID, err := client.ChainID(ctx)
	if err != nil {
		fmt.Println(label, "ChainID:", err)
		os.Exit(1)
	}
	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		fmt.Println(label, "nonce:", err)
		os.Exit(1)
	}
	if amount == nil {
		amount = big.NewInt(0)
	}
	tx := types.NewTransaction(nonce, to, amount, gasLimit, big.NewInt(1_000_000_000), calldata)
	signer := types.LatestSignerForChainID(chainID)
	signedTx, err := types.SignTx(tx, signer, privKey)
	if err != nil {
		fmt.Println(label, "sign:", err)
		os.Exit(1)
	}
	if err := client.SendTransaction(ctx, signedTx); err != nil {
		fmt.Println(label, "send:", err)
		os.Exit(1)
	}
	fmt.Println(label, "tx sent:", signedTx.Hash().Hex())
	for i := 0; i < 25; i++ {
		time.Sleep(2 * time.Second)
		receipt, err := client.TransactionReceipt(ctx, signedTx.Hash())
		if err != nil {
			continue
		}
		if receipt.Status != 1 {
			if soft {
				fmt.Println(label, "⚠️  reverted (soft — likely quorum already reached), continuing")
				return receipt
			}
			fmt.Println(label, "❌ REVERTED")
			reason, _ := client.CallContract(ctx, ethereum.CallMsg{From: from, To: &to, Data: calldata}, receipt.BlockNumber)
			fmt.Println(label, "revert reason bytes:", eth_common.Bytes2Hex(reason))
			os.Exit(1)
		}
		fmt.Println(label, "✅ succeeded, block", receipt.BlockNumber)
		return receipt
	}
	fmt.Println(label, "timed out waiting for receipt")
	os.Exit(1)
	return nil
}

// proposeVoteExecute runs the full propose -> vote (all 4 quorum members) -> wait devnet
// timelock -> executeProposal cycle for the given kind+payload, using the deployer key to pay
// gas for propose/vote/execute (any funded account may submit these — the actual authority is
// the BLS-signed votes, not the transaction sender).
func proposeVoteExecute(deployerKey string, kind uint8, payload []byte, committee []committeeMember, timelockWaitSeconds int) eth_common.Hash {
	now := uint64(time.Now().Unix())
	proposeCalldata, err := gatewayABI.Pack("propose", kind, payload, now)
	if err != nil {
		fmt.Println("pack propose:", err)
		os.Exit(1)
	}
	sendCalldata(deployerKey, p_common.GATEWAY_CONTRACT_ADDRESS, proposeCalldata, big.NewInt(100_000_000_000_000_000), 2_000_000, fmt.Sprintf("propose(kind=%d)", kind))

	var buf []byte
	buf = append(buf, kind)
	var tsBytes [8]byte
	for i := 0; i < 8; i++ {
		tsBytes[7-i] = byte(now >> (8 * i))
	}
	buf = append(buf, tsBytes[:]...)
	buf = append(buf, payload...)
	proposalID := crypto.Keccak256Hash(buf)
	fmt.Println("computed proposalID:", proposalID.Hex())

	voteNow := uint64(time.Now().Unix())
	// Quorum is ceil(2N/3) of the CURRENT ActiveChains size, which grows every time a prior
	// ProposalRegisterChain executes (ExecuteGovernanceProposal calls RegisterActiveChain) — not
	// just len(committee), the original bootstrap set. Rather than tracking that externally,
	// cast every committee member's vote unconditionally and tolerate an
	// "already timelocked"/"already reached quorum" revert on any vote past the real threshold
	// as an expected outcome, not a failure.
	for _, m := range committee {
		kp := bls.NewKeyPair(eth_common.FromHex("0x" + m.PrivHex))
		voteMsg := cross_chain.ComputeGovernanceVoteMessage(proposalID, m.ChainID)
		sig := bls.Sign(kp.PrivateKey(), voteMsg)
		voteCalldata, err := gatewayABI.Pack("vote", proposalID, new(big.Int).SetUint64(m.ChainID), voteNow, kp.BytesPublicKey(), sig.Bytes())
		if err != nil {
			fmt.Println("pack vote:", err)
			os.Exit(1)
		}
		sendCalldataSoft(deployerKey, p_common.GATEWAY_CONTRACT_ADDRESS, voteCalldata, nil, 1_000_000, fmt.Sprintf("vote(chain=%d)", m.ChainID), true)
	}

	fmt.Printf("waiting for devnet timelock (%ds)...\n", timelockWaitSeconds)
	time.Sleep(time.Duration(timelockWaitSeconds) * time.Second)
	execNow := uint64(time.Now().Unix())
	execCalldata, err := gatewayABI.Pack("executeProposal", proposalID, execNow)
	if err != nil {
		fmt.Println("pack executeProposal:", err)
		os.Exit(1)
	}
	sendCalldata(deployerKey, p_common.GATEWAY_CONTRACT_ADDRESS, execCalldata, nil, 1_000_000, fmt.Sprintf("executeProposal(kind=%d)", kind))
	return proposalID
}

func main() {
	rpcURL := flag.String("rpc", "http://127.0.0.1:8551", "chain RPC endpoint")
	deployerKeyHex := flag.String("key", "", "deployer/submitter ECDSA private key hex")
	statePath := flag.String("state", "", "path to JSON state file for this chain")
	step := flag.String("step", "", "deploy|bootstrap|register-chain|register-asset|allocate-supply|outbound|attest|claim")
	supply := flag.String("supply", "1000000", "initial token supply (deploy step)")
	assetID := flag.String("asset-id", "42", "asset id")
	thisChainID := flag.Uint64("this-chain", 101, "this chain's own id")
	otherChainID := flag.Uint64("other-chain", 102, "the other chain id")
	canonicalAddr := flag.String("canonical", "", "canonical token address on the home chain (register-asset step)")
	wrappedAddr := flag.String("wrapped", "", "wrapped token address on the other chain (register-asset step)")
	homeChainID := flag.Uint64("home-chain", 101, "home chain id for the asset (register-asset step)")
	value := flag.String("value", "100", "value to bridge (outbound/attest/claim step)")
	targetAddr := flag.String("target", "", "recipient/target address (outbound) or token contract on this chain (attest/claim)")
	recipientAddr := flag.String("recipient", "", "final recipient address on this chain (attest/claim step)")
	senderAddr := flag.String("sender", "", "original outbound sender address (attest/claim step)")
	messageID := flag.String("message-id", "", "the outbound tx hash, used as MessageID (attest/claim step)")
	sequence := flag.Uint64("sequence", 1, "message sequence (attest/claim step)")
	timelockWait := flag.Int("timelock-wait", 12, "seconds to sleep for the devnet timelock override")
	flag.Parse()

	var err error
	client, err = ethclient.Dial(*rpcURL)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer client.Close()
	st := loadState(*statePath)

	gatewayABI, err = abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	if err != nil {
		fmt.Println("parse gateway ABI:", err)
		os.Exit(1)
	}
	tokenABI, err = abi.JSON(strings.NewReader(testWrappedAssetABIJSON))
	if err != nil {
		fmt.Println("parse token ABI:", err)
		os.Exit(1)
	}

	switch *step {
	case "deploy":
		supplyBig, _ := new(big.Int).SetString(*supply, 10)
		ctorArgs, _ := tokenABI.Pack("", supplyBig)
		bytecode := eth_common.FromHex("0x" + testWrappedAssetBytecode)
		payload := append(append([]byte{}, bytecode...), ctorArgs...)

		privKey, _ := crypto.HexToECDSA(strings.TrimPrefix(*deployerKeyHex, "0x"))
		deployer := crypto.PubkeyToAddress(privKey.PublicKey)
		chainID, _ := client.ChainID(ctx)
		nonce, _ := client.PendingNonceAt(ctx, deployer)
		deployTx := types.NewContractCreation(nonce, big.NewInt(0), 5_000_000, big.NewInt(1_000_000_000), payload)
		signer := types.LatestSignerForChainID(chainID)
		signedTx, _ := types.SignTx(deployTx, signer, privKey)
		if err := client.SendTransaction(ctx, signedTx); err != nil {
			fmt.Println("send deploy:", err)
			os.Exit(1)
		}
		fmt.Println("deploy tx:", signedTx.Hash().Hex())
		var addr eth_common.Address
		for i := 0; i < 20; i++ {
			time.Sleep(2 * time.Second)
			r, err := client.TransactionReceipt(ctx, signedTx.Hash())
			if err != nil {
				continue
			}
			if r.Status != 1 {
				fmt.Println("deploy REVERTED")
				os.Exit(1)
			}
			addr = r.ContractAddress
			break
		}
		if addr == (eth_common.Address{}) {
			fmt.Println("deploy timed out")
			os.Exit(1)
		}
		fmt.Println("✅ deployed at:", addr.Hex())

		approveCalldata, _ := tokenABI.Pack("approve", p_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(1_000_000_000))
		sendCalldata(*deployerKeyHex, addr, approveCalldata, nil, 200_000, "approve")

		st.TokenAddress = addr.Hex()
		saveState(*statePath, st)

	case "bootstrap":
		var payloads [][]byte
		var committee []committeeMember
		for i := 0; i < 4; i++ {
			kp := bls.GenerateKeyPair()
			priv := kp.PrivateKey()
			pub := kp.PublicKey()
			popSig := cross_chain.PopSign(priv, pub)
			fakeChainID := uint64(90001 + i)
			reg := cross_chain.ChainRegistry{
				ChainID: fakeChainID,
				Committee: []cross_chain.ValidatorEntry{
					{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000, PopSignature: popSig.Bytes()},
				},
				Epoch:           0,
				QuorumThreshold: 6667,
			}
			payload, err := json.Marshal(reg)
			if err != nil {
				fmt.Println("marshal registry:", err)
				os.Exit(1)
			}
			payloads = append(payloads, payload)
			committee = append(committee, committeeMember{
				ChainID: fakeChainID,
				PrivHex: eth_common.Bytes2Hex(kp.BytesPrivateKey()),
			})
		}
		calldata, err := gatewayABI.Pack("bootstrapFoundingChains", payloads)
		if err != nil {
			fmt.Println("pack bootstrapFoundingChains:", err)
			os.Exit(1)
		}
		sendCalldata(*deployerKeyHex, p_common.GATEWAY_CONTRACT_ADDRESS, calldata, nil, 3_000_000, "bootstrapFoundingChains")
		st.Committee = committee
		saveState(*statePath, st)

	case "register-chain":
		if len(st.Committee) == 0 {
			fmt.Println("state missing committee — run bootstrap first")
			os.Exit(1)
		}
		kp := bls.GenerateKeyPair()
		reg := cross_chain.ChainRegistry{
			ChainID: *otherChainID,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: kp.BytesPublicKey(), Stake: 1000},
			},
			Epoch:           1,
			QuorumThreshold: 6667,
		}
		payload, err := json.Marshal(reg)
		if err != nil {
			fmt.Println("marshal ChainRegistry:", err)
			os.Exit(1)
		}
		proposeVoteExecute(*deployerKeyHex, 0 /* ProposalRegisterChain */, payload, st.Committee, *timelockWait)
		if st.RemoteCommitteePrivByChain == nil {
			st.RemoteCommitteePrivByChain = make(map[string]string)
		}
		st.RemoteCommitteePrivByChain[fmt.Sprintf("%d", *otherChainID)] = eth_common.Bytes2Hex(kp.BytesPrivateKey())
		saveState(*statePath, st)
		fmt.Println("✅ registered chain", *otherChainID, "with a self-controlled committee key")

	case "register-asset":
		if len(st.Committee) == 0 {
			fmt.Println("state missing committee — run bootstrap first")
			os.Exit(1)
		}
		canonical := eth_common.HexToAddress(*canonicalAddr)
		wrapped := eth_common.HexToAddress(*wrappedAddr)
		assetIDBig, _ := new(big.Int).SetString(*assetID, 10)
		entry := cross_chain.AssetEntry{
			AssetID:           assetIDBig,
			HomeChainID:       *homeChainID,
			CanonicalContract: canonical,
			WrappedContracts:  map[uint64]eth_common.Address{*otherChainID: wrapped},
			Active:            true,
		}
		payload, err := json.Marshal(entry)
		if err != nil {
			fmt.Println("marshal AssetEntry:", err)
			os.Exit(1)
		}
		proposalID := proposeVoteExecute(*deployerKeyHex, 2 /* ProposalRegisterAsset */, payload, st.Committee, *timelockWait)

		supplyBig, _ := new(big.Int).SetString(*supply, 10)
		regCalldata, err := gatewayABI.Pack("registerAsset", proposalID, supplyBig)
		if err != nil {
			fmt.Println("pack registerAsset:", err)
			os.Exit(1)
		}
		sendCalldata(*deployerKeyHex, p_common.GATEWAY_CONTRACT_ADDRESS, regCalldata, nil, 1_000_000, "registerAsset")

	case "allocate-supply":
		// C7 fix (2026-08-27, note/cross_chain_attack_scenario_catalog.md item C7 /
		// note/eurozone_unified_native_coin_plan.md): ProposalAllocateSupply (kind=5) no longer
		// grants an arbitrary chain allocation out of thin air via a vote — that was a real
		// Sybil-mintable path. It now only mints the one-time genesis supply, and only to this
		// chain's own configured ReserveChainID (config.CrossChain.ReserveChainID must equal
		// -this-chain for this RPC target to be a valid mint target). This step now uses
		// ProposalTransferAllocation (kind=6) instead: it moves already-minted allocation from
		// -this-chain (assumed to be Reserve, already holding real minted supply) to
		// -other-chain — safe and repeatable, can never inflate supply, only redistribute it.
		if len(st.Committee) == 0 {
			fmt.Println("state missing committee — run bootstrap first")
			os.Exit(1)
		}
		amountBig, ok := new(big.Int).SetString(*value, 10)
		if !ok {
			fmt.Println("invalid -value for allocate-supply")
			os.Exit(1)
		}
		transfer := cross_chain.AllocationTransferPayload{
			FromChainID: *thisChainID,
			ToChainID:   *otherChainID,
			Amount:      amountBig,
		}
		payload, err := json.Marshal(transfer)
		if err != nil {
			fmt.Println("marshal AllocationTransferPayload:", err)
			os.Exit(1)
		}
		proposeVoteExecute(*deployerKeyHex, 6 /* ProposalTransferAllocation */, payload, st.Committee, *timelockWait)
		fmt.Println("✅ transferred allocation of", amountBig.String(), "from chain", *thisChainID, "to chain", *otherChainID)

	case "query-registry":
		calldata, err := gatewayABI.Pack("getChainRegistry", new(big.Int).SetUint64(*otherChainID))
		if err != nil {
			fmt.Println("pack getChainRegistry:", err)
			os.Exit(1)
		}
		gwAddr := p_common.GATEWAY_CONTRACT_ADDRESS
		out, err := client.CallContract(ctx, ethereum.CallMsg{To: &gwAddr, Data: calldata}, nil)
		if err != nil {
			fmt.Println("call getChainRegistry:", err)
			os.Exit(1)
		}
		results, err := gatewayABI.Unpack("getChainRegistry", out)
		if err != nil {
			fmt.Println("unpack getChainRegistry:", err)
			os.Exit(1)
		}
		fmt.Println("exists:", results[0])
		pubkeys := results[1].([][]byte)
		for i, pk := range pubkeys {
			fmt.Printf("committee[%d] pubkey: %s\n", i, eth_common.Bytes2Hex(pk))
		}
		fmt.Println("epoch:", results[4])
		if priv, ok := st.RemoteCommitteePrivByChain[fmt.Sprintf("%d", *otherChainID)]; ok {
			kp := bls.NewKeyPair(eth_common.FromHex("0x" + priv))
			fmt.Println("my derived pubkey:      ", eth_common.Bytes2Hex(kp.BytesPublicKey()))
		}

	case "outbound":
		assetIDBig, _ := new(big.Int).SetString(*assetID, 10)
		valueBig, _ := new(big.Int).SetString(*value, 10)
		target := eth_common.HexToAddress(*targetAddr)
		calldata, err := gatewayABI.Pack("outbound",
			new(big.Int).SetUint64(*otherChainID), target, []byte{}, assetIDBig, valueBig, big.NewInt(0), big.NewInt(0), uint8(1), false,
		)
		if err != nil {
			fmt.Println("pack outbound:", err)
			os.Exit(1)
		}
		sendCalldata(*deployerKeyHex, p_common.GATEWAY_CONTRACT_ADDRESS, calldata, nil, 3_000_000, "outbound")

	case "attest":
		remotePriv, ok := st.RemoteCommitteePrivByChain[fmt.Sprintf("%d", *otherChainID)]
		if !ok {
			fmt.Println("state missing remote_committee_priv for chain", *otherChainID, "— run register-chain -other-chain", *otherChainID, "first")
			os.Exit(1)
		}
		assetIDBig, _ := new(big.Int).SetString(*assetID, 10)
		valueBig, _ := new(big.Int).SetString(*value, 10)
		msg := cross_chain.CrossChainMessage{
			MessageID:     eth_common.HexToHash(*messageID),
			SourceChainID: *otherChainID,
			DestChainID:   *thisChainID,
			Sequence:      *sequence,
			HopCount:      1,
			Sender:        eth_common.HexToAddress(*senderAddr),
			Target:        eth_common.HexToAddress(*targetAddr),
			AssetID:       assetIDBig,
			Value:         valueBig,
			Payload:       eth_common.HexToAddress(*recipientAddr).Bytes(),
			Tip:           big.NewInt(0),
			GasFee:        big.NewInt(0),
		}
		commitRoot, layers, aggAmounts, aggIndex, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
		if err != nil {
			fmt.Println("BuildCommitTree:", err)
			os.Exit(1)
		}
		assetKey := assetIDBig.String()
		aggregateProof := cross_chain.GetMerkleProof(layers, aggIndex[assetKey])
		commitMsg := cross_chain.ComputeCommitRootAttestMessage(commitRoot)
		kp := bls.NewKeyPair(eth_common.FromHex("0x" + remotePriv))
		sig := bls.Sign(kp.PrivateKey(), commitMsg)

		attestCalldata, err := gatewayABI.Pack("attestCommit",
			new(big.Int).SetUint64(*otherChainID), commitRoot, aggAmounts[assetKey], assetIDBig,
			new(big.Int).SetUint64(aggregateProof.LeafIndex), hashesToBytes32(aggregateProof.Siblings),
			uint64(1), sig.Bytes(), []byte{0x01},
		)
		if err != nil {
			fmt.Println("pack attestCommit:", err)
			os.Exit(1)
		}
		sendCalldata(*deployerKeyHex, p_common.GATEWAY_CONTRACT_ADDRESS, attestCalldata, nil, 2_000_000, "attestCommit")
		fmt.Println("commitRoot:", commitRoot.Hex())
		fmt.Println("(save this — the claim step needs the exact same message fields to")
		fmt.Println("recompute the same commitRoot and Merkle proof)")

	case "claim":
		assetIDBig, _ := new(big.Int).SetString(*assetID, 10)
		valueBig, _ := new(big.Int).SetString(*value, 10)
		msg := cross_chain.CrossChainMessage{
			MessageID:     eth_common.HexToHash(*messageID),
			SourceChainID: *otherChainID,
			DestChainID:   *thisChainID,
			Sequence:      *sequence,
			HopCount:      1,
			Sender:        eth_common.HexToAddress(*senderAddr),
			Target:        eth_common.HexToAddress(*targetAddr),
			AssetID:       assetIDBig,
			Value:         valueBig,
			Payload:       eth_common.HexToAddress(*recipientAddr).Bytes(),
			Tip:           big.NewInt(0),
			GasFee:        big.NewInt(0),
		}
		commitRoot, layers, _, _, err := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
		if err != nil {
			fmt.Println("BuildCommitTree:", err)
			os.Exit(1)
		}
		messageProof := cross_chain.GetMerkleProof(layers, 0)
		calldata, err := gatewayABI.Pack("claimMessage",
			msg.MessageID, new(big.Int).SetUint64(msg.SourceChainID), new(big.Int).SetUint64(msg.DestChainID),
			new(big.Int).SetUint64(msg.Sequence), msg.HopCount, msg.Sender, msg.Target,
			msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
			new(big.Int).SetUint64(messageProof.LeafIndex), hashesToBytes32(messageProof.Siblings), commitRoot,
		)
		if err != nil {
			fmt.Println("pack claimMessage:", err)
			os.Exit(1)
		}
		sendCalldata(*deployerKeyHex, p_common.GATEWAY_CONTRACT_ADDRESS, calldata, nil, 3_000_000, "claimMessage")

	default:
		fmt.Println("unknown -step:", *step)
		os.Exit(1)
	}
}

func hashesToBytes32(hs []eth_common.Hash) [][32]byte {
	out := make([][32]byte, len(hs))
	for i, h := range hs {
		out[i] = h
	}
	return out
}
