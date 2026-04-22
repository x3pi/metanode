package node

import (
	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

type Node interface {
	Unmarshal([]byte, []byte) error
	Marshal() ([]byte, error)
	FString(string) string
	Cache() (HashNode, bool)
}

type (
	NodeFlag struct {
		Hash  HashNode // cached hash of the node (may be nil)
		Dirty bool     // whether the node has changes that must be written to the database
	}
)

// newFlag returns the cache flag value for a newly created node.
func NewFlag() NodeFlag {
	return NodeFlag{Dirty: true}
}

// nilValueNode is used when collapsing internal trie nodes for hashing, since
// unset children need to serialize correctly.
var NilValueNode = ValueNode(nil)

func DecodeNode(hash, buf []byte) (Node, error) {
	protoNode := &pb.MPTNode{}

	err := proto.Unmarshal(buf, protoNode)
	if err != nil {
		// unable to parse node so this will be hash node
		return HashNode(buf), nil
	}
	switch protoNode.Type {
	case pb.MPTNODE_TYPE_FULL:
		fullNode := &FullNode{}
		err = fullNode.Unmarshal(hash, protoNode.Data)
		if err != nil {
			return HashNode(protoNode.Data), nil
		}
		return fullNode, nil

	case pb.MPTNODE_TYPE_SHORT:
		shortNode := &ShortNode{}
		err = shortNode.Unmarshal(hash, protoNode.Data)
		if err != nil {
			// unable to parse node so this will be hash node
			return HashNode(protoNode.Data), nil
		}
		return shortNode, nil
	default:
		return nil, nil
	}
}

func NodeToBytes(n Node) []byte {
	b, _ := n.Marshal()
	return b
}
