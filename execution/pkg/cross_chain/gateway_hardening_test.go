package cross_chain

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGateway_Timeout_RejectsClaim(t *testing.T) {
	engine, _ := setupTestGatewayEngine()
	sender := common.HexToAddress("0x1111111111111111111111111111111111111111")
	target := common.HexToAddress("0x2222222222222222222222222222222222222222")
	relayer := common.HexToAddress("0x9999999999999999999999999999999999999999")

	msg := CrossChainMessage{
		MessageID:        common.HexToHash("0x11"),
		SourceChainID:    101,
		DestChainID:      engine.LocalChainID,
		Sender:           sender,
		Target:           target,
		Value:            big.NewInt(100),
		AssetID:          big.NewInt(0),
		Payload:          []byte{},
		TimeoutTimestamp: 5000,
	}

	proof := MerkleProof{
		LeafIndex: 0,
		Siblings:  []common.Hash{},
	}

	leafHash := ComputeMessageLeafHash(msg)
	commitRoot := leafHash
	engine.AttestedCommits[fmt.Sprintf("101:%s:0", commitRoot.Hex())] = AttestedCommit{
		CommitRoot:    commitRoot,
		FundedAmount:  big.NewInt(100),
		ClaimedAmount: big.NewInt(0),
	}

	status, err := engine.ClaimMessage(msg, proof, commitRoot, relayer, 4000)
	require.NoError(t, err)
	assert.Equal(t, MessageStatusSuccess, status)

	msg2 := msg
	msg2.MessageID = common.HexToHash("0x33")
	leafHash2 := ComputeMessageLeafHash(msg2)
	commitRoot2 := leafHash2
	engine.AttestedCommits[fmt.Sprintf("101:%s:0", commitRoot2.Hex())] = AttestedCommit{
		CommitRoot:    commitRoot2,
		FundedAmount:  big.NewInt(100),
		ClaimedAmount: big.NewInt(0),
	}

	status2, err2 := engine.ClaimMessage(msg2, proof, commitRoot2, relayer, 6000)
	require.NoError(t, err2)
	assert.Equal(t, MessageStatusFailedTimeout, status2)
}
