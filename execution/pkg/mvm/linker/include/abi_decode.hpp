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
#include "abi_utilities.hpp"

using namespace std;

json decodeElement(const uint8_t* bytes, uint32_t i, json abi, uint32_t totalLength);
json decodeTuple(const uint8_t* bytes, uint32_t i, json abi, uint32_t totalLength);
vector<json> decodeArray(const uint8_t* bytes, uint32_t i, json abi, uint32_t totalLength);
vector<json> decodeSlice(const uint8_t* bytes, uint32_t i, json abi, uint32_t totalLength);

std::string replaceAllDoubleSlashes(const std::string& input, const std::string& target, const std::string& replacement) {
    std::string result = input;
    std::string::size_type pos = 0;
    // std::string target = "\"";
    while ((pos = result.find(target, pos)) != std::string::npos) {
        result.replace(pos, target.length(), replacement);
        pos += replacement.length();
    }
    return result;
}

//json decodeString(const uint8_t* bytes, uint32_t i, json abi) {
//    json result = json::object();
////     cout << "DB String1:  " << bytesToHexString(bytes, 64)  << " i:" << i << endl;
//    // Read the offset and length of the string
//
//    uint32_t offset = 0;
//    for (int j = 0; j < 32; j++) {
//        offset <<= 8;
//        offset |= bytes[i + j];
//    }
////    cout << "DB String Off set:  " << offset << endl;
//    uint32_t length = 0;
//    for (int j = 0; j < 32; j++) {
//        length <<= 8;
//        length |= bytes[offset + j];
//    }
////    cout << "DB String lengtth: " << bytesToHexString(bytes + offset, 32) << endl;
//    const uint8_t* string_bytes = bytes + offset + 32; // 32 bytes for string length
//    string element_str(string_bytes, string_bytes + length);
////    cout << "DB String: " << offset << ":" << length << ":"<< bytesToHexString(string_bytes, length) << endl;
////    cout << "DB String result1: " << element_str << endl;
//   
//    // Find the position of the NUL character
//    size_t pos = element_str.find('\0');
//
//    // Replace the NUL character with the escaped representation
//    while (pos != string::npos) {
//        element_str.replace(pos, 1, "");
//        pos = element_str.find('\0', pos);
//    }
//
//    element_str = replaceAllDoubleSlashes(element_str, "\"", "");
//    element_str = replaceAllDoubleSlashes(element_str, "\\,", "");
//    result = json::parse("\"" + element_str + "\"");
////    cout << "DB String result2: " << result << endl;
//    return result;
//}
json decodeString(const uint8_t *bytes, uint32_t i, json abi)
{
    try
    {
        json result = json::object();
//        cout << "DB String 1: " << bytes << endl;
        // Read the offset and length of the string
        uint32_t offset = 0;
        for (int j = 0; j < 32; j++)
        {
            offset <<= 8;
            offset |= bytes[i + j];
        }
        
        uint32_t length = 0;
        for (int j = 0; j < 32; j++)
        {
            length <<= 8;
            length |= bytes[offset + j];
        }
        
//        cout << "DB String 2: " << length << endl;
        const uint8_t *string_bytes = bytes + offset + 32; // 32 bytes for string length
        string element_str(string_bytes, string_bytes + length);
        
//        cout << "DB String 3: " << element_str << endl;
        
        // Remove NUL characters
        element_str.erase(remove(element_str.begin(), element_str.end(), '\0'), element_str.end());
        
        // Replace unwanted characters (adjust as per your use case)
        element_str = replaceAllDoubleSlashes(element_str, "\"", "\\\"");
        
//        cout << "DB String 4: " << element_str << endl;
        element_str = replaceAllDoubleSlashes(element_str, "\\,", "");
        
//        cout << "DB String 5: " << element_str << endl;
        
        // Instead of parsing, directly assign the string to the result
        result = element_str;
        
//        cout << "DB String result: " << result << endl;
        return result;
    }
    catch (const std::exception &e)
    {
//        cout << "Exception caught in decode string: " << e.what() << endl;
        throw;
    }
}

