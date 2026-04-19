// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "@openzeppelin/contracts/utils/math/SafeMath.sol";

/**
 * @title ValidatorActions
 * @dev Contract để validator tự quản lý, sử dụng NATIVE COIN để staking.
 */
contract ValidatorActions {
    using SafeMath for uint256;

    // --- State Variables ---
    // ✏️ THAY ĐỔI: Đã loại bỏ IERC20 stakingToken

    struct Validator {
        address owner;
        string name;
        string description;
        string website;
        string image;
        uint64 commissionRate;
        uint256 minSelfDelegation;
        uint256 totalStakedAmount;
        uint256 accumulatedRewardsPerShare;
    }

    struct Delegation {
        uint256 amount;
        uint256 rewardDebt;
    }

    mapping(address => Validator) public validators;
    mapping(address => mapping(address => Delegation)) public delegations;
    
    address[] public validatorAddresses;
    mapping(address => uint256) private validatorIndexes;

    uint256 private constant PRECISION = 1e18;

    // --- Events ---
    // Không thay đổi

    event ValidatorRegistered(address indexed validatorAddress, string name);
    event ValidatorInfoUpdated(address indexed validatorAddress);
    event ValidatorDeregistered(address indexed validatorAddress);
    event Delegated(address indexed delegator, address indexed validator, uint256 amount);
    event Undelegated(address indexed delegator, address indexed validator, uint256 amount);
    event RewardWithdrawn(address indexed delegator, address indexed validator, uint256 rewardAmount);
    event CommissionRateUpdated(address indexed validator, uint64 newRate);
    
    // --- Constructor ---
    // ✏️ THAY ĐỔI: Constructor không còn cần thiết vì không có địa chỉ token
    constructor() {}

    // =================================================================
    //                    VALIDATOR MANAGEMENT FUNCTIONS
    // =================================================================
    // Các hàm trong mục này không thay đổi
    
    function registerValidator(
        string calldata _name,
        string calldata _description,
        string calldata _website,
        string calldata _image,
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
            image: _image,
            commissionRate: _commissionRate,
            minSelfDelegation: _minSelfDelegation,
            totalStakedAmount: 0,
            accumulatedRewardsPerShare: 0
        });
        
        validatorIndexes[msg.sender] = validatorAddresses.length;
        validatorAddresses.push(msg.sender);

        emit ValidatorRegistered(msg.sender, _name);
    }

    function deregisterValidator() external {
        Validator storage validator = validators[msg.sender];
        require(validator.owner != address(0), "Not a validator");
        require(validator.totalStakedAmount == 0, "Validator still has staked tokens");
        
        uint256 indexToRemove = validatorIndexes[msg.sender];
        address lastValidator = validatorAddresses[validatorAddresses.length - 1];
        
        validatorAddresses[indexToRemove] = lastValidator;
        validatorIndexes[lastValidator] = indexToRemove;

        validatorAddresses.pop();
        
        delete validatorIndexes[msg.sender];
        delete validators[msg.sender];
        
        emit ValidatorDeregistered(msg.sender);
    }

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

    function setCommissionRate(uint64 _newRate) external {
        require(validators[msg.sender].owner != address(0), "Not a validator");
        require(_newRate <= 10000, "Commission rate too high");
        validators[msg.sender].commissionRate = _newRate;
        emit CommissionRateUpdated(msg.sender, _newRate);
    }

    // =================================================================
    //                    STAKING & REWARD FUNCTIONS
    // =================================================================
    
    /**
     * @dev Ủy quyền (stake) native coin vào một validator.
     * Người dùng gửi coin kèm theo khi gọi hàm này.
     */
    // ✏️ THAY ĐỔI: Chuyển thành `payable` và dùng `msg.value`
    function delegate(address _validatorAddress) external payable {
        Validator storage validator = validators[_validatorAddress];
        require(validator.owner != address(0), "Validator does not exist");
        require(msg.value > 0, "Amount must be positive");

        _withdrawReward(msg.sender);

        Delegation storage delegation = delegations[_validatorAddress][msg.sender];
        
        // Không cần `transferFrom` vì coin đã được gửi kèm giao dịch (`msg.value`)

        delegation.amount = delegation.amount.add(msg.value);
        validator.totalStakedAmount = validator.totalStakedAmount.add(msg.value);
        delegation.rewardDebt = delegation.amount.mul(validator.accumulatedRewardsPerShare).div(PRECISION);

        emit Delegated(msg.sender, _validatorAddress, msg.value);
    }

    /**
     * @dev Rút ủy quyền (unstake) native coin.
     */
    function undelegate(address _validatorAddress, uint256 _amount) external {
        Validator storage validator = validators[_validatorAddress];
        require(validator.owner != address(0), "Validator does not exist");
        require(_amount > 0, "Amount must be positive");
        
        _withdrawReward(_validatorAddress);

        Delegation storage delegation = delegations[_validatorAddress][msg.sender];
        require(delegation.amount >= _amount, "Insufficient staked amount");

        if (msg.sender == _validatorAddress) {
            require(delegation.amount.sub(_amount) >= validator.minSelfDelegation, "Cannot undelegate below min self delegation");
        }

        delegation.amount = delegation.amount.sub(_amount);
        validator.totalStakedAmount = validator.totalStakedAmount.sub(_amount);
        
        // ✏️ THAY ĐỔI: Gửi native coin về cho người dùng
        (bool sent, ) = msg.sender.call{value: _amount}("");
        require(sent, "Failed to send native coin");

        delegation.rewardDebt = delegation.amount.mul(validator.accumulatedRewardsPerShare).div(PRECISION);

        emit Undelegated(msg.sender, _validatorAddress, _amount);
    }
    
    /**
     * @dev Rút phần thưởng.
     */
    function withdrawReward(address _validatorAddress) external {
        _withdrawReward(_validatorAddress);
    }

    // --- Internal & View Functions ---

    function _withdrawReward(address _validatorAddress) internal {
        uint256 pendingReward = getPendingRewards(msg.sender, _validatorAddress);
        if (pendingReward > 0) {
            Delegation storage delegation = delegations[_validatorAddress][msg.sender];
            Validator storage validator = validators[_validatorAddress];

            delegation.rewardDebt = delegation.amount.mul(validator.accumulatedRewardsPerShare).div(PRECISION);
            
            // ✏️ THAY ĐỔI: Gửi native coin về cho người dùng
            (bool sent, ) = msg.sender.call{value: pendingReward}("");
            require(sent, "Failed to send native coin");

            emit RewardWithdrawn(msg.sender, _validatorAddress, pendingReward);
        }
    }

    function getPendingRewards(address _delegator, address _validatorAddress) public view returns (uint256) {
        // ... Logic hàm này không thay đổi ...
        Validator storage validator = validators[_validatorAddress];
        Delegation storage delegation = delegations[_validatorAddress][_delegator];

        if (delegation.amount == 0) return 0;

        uint256 totalEarned = delegation.amount.mul(validator.accumulatedRewardsPerShare).div(PRECISION);
        return totalEarned.sub(delegation.rewardDebt);
    }

    function getValidatorCount() public view returns (uint256) {
        return validatorAddresses.length;
    }
}