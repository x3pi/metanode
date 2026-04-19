import { createPublicClient, http, Hex } from 'viem';
import { localhost } from 'viem/chains';


// Tạo client kết nối với RPC
const client = createPublicClient({
  chain: localhost,
  transport: http('http://127.0.0.1:8545'), // URL node local
});

async function getEthCall(params: { to: string; data: string }): Promise<string> { // Sửa đổi kiểu dữ liệu của params.to và params.data thành string
  try {
    // Chuyển đổi params.to và params.data thành Hex nếu cần thiết
    const toHex = params.to.startsWith('0x') ? params.to : `0x${params.to}`;
    const dataHex = params.data.startsWith('0x') ? params.data : `0x${params.data}`;

    const result = await client.call({
      to: toHex as Hex, // ép kiểu thành Hex
      data: dataHex as Hex, // ép kiểu thành Hex
    });

    return result.data ?? '0x'; // Không cần ép kiểu nữa vì đã là string
  } catch (error) {
    console.error('Lỗi khi gọi eth_call:', error);
    return '0x'; // Trả về '0x' nếu có lỗi
  }
}

// Ví dụ sử dụng getEthCall 0x8da5cb5b 0xeef814d3
async function callExample() {
  const params = {
    to: '0x136A0e4c974AF1C3Bba9cFBAD417f453a841F78a' as Hex,
    data: '0x95d89b41' as Hex,
  };

  const result = await getEthCall(params);
  console.log('Kết quả eth_call:', result);
}

callExample();