json decodeTuple(const uint8_t* bytes, uint32_t i, json abi, uint32_t totalLength) {
    const uint8_t* tuple_bytes;
    uint32_t new_total_length = totalLength;
    if(isDynamicType(abi)){
        uint32_t offset = 0;
        for (int j = 0; j < 32; j++) {
            offset <<= 8;
            offset |= bytes[i + j];
        }
        tuple_bytes = bytes + offset;
        new_total_length = (totalLength > offset) ? (totalLength - offset) : 0;
    } else {
        tuple_bytes = bytes + i;
        new_total_length = (totalLength > i) ? (totalLength - i) : 0;
    }
    uint32_t aI = 0;
    json result = json::object();
    for (const auto& tuple_element : abi["components"]) {
        result[string(tuple_element["name"])] = decodeElement(tuple_bytes, aI, tuple_element, new_total_length);
        aI += getTypeSize(tuple_element);
    }
    return result;
}

vector<json> decodeArray(const uint8_t* bytes, uint32_t i, json abi, uint32_t totalLength) {
    vector<json> rs;
    uint32_t array_len = getArrayLength(abi["type"]);
    json newAbi;
    newAbi["name"] = abi["name"];
    newAbi["type"] = removeArraySizeFromType(abi["type"]);
    newAbi["components"] = abi["components"];
    const uint8_t* array_bytes;
    uint32_t new_total_length = totalLength;
    if (isDynamicType(newAbi)) {
        uint32_t offset = 0;
        for (int j = 0; j < 32; j++) {
            offset <<= 8;
            offset |= bytes[i + j];
        }
        array_bytes = bytes + offset;
        new_total_length = (totalLength > offset) ? (totalLength - offset) : 0;
    } else {
        array_bytes = bytes + i;
        new_total_length = (totalLength > i) ? (totalLength - i) : 0;
    }

    uint32_t aI = 0;
    for (uint32_t j = 0; j < array_len; j++) {
        json elem = decodeElement(array_bytes, aI, newAbi, new_total_length);
        rs.push_back(elem);
        aI += getTypeSize(newAbi);
    }

    return rs;
}

vector<json> decodeSlice(const uint8_t* bytes, uint32_t i, json abi, uint32_t totalLength) {
    vector<json> rs;
    uint32_t offset = 0;
    for (int j = 0; j < 32; j++) {
        offset <<= 8;
        offset |= bytes[i + j];
    }

    uint32_t array_len = 0;
    for (int j = 0; j < 32; j++) {
        array_len <<= 8;
        array_len |= bytes[offset + j];
    }

    json newAbi;
    newAbi["name"] = abi["name"];
    newAbi["type"] = removeArraySizeFromType(abi["type"]);
    newAbi["components"] = abi["components"];
    uint32_t aI = 0;

    const uint8_t* slice_bytes = bytes + offset + 32;
    uint32_t new_total_length = (totalLength > (offset + 32)) ? (totalLength - (offset + 32)) : 0;
    for (uint32_t j = 0; j < array_len; j++) {
        json elem = decodeElement(slice_bytes, aI, newAbi, new_total_length);
        rs.push_back(elem);
        aI += getTypeSize(newAbi);
    }

    return rs;
}

json decodeFixedBytesTy(const uint8_t* bytes, uint32_t i, json abi) {
    const uint8_t* element_bytes = bytes + i;
    json result = json::object();
//    cout << "start decodeFixedBytesTy:  " << element_bytes << endl;
    result = bytesToHexString(element_bytes, 32);
//    cout << "DB decodeFixedBytesTy:  " << result << endl;
    
    std::string hexString = bytesToHexString(element_bytes, 32);
//    cout << "DB decodeFixedBytesTy - hexString:  " << hexString << endl;
    return hexString;
//    NSString * x = [Utilities convertBigEdianToString:];
//
//    return [x UTF8String];
}

json decodeBool(const uint8_t* bytes, uint32_t i, json abi) {
    const uint8_t* element_bytes = bytes + i;
    json result = json::object();
//    result = (element_bytes[31] == 1) ? "true" : "false";
    result = (element_bytes[31] == 1);
//    cout << "DB decodeBool:  " << result << endl;
    return result;
}

