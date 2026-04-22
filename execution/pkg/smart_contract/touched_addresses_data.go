package smart_contract

import (
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

type TouchedAddressesData struct {
	blockNumber uint64
	addresses   []common.Address
}

func NewTouchedAddressesData(blockNumber uint64, addresses []common.Address) *TouchedAddressesData {
	return &TouchedAddressesData{
		blockNumber: blockNumber,
		addresses:   addresses,
	}
}

func (tad *TouchedAddressesData) BlockNumber() uint64 {
	return tad.blockNumber
}

func (tad *TouchedAddressesData) Addresses() []common.Address {
	return tad.addresses
}

func (tad *TouchedAddressesData) Proto() *pb.TouchedAddressesData {
	bAddresses := make([][]byte, len(tad.addresses))
	for i, addr := range tad.addresses {
		bAddresses[i] = addr.Bytes()
	}
	return &pb.TouchedAddressesData{
		BlockNumber: tad.blockNumber,
		Addresses:   bAddresses,
	}
}

func (tad *TouchedAddressesData) FromProto(pbTad *pb.TouchedAddressesData) {
	tad.blockNumber = pbTad.BlockNumber
	tad.addresses = make([]common.Address, len(pbTad.Addresses))
	for i, bAddr := range pbTad.Addresses {
		tad.addresses[i] = common.BytesToAddress(bAddr)
	}
}

func (tad *TouchedAddressesData) Unmarshal(data []byte) error {
	pbTad := &pb.TouchedAddressesData{}
	if err := proto.Unmarshal(data, pbTad); err != nil {
		return err
	}
	tad.FromProto(pbTad)
	return nil
}

func (tad *TouchedAddressesData) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(tad.Proto())
}
