// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

contract ContractB {
    string public message;

    function setMessage(string memory _message) public {
        message = _message;
    }

    function getMessage() public view returns (string memory) {
        return message;
    }
}

contract ContractATest {
    function testSetAndGetMessage(address contractBAddress, string memory _msg) public returns (string memory) {
        ContractB(contractBAddress).setMessage(_msg);
        return ContractB(contractBAddress).getMessage();
    }
}
