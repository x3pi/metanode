package tx_processor

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHighGasTx is like newTx (true_block_stm_integration_test.go) but with a much higher gas
// limit — newTx's hardcoded 21000 (a plain-transfer-sized limit) is nowhere near enough for a
// real CREATE (contract deployment) or a real contract call in this test file.
func newHighGasTx(from, to common.Address, nonce uint64, amount *big.Int, data []byte) *transaction.Transaction {
	tx := transaction.NewTransaction(
		from, to, amount,
		5_000_000, // maxGas — enough for CREATE of a small contract + a few SSTOREs
		1,         // maxGasPrice
		1000,      // maxTimeUse
		data,
		nil,
		common.Hash{},
		common.Hash{},
		nonce,
		1,
	)
	return tx.(*transaction.Transaction)
}

// testWrappedAssetBytecode / testWrappedAssetABIJSON — real, solc-0.8.35-compiled bytecode for
// a minimal ERC-20-shaped fixture contract (transfer/transferFrom/mint), used ONLY to close the
// Task 1.2 test-coverage gap flagged in note/cross_chain_task1_native_value_fix_plan.md and
// note/cross_chain_production_readiness_plan.md Phase 0.7: the existing
// TestGatewayHandler_CustomAsset_Outbound_ClaimMessage only proves the custom-asset code path
// fails gracefully against a non-contract address — it never proves a real transferFrom/mint
// call actually succeeds against a real deployed token contract. Source kept for reference in
// this comment (not compiled at test time — no solc dependency in the test suite):
//
//	contract TestWrappedAsset {
//	    mapping(address => uint256) public balanceOf;
//	    mapping(address => mapping(address => uint256)) public allowance;
//	    constructor(uint256 initialSupply) { balanceOf[msg.sender] = initialSupply; }
//	    function approve(address spender, uint256 value) public returns (bool) { ... }
//	    function transfer(address to, uint256 value) public returns (bool) { ... }
//	    function transferFrom(address from, address to, uint256 value) public returns (bool) { ... }
//	    function mint(address to, uint256 value) public returns (bool) { ... }
//	}
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

func testWrappedAssetABI(t *testing.T) abi.ABI {
	t.Helper()
	parsed, err := abi.JSON(strings.NewReader(testWrappedAssetABIJSON))
	if err != nil {
		t.Fatalf("parse testWrappedAsset ABI: %v", err)
	}
	return parsed
}

// deployTestWrappedAsset deploys a real instance of the fixture contract above via the same
// mvm.ExecutionEngine.Deploy path production code uses (not a shortcut state injection), and
// returns its real deployed address.
func deployTestWrappedAsset(t *testing.T, cs *blockchain.ChainState, deployer common.Address, initialSupply *big.Int) common.Address {
	t.Helper()

	parsedABI := testWrappedAssetABI(t)
	ctorArgs, err := parsedABI.Pack("", initialSupply)
	if err != nil {
		t.Fatalf("pack constructor args: %v", err)
	}
	bytecode, err := hexDecode(testWrappedAssetBytecode)
	if err != nil {
		t.Fatalf("decode bytecode: %v", err)
	}
	constructorPayload := append(append([]byte{}, bytecode...), ctorArgs...)

	deployTx := newHighGasTx(deployer, common.Address{}, 0, big.NewInt(0), constructorPayload)
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
		t.Fatalf("deploy TestWrappedAsset failed: status=%v exception=%v msg=%s", res.Status, res.Exception, res.Exmsg)
	}
	if err := applyFullMvmResultToStateDB(cs, res); err != nil {
		t.Fatalf("apply deploy result to state: %v", err)
	}
	// SmartContractDB.SetCode only stages new bytecode into pendingCode — Code(address) (the
	// reader every later real contract CALL in this test goes through) only ever looks at the
	// committed codeStorage, never pendingCode (only GetCodeByCodeHash checks pendingCode
	// first, per its own doc comment on the "same block" case). Without an explicit Commit()
	// here, a contract deployed and then called within the same test would appear to have no
	// code at all ("Error getting code from storage") even though its codeHash was set
	// correctly on the account. Mirrors the production commit sequence real blocks use
	// (SmartContractDB.Commit(), called at block finalization).
	// LateBindRoots must run before CommitAllStorage (which Commit() calls internally) — it
	// binds each touched contract's real post-write storage root into its AccountState first;
	// skipping it makes CommitAllStorage's own consistency check reject the commit ("storage
	// root mismatch... expected 0x0" — the account still thinks it has no storage at all).
	// Same sequence TestGatewayHandler_OutboundPersistsAcrossChainStateReload documents as the
	// real production commit order (tx_processor.go calls this mid-processing).
	if err := cs.GetSmartContractDB().LateBindRoots(); err != nil {
		t.Fatalf("LateBindRoots after deploy: %v", err)
	}
	if _, err := cs.GetSmartContractDB().Commit(); err != nil {
		t.Fatalf("commit deployed contract code: %v", err)
	}
	if len(res.MapCodeHash) != 1 {
		t.Fatalf("expected exactly 1 new contract code hash from deploy, got %d: %v", len(res.MapCodeHash), res.MapCodeHash)
	}
	var deployedAddr common.Address
	for addrHex := range res.MapCodeHash {
		deployedAddr = common.HexToAddress(addrHex)
	}
	return deployedAddr
}

func hexDecode(s string) ([]byte, error) {
	return common.FromHex("0x" + s), nil
}

// realTokenBalanceOf calls the real deployed contract's balanceOf(address) via the same
// mvm.ExecutionEngine.Execute path production code uses, and decodes the real returned value —
// not a shortcut read of internal Go state.
func realTokenBalanceOf(t *testing.T, cs *blockchain.ChainState, token, account common.Address) *big.Int {
	t.Helper()
	parsedABI := testWrappedAssetABI(t)
	calldata, err := parsedABI.Pack("balanceOf", account)
	if err != nil {
		t.Fatalf("pack balanceOf: %v", err)
	}
	caller := common.HexToAddress("0xCA11CA11CA11CA11CA11CA11CA11CA11CA11CA1")
	callTx := newHighGasTx(caller, token, 0, big.NewInt(0), calldata)
	_, mvmE := createVmProcessorForGateway(context.Background(), cs, callTx, 0)
	lastBlockHeader := *cs.GetcurrentBlockHeader()
	leaderAddr := lastBlockHeader.LeaderAddress()
	if leaderAddr == (common.Address{}) {
		leaderAddr = caller
	}
	res := mvmE.Execute(
		caller.Bytes(), token.Bytes(), calldata, big.NewInt(0),
		callTx.MaxGasPrice(), callTx.MaxGas(),
		lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, uint64(0), mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1, leaderAddr, mvmE.GetKey(), callTx.Hash().Bytes(),
		[]common.Address{}, false, false,
	)
	if res.Status != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("balanceOf(%s) call failed: status=%v exception=%v msg=%s", account.Hex(), res.Status, res.Exception, res.Exmsg)
	}
	out, err := parsedABI.Unpack("balanceOf", res.Return)
	if err != nil {
		t.Fatalf("unpack balanceOf return: %v", err)
	}
	return out[0].(*big.Int)
}

