package sign_helper

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	pb_cross "github.com/meta-node-blockchain/meta-node/pkg/proto/cross_chain_proto"
	"google.golang.org/protobuf/proto"
)

// SignMessageWithPrivateKey ký message với private key và trả về signature
func SignMessageWithPrivateKey(message []byte, privateKeyHex string) ([]byte, error) {
	// Parse private key từ hex string
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	// Create Ethereum signed message
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)
	messageHash := crypto.Keccak256Hash([]byte(prefixedMessage))

	// Sign message
	signature, err := crypto.Sign(messageHash.Bytes(), privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign message: %w", err)
	}

	return signature, nil
}

// RecoverSignerAddress phục hồi địa chỉ người ký từ message và signature
func RecoverSignerAddress(message []byte, signatureBytes []byte) (common.Address, error) {
	if len(signatureBytes) < 65 {
		return common.Address{}, fmt.Errorf("invalid signature length: expected at least 65, got %d", len(signatureBytes))
	}

	// Create Ethereum signed message
	prefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)
	messageHash := crypto.Keccak256Hash([]byte(prefixedMessage))

	// Adjust V value (Ethereum uses 27/28, crypto.Ecrecover expects 0/1)
	signature := make([]byte, 65)
	copy(signature, signatureBytes)
	if signature[64] >= 27 {
		signature[64] -= 27
	}

	// Recover public key
	pubKey, err := crypto.SigToPub(messageHash.Bytes(), signature)
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to recover public key: %w", err)
	}

	return crypto.PubkeyToAddress(*pubKey), nil
}

// VerifyMintTransactionSignature verifies the signature of a mint transaction
// Returns true if the signature is valid and matches the expected signer address
func VerifyMintTransactionSignature(callDataInput []byte, expectedSignerAddress common.Address) (bool, error) {
	// 1. Unmarshal Proto trực tiếp (thay thế JSON Unmarshal)
	mintData := &pb_cross.MintData{}
	if err := proto.Unmarshal(callDataInput, mintData); err != nil {
		return false, fmt.Errorf("failed to unmarshal mint data (proto): %w", err)
	}
	// 2. Validate dữ liệu
	if len(mintData.Signature) == 0 {
		return false, fmt.Errorf("signature not found in mint data")
	}
	if len(mintData.SourceTxHash) == 0 {
		return false, fmt.Errorf("source_tx_hash not found in mint data")
	}

	// 3. Tái tạo message gốc để verify
	// LƯU Ý QUAN TRỌNG:
	// Trong hàm CreateMintTransaction, bạn đã ký vào chuỗi Hex: messageToSign := sourceTxHash.Hex()
	// Do đó, ở đây dù Proto lưu bytes, ta phải chuyển về Hex String để verify đúng message đó.
	sourceTxHash := common.BytesToHash(mintData.SourceTxHash)
	messageToVerify := sourceTxHash.Hex()

	// Nếu sau này bạn tối ưu hàm Sign để ký thẳng vào bytes ([]byte(messageToSign)),
	// thì chỗ này chỉ cần: messageToVerify := mintData.SourceTxHash
	// 4. Verify Signature
	// Proto lưu Signature là []byte gốc, nên KHÔNG cần hex.DecodeString nữa
	signerAddress, err := RecoverSignerAddress([]byte(messageToVerify), mintData.Signature)
	if err != nil {
		return false, fmt.Errorf("failed to recover signer address: %w", err)
	}

	return signerAddress == expectedSignerAddress, nil
}
