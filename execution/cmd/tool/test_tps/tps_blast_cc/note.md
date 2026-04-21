-count 10000 Số TXs
-batch 500 TXs per batch
-sleep 10 ms giữa mỗi batch
-wait 120 Max seconds đợi chain xử lý
-config ../../../cmd/tool/tps_blast/config.json Client config
-keys ../gen_spam_keys/generated_keys.json Keys file
-recipient ADDRESS_LOCK_BALANCE Recipient address
-dest 2 Destination chain ID
-node từ config Override node TCP address
-rpc auto

go run main.go --count 10000

``` bash
single chỉ test kh có load blance khi fetch nonce
--verify : verify sau khi giao dịch
go run main_single.go --count 50000 --parallel_native=true --rounds 5 --load_balance=true  --batch=500
go run main_single.go --count 1000 --parallel_native=true --rounds 5 --load_balance=true  --batch=500 

go run main.go --count 20000 --parallel_native=true --rounds 5 --load_balance=true  --batch=500 
go run main.go --count 30000 --parallel_native=true --rounds 20 --load_balance=true  --batch=500 
go run main.go --count 50000 --parallel_native=true --rounds 5 --load_balance=true --batch=10000 --sleep=0
go run main.go --count 1000 --parallel_native=true --rounds 1 --load_balance=false  --batch=5 --verify
go run main.go --count 20000 --parallel_native=true --rounds 5 --load_balance=false  --batch=500

```

``` bash
./generate_reports.sh 0 1 2 3
```

# get logs

grep -rn "\[MVM CLEANUP\]" /home/abc/nhat/con-chain-v2/mtn-consensus/metanode/logs/node_0

grep -rn " Hoàn thành đồng bộ (Lưu" /home/abc/nhat/consensus-chain/mtn-consensus/metanode/logs/node_1

# Check master (phải trả nonce cao)

curl -s <http://192.168.1.234:8646> -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getTransactionCount","params":["0x4474E7E565E684bE0f054322431F5273817e696A","latest"],"id":1}'

# Check sub-node 233 (đang trả nonce=1, PHẢI bằng master)

curl -s <http://192.168.1.233:10650> -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getTransactionCount","params":["0x4474E7E565E684bE0f054322431F5273817e696A","latest"],"id":1}'

# Check sub-node 231

curl -s <http://192.168.1.234:10747> -X POST -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"eth_getTransactionCount","params":["0x4474E7E565E684bE0f054322431F5273817e696A","latest"],"id":1}'

╔═══════════════════════════════════════════════════╗
║  📊 BENCHMARK SUMMARY
╠═══════════════════════════════════════════════════╣
║  🔄 Rounds         : 5
║  📤 TXs per round  : 50000
║  ─────────────────────────────────────────────────
║  Round 1  TPS      : ~6508 tx/s
║  Round 2  TPS      : ~6856 tx/s
║  Round 3  TPS      : ~6715 tx/s
║  Round 4  TPS      : ~7006 tx/s
║  Round 5  TPS      : ~7077 tx/s
║  ─────────────────────────────────────────────────
║  📉 Min TPS        : ~6508 tx/s
║  📈 Max TPS        : ~7077 tx/s
║  📊 Avg TPS        : ~6832 tx/s
╚═══════════════════════════════════════════════════╝
