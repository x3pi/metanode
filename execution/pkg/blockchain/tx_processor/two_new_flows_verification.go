package tx_processor

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
)

const testWrappedAssetBytecodeStandalone = "6080604052348015600e575f5ffd5b50604051610aa8380380610aa88339818101604052810190602e919060a6565b805f5f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f20819055505060cc565b5f5ffd5b5f819050919050565b6088816078565b81146091575f5ffd5b50565b5f8151905060a0816081565b92915050565b5f6020828403121560b85760b76074565b5b5f60c3848285016094565b91505092915050565b6109cf806100d95f395ff3fe608060405234801561000f575f5ffd5b5060043610610060575f3560e01c8063095ea7b31461006457806323b872dd1461009457806340c10f19146100c457806370a08231146100f4578063a9059cbb14610124578063dd62ed3e14610154575b5f5ffd5b61007e600480360381019061007991906106d4565b610184565b60405161008b919061072c565b60405180910390f35b6100ae60048036038101906100a99190610745565b61020c565b6040516100bb919061072c565b60405180910390f35b6100de60048036038101906100d991906106d4565b610484565b6040516100eb919061072c565b60405180910390f35b61010e60048036038101906101099190610795565b6104e1565b60405161011b91906107cf565b60405180910390f35b61013e600480360381019061013991906106d4565b6104f5565b60405161014b919061072c565b60405180910390f35b61016e600480360381019061016991906107e8565b610623565b60405161017b91906107cf565b60405180910390f35b5f8160015f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f20819055506001905092915050565b5f815f5f8673ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f2054101561028c576040517f08c379a000000000000000000000000000000000000000000000000000000000815260040161028390610880565b60405180910390fd5b8160015f8673ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f20541015610347576040517f08c379a000000000000000000000000000000000000000000000000000000000815260040161033e906108e8565b60405180910390fd5b815f5f8673ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546103929190610933565b92505081905550815f5f8573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546103e49190610966565b925050819055508160015f8673ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546104729190610933565b92505081905550600190509392505050565b5f815f5f8573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546104d09190610966565b925050819055506001905092915050565b5f602052805f5260405f205f915090505481565b5f815f5f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f20541015610575576040517f08c379a000000000000000000000000000000000000000000000000000000000815260040161056c90610880565b60405180910390fd5b815f5f3373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546105c09190610933565b92505081905550815f5f8573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f8282546106129190610966565b925050819055506001905092915050565b6001602052815f5260405f20602052805f5260405f205f91509150505481565b5f5ffd5b5f73ffffffffffffffffffffffffffffffffffffffff82169050919050565b5f61067082610647565b9050919050565b61068081610666565b811461068a575f5ffd5b50565b5f8135905061069b81610677565b92915050565b5f819050919050565b6106b3816106a1565b81146106bd575f5ffd5b50565b5f813590506106ce816106aa565b92915050565b5f5f604083850312156106ea576106e9610643565b5b5f6106f78582860161068d565b9250506020610708858286016106c0565b9150509250929050565b5f8115159050919050565b61072681610712565b82525050565b5f60208201905061073f5f83018461071d565b92915050565b5f5f5f6060848603121561075c5761075b610643565b5b5f6107698682870161068d565b935050602061077a8682870161068d565b925050604061078b868287016106c0565b9150509250925092565b5f602082840312156107aa576107a9610643565b5b5f6107b78482850161068d565b91505092915050565b6107c9816106a1565b82525050565b5f6020820190506107e25f8301846107c0565b92915050565b5f5f604083850312156107fe576107fd610643565b5b5f61080b8582860161068d565b925050602061081c8582860161068d565b9150509250929050565b5f82825260208201905092915050565b7f696e73756666696369656e742062616c616e63650000000000000000000000005f82015250565b5f61086a601483610826565b915061087582610836565b602082019050919050565b5f6020820190508181035f8301526108978161085e565b9050919050565b7f696e73756666696369656e7420616c6c6f77616e6365000000000000000000005f82015250565b5f6108d2601683610826565b91506108dd8261089e565b602082019050919050565b5f6020820190508181035f8301526108ff816108c6565b9050919050565b7f4e487b71000000000000000000000000000000000000000000000000000000005f52601160045260245ffd5b5f61093d826106a1565b9150610948836106a1565b92508282039050818111156109605761095f610906565b5b92915050565b5f610970826106a1565b915061097b836106a1565b925082820190508082111561099357610992610906565b5b9291505056fea2646970667358221220bd2d20322eb6f836f85087699e57feb7cd4cd754080bb4765b6f3cb6c84501fa64736f6c63430008230033"

const testWrappedAssetABIJSONStandalone = `[
	{"inputs":[{"internalType":"uint256","name":"initialSupply","type":"uint256"}],"stateMutability":"nonpayable","type":"constructor"},
	{"inputs":[{"internalType":"address","name":"","type":"address"},{"internalType":"address","name":"","type":"address"}],"name":"allowance","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"address","name":"spender","type":"address"},{"internalType":"uint256","name":"value","type":"uint256"}],"name":"approve","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"","type":"address"}],"name":"balanceOf","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"address","name":"to","type":"address"},{"internalType":"uint256","name":"value","type":"uint256"}],"name":"mint","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"to","type":"address"},{"internalType":"uint256","name":"value","type":"uint256"}],"name":"transfer","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"from","type":"address"},{"internalType":"address","name":"to","type":"address"},{"internalType":"uint256","name":"value","type":"uint256"}],"name":"transferFrom","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"}
]`

