package nomt_ffi

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNOMT_GenerateAndVerifyProof(t *testing.T) {
	tempDir := t.TempDir()
	handle, err := Open(tempDir, 1, 64, 64, 64000, true)
	require.NoError(t, err)
	defer handle.Close()

	var key [32]byte
	_, _ = rand.Read(key[:])
	value := []byte("merkle_proof_verification_value_123")

	// 1. Session write & commit
	session := BeginSession(handle)
	err = session.BatchWrite([][32]byte{key}, [][]byte{value})
	require.NoError(t, err)

	root, finSession, err := session.Finish(handle)
	require.NoError(t, err)
	require.NotNil(t, finSession)

	err = finSession.CommitPayload(handle)
	require.NoError(t, err)

	// 2. Generate proof
	proof, err := handle.GenerateProof(key)
	require.NoError(t, err)
	require.NotEmpty(t, proof)

	// 3. Verify proof with correct value
	valid, err := VerifyProof(root, key, value, proof)
	require.NoError(t, err)
	assert.True(t, valid, "proof should be valid for correct key and value")

	// 4. Verify proof with wrong value -> should return false (not valid)
	wrongVal := []byte("wrong_value")
	validWrong, err := VerifyProof(root, key, wrongVal, proof)
	require.NoError(t, err)
	assert.False(t, validWrong, "proof should be invalid for wrong value")

	// 5. Verify proof with wrong root -> should return false
	var wrongRoot [32]byte
	wrongRoot[0] = root[0] ^ 0xFF
	validWrongRoot, err := VerifyProof(wrongRoot, key, value, proof)
	require.NoError(t, err)
	assert.False(t, validWrongRoot, "proof should be invalid for wrong root")

	// 6. Test non-existence proof
	var nonExistentKey [32]byte
	nonExistentKey[0] = 0xFE
	nonExistentKey[31] = 0xFE
	nonExistentProof, err := handle.GenerateProof(nonExistentKey)
	require.NoError(t, err)
	require.NotEmpty(t, nonExistentProof)

	validNonExistent, err := VerifyProof(root, nonExistentKey, nil, nonExistentProof)
	require.NoError(t, err)
	assert.True(t, validNonExistent, "proof should confirm non-existence when value is nil")
}
