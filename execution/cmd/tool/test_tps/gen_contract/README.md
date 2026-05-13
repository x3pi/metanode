# Contract TPS Tool Guide

## Muc tieu
Tool nay ho tro 2 buoc test:

1. Deploy contract hang loat bang danh sach private key trong `generated_keys.json`.
2. Goi ham `setValue` song song theo so luong contract ban truyen vao.

Tool dam bao danh sach contract duoc loc unique theo `contract_address` de tranh trung contract khi chay `setValue`.

## File dung
- Tool: `main.go`
- ABI: `abi.json`
- Bytecode va chain config: `config.json`
- Keys input mac dinh: `../gen_spam_keys/generated_keys.json`
- Deploy output mac dinh: `deployed_contracts.json`
- SetValue output mac dinh: `setvalue_results.json`

## Yeu cau
- Dang o thu muc `execution/cmd/tool/test_tps/contract`.
- Node RPC dang chay va truy cap duoc.
- Keys trong `generated_keys.json` da duoc cap balance de tra phi gas.

## Cach tool tu dong chon tham so
- `rpc` lay tu `config.json`.
- `chain_id` lay tu `config.json`.
- ABI mac dinh lay tu `./abi.json`.
- Gas price tu dong lay bang `eth_gasPrice`.
- Gas limit tu dong `estimateGas`, co fallback an toan neu RPC khong ho tro.

## Chay nhanh
Di chuyen vao thu muc tool:

```bash
cd /home/abc/nhat/consensus-chain/metanode/execution/cmd/tool/test_tps/contract
```

Xem help:

```bash
go run . -h
```

## 1) Deploy contract hang loat
Lenh mau:

```bash
go run . \
  -mode deploy \
  -wallet-count 200 \
  -workers 50 \
  -keys ../gen_spam_keys/generated_keys.json \
  -bytecode-config ./config.json \
  -deployed-file ./deployed_contracts.json
```

Y nghia:
- `wallet-count`: so vi su dung de deploy. Moi vi gui 1 deploy tx.
- `workers`: so luong goroutine gui tx song song.
- `deployed-file`: noi luu danh sach contract da deploy, dung cho test `setValue`.

Neu muon doi receipt de lay `contractAddress` tu receipt:

```bash
go run . \
  -mode deploy \
  -wallet-count 200 \
  -workers 50 \
  -wait-receipt \
  -receipt-timeout 120
```

## 2) Goi `setValue` song song theo so contract
Lenh mau:

```bash
go run . \
  -mode setvalue \
  -contract-count 200 \
  -workers 50 \
  -set-start 1 \
  -deployed-file ./deployed_contracts.json \
  -setvalue-result ./setvalue_results.json
```

Y nghia:
- `contract-count`: so contract se duoc chon de goi `setValue`.
- Tool se lay toi da `contract-count` contract unique tu `deployed-file`.
- Moi contract duoc goi 1 lan trong 1 lan chay `setValue`.
- Tool quan ly nonce theo tung signer de tranh dung nonce khi chay song song.

Gia tri `setValue`:
- Contract thu i se nhan gia tri `set-start + i`.
- Vi du `set-start=1` thi cac contract nhan `1, 2, 3, ...`.

## Dinh dang file output
### `deployed_contracts.json`
Moi phan tu co:
- `contract_address`
- `deployer_address`
- `key_index`
- `deploy_tx_hash`
- `deploy_nonce`
- `deployed_at`

File nay duoc merge voi du lieu cu va loc unique theo `contract_address`.

### `setvalue_results.json`
Moi phan tu co:
- `contract_address`
- `caller_address`
- `value`
- `nonce`
- `tx_hash`
- `sent_at`

## Toan bo tham so con lai
- `mode`: `deploy` hoac `setvalue`.
- `config`: file JSON chua `rpc` va `chain_id`.
- `keys`: file JSON chua `index`, `private_key`, `address`.
- `bytecode-config`: file JSON chua bytecode hoac `bytescode`.
- `deployed-file`: file luu contract da deploy.
- `setvalue-result`: file luu ket qua goi `setValue`.
- `wallet-count`: so vi dung trong deploy.
- `contract-count`: so contract dung trong `setvalue`.
- `workers`: muc do song song.
- `set-start`: gia tri bat dau cho `setValue`.
- `wait-receipt`: doi receipt trong mode deploy va setvalue. Mac dinh la `true`.
- `receipt-timeout`: timeout doi receipt theo giay.

## Quy trinh de xuat cho test TPS
1. Chay deploy voi `wallet-count` bang so contract muon tao.
2. Kiem tra `deployed_contracts.json` co du so luong contract.
3. Chay `setvalue` voi `contract-count` mong muon.
4. Theo doi log `setvalue-ok` va file `setvalue_results.json`.

## Loi thuong gap va cach xu ly
1. `all deploy tx failed`
- Kiem tra `config.json` co dung `rpc` va `chain_id`.
- Kiem tra key co balance.
- Tang gas limit neu contract lon, hoac bat `-wait-receipt` neu can.

2. `method setValue not found in ABI`
- Kiem tra `abi.json` dung contract.
- Dam bao dung ten method `setValue`.

3. `cannot map key for deployer`
- `deployed_contracts.json` khong dong bo voi keys file.
- Dung lai cung `generated_keys.json` da deploy truoc do.

4. `send failed ... invalid nonce`
- Co the dang co tx pending tu truoc.
- Giam `workers` hoac cho pending tx duoc mine.
- Dam bao mot signer khong bi tool khac gui tx cung luc.

## Ghi chu van hanh
- Khi deploy khong `wait-receipt`, `contract_address` duoc tinh theo `CreateAddress(from, nonce)`.
- Khi bat `wait-receipt`, tool uu tien `contractAddress` trong receipt.
- Neu chay lai deploy nhieu lan, file deployed se duoc merge va loc unique.

## Lenh mau full pipeline
Deploy 300 contract roi `setValue` 300 contract:

```bash
go run . -mode deploy -wallet-count 300 -workers 60
go run . -mode setvalue -contract-count 300 -workers 60 -set-start 1
```
