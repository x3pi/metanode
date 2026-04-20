package node

import (
	"encoding/hex"
	"fmt"
)

type (
	ValueNode []byte
)

func (n ValueNode) Unmarshal([]byte, []byte) error {
	return nil
}

func (n ValueNode) Marshal() ([]byte, error) {
	return n, nil
}

func (n ValueNode) Cache() (HashNode, bool) { return nil, true }

func (n ValueNode) FString(ind string) string {
	return fmt.Sprintf("%vVN:%v", ind, hex.EncodeToString([]byte(n)))
}