// realTokenApprove calls the real deployed contract's approve(spender, value) — used to give the
// Gateway contract a real allowance before outbound()'s transferFrom, exactly as a real user
// would have to do before bridging a real custom asset.
func realTokenApprove(t *testing.T, cs *blockchain.ChainState, token, owner, spender common.Address, value *big.Int) {
	t.Helper()
	parsedABI := testWrappedAssetABI(t)
	calldata, err := parsedABI.Pack("approve", spender, value)
	if err != nil {
		t.Fatalf("pack approve: %v", err)
	}
	callTx := newHighGasTx(owner, token, 0, big.NewInt(0), calldata)
	_, mvmE := createVmProcessorForGateway(context.Background(), cs, callTx, 0)
	lastBlockHeader := *cs.GetcurrentBlockHeader()
	leaderAddr := lastBlockHeader.LeaderAddress()
	if leaderAddr == (common.Address{}) {
		leaderAddr = owner
	}
	res := mvmE.Execute(
		owner.Bytes(), token.Bytes(), calldata, big.NewInt(0),
		callTx.MaxGasPrice(), callTx.MaxGas(),
		lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, uint64(0), mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1, leaderAddr, mvmE.GetKey(), callTx.Hash().Bytes(),
		[]common.Address{}, false, false,
	)
	if res.Status != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("approve(%s, %s) failed: status=%v exception=%v msg=%s", spender.Hex(), value.String(), res.Status, res.Exception, res.Exmsg)
	}
	if err := applyFullMvmResultToStateDB(cs, res); err != nil {
		t.Fatalf("apply approve result to state: %v", err)
	}
}

// realTokenMint calls the real deployed contract's mint(to, value) via the same
// mvm.ExecutionEngine.Execute path.
func realTokenMint(t *testing.T, cs *blockchain.ChainState, token, caller, to common.Address, value *big.Int) {
	t.Helper()
	parsedABI := testWrappedAssetABI(t)
	calldata, err := parsedABI.Pack("mint", to, value)
	if err != nil {
		t.Fatalf("pack mint: %v", err)
	}
	callTx := newHighGasTx(caller, token, 0, big.NewInt(0), calldata)
	_, mvmE := createVmProcessorForGateway(context.Background(), cs, callTx, 0)
	lastBlockHeader := *cs.GetcurrentBlockHeader()
	leaderAddr := lastBlockHeader.LeaderAddress()
	if leaderAddr == (common.Address{}) {
		leaderAddr = caller
	}
	res := mvmE.Execute(
		caller.Bytes(), token.Bytes(), calldata, big.NewInt(0),
		callTx.MaxGasPrice(), callTx.MaxGas(),
		lastBlockHeader.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, uint64(0), mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1, leaderAddr, mvmE.GetKey(), callTx.Hash().Bytes(),
		[]common.Address{}, false, false,
	)
	if res.Status != pb.RECEIPT_STATUS_RETURNED {
		t.Fatalf("mint(%s, %s) failed: status=%v exception=%v msg=%s", to.Hex(), value.String(), res.Status, res.Exception, res.Exmsg)
	}
	if err := applyFullMvmResultToStateDB(cs, res); err != nil {
		t.Fatalf("apply mint result to state: %v", err)
	}
}

