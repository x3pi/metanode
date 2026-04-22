// file: parse-revert.js

// Yêu cầu: Đảm bảo bạn đã cài đặt đúng phiên bản node-fetch: npm install node-fetch@2
// Tắt kiểm tra chứng chỉ TLS (chỉ dùng cho môi trường test local, không dùng trong production)
process.env["NODE_TLS_REJECT_UNAUTHORIZED"] = 0;

const { ethers } = require("ethers");
const fetch = require("node-fetch");

/**
 * RPC endpoint
 */
const RPC = "https://rpc-proxy-sequoia.iqnb.com:8446/";
const provider = new ethers.JsonRpcProvider(RPC);

// ABI của contract, bao gồm custom errors để giải mã revert reason
const ABI = [
  "function transfer(address to, uint256 amount) external returns (bool)",
  "error ERC20InsufficientBalance(address sender, uint256 balance, uint256 needed)",
  "error ERC20InsufficientAllowance(address spender, uint256 allowance, uint256 needed)"
];
const iface = new ethers.Interface(ABI);

/**
 * decodeTxInput: Giải mã calldata của giao dịch.
 */
function decodeTxInput(txData) {
  try {
    const parsed = iface.parseTransaction({ data: txData });
    return { functionName: parsed.name, args: parsed.args, signature: parsed.signature };
  } catch {
    return { functionName: null, args: null, selector: txData.slice(0, 10) };
  }
}

/**
 * decodeRevertData: Giải mã dữ liệu revert từ eth_call hoặc giao dịch thất bại.
 * Xử lý cả Error(string) tiêu chuẩn và custom errors được định nghĩa trong ABI.
 */
function decodeRevertData(revertDataHex) {
  if (!revertDataHex || revertDataHex === "0x") {
    return { kind: "none", message: null };
  }

  // Kiểm tra selector của Error(string) tiêu chuẩn: 0x08c379a0
  const ERROR_STRING_SELECTOR = "0x08c379a0";
  if (revertDataHex.startsWith(ERROR_STRING_SELECTOR)) {
    try {
      // Bỏ qua 4 bytes selector đầu tiên để lấy nội dung lỗi string
      const reason = ethers.AbiCoder.defaultAbiCoder().decode(["string"], ethers.dataSlice(revertDataHex, 4));
      return { kind: "Error(string)", message: reason[0] };
    } catch (e) {
      return { kind: "Error(string)", message: "<decoding failed>", raw: revertDataHex };
    }
  }

  // Cố gắng giải mã dưới dạng custom error dựa trên ABI
  try {
    const parsed = iface.parseError(revertDataHex);
    return { kind: "custom", name: parsed.name, args: parsed.args, signature: parsed.signature };
  } catch (e) {
    // Không khớp với bất kỳ lỗi nào đã biết
    return { kind: "unknown", raw: revertDataHex, selector: ethers.dataSlice(revertDataHex, 0, 4) };
  }
}

/**
 * rawEthCall: Hàm fallback để gọi JSON-RPC trực tiếp.
 * Được sử dụng khi provider.call của ethers.js không trả về dữ liệu revert trong exception.
 */
async function rawEthCall(callParams, blockTag = "latest") {
  const payload = {
    jsonrpc: "2.0",
    id: 1,
    method: "eth_call",
    params: [callParams, blockTag]
  };

  const res = await fetch(RPC, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload)
  });

  const out = await res.json();

  if (out.error) {
    // Một số node RPC trả về dữ liệu revert bên trong trường error.data
    if (out.error.data) {
      return out.error.data;
    }
    throw new Error(`RPC Error: ${out.error.message || JSON.stringify(out.error)}`);
  }
  return out.result;
}

/**
 * runSampleCall: Mô phỏng một cuộc gọi giao dịch để giải mã các revert có thể xảy ra.
 */
async function runSampleCall() {
  const callParams = {
    from: "0xf8d8ffea90386473317caba62db66c3cb047880b",
    to: "0x5Ab7A4478136ab12Ed32B2D098DE632c68f49578",
    data: "0xa9059cbb00000000000000000000000081be168b04f315f263288034c0fb33d30ad3235300000000000000000000000000000000000000000000021e19e0c9bab2400000"
  };

  console.log("Attempting eth_call with parameters:");
  console.log(JSON.stringify(callParams, null, 2));
  console.log("----------------------------------------");

  try {
    // 1. Thử gọi bằng provider của ethers.
    const resultHex = await provider.call(callParams, "latest");
    console.log("Call successful (no revert).");
    console.log("Result Hex:", resultHex);
    console.log("Decoded data:", decodeRevertData(resultHex));

  } catch (error) {
    // 2. Bắt exception: Đây là nơi xử lý revert theo chuẩn của ethers.js.
    console.error(`[INFO] Call reverted or failed. Error: ${error.message.split('\n')[0]}`);

    let revertData = null;

    // Chuẩn ethers.js: Dữ liệu revert nằm trong `error.data`.
    if (error.data) {
      revertData = error.data;
      console.log("[INFO] Extracted revert data from ethers exception object.");
    } else {
      // 3. Fallback: Nếu exception không chứa data, thử gọi RPC thô.
      console.warn("[WARN] Revert data not found in exception. Attempting raw RPC call fallback...");
      try {
        revertData = await rawEthCall(callParams, "latest");
        console.log("[INFO] Fallback call successful.");
      } catch (fallbackError) {
        console.error("[ERROR] Raw eth_call fallback also failed:", fallbackError);
        return;
      }
    }

    // 4. Giải mã dữ liệu revert đã trích xuất.
    if (revertData) {
      console.log("Raw Revert Data:", revertData);
      const decodedRevert = decodeRevertData(revertData);

      // --- SỬA LỖI BigInt ---
      // Hàm replacer để chuyển đổi BigInt thành String khi dùng JSON.stringify
      // Vì JSON.stringify không hỗ trợ kiểu BigInt nguyên bản.
      const replacer = (key, value) => {
        if (typeof value === 'bigint') {
          return value.toString();
        }
        return value;
      };

      console.log("Decoded Revert Info:", JSON.stringify(decodedRevert, replacer, 2));
    } else {
      console.log("[INFO] No revert data could be extracted.");
    }
  }
}

/**
 * Main execution logic
 */
if (require.main === module) {
  const txHash = process.argv[2];
  if (!txHash) {
    console.log("No transaction hash provided. Running sample eth_call simulation...\n");
    runSampleCall().catch(console.error);
  } else {
    console.log("Analyzing specific transaction hash is not implemented in this script.");
  }
}