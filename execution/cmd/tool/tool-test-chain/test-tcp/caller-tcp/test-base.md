# TCP Base Tool (base.go)

Tool nay dung chung de check nhieu node TCP trong mot lan chay.

Ho tro `type`:
- `get_logs`
- `account_state`
- `get_chain_id`

## 1) Chay tool

Mac dinh doc config tu:
- `./base/config-local-multi.json`

Lenh:

```bash
go run -tags tool base.go
```

Hoac chi dinh file config:

```bash
go run -tags tool base.go -config=./base/config-local-multi.json
```

## 2) Cau hinh mau

```json
{
	"private_key": "...",
	"version": "0.0.1.0",
	"parent_connection_address": "127.0.0.1:4201",
	"connection_addresses": [
		"127.0.0.1:4201",
		"127.0.0.1:6201"
	],
	"chain_id": 991,
	"nation_id": 1,
	"parent_connection_type": "client",
	"parent_address": "0x...",
	"type": "get_logs",
	"params": {
		"from_block": "83",
		"to_block": "83",
		"address": "0x00000000000000000000000000000000B429C0B2",
		"topics": [["0x...", "0x..."]]
	}
}
```

## 3) Y nghia output

- `get_logs`: tra ve ca 2 case
	- `raw_logs`: khong filter (chi from/to block)
	- `filtered_logs`: co filter theo `address/topics` neu co
- `account_state`: `nonce=...` theo address
- `get_chain_id`: `chain_id=...`

## 4) Params theo type

### get_logs
- Bat buoc: `from_block`
- Tuy chon: `to_block` (mac dinh = `from_block`)
- Tuy chon: `address` hoac `addresses`
- Tuy chon: `topics`

Luu y block:
- Truyen so thuong `83` hoac chuoi so `"83"` deu duoc, tool tu convert sang hex `0x53`.
- Cung ho tro `"0x53"`, `latest`, `pending`, `earliest`, `safe`, `finalized`.

### account_state
- Bat buoc: `address`

Vi du:

```json
{
	"type": "account_state",
	"params": {
		"address": "0x824fef8A3cE4b93C546209CC254D97E5Fee804e0"
	}
}
```

### get_chain_id

Vi du:

```json
{
	"type": "get_chain_id",
	"params": {}
}
```
