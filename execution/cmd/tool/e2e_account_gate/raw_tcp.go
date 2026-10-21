package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/command"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// sendRawOverTCP opens a client connection to the node's TCP ingress, performs the InitConnection handshake and submits
// one raw Ethereum transaction (EIP-2718 envelope) with the SendRawTransaction command. It does not wait for a reply; the
// node reports a refused transaction only in its log, so callers check the log or poll the receipt.
func sendRawOverTCP(addr string, raw []byte) error {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect TCP to %s: %w", addr, err)
	}
	defer conn.Close()
	w := bufio.NewWriter(conn)
	send := func(cmd string, body []byte) error {
		msg := &pb.Message{Header: &pb.Header{Command: cmd, Version: "0.0.1.0", ID: uuid.New().String()}, Body: body}
		b, err := proto.Marshal(msg)
		if err != nil {
			return err
		}
		lengthBuf := make([]byte, 8)
		binary.LittleEndian.PutUint64(lengthBuf, uint64(len(b)))
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := w.Write(lengthBuf); err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
	initBody, err := proto.Marshal(&pb.InitConnection{Address: common.HexToAddress("0x01").Bytes(), Type: "client", Replace: true})
	if err != nil {
		return err
	}
	if err := send(command.InitConnection, initBody); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond) // the handshake completes asynchronously on the node
	if err := send(command.SendRawTransaction, raw); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond) // let the node read the frame before the connection closes
	return nil
}
