package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRawTxSender struct {
	lastMetaTx []byte
	lastEthTx  []byte
	lastPubKey []byte
	returnHash common.Hash
	returnErr  error
}

func (m *mockRawTxSender) SendRawTransactionWithDeviceKey(ctx context.Context, metaTx, ethTx, pubKey []byte) (common.Hash, error) {
	m.lastMetaTx = metaTx
	m.lastEthTx = ethTx
	m.lastPubKey = pubKey
	return m.returnHash, m.returnErr
}

func encodeTestPayload(metaTx, ethTx, pubKey []byte) []byte {
	buf := new(bytes.Buffer)
	_ = binary.Write(buf, binary.BigEndian, uint32(len(metaTx)))
	buf.Write(metaTx)
	_ = binary.Write(buf, binary.BigEndian, uint32(len(ethTx)))
	buf.Write(ethTx)
	_ = binary.Write(buf, binary.BigEndian, uint32(len(pubKey)))
	buf.Write(pubKey)
	return buf.Bytes()
}

func TestSendRawTransactionBin_MaxBytesLimit(t *testing.T) {
	sender := &mockRawTxSender{}
	handler := sendRawTransactionBinHandler(sender)

	// Create payload larger than 8MB (8MB + 1KB)
	largeBody := bytes.Repeat([]byte{0x42}, (8<<20)+1024)
	req := httptest.NewRequest(http.MethodPost, "/mtn/sendRawTransactionBin", bytes.NewReader(largeBody))
	rec := httptest.NewRecorder()

	handler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "request body too large")
}

func TestSendRawTransactionBin_MethodNotAllowed(t *testing.T) {
	sender := &mockRawTxSender{}
	handler := sendRawTransactionBinHandler(sender)

	req := httptest.NewRequest(http.MethodGet, "/mtn/sendRawTransactionBin", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestSendRawTransactionBin_InvalidPayload(t *testing.T) {
	sender := &mockRawTxSender{}
	handler := sendRawTransactionBinHandler(sender)

	// Send an invalid short payload
	req := httptest.NewRequest(http.MethodPost, "/mtn/sendRawTransactionBin", bytes.NewReader([]byte("short")))
	rec := httptest.NewRecorder()

	handler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid payload")
}

func TestSendRawTransactionBin_Success(t *testing.T) {
	expectedHash := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	sender := &mockRawTxSender{
		returnHash: expectedHash,
	}
	handler := sendRawTransactionBinHandler(sender)

	metaTx := []byte("meta-tx-data")
	ethTx := []byte("eth-tx-data")
	pubKey := []byte("pubkey-bytes")
	payload := encodeTestPayload(metaTx, ethTx, pubKey)

	req := httptest.NewRequest(http.MethodPost, "/mtn/sendRawTransactionBin", bytes.NewReader(payload))
	rec := httptest.NewRecorder()

	handler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, expectedHash.Bytes(), rec.Body.Bytes())
	assert.Equal(t, metaTx, sender.lastMetaTx)
	assert.Equal(t, ethTx, sender.lastEthTx)
	assert.Equal(t, pubKey, sender.lastPubKey)
}

func TestDecodeBinaryRawTxPayload_EdgeCases(t *testing.T) {
	// 1. Payload too short (< 12 bytes)
	_, _, _, err := decodeBinaryRawTxPayload([]byte{0, 1, 2})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "payload too short")

	// 2. Segment length exceeds remaining payload
	buf := new(bytes.Buffer)
	_ = binary.Write(buf, binary.BigEndian, uint32(100)) // claims 100 bytes
	buf.Write([]byte("short"))                            // only 5 bytes
	_ = binary.Write(buf, binary.BigEndian, uint32(0))
	_ = binary.Write(buf, binary.BigEndian, uint32(0))
	_, _, _, err = decodeBinaryRawTxPayload(buf.Bytes())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds remaining payload")

	// 3. Trailing bytes
	valid := encodeTestPayload([]byte("a"), []byte("b"), []byte("c"))
	trailing := append(valid, []byte("extra")...)
	_, _, _, err = decodeBinaryRawTxPayload(trailing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected trailing bytes")

	// 4. Empty segments are allowed
	m, e, p, err := decodeBinaryRawTxPayload(encodeTestPayload(nil, nil, nil))
	require.NoError(t, err)
	assert.Nil(t, m)
	assert.Nil(t, e)
	assert.Nil(t, p)
}

func BenchmarkDecodeBinaryRawTxPayload(b *testing.B) {
	metaTx := bytes.Repeat([]byte{0x01}, 256)
	ethTx := bytes.Repeat([]byte{0x02}, 512)
	pubKey := bytes.Repeat([]byte{0x03}, 48)
	payload := encodeTestPayload(metaTx, ethTx, pubKey)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, _, err := decodeBinaryRawTxPayload(payload)
		if err != nil {
			b.Fatal(err)
		}
	}
}

