package tx_processor

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
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
	if err := cs.GetSmartContractDB().Commit(); err != nil {
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
	engine := cross_chain.NewGatewayEngine(homeChainID, map[uint64]cross_chain.ChainRegistry{}, supplyLedger)
	engine.AssetRegistry = cross_chain.NewAssetRegistryEngine(engine.ChainRegistry, nil)
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
	engine.AssetRegistry = cross_chain.NewAssetRegistryEngine(engine.ChainRegistry, nil)
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
