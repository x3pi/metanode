# Hướng dẫn chạy ví dụ SimpleDB DApp

Tài liệu này hướng dẫn cách thiết lập và chạy ứng dụng phi tập trung (DApp) ví dụ tương tác với smart contract `FullDB.sol`.

## Yêu cầu hệ thống

* Node.js (phiên bản LTS khuyến nghị)
* Yarn (hoặc npm...) 
* Một công cụ/môi trường để triển khai smart contract Solidity (ví dụ: Remix IDE, Hardhat, Truffle)
* Một ví Web3 trên trình duyệt (ví dụ: MetaMask) được cấu hình với mạng lưới mà bạn sẽ triển khai contract (ví dụ: Sepolia, Goerli, hoặc mạng local).

## Các bước cài đặt và chạy

### 1. Triển khai Smart Contract

1.  **Biên dịch & Triển khai:** Sử dụng công cụ của bạn (Remix, Hardhat, v.v.) để biên dịch để biên dịch mà không lỗi `deep stack` cần setup `"viaIR": true` và triển khai tệp contract tại đường dẫn: 
    ```
    web3/dapp/full-db-dapp/contracs/FullDB.sol
    ```
    Hãy chắc chắn bạn triển khai nó lên mạng lưới blockchain mong muốn (local, testnet, mainnet).
2.  **Lấy địa chỉ Contract:** Sau khi triển khai thành công, bạn sẽ nhận được một địa chỉ contract duy nhất.
    **Quan trọng:** **Sao chép (copy)** địa chỉ contract này. Bạn sẽ cần nó ở bước tiếp theo.

### 2. Cấu hình ứng dụng Web

1.  **Mở tệp:** Điều hướng đến thư mục dự án và mở tệp sau bằng trình soạn thảo code:
    ```
    web3/dapp/full-db-dapp/src/FullDbInteractionPage.jsx
    ```
2.  **Cập nhật địa chỉ:** Tìm dòng code khai báo hằng số `contractAddress`.
    ```javascript
    const contractAddress = '0xbaA3491c4Ae81e9a0dd6b0dAF9359C2d5a5d2ffb'; // Địa chỉ ví dụ
    ```
3.  **Thay thế địa chỉ:** Xóa địa chỉ ví dụ và **dán địa chỉ contract** bạn đã sao chép ở **Bước 1** vào giữa dấu nháy đơn (`''`). Ví dụ:
    ```javascript
    // Thay thế địa chỉ này bằng địa chỉ contract của bạn đã triển khai
    const contractAddress = 'ĐỊA_CHỈ_CONTRACT_CỦA_BẠN';
    ```
4.  **Lưu tệp:** Lưu lại các thay đổi bạn vừa thực hiện trong tệp `ContractInteractionPage.jsx`.

### 3. Khởi chạy Web App

1.  **Mở Terminal:** Mở cửa sổ dòng lệnh (terminal, command prompt, PowerShell) và điều hướng (`cd`) đến thư mục gốc của dự án web app (thư mục chứa tệp `package.json`).
2.  **Cài đặt Dependencies:** Nếu đây là lần đầu bạn chạy dự án hoặc có cập nhật thư viện, hãy chạy lệnh sau để cài đặt các gói cần thiết:
    ```bash
    yarn install
    ```
3.  **Khởi động Server:** Chạy lệnh sau để khởi động server phát triển cho ứng dụng web:
    ```bash
    yarn dev
    ```

## Truy cập ứng dụng

Sau khi chạy `yarn dev` thành công, ứng dụng web sẽ có sẵn tại địa chỉ mặc định trên trình duyệt của bạn:

`http://localhost:5173/`

Mở trình duyệt và truy cập vào địa chỉ này.

## Hướng dẫn sử dụng cơ bản

1.  **Kết nối Ví:** Nhấn nút "Kết nối Ví" và chấp nhận yêu cầu từ ví của bạn.
2.  **Quản lý DB:**
    * Nhập tên cho database (ví dụ: `products`) vào ô "Tên DB".
    * Nhấn "Tạo / Chọn DB". Ứng dụng sẽ tương tác với contract để tạo DB nếu chưa có hoặc chọn nếu đã tồn tại. Tên DB hiện tại sẽ được hiển thị.
    * (Tùy chọn) Nhấn "Tạo DB Mẫu" để contract tự động thêm một số sản phẩm mẫu vào DB bạn vừa tạo/chọn.
3.  **Thêm Sản phẩm:** Điền thông tin vào form "Thêm Sản phẩm Mới" và nhấn nút "Thêm Sản phẩm".
4.  **Tìm kiếm:**
    * Nhập từ khóa, tiền tố (ví dụ: `T:áo thun`, `B:apple`), hoặc để trống để tìm tất cả.
    * Đặt bộ lọc giá gốc hoặc giá khuyến mãi (nếu cần).
    * Chọn cách sắp xếp.
    * Nhấn nút "Tìm kiếm".
5.  **Xem Kết quả:** Kết quả sẽ hiển thị bên dưới. Bạn có thể xem chi tiết sản phẩm, rank, score.
6.  **Phân trang:** Sử dụng các nút "< Trước" và "Sau >" để xem các trang kết quả khác (nếu có).
7.  **Xóa Sản phẩm:** Nhấn nút "Xóa" bên cạnh sản phẩm bạn muốn loại bỏ và xác nhận.
8.  **Theo dõi Trạng thái:** Các thông báo về trạng thái (loading, lỗi, thành công) và link giao dịch sẽ hiển thị khi bạn thực hiện các hành động ghi (thêm, xóa, tìm kiếm...).

## Chú ý:

```
   prefixMap: [ // Default prefix map (adjust if your fields/prefixes differ)
        { key: "title", value: "T" }, { key: "T", value: "T" },
        { key: "category", value: "C" }, { key: "C", value: "C" },
        { key: "brand", value: "B" }, { key: "B", value: "B" },
        { key: "color", value: "CO:" }, { key: "CO", value: "CO:" },
        { key: "filter", value: "F:" }, { key: "F", value: "F:" }
      ],
```
prefixMap dùng để nhập dữ liệu vào mục tìm kiếm ánh xa tới tiền tố cần tìm:
- ví dụ tìm kiếm màu gold thì nhập `CO:gold` hoặc nhập `color:gold` do lúc thêm dữ liệu vào hàm `addTermDocument` có nỗi chuỗi `CO` + `:` + `gold` nên value trong map là `CO:`. prefixMap khi nhập `CO` hoặc `color:` là đang ánh xạ tới tiền tố  `CO:`
- Tương tự `T:pro` hoặc `title:pro` là tìm kiếm cho `Tpro` là title có từ `pro`
