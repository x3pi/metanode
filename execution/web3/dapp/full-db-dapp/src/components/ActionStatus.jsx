// src/components/ActionStatus.jsx
import React from 'react';

/**
 * Component hiển thị các thông báo trạng thái: loading, lỗi, thành công, và link transaction hash.
 * @param {object} props - Props của component.
 * @param {boolean} props.isLoading - Trạng thái đang tải.
 * @param {string} props.error - Chuỗi thông báo lỗi.
 * @param {string} props.statusMessage - Chuỗi thông báo trạng thái thành công.
 * @param {string} props.txHash - Hash của giao dịch cuối cùng.
 * @param {object} props.chain - Object chain từ viem (vd: sepolia) để lấy tên cho link Etherscan.
 */
function ActionStatus({ isLoading, error, statusMessage, txHash, chain }) {
    // Xác định URL Etherscan dựa trên chain được cung cấp
    // Mặc định là sepolia nếu không có chain hoặc tên chain không được hỗ trợ/biết đến
    const chainName = chain?.name?.toLowerCase() || 'sepolia';
    let etherscanBaseUrl = `https://${chainName}.etherscan.io/tx/`;
    // Xử lý trường hợp đặc biệt như localhost hoặc tên chain không chuẩn
    if (chainName === 'localhost' || !chain?.name) {
       etherscanBaseUrl = null; // Không hiển thị link cho localhost
    } else if (chainName === 'mainnet') {
        etherscanBaseUrl = `https://etherscan.io/tx/`; // Mainnet không có prefix subdomain
    } // Thêm các else if cho các mạng khác nếu cần (polygon, arbitrum, ...)

    const etherscanUrl = txHash && etherscanBaseUrl ? `${etherscanBaseUrl}${txHash}` : null;

    // Ưu tiên hiển thị theo thứ tự: Loading > Error > Status Message
    if (isLoading) {
        // Hiển thị thông báo loading, có thể thêm spinner ở đây nếu muốn
        return <p className="loading-message">Đang xử lý...</p>;
    }
    if (error) {
        // Hiển thị thông báo lỗi
        return <p className="error-message">{error}</p>;
    }
    if (statusMessage) {
        // Hiển thị thông báo trạng thái thành công và link tx nếu có
        return (
            <div>
                <p className="status-message">{statusMessage}</p>
                {/* Hiển thị link Etherscan dưới thông báo thành công */}
                {etherscanUrl && (
                    <p className="tx-info" style={{ textAlign: 'center', marginTop: '-10px', marginBottom: '15px' }}>
                        Hash: <a href={etherscanUrl} target="_blank" rel="noopener noreferrer" title={txHash}>
                            {`${txHash.substring(0, 10)}...${txHash.substring(txHash.length - 8)}`} (Xem trên {chain.name || 'Etherscan'})
                        </a>
                    </p>
                )}
            </div>
        );
    }

    // Nếu không loading, không error, không status message, nhưng có txHash cũ, vẫn hiển thị link
    if (etherscanUrl) {
        return (
            <p className="tx-info" style={{ textAlign: 'center' }}>
                Hash giao dịch cuối: <a href={etherscanUrl} target="_blank" rel="noopener noreferrer" title={txHash}>
                    {`${txHash.substring(0, 10)}...${txHash.substring(txHash.length - 8)}`} (Xem trên {chain.name || 'Etherscan'})
                </a>
            </p>
        );
    }

    // Không hiển thị gì nếu không có trạng thái nào cần thông báo
    return null;
}

export default ActionStatus;