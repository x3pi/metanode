package executor

import (
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

// WriteMessage encodes and writes a protobuf message with 4-byte BigEndian length prefix.
func WriteMessage(writer io.Writer, msg proto.Message) error {
	// 1. Serialize the protobuf message to bytes.
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("could not marshal message: %w", err)
	}
	// 2. Create a 4-byte buffer for the length.
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(data)))

	// 3. Write the length prefix to the stream.
	if _, err := writer.Write(lenBuf); err != nil {
		return fmt.Errorf("could not write message length: %w", err)
	}
	// 4. Write the actual message data to the stream.
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("could not write message data: %w", err)
	}
	return nil
}

// ReadMessage reads a 4-byte BigEndian length prefix and then the protobuf message.
func ReadMessage(reader io.Reader, msg proto.Message) error {
	// 1. Read 4-byte length.
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(reader, lenBuf); err != nil {
		// Return io.EOF directly to allow the caller to detect closed connections.
		if err == io.EOF {
			return io.EOF
		}
		return fmt.Errorf("could not read message length: %w", err)
	}
	// 2. Decode the length.
	msgLen := binary.BigEndian.Uint32(lenBuf)

	// LOG: Help debug where the 100MB payload drops
	if msgLen > 1024*1024 {
		fmt.Printf("📥 [IPC READ] Incoming message length: %d bytes (~%d MB)\n", msgLen, msgLen/(1024*1024))
	}

	// SECURITY FIX: Prevent Memory Exhaustion (OOM) via excessively large IPC payloads.
	// Reject any message larger than 512MB.
	const MaxIPCMessageLength = 512 * 1024 * 1024 // 512 MB
	if msgLen > MaxIPCMessageLength {
		return fmt.Errorf("could not read message: payload size %d exceeds maximum limit of %d bytes", msgLen, MaxIPCMessageLength)
	}

	// 3. Read the exact number of bytes for the message body.
	msgBuf := make([]byte, msgLen)
	if _, err := io.ReadFull(reader, msgBuf); err != nil {
		return fmt.Errorf("could not read message body: %w", err)
	}
	// 4. Unmarshal the bytes into the provided protobuf message struct.
	if err := proto.Unmarshal(msgBuf, msg); err != nil {
		return fmt.Errorf("could not unmarshal message: %w", err)
	}

	return nil
}
