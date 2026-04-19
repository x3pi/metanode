import React, { useState, useEffect } from 'react';
import {
  createPublicClient,
  createWalletClient,
  custom,
  http,
  getAddress,
  parseAbi, // Dùng để parse ABI dạng string hoặc array
  ContractFunctionExecutionError // Để bắt lỗi contract cụ thể
} from 'viem';
import { localhost } from 'viem/chains'; // *** Chọn chain phù hợp với contract của bạn ***
import './ContractInteractionPage.css'; // File CSS
import contractABI_JSON from './abis/PublicSimpleDB.json'; // <-- Import file JSON

// --- CẤU HÌNH ---
// !!! THAY THẾ BẰNG ĐỊA CHỈ CONTRACT CỦA BẠN SAU KHI DEPLOY !!!
const contractAddress = '0xbaA3491c4Ae81e9a0dd6b0dAF9359C2d5a5d2ffb'; // <-- THAY ĐỊA CHỈ CONTRACT Ở ĐÂY
const contractABI = contractABI_JSON; // <-- Sử dụng ABI từ file JSON đã import

// Cấu hình Public Client (để đọc dữ liệu và chờ transaction)
const publicClient = createPublicClient({
  chain: { ...localhost, id: 991 }, // *** Đảm bảo chain này khớp với contract ***
  transport:  custom(window.ethereum), // Có thể dùng http(RPC_URL) nếu muốn
});

