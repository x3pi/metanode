import { Transaction } from "./transaction_bls/transaction";
import { transaction } from "./proto/transaction";
import bls from "bls-eth-wasm";

// ✅ Chuyển khóa riêng từ hex sang Uint8Array
function hexToUint8Array(hex: string): Uint8Array {
  return new Uint8Array(hex.match(/.{1,2}/g)!.map(byte => parseInt(byte, 16)));
}

async function main() {
  await bls.init(bls.BLS12_381); // 🔄 Khởi tạo thư viện BLS

  // 🛠 Tạo một giao dịch giả lập
  const txProto = new transaction.Transaction();
  txProto.Amount = new Uint8Array([13, 224, 182, 179, 167, 100, 0, 0]);

  const tx = new Transaction(txProto);

  // 🔑 Chuyển đổi khóa riêng từ hex
  const privateKeyHex = "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b";
  const privateKeyBytes = hexToUint8Array(privateKeyHex);

  // 📌 Tạo đối tượng BLS PrivateKey
  const blsPrivateKey = new bls.SecretKey();
  blsPrivateKey.deserialize(privateKeyBytes);

  // ✍️ Ký giao dịch
  await tx.setSign(blsPrivateKey.serialize());
  
  console.log(tx.getAmount())

  // 📌 In chữ ký
  console.log("Chữ ký của giao dịch (hex):", Buffer.from(txProto.Sign).toString("hex"));
}

main();
