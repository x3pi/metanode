import { createPublicClient, http, Hex, createWalletClient } from 'viem';
import { localhost } from 'viem/chains';
import { Transaction } from '../transaction_bls/transaction';
import { NewCallData } from '../transaction_bls/call_data';

import { transaction } from '../proto/transaction';
import bls from 'bls-eth-wasm';
import { keccak256 } from 'ethereum-cryptography/keccak.js';
import { privateKeyToAccount } from 'viem/accounts';
import { secp256k1 } from '@noble/curves/secp256k1';

const client = createPublicClient({
  chain: { ...localhost, id: 991 },
  transport: http('http://127.0.0.1:8646'),
});
// const privateKeyHex =
// ('0x5f611258bf9bb81bde8aa52432d8794011e7861f175e9fe75e8c66a87edfe77d');

const privateKeyHex =
  '722ed7cab5e0a3584b9a19f71d1b9e36454fafd1f0fe1613a98c6d66114b3416';
// Chuyển privateKey thành Uint8Array hợp lệ
const privateKeyBytesR = Uint8Array.from(
  Buffer.from(privateKeyHex.replace(/^0x/, ''), 'hex')
);
// const account = privateKeyToAccount(privateKeyHex);
// console.log('account -->', account);

// const from = '0xea3db8925b49b5452ed74959eb86bbbb7fab59ce';
const from = '0x9819B235DC91377E3B7AB64733f1865613934e1e';

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
    const state = (await client.request({
      method: 'mtn_getAccountState' as any,
      params: [address, 'latest'],
    })) as AccountState;

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
      publicKeyBls: state.publicKeyBls ?? '',
    };
  } catch (error) {
    console.error('Lỗi khi lấy trạng thái tài khoản:', error, address);
    return null;
  }
}

async function getDeviceKey(lasThash: Hex): Promise<any | null> {
  try {
    const state = (await client.request({
      method: 'mtn_getDeviceKey' as any,
      params: [lasThash],
    })) as AccountState;

    if (!state) {
      throw new Error('Không nhận được dữ liệu trạng thái tài khoản');
    }
    return state;
  } catch (error) {
    console.error('Lỗi khi lấy trạng thái tài khoản:', error);
    return null;
  }
}

// Hàm chuyển đổi Uint8Array thành chuỗi hex
const uint8ArrayToHex = (uint8Array: Uint8Array): Hex => {
  return ('0x' + Buffer.from(uint8Array).toString('hex')) as Hex;
};

// Chuyển khóa riêng từ hex sang Uint8Array
function hexToUint8Array(hex: string): Uint8Array {
  return new Uint8Array(
    hex.match(/.{1,2}/g)!.map((byte) => parseInt(byte, 16))
  );
}

const generateDeviceKey = async (
  lastHash: Uint8Array
): Promise<{ rawNewDeviceKey: Uint8Array; newDeviceKey: Uint8Array }> => {
  // Chuyển đổi lastHash thành chuỗi hex
  const lastHashHex = '0x' + Buffer.from(lastHash).toString('hex');

  // Tạo chuỗi để tạo device key
  const rawNewDeviceKeyString = `${lastHashHex}-${Date.now()}`;

  // Tính toán hash của chuỗi trên
  const rawNewDeviceKey = new Uint8Array(
    await keccak256(new TextEncoder().encode(rawNewDeviceKeyString))
  );

  // Tính toán hash lần thứ hai
  const newDeviceKey = new Uint8Array(await keccak256(rawNewDeviceKey));

  return { rawNewDeviceKey, newDeviceKey };
};

function bigIntToBytes(bigInt: bigint, byteLength: number = 32): Uint8Array {
  if (bigInt < 0n) throw new Error('BigInt must be non-negative');

  let hex = bigInt.toString(16); // Chuyển BigInt sang hex string
  if (hex.length % 2) hex = '0' + hex; // Đảm bảo số lượng ký tự là chẵn

  let bytes = Uint8Array.from(Buffer.from(hex, 'hex')); // Chuyển hex thành Uint8Array

  // Kiểm tra nếu số bytes vượt quá giới hạn
  if (bytes.length > byteLength) {
    throw new Error('BigInt quá lớn so với độ dài byte mong muốn');
  }

  // Tạo mảng Uint8Array có độ dài cố định với giá trị mặc định là 0
  let paddedBytes = new Uint8Array(byteLength);
  paddedBytes.set(bytes, byteLength - bytes.length); // Căn phải (big-endian)

  return paddedBytes;
}
const sendNativeCoin = async (
  to: string,
  value: bigint,
  data: string
): Promise<Hex> => {
  // Get acountState
  const accountState = await getAccountState(from);
  console.log('sendNativeCoin accountState --> ', accountState);
  if (!accountState) {
    return '0x';
  } else {
    const lastHash = accountState?.lastHash;
    const lastDevicekey =
      lastHash ==
      '0x0000000000000000000000000000000000000000000000000000000000000000'
        ? '0x0000000000000000000000000000000000000000000000000000000000000000'
        : await getDeviceKey(accountState?.lastHash);

    // Tạo giao dịch
    const txProto = new transaction.Transaction();
    const amountBytes = new Uint8Array(new BigUint64Array([value]).buffer);
    const newDeviceKey = await generateDeviceKey(
      hexToUint8Array(accountState?.lastHash)
    );
    txProto.Amount = bigIntToBytes(value);

    txProto.MaxGas = 100000;
    txProto.MaxGasPrice = 1000000000;
    txProto.MaxTimeUse = 6000;
    txProto.LastDeviceKey = hexToUint8Array(lastDevicekey);
    txProto.NewDeviceKey = newDeviceKey.newDeviceKey;
    txProto.ChainID = 991;
    const callData = NewCallData(hexToBuffer(data))
    if (data != null && data.length > 0) {
      console.log(
        'data send --> ',
        data,
        data.length,
        hexToUint8Array(data).length,
        hexToBuffer(data).length
      );
      txProto.Data = callData.Proto().serializeBinary();
    }
    console.log('txProto.Data', txProto.Data);
    console.log('s0', uint8ArrayToHex(txProto.serialize()), from, to);
    const tx = new Transaction(txProto);
    tx.setFromAddress(from);
    tx.setToAddress(to);
    tx.setNonce(accountState ? accountState.nonce : 0);
    // Ký giao dịch

    console.log('s1', uint8ArrayToHex(tx.marshal()));
    // 🔑 Chuyển đổi khóa riêng từ hex
    const privateKeyBytes = hexToUint8Array(privateKeyHex);
    await bls.init(bls.BLS12_381);
    // 📌 Tạo đối tượng BLS PrivateKey
    const blsPrivateKey = new bls.SecretKey();
    console.log('s2');
    blsPrivateKey.deserialize(privateKeyBytes);
    console.log('s3');

    // ✍️ Ký giao dịch
    await tx.setSign(blsPrivateKey.serialize());

    console.log('s4');
    // Get device key

    // Tạo TransactionWithDeviceKey
    const txWDeviceKey = new transaction.TransactionWithDeviceKey();
    txWDeviceKey.DeviceKey = newDeviceKey.rawNewDeviceKey;
    txWDeviceKey.Transaction = tx.Proto();

    // Gửi giao dịch
    const txB = txWDeviceKey.serialize();

    return client.request({
      method: 'mtn_sendRawTransactionWithDeviceKey' as any,
      params: [uint8ArrayToHex(txB)],
    });
  }
};

