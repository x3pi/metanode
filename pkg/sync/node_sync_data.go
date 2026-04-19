package sync

import (
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

type GetNodeSyncData struct {
	latestCheckPointBlockNumber uint64
	validatorAddress            common.Address
	nodeStatesIndex             int
}

func NewGetNodeSyncData(
	latestCheckPointBlockNumber uint64,
	validatorAddress common.Address,
	nodeStatesIndex int,
) *GetNodeSyncData {
	return &GetNodeSyncData{
		latestCheckPointBlockNumber: latestCheckPointBlockNumber,
		validatorAddress:            validatorAddress,
		nodeStatesIndex:             nodeStatesIndex,
	}
}

func GetNodeSyncDataFromProto(pbData *pb.GetNodeSyncData) *GetNodeSyncData {
	return NewGetNodeSyncData(
		pbData.LatestCheckPointBlockNumber,
		common.BytesToAddress(pbData.ValidatorAddress),
		int(pbData.NodeStatesIndex),
	)
}

func GetNodeSyncDataToProto(data *GetNodeSyncData) *pb.GetNodeSyncData {
	return &pb.GetNodeSyncData{
		LatestCheckPointBlockNumber: data.latestCheckPointBlockNumber,
		ValidatorAddress:            data.validatorAddress.Bytes(),
		NodeStatesIndex:             int64(data.nodeStatesIndex),
	}
}

func (data *GetNodeSyncData) Unmarshal(b []byte) error {
	pbData := &pb.GetNodeSyncData{}
	err := proto.Unmarshal(b, pbData)
	if err != nil {
		return err
	}
	*data = *GetNodeSyncDataFromProto(pbData)
	return nil
}

func (data *GetNodeSyncData) LatestCheckPointBlockNumber() uint64 {
	return data.latestCheckPointBlockNumber
}

func (data *GetNodeSyncData) ValidatorAddress() common.Address {
	return data.validatorAddress
}

func (data *GetNodeSyncData) NodeStatesIndex() int {
	return data.nodeStatesIndex
}

type NodeSyncData struct {
	validatorAddress common.Address
	nodeStatesIndex  int
	accountStateRoot common.Hash
	data             [][2][]byte
	finished         bool
}

func NewNodeSyncData(
	validatorAddress common.Address,
	nodeStatesIndex int,
	accountStateRoot common.Hash,
	data [][2][]byte,
	finished bool,
) *NodeSyncData {
	return &NodeSyncData{
		validatorAddress: validatorAddress,
		nodeStatesIndex:  nodeStatesIndex,
		accountStateRoot: accountStateRoot,
		data:             data,
		finished:         finished,
	}
}

func (data *NodeSyncData) Unmarshal(b []byte) error {
	pbData := &pb.NodeSyncData{}
	err := proto.Unmarshal(b, pbData)
	if err != nil {
		return err
	}
	*data = *NodeSyncDataFromProto(pbData)
	return nil
}

func (data *NodeSyncData) ValidatorAddress() common.Address {
	return data.validatorAddress
}

func (data *NodeSyncData) NodeStatesIndex() int {
	return data.nodeStatesIndex
}

func (data *NodeSyncData) Finished() bool {
	return data.finished
}

func (data *NodeSyncData) AccountStateRoot() common.Hash {
	return data.accountStateRoot
}

func (data *NodeSyncData) Data() [][2][]byte {
	return data.data
}

func NodeSyncDataFromProto(pbData *pb.NodeSyncData) *NodeSyncData {
	data := make([][2][]byte, 0, len(pbData.StorageData))
	for _, d := range pbData.StorageData {
		data = append(data, [2][]byte{d.Key, d.Value})
	}
	return NewNodeSyncData(
		common.BytesToAddress(pbData.ValidatorAddress),
		int(pbData.NodeStatesIndex),
		common.BytesToHash(pbData.AccountStateRoot),
		data,
		pbData.Finished,
	)
}

func NodeSyncDataToProto(data *NodeSyncData) *pb.NodeSyncData {
	pbData := &pb.NodeSyncData{
		ValidatorAddress: data.validatorAddress.Bytes(),
		NodeStatesIndex:  int64(data.nodeStatesIndex),
		AccountStateRoot: data.accountStateRoot.Bytes(),
		StorageData:      make([]*pb.StorageData, 0, len(data.data)),
		Finished:         data.finished,
	}
	for _, d := range data.data {
		pbData.StorageData = append(pbData.StorageData, &pb.StorageData{Key: d[0], Value: d[1]})
	}
	return pbData
}
