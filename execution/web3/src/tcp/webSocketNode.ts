import { message } from "../proto/message";
import WebSocket from "ws";

const wsOptions = {
    rejectUnauthorized: false, // Bỏ kiểm tra SSL
};

const socket = new WebSocket("wss://localhost:5000", wsOptions);

socket.on("open", () => {
    console.log("✅ Connected to server!");
    const newMessage = generateMessage();
    sendMessage(socket, newMessage)
        .then(() => console.log("📩 Message sent successfully!"))
        .catch((err) => console.error("❌ Error sending message:", err));
});

socket.on("message", (data) => {
    try {
        const response = message.Message.deserializeBinary(new Uint8Array(data as any));
        console.log("📥 Received response:", response.toObject());
    } catch (error) {
        console.error(`❌ Lỗi giải mã phản hồi: ${error}`);
    }
});

socket.on("error", (err) => {
    console.error("❌ Lỗi WebSocket:", err);
});

socket.on("close", () => {
    console.log("❌ WebSocket connection closed");
});

async function sendMessage(socket: WebSocket, newMessage: message.Message): Promise<void> {
    return new Promise((resolve, reject) => {
        if (socket.readyState !== WebSocket.OPEN) {
            return reject(new Error("❌ WebSocket is not open"));
        }

        const messageBuffer = newMessage.serializeBinary();
        socket.send(messageBuffer, (err) => {
            if (err) reject(err);
            else resolve();
        });
    });
}

function generateMessage(): message.Message {
    const newHeader = new message.Header({
        Command: "GetNonce",
    });

    const newMessage = new message.Message({
        Header: newHeader,
        Body: hexToBytes("5AE1e723973577AcaB776ebC4be66231fc57b370"),
    });

    return newMessage;
}

function hexToBytes(hex: string): Uint8Array {
    return new Uint8Array(hex.match(/.{1,2}/g)!.map(byte => parseInt(byte, 16)));
}
