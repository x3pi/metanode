package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
)

// TestParseSingleChainID is the regression test for the small helper publish-genesis-digest /
// verify-genesis / query-alloc-raw / query-genesis-wallet-raw all share to pull exactly one chain
// ID out of the (comma-separated, multi-chain-oriented) -chains flag.
func TestParseSingleChainID(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    uint64
		wantErr bool
	}{
		{"single value", "101", 101, false},
		{"first of several -- these actions only ever operate on one chain", "101,102,103", 101, false},
		{"leading/trailing whitespace", "  101  ", 101, false},
		{"leading empty entries skipped", ",,101", 101, false},
		{"empty string", "", 0, true},
		{"only commas", ",,,", 0, true},
		{"non-numeric", "abc", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSingleChainID(c.input)
			if c.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

// mockGatewayViewServer serves eth_call for exactly the Gateway view methods these tests need,
// keyed by the request's 4-byte function selector -- same shape as
// pkg/cross_chain/rootanchor/client_test.go's own mock server, kept independent here since this
// package (cmd/tool/register_chains) cannot import a package-internal test helper from another
// package.
func mockGatewayViewServer(t *testing.T, parsedABI abi.ABI, handlers map[string]func(args []interface{}) []interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     json.RawMessage `json:"id"`
		}
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &req))

		reply := func(result interface{}) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}

		switch req.Method {
		case "eth_chainId":
			reply("0x270f") // 9999, unused by these tests but every ethclient.Dial call checks it
		case "eth_call":
			// params is [callObject, blockTag] -- blockTag is a bare string ("latest"), not an
			// object, so it must be decoded as []json.RawMessage first, not []map[string]any.
			var params []json.RawMessage
			require.NoError(t, json.Unmarshal(req.Params, &params))
			var callObj map[string]interface{}
			require.NoError(t, json.Unmarshal(params[0], &callObj))
			// go-ethereum's ethclient.toCallArg() encodes calldata under "input", not "data".
			dataHex, _ := callObj["input"].(string)
			calldata, err := common.ParseHexOrString(dataHex)
			require.NoError(t, err)
			method, err := parsedABI.MethodById(calldata[:4])
			require.NoError(t, err)
			args, err := method.Inputs.Unpack(calldata[4:])
			require.NoError(t, err)
			h, ok := handlers[method.Name]
			if !ok {
				t.Fatalf("mockGatewayViewServer: no handler registered for method %q", method.Name)
			}
			out, err := method.Outputs.Pack(h(args)...)
			require.NoError(t, err)
			reply("0x" + common.Bytes2Hex(out))
		default:
			reply(nil)
		}
	}))
}

// captureStdout runs fn with os.Stdout redirected, returning everything fn printed. Needed
// because handleQueryAllocationRaw/handleQueryGenesisWalletRaw are deliberately "print exactly
// the value, nothing else" CLI actions (see their own doc comments) -- the printed stdout IS
// their real, user-facing contract, not just an implementation detail to skip over.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func TestHandleQueryAllocationRaw_PrintsExactlyTheWeiAmount(t *testing.T) {
	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	require.NoError(t, err)

	srv := mockGatewayViewServer(t, parsedABI, map[string]func(args []interface{}) []interface{}{
		"getAllocation": func(args []interface{}) []interface{} {
			return []interface{}{big.NewInt(424242)}
		},
	})
	defer srv.Close()

	out := captureStdout(t, func() {
		handleQueryAllocationRaw(context.Background(), srv.URL, "101", parsedABI)
	})
	assert.Equal(t, "424242\n", out, "must print ONLY the decimal wei amount, nothing else -- this is a machine-readable action")
}

func TestHandleQueryGenesisWalletRaw_PrintsExactlyTheAddress(t *testing.T) {
	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	require.NoError(t, err)

	wantWallet := common.HexToAddress("0x9999999999999999999999999999999999999999")
	srv := mockGatewayViewServer(t, parsedABI, map[string]func(args []interface{}) []interface{}{
		"getChainRegistry": func(args []interface{}) []interface{} {
			return []interface{}{
				true, [][]byte{}, []uint64{}, [][]byte{},
				uint64(1), uint64(6667), common.Address{}, [32]byte{}, [32]byte{}, "", uint64(0),
				wantWallet, [32]byte{},
			}
		},
	})
	defer srv.Close()

	out := captureStdout(t, func() {
		handleQueryGenesisWalletRaw(context.Background(), srv.URL, "101", parsedABI)
	})
	assert.Equal(t, wantWallet.Hex()+"\n", out, "must print ONLY the 0x-prefixed address, nothing else")
}