function hexToBuffer(hex: string) {
  if (hex.startsWith('0x')) hex = hex.slice(2); // Loại bỏ tiền tố '0x' nếu có
  return Buffer.from(hex, 'hex'); // Tạo buffer từ hex
}

const AddAccountForClient = async () => {
  const publicKey =
    '9371360f70bd6e4babb32fc24093e8762ca85790bf06890eafa9c7d0561d80d83263d6b03eb2f0debbb4959ae04807fa';
  const num = '991';
  const buffer = Buffer.from('991', 'utf8');

  const hexString = buffer.toString('hex'); // Chuyển số thành hex

  console.log(hexString); // Output: "3df"

  const message = publicKey + hexString;
  console.log('message', message);
  const hash = keccak256(hexToBuffer(message));
  console.log('hash = ', uint8ArrayToHex(hash));
  const signature = secp256k1.sign(hash, privateKeyBytesR);
  const rHex = signature.r.toString(16).padStart(64, '0'); // 32 bytes (64 hex)
  const sHex = signature.s.toString(16).padStart(64, '0'); // 32 bytes (64 hex)
  const recoveryHex = signature.recovery.toString(16).padStart(2, '0'); // 1 byte (2 hex)

  // Ghép lại full chữ ký: 64 + 64 + 2 = 130 ký tự hex (65 bytes)
  const fullSignature = `${rHex}${sHex}${recoveryHex}`;
  console.log('🔏 Full Signature:', fullSignature);
  console.log('sign --> ', signature);
  const data = publicKey + fullSignature;
  console.log('data --> ', data, data.length);
  const txHash = await sendNativeCoin(from, 0n, data);
  console.log('txHash = ', txHash);
  //   const accountState = await getAccountState(from);
  //   console.log('accountState-12', accountState?.nonce);
};

const ac = await getAccountState(from);
console.log('accountState-1', ac);
if (ac?.nonce == 0) {
  AddAccountForClient();
} else {
  const txHash = await sendNativeCoin(
    '0x19D06bEDFD6EAB039A7d59ADf3FccD6EB6e236d7',
    0n,
    ''
  );
  console.log(txHash);
  monitorTransaction(txHash)
}


// Function to monitor a transaction until it's confirmed
async function monitorTransaction(txHash: `0x${string}`): Promise<boolean> {
  console.log(`Monitoring transaction: ${txHash}`);
  
  return new Promise((resolve, reject) => {
      const unwatch = client.watchBlockNumber({
          onBlockNumber: async (blockNumber: bigint) => {
              try {
                  console.log(`New block mined: ${blockNumber}`);
                  
                  const receipt = await client.getTransactionReceipt({
                      hash: txHash,
                  });
                  
                  if (receipt) {
                      // Always clean up the watcher when we have a receipt
                      unwatch();
                      
                      if (receipt.status === 'success') {
                          console.log(`Transaction successful at block ${receipt.blockNumber}!`);
                          console.log(`Gas used: ${receipt.gasUsed}`);
                          resolve(true);
                      } else {
                          console.log(`Transaction failed at block ${blockNumber}.`);
                          reject(new Error(`Transaction failed: ${txHash}`));
                      }
                  }
                  // If no receipt is found, continue watching for new blocks
              } catch (error) {
                  console.error('Error while checking transaction receipt:', error);
                  unwatch();
                  reject(error);
              }
          },
          onError: (error) => {
              console.error('Error in block watcher:', error);
              unwatch();
              reject(error);
          },
          poll: true,
          pollingInterval: 3000, // Check every 3 seconds
      });
  });
}