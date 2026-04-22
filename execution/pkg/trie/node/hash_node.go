package node

import "encoding/hex"

type (
	HashNode []byte
)

func (n HashNode) Unmarshal([]byte, []byte) error {
	return nil
}

func (n HashNode) Marshal() ([]byte, error) {
	return n[:], nil
}

func (n HashNode) Cache() (HashNode, bool) { return nil, true }

func (n HashNode) FString(ind string) string {
	return hex.EncodeToString(n)
}
