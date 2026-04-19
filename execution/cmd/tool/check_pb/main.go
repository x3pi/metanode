package main

import (
	"fmt"
	"log"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	protogo "google.golang.org/protobuf/proto"
)

func main() {
	// Create a dummy BlockData with 100MB backupData
	backupBytes := make([]byte, 100*1024*1024)
	for i := range backupBytes {
		backupBytes[i] = 1 // Just fill with non-zero
	}

	blockData := &pb.BlockData{
		BlockNumber: 3,
		BackupData:  backupBytes,
	}

	// Wrap in SyncBlocksRequest
	req := &pb.Request{
		Payload: &pb.Request_SyncBlocksRequest{
			SyncBlocksRequest: &pb.SyncBlocksRequest{
				Blocks: []*pb.BlockData{blockData},
			},
		},
	}

	// Marshal
	marshaledBytes, err := protogo.Marshal(req)
	if err != nil {
		log.Fatalf("Marshal failed: %v", err)
	}
	fmt.Printf("Marshaled size: %d bytes\n", len(marshaledBytes))

	// Unmarshal
	newReq := &pb.Request{}
	err = protogo.Unmarshal(marshaledBytes, newReq)
	if err != nil {
		log.Fatalf("Unmarshal failed: %v", err)
	}

	syncReq := newReq.GetSyncBlocksRequest()
	if syncReq == nil {
		log.Fatalf("SyncBlocksRequest is nil")
	}

	if len(syncReq.Blocks) == 0 {
		log.Fatalf("Blocks is empty")
	}

	newBlock := syncReq.Blocks[0]
	fmt.Printf("Unmarshaled BackupData length: %d bytes\n", len(newBlock.BackupData))
}