// TestGatewayHandler_CustomAsset_RealTokenTransferSucceeds closes the Task 1.2 test-coverage
// gap flagged in note/cross_chain_task1_native_value_fix_plan.md: unlike
// TestGatewayHandler_CustomAsset_Outbound_ClaimMessage (which only proves the code fails
// gracefully against a non-contract address), this deploys a real ERC-20-shaped contract via
// the real mvm.ExecutionEngine.Deploy path, approves a real allowance, and proves outbound()'s
// real transferFrom() call actually moves real token balance from the sender to the Gateway
// contract — verified by calling the real deployed contract's balanceOf() afterwards, not by
// reading internal Go state.
func TestGatewayHandler_CustomAsset_RealTokenTransferSucceeds(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	sender := common.HexToAddress("0xAAAA1111AAAA1111AAAA1111AAAA1111AAAA1111")
	target := common.HexToAddress("0xBBBB2222BBBB2222BBBB2222BBBB2222BBBB2222")

	assetID := big.NewInt(999)
	homeChainID := uint64(101)
	destChainID := uint64(102)

	// Deploy a REAL canonical token contract on the "home" chain, sender holding the entire
	// initial supply — a real CREATE, not a state shortcut.
	canonicalToken := deployTestWrappedAsset(t, cs, sender, big.NewInt(1_000_000))

	supplyLedger, _ := cross_chain.NewGlobalSupplyLedger(big.NewInt(1000), nil)
	// destChainID must be a known registered chain -- outbound() now rejects an unregistered
	// destination before it ever locks/burns funds (2026-09-05 security fix, see that case's own
	// comment in gateway_handler.go).
	engine := cross_chain.NewGatewayEngine(homeChainID, map[uint64]cross_chain.ChainRegistry{
		destChainID: {
			ChainID: destChainID,
			Epoch:   1,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: make([]byte, 48), Stake: 1000},
			},
		},
	}, supplyLedger)
	engine.AssetRegistry = cross_chain.NewAssetRegistryEngine(engine.ChainRegistry)
	entry := &cross_chain.AssetEntry{
		AssetID:           assetID,
		Active:            true,
		HomeChainID:       homeChainID,
		CanonicalContract: canonicalToken,
		WrappedContracts:  map[uint64]common.Address{destChainID: common.HexToAddress("0xC0DEC0DEC0DEC0DEC0DEC0DEC0DEC0DEC0DEC0DE")},
	}
	engine.AssetRegistry.Assets[assetID.String()] = entry
	engine.AssetRegistry.CirculationBalances[fmt.Sprintf("%s:%d", assetID.String(), homeChainID)] = big.NewInt(1000)
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine: %v", err)
	}

	// Real approve() — a real user bridging a real asset must do this before outbound() can
	// transferFrom() on their behalf.
	realTokenApprove(t, cs, canonicalToken, sender, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(100))

	senderBalBefore := realTokenBalanceOf(t, cs, canonicalToken, sender)
	gatewayBalBefore := realTokenBalanceOf(t, cs, canonicalToken, mt_common.GATEWAY_CONTRACT_ADDRESS)
	if senderBalBefore.Cmp(big.NewInt(1_000_000)) != 0 {
		t.Fatalf("sanity: expected sender's real initial balance 1000000, got %s", senderBalBefore)
	}

	outboundCalldata, err := h.abi.Pack("outbound",
		big.NewInt(int64(destChainID)), target, []byte{}, assetID, big.NewInt(100), big.NewInt(0), big.NewInt(0), uint8(1), false,
		uint64(0), // timeoutTimestamp
	)
	if err != nil {
		t.Fatalf("pack outbound: %v", err)
	}
	tx := newHighGasTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, outboundCalldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if failed {
		t.Fatalf("outbound() with a real token + real allowance should succeed, got: %+v", rcp)
	}

	// The real, defining assertion: read the real deployed contract's state back, via a real
	// EVM call — not Go-side bookkeeping — and prove exactly 100 tokens moved.
	senderBalAfter := realTokenBalanceOf(t, cs, canonicalToken, sender)
	gatewayBalAfter := realTokenBalanceOf(t, cs, canonicalToken, mt_common.GATEWAY_CONTRACT_ADDRESS)

	wantSenderAfter := new(big.Int).Sub(senderBalBefore, big.NewInt(100))
	if senderBalAfter.Cmp(wantSenderAfter) != 0 {
		t.Fatalf("real sender token balance after outbound: got %s, want %s (100 real tokens should have moved)", senderBalAfter, wantSenderAfter)
	}
	wantGatewayAfter := new(big.Int).Add(gatewayBalBefore, big.NewInt(100))
	if gatewayBalAfter.Cmp(wantGatewayAfter) != 0 {
		t.Fatalf("real Gateway contract token balance after outbound: got %s, want %s (real transferFrom should have locked 100 tokens into it)", gatewayBalAfter, wantGatewayAfter)
	}
}

// TestGatewayHandler_CustomAsset_RealTokenMintSucceeds closes the other half of the Task 1.2
// gap: proves claimMessage()'s real mint() call against a real deployed wrapped-token contract
// actually credits the real recipient, verified via a real balanceOf() call.
func TestGatewayHandler_CustomAsset_RealTokenMintSucceeds(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	recipient := common.HexToAddress("0x2222222222222222222222222222222222222222")
	deployer := common.HexToAddress("0x3333333333333333333333333333333333333333")

	assetID := big.NewInt(777)
	homeChainID := uint64(101)
	destChainID := uint64(102)

	// Deploy a REAL wrapped token contract on the "dest" chain with zero initial supply — all
	// balance must come from a real mint() call, exactly like a real wrapped asset.
	wrappedToken := deployTestWrappedAsset(t, cs, deployer, big.NewInt(0))

	engine := cross_chain.NewGatewayEngine(destChainID, map[uint64]cross_chain.ChainRegistry{
		homeChainID: {
			ChainID: homeChainID,
			Epoch:   1,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: make([]byte, 48), Stake: 1000},
			},
		},
	}, nil)
	engine.AssetRegistry = cross_chain.NewAssetRegistryEngine(engine.ChainRegistry)
	entry := &cross_chain.AssetEntry{
		AssetID:           assetID,
		Active:            true,
		HomeChainID:       homeChainID,
		CanonicalContract: common.HexToAddress("0xCAFECAFECAFECAFECAFECAFECAFECAFECAFECAFE"),
		WrappedContracts:  map[uint64]common.Address{destChainID: wrappedToken},
	}
	engine.AssetRegistry.Assets[assetID.String()] = entry
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine: %v", err)
	}

	recipientBalBefore := realTokenBalanceOf(t, cs, wrappedToken, recipient)
	if recipientBalBefore.Sign() != 0 {
		t.Fatalf("sanity: expected recipient's real initial balance 0, got %s", recipientBalBefore)
	}

	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0xdeadbeef"),
		SourceChainID: homeChainID,
		DestChainID:   destChainID,
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        wrappedToken,
		AssetID:       assetID,
		Value:         big.NewInt(250),
		Payload:       recipient.Bytes(), // recipient encoded in Payload, per LockAndBridgeAsset's convention
		Tip:           big.NewInt(0),
		GasFee:        big.NewInt(0),
	}
	leafHash := cross_chain.ComputeMessageLeafHash(msg)
	engine.MessageStatus[msg.MessageID] = cross_chain.MessageStatusPending
	engine.AttestedCommits[fmt.Sprintf("%d:%s:%s", homeChainID, leafHash.Hex(), assetID.String())] = cross_chain.AttestedCommit{
		SourceChainID: homeChainID,
		CommitRoot:    leafHash,
		AssetID:       assetID,
		Epoch:         1,
		FundedAmount:  big.NewInt(250),
		ClaimedAmount: big.NewInt(0),
	}
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine (pending message): %v", err)
	}

	claimCalldata, err := h.abi.Pack("claimMessage",
		msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
		uint64(0), // timeoutTimestamp
		big.NewInt(0), [][32]byte{}, leafHash,
	)
	if err != nil {
		t.Fatalf("pack claimMessage: %v", err)
	}
	claimTx := newHighGasTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if failed {
		t.Fatalf("claimMessage() against a real wrapped token contract should succeed, got: %+v", rcp)
	}

	// The real, defining assertion: read the real deployed contract's state back, via a real
	// EVM call.
	recipientBalAfter := realTokenBalanceOf(t, cs, wrappedToken, recipient)
	if recipientBalAfter.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("real recipient token balance after claimMessage: got %s, want 250 (real mint() should have credited it)", recipientBalAfter)
	}
}

