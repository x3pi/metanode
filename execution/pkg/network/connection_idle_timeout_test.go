package network

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

func writeCommand(t *testing.T, conn net.Conn, command string) {
	t.Helper()
	body, err := proto.Marshal(&pb.Message{Header: &pb.Header{Command: command}})
	if err != nil {
		t.Fatalf("marshal %s: %v", command, err)
	}
	length := make([]byte, 8)
	binary.LittleEndian.PutUint64(length, uint64(len(body)))
	if _, err := conn.Write(append(length, body...)); err != nil {
		t.Fatalf("write %s: %v", command, err)
	}
}

func commandBody(t *testing.T, command string, payloadSize int) []byte {
	t.Helper()
	body, err := proto.Marshal(&pb.Message{
		Header: &pb.Header{Command: command},
		Body:   make([]byte, payloadSize),
	})
	if err != nil {
		t.Fatalf("marshal %s: %v", command, err)
	}
	return body
}

func TestConnectionPingRefreshesIdleDeadline(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	cfg := DefaultConfig()
	cfg.AcceptedConnectionIdleTimeout = 120 * time.Millisecond
	conn, err := ConnectionFromTcpConnection(serverConn, cfg)
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}
	defer conn.Disconnect()

	_, errorChan := conn.RequestChan()
	time.Sleep(80 * time.Millisecond)
	writeCommand(t, clientConn, "Ping")

	select {
	case err := <-errorChan:
		t.Fatalf("ping did not refresh idle deadline: %v", err)
	case <-time.After(70 * time.Millisecond):
	}

	select {
	case err := <-errorChan:
		if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
			t.Fatalf("expected idle timeout, got %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected connection idle timeout after ping deadline")
	}
}

func TestConnectionValidMessageRefreshesIdleDeadline(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	cfg := DefaultConfig()
	cfg.AcceptedConnectionIdleTimeout = 100 * time.Millisecond
	conn, err := ConnectionFromTcpConnection(serverConn, cfg)
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}
	defer conn.Disconnect()

	_, errorChan := conn.RequestChan()
	time.Sleep(60 * time.Millisecond)
	writeCommand(t, clientConn, "GetChainId")

	select {
	case err := <-errorChan:
		t.Fatalf("valid non-Ping message did not refresh idle deadline: %v", err)
	case <-time.After(70 * time.Millisecond):
	}

	select {
	case err := <-errorChan:
		if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
			t.Fatalf("expected idle timeout, got %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected connection idle timeout after valid non-Ping message")
	}
}

func TestConnectionAllowsBoundedLargeMessageTransfer(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	cfg := DefaultConfig()
	cfg.AcceptedConnectionIdleTimeout = 100 * time.Millisecond
	conn, err := ConnectionFromTcpConnection(serverConn, cfg)
	if err != nil {
		t.Fatalf("create server connection: %v", err)
	}
	defer conn.Disconnect()

	_, errorChan := conn.RequestChan()
	body := commandBody(t, "GetChainId", 128*1024)
	length := make([]byte, 8)
	binary.LittleEndian.PutUint64(length, uint64(len(body)))
	if _, err := clientConn.Write(length); err != nil {
		t.Fatalf("write message length: %v", err)
	}

	// This exceeds the idle interval. The admitted frame has a bounded
	// transfer deadline based on its size, so it must still be accepted.
	time.Sleep(150 * time.Millisecond)
	if _, err := clientConn.Write(body); err != nil {
		t.Fatalf("write message body: %v", err)
	}

	select {
	case err := <-errorChan:
		t.Fatalf("large message transfer was cut off: %v", err)
	case <-time.After(70 * time.Millisecond):
	}
}
