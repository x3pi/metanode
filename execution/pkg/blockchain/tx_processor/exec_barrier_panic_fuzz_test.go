package tx_processor

import (
	"context"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"reflect"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Panic fuzz for the block-execution path. A panic here is not a failed tx: nothing on this path recovers, so it takes the
// node down, and every validator executing the same block goes down with it (and again on replay). The generator packs
// random arguments for EVERY method of the validator and gateway ABIs (edge-biased: zero, one, 2^k, 2^256-1, empty and long
// strings), mutates valid calldata byte-wise, and sends raw garbage to the validator contract, gateway, parent-chain
// gateway, rollup system and account-setting addresses, in blocks of several txs from the same pre-registered senders so
// that sequences such as deregister -> delegate -> register hit tombstones and half-updated state. Any panic fails the test
// and is reported with the repo frame that raised it.

func edgeBig(r *rand.Rand, bits uint) *big.Int {
	switch r.Intn(7) {
	case 0:
		return big.NewInt(0)
	case 1:
		return big.NewInt(1)
	case 2:
		return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), bits), big.NewInt(1)) // max
	case 3:
		return new(big.Int).Lsh(big.NewInt(1), uint(r.Intn(int(bits))))
	case 4:
		return big.NewInt(int64(r.Intn(10_000)))
	case 5:
		return big.NewInt(int64(1000 + r.Intn(5_000_000_000)))
	default:
		b := make([]byte, (bits+7)/8)
		r.Read(b)
		v := new(big.Int).SetBytes(b)
		return v.Mod(v, new(big.Int).Lsh(big.NewInt(1), bits))
	}
}

func randString(r *rand.Rand) string {
	switch r.Intn(4) {
	case 0:
		return ""
	case 1:
		return "x"
	case 2:
		return strings.Repeat("A", 1+r.Intn(400))
	default:
		b := make([]byte, r.Intn(40))
		r.Read(b)
		return string(b)
	}
}

// fillABIValue sets v (addressable, of t.GetType()) to a random value for the ABI type t.
func fillABIValue(r *rand.Rand, v reflect.Value, t abi.Type, users []common.Address) {
	switch t.T {
	case abi.UintTy:
		switch v.Kind() {
		case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			x := edgeBig(r, uint(t.Size))
			v.SetUint(x.Uint64())
		default:
			v.Set(reflect.ValueOf(edgeBig(r, uint(t.Size))))
		}
	case abi.IntTy:
		switch v.Kind() {
		case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			x := edgeBig(r, uint(t.Size)-1)
			v.SetInt(x.Int64())
		default:
			x := edgeBig(r, uint(t.Size)-1)
			if r.Intn(3) == 0 {
				x.Neg(x)
			}
			v.Set(reflect.ValueOf(x))
		}
	case abi.BoolTy:
		v.SetBool(r.Intn(2) == 0)
	case abi.StringTy:
		v.SetString(randString(r))
	case abi.AddressTy:
		var a common.Address
		switch r.Intn(4) {
		case 0:
			a = common.Address{}
		case 1:
			a = users[r.Intn(len(users))]
		default:
			r.Read(a[:])
		}
		v.Set(reflect.ValueOf(a))
	case abi.BytesTy:
		b := make([]byte, r.Intn(70))
		r.Read(b)
		v.SetBytes(b)
	case abi.FixedBytesTy:
		for i := 0; i < v.Len(); i++ {
			v.Index(i).SetUint(uint64(r.Intn(256)))
		}
	case abi.SliceTy:
		n := r.Intn(4)
		s := reflect.MakeSlice(v.Type(), n, n)
		for i := 0; i < n; i++ {
			fillABIValue(r, s.Index(i), *t.Elem, users)
		}
		v.Set(s)
	case abi.ArrayTy:
		for i := 0; i < v.Len(); i++ {
			fillABIValue(r, v.Index(i), *t.Elem, users)
		}
	case abi.TupleTy:
		for i, el := range t.TupleElems {
			fillABIValue(r, v.Field(i), *el, users)
		}
	}
}

func randomMethodCall(r *rand.Rand, a abi.ABI, users []common.Address) []byte {
	names := make([]string, 0, len(a.Methods))
	for n := range a.Methods {
		names = append(names, n)
	}
	sort.Strings(names)
	m := a.Methods[names[r.Intn(len(names))]]
	args := make([]interface{}, len(m.Inputs))
	for i, in := range m.Inputs {
		v := reflect.New(in.Type.GetType()).Elem()
		fillABIValue(r, v, in.Type, users)
		args[i] = v.Interface()
	}
	data, err := a.Pack(m.Name, args...)
	if err != nil {
		return nil
	}
	return data
}

