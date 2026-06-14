//go:build ignore

package main

import (
"encoding/hex"
"fmt"
"os"

"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
)

func main() {
tempDir, err := os.MkdirTemp("", "nomt_verify_*")
if err != nil {
ic(err)
}
defer os.RemoveAll(tempDir)

tempHandle, err := nomt_ffi.Open(tempDir, 1, 64, 64)
if err != nil {
ic(err)
}
defer tempHandle.Close()

session := tempHandle.BeginSession()
keyPath := make([]byte, 32)
keyPath[0] = 1 // Just some key
val := []byte("hello")

err = session.RecordRead(keyPath, nil)
if err != nil {
ic(err)
}
err = session.BatchWrite([][]byte{keyPath}, [][]byte{val})
if err != nil {
ic(err)
}
root, _, err := session.Finish(tempHandle)
if err != nil {
ic(err)
}
fmt.Printf("Root: %s\n", hex.EncodeToString(root))
}
