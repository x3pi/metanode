//go:build ignore

package main

import (
"encoding/binary"
"fmt"
)

func main() {
key := make([]byte, 0)
key = binary.BigEndian.AppendUint64(key, 0)
fmt.Printf("Block 0: %x\n", key)

key2 := make([]byte, 0)
key2 = binary.BigEndian.AppendUint64(key2, 3)
fmt.Printf("Block 3: %x\n", key2)
}
