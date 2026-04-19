import React, { useState } from 'react';
import {
  createWalletClient,
  custom,
  getAddress
} from 'viem';
import { localhost } from 'viem/chains'; // *** CHỌN CHAIN PHÙ HỢP VỚI CONTRACT VÀ VÍ ***
import ContractInteractionPage from './ContractInteractionPage';
import './App.css'; // CSS chung của App
import './ConnectWalletButton.css'; // CSS cho nút kết nối ví (từ ví dụ trước)

function App() {
  // --- State Quản lý Ví ---
  const [account, setAccount] = useState(null);
  const [walletClient, setWalletClient] = useState(null);
  const [isConnecting, setIsConnecting] = useState(false);
  const [connectError, setConnectError] = useState('');

  // --- Hàm Kết nối Ví ---
  const handleConnectWallet = async () => {
    setIsConnecting(true);
    setConnectError('');
    try {
      // Kiểm tra provider (MetaMask)
      if (typeof window.ethereum === 'undefined') {
        throw new Error('Vui lòng cài đặt ví Ethereum như MetaMask!');
      }

      // Tạo Wallet Client
      const client = createWalletClient({
        chain: localhost, // *** Đảm bảo chain khớp ***
        transport: custom(window.ethereum),
      });

      // Yêu cầu kết nối tài khoản
      const addresses = await client.requestAddresses();
      const userAddress = getAddress(addresses[0]); // Lấy địa chỉ và chuẩn hóa

      // Cập nhật state
      setAccount(userAddress);
      setWalletClient(client);
      setConnectError(''); // Xóa lỗi nếu thành công

      console.log("Ví đã kết nối:", userAddress);

    } catch (err) {
      console.error("Lỗi kết nối ví:", err);
      // Xử lý các lỗi thường gặp
      let message = 'Có lỗi xảy ra khi kết nối ví.';
      if (err.message.includes('User rejected the request')) {
        message = 'Bạn đã từ chối yêu cầu kết nối.';
      } else if (err.message.includes('Vui lòng cài đặt ví')) {
        message = err.message;
      } else if (err.message.includes("Already processing eth_requestAccounts")) {
        message = "Yêu cầu kết nối đang được xử lý. Vui lòng kiểm tra ví của bạn.";
      }
      setConnectError(message);
      setAccount(null);
      setWalletClient(null);
    } finally {
      setIsConnecting(false);
    }
  };

  // --- Hàm Ngắt Kết nối Ví ---
  const handleDisconnectWallet = () => {
    setAccount(null);
    setWalletClient(null);
    setConnectError('');
    console.log("Đã ngắt kết nối ví.");
  };

  return (
    <div className="App">
      <header className="App-header">
        <h1>Simple DB DApp</h1>
      </header>
      <main>
        {/* --- Hiển thị có điều kiện dựa trên state 'account' --- */}
        {!account ? (
          // --- Giao diện khi CHƯA kết nối ví ---
          <div className="wallet-connect-container section-box"> {/* Thêm class section-box nếu CSS có định nghĩa */}
             <h2>Kết nối Ví</h2>
             <p>Vui lòng kết nối ví Ethereum của bạn để tương tác với Contract.</p>
             {/* Sử dụng div và class từ ConnectWalletButton.css */}
             <div className="wallet-container">
                <button
                    onClick={handleConnectWallet}
                    disabled={isConnecting}
                    className="connect-button" // Class từ ConnectWalletButton.css
                >
                    {isConnecting ? 'Đang kết nối...' : 'Kết nối Ví'}
                </button>
                {/* Hiển thị lỗi kết nối nếu có */}
                {connectError && <p className="error-message" style={{marginTop: '15px'}}>{connectError}</p>}
             </div>
          </div>

        ) : (
          // --- Giao diện SAU KHI đã kết nối ví ---
          // Render trang tương tác và truyền props cần thiết xuống
          <ContractInteractionPage
            account={account}
            walletClient={walletClient}
            onDisconnect={handleDisconnectWallet} // Hàm để component con gọi khi muốn ngắt kết nối
          />
        )}
      </main>
       <footer>
          <p style={{ textAlign: 'center', marginTop: '30px', color: '#888', fontSize: '0.9em' }}>
             Simple DApp Demo with Viem
          </p>
       </footer>
    </div>
  );
}

export default App;