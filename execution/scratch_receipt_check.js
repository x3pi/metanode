const http = require('http');

function callRPC(port, method, params) {
    return new Promise((resolve, reject) => {
        const req = http.request({
            host: '127.0.0.1',
            port: port,
            method: 'POST',
            headers: { 'Content-Type': 'application/json' }
        }, res => {
            let body = '';
            res.on('data', chunk => body += chunk);
            res.on('end', () => {
                if (!body) {
                    return reject(new Error(`Empty response from port ${port}`));
                }
                try {
                    const parsed = JSON.parse(body);
                    resolve(parsed);
                } catch (e) {
                    reject(new Error(`Invalid JSON from port ${port}: ${body.substring(0, 50)}`));
                }
            });
        });
        req.on('error', reject);
        req.write(JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }));
        req.end();
    });
}

async function main() {
    const blockNum = '0x1c'; // 28
    const ports = [8757, 10750]; // node 0 is 10100, node 3 is 10103
    // Wait, is node 3 actually 10748? Node 0 is 8747 or 10100?
    // The cluster output showed: 
    // RPC server started on 127.0.0.1:10100 for node 0.
    // But let's check config if it fails.

    const blocks = [];
    for (const port of ports) {
        try {
            const res = await callRPC(port, 'eth_getBlockByNumber', [blockNum, true]);
            if (!res.result) {
                console.log(`Node on port ${port} returned null block`);
                continue;
            }
            blocks.push(res.result);
            console.log(`Node on port ${port} has ${res.result.transactions.length} transactions`);
        } catch (e) {
            console.error(`Failed on port ${port}:`, e.message);
        }
    }

    if (blocks.length < 2) return;

    const b0 = blocks[0];
    const b1 = blocks[1];

    if (b0.transactions.length !== b1.transactions.length) {
        console.log("Different number of transactions!");
        return;
    }

    for (let i = 0; i < b0.transactions.length; i++) {
        const t0 = b0.transactions[i];
        const t1 = b1.transactions[i];

        // Fetch receipts
        const r0 = await callRPC(ports[0], 'eth_getTransactionReceipt', [t0.hash]);
        const r1 = await callRPC(ports[1], 'eth_getTransactionReceipt', [t1.hash]);

        const str0 = JSON.stringify(r0.result);
        const str1 = JSON.stringify(r1.result);

        if (str0 !== str1) {
            console.log(`\nMISMATCH in Tx ${i} (Hash: ${t0.hash}):`);

            // Compare fields
            for (const key in r0.result) {
                if (JSON.stringify(r0.result[key]) !== JSON.stringify(r1.result[key])) {
                    console.log(`Field ${key} differs:`);
                    console.log(`  Node0:`, JSON.stringify(r0.result[key]));
                    console.log(`  Node3:`, JSON.stringify(r1.result[key]));
                }
            }
        }
    }
}
main();
