
```go run main.go -test=batch-bls -n 10```

```go run main.go -test=tps -n 10000 -c 50```

# Test TCP

go run main.go -test=tps-single -mode=tcp -wallets=200 -txpw=5 -rounds=5 -pause=2

# Test HTTP (chạy riêng, sau khi TCP xong + đợi chain settle)

go run main.go -test=tps-single -mode=http -wallets=200 -txpw=5 -rounds=5 -pause=2
╔══════════════════════════════════════════════════════════════╗
║  AGGREGATE: TCP-DIRECT (5 rounds)
╠══════════════════════════════════════════════════════════════╣
║  Config:  200 wallets × 5 tx/wallet = 1000 total/round
║  ─────────────────────────────────────────────────
║  Avg TPS:         2061 tx/s
║  Min TPS:         1900 tx/s
║  Max TPS:         2153 tx/s
║  Total OK:        1000 / 5000
║  ─────────────────────────────────────────────────
║  Round │     TPS      │  Duration  │  OK/Total
║      1 │     2090 tx/s │      96ms  │  200/1000
║      2 │     2153 tx/s │      93ms  │  200/1000
║      3 │     2025 tx/s │      99ms  │  200/1000
║      4 │     1900 tx/s │     105ms  │  200/1000
║      5 │     2136 tx/s │      94ms  │  200/1000
╚══════════════════════════════════════════════════════════════╝

╔══════════════════════════════════════════════════════════════╗
║  AGGREGATE: HTTP-FORWARD (5 rounds)
╠══════════════════════════════════════════════════════════════╣
║  Config:  200 wallets × 5 tx/wallet = 1000 total/round
║  ─────────────────────────────────────────────────
║  Avg TPS:         1441 tx/s
║  Min TPS:          636 tx/s
║  Max TPS:         1843 tx/s
║  Total OK:        1000 / 5000
║  ─────────────────────────────────────────────────
║  Round │     TPS      │  Duration  │  OK/Total
║      1 │     1488 tx/s │     134ms  │  200/1000
║      2 │     1622 tx/s │     123ms  │  200/1000
║      3 │     1843 tx/s │     109ms  │  200/1000
║      4 │     1618 tx/s │     124ms  │  200/1000
║      5 │      636 tx/s │     314ms  │  200/1000
╚══════════════════════════════════════════════════════════════╝
