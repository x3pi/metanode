// SPDX-License-Identifier: SEE LICENSE IN LICENSE
pragma solidity ^0.8.20;
struct MiningCode {
    bytes publicKey;          // Public key (32 bytes)
    uint256 boostRate;        // Mining boost rate
    uint256 maxDuration;      // Maximum valid duration
    CodeStatus status;        // Current status of the code
    address assignedTo;       // Address that owns the code
    address referrer;         // Address of the referrer
    uint256 referralReward;   // Reward for the referrer
    bool transferable;        // Whether the code is transferable
    uint256 lockUntil;        // Lock timestampưe
    LockType lockType;        // Type of lock
    uint256 expireTime;       //max time to activate code
}
enum CodeStatus { Pending, Approved, Actived, Expired }
enum LockType { None, ActiveLock, MiningLock, TransferLock }
struct BalanceDevice {
    address device;
    uint256 balance;
    bool isCodeDevice; 
    bool isLock;
    bytes publicCode;
}
struct BalanceWallet {
    address userAddress;
    uint256 balance;
}

interface PublicKeyFromPrivateKey {
    function getPublicKeyFromPrivate(bytes32 _privateCode) external returns (bytes memory);
}
interface IMiningDevice {
    function addBalance(address miner, uint256 amount) external;
    function addBalanceMigrate(address miner, uint256 amount) external;
    function linkCodeWithUser(address _user, address _device, bytes memory publicCode) external;
    function updateNewUserLinkDevice(address _newWallet, address _oldWallet)external ;
}
interface ICode {
        function createCodeDirect(
        bytes memory publicKey,
        uint256 boostRate,
        uint256 maxDuration,
        address assignedTo,
        address referrer,
        uint256 referralReward,
        bool transferable,
        uint256 expireTime
    ) external returns(bytes memory);
    
    function requestCode(
        bytes memory publicKey,
        uint256 boostRate,
        uint256 maxDuration,
        address assignedTo,
        address referrer,
        uint256 referralReward,
        bool transferable,
        uint256 expireTime
    ) external returns(bytes memory);
    
    function voteCode(bytes memory code, bool approve) external;
    function isDAOMember(address member) external view returns (bool);
    function getCodeStatus(bytes memory code) external view returns(uint256 approveVotes, uint256 denyVotes);
    function activateCode(uint256 indexCode, address user) external returns (uint256, uint256, uint256);
    function getCodesByOwner(address owner) external view returns (bytes[] memory);

}
interface IMiningUser {
    function lockUser(address _user) external;
    function checkJoined(address _user) external view returns (bool);
    function getParentUser(address _user, uint8 _level) external view returns (address[] memory);
}
interface IMiningCode {
    // function migrateAmount(MigrateData[] memory datas)external;
    function migrateAmount(address user, bytes32  _privateCode, uint256 _activeTime, uint256 _amount) external; 
}