package state

import (
	"math/big"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ValidatorState interface
type ValidatorState interface {
	Address() common.Address
	TotalStakedAmount() *big.Int
	IsJailed() bool
	CommissionRate() uint64
	Name() string
	Description() string
	Website() string
	Image() string
	PrimaryAddress() string
	WorkerAddress() string
	P2PAddress() string
	PubKeyBls() string
	PubKeySecp() string
	ProtocolKey() string  // Protocol key (Ed25519) - tương thích với committee.json
	NetworkKey() string   // Network key (Ed25519) - tương thích với committee.json
	Hostname() string     // Hostname - tương thích với committee.json
	AuthorityKey() string // Authority key (BLS) - tương thích với committee.json

	MinSelfDelegation() *big.Int
	AccumulatedRewardsPerShare() *big.Int
	ResetRewardDebt(delegatorAddress common.Address)
	GetDelegation(delegatorAddress common.Address) (amount *big.Int, rewardDebt *big.Int)

	SetDelegate(delegatorAddress common.Address, amount *big.Int)
	SetUndelegate(delegatorAddress common.Address, amount *big.Int) error
	DistributeRewards(totalReward *big.Int) (delegatorsReward *big.Int)
	WithdrawReward(delegatorAddress common.Address) *big.Int

	SetJailed(jailed bool, until time.Time)
	SetCommissionRate(rate uint64) error
	SetName(name string)
	SetDescription(desc string)
	SetWebsite(url string)
	SetImage(url string)
	SetMinSelfDelegation(amount *big.Int)
	SetPrimaryAddress(address string)
	SetWorkerAddress(address string)
	SetP2PAddress(address string)
	SetPubKeyBls(pubKey string)
	SetPubKeySecp(pubKey string)
	SetProtocolKey(pubKey string)  // Set protocol key (Ed25519) - tương thích với committee.json
	SetNetworkKey(pubKey string)   // Set network key (Ed25519) - tương thích với committee.json
	SetHostname(name string)       // Set hostname
	SetAuthorityKey(pubKey string) // Set authority key (BLS) - tương thích với committee.json

	// Serialization
	Marshal() ([]byte, error)
	Unmarshal(data []byte) error
}

type validatorStateImpl struct {
	*pb.Validator
}

var PRECISION = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

func NewValidatorState(address common.Address) ValidatorState {
	return &validatorStateImpl{
		Validator: &pb.Validator{
			Address:           address.Hex(),
			IsJailed:          false,
			CommissionRate:    0,
			MinSelfDelegation: "0",
			// SỬA ĐỔI: Khởi tạo slice thay vì map
			Delegators:                 make([]*pb.StringStringPair, 0),
			DelegatorRewardIndexes:     make([]*pb.StringStringPair, 0),
			JailedUntil:                timestamppb.New(time.Unix(0, 0)),
			AccumulatedRewardsPerShare: "0",
		},
	}
}

// --- Các hàm Getter tường minh để thỏa mãn Interface ---
func (vs *validatorStateImpl) CommissionRate() uint64  { return vs.GetCommissionRate() }
func (vs *validatorStateImpl) IsJailed() bool          { return vs.GetIsJailed() }
func (vs *validatorStateImpl) Address() common.Address { return common.HexToAddress(vs.GetAddress()) }
func (vs *validatorStateImpl) Name() string            { return vs.GetName() }
func (vs *validatorStateImpl) Hostname() string        { return vs.GetHostname() } // Added Hostname
func (vs *validatorStateImpl) Description() string     { return vs.GetDescription() }
func (vs *validatorStateImpl) Website() string         { return vs.GetWebsite() }
func (vs *validatorStateImpl) Image() string           { return vs.GetImage() }
func (vs *validatorStateImpl) MinSelfDelegation() *big.Int {
	amount, ok := new(big.Int).SetString(vs.GetMinSelfDelegation(), 10)
	if !ok {
		return big.NewInt(0)
	}
	return amount
}
func (vs *validatorStateImpl) PrimaryAddress() string {
	return vs.GetPrimaryAddress()
}
func (vs *validatorStateImpl) WorkerAddress() string {
	return vs.GetWorkerAddress()
}
func (vs *validatorStateImpl) P2PAddress() string {
	return vs.GetP2PAddress()
}
func (vs *validatorStateImpl) AccumulatedRewardsPerShare() *big.Int {
	amount, ok := new(big.Int).SetString(vs.GetAccumulatedRewardsPerShare(), 10)
	if !ok {
		return big.NewInt(0)
	}
	return amount
}
func (vs *validatorStateImpl) PubKeyBls() string {
	return vs.GetPubkeyBls()
}
func (vs *validatorStateImpl) PubKeySecp() string {
	return vs.GetPubkeySecp()
}
func (vs *validatorStateImpl) ProtocolKey() string {
	// Ưu tiên dùng protocol_key, fallback về pubkey_secp
	if protocolKey := vs.GetProtocolKey(); protocolKey != "" {
		return protocolKey
	}
	return vs.GetPubkeySecp()
}
func (vs *validatorStateImpl) NetworkKey() string {
	// Ưu tiên dùng network_key, fallback về pubkey_secp
	if networkKey := vs.GetNetworkKey(); networkKey != "" {
		return networkKey
	}
	return vs.GetPubkeySecp()
}
func (vs *validatorStateImpl) AuthorityKey() string {
	// Ưu tiên dùng authority_key, fallback về pubkey_bls
	if authorityKey := vs.GetAuthorityKey(); authorityKey != "" {
		return authorityKey
	}
	return vs.GetPubkeyBls()
}

// --- Các hàm Setter ---
func (vs *validatorStateImpl) SetName(name string) {
	vs.Validator.Name = name
}
func (vs *validatorStateImpl) SetDescription(desc string) {
	vs.Validator.Description = desc
}
func (vs *validatorStateImpl) SetWebsite(url string) {
	vs.Validator.Website = url
}
func (vs *validatorStateImpl) SetImage(url string) {
	vs.Validator.Image = url
}
func (vs *validatorStateImpl) SetMinSelfDelegation(amount *big.Int) {
	vs.Validator.MinSelfDelegation = amount.String()
}
func (vs *validatorStateImpl) SetJailed(jailed bool, until time.Time) {
	vs.Validator.IsJailed = jailed
	vs.Validator.JailedUntil = timestamppb.New(until)
}
func (vs *validatorStateImpl) SetPrimaryAddress(address string) {
	vs.Validator.PrimaryAddress = address
}
func (vs *validatorStateImpl) SetWorkerAddress(address string) {
	vs.Validator.WorkerAddress = address
}
func (vs *validatorStateImpl) SetP2PAddress(address string) {
	vs.Validator.P2PAddress = address
}
func (vs *validatorStateImpl) SetPubKeyBls(pubKey string) {
	vs.Validator.PubkeyBls = pubKey
}
func (vs *validatorStateImpl) SetPubKeySecp(pubKey string) {
	vs.Validator.PubkeySecp = pubKey
}
func (vs *validatorStateImpl) SetProtocolKey(pubKey string) {
	vs.Validator.ProtocolKey = pubKey
	// Also set pubkey_secp for backward compatibility
	if vs.Validator.PubkeySecp == "" {
		vs.Validator.PubkeySecp = pubKey
	}
}
func (vs *validatorStateImpl) SetNetworkKey(pubKey string) {
	vs.Validator.NetworkKey = pubKey
}
func (vs *validatorStateImpl) SetHostname(name string) {
	vs.Validator.Hostname = name
}
func (vs *validatorStateImpl) SetAuthorityKey(pubKey string) {
	vs.Validator.AuthorityKey = pubKey
	// Also set pubkey_bls for backward compatibility
	if vs.Validator.PubkeyBls == "" {
		vs.Validator.PubkeyBls = pubKey
	}
}
func (vs *validatorStateImpl) SetCommissionRate(rate uint64) error {
	if rate > 10000 {
		return types.ErrInvalidCommissionRate
	}
	vs.Validator.CommissionRate = rate
	return nil
}

// TotalStakedAmount tính toán tổng số tiền đã stake.
func (vs *validatorStateImpl) TotalStakedAmount() *big.Int {
	totalStake := big.NewInt(0)
	// SỬA ĐỔI: Duyệt qua slice
	for _, pair := range vs.GetDelegators() {
		amount, ok := new(big.Int).SetString(pair.Value, 10)
		if !ok {
			amount = big.NewInt(0)
		}
		totalStake.Add(totalStake, amount)
	}
	return totalStake
}

func (vs *validatorStateImpl) GetDelegation(delegatorAddress common.Address) (amount *big.Int, rewardDebt *big.Int) {
	delegatorAddrStr := delegatorAddress.Hex()
	amount = big.NewInt(0)
	rewardDebt = big.NewInt(0)

	// SỬA ĐỔI: Tìm kiếm trong slice Delegators
	for _, pair := range vs.Delegators {
		if pair.Key == delegatorAddrStr {
			val, ok := new(big.Int).SetString(pair.Value, 10)
			if ok {
				amount = val
			}
			break
		}
	}

	// SỬA ĐỔI: Tìm kiếm trong slice DelegatorRewardIndexes
	for _, pair := range vs.DelegatorRewardIndexes {
		if pair.Key == delegatorAddrStr {
			val, ok := new(big.Int).SetString(pair.Value, 10)
			if ok {
				rewardDebt = val
			}
			break
		}
	}

	return amount, rewardDebt
}

// --- Logic Ủy quyền và Phần thưởng (ĐÃ SỬA ĐỔI) ---

func (vs *validatorStateImpl) SetDelegate(delegatorAddress common.Address, amount *big.Int) {
	delegatorAddrStr := delegatorAddress.Hex()

	// --- Cập nhật lượng stake ---
	var existingDelegator *pb.StringStringPair
	for _, pair := range vs.Delegators {
		if pair.Key == delegatorAddrStr {
			existingDelegator = pair
			break
		}
	}

	currentAmount := big.NewInt(0)
	if existingDelegator != nil {
		currentAmount, _ = new(big.Int).SetString(existingDelegator.Value, 10)
	}
	currentAmount.Add(currentAmount, amount)

	if existingDelegator != nil {
		existingDelegator.Value = currentAmount.String()
	} else {
		vs.Delegators = append(vs.Delegators, &pb.StringStringPair{
			Key:   delegatorAddrStr,
			Value: currentAmount.String(),
		})
	}

	// --- Cập nhật reward debt ---
	accumulatedRewards := vs.AccumulatedRewardsPerShare()
	newRewardDebt := new(big.Int).Mul(currentAmount, accumulatedRewards)
	newRewardDebt.Div(newRewardDebt, PRECISION)

	var existingRewardIndex *pb.StringStringPair
	for _, pair := range vs.DelegatorRewardIndexes {
		if pair.Key == delegatorAddrStr {
			existingRewardIndex = pair
			break
		}
	}

	if existingRewardIndex != nil {
		existingRewardIndex.Value = newRewardDebt.String()
	} else {
		vs.DelegatorRewardIndexes = append(vs.DelegatorRewardIndexes, &pb.StringStringPair{
			Key:   delegatorAddrStr,
			Value: newRewardDebt.String(),
		})
	}
}

func (vs *validatorStateImpl) SetUndelegate(delegatorAddress common.Address, amount *big.Int) error {
	delegatorAddrStr := delegatorAddress.Hex()

	var existingDelegator *pb.StringStringPair
	for _, pair := range vs.Delegators {
		if pair.Key == delegatorAddrStr {
			existingDelegator = pair
			break
		}
	}

	if existingDelegator == nil {
		return types.ErrInsufficientBalance
	}

	currentAmount, _ := new(big.Int).SetString(existingDelegator.Value, 10)
	if currentAmount.Cmp(amount) < 0 {
		return types.ErrInsufficientBalance
	}
	currentAmount.Sub(currentAmount, amount)

	// Cập nhật giá trị stake
	existingDelegator.Value = currentAmount.String()

	// --- Cập nhật reward debt ---
	accumulatedRewards := vs.AccumulatedRewardsPerShare()
	newRewardDebt := new(big.Int).Mul(currentAmount, accumulatedRewards)
	newRewardDebt.Div(newRewardDebt, PRECISION)

	var existingRewardIndex *pb.StringStringPair
	for _, pair := range vs.DelegatorRewardIndexes {
		if pair.Key == delegatorAddrStr {
			existingRewardIndex = pair
			break
		}
	}

	if existingRewardIndex != nil {
		existingRewardIndex.Value = newRewardDebt.String()
	} else {
		// Trường hợp này ít xảy ra nhưng vẫn cần xử lý
		vs.DelegatorRewardIndexes = append(vs.DelegatorRewardIndexes, &pb.StringStringPair{
			Key:   delegatorAddrStr,
			Value: newRewardDebt.String(),
		})
	}
	return nil
}

func (vs *validatorStateImpl) ResetRewardDebt(delegatorAddress common.Address) {
	delegatorAddrStr := delegatorAddress.Hex()
	stakedAmount, _ := vs.GetDelegation(delegatorAddress)

	if stakedAmount.Sign() == 0 {
		return
	}

	accumulatedRewards := vs.AccumulatedRewardsPerShare()
	newRewardDebt := new(big.Int).Mul(stakedAmount, accumulatedRewards)
	newRewardDebt.Div(newRewardDebt, PRECISION)

	// Tìm và cập nhật reward debt
	var existingRewardIndex *pb.StringStringPair
	for _, pair := range vs.DelegatorRewardIndexes {
		if pair.Key == delegatorAddrStr {
			existingRewardIndex = pair
			break
		}
	}

	if existingRewardIndex != nil {
		existingRewardIndex.Value = newRewardDebt.String()
	} else {
		vs.DelegatorRewardIndexes = append(vs.DelegatorRewardIndexes, &pb.StringStringPair{
			Key:   delegatorAddrStr,
			Value: newRewardDebt.String(),
		})
	}
}

func (vs *validatorStateImpl) DistributeRewards(totalReward *big.Int) *big.Int {
	// Hàm này không cần thay đổi vì nó sử dụng TotalStakedAmount, đã được sửa
	if totalReward == nil || totalReward.Sign() <= 0 {
		return big.NewInt(0)
	}
	totalStake := vs.TotalStakedAmount()
	if totalStake.Sign() == 0 {
		return big.NewInt(0)
	}
	commissionAmount := new(big.Int).Mul(totalReward, new(big.Int).SetUint64(vs.GetCommissionRate()))
	commissionAmount.Div(commissionAmount, big.NewInt(10000))
	rewardForStakers := new(big.Int).Sub(totalReward, commissionAmount)
	if rewardForStakers.Sign() > 0 {
		rewardPerUnit := new(big.Int).Mul(rewardForStakers, PRECISION)
		rewardPerUnit.Div(rewardPerUnit, totalStake)
		accumulated, _ := new(big.Int).SetString(vs.GetAccumulatedRewardsPerShare(), 10)
		accumulated.Add(accumulated, rewardPerUnit)
		vs.Validator.AccumulatedRewardsPerShare = accumulated.String()
	}
	return rewardForStakers
}

func (vs *validatorStateImpl) WithdrawReward(delegatorAddress common.Address) *big.Int {
	stakedAmount, rewardDeb := vs.GetDelegation(delegatorAddress)

	if stakedAmount.Sign() == 0 {
		return big.NewInt(0)
	}

	currentAcc := vs.AccumulatedRewardsPerShare()
	totalEarned := new(big.Int).Mul(stakedAmount, currentAcc)
	totalEarned.Div(totalEarned, PRECISION)

	pendingReward := new(big.Int).Sub(totalEarned, rewardDeb)
	if pendingReward.Sign() <= 0 {
		return big.NewInt(0)
	}
	return pendingReward
}

// --- Serialization (ĐÃ SỬA ĐỔI) ---

// Marshal đảm bảo Canonical Encoding bằng cách sắp xếp các slice trước khi tuần tự hóa.
func (vs *validatorStateImpl) Marshal() ([]byte, error) {
	// Tạo một bản sao của validator object để không làm thay đổi state gốc
	valClone := proto.Clone(vs.Validator).(*pb.Validator)

	// Sắp xếp slice 'delegators' theo key
	if valClone.Delegators != nil {
		sort.Slice(valClone.Delegators, func(i, j int) bool {
			return valClone.Delegators[i].Key < valClone.Delegators[j].Key
		})
	}

	// Sắp xếp slice 'delegator_reward_indexes' theo key
	if valClone.DelegatorRewardIndexes != nil {
		sort.Slice(valClone.DelegatorRewardIndexes, func(i, j int) bool {
			return valClone.DelegatorRewardIndexes[i].Key < valClone.DelegatorRewardIndexes[j].Key
		})
	}

	// Tuần tự hóa object đã được sắp xếp
	return proto.MarshalOptions{Deterministic: true}.Marshal(valClone)
}

func (vs *validatorStateImpl) Unmarshal(data []byte) error {
	valProto := &pb.Validator{}
	if err := proto.Unmarshal(data, valProto); err != nil {
		return err
	}
	// Đảm bảo các slice không bị nil sau khi unmarshal, tránh lỗi panic
	if valProto.Delegators == nil {
		valProto.Delegators = make([]*pb.StringStringPair, 0)
	}
	if valProto.DelegatorRewardIndexes == nil {
		valProto.DelegatorRewardIndexes = make([]*pb.StringStringPair, 0)
	}
	vs.Validator = valProto
	return nil
}
