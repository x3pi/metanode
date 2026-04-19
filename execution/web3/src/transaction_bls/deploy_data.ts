import { transaction } from "../proto/transaction";

// ... existing code ...

/**
 * Tạo một đối tượng DeployData mới
 * @param code Mã byte của hợp đồng
 * @param storageAddress Địa chỉ lưu trữ
 * @returns Đối tượng DeployData
 */
export function NewDeployData(code: Uint8Array, storageAddress: Uint8Array): DeployData {
    const deployData = new transaction.DeployData(); //Tạo đối tượng DeployData
    deployData.Code = code; // Gán mã byte
    deployData.StorageAddress = storageAddress; // Gán địa chỉ lưu trữ
    return new DeployData(deployData); // Trả về đối tượng DeployData
}


class DeployData { //Thêm class DeployData
    private proto: transaction.DeployData;

    constructor(proto: transaction.DeployData) {
        this.proto = proto;
    }

    Proto(): transaction.DeployData {
        return this.proto;
    }
}
