package main

import (
	"flag"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"

	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
)

// ValidatorInfo struct mirroring the fields required for committee
type ValidatorInfo struct {
	Address                    common.Address
	Owner                      common.Address
	PrimaryAddress             string
	WorkerAddress              string
	P2PAddress                 string
	Name                       string
	Description                string
	Website                    string
	Image                      string
	CommissionRate             *big.Int
	MinSelfDelegation          *big.Int
	TotalStakedAmount          *big.Int
	AccumulatedRewardsPerShare *big.Int
	Hostname                   string
	AuthorityKey               string
	ProtocolKey                string
	NetworkKey                 string
}

func main() {
	blockNum := flag.Int64("block", -1, "Block number to query (default: latest)")
	flag.Parse()

	blockTag := "latest"
	if *blockNum >= 0 {
		blockTag = hexutil.EncodeUint64(uint64(*blockNum))
		fmt.Printf("Retrieving state for Block: %d (%s)\n", *blockNum, blockTag)
	} else {
		fmt.Println("Retrieving state for Block: latest")
	}

	fmt.Printf("VALIDATOR_CONTRACT_ADDRESS: %s\n", mt_common.VALIDATOR_CONTRACT_ADDRESS.Hex())
	fmt.Printf("IDENTIFIER_STORAGE selector: %s\n", utils.GetAddressSelector(mt_common.IDENTIFIER_STORAGE).Hex())

	// Connect to Go Master RPC (8747) using raw RPC client
	client, err := rpc.Dial("http://localhost:8545")
	if err != nil {
		fmt.Printf("Error creating client: %v\n", err)
		return
	}
	defer client.Close()

	// Address of Validator Contract
	contractAddr := common.HexToAddress("0x0000000000000000000000000000000000001001")
	// Sender address for calls
	fromAddr := common.HexToAddress("0xa87c6FD018Da82a52158B0328D61BAc29b556e86")

	// ============================================================
	// Demo: GetValidatorCount
	// ============================================================
	fmt.Println("\n=== GetValidatorCount ===")
	count, err := GetValidatorCount(client, fromAddr, contractAddr, blockTag)
	if err != nil {
		fmt.Printf("Error getting validator count: %v\n", err)
	} else {
		fmt.Printf("Validator Count: %d\n", count)
	}

	// ============================================================
	// Demo: GetCommittee (full list)
	// ============================================================
	fmt.Println("\n=== GetCommittee ===")
	validators, err := GetCommittee(client, contractAddr, blockTag)
	if err != nil {
		fmt.Printf("Error getting committee: %v\n", err)
		return
	}

	fmt.Printf("Total Validators: %d\n", len(validators))

	for i, v := range validators {
		fmt.Printf("  [%d] Address: %s\n", i, v.Address.Hex())
		fmt.Printf("    Name: %s\n", v.Name)
		fmt.Printf("    Description: %s\n", v.Description)
		fmt.Printf("    Website: %s\n", v.Website)
		fmt.Printf("    Image: %s\n", v.Image)
		fmt.Printf("    Hostname (address): %s\n", v.Hostname)
		fmt.Printf("    Stake (TotalStakedAmount): %s\n", v.TotalStakedAmount.String())
		fmt.Printf("    Authority Key: %s\n", v.AuthorityKey)
		fmt.Printf("    Protocol Key: %s\n", v.ProtocolKey)
		fmt.Printf("    Network Key: %s\n", v.NetworkKey)
		fmt.Printf("    Commission Rate: %s\n", v.CommissionRate.String())
		fmt.Printf("    Min Self Delegation: %s\n", v.MinSelfDelegation.String())
		fmt.Printf("    Accumulated Rewards Per Share: %s\n", v.AccumulatedRewardsPerShare.String())
		fmt.Printf("    Owner: %s\n", v.Owner.Hex())
		fmt.Printf("    Primary Address: %s\n", v.PrimaryAddress)
		fmt.Printf("    Worker Address: %s\n", v.WorkerAddress)
		fmt.Printf("    P2P Address: %s\n", v.P2PAddress)
	}

	// Print epoch information (similar to Rust response)
	fmt.Println("\n=== Epoch Info ===")
	var epochHeader *types.Header
	err = client.Call(&epochHeader, "eth_getBlockByNumber", blockTag, false)
	if err == nil && epochHeader != nil {
		timestampMs := epochHeader.Time * 1000
		fmt.Printf("Epoch Timestamp (ms): %d\n", timestampMs)
		fmt.Printf("Epoch Time: %s\n", time.Unix(int64(epochHeader.Time), 0).Format(time.RFC3339))
	}

	// ============================================================
	// Demo: GetValidatorAddressByIndex
	// ============================================================
	fmt.Println("\n=== GetValidatorAddressByIndex ===")
	if len(validators) > 0 {
		addr, err := GetValidatorAddressByIndex(client, fromAddr, contractAddr, 0, blockTag)
		if err != nil {
			fmt.Printf("Error getting validator address at index 0: %v\n", err)
		} else {
			fmt.Printf("Validator at index 0: %s\n", addr.Hex())
		}
	}

	// ============================================================
	// Demo: GetValidatorByAddress
	// ============================================================
	fmt.Println("\n=== GetValidatorByAddress ===")
	if len(validators) > 0 {
		validatorAddr := validators[0].Address
		info, err := GetValidatorByAddress(client, fromAddr, contractAddr, validatorAddr, blockTag)
		if err != nil {
			fmt.Printf("Error getting validator info: %v\n", err)
		} else {
			fmt.Printf("Validator %s:\n", validatorAddr.Hex())
			fmt.Printf("  Owner: %s\n", info.Owner.Hex())
			fmt.Printf("  Name: %s\n", info.Name)
			fmt.Printf("  TotalStakedAmount: %s\n", info.TotalStakedAmount.String())
		}
	}

	// Note: validatorIndexes(address) is not exposed in the public ABI
	// It is only used internally by the handler

	// ============================================================
	// Demo: GetBalanceOf (returns account's total ETH balance, NOT staked amount)
	// ============================================================
	fmt.Println("\n=== GetBalanceOf ===")
	balance, err := GetBalanceOf(client, fromAddr, contractAddr, fromAddr, blockTag)
	if err != nil {
		fmt.Printf("Error getting balance: %v\n", err)
	} else {
		fmt.Printf("Account balance of %s: %s\n", fromAddr.Hex(), balance.String())
	}

	// ============================================================
	// Demo: GetDelegation (returns actual staked amount and reward debt)
	// ============================================================
	fmt.Println("\n=== GetDelegation ===")
	if len(validators) > 0 {
		validatorAddr := validators[0].Address
		delegator := validatorAddr // Check self-stake (validator stakes to themselves)
		amount, rewardDebt, err := GetDelegation(client, fromAddr, contractAddr, validatorAddr, delegator, blockTag)
		if err != nil {
			fmt.Printf("Error getting delegation: %v\n", err)
		} else {
			fmt.Printf("Delegation of %s on validator %s:\n", delegator.Hex(), validatorAddr.Hex())
			fmt.Printf("  Staked Amount: %s\n", amount.String())
			fmt.Printf("  Reward Debt: %s\n", rewardDebt.String())
		}
	}

	// ============================================================
	// Demo: GetPendingRewards
	// ============================================================
	fmt.Println("\n=== GetPendingRewards ===")
	if len(validators) > 0 {
		validatorAddr := validators[0].Address
		delegator := fromAddr
		// Note: ABI signature is getPendingRewards(delegator, validator)
		rewards, err := GetPendingRewards(client, fromAddr, contractAddr, delegator, validatorAddr, blockTag)
		if err != nil {
			fmt.Printf("Error getting pending rewards: %v\n", err)
		} else {
			fmt.Printf("Pending rewards of %s on validator %s: %s\n", delegator.Hex(), validatorAddr.Hex(), rewards.String())
		}
	}

	// ============================================================
	// Get Block Header Info
	// ============================================================
	fmt.Println("\n=== Block Info ===")
	var head *types.Header
	err = client.Call(&head, "eth_getBlockByNumber", blockTag, false)
	if err != nil {
		fmt.Printf("Error getting block header: %v\n", err)
	} else {
		if head != nil {
			timestampMs := head.Time * 1000
			fmt.Printf("Block Number: %d\n", head.Number.Uint64())
			fmt.Printf("Epoch Timestamp: %d ms (%s)\n", timestampMs, time.Unix(int64(head.Time), 0).Format(time.RFC3339))
		}
	}

	fmt.Println("\nDone")
}

