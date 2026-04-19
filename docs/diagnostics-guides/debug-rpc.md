# Debug RPC Methods

## `debug_getTransactionError`

Lấy dữ liệu giao dịch gốc của một giao dịch đã gây ra lỗi, dựa trên hash của nó.

*   **Parameters:**
    1.  `common.Hash`: Hash của giao dịch cần lấy thông tin. Ví dụ: `"0x123..."`
*   **Returns:**
    *   `Object | null`: Đối tượng giao dịch (`types.Transaction`) hoặc `null` nếu có lỗi.
*   **Ví dụ Curl:**

```bash
curl -X POST --data '{
  "jsonrpc":"2.0",
  "method":"debug_getTransactionError",
  "params":["0x...transactionHash..."],
  "id":1
}' -H "Content-Type: application/json" http://localhost:8545
```

---

## `debug_getRCPTransactionError`

Lấy dữ liệu biên nhận (receipt) của một giao dịch đã gây ra lỗi, dựa trên hash của nó.

*   **Parameters:**
    1.  `common.Hash`: Hash của giao dịch cần lấy biên nhận. Ví dụ: `"0xabc..."`
*   **Returns:**
    *   `Object | null`: Đối tượng biên nhận (`types.Receipt`) hoặc `null` nếu có lỗi.
*   **Ví dụ Curl:**

```bash
curl -X POST --data '{
  "jsonrpc":"2.0",
  "method":"debug_getRCPTransactionError",
  "params":["0x...transactionHash..."],
  "id":1
}' -H "Content-Type: application/json" http://localhost:8545
```

---

## `debug_traceTransaction`

Thực thi lại một giao dịch ở chế độ debug để theo dõi (trace) quá trình thực thi của nó, dựa trên hash giao dịch (hỗ trợ cả hash Meta Node và hash Ethereum nếu có mapping).

*   **Parameters:**
    1.  `common.Hash`: Hash của giao dịch cần trace (có thể là hash Ethereum). Ví dụ: `"0xdef..."`
*   **Returns:**
    *   `Object | null`: Kết quả thực thi và trace của smart contract (`types.ExecuteSCResult`) hoặc `null` nếu có lỗi. Kết quả này chứa thông tin chi tiết về các bước thực thi, gas sử dụng, và các sự kiện được ghi lại bởi hệ thống trace.
*   **Ví dụ Curl:**

```bash
curl -X POST --data '{
  "jsonrpc":"2.0",
  "method":"debug_traceTransaction",
  "params":["0x...transactionHash..."],
  "id":1
}' -H "Content-Type: application/json" http://localhost:8545
```

---

## `debug_traceBlock`

Thực thi lại tất cả các giao dịch trong một block cụ thể ở chế độ debug và trả về các span trace đã thu thập được trong quá trình xử lý block đó.

*   **Parameters:**
    1.  `QUANTITY | TAG`: Số hiệu của block cần trace dưới dạng số nguyên (ví dụ: `467`). Không hỗ trợ các tag như `"latest"`.
*   **Returns:**
    *   `Array | null`: Một mảng các đối tượng span trace (`[]*trace.Span`) được thu thập trong quá trình thực thi lại các giao dịch của block. Mỗi span chứa thông tin về tên, thời gian, thuộc tính, sự kiện, và mối quan hệ cha-con. Trả về `null` nếu có lỗi (ví dụ: block không tồn tại, lỗi trong quá trình trace).
*   **Ví dụ Curl:**

```bash
# Trace block số 467 (dạng số nguyên)
curl -X POST --data '{
  "jsonrpc":"2.0",
  "method":"debug_traceBlock",
  "params":[1],
  "id":1
}' -H "Content-Type: application/json" http://localhost:8545

---