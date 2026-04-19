import { message } from "../proto/message";


const socket = new WebSocket("ws://localhost:5001"); // Thay bằng URL WebSocket server của bạn

socket.binaryType = "arraybuffer"; // Đảm bảo nhận dữ liệu dạng binary

socket.onopen = () => {
    console.log("✅ Connected to server!");

    const newMessage = generateMessage();
    sendMessage(socket, newMessage)
        .then(() => console.log("📩 Message sent successfully!"))
        .catch((err) => console.error("❌ Error sending message:", err));
};

socket.onmessage = (event) => {
    try {
        const response = message.Message.deserializeBinary(new Uint8Array(event.data));
        console.log("📥 Received response:", response.toObject()); // In ra dạng object
    } catch (error) {
        console.error(`❌ Lỗi giải mã phản hồi: ${error}`);
    }
};

socket.onerror = (err) => {
    console.error("❌ Lỗi WebSocket:", err);
};

socket.onclose = () => {
    console.log("❌ WebSocket connection closed");
};

/**
 * Gửi message đến server qua WebSocket
 */
async function sendMessage(socket: WebSocket, newMessage: message.Message): Promise<void> {
    return new Promise((resolve, reject) => {
        if (socket.readyState !== WebSocket.OPEN) {
            return reject(new Error("❌ WebSocket is not open"));
        }

        const messageBuffer = newMessage.serializeBinary(); // Chuyển message thành binary

        // Định dạng dữ liệu gửi: gồm độ dài message (8 bytes) + message
        const messageLengthBuffer = new ArrayBuffer(8);
        new DataView(messageLengthBuffer).setBigUint64(0, BigInt(messageBuffer.length), true);

        const combinedBuffer = new Uint8Array(8 + messageBuffer.length);
        combinedBuffer.set(new Uint8Array(messageLengthBuffer), 0);
        combinedBuffer.set(new Uint8Array(messageBuffer), 8);

        // Gửi dữ liệu qua WebSocket
        socket.send(combinedBuffer);
        resolve();
    });
}

/**
 * Chuyển đổi chuỗi hex sang Uint8Array
 */
function hexToBytes(hex: string): Uint8Array {
    return new Uint8Array(hex.match(/.{1,2}/g)!.map(byte => parseInt(byte, 16)));
}

/**
 * Tạo một message mẫu theo protobuf
 */

function generateMessage(): message.Message {

    const newHeader = new message.Header({
        Command: "GetNonce",
    }
    );

    const newMessage = new message.Message({
        Header: newHeader,
        Body: hexToBytes("0x5AE1e723973577AcaB776ebC4be66231fc57b370"),
    }
    );
    return newMessage
}