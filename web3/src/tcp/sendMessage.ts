import * as net from 'net';
import { message } from "../proto/message";


async function sendMessage(socket: net.Socket, newMessage: message.Message): Promise<void> {
    return new Promise((resolve, reject) => {
        // Chuyển đổi message thành buffer nếu cần thiết.
        const messageBuffer = newMessage.serializeBinary(); // Giả sử message đã là buffer

        // Viết độ dài của message (8 bytes)
        const messageLengthBuffer = Buffer.alloc(8);
        messageLengthBuffer.writeBigUInt64LE(BigInt(messageBuffer.length), 0);

        // Gửi độ dài message
        socket.write(messageLengthBuffer, (err) => {
            if (err) {
                reject(err);
                return;
            }

            // Gửi message
            socket.write(messageBuffer, (err) => {
                if (err) {
                    reject(err);
                    return;
                }
                resolve();
            });
        });

        // Thêm lắng nghe dữ liệu từ socket
        socket.once('data', (data) => {
            try {
                const response = message.Message.deserializeBinary(data);
                console.log(response)
                // resolve(response); // Trả về phản hồi
            } catch (error) {
                reject(new Error(`Lỗi giải mã phản hồi: ${error}`));
            }
        });

        socket.once('error', (err) => {
            reject(new Error(`Lỗi socket: ${err}`));
        });

        socket.once('close', () => {
            reject(new Error('Kết nối bị đóng'));
        });
    });
}


// Ví dụ sử dụng:
async function main() {
    const socket = new net.Socket();
    socket.connect(4200, 'localhost', () => {
        console.log('Connected to server!');

        const newMessage: message.Message = generateMessage();
        sendMessage(socket, newMessage)
            .then(() => {
                console.log('Message sent successfully!');
                socket.end();
            })
            .catch((err) => {
                console.error('Error sending message:', err);
                socket.end();
            });
    });
}
function hexToBytes(hex: string): Uint8Array {
    return new Uint8Array(hex.match(/.{1,2}/g)!.map(byte => parseInt(byte, 16)));
}


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

main();