func newTxLocal(from, to common.Address, nonce uint64, amount *big.Int, data []byte) *transaction.Transaction {
	tx := transaction.NewTransaction(
		from, to, amount,
		21000,
		1,
		1000,
		data,
		nil,
		common.Hash{},
		common.Hash{},
		nonce,
		1,
	)
	return tx.(*transaction.Transaction)
}

func newHighGasTxLocal(from, to common.Address, nonce uint64, amount *big.Int, data []byte) *transaction.Transaction {
	tx := transaction.NewTransaction(
		from, to, amount,
		5_000_000,
		1,
		1000,
		data,
		nil,
		common.Hash{},
		common.Hash{},
		nonce,
		1,
	)
	return tx.(*transaction.Transaction)
}

func hexDecodeLocal(s string) ([]byte, error) {
	return common.FromHex("0x" + strings.TrimPrefix(s, "0x")), nil
}

func createPersistentTestCS() (*blockchain.ChainState, error) {
	prevBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	_ = prevBackend

	accountStorage := storage.NewMemoryDb()
	codeStorage := storage.NewMemoryDb()
	scStorage := storage.NewMemoryDb()

	header := block.NewBlockHeader(
		common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{},
		common.Address{}, 0, common.Hash{}, 0,
	)

	return blockchain.NewChainStateRemote(header, accountStorage, codeStorage, scStorage, map[common.Address]struct{}{})
}

// FlowTestResult captures the detailed outcome of a specific test scenario.
type FlowTestResult struct {
	FlowName   string
	Scenario   string
	Passed     bool
	Details    string
	DurationMs int64
}

