// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

// Contract A lưu trữ một số và cung cấp các hàm để cập nhật số đó
pragma solidity ^0.8.0;

contract ContractA {
    uint256 private storedNumber;

    // Sự kiện để theo dõi số được cập nhật
    event NumberUpdated(uint256 newNumber);

    // Hàm cập nhật giá trị storedNumber
    function setNumber(uint256 _num) public {
        storedNumber = _num;
        emit NumberUpdated(_num);
    }

    // Hàm đọc giá trị storedNumber
    function getNumber() public view returns (uint256) {
        return storedNumber;
    }
}


// Contract B sẽ gọi đến Contract A
contract ContractB {
    ContractA private contractA;  // Biến lưu địa chỉ contract A

    // Khởi tạo contract B với địa chỉ của contract A
    constructor(address _contractA) {
        contractA = ContractA(_contractA);
    }

    // Hàm gọi contract A để cập nhật giá trị
    function updateContractA(uint256 _num) public {
        contractA.setNumber(_num);
    }

    // Hàm lấy giá trị từ contract A
    function readFromContractA() public view  returns (uint256) {
        return contractA.getNumber();
    }
}