// TestGatewayHandler_CustomAsset_NoCodeToken_Regression verifies the security fix for PR #181:
// When an asset is registered with a token address that has no code deployed (e.g. undeployed address, EOA,
// or revoked EIP-7702), the Gateway MUST NOT treat empty-code execution as success:
// 1. outbound() MUST reject the transaction, preventing minting wrapped tokens on dest chain without locking assets.
// 2. claimMessage() MUST mark the message as Failed (payloadReverted), allowing the sender to refund on source chain.
func TestGatewayHandler_CustomAsset_NoCodeToken_Regression(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	if err != nil {
		t.Fatalf("GetGatewayHandler: %v", err)
	}

	sender := common.HexToAddress("0xAAAA1111AAAA1111AAAA1111AAAA1111AAAA1111")
	recipient := common.HexToAddress("0xBBBB2222BBBB2222BBBB2222BBBB2222BBBB2222")
	undeployedToken := common.HexToAddress("0x9999888877776666555544443333222211110000") // Has NO code

	assetID := big.NewInt(888)
	homeChainID := uint64(101)
	destChainID := uint64(102)

	supplyLedger, _ := cross_chain.NewGlobalSupplyLedger(big.NewInt(1000), nil)
	engine := cross_chain.NewGatewayEngine(homeChainID, map[uint64]cross_chain.ChainRegistry{
		destChainID: {
			ChainID: destChainID,
			Epoch:   1,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: bls.GenerateKeyPair().BytesPublicKey(), Stake: 1000},
			},
		},
	}, supplyLedger)
	engine.AssetRegistry = cross_chain.NewAssetRegistryEngine(engine.ChainRegistry)

	entry := &cross_chain.AssetEntry{
		AssetID:           assetID,
		Active:            true,
		HomeChainID:       homeChainID,
		CanonicalContract: undeployedToken,
		WrappedContracts: map[uint64]common.Address{
			destChainID: undeployedToken,
		},
	}
	engine.AssetRegistry.Assets[assetID.String()] = entry
	engine.AssetRegistry.CirculationBalances[fmt.Sprintf("%s:%d", assetID.String(), homeChainID)] = big.NewInt(1000)
	if err := saveGatewayEngine(cs, engine); err != nil {
		t.Fatalf("saveGatewayEngine failed: %v", err)
	}

	// 1. Outbound on Home Chain: must fail because token contract has no code deployed
	outboundCalldata, err := h.abi.Pack("outbound",
		big.NewInt(int64(destChainID)), recipient, []byte{}, assetID, big.NewInt(100), big.NewInt(0), big.NewInt(0), uint8(1), false,
		uint64(0), // timeoutTimestamp
	)
	if err != nil {
		t.Fatalf("pack outbound: %v", err)
	}
	tx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, outboundCalldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, tx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if !failed {
		t.Fatalf("expected outbound custom asset against undeployed token to fail, but it succeeded")
	}
	if !strings.Contains(string(rcp.Return()), "has no code deployed") {
		t.Fatalf("expected error mentioning 'has no code deployed', got: %s", string(rcp.Return()))
	}

	engineAfterOutbound, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	circAfter := engineAfterOutbound.AssetRegistry.CirculationBalances[fmt.Sprintf("%s:%d", assetID.String(), homeChainID)]
	assert.Equal(t, "1000", circAfter.String(), "no-code outbound failure must not change circulation balance")
	assert.Empty(t, engineAfterOutbound.PendingOutboundMessages[destChainID], "no-code outbound failure must not enqueue pending outbound message")

	// 2. ClaimMessage on Dest Chain: must finalize message as Failed (not Success) so it can be refunded
	engineDest := cross_chain.NewGatewayEngine(destChainID, map[uint64]cross_chain.ChainRegistry{
		homeChainID: {
			ChainID: homeChainID,
			Epoch:   1,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: bls.GenerateKeyPair().BytesPublicKey(), Stake: 1000},
			},
		},
	}, nil)
	engineDest.AssetRegistry = engine.AssetRegistry
	if err := saveGatewayEngine(cs, engineDest); err != nil {
		t.Fatalf("saveGatewayEngine for dest chain failed: %v", err)
	}

	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0x9999aaaa"),
		SourceChainID: homeChainID,
		DestChainID:   destChainID,
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        undeployedToken,
		AssetID:       assetID,
		Value:         big.NewInt(100),
		Payload:       recipient.Bytes(),
		Tip:           big.NewInt(0),
		GasFee:        big.NewInt(0),
	}
	leafHash := cross_chain.ComputeMessageLeafHash(msg)
	engineDest.MessageStatus[msg.MessageID] = cross_chain.MessageStatusPending
	engineDest.AttestedCommits[fmt.Sprintf("%d:%s:%s", homeChainID, leafHash.Hex(), assetID.String())] = cross_chain.AttestedCommit{
		SourceChainID: homeChainID,
		CommitRoot:    leafHash,
		AssetID:       assetID,
		Epoch:         1,
		FundedAmount:  big.NewInt(100),
		ClaimedAmount: big.NewInt(0),
	}
	if err := saveGatewayEngine(cs, engineDest); err != nil {
		t.Fatalf("saveGatewayEngine for pending message failed: %v", err)
	}

	claimCalldata, err := h.abi.Pack("claimMessage",
		msg.MessageID, big.NewInt(int64(msg.SourceChainID)), big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
		uint64(0),
		big.NewInt(0), [][32]byte{}, leafHash,
	)
	if err != nil {
		t.Fatalf("pack claimMessage: %v", err)
	}
	claimTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, claimCalldata))
	_, _, failedClaim := h.HandleTransaction(context.Background(), cs, claimTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	if failedClaim {
		t.Fatalf("claimMessage transaction should execute and finalize Failed (not hard-revert the tx)")
	}

	engineAfterClaim, err := loadGatewayEngine(cs)
	if err != nil {
		t.Fatalf("loadGatewayEngine after claim failed: %v", err)
	}
	if status := engineAfterClaim.GetMessageStatus(msg.MessageID); status != cross_chain.MessageStatusFailed {
		t.Fatalf("expected message status Failed (2) after claimMessage against undeployed token, got: %d", status)
	}
}

