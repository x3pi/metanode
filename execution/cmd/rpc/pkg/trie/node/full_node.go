package node

import (
	"encoding/hex"
	"fmt"

	e_common "github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

const (
	TOTAL_CHILD_NODE = 17
)

type FullNode struct {
	Children [TOTAL_CHILD_NODE]Node
	Flags    NodeFlag
}

func (fn *FullNode) Unmarshal(hash, buf []byte) error {
	protoFN := &pb.MPTFullNode{}
	err := proto.Unmarshal(buf, protoFN)
	if err != nil {
		// fmt.Print("\n😿Unmarshal Error - buf: ", buf)
		// fmt.Println("\n😿Unmarshal Error -", err, hex.EncodeToString(buf))
		return err
	}
	for i, v := range protoFN.Nodes {
		if (e_common.BytesToHash(v) == e_common.Hash{}) {
			continue
		}
		hashNode := HashNode(v)
		fn.Children[i] = hashNode
	}
	fn.Flags.Hash = hash
	return nil
}

func (fn *FullNode) Marshal() ([]byte, error) {
	bufNodes := [TOTAL_CHILD_NODE][]byte{}
	for i := 0; i < TOTAL_CHILD_NODE; i++ {
		if fn.Children[i] == nil {
			bufNodes[i] = e_common.Hash{}.Bytes()
		} else {
			var err error
			bufNodes[i], err = fn.Children[i].Marshal()
			if err != nil {
				return nil, err
			}
		}
	}
	protoFN := &pb.MPTFullNode{
		Nodes: bufNodes[:],
	}
	bFN, _ := proto.Marshal(protoFN)
	protoNode := &pb.MPTNode{
		Type: pb.MPTNODE_TYPE_FULL,
		Data: bFN,
	}
	return proto.Marshal(protoNode)
}

func (fn *FullNode) FString(ind string) string {
	resp := fmt.Sprintf("%s FullNode %v:\n", ind, hex.EncodeToString(fn.Flags.Hash))
	for i, node := range &fn.Children {
		if node == nil {
			resp += fmt.Sprintf("%v%v: <nil>\n", ind, i)
		} else {
			resp += fmt.Sprintf("%v%v: \n%v\n", ind, i, node.FString(ind+ind))
		}
	}
	return resp
}

func (fn *FullNode) String() string {
	return fn.FString("")
}

func (fn *FullNode) Cache() (HashNode, bool) {
	return fn.Flags.Hash, fn.Flags.Dirty
}

func (n *FullNode) Copy() *FullNode { copy := *n; return &copy }