function ContractInteractionPage() {
  // --- State Quản lý Ví ---
  const [account, setAccount] = useState(null);
  const [walletClient, setWalletClient] = useState(null);
  const [isConnecting, setIsConnecting] = useState(false);
  const [connectError, setConnectError] = useState('');

  // --- State Tương tác Contract ---
  const [dbNameInput, setDbNameInput] = useState('');
  const [keyInput, setKeyInput] = useState('');
  const [valueInput, setValueInput] = useState('');
  const [searchInput, setSearchInput] = useState('');
  const [limitInput, setLimitInput] = useState('10'); // Mặc định limit là 10

  // --- State Hiển thị Kết quả từ Contract ---
  const [currentDbName, setCurrentDbName] = useState('');
  const [resultValue, setResultValue] = useState('');
  const [resultStatus, setResultStatus] = useState(null); // null, true, false
  const [isLoading, setIsLoading] = useState(false);
  const [txHash, setTxHash] = useState('');
  const [error, setError] = useState('');

  // --- Kết nối Ví ---
  const connectWallet = async () => {
    setIsConnecting(true);
    setConnectError('');
    try {
      if (typeof window.ethereum === 'undefined') {
        throw new Error('Vui lòng cài đặt ví Ethereum như MetaMask!');
      }
      const client = createWalletClient({
        chain: { ...localhost, id: 991 }, // *** Đảm bảo chain khớp ***
        transport: custom(window.ethereum),
      });
      const addresses = await client.requestAddresses();
      const userAddress = getAddress(addresses[0]);
      setAccount(userAddress);
      setWalletClient(client); // Lưu lại wallet client để dùng cho việc ghi contract
    } catch (err) {
      console.error("Lỗi kết nối ví:", err);
      setConnectError(err.message || 'Có lỗi xảy ra khi kết nối ví.');
      setAccount(null);
      setWalletClient(null);
    } finally {
      setIsConnecting(false);
    }
  };

  const disconnectWallet = () => {
    setAccount(null);
    setWalletClient(null);
    setCurrentDbName(''); // Reset luôn trạng thái contract khi ngắt kết nối
    setResultValue('');
    setResultStatus(null);
    setTxHash('');
    setError('');
  };

  // --- Hàm đọc trạng thái contract sau khi transaction thành công ---
  const readContractState = async () => {
    if (!contractAddress || contractAddress === '0x...') {
       console.warn("Địa chỉ contract chưa được cấu hình.");
       return; // Không đọc nếu chưa có địa chỉ contract
    }
    try {
      console.log("Đang đọc trạng thái contract...");
      const [fetchedDbName, fetchedStatus, fetchedStoredValue] = await Promise.all([
        publicClient.readContract({
          address: contractAddress,
          abi: contractABI,
          functionName: 'dbName',
        }),
        publicClient.readContract({
          address: contractAddress,
          abi: contractABI,
          functionName: 'status',
        }),
        publicClient.readContract({
          address: contractAddress,
          abi: contractABI,
          functionName: 'storedValue',
        }),
      ]);
      console.log("Kết quả đọc:", { fetchedDbName, fetchedStatus, fetchedStoredValue });
      setCurrentDbName(fetchedDbName);
      setResultStatus(fetchedStatus);
      setResultValue(fetchedStoredValue);
    } catch (readError) {
      console.error("Lỗi đọc trạng thái contract:", readError);
      setError("Không thể đọc trạng thái mới nhất từ contract.");
      // Giữ lại trạng thái cũ nếu đọc lỗi
    }
  }

  // --- Hàm Chung để Gửi Transaction và Xử lý Kết quả ---
  const executeContractWrite = async (functionName, args) => {
    if (!walletClient || !account) {
      setError("Vui lòng kết nối ví trước.");
      return;
    }
     if (!contractAddress || contractAddress === '0x...') {
      setError("Địa chỉ contract chưa được cấu hình. Vui lòng cập nhật trong code.");
      return;
    }

    setIsLoading(true);
    setError('');
    setTxHash('');
    // Reset kết quả tạm thời để user biết đang có thay đổi
    // setResultValue('');
    // setResultStatus(null);

    try {
      console.log(`Đang gọi ${functionName} với args:`, args);
      const hash = await walletClient.writeContract({
        address: contractAddress,
        abi: contractABI,
        functionName: functionName,
        args: args,
        account: account, // Đảm bảo truyền account đã kết nối
      });
      console.log("Transaction hash:", hash);
      setTxHash(hash);
      setError(`Đang chờ xác nhận transaction: ${hash}...`); // Thông báo chờ

      // Chờ transaction được xác nhận
      const receipt = await publicClient.waitForTransactionReceipt({ hash });
      console.log("Transaction receipt:", receipt);

      if (receipt.status === 'success') {
        setError(''); // Xóa thông báo chờ
        // Đọc lại trạng thái public variables từ contract sau khi thành công
        await readContractState();
        // Nếu là hàm tạo DB thành công, cập nhật luôn input
        if (functionName === 'getOrCreateSimpleDb') {
             // Không cần làm gì thêm vì readContractState đã cập nhật currentDbName
        }
      } else {
        throw new Error(`Transaction thất bại. Status: ${receipt.status}`);
      }
    } catch (err) {
        console.error(`Lỗi khi gọi hàm ${functionName}:`, err);
        let userMessage = `Lỗi khi thực hiện ${functionName}.`;
        if (err instanceof ContractFunctionExecutionError) {
            // Lỗi revert từ contract
            userMessage += ` Lỗi Contract: ${err.shortMessage || err.message}`;
        } else if (err.message?.includes('User rejected the request')) {
            userMessage = 'Người dùng đã từ chối giao dịch.';
        } else if (err.message?.includes('insufficient funds')) {
            userMessage = 'Không đủ gas để thực hiện giao dịch.';
        } else {
            userMessage += ` Chi tiết: ${err.message}`;
        }
        setError(userMessage);
        // Reset kết quả nếu lỗi
        setResultValue('');
        setResultStatus(null);
    } finally {
      setIsLoading(false);
    }
  };

  // --- Hàm Xử lý cho từng Nút Bấm ---
  const handleGetOrCreateDb = () => {
    if (!dbNameInput) {setError("Vui lòng nhập tên DB."); return;}
    executeContractWrite('getOrCreateSimpleDb', [dbNameInput]);
  };

  const handleDeleteDb = () => {
    if (!dbNameInput) {setError("Vui lòng nhập tên DB để xóa."); return;}
    executeContractWrite('deleteDb', [dbNameInput]);
  };

  const handleSet = () => {
    if (!keyInput || !valueInput) {setError("Vui lòng nhập Key và Value."); return;}
    executeContractWrite('set', [keyInput, valueInput]);
  };

  const handleGet = () => {
    if (!keyInput) {setError("Vui lòng nhập Key."); return;}
    executeContractWrite('get', [keyInput]);
  };

  const handleGetAll = () => {
    executeContractWrite('getAll', []);
  };

  const handleSearchByValue = () => {
     if (!searchInput) {setError("Vui lòng nhập giá trị cần tìm."); return;}
    executeContractWrite('searchByValue', [searchInput]);
  };

  const handleGetNextKeys = () => {
    if (!keyInput) {setError("Vui lòng nhập Key bắt đầu."); return;}
    const limit = parseInt(limitInput, 10);
    if (isNaN(limit) || limit <= 0 || limit > 255) {setError("Limit phải là số từ 1 đến 255."); return;}
    executeContractWrite('getNextKeys', [keyInput, limit]);
  };

   const handleRefresh = () => {
    executeContractWrite('refresh', []);
  };

  // Tự động đọc trạng thái contract ban đầu nếu đã kết nối ví và có địa chỉ contract
  useEffect(() => {
    if (account && contractAddress && contractAddress !== '0x...') {
      readContractState();
    }
     // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [account]); // Chỉ chạy khi account thay đổi (kết nối/ngắt kết nối)

  // --- Render UI ---
  return (
    <div className="contract-page">
      <h2>Tương tác với PublicSimpleDB</h2>

      {/* Phần Kết nối Ví */}
      <div className="wallet-section section-box">
        <h3>Trạng thái Ví</h3>
        {account ? (
          <div className="wallet-info">
            <p>Đã kết nối: <span className="address">{`${account.substring(0, 6)}...${account.substring(account.length - 4)}`}</span></p>
            <button onClick={disconnectWallet} className="btn btn-disconnect">Ngắt kết nối</button>
          </div>
        ) : (
          <>
            <button onClick={connectWallet} disabled={isConnecting} className="btn btn-connect">
              {isConnecting ? 'Đang kết nối...' : 'Ví đã kết nối click để tương tác contact'}
            </button>
            {connectError && <p className="error-message">{connectError}</p>}
          </>
        )}
        {!contractAddress || contractAddress === '0x...' &&
           <p className="warning-message">Lưu ý: Địa chỉ contract chưa được cấu hình trong code.</p>
        }
      </div>

      {/* Chỉ hiển thị phần tương tác khi đã kết nối ví */}
      {account && contractAddress && contractAddress !== '0x...' && (
        <>
          {/* Phần Kết quả Contract */}
          <div className="result-section section-box">
            <h3>Trạng thái Contract</h3>
            <p>DB Hiện tại (trên contract): <span className="result-data">{currentDbName || 'Chưa đặt'}</span></p>
            <p>Kết quả (storedValue):</p>
            <pre className="result-data result-multiline">{resultValue !== null && resultValue !== undefined ? String(resultValue) : '(chưa có)'}</pre>
            <p>Trạng thái (status): <span className="result-data">{resultStatus === null ? '(chưa có)' : String(resultStatus)}</span></p>
            {txHash && <p>Hash giao dịch cuối: <span className="tx-hash">{txHash.substring(0,10)}...</span></p>}
             <button onClick={handleRefresh} disabled={isLoading} className="btn btn-refresh">
               Làm mới State Contract (Gọi hàm refresh)
            </button>
             <button onClick={readContractState} disabled={isLoading} className="btn" style={{marginLeft: '10px'}}>
              Cập nhật State từ Contract (Chỉ đọc)
            </button>
          </div>

          {/* Phần Controls */}
          <div className="controls-section section-box">
            <h3>Thao tác với Contract</h3>

            {/* DB Operations */}
            <div className="control-group">
              <label htmlFor="dbName">Tên DB:</label>
              <input
                type="text"
                id="dbName"
                value={dbNameInput}
                onChange={(e) => setDbNameInput(e.target.value)}
                placeholder="Nhập tên DB"
                disabled={isLoading}
              />
              <button onClick={handleGetOrCreateDb} disabled={isLoading || !dbNameInput} className="btn">Tạo / Chọn DB</button>
              <button onClick={handleDeleteDb} disabled={isLoading || !dbNameInput} className="btn btn-danger">Xóa DB</button>
            </div>

             {/* Key-Value Operations (chỉ hiện khi có currentDbName) */}
             {currentDbName && (
                <>
                  <h4>Lưu ý: nếu set thì nhập cả key và value còn get thì giá trị value sẽ được bỏ qua</h4>
                 <div className="control-group">
                    <label htmlFor="key">Key:</label>
                    <input
                        type="text"
                        id="key"
                        value={keyInput}
                        onChange={(e) => setKeyInput(e.target.value)}
                        placeholder="Nhập key"
                        disabled={isLoading}
                    />
                     <button onClick={handleGet} disabled={isLoading || !keyInput} className="btn">Get Value</button>
                </div>
                <div className="control-group">
                    <label htmlFor="value">Value:</label>
                    <input
                        type="text"
                        id="value"
                        value={valueInput}
                        onChange={(e) => setValueInput(e.target.value)}
                        placeholder="Nhập value"
                        disabled={isLoading}
                    />
                    <button onClick={handleSet} disabled={isLoading || !keyInput || !valueInput} className="btn">Set Value</button>
                </div>

                <div className="control-group">
                    <button onClick={handleGetAll} disabled={isLoading} className="btn">Get All (trong DB hiện tại)</button>
                </div>

                 <div className="control-group">
                     <label htmlFor="searchValue">Tìm kiếm Value:</label>
                     <input
                         type="text"
                         id="searchValue"
                         value={searchInput}
                         onChange={(e) => setSearchInput(e.target.value)}
                         placeholder="Nhập giá trị cần tìm"
                         disabled={isLoading}
                     />
                     <button onClick={handleSearchByValue} disabled={isLoading || !searchInput} className="btn">Search by Value</button>
                 </div>

                 <div className="control-group">
                     <label htmlFor="limit">Limit (Next Keys):</label>
                     <input
                         type="number"
                         id="limit"
                         value={limitInput}
                         min="1"
                         max="255"
                         onChange={(e) => setLimitInput(e.target.value)}
                         disabled={isLoading}
                         style={{width: '60px', marginRight: '10px'}}
                     />
                     <button onClick={handleGetNextKeys} disabled={isLoading || !keyInput} className="btn">Get Next Keys (từ Key đã nhập)</button>
                 </div>

                </>
             )}


          </div>

          {/* Thông báo Loading/Error chung */}
          {isLoading && <p className="loading-message">Đang xử lý giao dịch, vui lòng đợi...</p>}
          {error && <p className="error-message">{error}</p>}
        </>
      )}
    </div>
  );
}

export default ContractInteractionPage;