func setDelegationDesignator(cs *blockchain.ChainState, authority, delegate common.Address) {
	designator := e_types.AddressToDelegation(delegate)
	codeHash := crypto.Keccak256Hash(designator)
	cs.GetSmartContractDB().SetCode(authority, codeHash, designator)
	cs.GetAccountStateDB().SetCodeHash(authority, codeHash)
	_ = cs.GetSmartContractDB().LateBindRoots()
	_, _ = cs.GetSmartContractDB().Commit()
}

func revokeDelegation(cs *blockchain.ChainState, authority common.Address) {
	cs.GetAccountStateDB().SetCodeHash(authority, common.Hash{})
}

// TestGatewayHandler_IsContractCall_DelegationAndRevocation tests isContractCall against:
// - Undeployed EOA (false)
// - Deployed contract (true)
// - 1-hop valid delegation to deployed contract (true)
// - Revocation: CodeHash cleared back to zero (false)
// - 1-hop delegation to undeployed EOA (false)
// - Self-delegation: A -> A (false, no infinite recursion/stack overflow)
// - Chained delegation: A -> B -> RealContract (false, exactly 1 hop allowed per EIP-7702)
// - Circular delegation: E -> F -> E (false, no infinite recursion/stack overflow)
func TestGatewayHandler_IsContractCall_DelegationAndRevocation(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)

	undeployed := common.HexToAddress("0x1111000000000000000000000000000000000001")
	assert.False(t, isContractCall(cs, undeployed), "pure undeployed EOA must not have code")

	deployer := common.HexToAddress("0xDEAD000000000000000000000000000000000001")
	tokenAddr := deployTestWrappedAsset(t, cs, deployer, big.NewInt(1_000_000))
	assert.True(t, isContractCall(cs, tokenAddr), "deployed token must have code")

	// 1-hop valid delegation
	auth1 := common.HexToAddress("0x2222000000000000000000000000000000000002")
	setDelegationDesignator(cs, auth1, tokenAddr)
	assert.True(t, isContractCall(cs, auth1), "1-hop delegation to deployed contract must return true")

	// Revocation
	revokeDelegation(cs, auth1)
	assert.False(t, isContractCall(cs, auth1), "revoked delegation must return false")

	// 1-hop delegation to undeployed address
	auth2 := common.HexToAddress("0x3333000000000000000000000000000000000003")
	setDelegationDesignator(cs, auth2, undeployed)
	assert.False(t, isContractCall(cs, auth2), "delegation to undeployed EOA must return false")

	// Self-delegation (A -> A)
	authSelf := common.HexToAddress("0x4444000000000000000000000000000000000004")
	setDelegationDesignator(cs, authSelf, authSelf)
	assert.False(t, isContractCall(cs, authSelf), "self-delegation must return false without hanging or stack overflow")

	// Chained delegation (A -> B -> RealContract)
	authChainedA := common.HexToAddress("0x5555000000000000000000000000000000000005")
	authChainedB := common.HexToAddress("0x6666000000000000000000000000000000000006")
	setDelegationDesignator(cs, authChainedB, tokenAddr)
	setDelegationDesignator(cs, authChainedA, authChainedB)
	assert.True(t, isContractCall(cs, authChainedB), "B is 1 hop to token, must return true")
	assert.False(t, isContractCall(cs, authChainedA), "A is 2 hops (chained), must return false per EIP-7702")

	// Circular delegation (E -> F -> E)
	authCircE := common.HexToAddress("0x7777000000000000000000000000000000000007")
	authCircF := common.HexToAddress("0x8888000000000000000000000000000000000008")
	setDelegationDesignator(cs, authCircE, authCircF)
	setDelegationDesignator(cs, authCircF, authCircE)
	assert.False(t, isContractCall(cs, authCircE), "circular delegation must return false without stack overflow")
	assert.False(t, isContractCall(cs, authCircF), "circular delegation must return false without stack overflow")
}

