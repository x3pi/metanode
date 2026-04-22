import bls from 'bls-eth-wasm';
import { keccak256 } from 'ethereum-cryptography/keccak.js';
import { transaction } from "../proto/transaction";

/**
 * Tạo một đối tượng CallData mới
 * @param input Dữ liệu đầu vào dạng Uint8Array
 * @returns Đối tượng CallData
 */
export function NewCallData(input: Uint8Array): CallData {
    const callData = new transaction.CallData()
    callData.Input = input
    return new CallData(callData); // Sửa đổi ở đây
}

class CallData {
    private proto: transaction.CallData; // Đối tượng giao dịch gốc

    constructor(proto: transaction.CallData) {
        this.proto = proto;
    }

    Proto(): transaction.CallData {
        return this.proto;
    }
    // ... các phương thức khác của CallData nếu cần ...
}
