const http = require('http');

async function rpcCall(port, method, params) {
  const data = JSON.stringify({ jsonrpc: "2.0", method, params, id: 1 });
  return new Promise((resolve, reject) => {
    const req = http.request({
      hostname: '127.0.0.1', port, path: '/', method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Content-Length': data.length }
    }, res => {
      let body = '';
      res.on('data', chunk => body += chunk);
      res.on('end', () => resolve(JSON.parse(body)));
    });
    req.on('error', reject);
    req.write(data);
    req.end();
  });
}

async function main() {
  const txHash = "0x0b1f8f10c89bcdb50e11db14f1ad4b9795f6bafe198e687885cf30ae056eac5f";
  const tx0 = await rpcCall(8757, "eth_getTransactionByHash", [txHash]);
  const tx3 = await rpcCall(10750, "eth_getTransactionByHash", [txHash]);
  
  console.log("Tx0:", JSON.stringify(tx0.result, null, 2));
  console.log("Tx3:", JSON.stringify(tx3.result, null, 2));
}
main();