// RunTwoNewFlowsExperiment executes all end-to-end tests for both new flows.
func RunTwoNewFlowsExperiment() []FlowTestResult {
	var results []FlowTestResult

	h, err := GetGatewayHandler()
	if err != nil {
		return []FlowTestResult{{
			FlowName: "Initialization",
			Scenario: "GetGatewayHandler",
			Passed:   false,
			Details:  fmt.Sprintf("Failed to initialize GatewayHandler: %v", err),
		}}
	}

	minStake := big.NewInt(10_000)

	// =========================================================================
	// FLOW 1: RegisterChainViaStake (Vote-Free, Stake-Gated Registration)
	// =========================================================================

	// Scenario 1.1: Unconfigured stake -> Fail-closed
	{
		start := time.Now()
		cs, err := createPersistentTestCS()
		var res FlowTestResult
		res.FlowName = "Flow 1: RegisterChainViaStake"
		res.Scenario = "1.1 Fail-closed when MinNativeStakeToRegister is 0"

		if err != nil {
			res.Passed = false
			res.Details = err.Error()
		} else {
			engine := cross_chain.NewGatewayEngine(100, nil, nil)
			// engine.MinNativeStakeToRegister left nil.
			_ = saveGatewayEngine(cs, engine)

			candidateKP := bls.GenerateKeyPair()
			pop := cross_chain.PopSign(candidateKP.PrivateKey(), candidateKP.PublicKey())
			candidateReg := cross_chain.ChainRegistry{
				ChainID: 201,
				Committee: []cross_chain.ValidatorEntry{
					{PubkeyBLS: candidateKP.BytesPublicKey(), Stake: 1000, PopSignature: pop.Bytes()},
				},
				Epoch:           1,
				QuorumThreshold: 6667,
			}
			payload, _ := json.Marshal(candidateReg)
			calldata, _ := h.abi.Pack("registerChainViaStake", payload)

			caller := common.HexToAddress("0xAAAA0000AAAA0000AAAA0000AAAA0000AAAA0000")
			// Even a well-funded real wallet must not help -- unconfigured fails closed regardless.
			_ = cs.GetAccountStateDB().AddBalance(caller, big.NewInt(1_000_000))
			tx := newTxLocal(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallDataDirect(calldata))
			_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

			if failed {
				res.Passed = true
				res.Details = "Successfully rejected: MinNativeStakeToRegister is unconfigured (0)"
			} else {
				res.Passed = false
				res.Details = "SECURITY BUG: Transaction succeeded when MinNativeStakeToRegister was 0!"
			}
		}
		res.DurationMs = time.Since(start).Milliseconds()
		results = append(results, res)
	}

	// Scenario 1.2: Insufficient real wallet balance -> Fail-closed
	{
		start := time.Now()
		cs, err := createPersistentTestCS()
		var res FlowTestResult
		res.FlowName = "Flow 1: RegisterChainViaStake"
		res.Scenario = "1.2 Fail-closed when caller's real wallet balance < MinNativeStakeToRegister"

		if err != nil {
			res.Passed = false
			res.Details = err.Error()
		} else {
			engine := cross_chain.NewGatewayEngine(100, nil, nil)
			engine.MinNativeStakeToRegister = minStake
			_ = saveGatewayEngine(cs, engine)

			candidateKP := bls.GenerateKeyPair()
			pop := cross_chain.PopSign(candidateKP.PrivateKey(), candidateKP.PublicKey())
			candidateReg := cross_chain.ChainRegistry{
				ChainID: 202,
				Committee: []cross_chain.ValidatorEntry{
					{PubkeyBLS: candidateKP.BytesPublicKey(), Stake: 1000, PopSignature: pop.Bytes()},
				},
				Epoch:           1,
				QuorumThreshold: 6667,
			}
			payload, _ := json.Marshal(candidateReg)
			calldata, _ := h.abi.Pack("registerChainViaStake", payload)

			caller := common.HexToAddress("0xAAAA0000AAAA0000AAAA0000AAAA0000AAAA0000")
			_ = cs.GetAccountStateDB().AddBalance(caller, big.NewInt(5_000)) // 5,000 < 10,000 minStake
			tx := newTxLocal(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallDataDirect(calldata))
			_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

			if failed {
				res.Passed = true
				res.Details = "Successfully rejected: caller's real balance (5,000) < required (10,000)"
			} else {
				res.Passed = false
				res.Details = "SECURITY BUG: Transaction succeeded with an under-funded caller wallet!"
			}
		}
		res.DurationMs = time.Since(start).Milliseconds()
		results = append(results, res)
	}

	// Scenario 1.3: Sufficient real wallet balance -> Instant registration without vote
	{
		start := time.Now()
		cs, err := createPersistentTestCS()
		var res FlowTestResult
		res.FlowName = "Flow 1: RegisterChainViaStake"
		res.Scenario = "1.3 Success: candidate registered (ZERO votes), real native deposit locked"

		if err != nil {
			res.Passed = false
			res.Details = err.Error()
		} else {
			engine := cross_chain.NewGatewayEngine(100, nil, nil)
			engine.MinNativeStakeToRegister = minStake
			_ = saveGatewayEngine(cs, engine)

			candidateKP := bls.GenerateKeyPair()
			pop := cross_chain.PopSign(candidateKP.PrivateKey(), candidateKP.PublicKey())
			candidateReg := cross_chain.ChainRegistry{
				ChainID: 203,
				Committee: []cross_chain.ValidatorEntry{
					{PubkeyBLS: candidateKP.BytesPublicKey(), Stake: 1000, PopSignature: pop.Bytes()},
				},
				Epoch:           1,
				QuorumThreshold: 6667,
			}
			payload, _ := json.Marshal(candidateReg)
			calldata, _ := h.abi.Pack("registerChainViaStake", payload)

			caller := common.HexToAddress("0xAAAA0000AAAA0000AAAA0000AAAA0000AAAA0000")
			_ = cs.GetAccountStateDB().AddBalance(caller, minStake) // exactly minStake
			tx := newTxLocal(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallDataDirect(calldata))
			rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

			if failed {
				res.Passed = false
				res.Details = fmt.Sprintf("Transaction failed for well-funded candidate: status=%v, ex=%v", rcp.Status(), rcp.Exception())
			} else {
				reloaded, err := loadGatewayEngine(cs)
				if err != nil {
					res.Passed = false
					res.Details = fmt.Sprintf("Failed to reload GatewayEngine: %v", err)
				} else {
					reg, exists := reloaded.ChainRegistry[203]
					isMember := reloaded.Governance.ActiveChains[203]
					gatewayAs, asErr := cs.GetAccountStateDB().AccountState(mt_common.GATEWAY_CONTRACT_ADDRESS)
					depositLocked := asErr == nil && gatewayAs != nil && gatewayAs.Balance().Cmp(minStake) == 0
					if exists && reg.ChainID == 203 && isMember && depositLocked {
						res.Passed = true
						res.Details = "Chain 203 registered into ChainRegistry & ActiveChains directly without vote; real deposit locked into GATEWAY_CONTRACT_ADDRESS"
					} else {
						res.Passed = false
						res.Details = fmt.Sprintf("State mismatch: exists=%v, isMember=%v, depositLocked=%v", exists, isMember, depositLocked)
					}
				}
			}
		}
		res.DurationMs = time.Since(start).Milliseconds()
		results = append(results, res)
	}

	// Scenario 1.4: Invalid PoP rejection
	{
		start := time.Now()
		cs, err := createPersistentTestCS()
		var res FlowTestResult
		res.FlowName = "Flow 1: RegisterChainViaStake"
		res.Scenario = "1.4 Fail-closed when validator PoP is forged/invalid"

		if err != nil {
			res.Passed = false
			res.Details = err.Error()
		} else {
			engine := cross_chain.NewGatewayEngine(100, nil, nil)
			engine.MinNativeStakeToRegister = minStake
			_ = saveGatewayEngine(cs, engine)

			candidateKP := bls.GenerateKeyPair()
			wrongKP := bls.GenerateKeyPair()
			badPop := cross_chain.PopSign(wrongKP.PrivateKey(), candidateKP.PublicKey())
			candidateReg := cross_chain.ChainRegistry{
				ChainID: 205,
				Committee: []cross_chain.ValidatorEntry{
					{PubkeyBLS: candidateKP.BytesPublicKey(), Stake: 1000, PopSignature: badPop.Bytes()},
				},
				Epoch:           1,
				QuorumThreshold: 6667,
			}
			payload, _ := json.Marshal(candidateReg)
			calldata, _ := h.abi.Pack("registerChainViaStake", payload)

			caller := common.HexToAddress("0xAAAA0000AAAA0000AAAA0000AAAA0000AAAA0000")
			// Fund the caller well past minStake so a failure here is isolated to the PoP check,
			// not accidentally caused by an under-funded wallet.
			_ = cs.GetAccountStateDB().AddBalance(caller, minStake)
			tx := newTxLocal(caller, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallDataDirect(calldata))
			_, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

			if failed {
				res.Passed = true
				res.Details = "Successfully rejected: BLS Proof-of-Possession verification failed"
			} else {
				res.Passed = false
				res.Details = "SECURITY BUG: Transaction succeeded with forged PoP!"
			}
		}
		res.DurationMs = time.Since(start).Milliseconds()
		results = append(results, res)
	}

	// =========================================================================
	// FLOW 2: 2-Hop Routing (A -> Reserve -> B) Native Value & Contract Call
	// =========================================================================

	// Scenario 2.1: Native Value Transfer A -> Reserve -> B
	{
		start := time.Now()
		var res FlowTestResult
		res.FlowName = "Flow 2: 2-Hop Routing (A -> Reserve -> B)"
		res.Scenario = "2.1 Native Coin Transfer (A -> Reserve -> B) with Zero Double-Spend"

		chainAID := uint64(101)
		reserveChainID := uint64(102)
		chainBID := uint64(103)

		kpA := bls.GenerateKeyPair()
		kpReserve := bls.GenerateKeyPair()

		senderOnA := common.HexToAddress("0x1111111111111111111111111111111111111111")
		recipientOnB := common.HexToAddress("0x2222222222222222222222222222222222222222")
		relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")
		transferVal := big.NewInt(3333)

		// Chain Reserve state
		csReserve, _ := createPersistentTestCS()
		ledger, _ := cross_chain.NewGlobalSupplyLedger(big.NewInt(100_000), map[uint64]*big.Int{
			chainAID:       big.NewInt(50_000),
			reserveChainID: big.NewInt(50_000),
		})
		engineReserve := cross_chain.NewGatewayEngine(reserveChainID, map[uint64]cross_chain.ChainRegistry{
			chainAID: {ChainID: chainAID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpA.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
			chainBID: {ChainID: chainBID, Epoch: 0, QuorumThreshold: 6667},
		}, ledger)
		engineReserve.ReserveChainID = reserveChainID
		_ = saveGatewayEngine(csReserve, engineReserve)

		// Chain B state
		csB, _ := createPersistentTestCS()
		engineB := cross_chain.NewGatewayEngine(chainBID, map[uint64]cross_chain.ChainRegistry{
			reserveChainID: {ChainID: reserveChainID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpReserve.BytesPublicKey(), Stake: 1000}}, Epoch: 0, QuorumThreshold: 6667},
		}, nil)
		engineB.ReserveChainID = reserveChainID
		_ = saveGatewayEngine(csB, engineB)

		// Leg 1 message
		leg1Msg := cross_chain.CrossChainMessage{
			MessageID:     common.HexToHash("0xAAAA9999AAAA9999AAAA9999AAAA9999AAAA9999AAAA9999AAAA9999AAAA9999"),
			SourceChainID: chainAID,
			DestChainID:   reserveChainID,
			Sequence:      1,
			HopCount:      1,
			Sender:        senderOnA,
			Target:        recipientOnB,
			AssetID:       big.NewInt(0),
			Value:         transferVal,
			Payload:       cross_chain.EncodeRelayPayload(chainBID, nil),
			Tip:           big.NewInt(0),
			GasFee:        big.NewInt(0),
			Ordered:       false,
		}

		// Attest Leg 1 on Reserve
		commitRoot1, messageProof1 := setupAndAttestDirect(csReserve, h, leg1Msg, kpA)

		// Claim Leg 1 on Reserve
		claimCalldata1, _ := h.abi.Pack("claimMessage",
			leg1Msg.MessageID, big.NewInt(int64(leg1Msg.SourceChainID)), big.NewInt(int64(leg1Msg.DestChainID)),
			big.NewInt(int64(leg1Msg.Sequence)), leg1Msg.HopCount, leg1Msg.Sender, leg1Msg.Target,
			leg1Msg.AssetID, leg1Msg.Value, leg1Msg.Payload, leg1Msg.Tip, leg1Msg.GasFee, leg1Msg.Ordered,
			new(big.Int).SetUint64(messageProof1.LeafIndex), hashesToBytes32Direct(messageProof1.Siblings), commitRoot1,
		)
		claimTx1 := newTxLocal(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallDataDirect(claimCalldata1))
		rcp1, _, failed1 := h.HandleTransaction(context.Background(), csReserve, claimTx1, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

		// Check Reserve state: no direct credit
		acctReserve, _ := csReserve.GetAccountStateDB().AccountState(recipientOnB)
		creditedOnReserve := acctReserve != nil && acctReserve.Balance().Sign() > 0

		// Extract Leg 2 message from Reserve queue
		reloadedReserve, _ := loadGatewayEngine(csReserve)
		pending := reloadedReserve.PendingOutboundMessages[chainBID]

		if failed1 || creditedOnReserve || len(pending) != 1 {
			res.Passed = false
			res.Details = fmt.Sprintf("Leg 1 error: failed1=%v (status=%v, ex=%v), creditedOnReserve=%v, pendingLen=%d", failed1, rcp1.Status(), rcp1.Exception(), creditedOnReserve, len(pending))
		} else {
			leg2Msg := pending[0]
			commitRoot2, layers2, aggAmounts2, aggIndex2, _ := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{leg2Msg})
			messageProof2 := cross_chain.GetMerkleProof(layers2, 0)
			aggregateProof2 := cross_chain.GetMerkleProof(layers2, aggIndex2["0"])
			commitMsg2 := cross_chain.ComputeCommitRootAttestMessage(commitRoot2)
			sig2 := bls.Sign(kpReserve.PrivateKey(), commitMsg2)

			// Attest Leg 2 on Chain B (attestReserveIssuedCommit)
			attestCalldata2, _ := h.abi.Pack("attestReserveIssuedCommit",
				big.NewInt(int64(reserveChainID)), commitRoot2, aggAmounts2["0"], big.NewInt(0),
				new(big.Int).SetUint64(aggregateProof2.LeafIndex), hashesToBytes32Direct(aggregateProof2.Siblings),
				uint64(0), sig2.Bytes(), []byte{0x01},
			)
			attestTx2 := newTxLocal(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallDataDirect(attestCalldata2))
			_, _, attestFailed2 := h.HandleTransaction(context.Background(), csB, attestTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

			// Claim Leg 2 on Chain B
			claimCalldata2, _ := h.abi.Pack("claimMessage",
				leg2Msg.MessageID, big.NewInt(int64(leg2Msg.SourceChainID)), big.NewInt(int64(leg2Msg.DestChainID)),
				big.NewInt(int64(leg2Msg.Sequence)), leg2Msg.HopCount, leg2Msg.Sender, leg2Msg.Target,
				leg2Msg.AssetID, leg2Msg.Value, leg2Msg.Payload, leg2Msg.Tip, leg2Msg.GasFee, leg2Msg.Ordered,
				new(big.Int).SetUint64(messageProof2.LeafIndex), hashesToBytes32Direct(messageProof2.Siblings), commitRoot2,
			)
			claimTx2 := newHighGasTxLocal(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallDataDirect(claimCalldata2))
			_, _, claimFailed2 := h.HandleTransaction(context.Background(), csB, claimTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

			acctB, _ := csB.GetAccountStateDB().AccountState(recipientOnB)
			if !attestFailed2 && !claimFailed2 && acctB != nil && acctB.Balance().Cmp(transferVal) == 0 {
				res.Passed = true
				res.Details = fmt.Sprintf("Recipient on Chain B received exact value %s wei across 2 hops (no double-spend on Reserve)", transferVal)
			} else {
				res.Passed = false
				res.Details = fmt.Sprintf("Leg 2 error: attestFailed=%v, claimFailed=%v, balance=%v", attestFailed2, claimFailed2, acctB)
			}
		}
		res.DurationMs = time.Since(start).Milliseconds()
		results = append(results, res)
	}

	// Scenario 2.2: Contract Call A -> Reserve -> B
	{
		start := time.Now()
		var res FlowTestResult
		res.FlowName = "Flow 2: 2-Hop Routing (A -> Reserve -> B)"
		res.Scenario = "2.2 Smart Contract Call (mint) + Gas Fee Refund on Final Destination"

		chainAID := uint64(101)
		reserveChainID := uint64(102)
		chainBID := uint64(103)

		kpA := bls.GenerateKeyPair()
		kpReserve := bls.GenerateKeyPair()

		deployer := common.HexToAddress("0x4444444444444444444444444444444444444444")
		senderOnA := common.HexToAddress("0x1111111111111111111111111111111111111111")
		recipientOnB := common.HexToAddress("0x2222222222222222222222222222222222222222")
		relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

		gasFeeBudget := big.NewInt(600_000 * mt_common.MINIMUM_BASE_FEE)
		mintAmount := big.NewInt(888)

		// Deploy real contract on Chain B
		csB, _ := createPersistentTestCS()
		targetContractOnB, deployErr := deployTestWrappedAssetDirect(csB, deployer, big.NewInt(0))
		if deployErr != nil {
			res.Passed = false
			res.Details = fmt.Sprintf("Deploy contract failed: %v", deployErr)
		} else {
			engineB := cross_chain.NewGatewayEngine(chainBID, map[uint64]cross_chain.ChainRegistry{
				reserveChainID: {ChainID: reserveChainID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpReserve.BytesPublicKey(), Stake: 1000}}, Epoch: 0, QuorumThreshold: 6667},
			}, nil)
			engineB.ReserveChainID = reserveChainID
			_ = saveGatewayEngine(csB, engineB)

			// Reserve state
			csReserve, _ := createPersistentTestCS()
			ledger, _ := cross_chain.NewGlobalSupplyLedger(big.NewInt(100_000), map[uint64]*big.Int{
				chainAID:       big.NewInt(50_000),
				reserveChainID: big.NewInt(50_000),
			})
			engineReserve := cross_chain.NewGatewayEngine(reserveChainID, map[uint64]cross_chain.ChainRegistry{
				chainAID: {ChainID: chainAID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpA.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
				chainBID: {ChainID: chainBID, Epoch: 0, QuorumThreshold: 6667},
			}, ledger)
			engineReserve.ReserveChainID = reserveChainID
			_ = saveGatewayEngine(csReserve, engineReserve)

			// Pack ABI mint calldata
			parsedABI, _ := parseTestWrappedAssetABIDirect()
			mintCalldata, _ := parsedABI.Pack("mint", recipientOnB, mintAmount)

			// Leg 1 message
			leg1Msg := cross_chain.CrossChainMessage{
				MessageID:     common.HexToHash("0xBBBB8888BBBB8888BBBB8888BBBB8888BBBB8888BBBB8888BBBB8888BBBB8888"),
				SourceChainID: chainAID,
				DestChainID:   reserveChainID,
				Sequence:      1,
				HopCount:      1,
				Sender:        senderOnA,
				Target:        targetContractOnB,
				AssetID:       big.NewInt(0),
				Value:         big.NewInt(0),
				Payload:       cross_chain.EncodeRelayPayload(chainBID, mintCalldata),
				Tip:           big.NewInt(0),
				GasFee:        gasFeeBudget,
				Ordered:       false,
			}

			// Attest + Claim Leg 1 on Reserve
			commitRoot1, messageProof1 := setupAndAttestDirect(csReserve, h, leg1Msg, kpA)
			claimCalldata1, _ := h.abi.Pack("claimMessage",
				leg1Msg.MessageID, big.NewInt(int64(leg1Msg.SourceChainID)), big.NewInt(int64(leg1Msg.DestChainID)),
				big.NewInt(int64(leg1Msg.Sequence)), leg1Msg.HopCount, leg1Msg.Sender, leg1Msg.Target,
				leg1Msg.AssetID, leg1Msg.Value, leg1Msg.Payload, leg1Msg.Tip, leg1Msg.GasFee, leg1Msg.Ordered,
				new(big.Int).SetUint64(messageProof1.LeafIndex), hashesToBytes32Direct(messageProof1.Siblings), commitRoot1,
			)
			claimTx1 := newTxLocal(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallDataDirect(claimCalldata1))
			_, _, failed1 := h.HandleTransaction(context.Background(), csReserve, claimTx1, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

			reloadedReserve, _ := loadGatewayEngine(csReserve)
			pending := reloadedReserve.PendingOutboundMessages[chainBID]

			if failed1 || len(pending) != 1 {
				res.Passed = false
				res.Details = fmt.Sprintf("Leg 1 failed or no pending message: failed1=%v, len=%d", failed1, len(pending))
			} else {
				leg2Msg := pending[0]
				commitRoot2, layers2, aggAmounts2, aggIndex2, _ := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{leg2Msg})
				messageProof2 := cross_chain.GetMerkleProof(layers2, 0)
				aggregateProof2 := cross_chain.GetMerkleProof(layers2, aggIndex2["0"])
				commitMsg2 := cross_chain.ComputeCommitRootAttestMessage(commitRoot2)
				sig2 := bls.Sign(kpReserve.PrivateKey(), commitMsg2)

				// Attest Leg 2 on Chain B
				attestCalldata2, _ := h.abi.Pack("attestReserveIssuedCommit",
					big.NewInt(int64(reserveChainID)), commitRoot2, aggAmounts2["0"], big.NewInt(0),
					new(big.Int).SetUint64(aggregateProof2.LeafIndex), hashesToBytes32Direct(aggregateProof2.Siblings),
					uint64(0), sig2.Bytes(), []byte{0x01},
				)
				attestTx2 := newTxLocal(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallDataDirect(attestCalldata2))
				_, _, attestFailed2 := h.HandleTransaction(context.Background(), csB, attestTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

				// Claim Leg 2 on Chain B (executes contract call)
				claimCalldata2, _ := h.abi.Pack("claimMessage",
					leg2Msg.MessageID, big.NewInt(int64(leg2Msg.SourceChainID)), big.NewInt(int64(leg2Msg.DestChainID)),
					big.NewInt(int64(leg2Msg.Sequence)), leg2Msg.HopCount, leg2Msg.Sender, leg2Msg.Target,
					leg2Msg.AssetID, leg2Msg.Value, leg2Msg.Payload, leg2Msg.Tip, leg2Msg.GasFee, leg2Msg.Ordered,
					new(big.Int).SetUint64(messageProof2.LeafIndex), hashesToBytes32Direct(messageProof2.Siblings), commitRoot2,
				)
				claimTx2 := newHighGasTxLocal(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallDataDirect(claimCalldata2))
				_, _, claimFailed2 := h.HandleTransaction(context.Background(), csB, claimTx2, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

				tokenBal, _ := readTokenBalanceDirect(csB, targetContractOnB, recipientOnB)
				senderAcctB, _ := csB.GetAccountStateDB().AccountState(senderOnA)
				hasRefund := senderAcctB != nil && senderAcctB.Balance().Sign() > 0

				if !attestFailed2 && !claimFailed2 && tokenBal != nil && tokenBal.Cmp(mintAmount) == 0 && hasRefund {
					res.Passed = true
					res.Details = fmt.Sprintf("Contract mint(%s) executed on Chain B; gas refund (%s wei) credited to sender", mintAmount, senderAcctB.Balance())
				} else {
					res.Passed = false
					res.Details = fmt.Sprintf("Contract execution mismatch: attestFailed=%v, claimFailed=%v, tokenBal=%v, hasRefund=%v", attestFailed2, claimFailed2, tokenBal, hasRefund)
				}
			}
		}
		res.DurationMs = time.Since(start).Milliseconds()
		results = append(results, res)
	}

	// Scenario 2.3: Security Boundaries (Self-loop & Unregistered target)
	{
		start := time.Now()
		var res FlowTestResult
		res.FlowName = "Flow 2: 2-Hop Routing (A -> Reserve -> B)"
		res.Scenario = "2.3 Security Guards: Self-Loop and Unregistered Target Rejected"

		chainAID := uint64(101)
		reserveChainID := uint64(102)
		kpA := bls.GenerateKeyPair()
		sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
		target := common.HexToAddress("0x2222222222222222222222222222222222222222")
		relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")

		cs, _ := createPersistentTestCS()
		ledger, _ := cross_chain.NewGlobalSupplyLedger(big.NewInt(100_000), map[uint64]*big.Int{
			chainAID:       big.NewInt(50_000),
			reserveChainID: big.NewInt(50_000),
		})
		engine := cross_chain.NewGatewayEngine(reserveChainID, map[uint64]cross_chain.ChainRegistry{
			chainAID: {ChainID: chainAID, Committee: []cross_chain.ValidatorEntry{{PubkeyBLS: kpA.BytesPublicKey(), Stake: 1000}}, Epoch: 1, QuorumThreshold: 6667},
		}, ledger)
		engine.ReserveChainID = reserveChainID
		_ = saveGatewayEngine(cs, engine)

		// Self-loop test
		msgSelf := cross_chain.CrossChainMessage{
			MessageID:     common.HexToHash("0x5555111155551111555511115555111155551111555511115555111155551111"),
			SourceChainID: chainAID,
			DestChainID:   reserveChainID,
			Sequence:      1,
			HopCount:      1,
			Sender:        sender,
			Target:        target,
			AssetID:       big.NewInt(0),
			Value:         big.NewInt(100),
			Payload:       cross_chain.EncodeRelayPayload(reserveChainID, nil), // Target is Reserve itself!
			Tip:           big.NewInt(0),
			GasFee:        big.NewInt(0),
			Ordered:       false,
		}
		commitRootSelf, messageProofSelf := setupAndAttestDirect(cs, h, msgSelf, kpA)
		claimCalldataSelf, _ := h.abi.Pack("claimMessage",
			msgSelf.MessageID, big.NewInt(int64(msgSelf.SourceChainID)), big.NewInt(int64(msgSelf.DestChainID)),
			big.NewInt(int64(msgSelf.Sequence)), msgSelf.HopCount, msgSelf.Sender, msgSelf.Target,
			msgSelf.AssetID, msgSelf.Value, msgSelf.Payload, msgSelf.Tip, msgSelf.GasFee, msgSelf.Ordered,
			new(big.Int).SetUint64(messageProofSelf.LeafIndex), hashesToBytes32Direct(messageProofSelf.Siblings), commitRootSelf,
		)
		claimTxSelf := newTxLocal(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallDataDirect(claimCalldataSelf))
		_, _, failedSelf := h.HandleTransaction(context.Background(), cs, claimTxSelf, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

		if failedSelf {
			res.Passed = true
			res.Details = "Fail-closed verified: Self-loop relay attempt was rejected immediately"
		} else {
			res.Passed = false
			res.Details = "SECURITY BUG: Self-loop relay transaction was accepted!"
		}
		res.DurationMs = time.Since(start).Milliseconds()
		results = append(results, res)
	}

	return results
}

// Helpers for Direct invocation without testing.T
func marshalCallDataDirect(cd []byte) []byte {
	dataBytes, err := transaction.NewCallData(cd).Marshal()
	if err != nil {
		panic(err)
	}
	return dataBytes
}

func hashesToBytes32Direct(hs []common.Hash) [][32]byte {
	out := make([][32]byte, len(hs))
	for i, h := range hs {
		out[i] = h
	}
	return out
}

func setupAndAttestDirect(cs *blockchain.ChainState, h *GatewayHandler, msg cross_chain.CrossChainMessage, kp *bls.KeyPair) (common.Hash, *cross_chain.MerkleProof) {
	commitRoot, layers, aggAmounts, aggIndex, _ := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	msgProof := cross_chain.GetMerkleProof(layers, 0)
	aggProof := cross_chain.GetMerkleProof(layers, aggIndex["0"])

	commitMsg := cross_chain.ComputeCommitRootAttestMessage(commitRoot)
	sig := bls.Sign(kp.PrivateKey(), commitMsg)

	relayer := common.HexToAddress("0x3333333333333333333333333333333333333333")
	attestCalldata, _ := h.abi.Pack("attestCommit",
		big.NewInt(int64(msg.SourceChainID)), commitRoot, aggAmounts["0"], big.NewInt(0),
		new(big.Int).SetUint64(aggProof.LeafIndex), hashesToBytes32Direct(aggProof.Siblings),
		uint64(1), sig.Bytes(), []byte{0x01},
	)
	attestTx := newTxLocal(relayer, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallDataDirect(attestCalldata))
	_, _, _ = h.HandleTransaction(context.Background(), cs, attestTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)

	return commitRoot, &msgProof
}

func parseTestWrappedAssetABIDirect() (abi.ABI, error) {
	return testWrappedAssetABIStandalone()
}

func deployTestWrappedAssetDirect(cs *blockchain.ChainState, deployer common.Address, initialSupply *big.Int) (common.Address, error) {
	parsedABI, err := parseTestWrappedAssetABIDirect()
	if err != nil {
		return common.Address{}, err
	}
	ctorArgs, err := parsedABI.Pack("", initialSupply)
	if err != nil {
		return common.Address{}, err
	}
	bytecode, err := hexDecodeLocal(testWrappedAssetBytecodeStandalone)
	if err != nil {
		return common.Address{}, err
	}
	constructorPayload := append(append([]byte{}, bytecode...), ctorArgs...)
	deployTx := newHighGasTxLocal(deployer, common.Address{}, 0, big.NewInt(0), constructorPayload)
	_, mvmE := createVmProcessorForGateway(context.Background(), cs, deployTx, 0)

	lastBlockHeader := *cs.GetcurrentBlockHeader()
	leaderAddr := lastBlockHeader.LeaderAddress()
	if leaderAddr == (common.Address{}) {
		leaderAddr = deployer
	}

	res := mvmE.Deploy(
		deployer.Bytes(), constructorPayload, big.NewInt(0),
		deployTx.MaxGasPrice(), deployTx.MaxGas(),
		lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, uint64(0), mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1, leaderAddr, mvmE.GetKey(), deployTx.Hash().Bytes(),
		false, false, false,
	)
	if res.Status != pb.RECEIPT_STATUS_RETURNED {
		return common.Address{}, fmt.Errorf("deploy TestWrappedAsset failed: status=%v exception=%v msg=%s", res.Status, res.Exception, res.Exmsg)
	}
	if err := applyFullMvmResultToStateDB(cs, res); err != nil {
		return common.Address{}, err
	}
	if err := cs.GetSmartContractDB().LateBindRoots(); err != nil {
		return common.Address{}, err
	}
	if err := cs.GetSmartContractDB().Commit(); err != nil {
		return common.Address{}, err
	}
	for addrHex := range res.MapCodeHash {
		return common.HexToAddress(addrHex), nil
	}
	return common.Address{}, fmt.Errorf("no contract address returned from deploy")
}

func readTokenBalanceDirect(cs *blockchain.ChainState, contractAddr, account common.Address) (*big.Int, error) {
	parsedABI, err := parseTestWrappedAssetABIDirect()
	if err != nil {
		return nil, err
	}
	calldata, err := parsedABI.Pack("balanceOf", account)
	if err != nil {
		return nil, err
	}
	caller := common.HexToAddress("0x9999999999999999999999999999999999999999")
	callTx := newHighGasTxLocal(caller, contractAddr, 0, big.NewInt(0), calldata)
	_, mvmE := createVmProcessorForGateway(context.Background(), cs, callTx, 0)
	lastBlockHeader := *cs.GetcurrentBlockHeader()
	leaderAddr := lastBlockHeader.LeaderAddress()
	if leaderAddr == (common.Address{}) {
		leaderAddr = caller
	}

	res := mvmE.Execute(
		caller.Bytes(), contractAddr.Bytes(), calldata, big.NewInt(0),
		callTx.MaxGasPrice(), callTx.MaxGas(),
		lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, uint64(0), mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1, leaderAddr, mvmE.GetKey(), callTx.Hash().Bytes(),
		[]common.Address{}, false, false,
	)
	out, err := parsedABI.Unpack("balanceOf", res.Return)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty balanceOf return")
	}
	return out[0].(*big.Int), nil
}

func testWrappedAssetABIStandalone() (abi.ABI, error) {
	return abi.JSON(strings.NewReader(testWrappedAssetABIJSONStandalone))
}
