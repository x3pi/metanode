go tool pprof -http=:8081 heap_baseline_start.prof
go tool pprof -http=:8082 heap_during_test.prof
go tool pprof -http=:8083 heap_immediately_after.prof
go tool pprof -http=:8084 heap_after_5min.prof
go tool pprof -http=:8085 heap_after_30sec.prof
curl -s "http://127.0.0.1:6061/debug/pprof/heap" > "heap_1.prof"
 go tool pprof -http=:8083 heap_90.prof

 go tool pprof heap_155.prof

 go tool pprof -http=:8085 heap_155.prof



key

  {
    "index": 49995,
    "private_key": "a70c079c7d118affc61170f6c8757aadfba8cbc714e1903f45e4daa06660efb7",
    "address": "0x294f72878a83B7d076E1d28eedecd184863df846"
  },
  {
    "index": 49996,
    "private_key": "da62671fae8ee9aee7d0aa8ecc57aca918565de0f88ac12990f313e9fc2de2bd",
    "address": "0x616969160142a381bb315A286fA54B7eD1749C49"
  },
  {
    "index": 49997,
    "private_key": "6451950adcce1e30efc6c029bbba140fdfcda79756047c8605709173d1600ff3",
    "address": "0x7EB655B6A3f58DE47CA598385ba531A8f4e156B1"
  },
  {
    "index": 49998,
    "private_key": "3b6e5c303928bd05a74394d4cf440b578a9fc4f41618011b51fa7ad1121e0d94",
    "address": "0x09f3fc68e7A532737903BD5111235e2eCfd7A31C"
  }