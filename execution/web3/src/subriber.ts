import { createPublicClient, webSocket, parseAbiItem, http } from 'viem';
import { localhost } from 'viem/chains'; // Sử dụng mạng Sepolia làm ví dụ

// --- Cấu hình ---
// Thay thế bằng WebSocket RPC URL của bạn cho mạng Sepolia
// Lấy từ Alchemy, Infura, QuickNode, ...
const webSocketRpcUrl = 'ws://192.168.1.99:8545'; // <--- THAY THẾ URL NÀY
// const client = createPublicClient({
//   chain: localhost,
//   transport: http('http://127.0.0.1:8545'), // URL node local
// });
// Địa chỉ hợp đồng token ERC20 bạn muốn theo dõi (ví dụ: một token trên Sepolia)
// Bạn có thể tìm địa chỉ này trên Etherscan Sepolia
const contractAddress = '0xfb961F92E8Ade3A2efFd4a8b095f8C009f2c8667'; // <--- THAY THẾ ĐỊA CHỈ NÀY (Đây là ví dụ Link Token trên Sepolia)

// ABI của sự kiện bạn muốn lắng nghe (chỉ cần phần sự kiện)
// Ví dụ: Sự kiện Transfer(address indexed from, address indexed to, uint256 value) của ERC20
const transferEventAbi = parseAbiItem(
    'event Transfer(address indexed from, address indexed to, uint256 value)'
);

// --- Khởi tạo Client ---
// Sử dụng createPublicClient vì chúng ta chỉ đọc/lắng nghe
// Cần transport là webSocket để dùng watchContractEvent
const client = createPublicClient({
    chain: localhost,
    transport: webSocket(webSocketRpcUrl, {
        // Tăng thời gian timeout nếu cần
        // timeout: 120_000,
    }),
    // Có thể thêm transport http dự phòng nhưng watch sẽ chỉ dùng ws
    // transport: webSocket(webSocketRpcUrl) ?? http(),
});

console.log(`Đang kết nối tới ${localhost.name} qua WebSocket...`);
console.log(`Lắng nghe sự kiện 'Transfer' từ hợp đồng: ${contractAddress}`);

// --- Bắt đầu Lắng nghe Sự kiện ---
// watchContractEvent trả về một hàm để dừng lắng nghe (unwatch)
const unwatch = client.watchContractEvent({
    address: contractAddress,
    abi: [transferEventAbi], // Phải là một mảng ABI item
    eventName: 'Transfer', // Tên sự kiện khớp với ABI
    onLogs: (logs) => {
        console.log('-----------------------------------------');
        console.log(`Nhận được ${logs.length} log sự kiện Transfer mới:`);
        logs.forEach((log) => {
            // log.args chứa các tham số đã được parse từ sự kiện
            const { from, to, value } = log.args;
            console.log(
                `  - Tx: ${log.transactionHash}` // Hash của giao dịch chứa sự kiện
            );
            console.log(`    Block: ${log.blockNumber}`); // Số khối chứa sự kiện
            console.log(`    From: ${from}`);
            console.log(`    To: ${to}`);
            console.log(`    Value: ${value?.toString()}`); // Giá trị thường là BigInt, cần chuyển đổi sang string
        });
        console.log('-----------------------------------------');
    },
    onError: (error) => {
        console.error('Lỗi khi lắng nghe sự kiện:', error);
        // Có thể thêm logic retry hoặc xử lý lỗi khác ở đây
    },
    // poll: true, // Sử dụng polling thay vì WebSocket (không khuyến khích cho subscriptions)
    // pollingInterval: 4000, // Thời gian poll (ms) nếu poll=true
});

console.log('Đã bắt đầu lắng nghe. Nhấn Ctrl+C để dừng.');

// --- Xử lý dừng ---
// Giữ cho script chạy và chờ sự kiện
// Trong ứng dụng thực tế, bạn có thể có logic phức tạp hơn
process.on('SIGINT', () => {
    console.log('\nĐang dừng lắng nghe...');
    unwatch(); // Gọi hàm unwatch để đóng kết nối
    console.log('Đã dừng.');
    process.exit(0);
});

// Giữ tiến trình chạy vô hạn (hoặc cho đến khi nhận SIGINT)
// Cần thiết vì watchContractEvent chạy bất đồng bộ
setInterval(() => { }, 1 << 30); // Hack đơn giản để giữ tiến trình sống