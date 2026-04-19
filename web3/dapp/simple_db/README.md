# Hướng dẫn chạy ví dụ SimpleDB DApp

Tài liệu này hướng dẫn cách thiết lập và chạy ứng dụng phi tập trung (DApp) ví dụ tương tác với smart contract `SimpleDB.sol`.

## Yêu cầu hệ thống

* Node.js (phiên bản LTS khuyến nghị)
* Yarn (hoặc npm...) 
* Một công cụ/môi trường để triển khai smart contract Solidity (ví dụ: Remix IDE, Hardhat, Truffle)
* Một ví Web3 trên trình duyệt (ví dụ: MetaMask) được cấu hình với mạng lưới mà bạn sẽ triển khai contract (ví dụ: Sepolia, Goerli, hoặc mạng local).

## Các bước cài đặt và chạy

### 1. Triển khai Smart Contract

1.  **Biên dịch & Triển khai:** Sử dụng công cụ của bạn (Remix, Hardhat, v.v.) để biên dịch và triển khai tệp contract tại đường dẫn:
    ```
    web3/dapp/simple_db/src/contracts/SimpleDB.sol
    ```
    Hãy chắc chắn bạn triển khai nó lên mạng lưới blockchain mong muốn (local, testnet, mainnet).
2.  **Lấy địa chỉ Contract:** Sau khi triển khai thành công, bạn sẽ nhận được một địa chỉ contract duy nhất.
    **Quan trọng:** **Sao chép (copy)** địa chỉ contract này. Bạn sẽ cần nó ở bước tiếp theo.

### 2. Cấu hình ứng dụng Web

1.  **Mở tệp:** Điều hướng đến thư mục dự án và mở tệp sau bằng trình soạn thảo code:
    ```
    web3/dapp/simple_db/src/ContractInteractionPage.jsx
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

1.  **Kết nối Ví:** Khi truy cập ứng dụng lần đầu, bạn cần **kết nối ví Web3** của mình (ví dụ: MetaMask) bằng cách nhấp vào nút kết nối tương ứng trên giao diện. Đảm bảo ví của bạn đang kết nối với cùng mạng lưới mà bạn đã triển khai contract.
2.  **Nhập tên & Tương tác DB:**
    * Nhập một tên định danh (ví dụ: username) vào ô input.
    * Sử dụng nút **"Get"** để kiểm tra xem đã có dữ liệu nào được lưu trữ cho tên và địa chỉ ví của bạn chưa.
    * Sử dụng nút **"Create DB"** (hoặc tương tự) để tạo một bản ghi mới liên kết với tên và địa chỉ ví của bạn nếu chưa có.
3.  **Sử dụng các chức năng:** Sau khi đã kết nối ví và có một "DB" (bản ghi) được liên kết, bạn có thể bắt đầu sử dụng các nút chức năng khác trên giao diện để tương tác với các hàm của smart contract `SimpleDB` (ví dụ: set giá trị, get giá trị, v.v.).