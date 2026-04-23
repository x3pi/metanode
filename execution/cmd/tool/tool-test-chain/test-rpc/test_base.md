
```bash
go run -tags tool base.go -config=./base/config-getlogs.json
```


# Tool check state all RPC (base.go)

Mục tiêu:
- Dùng 1 tool chung để gọi nhiều RPC cùng lúc.
- Chọn kiểu kiểm tra qua trường `type`.
- Có thể mở rộng dễ cho các method khác.

File chính:
- `base.go`

Config mặc định:
- `config-local.json`

## 1) Cấu trúc config

```json
{
	"rpc_urls": [
		"http://127.0.0.1:8757",
		"http://127.0.0.1:10747"
	],
	"type": "get_logs",
	"timeout_seconds": 10,
	"params": {
		"from_block": "0x1",
		"to_block": "0x1"
	}
}
```
## 2) Các type đang hỗ trợ

### A. get_logs

Gọi method `eth_getLogs`, chạy đồng thời 2 case và in ra số log theo từng RPC:
- Case 1: không filter (chỉ dùng `fromBlock`, `toBlock`)
- Case 2: có filter (dùng thêm `address`, `topics` nếu có)

Params:
- Bắt buộc: `from_block`
- Tùy chọn: `to_block` (mặc định = `from_block`)
- Tùy chọn: `address`
- Tùy chọn: `topics`

Lưu ý block number:
- Bạn có thể truyền số thường (`83`) hoặc chuỗi số (`"83"`), tool sẽ tự convert sang hex (`0x53`) trước khi gọi RPC.
- Vẫn hỗ trợ truyền trực tiếp hex (`"0x53"`) hoặc block tag (`latest`, `pending`, `earliest`, `safe`, `finalized`).

Ví dụ:

```json
{
	"rpc_urls": [
		"http://127.0.0.1:8757",
		"http://127.0.0.1:10747",
		"http://127.0.0.1:10749",
		"http://127.0.0.1:10750"
	],
	"type": "get_logs",
	"timeout_seconds": 10,
	"params": {
		"from_block": "0x1",
		"to_block": "0x1",
		"address": "0x00000000000000000000000000000000B429C0B2",
		"topics": [
			[
				"0xb528e3a3d4cbfd0b61a83cc28a004e801777b8ed6274adee62a727632fee66dd",
				"0xa92be8788ad097ce638b4b327d9930cc1d8545abf05c0a399f37b7a6ce8b94ce"
			]
		]
	}
}
```

### B. account_state

Gọi method `mtn_getAccountState`, in ra `nonce` theo từng RPC.

Params:
- Bắt buộc: `address`
- Tùy chọn: `block_tag` (mặc định `latest`)

Ví dụ:

```json
{
	"rpc_urls": [
		"http://127.0.0.1:8757",
		"http://127.0.0.1:10747"
	],
	"type": "account_state",
	"params": {
		"address": "0x1234567890abcdef1234567890abcdef12345678",
		"block_tag": "latest"
	}
}
```

### C. get_chain_id

Gọi method `eth_chainId`, in ra chain id theo từng RPC.

Ví dụ:

```json
{
	"rpc_urls": [
		"http://127.0.0.1:8757",
		"http://127.0.0.1:10747"
	],
	"type": "get_chain_id",
	"params": {}
}
```

## 3) Cách chạy

Chạy với config mặc định:

```bash
go run -tags tool base.go
```

Chạy với config chỉ định:

```bash
go run -tags tool base.go ./config-local.json
go run -tags tool base.go -config=./config-local-multi.json
```

Kết quả hiển thị:
- Cột RPC URL
- Cột Result:
	- Với `get_logs`: `raw_logs=... | filtered_logs=...`
	- Với `account_state`: `nonce=...`
	- Với `get_chain_id`: `chain_id=...`

## 4) Cách mở rộng type mới

Thêm nhanh method mới (ví dụ get block number):
- Tạo hàm mới theo form: `func runX(url string, cfg Config) (string, error)`
- Bên trong gọi `callRPC(url, cfg.TimeoutSeconds, "eth_blockNumber", []interface{}{})`
- Parse `result` và trả string output
- Đăng ký vào map `handlers` trong `main()`

Theo cách này bạn tái sử dụng được cho `getchainId` và các RPC khác mà không cần viết lại logic HTTP/JSON.