// ============================================================
// Helper Functions for Validator Contract
// ============================================================

// GetValidatorCount returns the total number of validators
// Method: getValidatorCount() or selector 0x7071688a
func GetValidatorCount(client *rpc.Client, from, contractAddr common.Address, blockTag string) (int64, error) {
	// Method selector for getValidatorCount()
	callData := common.FromHex("0x7071688a")

	result, err := callContract(client, from, contractAddr, callData, blockTag)
	if err != nil {
		return 0, err
	}

	countInt := new(big.Int).SetBytes(result)
	return countInt.Int64(), nil
}

// GetValidatorAddressByIndex returns validator address at given index
// Method: validatorAddresses(uint256)
func GetValidatorAddressByIndex(client *rpc.Client, from, contractAddr common.Address, index int64, blockTag string) (common.Address, error) {
	methodId := crypto.Keccak256Hash([]byte("validatorAddresses(uint256)")).Bytes()[:4]
	indexBig := big.NewInt(index)
	paddedIndex := common.LeftPadBytes(indexBig.Bytes(), 32)
	callData := append(methodId, paddedIndex...)

	result, err := callContract(client, from, contractAddr, callData, blockTag)
	if err != nil {
		return common.Address{}, err
	}

	if len(result) >= 32 {
		return common.BytesToAddress(result), nil
	}
	return common.Address{}, fmt.Errorf("invalid response length")
}