func mutateBytes(r *rand.Rand, in []byte) []byte {
	out := append([]byte{}, in...)
	switch r.Intn(5) {
	case 0: // truncate
		if len(out) > 0 {
			out = out[:r.Intn(len(out))]
		}
	case 1: // flip bytes
		for i, k := 0, 1+r.Intn(4); i < k && len(out) > 0; i++ {
			out[r.Intn(len(out))] ^= byte(1 << uint(r.Intn(8)))
		}
	case 2: // append garbage
		g := make([]byte, 1+r.Intn(70))
		r.Read(g)
		out = append(out, g...)
	case 3: // keep selector, randomise the body
		if len(out) >= 4 {
			g := make([]byte, r.Intn(200))
			r.Read(g)
			out = append(out[:4], g...)
		}
	default: // double
		out = append(out, out...)
	}
	return out
}

// validatorOp packs a validator-contract call with argument values the handler accepts, so that long valid sequences
// (register / deregister / delegate / undelegate / withdraw ...) reach the stake DB instead of failing at argument checks.
func validatorOp(r *rand.Rand, vh *ValidatorHandler, env barrierEnv, from common.Address) ([]byte, *big.Int) {
	target := env.nodeKP.Address()
	if r.Intn(3) != 0 {
		target = env.evm[r.Intn(len(env.evm))].addr
	}
	amount := big.NewInt(0)
	var in []byte
	var err error
	switch r.Intn(8) {
	case 0:
		in, err = vh.abi.Pack("registerValidator", randString(r), "w", "n", "name", "d", "web", "img", uint64(r.Intn(10001)),
			big.NewInt(int64(r.Intn(3))), "netkey", "host", "authkey-"+from.Hex()[34:], "protokey")
	case 1:
		in, err = vh.abi.Pack("deregisterValidator")
	case 2:
		in, err = vh.abi.Pack("setCommissionRate", uint64(r.Intn(10001)))
	case 3:
		in, err = vh.abi.Pack("updateValidatorInfo", randString(r), randString(r), randString(r), randString(r))
	case 4:
		in, err = vh.abi.Pack("delegate", target)
		amount = big.NewInt(int64(900 + r.Intn(5_000_000)))
	case 5:
		in, err = vh.abi.Pack("undelegate", target, big.NewInt(int64(r.Intn(4_000_000))))
	case 6:
		in, err = vh.abi.Pack("withdrawReward", target)
	default:
		in, err = vh.abi.Pack("distributeRewards", target, big.NewInt(int64(r.Intn(1_000_000))))
	}
	if err != nil {
		return nil, amount
	}
	return in, amount
}

type panicSite struct {
	value string
	frame string
	count int
	seed  int64
	txs   string
}

// repoFrame returns the first stack frame inside the meta-node module that is not this test file or the panic machinery.
func repoFrame(stack string) string {
	lines := strings.Split(stack, "\n")
	for i, l := range lines {
		if strings.Contains(l, "meta-node/pkg/") && !strings.Contains(l, "panic(") && i+1 < len(lines) && !strings.Contains(lines[i+1], "_test.go") {
			return strings.TrimSpace(l) + " @ " + strings.TrimSpace(lines[i+1])
		}
	}
	return "unknown"
}

