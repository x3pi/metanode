import { createPublicClient, http, Hex } from 'viem';
import { localhost } from 'viem/chains';
import { Transaction } from "../transaction_bls/transaction";
import { transaction } from "../proto/transaction";
import bls from "bls-eth-wasm";
import { keccak256 } from 'ethereum-cryptography/keccak.js';

const client = createPublicClient({
    chain: { ...localhost, id: 991 },
    transport: http('http://127.0.0.1:8545'),
});


const privateKeyHex = "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b";
const from = "0x1eF5a8c5403c5aA1d43a8DC9eD391351012551Cf"


interface AccountState {
    accountType: number;
    address: Hex;
    balance: string;
    deviceKey: string;
    lastHash: Hex;
    nonce: number;
    pendingBalance: string;
    publicKeyBls: string;
}

async function getAccountState(address: Hex): Promise<AccountState | null> {
    try {
        const state = await client.request({
            method: 'mtn_getAccountState' as any,
            params: [address, 'latest'],
        }) as AccountState;

        if (!state) {
            throw new Error('Không nhận được dữ liệu trạng thái tài khoản');
        }

        return {
            accountType: state.accountType ?? 0,
            address: address,
            balance: state.balance ?? '0',
            deviceKey: state.deviceKey ?? '',
            lastHash: state.lastHash ?? '',
            nonce: state.nonce ?? 0,
            pendingBalance: state.pendingBalance ?? '0',
            publicKeyBls: state.publicKeyBls ?? ''
        };
    } catch (error) {
        console.error('Lỗi khi lấy trạng thái tài khoản:', error);
        return null;
    }
}

async function getDeviceKey(lasThash: Hex): Promise<any | null> {
    try {
        const state = await client.request({
            method: 'mtn_getDeviceKey' as any,
            params: [lasThash],
        }) as AccountState;

        if (!state) {
            throw new Error('Không nhận được dữ liệu trạng thái tài khoản');
        }
        return state
    } catch (error) {
        console.error('Lỗi khi lấy trạng thái tài khoản:', error);
        return null;
    }
}



// Hàm chuyển đổi Uint8Array thành chuỗi hex
const uint8ArrayToHex = (uint8Array: Uint8Array): Hex => {
    return '0x' + Buffer.from(uint8Array).toString('hex') as Hex;
};

// Chuyển khóa riêng từ hex sang Uint8Array
function hexToUint8Array(hex: string): Uint8Array {
    return new Uint8Array(hex.match(/.{1,2}/g)!.map(byte => parseInt(byte, 16)));
}

const generateDeviceKey = async (lastHash: Uint8Array): Promise<{ rawNewDeviceKey: Uint8Array; newDeviceKey: Uint8Array }> => {
    // Chuyển đổi lastHash thành chuỗi hex
    const lastHashHex = '0x' + Buffer.from(lastHash).toString('hex');

    // Tạo chuỗi để tạo device key
    const rawNewDeviceKeyString = `${lastHashHex}-${Date.now()}`;

    // Tính toán hash của chuỗi trên
    const rawNewDeviceKey = new Uint8Array(await keccak256(new TextEncoder().encode(rawNewDeviceKeyString)));

    // Tính toán hash lần thứ hai
    const newDeviceKey = new Uint8Array(await keccak256(rawNewDeviceKey));

    return { rawNewDeviceKey, newDeviceKey };
};

function bigIntToBytes(bigInt: bigint, byteLength: number = 32): Uint8Array {
    if (bigInt < 0n) throw new Error("BigInt must be non-negative");

    let hex = bigInt.toString(16); // Chuyển BigInt sang hex string
    if (hex.length % 2) hex = '0' + hex; // Đảm bảo số lượng ký tự là chẵn

    let bytes = Uint8Array.from(Buffer.from(hex, 'hex')); // Chuyển hex thành Uint8Array

    // Kiểm tra nếu số bytes vượt quá giới hạn
    if (bytes.length > byteLength) {
        throw new Error("BigInt quá lớn so với độ dài byte mong muốn");
    }

    // Tạo mảng Uint8Array có độ dài cố định với giá trị mặc định là 0
    let paddedBytes = new Uint8Array(byteLength);
    paddedBytes.set(bytes, byteLength - bytes.length); // Căn phải (big-endian)

    return paddedBytes;
}
const sendNativeCoin = async (to: string, value: bigint): Promise<Hex> => {


    // Get acountState
    const accountState = await getAccountState(from)
    if (!accountState) {
        return '0x'
    } else {
        console.log(accountState?.lastHash)
        console.log(accountState)
        var lastDevicekey = "0x0000000000000000000000000000000000000000000000000000000000000000"
        if (accountState?.lastHash != "0x0000000000000000000000000000000000000000000000000000000000000000") {
            lastDevicekey = await getDeviceKey(accountState?.lastHash)

        }

        // Tạo giao dịch
        const txProto = new transaction.Transaction()
        const amountBytes = new Uint8Array(new BigUint64Array([value]).buffer);
        const newDeviceKey = await generateDeviceKey(hexToUint8Array(accountState?.lastHash))
        txProto.Amount = bigIntToBytes(value)

        txProto.MaxGas = 1000000
        txProto.MaxGasPrice = 10000000
        txProto.MaxTimeUse = 600
        txProto.LastDeviceKey = hexToUint8Array(lastDevicekey)
        txProto.NewDeviceKey = newDeviceKey.newDeviceKey
        txProto.ChainID = 991
        const tx = new Transaction(txProto);
        tx.setFromAddress(from)
        tx.setToAddress(to)
        tx.setNonce(accountState ? accountState.nonce : 0)

        // Ký giao dịch

        // 🔑 Chuyển đổi khóa riêng từ hex
        const privateKeyBytes = hexToUint8Array(privateKeyHex);
        await bls.init(bls.BLS12_381);

        // 📌 Tạo đối tượng BLS PrivateKey
        const blsPrivateKey = new bls.SecretKey();
        blsPrivateKey.deserialize(privateKeyBytes);

        // ✍️ Ký giao dịch
        await tx.setSign(blsPrivateKey.serialize());

        // Get device key


        // Tạo TransactionWithDeviceKey
        const txWDeviceKey = new transaction.TransactionWithDeviceKey()
        txWDeviceKey.DeviceKey = newDeviceKey.rawNewDeviceKey
        txWDeviceKey.Transaction = tx.Proto()


        // Gửi giao dịch
        const txB = txWDeviceKey.serialize();
        return client.request({
            method: 'mtn_sendRawTransactionWithDeviceKey' as any,
            params: [uint8ArrayToHex(txB)],
        });

    }
};

const txHash = await sendNativeCoin("0x5AE1e723973577AcaB776ebC4be66231fc57b370", 800000000000000000000n)
console.log(txHash)