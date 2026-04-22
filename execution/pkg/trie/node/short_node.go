package node

import (
	"encoding/hex"
	"fmt"

	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

type ShortNode struct {
	Key   []byte
	Val   Node
	Flags NodeFlag
}

func (sn *ShortNode) Unmarshal(hash, buf []byte) error {
	protoSN := &pb.MPTShortNode{}
	err := proto.Unmarshal(buf, protoSN)
	if err != nil {
		return err
	}
	sn.Key = CompactToHex(protoSN.Key)
	if HasTerm(sn.Key) {
		// value node
		sn.Val = ValueNode(protoSN.Value)
	} else {
		sn.Val = HashNode(protoSN.Value)
	}

	sn.Flags.Hash = hash
	return nil
}

func (sn *ShortNode) Marshal() ([]byte, error) {
	valBuf, err := sn.Val.Marshal()
	if err != nil {
		return nil, err
	}
	
	protoSN := mptShortNodePool.Get().(*pb.MPTShortNode)
	protoSN.Key = sn.Key
	protoSN.Value = valBuf
	
	bSN, err := proto.MarshalOptions{Deterministic: true}.Marshal(protoSN)
	
	// Release immediately - wipe pointers
	protoSN.Key = nil
	protoSN.Value = nil
	mptShortNodePool.Put(protoSN)
	
	if err != nil {
		return nil, err
	}

	protoNode := mptNodePool.Get().(*pb.MPTNode)
	protoNode.Type = pb.MPTNODE_TYPE_SHORT
	protoNode.Data = bSN
	
	res, err := proto.MarshalOptions{Deterministic: true}.Marshal(protoNode)
	
	protoNode.Data = nil
	mptNodePool.Put(protoNode)
	
	return res, err
}

func (sn *ShortNode) FString(ind string) string {
	return fmt.Sprintf(
		"%vSN%v:%v: \n%v\n",
		ind,
		hex.EncodeToString(sn.Key),
		hex.EncodeToString(sn.Flags.Hash),
		sn.Val.FString(ind+ind),
	)
}

func (sn *ShortNode) String() string {
	return sn.FString("")
}

func (sn *ShortNode) Cache() (HashNode, bool) {
	return sn.Flags.Hash, sn.Flags.Dirty
}

func (n *ShortNode) Copy() *ShortNode { copy := *n; return &copy }
