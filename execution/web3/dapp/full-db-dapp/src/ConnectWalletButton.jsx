import React, { useState, useEffect } from 'react';
import { createWalletClient, custom, getAddress } from 'viem';
import { mainnet } from 'viem/chains'; // Hoặc chain bạn muốn dùng (e.g., sepolia)
import './ConnectWalletButton.css'; // File CSS để làm đẹp

function ConnectWalletButton() {
  const [account, setAccount] = useState(null); // State để lưu địa chỉ ví đã kết nối
  const [isLoading, setIsLoading] = useState(false); // State để quản lý trạng thái loading
  const [error, setError] = useState(''); // State để lưu lỗi (nếu có)

  // Hàm xử lý kết nối ví
  const connectWallet = async () => {
    setIsLoading(true);
    setError(''); // Xóa lỗi cũ
    try {
      // Kiểm tra xem trình duyệt có hỗ trợ Ethereum provider (MetaMask) không
      if (typeof window.ethereum === 'undefined') {
        throw new Error('Vui lòng cài đặt ví Ethereum như MetaMask!');
      }

      // Tạo một Wallet Client bằng viem
      const client = createWalletClient({
        chain: { ...localhost, id: 991 }, // Chỉ định chain
        transport: custom(window.ethereum), // Sử dụng provider từ ví trình duyệt
      });

      // Yêu cầu quyền truy cập vào tài khoản của người dùng
      const accounts = await client.requestAddresses();

      // Lấy địa chỉ đầu tiên và chuẩn hóa nó (checksum)
      const userAddress = getAddress(accounts[0]);
      setAccount(userAddress);

    } catch (err) {
      console.error("Lỗi kết nối ví:", err);
      // Xử lý các lỗi thường gặp
      if (err.message.includes('User rejected the request')) {
        setError('Bạn đã từ chối yêu cầu kết nối.');
      } else if (err.message.includes('Vui lòng cài đặt ví')) {
        setError(err.message);
      }
       else {
        setError('Có lỗi xảy ra khi kết nối ví.');
      }
      setAccount(null); // Đảm bảo không hiển thị địa chỉ nếu có lỗi
    } finally {
      setIsLoading(false); // Kết thúc loading dù thành công hay thất bại
    }
  };

  // Hàm ngắt kết nối (đơn giản là xóa state)
  const disconnectWallet = () => {
    setAccount(null);
    setError('');
  };

  // Tự động thử kết nối lại nếu đã cấp quyền trước đó (tùy chọn)
  // useEffect(() => {
  //   const tryAutoConnect = async () => {
  //     if (typeof window.ethereum !== 'undefined') {
  //       try {
  //         const accounts = await window.ethereum.request({ method: 'eth_accounts' });
  //         if (accounts.length > 0) {
  //           setAccount(getAddress(accounts[0]));
  //         }
  //       } catch (err) {
  //         console.error("Lỗi tự động kết nối:", err);
  //       }
  //     }
  //   };
  //   // tryAutoConnect(); // Bỏ comment dòng này nếu muốn tự động kết nối
  // }, []);

  return (
    <div className="wallet-container">
      {account ? (
        // Hiển thị khi đã kết nối
        <div className="wallet-info">
          <p>Đã kết nối:</p>
          {/* Hiển thị vài ký tự đầu và cuối của địa chỉ */}
          <span className="wallet-address">
            {`${account.substring(0, 6)}...${account.substring(account.length - 4)}`}
          </span>
          <button onClick={disconnectWallet} className="disconnect-button">
            Ngắt kết nối
          </button>
        </div>
      ) : (
        // Hiển thị khi chưa kết nối
        <button
          onClick={connectWallet}
          disabled={isLoading} // Vô hiệu hóa nút khi đang loading
          className="connect-button"
        >
          {isLoading ? 'Đang kết nối...' : 'Kết nối Ví'}
        </button>
      )}
      {/* Hiển thị thông báo lỗi */}
      {error && <p className="error-message">{error}</p>}
    </div>
  );
}

export default ConnectWalletButton;