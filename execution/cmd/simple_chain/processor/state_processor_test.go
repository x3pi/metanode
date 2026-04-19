package processor

import (
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// TestNewStateProcessor verifies the constructor wires dependencies correctly.
func TestNewStateProcessor(t *testing.T) {
	sender := NewMockMessageSender()
	sp := NewStateProcessor(sender, nil, nil, nil)

	if sp == nil {
		t.Fatal("NewStateProcessor returned nil")
	}
	if sp.messageSender == nil {
		t.Fatal("messageSender was not set")
	}
	if sp.scResultsCache == nil {
		t.Fatal("scResultsCache was not initialized")
	}
}

// TestSendFailureResponse verifies that sendFailureResponse marshals a
// CompleteJobResponse{Success: false} and sends it via the message sender.
func TestSendFailureResponse(t *testing.T) {
	sender := NewMockMessageSender()
	sp := NewStateProcessor(sender, nil, nil, nil)

	conn := NewMockConnection(e_common.HexToAddress("0x1234"))
	sp.sendFailureResponse(conn)

	if sender.SentCount() != 1 {
		t.Fatalf("expected 1 sent message, got %d", sender.SentCount())
	}

	sent := sender.Sent[0]
	if sent.Command != command.CompleteJob {
		t.Fatalf("expected command %q, got %q", command.CompleteJob, sent.Command)
	}

	// Verify the protobuf payload
	var resp pb.CompleteJobResponse
	if err := proto.Unmarshal(sent.Data, &resp); err != nil {
		t.Fatalf("failed to unmarshal CompleteJobResponse: %v", err)
	}
	if resp.Success {
		t.Fatalf("expected Success=false, got true")
	}
}

// TestSendTxHistoryByAddressErrorResponse verifies the error response helper
// for address-based tx history.
func TestSendTxHistoryByAddressErrorResponse(t *testing.T) {
	sender := NewMockMessageSender()
	sp := NewStateProcessor(sender, nil, nil, nil)

	conn := NewMockConnection(e_common.HexToAddress("0x5678"))
	errMsg := "test error message"
	sp.sendTxHistoryByAddressErrorResponse(conn, errMsg)

	if sender.SentCount() != 1 {
		t.Fatalf("expected 1 sent message, got %d", sender.SentCount())
	}

	sent := sender.Sent[0]
	if sent.Command != command.TxRewardHistoryByAddress {
		t.Fatalf("expected command %q, got %q", command.TxRewardHistoryByAddress, sent.Command)
	}

	var resp pb.GetTransactionHistoryByAddressResponse
	if err := proto.Unmarshal(sent.Data, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.Error != errMsg {
		t.Fatalf("expected error %q, got %q", errMsg, resp.Error)
	}
}

// TestSendTxHistoryByJobIDErrorResponse verifies the error response helper
// for job ID-based tx history.
func TestSendTxHistoryByJobIDErrorResponse(t *testing.T) {
	sender := NewMockMessageSender()
	sp := NewStateProcessor(sender, nil, nil, nil)

	conn := NewMockConnection(e_common.HexToAddress("0xABCD"))
	errMsg := "job not found"
	sp.sendTxHistoryByJobIDErrorResponse(conn, errMsg)

	if sender.SentCount() != 1 {
		t.Fatalf("expected 1 sent message, got %d", sender.SentCount())
	}

	sent := sender.Sent[0]
	if sent.Command != command.TxRewardHistoryByJobID {
		t.Fatalf("expected command %q, got %q", command.TxRewardHistoryByJobID, sent.Command)
	}

	var resp pb.GetTransactionHistoryByJobIDResponse
	if err := proto.Unmarshal(sent.Data, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.Error != errMsg {
		t.Fatalf("expected error %q, got %q", errMsg, resp.Error)
	}
}