func TestBarrierHandlers_NoPanicOnRandomAndMutatedCalldata(t *testing.T) {
	blocks := 700
	if v := os.Getenv("BARRIER_FUZZ_BLOCKS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			blocks = n // longer hunts: BARRIER_FUZZ_BLOCKS=20000 go test -run TestBarrierHandlers_NoPanic ...
		}
	}
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "true")
	t.Setenv("METANODE_DEVNET", "true")
	vh, err := GetValidatorHandler()
	if err != nil {
		t.Fatal(err)
	}
	gh, err := GetGatewayHandler()
	if err != nil {
		t.Fatal(err)
	}
	sites := map[string]*panicSite{}

	for seed := int64(1); seed <= int64(blocks); seed++ {
		r := rand.New(rand.NewSource(seed * 104729))
		env, cs := barrierSetup(t)
		contract := deployTestWrappedAsset(t, cs, env.evm[4].addr, big.NewInt(1000))
		assetABI := testWrappedAssetABI(t)
		wrap := func(in []byte) []byte {
			d, err := transaction.NewCallData(in).Marshal()
			if err != nil {
				return in
			}
			return d
		}
		nonces := map[common.Address]uint64{}
		next := func(a common.Address) uint64 { n := nonces[a]; nonces[a]++; return n }
		var txs []mt_types.Transaction
		for i, n := 0, 3+r.Intn(8); i < n; i++ {
			from := env.evm[r.Intn(len(env.evm))].addr
			var to common.Address
			var data []byte
			var validAmount *big.Int
			switch r.Intn(11) {
			case 0:
				to, data = mt_common.VALIDATOR_CONTRACT_ADDRESS, wrap(randomMethodCall(r, vh.abi, env.users))
			case 1:
				in, amt := validatorOp(r, vh, env, from)
				to, data, validAmount = mt_common.VALIDATOR_CONTRACT_ADDRESS, wrap(in), amt
			case 2:
				to, data = mt_common.GATEWAY_CONTRACT_ADDRESS, wrap(randomMethodCall(r, gh.abi, env.users))
			case 9, 10: // a deployed contract (EVM path through the MVM): random ABI calls and mangled calldata
				data = randomMethodCall(r, assetABI, env.users)
				if r.Intn(2) == 0 {
					data = mutateBytes(r, data)
				}
				to = contract
			case 7, 8: // more of the valid-biased validator sequences
				in, amt := validatorOp(r, vh, env, from)
				to, data, validAmount = mt_common.VALIDATOR_CONTRACT_ADDRESS, wrap(in), amt
			case 3: // mutated valid calldata for either contract
				if r.Intn(2) == 0 {
					to, data = mt_common.VALIDATOR_CONTRACT_ADDRESS, wrap(mutateBytes(r, randomMethodCall(r, vh.abi, env.users)))
				} else {
					to, data = mt_common.GATEWAY_CONTRACT_ADDRESS, wrap(mutateBytes(r, randomMethodCall(r, gh.abi, env.users)))
				}
			case 4: // parent-chain gateway: destPubKey(48) || payloadHash(32) || target(20), then mangled
				raw := make([]byte, 100)
				r.Read(raw)
				to, data = mt_common.PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS, wrap(mutateBytes(r, raw))
			case 5: // account setting: setBlsPublicKey(bytes) with a mangled key, or raw garbage
				acct := utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT)
				key := make([]byte, 48)
				r.Read(key)
				if packed, err := PackSetBlsPublicKey(key); err == nil && r.Intn(2) == 0 {
					to, data = acct, wrap(mutateBytes(r, packed))
				} else {
					g := make([]byte, r.Intn(120))
					r.Read(g)
					to, data = acct, wrap(g)
				}
			default: // rollup system: raw payload (not wrapped in a CallData envelope), usually from the node identity
				from = env.nodeKP.Address()
				g := make([]byte, r.Intn(200))
				r.Read(g)
				if r.Intn(2) == 0 {
					g = mutateBytes(r, attestedRegistrationPayload(t, env.nodeKP, env.users[r.Intn(len(env.users))], diffBarrierCluster, uint64(1+r.Intn(3))))
				}
				to, data = rollup.RollupSystemAddress, g
			}
			amount := edgeBig(r, 96)
			if r.Intn(2) == 0 {
				amount = big.NewInt(0)
			}
			if validAmount != nil {
				amount = validAmount
			}
			gas := []uint64{21000, 50_000, 400_000, 400_000, 0, 1<<63 - 1}[r.Intn(6)]
			price := []uint64{0, 1, mt_common.MINIMUM_BASE_FEE, 1 << 40}[r.Intn(4)]
			txs = append(txs, transaction.NewTransaction(from, to, amount, gas, price, uint64(9000+i), data, nil,
				common.Hash{}, common.Hash{}, next(from), 991))
		}

		func() {
			defer func() {
				if rec := recover(); rec != nil {
					stack := string(debug.Stack())
					frame := repoFrame(stack)
					key := fmt.Sprint(rec) + " | " + frame
					s := sites[key]
					if s == nil {
						var d strings.Builder
						for i, tx := range txs {
							fmt.Fprintf(&d, "  #%d from=%s to=%s nonce=%d amount=%s gas=%d price=%d data=%dB\n", i, tx.FromAddress().Hex()[34:], tx.ToAddress().Hex()[34:],
								tx.GetNonce(), tx.Amount(), tx.MaxGas(), tx.MaxGasPrice(), len(tx.Data()))
						}
						s = &panicSite{value: fmt.Sprint(rec), frame: frame, seed: seed, txs: d.String()}
						sites[key] = s
					}
					s.count++
				}
			}()
			NewTrueBlockSTM(txs).Process(context.Background(), cs, diffLeader, blankHeader(), 999)
			_ = cs.GetSmartContractDB().LateBindRoots()
			_, _ = cs.GetAccountStateDB().IntermediateRoot(true)
			_, _ = cs.GetStakeStateDB().IntermediateRoot()
		}()
	}

	for key, s := range sites {
		t.Errorf("PANIC x%d (first at seed %d): %s\n  raised at %s\n  block:\n%s", s.count, s.seed, s.value, s.frame, s.txs)
		_ = key
	}
}
