/*
 *   Copyright (c) 2023 hieuphanuit
 *   All rights reserved.
 */
#pragma once
#include <iostream>
#include <string>
#include <vector>
#include <nlohmann/json.hpp>
#include <sstream>
using namespace std;


using json = nlohmann::json;

enum SolidityType {
    IntTy,
    UintTy,
    BoolTy,
    StringTy,
    SliceTy,
    ArrayTy,
    TupleTy,
    AddressTy,
    FixedBytesTy,
    BytesTy,
    HashTy,
    FixedPointTy,
    FunctionTy,
};

string bytesToHexString(const uint8_t* bytes, const uint32_t len) {
    stringstream ss;
    ss << hex << setfill('0');
    
    // Log thông tin khởi đầu
//    cout << "DEBUG: Bắt đầu bytesToHexString với len = " << len << endl;
    
    // Kiểm tra nếu con trỏ bytes bị null
    if (bytes == nullptr) {
//        cout << "ERROR: Con trỏ bytes là null!" << endl;
        return "";
    }
    
    for (uint32_t i = 0; i < len; i++) {
        unsigned int byteVal = static_cast<unsigned int>(bytes[i]);
        // Log giá trị của từng byte: dạng thập phân và dạng hex
//        cout << "DEBUG: Index " << i
//             << " - Giá trị byte (decimal): " << byteVal
//             << ", hex: " << setw(2) << setfill('0') << hex << byteVal
//             << endl;
        ss << setw(2) << byteVal;
    }
    
    string result = ss.str();
//    cout << "DEBUG: Kết quả hex string: " << result << endl;
    return result;
}


string removeArraySizeFromType(string type) {
    size_t pos = type.rfind("[");
    if (pos == string::npos) {
        // No array size found, return the original string
        return type;
    } else {
        // Remove the array size and any following dimensions
        return type.substr(0, pos) + type.substr(type.rfind("]")+1);
    }
}

json getArrayElementAbi(json abi) {
    json newAbi;
    newAbi["name"] = abi["name"];
    newAbi["type"] = removeArraySizeFromType(abi["type"]);
    newAbi["components"] = abi["components"];
    return newAbi;
}

SolidityType getType(json element) {
    string type = element["type"];
    size_t pos = type.rfind("[");
    if (pos != string::npos) {
        size_t end_pos = type.find("]", pos);
        string count_str = type.substr(pos+1, end_pos-pos-1);
        if(count_str == "") {
            return SliceTy;
        }
        return ArrayTy;
    }
    if (type == "tuple") {
        return TupleTy;
    }
    if (type == "string") {
        return StringTy;
    }
    if (type == "bool") {
        return BoolTy;
    }
    if (type == "address") {
        return AddressTy;
    }
    if (type == "bytes") {
        return BytesTy;
    }
    size_t posBytes = type.rfind("bytes");
    if (posBytes != string::npos) {
        return FixedBytesTy;
    }
    size_t posInt = type.rfind("int");
    if (posInt != string::npos) {
        return IntTy;
    }
    return IntTy;
}

uint32_t getArrayLength(string type) {
    size_t pos = type.rfind("[");
    size_t end_pos = type.find("]", pos);
    string count_str = type.substr(pos+1, end_pos-pos-1);
    uint32_t count = 0;
    try {
        count = stoi(count_str);
    } catch (...) {
        throw invalid_argument("Invalid type string");
    }
    return count;
}


// isDynamicType returns true if the type is dynamic.
// The following types are called “dynamic”:
// * bytes
// * string
// * T[] for any T
// * T[k] for any dynamic T and any k >= 0
// * (T1,...,Tk) if Ti is dynamic for some 1 <= i <= k
bool isDynamicType(json abi)  {
//    cout << "DB::: XX2" << abi << endl;
    SolidityType type = getType(abi);
    if (type == TupleTy) {
		for (const auto& tuple_element : abi["components"]) {
			if (isDynamicType(tuple_element)) {
				return true;
			}
		}
		return false;
	}
    if (type == ArrayTy) {
        json elemAbi = getArrayElementAbi(abi);
        return isDynamicType(elemAbi);
    }

	return (type == StringTy || type == BytesTy || type == SliceTy);
}