json decodeAddress(const uint8_t* bytes, uint32_t i, json abi) {
    const uint8_t* element_bytes = bytes + i;
    json result = json::object();
//    cout << "DB decodeAddress:  " << endl;
    result = bytesToHexString(element_bytes + 12, 20);
//    cout << "DB decodeAddress:  " << result << endl;
    return result;
}

json decodeBytes(const uint8_t* bytes, uint32_t i, json abi, uint32_t totalLength) {
//    cout << "DEBUG: decodeBytes bắt đầu với i = " << i << " và totalLength = " << totalLength << endl;
    
    // Kiểm tra đảm bảo có đủ dữ liệu cho 32 byte offset
    if (i + 32 > totalLength) {
//        cout << "ERROR: Không đủ dữ liệu để đọc offset từ vị trí i." << endl;
        return json();
    }

    uint32_t offset = 0;
    for (int j = 0; j < 32; j++) {
        offset <<= 8;
        offset |= bytes[i + j];
    }
//    cout << "DEBUG: Tính được offset = " << offset << endl;

    // Kiểm tra đảm bảo có đủ dữ liệu cho 32 byte length từ offset
    if (offset + 32 > totalLength) {
//        cout << "ERROR: Offset vượt quá giới hạn mảng khi đọc length." << endl;
        return json();
    }

    uint32_t length = 0;
    for (int j = 0; j < 32; j++) {
        length <<= 8;
        length |= bytes[offset + j];
    }
//    cout << "DEBUG: Tính được length = " << length << endl;
    
    // Kiểm tra đảm bảo có đủ dữ liệu cho phần dữ liệu cần decode
    if (offset + 32 + length > totalLength) {
//        cout << "ERROR: Không đủ dữ liệu để đọc element_bytes." << endl;
        return json();
    }

    const uint8_t* element_bytes = bytes + offset + 32; // 32 byte cho length
//    cout << "DEBUG: Gọi bytesToHexString với length = " << length << endl;
    json result = bytesToHexString(element_bytes, length);
    return result;
}


json decodeInt(const uint8_t* bytes, uint32_t i, json abi) {
    const uint8_t* element_bytes = bytes + i;
    json result = json::object();
//    cout << "start decodeint:  " << element_bytes << endl;
    std::string hexString = bytesToHexString(element_bytes, 32);
//    cout << "DB decodeInt:  " << result << endl;
    
//    std::string hexString = bytesToHexString(element_bytes, 32);
//    cout << "DB decodeInt - hexString:  " << hexString << endl;
    
    
    return hexString;
}


json decodeElement(const uint8_t* bytes, uint32_t i, json abi, uint32_t totalLength) {
    SolidityType t = getType(abi);
    json result = json::object();
    if (t == TupleTy) {
        result = decodeTuple(bytes, i, abi, totalLength);
    }
    if (t == SliceTy) {
        result = decodeSlice(bytes, i, abi, totalLength);
    }
    if (t == ArrayTy) {
        result = decodeArray(bytes, i, abi, totalLength);
    }
    if (t == StringTy) {
        result = decodeString(bytes, i, abi);
    }
    if (t == IntTy) {
        result = decodeInt(bytes, i, abi);
    }
    if (t == BoolTy) {
        result = decodeBool(bytes, i, abi);
    }
    if (t == BytesTy) {
        result = decodeBytes(bytes, i, abi, totalLength);
    }
    if (t == FixedBytesTy) {
        result = decodeFixedBytesTy(bytes, i, abi);
    }
    if (t == AddressTy) {
        result = decodeAddress(bytes, i, abi);
    }
    return result;
}

json decode(vector<uint8_t> bytes, string strAbi) {
    vector<json> abi = json::parse(strAbi);
    json result = json::object();
    uint32_t i = 0;
    for (const auto& elementAbi : abi) {
        json element_value = decodeElement(bytes.data(), i, elementAbi, bytes.size());
        result[string(elementAbi["name"])] = element_value;
        i += getTypeSize(elementAbi);
    }
    return result;
}
