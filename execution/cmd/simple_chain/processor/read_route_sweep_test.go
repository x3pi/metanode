package processor

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	mt_proto "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// The state-read routes are open to every TCP client and run on worker goroutines that nothing recovers, so a panic on a
// body a client controls takes the node down. They run here against a real in-memory ChainState (see readRouteHarness),
// with the generic malformed corpus plus bodies shaped like each route's real request (8-byte block numbers, 20/32-byte
// ids, protobuf requests with extreme field values, JSON attestations).

func blockNumberBodies() [][]byte {
	var out [][]byte
	for _, n := range []uint64{0, 1, 2, 1 << 31, 1<<32 - 1, 1 << 32, 1<<63 - 1, 1 << 63, 1<<64 - 1} {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, n)
		out = append(out, b, append(b, 0xff, 0xff, 0xff))
	}
	return out
}

func idBodies(seed int64) [][]byte {
	r := rand.New(rand.NewSource(seed))
	var out [][]byte
	for _, n := range []int{0, 1, 19, 20, 21, 31, 32, 33, 64} {
		b := make([]byte, n)
		r.Read(b)
		out = append(out, b)
	}
	return out
}

func hugeBig() []byte {
	b := make([]byte, 40)
	for i := range b {
		b[i] = 0xff
	}
	return b
}

func structuredLogsBodies(t *testing.T) [][]byte {
	t.Helper()
	h32 := make([]byte, 32)
	var out [][]byte
	add := func(m proto.Message) {
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	manyAddr := make([][]byte, 5000)
	for i := range manyAddr {
		manyAddr[i] = make([]byte, 20)
	}
	for _, from := range [][]byte{nil, {0}, {1}, big.NewInt(1 << 62).Bytes(), hugeBig(), big.NewInt(-1).Bytes()} {
		for _, to := range [][]byte{nil, {0}, {5}, big.NewInt(1 << 62).Bytes(), hugeBig()} {
			add(&mt_proto.GetLogsRequest{FromBlock: from, ToBlock: to})
			add(&mt_proto.GetLogsRequest{FromBlock: from, ToBlock: to, Addresses: manyAddr[:3], Topics: []*mt_proto.TopicFilter{{Hashes: [][]byte{h32, nil, {1}}}, nil}})
		}
	}
	add(&mt_proto.GetLogsRequest{BlockHash: h32})
	add(&mt_proto.GetLogsRequest{BlockHash: []byte{1, 2, 3}})
	add(&mt_proto.GetLogsRequest{Addresses: manyAddr})
	add(&mt_proto.GetLogsRequest{Topics: []*mt_proto.TopicFilter{nil, nil, nil, nil, nil, nil}})
	return out
}

func structuredHashBodies(t *testing.T) (receipt, byHash [][]byte) {
	t.Helper()
	for _, h := range [][]byte{nil, {1}, make([]byte, 20), make([]byte, 31), make([]byte, 32), make([]byte, 33), make([]byte, 1000)} {
		a, err := proto.Marshal(&mt_proto.GetTransactionReceiptRequest{TransactionHash: h})
		if err != nil {
			t.Fatal(err)
		}
		b, err := proto.Marshal(&mt_proto.GetTransactionByHashRequest{TransactionHash: h})
		if err != nil {
			t.Fatal(err)
		}
		receipt, byHash = append(receipt, a), append(byHash, b)
	}
	return
}

func attestationBodies() [][]byte {
	var out [][]byte
	for _, a := range []StateAttestation{
		{},
		{BlockNumber: 1<<64 - 1},
		{BlockNumber: 0, AttestationHash: common.HexToHash("0x01"), NodeAddress: "x"},
		{BlockNumber: 0, BLSSignature: []byte{1}, ValidatorPubKey: []byte{2}},
		{BlockNumber: 0, BLSSignature: make([]byte, 96), ValidatorPubKey: make([]byte, 48)},
		{BlockNumber: 0, BLSSignature: make([]byte, 5000), ValidatorPubKey: make([]byte, 5000)},
	} {
		b, _ := json.Marshal(a)
		out = append(out, b)
	}
	out = append(out, []byte(`{"block_number":-1}`), []byte(`{"block_number":1e30}`), []byte(`{"attestation_hash":"zz"}`), []byte(`null`), []byte(`[]`), []byte(`"x"`), []byte(`{"bls_signature":"!!!"}`))
	return out
}

func TestReadRouteSweep_NoRoutePanics(t *testing.T) {
	bp, sp := readRouteHarness(t)
	generic := malformedCorpus(20261021, 600)
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	conn := func() network.Connection { return NewMockConnection(addr) }

	withBodies := func(extra ...[][]byte) [][]byte {
		all := append([][]byte(nil), generic...)
		for _, e := range extra {
			all = append(all, e...)
		}
		return all
	}
	ids, blocks := idBodies(1), blockNumberBodies()
	receiptBodies, byHashBodies := structuredHashBodies(t)

	routes := []struct {
		name, cmd string
		h         func(network.Request) error
		bodies    [][]byte
	}{
		{"GetAccountState", command.GetAccountState, sp.ProcessGetAccountState, withBodies(ids)},
		{"GetNonce", command.GetNonce, sp.ProcessGetNonce, withBodies(ids)},
		{"GetTransactionsByBlockNumber", command.GetTransactionsByBlockNumber, sp.ProcessGetTransactionsByBlockNumber, withBodies(blocks)},
		{"GetBlockHeaderByBlockNumber", command.GetBlockHeaderByBlockNumber, sp.ProcessGetBlockHeaderByBlockNumber, withBodies(blocks)},
		{"GetJob", command.GetJob, sp.ProcessGetJob, withBodies(ids)},
		{"SetCompleteJob", command.SetCompleteJob, sp.ProcessCompleteJob, withBodies()},
		{"GetTxRewardHistoryByAddress", command.GetTxRewardHistoryByAddress, sp.ProcessGetTxHistoryByAddress, withBodies(ids)},
		{"GetTxRewardHistoryByJobID", command.GetTxRewardHistoryByJobID, sp.ProcessGetTxHistoryByJobID, withBodies(ids)},
		{"GetBlockNumber", command.GetBlockNumber, bp.GetBlockNumber, withBodies()},
		{"GetLastBlockHeader", command.GetLastBlockHeader, bp.GetLastBlockHeader, withBodies()},
		{"GetLogs", command.GetLogs, bp.GetLogs, withBodies(structuredLogsBodies(t))},
		{"GetTransactionReceipt", command.GetTransactionReceipt, bp.GetTransactionReceipt, withBodies(ids, receiptBodies)},
		{"GetTransactionByHash", command.GetTransactionByHash, bp.GetTransactionByHash, withBodies(ids, byHashBodies)},
		{"GetChainId", command.GetChainId, bp.GetChainId, withBodies()},
		{"StateAttestation", p_common.StateAttestationTopic, bp.ProcessStateAttestation, withBodies(attestationBodies())},
	}
	var wg sync.WaitGroup
	for _, rt := range routes {
		rt := rt
		wg.Add(1)
		go func() { // each route in its own goroutine: a stuck handler is reported, not a hung suite
			defer wg.Done()
			sweepRoute(t, rt.name, rt.cmd, rt.h, conn, rt.bodies)
		}()
	}
	wg.Wait()
	_ = fmt.Sprint
}
