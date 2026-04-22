import bls from 'bls-eth-wasm';
import { keccak256 } from 'ethereum-cryptography/keccak.js';
import { transaction } from "../proto/transaction";

export class Transaction {
    private cachedHash?: Uint8Array; // Biến cache lưu hash giao dịch
    private proto: transaction.Transaction; // Đối tượng giao dịch gốc

    constructor(proto: transaction.Transaction) {
        this.proto = proto;
    }

    /**
     * Lấy hash của giao dịch (có cache)
     * @returns Hash Keccak256 của dữ liệu giao dịch
     */
    public getHash(): Uint8Array {
        if (this.cachedHash) return this.cachedHash;

        // Tạo dữ liệu cần băm
        const hashPb = new transaction.TransactionHashData();
        hashPb.LastHash = this.proto.LastHash;
        hashPb.FromAddress = this.proto.FromAddress;
        hashPb.ToAddress = this.proto.ToAddress;
        hashPb.PendingUse = this.proto.PendingUse;
        hashPb.Amount = this.proto.Amount;
        hashPb.MaxGas = this.proto.MaxGas;
        hashPb.MaxGasPrice = this.proto.MaxGasPrice;
        hashPb.MaxTimeUse = this.proto.MaxTimeUse;
        hashPb.Data = this.proto.Data;
        hashPb.LastDeviceKey = this.proto.LastDeviceKey;
        hashPb.NewDeviceKey = this.proto.NewDeviceKey;
        hashPb.Nonce = this.proto.Nonce;

        // Mã hóa thành buffer
        const bHashPb = hashPb.serializeBinary();

        // Tính băm Keccak256
        this.cachedHash = keccak256(bHashPb);

        return this.cachedHash;
    }

    /**
     * Thiết lập chữ ký cho giao dịch bằng khóa riêng
     * @param privateKey Khóa riêng để ký giao dịch (BLS Private Key)
     */
    public async setSign(privateKey: Uint8Array): Promise<void> {
        // Đảm bảo thư viện BLS được khởi tạo trước khi sử dụng
        await bls.init(bls.BLS12_381);

        // Chuyển đổi khóa riêng thành đối tượng BLS
        const blsPrivateKey = new bls.SecretKey();
        blsPrivateKey.deserialize(privateKey);

        // Ký dữ liệu hash
        const signature = blsPrivateKey.sign(this.getHash());

        // Lưu chữ ký vào transaction
        this.proto.Sign = signature.serialize();
    }

    /**
   * Lấy số lượng (Amount) của giao dịch dưới dạng BigInt
   * @returns BigInt số lượng (wei)
   */
    public getAmount(): bigint {
        return BigInt(`0x${Buffer.from(this.proto.Amount).toString("hex")}`) || BigInt(0);
    }

    /**
     * Mã hóa giao dịch thành buffer (binary)
     * @returns Uint8Array chứa dữ liệu đã serialize
     */
    public marshal(): Uint8Array {
        return this.proto.serializeBinary();
    }

    /**
     * Giải mã dữ liệu binary thành Transaction
     * @param data Uint8Array chứa dữ liệu binary của giao dịch
     * @returns Transaction instance được giải mã
     */
    public static unmarshal(data: Uint8Array): Transaction {
        const protoTx = transaction.Transaction.deserializeBinary(data);
        return new Transaction(protoTx);
    }

    /**
 * Đặt địa chỉ người gửi (FromAddress)
 * @param address Chuỗi hex hoặc Uint8Array của địa chỉ ví
 */
    public setFromAddress(address: string | Uint8Array): void {
        this.proto.FromAddress = typeof address === "string" ? Transaction.hexToBytes(address) : address;
    }

    /**
     * Đặt địa chỉ người nhận (ToAddress)
     * @param address Chuỗi hex hoặc Uint8Array của địa chỉ ví
     */
    public setToAddress(address: string | Uint8Array): void {
        this.proto.ToAddress = typeof address === "string" ? Transaction.hexToBytes(address) : address;
    }

    /**
     * Chuyển chuỗi hex thành Uint8Array
     * @param hex Chuỗi hex
     * @returns Uint8Array tương ứng
     */
    private static hexToBytes(hex: string): Uint8Array {
        return new Uint8Array(hex.match(/.{1,2}/g)!.map(byte => parseInt(byte, 16)));
    }

    /**
 * Đặt giá trị nonce (số thứ tự của giao dịch)
 * @param nonce Giá trị nonce dạng number hoặc bigint
 */
    public setNonce(nonce: number | bigint): void {
        const buffer = new ArrayBuffer(8);
        const view = new DataView(buffer);
        view.setBigUint64(0, BigInt(nonce), false); // False để dùng Big-Endian
        this.proto.Nonce = new Uint8Array(buffer);
    }

    /**
 * Trả về đối tượng proto gốc
 * Tương đương với `func (t *Transaction) Proto() protoreflect.ProtoMessage`
 * @returns Đối tượng `transaction.Transaction`
 */
    public Proto(): transaction.Transaction {
        return this.proto;
    }

    /**
     * Lấy giá trị PendingUse của giao dịch dưới dạng BigInt
     * @returns BigInt biểu diễn giá trị PendingUse
     */
    public getPendingUse(): bigint {
        return BigInt(`0x${Buffer.from(this.proto.PendingUse).toString("hex")}`) || BigInt(0);
    }
    
    /**
     * Thiết lập giá trị PendingUse cho giao dịch.
     * @param pendingUse Giá trị PendingUse dạng number hoặc bigint.
     */
    public setPendingUse(pendingUse: number | bigint): void {
        const buffer = new ArrayBuffer(8); // Giả sử PendingUse là 64-bit (8 bytes)
        const view = new DataView(buffer);
        view.setBigUint64(0, BigInt(pendingUse), false); // Sử dụng Big-Endian
        this.proto.PendingUse = new Uint8Array(buffer);
    }


}


