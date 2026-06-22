export const defaultRpcUrl = "http://localhost:8545";

export async function callRpc(method, params = [], url = defaultRpcUrl) {
  try {
    const response = await fetch(url, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        jsonrpc: "2.0",
        method: method,
        params: params,
        id: 1,
      }),
    });
    const data = await response.json();
    if (data.error) {
      throw new Error(data.error.message || JSON.stringify(data.error));
    }
    return data.result;
  } catch (error) {
    console.error(`RPC Error (${method}):`, error);
    throw error;
  }
}

export async function getPerformanceMetrics(url = defaultRpcUrl, limit = 100) {
  return await callRpc("mtn_getPerformanceMetrics", [limit], url);
}

export async function getBlockByNumber(url = defaultRpcUrl, blockNumber) {
  // Convert block number to hex if it's a number
  const hexBlockNum = "0x" + Number(blockNumber).toString(16);
  return await callRpc("eth_getBlockByNumber", [hexBlockNum, true], url);
}

export async function getTransactionByHash(url = defaultRpcUrl, txHash) {
  return await callRpc("eth_getTransactionByHash", [txHash], url);
}

export async function getAccountState(url = defaultRpcUrl, address) {
  return await callRpc("mtn_getAccountState", [address, "latest"], url);
}

export async function getLatestBlockNumber(url = defaultRpcUrl) {
  return await callRpc("eth_blockNumber", [], url);
}

export async function getBalance(url = defaultRpcUrl, address) {
  return await callRpc("eth_getBalance", [address, "latest"], url);
}

export async function getTransactionCount(url = defaultRpcUrl, address) {
  return await callRpc("eth_getTransactionCount", [address, "latest"], url);
}

export async function getChainId(url = defaultRpcUrl) {
  return await callRpc("eth_chainId", [], url);
}

export async function getNetworkVersion(url = defaultRpcUrl) {
  return await callRpc("net_version", [], url);
}

export async function getTransactionHistoryByAddress(url = defaultRpcUrl, address, offset = 0, limit = 10) {
  return await callRpc("mtn_getTransactionHistoryByAddress", [address, offset, limit], url);
}

export async function getTransactionReceipt(url = defaultRpcUrl, txHash) {
  return await callRpc("eth_getTransactionReceipt", [txHash], url);
}
