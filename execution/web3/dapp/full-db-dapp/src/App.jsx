// src/App.jsx
import React, { useState } from 'react';
import {
  createWalletClient,
  custom,
  getAddress
} from 'viem';
// import { sepolia } from 'viem/chains'; // *** CHỌN CHAIN PHÙ HỢP VỚI CONTRACT VÀ VÍ ***
import { localhost } from 'viem/chains'; // Hoặc dùng localhost nếu test local
import FullDbInteractionPage from './FullDbInteractionPage'; // <-- Đổi tên component
import './App.css';
import './ConnectWalletButton.css'; // Sử dụng lại CSS nút kết nối

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
      if (typeof window.ethereum === 'undefined') {
        throw new Error('Vui lòng cài đặt ví Ethereum như MetaMask!');
      }
      const client = createWalletClient({
        // chain: sepolia, // *** Đảm bảo chain khớp ***
        chain: { ...localhost, id: 991 }, // Chỉ định chain
        transport: custom(window.ethereum),
      });
      const addresses = await client.requestAddresses();
      const userAddress = getAddress(addresses[0]);
      setAccount(userAddress);
      setWalletClient(client);
      setConnectError('');
      console.log("Ví đã kết nối:", userAddress);
    } catch (err) {
      console.error("Lỗi kết nối ví:", err);
      let message = 'Có lỗi xảy ra khi kết nối ví.';
      if (err.message.includes('User rejected the request')) {
        message = 'Bạn đã từ chối yêu cầu kết nối.';
      } else if (err.message.includes('Vui lòng cài đặt ví')) {
        message = err.message;
      } else if (err.message.includes("Already processing")) {
        message = "Yêu cầu kết nối đang được xử lý. Vui lòng kiểm tra ví.";
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
        <h1>Full DB Product Search DApp</h1> {/* <-- Đổi tiêu đề */}
      </header>
      <main>
        {!account ? (
          // --- Giao diện khi CHƯA kết nối ví ---
          <div className="wallet-connect-container section-box">
             <h2>Kết nối Ví</h2>
             <p>Vui lòng kết nối ví Ethereum của bạn để tìm kiếm sản phẩm.</p>
             <div className="wallet-container">
                <button
                    onClick={handleConnectWallet}
                    disabled={isConnecting}
                    className="w-full flex items-center justify-center bg-indigo-600 hover:bg-indigo-700 text-white connect-button"

                >
                    {isConnecting ? 'Đang kết nối...' : 'Kết nối Ví'}
                </button>
                {connectError && <p className="error-message" style={{marginTop: '15px'}}>{connectError}</p>}
             </div>
          </div>
        ) : (
          // --- Giao diện SAU KHI đã kết nối ví ---
          <FullDbInteractionPage // <-- Đổi tên component
            account={account}
            walletClient={walletClient}
            onDisconnect={handleDisconnectWallet}
          />
        )}
      </main>
       <footer>
          <p style={{ textAlign: 'center', marginTop: '30px', color: '#888', fontSize: '0.9em' }}>
             FullDB DApp Demo with Viem
          </p>
       </footer>
    </div>
  );
}

export default App;