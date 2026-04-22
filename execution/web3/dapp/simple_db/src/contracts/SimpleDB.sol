// SPDX-License-Identifier: MIT

pragma solidity ^0.8.20;

interface SimpleDB0_1 {
    function getOrCreateSimpleDb(string memory name) external returns (bool);
    function deleteDb(string memory name) external returns (bool);
    function get(string memory dbName, string memory key) external returns (string memory);
    function set(string memory dbName, string memory key, string memory value) external returns (bool);
    function getAll(string memory dbName) external returns (string memory);
    function searchByValue(string memory dbName, string memory value) external returns (string memory);
    function getNextKeys(string memory dbName, string memory key,   uint8 limit) external returns (string memory);
}

contract PublicSimpleDB {
    SimpleDB0_1 public simpleDB = SimpleDB0_1(0x0000000000000000000000000000000000000105);

    string public storedValue; // Kết quả string cho hàm gọi gần nhất
    bool public status; // Kết quả bool cho hàm gọi gần nhất
    string public dbName; // Tên db đang được gọi
    
    function getOrCreateSimpleDb(string memory name) public returns (bool) {
        bool result = simpleDB.getOrCreateSimpleDb(name);
        if(result)dbName = name;
        status = result;
        return result;
    }

    function deleteDb(string memory name) public returns (bool) {
        bool result = simpleDB.deleteDb(name);
        status = result;
        return result;
    }

    function getNextKeys(string memory key,uint8 limit) public returns (string memory) {
        storedValue = simpleDB.getNextKeys(dbName, key, limit); // Assign the retrieved value to the public variable
        return storedValue;
    }

    function get(string memory key) public returns (string memory) {
        storedValue = simpleDB.get(dbName, key); // Assign the retrieved value to the public variable
        return storedValue;
    }

    function set(string memory key, string memory value) public returns (bool) {
        bool result = simpleDB.set(dbName, key, value);
        status = result;
        return result;
    }

    function getAll() public returns (string memory) {
        storedValue = simpleDB.getAll(dbName);
        return storedValue;
    }

    function searchByValue(string memory value) public returns (string memory) {
        storedValue = simpleDB.searchByValue(dbName, value);
        return storedValue;
    }

    function refresh() public {
        storedValue = ""; // Reset storedValue to an empty string
        status = false; // Reset status to false
    }
}