// TestGatewayHandler_CustomAsset_Delegation_OutboundAndClaim verifies that:
// 1. 1-hop valid delegation to real contract succeeds on outbound() and claimMessage()
//   - Execution operates on the storage of the delegated account (token1Hop).
//   - User balance decreases, Gateway vault balance increases on outbound().
//   - Destination recipient balance increases on claimMessage(), message status is Success.
//
// 2. Chained delegation (2 hops) is rejected on outbound() and finalized as Failed on claimMessage()
//   - Outbound failure preserves circulation balance and enqueues no messages.
//   - ClaimMessage failure marks the message as Failed (not hard-revert).
func TestGatewayHandler_CustomAsset_Delegation_OutboundAndClaim(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	deployer := common.HexToAddress("0xDEAD000000000000000000000000000000000001")
	user := common.HexToAddress("0xAAAA1111AAAA1111AAAA1111AAAA1111AAAA1111")
	recipient := common.HexToAddress("0xBBBB2222BBBB2222BBBB2222BBBB2222BBBB2222")

	homeChainID := uint64(101)
	destChainID := uint64(102)

	// Deploy real underlying contracts on home and dest chains
	realTokenHome := deployTestWrappedAsset(t, cs, deployer, big.NewInt(0))
	realTokenDest := deployTestWrappedAsset(t, cs, deployer, big.NewInt(0))

	// Setup 1-hop delegated token addresses
	token1HopHome := common.HexToAddress("0x1111111111111111111111111111111111111111")
	setDelegationDesignator(cs, token1HopHome, realTokenHome)

	token1HopDest := common.HexToAddress("0x2222222222222222222222222222222222222222")
	setDelegationDesignator(cs, token1HopDest, realTokenDest)

	// Setup 2-hop chained delegated token address (tokenChained -> token1HopHome -> realTokenHome)
	tokenChained := common.HexToAddress("0x3333333333333333333333333333333333333333")
	setDelegationDesignator(cs, tokenChained, token1HopHome)

	// Mint tokens to user directly on token1HopHome (execution uses storage of token1HopHome)
	realTokenMint(t, cs, token1HopHome, deployer, user, big.NewInt(1000))
	require.Equal(t, "1000", realTokenBalanceOf(t, cs, token1HopHome, user).String())
	require.Equal(t, "0", realTokenBalanceOf(t, cs, token1HopHome, mt_common.GATEWAY_CONTRACT_ADDRESS).String())

	// Approve Gateway to transfer tokens from user on token1HopHome
	realTokenApprove(t, cs, token1HopHome, user, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(100))

	supplyLedger, _ := cross_chain.NewGlobalSupplyLedger(big.NewInt(1_000_000), nil)
	engine := cross_chain.NewGatewayEngine(homeChainID, map[uint64]cross_chain.ChainRegistry{
		destChainID: {
			ChainID: destChainID,
			Epoch:   1,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: bls.GenerateKeyPair().BytesPublicKey(), Stake: 1000},
			},
		},
	}, supplyLedger)
	engine.AssetRegistry = cross_chain.NewAssetRegistryEngine(engine.ChainRegistry)

	// Register 1-hop delegated asset
	assetID1Hop := big.NewInt(1001)
	engine.AssetRegistry.Assets[assetID1Hop.String()] = &cross_chain.AssetEntry{
		AssetID:           assetID1Hop,
		Active:            true,
		HomeChainID:       homeChainID,
		CanonicalContract: token1HopHome,
		WrappedContracts: map[uint64]common.Address{
			destChainID: token1HopDest,
		},
	}
	engine.AssetRegistry.CirculationBalances[fmt.Sprintf("%s:%d", assetID1Hop.String(), homeChainID)] = big.NewInt(1000)

	// Register 2-hop chained delegated asset
	assetIDChained := big.NewInt(1002)
	engine.AssetRegistry.Assets[assetIDChained.String()] = &cross_chain.AssetEntry{
		AssetID:           assetIDChained,
		Active:            true,
		HomeChainID:       homeChainID,
		CanonicalContract: tokenChained,
		WrappedContracts: map[uint64]common.Address{
			destChainID: tokenChained,
		},
	}
	circBeforeChained := big.NewInt(1000)
	engine.AssetRegistry.CirculationBalances[fmt.Sprintf("%s:%d", assetIDChained.String(), homeChainID)] = circBeforeChained

	require.NoError(t, saveGatewayEngine(cs, engine))

	// -------------------------------------------------------------
	// 1. Valid 1-Hop Delegation: outbound() and claimMessage() MUST SUCCEED
	// -------------------------------------------------------------
	outbound1HopCalldata, err := h.abi.Pack("outbound",
		big.NewInt(int64(destChainID)), recipient, []byte{}, assetID1Hop, big.NewInt(100), big.NewInt(0), big.NewInt(0), uint8(1), false,
		uint64(0),
	)
	require.NoError(t, err)
	tx1Hop := newHighGasTx(user, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, outbound1HopCalldata))
	rcp1Hop, _, failed1Hop := h.HandleTransaction(context.Background(), cs, tx1Hop, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	require.False(t, failed1Hop, "outbound with 1-hop delegation token must succeed, return: %s", string(rcp1Hop.Return()))

	userBalAfter := realTokenBalanceOf(t, cs, token1HopHome, user)
	assert.Equal(t, "900", userBalAfter.String(), "user balance on 1-hop token must decrease by 100")
	gatewayBalAfter := realTokenBalanceOf(t, cs, token1HopHome, mt_common.GATEWAY_CONTRACT_ADDRESS)
	assert.Equal(t, "100", gatewayBalAfter.String(), "gateway balance on 1-hop token must increase by 100")

	// Dest chain claimMessage for 1-hop delegation
	engineDest := cross_chain.NewGatewayEngine(destChainID, map[uint64]cross_chain.ChainRegistry{
		homeChainID: {
			ChainID: homeChainID,
			Epoch:   1,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: bls.GenerateKeyPair().BytesPublicKey(), Stake: 1000},
			},
		},
	}, nil)
	engineDest.AssetRegistry = engine.AssetRegistry

	recipientBalBefore := realTokenBalanceOf(t, cs, token1HopDest, recipient)
	require.Equal(t, "0", recipientBalBefore.String(), "recipient balance on dest token must be 0 before claim")

	msg1Hop := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0x1111aaaa"),
		SourceChainID: homeChainID,
		DestChainID:   destChainID,
		Sequence:      1,
		HopCount:      1,
		Sender:        user,
		Target:        token1HopDest,
		AssetID:       assetID1Hop,
		Value:         big.NewInt(100),
		Payload:       recipient.Bytes(),
		Tip:           big.NewInt(0),
		GasFee:        big.NewInt(0),
	}
	leaf1Hop := cross_chain.ComputeMessageLeafHash(msg1Hop)
	engineDest.MessageStatus[msg1Hop.MessageID] = cross_chain.MessageStatusPending
	engineDest.AttestedCommits[fmt.Sprintf("%d:%s:%s", homeChainID, leaf1Hop.Hex(), assetID1Hop.String())] = cross_chain.AttestedCommit{
		SourceChainID: homeChainID,
		CommitRoot:    leaf1Hop,
		AssetID:       assetID1Hop,
		Epoch:         1,
		FundedAmount:  big.NewInt(100),
		ClaimedAmount: big.NewInt(0),
	}
	require.NoError(t, saveGatewayEngine(cs, engineDest))

	claim1HopCalldata, err := h.abi.Pack("claimMessage",
		msg1Hop.MessageID, big.NewInt(int64(msg1Hop.SourceChainID)), big.NewInt(int64(msg1Hop.DestChainID)),
		big.NewInt(int64(msg1Hop.Sequence)), msg1Hop.HopCount, msg1Hop.Sender, msg1Hop.Target,
		msg1Hop.AssetID, msg1Hop.Value, msg1Hop.Payload, msg1Hop.Tip, msg1Hop.GasFee, msg1Hop.Ordered,
		uint64(0), big.NewInt(0), [][32]byte{}, leaf1Hop,
	)
	require.NoError(t, err)
	claim1HopTx := newHighGasTx(user, mt_common.GATEWAY_CONTRACT_ADDRESS, 1, big.NewInt(0), marshalCallData(t, claim1HopCalldata))
	_, _, claim1HopFailed := h.HandleTransaction(context.Background(), cs, claim1HopTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.False(t, claim1HopFailed, "claimMessage with 1-hop delegation token must succeed")

	engineAfterClaim1Hop, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	assert.Equal(t, cross_chain.MessageStatusSuccess, engineAfterClaim1Hop.GetMessageStatus(msg1Hop.MessageID),
		"claimMessage with 1-hop delegation token must be finalized as Success")

	recipientBalAfter := realTokenBalanceOf(t, cs, token1HopDest, recipient)
	assert.Equal(t, "100", recipientBalAfter.String(), "recipient token balance must increase by 100 via 1-hop delegation mint")

	// -------------------------------------------------------------
	// 2. Chained Delegation (2 hops): MUST FAIL on outbound() and claimMessage()
	// -------------------------------------------------------------
	require.NoError(t, saveGatewayEngine(cs, engine))

	outboundChainedCalldata, err := h.abi.Pack("outbound",
		big.NewInt(int64(destChainID)), recipient, []byte{}, assetIDChained, big.NewInt(100), big.NewInt(0), big.NewInt(0), uint8(1), false,
		uint64(0),
	)
	require.NoError(t, err)
	txChained := newHighGasTx(user, mt_common.GATEWAY_CONTRACT_ADDRESS, 2, big.NewInt(0), marshalCallData(t, outboundChainedCalldata))
	rcpChained, _, failedChained := h.HandleTransaction(context.Background(), cs, txChained, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.True(t, failedChained, "outbound with chained delegation token must fail")
	assert.Contains(t, string(rcpChained.Return()), "has no code deployed")

	engineAfterOutboundChained, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	circAfterChained := engineAfterOutboundChained.AssetRegistry.CirculationBalances[fmt.Sprintf("%s:%d", assetIDChained.String(), homeChainID)]
	assert.Equal(t, circBeforeChained.String(), circAfterChained.String(), "chained outbound failure must not change circulation balance")
	assert.Empty(t, engineAfterOutboundChained.PendingOutboundMessages[destChainID], "chained outbound failure must not enqueue pending outbound message")

	// ClaimMessage with chained token MUST finalize as Failed (payloadReverted)
	msgChained := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0xcccc1111"),
		SourceChainID: homeChainID,
		DestChainID:   destChainID,
		Sequence:      2,
		HopCount:      1,
		Sender:        user,
		Target:        tokenChained,
		AssetID:       assetIDChained,
		Value:         big.NewInt(100),
		Payload:       recipient.Bytes(),
		Tip:           big.NewInt(0),
		GasFee:        big.NewInt(0),
	}
	leafChained := cross_chain.ComputeMessageLeafHash(msgChained)
	engineDest.MessageStatus[msgChained.MessageID] = cross_chain.MessageStatusPending
	engineDest.AttestedCommits[fmt.Sprintf("%d:%s:%s", homeChainID, leafChained.Hex(), assetIDChained.String())] = cross_chain.AttestedCommit{
		SourceChainID: homeChainID,
		CommitRoot:    leafChained,
		AssetID:       assetIDChained,
		Epoch:         1,
		FundedAmount:  big.NewInt(100),
		ClaimedAmount: big.NewInt(0),
	}
	require.NoError(t, saveGatewayEngine(cs, engineDest))

	claimChainedCalldata, err := h.abi.Pack("claimMessage",
		msgChained.MessageID, big.NewInt(int64(msgChained.SourceChainID)), big.NewInt(int64(msgChained.DestChainID)),
		big.NewInt(int64(msgChained.Sequence)), msgChained.HopCount, msgChained.Sender, msgChained.Target,
		msgChained.AssetID, msgChained.Value, msgChained.Payload, msgChained.Tip, msgChained.GasFee, msgChained.Ordered,
		uint64(0), big.NewInt(0), [][32]byte{}, leafChained,
	)
	require.NoError(t, err)
	claimChainedTx := newHighGasTx(user, mt_common.GATEWAY_CONTRACT_ADDRESS, 3, big.NewInt(0), marshalCallData(t, claimChainedCalldata))
	_, _, claimChainedFailed := h.HandleTransaction(context.Background(), cs, claimChainedTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.False(t, claimChainedFailed, "claimMessage must execute and finalize Failed (not hard-revert)")

	engineAfterClaimChained, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	assert.Equal(t, cross_chain.MessageStatusFailed, engineAfterClaimChained.GetMessageStatus(msgChained.MessageID),
		"claimMessage against chained delegation token must be finalized as Failed")
}

// TestGatewayHandler_CustomAsset_Refund_And_VerifyAndExecute_NoCode verifies that:
// 1. refund() fails closed with a clear error if the custom asset token contract has no code.
// 2. verifyAndExecute() finalizes the message as Failed (not hard-revert) if the target contract has no code.
func TestGatewayHandler_CustomAsset_Refund_And_VerifyAndExecute_NoCode(t *testing.T) {
	cs, _, _, _ := newPersistentTestChainState(t)
	h, err := GetGatewayHandler()
	require.NoError(t, err)

	sender := common.HexToAddress("0xAAAA1111AAAA1111AAAA1111AAAA1111AAAA1111")
	recipient := common.HexToAddress("0xBBBB2222BBBB2222BBBB2222BBBB2222BBBB2222")
	undeployedToken := common.HexToAddress("0x9999888877776666555544443333222211110000") // Has NO code

	assetID := big.NewInt(777)
	homeChainID := uint64(101)
	destChainID := uint64(102)

	supplyLedger, _ := cross_chain.NewGlobalSupplyLedger(big.NewInt(1000), nil)
	engine := cross_chain.NewGatewayEngine(homeChainID, map[uint64]cross_chain.ChainRegistry{
		destChainID: {
			ChainID: destChainID,
			Epoch:   1,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: bls.GenerateKeyPair().BytesPublicKey(), Stake: 1000},
			},
		},
	}, supplyLedger)
	engine.AssetRegistry = cross_chain.NewAssetRegistryEngine(engine.ChainRegistry)

	entry := &cross_chain.AssetEntry{
		AssetID:           assetID,
		Active:            true,
		HomeChainID:       homeChainID,
		CanonicalContract: undeployedToken,
		WrappedContracts: map[uint64]common.Address{
			destChainID: undeployedToken,
		},
	}
	engine.AssetRegistry.Assets[assetID.String()] = entry
	engine.AssetRegistry.CirculationBalances[fmt.Sprintf("%s:%d", assetID.String(), homeChainID)] = big.NewInt(1000)
	require.NoError(t, saveGatewayEngine(cs, engine))

	// 1. Test refund against undeployed token
	msg := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0xfeedbeef"),
		SourceChainID: homeChainID,
		DestChainID:   destChainID,
		Sequence:      1,
		HopCount:      1,
		Sender:        sender,
		Target:        undeployedToken,
		AssetID:       assetID,
		Value:         big.NewInt(100),
		Payload:       recipient.Bytes(),
		Tip:           big.NewInt(0),
		GasFee:        big.NewInt(0),
	}
	commitRoot, commitLayers, _, _, errTree := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msg})
	require.NoError(t, errTree)
	proof := cross_chain.GetMerkleProof(commitLayers, 0)

	engine.CommittedBatches[commitRoot] = cross_chain.CommittedOutboundBatch{Messages: []cross_chain.CrossChainMessage{msg}, Epoch: 1}
	engine.MessageStatus[msg.MessageID] = cross_chain.MessageStatusPending

	kpDest := bls.GenerateKeyPair()
	engine.ChainRegistry[destChainID] = cross_chain.ChainRegistry{
		ChainID:         destChainID,
		Epoch:           1,
		QuorumThreshold: 6667,
		Committee: []cross_chain.ValidatorEntry{
			{PubkeyBLS: kpDest.BytesPublicKey(), Stake: 1000},
		},
	}
	require.NoError(t, saveGatewayEngine(cs, engine))

	failMsg := cross_chain.ComputeMessageFailureAttestMessage(msg.MessageID, destChainID)
	failSig := bls.Sign(kpDest.PrivateKey(), failMsg)

	refundCalldata, err := h.abi.Pack("refund",
		msg.MessageID,
		big.NewInt(int64(msg.SourceChainID)),
		big.NewInt(int64(msg.DestChainID)),
		big.NewInt(int64(msg.Sequence)),
		msg.HopCount,
		msg.Sender,
		msg.Target,
		msg.AssetID,
		msg.Value,
		msg.Payload,
		msg.Tip,
		msg.GasFee,
		msg.Ordered,
		new(big.Int).SetUint64(proof.LeafIndex),
		hashesToBytes32(proof.Siblings),
		commitRoot,
		uint64(1), // failCertEpoch
		failSig.Bytes(),
		[]byte{0x01}, // bitmap
	)
	require.NoError(t, err)

	refundTx := newTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 0, big.NewInt(0), marshalCallData(t, refundCalldata))
	rcp, _, failed := h.HandleTransaction(context.Background(), cs, refundTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.True(t, failed, "refund against undeployed token must fail")
	assert.Contains(t, string(rcp.Return()), "refund custom asset restoration failed: token contract")
	assert.Contains(t, string(rcp.Return()), "has no code deployed")

	engineAfterRefund, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	assert.Equal(t, cross_chain.MessageStatusPending, engineAfterRefund.GetMessageStatus(msg.MessageID),
		"refund against no-code token must remain Pending to allow retry after code is deployed")
	assert.NotEqual(t, cross_chain.MessageStatusRefunded, engineAfterRefund.GetMessageStatus(msg.MessageID),
		"refund failure must NOT mark message as refunded")

	// 2. Test verifyAndExecute against undeployed token
	engineDest := cross_chain.NewGatewayEngine(destChainID, map[uint64]cross_chain.ChainRegistry{
		homeChainID: {
			ChainID:         homeChainID,
			Epoch:           1,
			QuorumThreshold: 6667,
			Committee: []cross_chain.ValidatorEntry{
				{PubkeyBLS: bls.GenerateKeyPair().BytesPublicKey(), Stake: 1000},
			},
		},
	}, nil)
	engineDest.AssetRegistry = engine.AssetRegistry

	msgVerify := cross_chain.CrossChainMessage{
		MessageID:     common.HexToHash("0xbeefcafe"),
		SourceChainID: homeChainID,
		DestChainID:   destChainID,
		Sequence:      2,
		HopCount:      1,
		Sender:        sender,
		Target:        undeployedToken,
		AssetID:       assetID,
		Value:         big.NewInt(100),
		Payload:       recipient.Bytes(),
		Tip:           big.NewInt(0),
		GasFee:        big.NewInt(0),
	}
	commitRootVerify, commitLayersVerify, _, aggIndexVerify, errTreeVerify := cross_chain.BuildCommitTree([]cross_chain.CrossChainMessage{msgVerify})
	require.NoError(t, errTreeVerify)
	messageProofVerify := cross_chain.GetMerkleProof(commitLayersVerify, 0)
	aggregateProofVerify := cross_chain.GetMerkleProof(commitLayersVerify, aggIndexVerify[assetID.String()])

	kpHome := bls.GenerateKeyPair()
	engineDest.ChainRegistry[homeChainID] = cross_chain.ChainRegistry{
		ChainID:         homeChainID,
		Epoch:           1,
		QuorumThreshold: 6667,
		Committee: []cross_chain.ValidatorEntry{
			{PubkeyBLS: kpHome.BytesPublicKey(), Stake: 1000},
		},
	}
	require.NoError(t, saveGatewayEngine(cs, engineDest))

	epoch := uint64(1)
	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRootVerify.Bytes()...)
	sigVerify := bls.Sign(kpHome.PrivateKey(), commitMsg)

	verifyCalldata, err := h.abi.Pack("verifyAndExecute",
		msgVerify.MessageID, big.NewInt(int64(msgVerify.SourceChainID)), big.NewInt(int64(msgVerify.DestChainID)),
		big.NewInt(int64(msgVerify.Sequence)), msgVerify.HopCount, msgVerify.Sender, msgVerify.Target,
		msgVerify.AssetID, msgVerify.Value, msgVerify.Payload, msgVerify.Tip, msgVerify.GasFee, msgVerify.Ordered,
		new(big.Int).SetUint64(aggregateProofVerify.LeafIndex), hashesToBytes32(aggregateProofVerify.Siblings),
		new(big.Int).SetUint64(messageProofVerify.LeafIndex), hashesToBytes32(messageProofVerify.Siblings),
		commitRootVerify, epoch, sigVerify.Bytes(), []byte{0x01},
	)
	require.NoError(t, err)

	verifyTx := newHighGasTx(sender, mt_common.GATEWAY_CONTRACT_ADDRESS, 1, big.NewInt(0), marshalCallData(t, verifyCalldata))
	_, _, failedVerify := h.HandleTransaction(context.Background(), cs, verifyTx, mt_common.GATEWAY_CONTRACT_ADDRESS, false, 0)
	assert.False(t, failedVerify, "verifyAndExecute must execute and finalize Failed (not hard-revert the tx)")

	engineAfterVerify, err := loadGatewayEngine(cs)
	require.NoError(t, err)
	assert.Equal(t, cross_chain.MessageStatusFailed, engineAfterVerify.GetMessageStatus(msgVerify.MessageID),
		"verifyAndExecute against undeployed token must finalize message as Failed")
}