// GetValidatorByAddress returns full validator info by address
// Method: validators(address)
func GetValidatorByAddress(client *rpc.Client, from, contractAddr, validatorAddr common.Address, blockTag string) (*ValidatorInfo, error) {
	validatorsAbiJSON := `[{"inputs":[{"internalType":"address","name":"","type":"address"}],"name":"validators","outputs":[{"internalType":"address","name":"owner","type":"address"},{"internalType":"string","name":"primaryAddress","type":"string"},{"internalType":"string","name":"workerAddress","type":"string"},{"internalType":"string","name":"p2pAddress","type":"string"},{"internalType":"string","name":"name","type":"string"},{"internalType":"string","name":"description","type":"string"},{"internalType":"string","name":"website","type":"string"},{"internalType":"string","name":"image","type":"string"},{"internalType":"uint256","name":"commissionRate","type":"uint256"},{"internalType":"uint256","name":"minSelfDelegation","type":"uint256"},{"internalType":"uint256","name":"totalStakedAmount","type":"uint256"},{"internalType":"uint256","name":"accumulatedRewardsPerShare","type":"uint256"},{"internalType":"string","name":"hostname","type":"string"},{"internalType":"string","name":"authority_key","type":"string"},{"internalType":"string","name":"protocol_key","type":"string"},{"internalType":"string","name":"network_key","type":"string"}],"stateMutability":"view","type":"function"}]`

	parsedAbi, err := abi.JSON(strings.NewReader(validatorsAbiJSON))
	if err != nil {
		return nil, fmt.Errorf("parsing ABI: %v", err)
	}

	methodID := parsedAbi.Methods["validators"].ID
	paddedAddr := common.LeftPadBytes(validatorAddr.Bytes(), 32)
	callData := append(methodID, paddedAddr...)

	result, err := callContract(client, from, contractAddr, callData, blockTag)
	if err != nil {
		return nil, err
	}

	unpacked, err := parsedAbi.Unpack("validators", result)
	if err != nil {
		return nil, fmt.Errorf("unpacking response: %v", err)
	}

	if len(unpacked) >= 16 {
		return &ValidatorInfo{
			Address:                    validatorAddr,
			Owner:                      unpacked[0].(common.Address),
			PrimaryAddress:             unpacked[1].(string),
			WorkerAddress:              unpacked[2].(string),
			P2PAddress:                 unpacked[3].(string),
			Name:                       unpacked[4].(string),
			Description:                unpacked[5].(string),
			Website:                    unpacked[6].(string),
			Image:                      unpacked[7].(string),
			CommissionRate:             unpacked[8].(*big.Int),
			MinSelfDelegation:          unpacked[9].(*big.Int),
			TotalStakedAmount:          unpacked[10].(*big.Int),
			AccumulatedRewardsPerShare: unpacked[11].(*big.Int),
			Hostname:                   unpacked[12].(string),
			AuthorityKey:               unpacked[13].(string),
			ProtocolKey:                unpacked[14].(string),
			NetworkKey:                 unpacked[15].(string),
		}, nil
	}
	return nil, fmt.Errorf("unexpected number of return values")
}

// GetValidatorIndex returns the index of a validator by address
// Method: validatorIndexes(address) - NOTE: This method is not in the public ABI
// It is only used internally, so this function may not work via RPC
// func GetValidatorIndex(client *rpc.Client, from, contractAddr, validatorAddr common.Address, blockTag string) (int64, error) {
// 	methodId := crypto.Keccak256Hash([]byte("validatorIndexes(address)")).Bytes()[:4]
// 	paddedAddr := common.LeftPadBytes(validatorAddr.Bytes(), 32)
// 	callData := append(methodId, paddedAddr...)
// 	result, err := callContract(client, from, contractAddr, callData, blockTag)
// 	if err != nil {
// 		return 0, err
// 	}
// 	indexInt := new(big.Int).SetBytes(result)
// 	return indexInt.Int64(), nil
// }

