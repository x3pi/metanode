// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

contract AccountManager {
    event AccountConfirmed(address account, uint time,string message);
    event RegisterBls(address account, uint time ,bytes publicKey,string message);
    event TransferFrom(address from, address to , uint amount, uint time,string message);
    function setBlsPublicKey(bytes memory _publicKey) external {
        address account;
        uint time;
        string memory message;
        emit RegisterBls(account, time,_publicKey, message );
    }
    function setBlsPublicKeyAutoConfirm(bytes memory _publicKey) external {
        address account;
        uint time;
        string memory message;
        emit AccountConfirmed(account, time, message );
    }
    function setAccountType(uint8 _type) external {
    }
    function getAllAccount(bytes memory _sign, bytes memory _publicKeyBls, uint _time, uint _page, uint _pageSize, bool _isConfirm) external {
    }
    function getPublickeyBls() external {
    }
    function getNotifications(address _account,uint page, uint pageSize)external { 

    }
    function confirmAccount(address _account, uint time,bytes memory _sign) external {
        string memory message;
        emit AccountConfirmed(_account, time, message );
    }
    function transferFrom(address to, uint amount ,uint time,bytes memory _sign) external {
        string memory message;
        address  from;
        emit TransferFrom(from,to, amount, time, message );
    }
    function confirmAccountWithoutSign(address _account) external {
    }
    function addContractFreeGas(address contractAddress) external {
    }
    function removeContractFreeGas(address contractAddress) external {
    }
    function getAllContractFreeGas(uint256 page, uint256 pageSize, uint256 time, bytes memory _sign) external {
    }
    function addAuthorizedWallet(address walletAddress) external {
    }
    function removeAuthorizedWallet(address walletAddress) external {
    }
    function getAllAuthorizedWallets(uint256 page, uint256 pageSize) external {
    }
    function addAdmin(address adminAddress) external {
    }
    function removeAdmin(address adminAddress) external {
    }
    function getAllAdmins(uint256 page, uint256 pageSize) external {
    }
    function getMyContracts(address adder, uint256 page, uint256 pageSize) external {
    }
}
