package proto_test

import (
	"encoding/hex"
	"testing"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

// TestLastBlockNumberResponse_GEI_Serialization verifies that last_global_exec_index
// is correctly serialized in protobuf wire format.
//
// ROOT CAUSE BUG (2026-03-11): validator.pb.go was generated from an old .proto file
// that didn't have last_global_exec_index in LastBlockNumberResponse.
// Someone manually added the struct field + getter to .pb.go, but Go protobuf v2 uses
// the embedded RAW DESCRIPTOR (not struct tags) for marshaling.
// Result: Go handler logs gei=18392 correctly (getter works), but marshal()
// serializes only last_block_number — gei is MISSING from wire data → Rust gets 0.
func TestLastBlockNumberResponse_GEI_Serialization(t *testing.T) {
	tests := []struct {
		name            string
		blockNumber     uint64
		gei             uint64
		expectFieldTag2 bool // 0x10 = tag 2 varint
	}{
		{
			name:            "both_zero_no_fields_needed",
			blockNumber:     0,
			gei:             0,
			expectFieldTag2: false, // proto3 skips default values
		},
		{
			name:            "block_only",
			blockNumber:     352,
			gei:             0,
			expectFieldTag2: false, // gei=0 is default, not encoded
		},
		{
			name:            "gei_only",
			blockNumber:     0,
			gei:             18392,
			expectFieldTag2: true, // gei != 0, MUST be in wire data
		},
		{
			name:            "both_nonzero",
			blockNumber:     352,
			gei:             18392,
			expectFieldTag2: true, // gei != 0, MUST be in wire data
		},
		{
			name:            "large_gei",
			blockNumber:     1000,
			gei:             1_000_000,
			expectFieldTag2: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := &pb.LastBlockNumberResponse{
				LastBlockNumber:     tc.blockNumber,
				LastGlobalExecIndex: tc.gei,
			}

			// Marshal inner message
			innerBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(resp)
			if err != nil {
				t.Fatalf("Failed to marshal: %v", err)
			}

			// Check if tag=2 (0x10) is present in wire data
			hasTag2 := false
			for _, b := range innerBytes {
				if b == 0x10 {
					hasTag2 = true
					break
				}
			}

			if tc.expectFieldTag2 && !hasTag2 {
				t.Errorf("CRITICAL BUG: last_global_exec_index=%d is NOT in protobuf wire data!\n"+
					"  Wire bytes: %s\n"+
					"  This means validator.pb.go raw descriptor is STALE.\n"+
					"  FIX: Regenerate with: protoc --go_out=. --go_opt=paths=source_relative pkg/proto/validator.proto",
					tc.gei, hex.EncodeToString(innerBytes))
			}
			if !tc.expectFieldTag2 && hasTag2 {
				// Not an error — proto might encode it anyway in some implementations
				t.Logf("Note: tag=2 found even for gei=%d (not expected but acceptable)", tc.gei)
			}

			// Unmarshal and verify round-trip
			decoded := &pb.LastBlockNumberResponse{}
			if err := proto.Unmarshal(innerBytes, decoded); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}

			if decoded.GetLastBlockNumber() != tc.blockNumber {
				t.Errorf("Round-trip mismatch: LastBlockNumber want=%d got=%d",
					tc.blockNumber, decoded.GetLastBlockNumber())
			}
			if decoded.GetLastGlobalExecIndex() != tc.gei {
				t.Errorf("Round-trip mismatch: LastGlobalExecIndex want=%d got=%d",
					tc.gei, decoded.GetLastGlobalExecIndex())
			}
		})
	}
}

// TestLastBlockNumberResponse_WrappedInResponse verifies that LastBlockNumberResponse
// round-trips correctly when wrapped in a Response oneof (same as Go→Rust IPC path).
func TestLastBlockNumberResponse_WrappedInResponse(t *testing.T) {
	resp := &pb.LastBlockNumberResponse{
		LastBlockNumber:     352,
		LastGlobalExecIndex: 18392,
	}

	wrapped := &pb.Response{
		Payload: &pb.Response_LastBlockNumberResponse{
			LastBlockNumberResponse: resp,
		},
	}

	// Marshal wrapped response
	wrappedBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(wrapped)
	if err != nil {
		t.Fatalf("Failed to marshal wrapped response: %v", err)
	}

	t.Logf("Wrapped Response bytes (%d): %s", len(wrappedBytes), hex.EncodeToString(wrappedBytes))

	// Unmarshal
	decoded := &pb.Response{}
	if err := proto.Unmarshal(wrappedBytes, decoded); err != nil {
		t.Fatalf("Failed to unmarshal wrapped response: %v", err)
	}

	// Extract LastBlockNumberResponse
	payload, ok := decoded.Payload.(*pb.Response_LastBlockNumberResponse)
	if !ok {
		t.Fatalf("Expected Response_LastBlockNumberResponse, got %T", decoded.Payload)
	}

	inner := payload.LastBlockNumberResponse
	if inner.GetLastBlockNumber() != 352 {
		t.Errorf("LastBlockNumber: want=352, got=%d", inner.GetLastBlockNumber())
	}
	if inner.GetLastGlobalExecIndex() != 18392 {
		t.Errorf("LastGlobalExecIndex: want=18392, got=%d", inner.GetLastGlobalExecIndex())
	}
}