// GetBalanceOf returns the total ETH/native token balance of an account
// NOTE: This is NOT the staked balance! Use GetDelegation for staked amounts.
// Method: balanceOf(address account)
func GetBalanceOf(client *rpc.Client, from, contractAddr, account common.Address, blockTag string) (*big.Int, error) {
	methodId := crypto.Keccak256Hash([]byte("balanceOf(address)")).Bytes()[:4]
	paddedAccount := common.LeftPadBytes(account.Bytes(), 32)
	callData := append(methodId, paddedAccount...)

	result, err := callContract(client, from, contractAddr, callData, blockTag)
	if err != nil {
		return nil, err
	}

	balance := new(big.Int).SetBytes(result)
	return balance, nil
}

// GetDelegation returns the staked amount and reward debt of a delegator on a validator
// Method: delegations(address validator, address delegator)
func GetDelegation(client *rpc.Client, from, contractAddr, validatorAddr, delegator common.Address, blockTag string) (*big.Int, *big.Int, error) {
	methodId := crypto.Keccak256Hash([]byte("delegations(address,address)")).Bytes()[:4]
	paddedValidator := common.LeftPadBytes(validatorAddr.Bytes(), 32)
	paddedDelegator := common.LeftPadBytes(delegator.Bytes(), 32)
	callData := append(methodId, paddedValidator...)
	callData = append(callData, paddedDelegator...)

	result, err := callContract(client, from, contractAddr, callData, blockTag)
	if err != nil {
		return nil, nil, err
	}

	// Response is (uint256 amount, uint256 rewardDebt)
	if len(result) < 64 {
		return big.NewInt(0), big.NewInt(0), nil
	}
	amount := new(big.Int).SetBytes(result[0:32])
	rewardDebt := new(big.Int).SetBytes(result[32:64])
	return amount, rewardDebt, nil
}

// GetPendingRewards returns the pending rewards of a delegator on a validator
// Method: getPendingRewards(address _delegator, address _validatorAddress)
func GetPendingRewards(client *rpc.Client, from, contractAddr, delegator, validatorAddr common.Address, blockTag string) (*big.Int, error) {
	methodId := crypto.Keccak256Hash([]byte("getPendingRewards(address,address)")).Bytes()[:4]
	paddedDelegator := common.LeftPadBytes(delegator.Bytes(), 32)
	paddedValidator := common.LeftPadBytes(validatorAddr.Bytes(), 32)
	callData := append(methodId, paddedDelegator...)
	callData = append(callData, paddedValidator...)

	result, err := callContract(client, from, contractAddr, callData, blockTag)
	if err != nil {
		return nil, err
	}

	rewards := new(big.Int).SetBytes(result)
	return rewards, nil
}

// GetCommittee retrieves the list of active validators from the contract
func GetCommittee(client *rpc.Client, contractAddr common.Address, blockTag string) ([]ValidatorInfo, error) {
	// Address of Node 4 (Sender) - used for calls
	fromAddr := common.HexToAddress("0xc98223c939f0313d5b5dace9c3c3759af4de663a")

	// 1. Get Validator Count
	count, err := GetValidatorCount(client, fromAddr, contractAddr, blockTag)
	if err != nil {
		return nil, fmt.Errorf("getting validator count: %v", err)
	}

	var validators []ValidatorInfo

	for i := int64(0); i < count; i++ {
		// Get validator address at index
		addr, err := GetValidatorAddressByIndex(client, fromAddr, contractAddr, i, blockTag)
		if err != nil {
			fmt.Printf("Error getting validator address at index %d: %v\n", i, err)
			continue
		}

		// Get full validator info
		info, err := GetValidatorByAddress(client, fromAddr, contractAddr, addr, blockTag)
		if err != nil {
			fmt.Printf("Error getting validator info for %s: %v\n", addr.Hex(), err)
			continue
		}

		validators = append(validators, *info)
	}

	return validators, nil
}

// callContract performs an eth_call with standard Ethereum format
func callContract(client *rpc.Client, from, to common.Address, data []byte, blockTag string) ([]byte, error) {
	// Use standard Ethereum eth_call parameter format
	// The rpc-client expects a JSON object with {from, to, data}
	callObject := map[string]interface{}{
		"from": from.Hex(),
		"to":   to.Hex(),
		"data": hexutil.Encode(data),
	}

	var result hexutil.Bytes
	err := client.Call(&result, "eth_call", callObject, blockTag)
	if err != nil {
		return nil, fmt.Errorf("rpc call error: %v", err)
	}

	return result, nil
}
