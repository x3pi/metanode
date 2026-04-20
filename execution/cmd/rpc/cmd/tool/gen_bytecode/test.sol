// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;
// contract_a.sol
interface IContractB {
    function fail(uint256 x) external;
}

contract ContractA {
    function callB(address bAddr, uint256 val) external {
        IContractB(bAddr).fail(val);
    }
}