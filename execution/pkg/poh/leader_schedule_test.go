package poh

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/stretchr/testify/assert"
)

var (
	testAddress1 = common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	testAddress2 = common.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	testAddress3 = common.HexToAddress("0xcccccccccccccccccccccccccccccccccccccccc")
)

func TestCalculateValidatorStakePercentage(t *testing.T) {
	validatorsWithStake := map[common.Address]*uint256.Int{}

	validatorsWithStake[testAddress1] = uint256.NewInt(20)
	validatorsWithStake[testAddress2] = uint256.NewInt(30)
	validatorsWithStake[testAddress3] = uint256.NewInt(50)

	rs := calculateValidatorStakePercentage(validatorsWithStake)
	assert.Equal(t, rs[testAddress1], uint64(200))
	assert.Equal(t, rs[testAddress2], uint64(300))
	assert.Equal(t, rs[testAddress3], uint64(500))

}

func TestCalculateSlot(t *testing.T) {
	validatorsWithStake := map[common.Address]*uint256.Int{}
	testAddress1 := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	testAddress2 := common.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	testAddress3 := common.HexToAddress("0xcccccccccccccccccccccccccccccccccccccccc")
	validatorsWithStake[testAddress1] = uint256.NewInt(0)
	validatorsWithStake[testAddress2] = uint256.NewInt(0)
	validatorsWithStake[testAddress3] = uint256.NewInt(0)
	seed, _ := uint256.FromHex("0xfefe")
	stakePercentage := calculateValidatorStakePercentage(validatorsWithStake)
	totalSlot := uint64(120)
	rs := calculateSlot(
		stakePercentage,
		seed,
		uint256.NewInt(0),
		totalSlot,
	)
	logger.Info(rs)
	assert.Equal(t, totalSlot, uint64(len(rs)))
}

func TestGetLeaderAtSlot(t *testing.T) {
	ls := &LeaderSchedule{
		slots: map[uint256.Int]common.Address{
			*uint256.NewInt(0): testAddress1,
			*uint256.NewInt(1): testAddress2,
			*uint256.NewInt(2): testAddress3,
		},
	}
	assert.Equal(t, ls.LeaderAtSlot(uint256.NewInt(0)), testAddress1)
	assert.Equal(t, ls.LeaderAtSlot(uint256.NewInt(1)), testAddress2)
	assert.Equal(t, ls.LeaderAtSlot(uint256.NewInt(2)), testAddress3)
}
