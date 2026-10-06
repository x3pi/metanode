package rollup

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

func validSystemPayload() RollupSystemPayload {
	var p RollupSystemPayload
	p.Event = Event{Type: EventCreditObserved, Value: big.NewInt(5), Sender: common.HexToAddress("0x01"), Target: common.HexToAddress("0x02")}
	p.MsgID = common.HexToHash("0xabc")
	p.SourceSeq = 3
	p.SourcePubKey[0], p.DestPubKey[0] = 1, 2
	return p
}

func TestProtoPayloadRoundTripIsLosslessAndDeterministic(t *testing.T) {
	p := validSystemPayload()
	a, err := MarshalRollupSystemPayload(&p)
	require.NoError(t, err)
	b, _ := MarshalRollupSystemPayload(&p)
	require.Equal(t, a, b, "same payload => same bytes")
	var out RollupSystemPayload
	require.NoError(t, UnmarshalRollupSystemPayload(a, &out))
	c, _ := MarshalRollupSystemPayload(&out)
	require.Equal(t, a, c, "decode then encode is the identity")
	require.Equal(t, p.Event.Value.String(), out.Event.Value.String())
	require.Equal(t, p.MsgID, out.MsgID)
}

// Fixed-size fields must be EXACT: zero-padding/truncating would let different byte strings decode to the same key.
func TestProtoPayloadsRejectWrongFieldLengths(t *testing.T) {
	p := validSystemPayload()
	good := p.ToProto()
	mut := map[string]func(m *pb.RollupSystemPayloadProto){
		"short msg_id":       func(m *pb.RollupSystemPayloadProto) { m.MsgId = m.MsgId[:31] },
		"long msg_id":        func(m *pb.RollupSystemPayloadProto) { m.MsgId = append(m.MsgId, 0) },
		"short source key":   func(m *pb.RollupSystemPayloadProto) { m.SourcePubKey = m.SourcePubKey[:47] },
		"long dest key":      func(m *pb.RollupSystemPayloadProto) { m.DestPubKey = append(m.DestPubKey, 0) },
		"short sender":       func(m *pb.RollupSystemPayloadProto) { m.Event.Sender = m.Event.Sender[:19] },
		"long target":        func(m *pb.RollupSystemPayloadProto) { m.Event.Target = append(m.Event.Target, 1) },
		"empty payload hash": func(m *pb.RollupSystemPayloadProto) { m.PayloadHash = nil },
	}
	for name, f := range mut {
		m := proto.Clone(good).(*pb.RollupSystemPayloadProto)
		f(m)
		raw, err := proto.Marshal(m)
		require.NoError(t, err)
		var out RollupSystemPayload
		require.Error(t, UnmarshalRollupSystemPayload(raw, &out), name)
	}

	att := func(pub, sig int) []byte {
		raw, _ := proto.Marshal(&pb.RollupSystemAttestedPayloadProto{Kind: PayloadKindRollupSystemAttested, Inner: []byte{1},
			Attestations: []*pb.RollupAttestationProto{{ValidatorPubkey: make([]byte, pub), Signature: make([]byte, sig)}}})
		return raw
	}
	var ap RollupSystemAttestedPayload
	require.NoError(t, UnmarshalRollupSystemAttestedPayload(att(48, 96), &ap))
	for _, c := range [][2]int{{47, 96}, {49, 96}, {48, 95}, {48, 97}, {0, 0}} {
		require.Error(t, UnmarshalRollupSystemAttestedPayload(att(c[0], c[1]), &ap), "pub=%d sig=%d", c[0], c[1])
	}

	reg := func(user, cluster, pub int) []byte {
		raw, _ := proto.Marshal(&pb.AccountRegistrationPayloadProto{Kind: SystemPayloadKindAccountRegistered, User: make([]byte, user),
			ClusterKey: make([]byte, cluster), ParentSeq: 1,
			Attestations: []*pb.RollupAttestationProto{{ValidatorPubkey: make([]byte, pub), Signature: make([]byte, 96)}}})
		return raw
	}
	var rp AccountRegistrationPayload
	require.NoError(t, rp.Unmarshal(reg(20, 48, 48)))
	for _, c := range [][3]int{{19, 48, 48}, {32, 48, 48}, {0, 48, 48}, {20, 47, 48}, {20, 49, 48}, {20, 48, 49}, {20, 48, 47}} {
		var x AccountRegistrationPayload
		require.Error(t, x.Unmarshal(reg(c[0], c[1], c[2])), "user=%d cluster=%d pub=%d", c[0], c[1], c[2])
	}
}