uint32_t getTypeSize(json abi) {

    SolidityType type = getType(abi);
//    cout << "XX2" << abi << endl;
//    cout << "XX3" << type << endl;

    if (type == ArrayTy){
        json elemAbi = getArrayElementAbi(abi);
        if (!isDynamicType(elemAbi)) {
            int size = getArrayLength(abi["type"]);
            SolidityType elemType = getType(elemAbi);
            // // Recursively calculate type size if it is a nested array
            if (elemType == ArrayTy || elemType == TupleTy) {
                return size * getTypeSize(elemAbi);
            }
            return size * 32;
        } else {
            return 32;
        }
    } 
    if (type == TupleTy){
//        cout << "XX4" << abi << endl;
       if (!isDynamicType(abi)) {
            int total = 0;
            for (const auto& tuple_element : abi["components"]) {
//                cout << "XX5.1" << tuple_element << endl;
//                cout << "XX5" << getTypeSize(tuple_element) << endl;
                total += getTypeSize(tuple_element);
            }
            return total;
        } else {
//            cout << "XX6"  << endl;

            return 32;
        }
	} 
//    cout << "XX7"  << endl;
    return 32;
}


vector<uint8_t> hexStringToUint8Array(const string& hexStr) {
    string processedHexStr = hexStr;

    // Convert hex string to lowercase
    transform(processedHexStr.begin(), processedHexStr.end(), processedHexStr.begin(), ::tolower);

    // Remove "0x" prefix if present
    if (processedHexStr.substr(0, 2) == "0x") {
        processedHexStr = processedHexStr.substr(2);
    }
    if (processedHexStr.length() % 2 == 1) {
        processedHexStr = "0" + processedHexStr;
    }
//    cout << "hexStringToUint8Array: " << processedHexStr << endl;
    vector<uint8_t> result;
    result.reserve(processedHexStr.size() / 2);

    for (size_t i = 0; i < processedHexStr.size(); i += 2) {
        uint8_t byte = static_cast<uint8_t>(stoi(processedHexStr.substr(i, 2), nullptr, 16));
        result.push_back(byte);
    }
    return result;
}

std::vector<std::string> splitStringArgument(const std::string& input) {
    std::vector<std::string> result;
    if(input.length() <= 2) {
        return result;
    }
    std::string processedInput= input.substr(1, input.length() - 2);
    int trackVar = 0;
    int from = 0;

    for (std::size_t i = 0; i < processedInput.length(); ++i) {
        char c = processedInput[i];
//        cout << "CCC = : " << c << endl;
        if (c == '[') {
            trackVar++;
        } else if (c == ']') {
            trackVar--;
        } else if (c == ',' && trackVar == 0) {
            if (i > 0) {
                char pre = processedInput[i-1];
                if (pre == '\\') {
                    continue;
                }
            }
            result.push_back(processedInput.substr(from, i - from));
            from = i + 1;
        }
    }

    result.push_back(processedInput.substr(from));

    return result;
}
std::string joinStringArgument(const std::vector<std::string>& parts) {
    std::string result = "[";
    
    for (std::size_t i = 0; i < parts.size(); ++i) {
        std::string part = parts[i];

        // Xử lý các ký tự escape nếu cần
        for (std::size_t j = 0; j < part.length(); ++j) {
            if (part[j] == ',') {
                part.insert(j, "\\"); // Thêm ký tự escape trước dấu phẩy
                ++j; // Nhảy qua ký tự vừa thêm để tránh vòng lặp vô hạn
            }
        }

        result += part;
        if (i < parts.size() - 1) {
            result += ",";
        }
    }

    result += "]";
    return result;
}