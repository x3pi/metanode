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

std::string bytesToHexString(const uint8_t* bytes, const uint32_t len) {
    std::stringstream ss;
    ss << std::hex << std::setfill('0');
    for (uint32_t i = 0; i < len; i++) {
        ss << std::setw(2) << static_cast<unsigned int>(bytes[i]);
    }
    return ss.str();
}

SolidityType getType(json element) {
    std::string type = element["type"];
    std::size_t pos = type.rfind("[");
    if (pos != std::string::npos) {
        std::size_t end_pos = type.find("]", pos);
        std::string count_str = type.substr(pos+1, end_pos-pos-1);
        if(count_str == "") {
            return SliceTy;
        }
        return ArrayTy;
    }
    if (type == "tuple") {
        return TupleTy;
    }
    if (type ==  "string") {
        return StringTy;
    }
    if (type ==  "bytes") {
        return BytesTy;
    }
    if (type ==  "address") {
        return AddressTy;
    }
    // default is IntTy, i dont check orther type cuz all of them is encode to 32 byte so just return IntTy
    return IntTy;
}

uint32_t getArrayLength(string type) {
    std::size_t pos = type.rfind("[");
    std::size_t end_pos = type.find("]", pos);
    std::string count_str = type.substr(pos+1, end_pos-pos-1);
    uint32_t count = 0;
    try {
        count = std::stoi(count_str);
    } catch (...) {
        throw std::invalid_argument("Invalid type string");
    }
    return count;
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

json parseElement(const uint8_t* bytes, uint32_t* i, json abi);

json parseString(const uint8_t* bytes, uint32_t* i, json abi) {
    json result = json::object();
    // cout << "DB String1:  " << bytesToHexString(bytes, 64)  << " i:" << *i << endl;
    // Read the offset and length of the string

    uint32_t offset = 0;
    for (int j = 0; j < 32; j++) {
        offset <<= 8;
        offset |= bytes[*i + j];
    }
    // cout << "DB String Off set:  " << offset << endl;
    uint32_t length = 0;
    for (int j = 0; j < 32; j++) {
        length <<= 8;
        length |= bytes[offset + j];
    }
    // cout << "DB String lengtth: " << bytesToHexString(bytes + offset, 32) << endl;
    const uint8_t* string_bytes = bytes + offset + 32; // 32 bytes for string length
    std::string element_str(string_bytes, string_bytes + length);
    // cout << "DB String: " << offset << ":" << length << ":"<< bytesToHexString(string_bytes, length) << endl;
    *i += 32;
    result = json::parse("\"" + element_str + "\"");
    return result;
}

json parseTuple(const uint8_t* bytes, uint32_t* i, json abi) {
    // cout << "DB parseTuple: " <<  abi << endl;
    json result = json::object();
    for (const auto& tuple_element : abi["components"]) {
        result[std::string(tuple_element["name"])] = parseElement(bytes, i, tuple_element);
        // cout << "DB parseTuple pushing key: " <<  std::string(abi["name"]) << " Value: " << result[std::string(tuple_element["name"])] << endl;
    }
    return result;
}

std::vector<json> parseArray(const uint8_t* bytes, uint32_t* i, json abi) {
    // cout << "DB array: " <<  abi << endl;
    std::vector<json> rs;
    uint32_t array_len = getArrayLength(abi["type"]);
    // cout << "DB parseArray: " <<  array_len << endl;

    json newAbi;
    newAbi["name"] = abi["name"];
    newAbi["type"] = removeArraySizeFromType(abi["type"]);
    newAbi["components"] = abi["components"];

    for (uint32_t j = 0; j < array_len; j++) {
        json elem = parseElement(bytes, i, newAbi);
        rs.push_back(elem);
        // cout << "DB parseArray pushing key: " <<  std::string(abi["name"]) << " Value: " << elem << endl;
        // cout << "DB parseArray RS: " <<  rs << endl;
        // cout << "DB parseArray ABI: " <<  abi << endl;
    }

    return rs;
}

std::vector<json> parseSlice(const uint8_t* bytes, uint32_t* i, json abi) {
    // cout << "DB parseSlice: " <<  abi << endl;
    std::vector<json> rs;
    uint32_t offset = 0;
    for (int j = 0; j < 32; j++) {
        offset <<= 8;
        offset |= bytes[*i + j];
    }
    *i += 32;

    uint32_t array_len = 0;
    for (int j = 0; j < 32; j++) {
        array_len <<= 8;
        array_len |= bytes[offset + j];
    }
    // cout << "DB parseSlice: " <<  array_len << endl;

    json newAbi;
    newAbi["name"] = abi["name"];
    newAbi["type"] = removeArraySizeFromType(abi["type"]);
    newAbi["components"] = abi["components"];
    uint32_t aI = 0;

    const uint8_t* slice_bytes = bytes + offset + 32;
    for (uint32_t j = 0; j < array_len; j++) {
        json elem = parseElement(slice_bytes, &aI, newAbi);
        rs.push_back(elem);
        // cout << "DB parseSlice pushing  Value: " << elem << endl;
    }
    // cout << "aI: " << aI << endl;

    return rs;
}

json parseAddress(const uint8_t* bytes, uint32_t* i, json abi) {
    const uint8_t* element_bytes = bytes + *i;
    json result = json::object();
    std::string hexString = bytesToHexString(element_bytes + 12, 20);
    result = hexString;
    *i += 32;
    return result;
}

json parseInt(const uint8_t* bytes, uint32_t* i, json abi) {
    const uint8_t* element_bytes = bytes + *i;
    json result = json::object();
    std::string type = abi["type"];
    std::string hexString = bytesToHexString(element_bytes, 32);
    long number = strtol(hexString.c_str(), NULL, 16);
    if (type.find("int") != std::string::npos) {
        result = number;
    } else if (type == "bool") {
        result = number == 1;
    } else {
        result = hexString;
    }
//    result = abi["type"];
    // cout << "DB parseInt:  " << bytesToHexString(element_bytes, 32) << endl;
    *i += 32;

    return result;
}

json parseElement(const uint8_t* bytes, uint32_t* i, json abi) {
//     cout << "I: " << *i << endl;
    SolidityType t = getType(abi);
    json result = json::object();
    cout << "t: " << t << endl;
    if (t == SliceTy) {
        result = parseSlice(bytes, i, abi);
    }
    if (t == ArrayTy) {
        result = parseArray(bytes, i, abi);
    }
    if (t == TupleTy) {
        result = parseTuple(bytes, i, abi);
    }
    if (t == StringTy) {
        result =  parseString(bytes, i, abi);
    }
    if (t == AddressTy) {
        result =  parseAddress(bytes, i, abi);
        cout << t << "addressssss" << result.dump() << endl;
    }
    if (t == IntTy) {
        result =  parseInt(bytes, i, abi);
    }
     cout << t << "result" << result.dump() << endl;
    return result;
}

std::vector<uint8_t> hexStringToUint8Array(const std::string& hexStr) {
    std::vector<uint8_t> result;
    result.reserve(hexStr.size() / 2);

    for (std::size_t i = 0; i < hexStr.size(); i += 2) {
        uint8_t byte = std::stoi(hexStr.substr(i, 2), nullptr, 16);
        result.push_back(byte);
    }
    return result;
}

json parseSolidityBytesToJSON(const uint8_t* bytes, const uint32_t len, string strAbi) {
    std::vector<json> abi = json::parse(strAbi);
    json result = json::object();
    uint32_t i = 0;
    for (const auto& elementAbi : abi) {
        json element_value = parseElement(bytes, &i, elementAbi);
        result[std::string(elementAbi["name"])] = element_value;
    }
    return result;
}
