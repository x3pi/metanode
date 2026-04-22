// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import "@openzeppelin/contracts/access/Ownable.sol";
import "@openzeppelin/contracts/utils/math/SafeMath.sol";

/**
 * @title StakingManager
 * @dev Quản lý validator, ủy quyền (staking) và phân phối phần thưởng.
 * Tương đương với logic trong các module Go `validator_state` và `stake_state_db`.
 */
contract StakingManager is Ownable {
    using SafeMath for uint256;

    // --- State Variables ---

    IERC20 public immutable stakingToken;

    // Thông tin chi tiết của một validator
    struct Validator {
        address owner;                // Địa chỉ ví của validator
        string name;
        string description;
        string website;
        string image; // ⭐ MỚI: URL tới ảnh đại diện của validator
        uint64 commissionRate;        // Tỷ lệ hoa hồng (ví dụ: 1000 tương đương 10%)
        uint256 minSelfDelegation;    // Lượng stake tối thiểu validator phải tự stake
        uint256 totalStakedAmount;    // Tổng lượng token được stake vào validator này
        bool isJailed;
        uint256 jailedUntil;
        // Tích lũy phần thưởng trên mỗi "cổ phần" (1 token đã stake)
        uint256 accumulatedRewardsPerShare;
    }

    // Thông tin ủy quyền của một delegator
    struct Delegation {
        uint256 amount;               // Lượng token đã stake
        // "Nợ" phần thưởng, dùng để tính toán phần thưởng chưa nhận
        uint256 rewardDebt;
    }

    // Mapping từ địa chỉ validator sang struct Validator
    mapping(address => Validator) public validators;

    // Mapping lồng: validator => delegator => thông tin ủy quyền
    mapping(address => mapping(address => Delegation)) public delegations;

    // Danh sách các địa chỉ validator để dễ dàng truy vấn off-chain
    address[] public validatorAddresses;
    
    // Hằng số để tính toán chính xác phần thưởng, tương tự PRECISION trong Go
    uint256 private constant PRECISION = 1e18;

    // --- Events ---

    event ValidatorRegistered(address indexed validatorAddress, string name);
    event ValidatorInfoUpdated(address indexed validatorAddress); // ⭐ MỚI
    event Delegated(address indexed delegator, address indexed validator, uint256 amount);
    event Undelegated(address indexed delegator, address indexed validator, uint256 amount);
    event RewardDistributed(address indexed validator, uint256 totalReward);
    event RewardWithdrawn(address indexed delegator, address indexed validator, uint256 rewardAmount);
    event CommissionRateUpdated(address indexed validator, uint64 newRate);
    event ValidatorJailed(address indexed validator, uint256 until);

    // --- Constructor ---

    constructor(address _stakingTokenAddress) Ownable(msg.sender) {
        stakingToken = IERC20(_stakingTokenAddress);
    }

    // --- Validator Management Functions ---

    /**
     * @dev Đăng ký một validator mới.
     */
    function registerValidator(
        string calldata _name,
        string calldata _description,
        string calldata _website,
        string calldata _image, // ⭐ MỚI
        uint64 _commissionRate,
        uint256 _minSelfDelegation
    ) external {
        require(validators[msg.sender].owner == address(0), "Validator already registered");
        require(_commissionRate <= 10000, "Commission rate too high");

        validators[msg.sender] = Validator({
            owner: msg.sender,
            name: _name,
            description: _description,
            website: _website,
            image: _image, // ⭐ MỚI
            commissionRate: _commissionRate,
            minSelfDelegation: _minSelfDelegation,
            totalStakedAmount: 0,
            isJailed: false,
            jailedUntil: 0,
            accumulatedRewardsPerShare: 0
        });
        
        validatorAddresses.push(msg.sender);
        emit ValidatorRegistered(msg.sender, _name);
    }

    /**
     * @dev ⭐ MỚI: Cho phép validator tự cập nhật thông tin mô tả của mình.
     */
    function updateValidatorInfo(
        string calldata _name,
        string calldata _description,
        string calldata _website,
        string calldata _image
    ) external {
        require(validators[msg.sender].owner != address(0), "Not a validator");
        Validator storage validator = validators[msg.sender];
        validator.name = _name;
        validator.description = _description;
        validator.website = _website;
        validator.image = _image;
        emit ValidatorInfoUpdated(msg.sender);
    }


    /**
     * @dev Cập nhật tỷ lệ hoa hồng.
     */
    function setCommissionRate(uint64 _newRate) external {
        require(validators[msg.sender].owner != address(0), "Not a validator");
        require(_newRate <= 10000, "Commission rate too high");
        validators[msg.sender].commissionRate = _newRate;
        emit CommissionRateUpdated(msg.sender, _newRate);
    }

    /**
     * @dev "Bỏ tù" một validator (do admin thực hiện).
     */
    function setJailed(address _validatorAddress, bool _jailed, uint256 _until) external onlyOwner {
        require(validators[_validatorAddress].owner != address(0), "Validator does not exist");
        validators[_validatorAddress].isJailed = _jailed;
        validators[_validatorAddress].jailedUntil = _until;
        emit ValidatorJailed(_validatorAddress, _until);
    }

    // --- Staking and Reward Functions (Không thay đổi) ---

    function delegate(address _validatorAddress, uint256 _amount) external {
        Validator storage validator = validators[_validatorAddress];
        require(validator.owner != address(0), "Validator does not exist");
        require(!validator.isJailed, "Validator is jailed");
        require(_amount > 0, "Amount must be positive");

        _withdrawReward(msg.sender, _validatorAddress);

        Delegation storage delegation = delegations[_validatorAddress][msg.sender];
        
        stakingToken.transferFrom(msg.sender, address(this), _amount);

        delegation.amount = delegation.amount.add(_amount);
        validator.totalStakedAmount = validator.totalStakedAmount.add(_amount);
        delegation.rewardDebt = delegation.amount.mul(validator.accumulatedRewardsPerShare).div(PRECISION);

        emit Delegated(msg.sender, _validatorAddress, _amount);
    }

    function undelegate(address _validatorAddress, uint256 _amount) external {
        Validator storage validator = validators[_validatorAddress];
        require(validator.owner != address(0), "Validator does not exist");
        require(_amount > 0, "Amount must be positive");
        
        _withdrawReward(msg.sender, _validatorAddress);

        Delegation storage delegation = delegations[_validatorAddress][msg.sender];
        require(delegation.amount >= _amount, "Insufficient staked amount");

        if (msg.sender == _validatorAddress) {
            require(delegation.amount.sub(_amount) >= validator.minSelfDelegation, "Cannot undelegate below min self delegation");
        }

        delegation.amount = delegation.amount.sub(_amount);
        validator.totalStakedAmount = validator.totalStakedAmount.sub(_amount);
        
        stakingToken.transfer(msg.sender, _amount);

        delegation.rewardDebt = delegation.amount.mul(validator.accumulatedRewardsPerShare).div(PRECISION);

        emit Undelegated(msg.sender, _validatorAddress, _amount);
    }
    
    function distributeRewards(address _validatorAddress, uint256 _totalReward) external onlyOwner {
        Validator storage validator = validators[_validatorAddress];
        require(validator.owner != address(0), "Validator does not exist");
        require(validator.totalStakedAmount > 0, "No one is staking");

        uint256 commissionAmount = _totalReward.mul(validator.commissionRate).div(10000);
        if (commissionAmount > 0) {
            stakingToken.transfer(validator.owner, commissionAmount);
        }

        uint256 rewardForStakers = _totalReward.sub(commissionAmount);
        if (rewardForStakers > 0) {
            uint256 rewardPerShare = rewardForStakers.mul(PRECISION).div(validator.totalStakedAmount);
            validator.accumulatedRewardsPerShare = validator.accumulatedRewardsPerShare.add(rewardPerShare);
        }

        emit RewardDistributed(_validatorAddress, _totalReward);
    }
    
    function withdrawReward(address _validatorAddress) external {
        _withdrawReward(msg.sender, _validatorAddress);
    }

    function _withdrawReward(address _delegator, address _validatorAddress) internal {
        uint256 pendingReward = getPendingRewards(_delegator, _validatorAddress);

        if (pendingReward > 0) {
            Delegation storage delegation = delegations[_validatorAddress][_delegator];
            Validator storage validator = validators[_validatorAddress];

            delegation.rewardDebt = delegation.amount.mul(validator.accumulatedRewardsPerShare).div(PRECISION);
            
            stakingToken.transfer(_delegator, pendingReward);
            emit RewardWithdrawn(_delegator, _validatorAddress, pendingReward);
        }
    }

    // --- View Functions (Không thay đổi) ---

    function getPendingRewards(address _delegator, address _validatorAddress) public view returns (uint256) {
        Validator storage validator = validators[_validatorAddress];
        Delegation storage delegation = delegations[_validatorAddress][_delegator];

        if (delegation.amount == 0) {
            return 0;
        }

        uint256 totalEarned = delegation.amount.mul(validator.accumulatedRewardsPerShare).div(PRECISION);
        return totalEarned.sub(delegation.rewardDebt);
    }
    
    function getValidatorCount() public view returns (uint256) {
        return validatorAddresses.length;
    }
}