// TestAdvanceEpochRequest_Serialization verifies AdvanceEpochRequest round-trip.
func TestAdvanceEpochRequest_Serialization(t *testing.T) {
	req := &pb.AdvanceEpochRequest{
		NewEpoch:              2,
		EpochStartTimestampMs: 1772784900000,
		BoundaryBlock:         18392,
	}

	bytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	decoded := &pb.AdvanceEpochRequest{}
	if err := proto.Unmarshal(bytes, decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.GetNewEpoch() != 2 {
		t.Errorf("NewEpoch: want=2, got=%d", decoded.GetNewEpoch())
	}
	if decoded.GetEpochStartTimestampMs() != 1772784900000 {
		t.Errorf("EpochStartTimestampMs: want=1772784900000, got=%d", decoded.GetEpochStartTimestampMs())
	}
	if decoded.GetBoundaryBlock() != 18392 {
		t.Errorf("BoundaryBlock: want=18392, got=%d", decoded.GetBoundaryBlock())
	}
}

// TestEpochBoundaryData_Serialization verifies EpochBoundaryData round-trip.
func TestEpochBoundaryData_Serialization(t *testing.T) {
	data := &pb.EpochBoundaryData{
		Epoch:                 1,
		EpochStartTimestampMs: 1772784000000,
		BoundaryBlock:         18392,
		Validators: []*pb.ValidatorInfo{
			{Address: "0x1234", Stake: "100000", AuthorityKey: "key1"},
			{Address: "0x5678", Stake: "200000", AuthorityKey: "key2"},
		},
		EpochDurationSeconds: 900,
	}

	bytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(data)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	decoded := &pb.EpochBoundaryData{}
	if err := proto.Unmarshal(bytes, decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.GetEpoch() != 1 {
		t.Errorf("Epoch: want=1, got=%d", decoded.GetEpoch())
	}
	if decoded.GetBoundaryBlock() != 18392 {
		t.Errorf("BoundaryBlock: want=18392, got=%d", decoded.GetBoundaryBlock())
	}
	if len(decoded.GetValidators()) != 2 {
		t.Errorf("Validators: want=2, got=%d", len(decoded.GetValidators()))
	}
	if decoded.GetEpochDurationSeconds() != 900 {
		t.Errorf("EpochDurationSeconds: want=900, got=%d", decoded.GetEpochDurationSeconds())
	}
}

// TestValidatorInfoList_GEI_Serialization verifies that last_global_exec_index
// is also correctly serialized in ValidatorInfoList (used during epoch transition).
func TestValidatorInfoList_GEI_Serialization(t *testing.T) {
	list := &pb.ValidatorInfoList{
		Validators: []*pb.ValidatorInfo{
			{Address: "0x1234", Stake: "100000"},
		},
		EpochTimestampMs:    1772784000000,
		LastGlobalExecIndex: 18392,
	}

	bytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(list)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	decoded := &pb.ValidatorInfoList{}
	if err := proto.Unmarshal(bytes, decoded); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if decoded.GetLastGlobalExecIndex() != 18392 {
		t.Errorf("CRITICAL: LastGlobalExecIndex: want=18392, got=%d (raw descriptor may be stale!)",
			decoded.GetLastGlobalExecIndex())
	}
	if decoded.GetEpochTimestampMs() != 1772784000000 {
		t.Errorf("EpochTimestampMs: want=1772784000000, got=%d", decoded.GetEpochTimestampMs())
	}
	if len(decoded.GetValidators()) != 1 {
		t.Errorf("Validators: want=1, got=%d", len(decoded.GetValidators()))
	}
}
