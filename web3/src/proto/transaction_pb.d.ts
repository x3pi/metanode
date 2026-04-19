import * as jspb from 'google-protobuf'



export class Transaction extends jspb.Message {
  getToaddress(): Uint8Array | string;
  getToaddress_asU8(): Uint8Array;
  getToaddress_asB64(): string;
  setToaddress(value: Uint8Array | string): Transaction;

  getAmount(): Uint8Array | string;
  getAmount_asU8(): Uint8Array;
  getAmount_asB64(): string;
  setAmount(value: Uint8Array | string): Transaction;

  getMaxgas(): number;
  setMaxgas(value: number): Transaction;

  getMaxgasprice(): number;
  setMaxgasprice(value: number): Transaction;

  getMaxtimeuse(): number;
  setMaxtimeuse(value: number): Transaction;

  getData(): Uint8Array | string;
  getData_asU8(): Uint8Array;
  getData_asB64(): string;
  setData(value: Uint8Array | string): Transaction;

  getRelatedaddressesList(): Array<Uint8Array | string>;
  setRelatedaddressesList(value: Array<Uint8Array | string>): Transaction;
  clearRelatedaddressesList(): Transaction;
  addRelatedaddresses(value: Uint8Array | string, index?: number): Transaction;

  getLastdevicekey(): Uint8Array | string;
  getLastdevicekey_asU8(): Uint8Array;
  getLastdevicekey_asB64(): string;
  setLastdevicekey(value: Uint8Array | string): Transaction;

  getNewdevicekey(): Uint8Array | string;
  getNewdevicekey_asU8(): Uint8Array;
  getNewdevicekey_asB64(): string;
  setNewdevicekey(value: Uint8Array | string): Transaction;

  getSign(): Uint8Array | string;
  getSign_asU8(): Uint8Array;
  getSign_asB64(): string;
  setSign(value: Uint8Array | string): Transaction;

  getNonce(): Uint8Array | string;
  getNonce_asU8(): Uint8Array;
  getNonce_asB64(): string;
  setNonce(value: Uint8Array | string): Transaction;

  getFromaddress(): Uint8Array | string;
  getFromaddress_asU8(): Uint8Array;
  getFromaddress_asB64(): string;
  setFromaddress(value: Uint8Array | string): Transaction;

  getReadonly(): boolean;
  setReadonly(value: boolean): Transaction;

  getChainid(): number;
  setChainid(value: number): Transaction;

  getType(): number;
  setType(value: number): Transaction;

  getR(): Uint8Array | string;
  getR_asU8(): Uint8Array;
  getR_asB64(): string;
  setR(value: Uint8Array | string): Transaction;

  getS(): Uint8Array | string;
  getS_asU8(): Uint8Array;
  getS_asB64(): string;
  setS(value: Uint8Array | string): Transaction;

  getV(): Uint8Array | string;
  getV_asU8(): Uint8Array;
  getV_asB64(): string;
  setV(value: Uint8Array | string): Transaction;

  getGastipcap(): Uint8Array | string;
  getGastipcap_asU8(): Uint8Array;
  getGastipcap_asB64(): string;
  setGastipcap(value: Uint8Array | string): Transaction;

  getGasfeecap(): Uint8Array | string;
  getGasfeecap_asU8(): Uint8Array;
  getGasfeecap_asB64(): string;
  setGasfeecap(value: Uint8Array | string): Transaction;

  getAccesslistList(): Array<AccessTuple>;
  setAccesslistList(value: Array<AccessTuple>): Transaction;
  clearAccesslistList(): Transaction;
  addAccesslist(value?: AccessTuple, index?: number): AccessTuple;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): Transaction.AsObject;
  static toObject(includeInstance: boolean, msg: Transaction): Transaction.AsObject;
  static serializeBinaryToWriter(message: Transaction, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): Transaction;
  static deserializeBinaryFromReader(message: Transaction, reader: jspb.BinaryReader): Transaction;
}

export namespace Transaction {
  export type AsObject = {
    toaddress: Uint8Array | string,
    amount: Uint8Array | string,
    maxgas: number,
    maxgasprice: number,
    maxtimeuse: number,
    data: Uint8Array | string,
    relatedaddressesList: Array<Uint8Array | string>,
    lastdevicekey: Uint8Array | string,
    newdevicekey: Uint8Array | string,
    sign: Uint8Array | string,
    nonce: Uint8Array | string,
    fromaddress: Uint8Array | string,
    readonly: boolean,
    chainid: number,
    type: number,
    r: Uint8Array | string,
    s: Uint8Array | string,
    v: Uint8Array | string,
    gastipcap: Uint8Array | string,
    gasfeecap: Uint8Array | string,
    accesslistList: Array<AccessTuple.AsObject>,
  }
}

export class AccessTuple extends jspb.Message {
  getAddress(): Uint8Array | string;
  getAddress_asU8(): Uint8Array;
  getAddress_asB64(): string;
  setAddress(value: Uint8Array | string): AccessTuple;

  getStoragekeysList(): Array<Uint8Array | string>;
  setStoragekeysList(value: Array<Uint8Array | string>): AccessTuple;
  clearStoragekeysList(): AccessTuple;
  addStoragekeys(value: Uint8Array | string, index?: number): AccessTuple;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): AccessTuple.AsObject;
  static toObject(includeInstance: boolean, msg: AccessTuple): AccessTuple.AsObject;
  static serializeBinaryToWriter(message: AccessTuple, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): AccessTuple;
  static deserializeBinaryFromReader(message: AccessTuple, reader: jspb.BinaryReader): AccessTuple;
}

export namespace AccessTuple {
  export type AsObject = {
    address: Uint8Array | string,
    storagekeysList: Array<Uint8Array | string>,
  }
}

export class TransactionHashData extends jspb.Message {
  getLasthash(): Uint8Array | string;
  getLasthash_asU8(): Uint8Array;
  getLasthash_asB64(): string;
  setLasthash(value: Uint8Array | string): TransactionHashData;

  getToaddress(): Uint8Array | string;
  getToaddress_asU8(): Uint8Array;
  getToaddress_asB64(): string;
  setToaddress(value: Uint8Array | string): TransactionHashData;

  getAmount(): Uint8Array | string;
  getAmount_asU8(): Uint8Array;
  getAmount_asB64(): string;
  setAmount(value: Uint8Array | string): TransactionHashData;

  getMaxgas(): number;
  setMaxgas(value: number): TransactionHashData;

  getMaxgasprice(): number;
  setMaxgasprice(value: number): TransactionHashData;

  getMaxtimeuse(): number;
  setMaxtimeuse(value: number): TransactionHashData;

  getData(): Uint8Array | string;
  getData_asU8(): Uint8Array;
  getData_asB64(): string;
  setData(value: Uint8Array | string): TransactionHashData;

  getRelatedaddressesList(): Array<Uint8Array | string>;
  setRelatedaddressesList(value: Array<Uint8Array | string>): TransactionHashData;
  clearRelatedaddressesList(): TransactionHashData;
  addRelatedaddresses(value: Uint8Array | string, index?: number): TransactionHashData;

  getLastdevicekey(): Uint8Array | string;
  getLastdevicekey_asU8(): Uint8Array;
  getLastdevicekey_asB64(): string;
  setLastdevicekey(value: Uint8Array | string): TransactionHashData;

  getNewdevicekey(): Uint8Array | string;
  getNewdevicekey_asU8(): Uint8Array;
  getNewdevicekey_asB64(): string;
  setNewdevicekey(value: Uint8Array | string): TransactionHashData;

  getNonce(): Uint8Array | string;
  getNonce_asU8(): Uint8Array;
  getNonce_asB64(): string;
  setNonce(value: Uint8Array | string): TransactionHashData;

  getFromaddress(): Uint8Array | string;
  getFromaddress_asU8(): Uint8Array;
  getFromaddress_asB64(): string;
  setFromaddress(value: Uint8Array | string): TransactionHashData;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): TransactionHashData.AsObject;
  static toObject(includeInstance: boolean, msg: TransactionHashData): TransactionHashData.AsObject;
  static serializeBinaryToWriter(message: TransactionHashData, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): TransactionHashData;
  static deserializeBinaryFromReader(message: TransactionHashData, reader: jspb.BinaryReader): TransactionHashData;
}

export namespace TransactionHashData {
  export type AsObject = {
    lasthash: Uint8Array | string,
    toaddress: Uint8Array | string,
    amount: Uint8Array | string,
    maxgas: number,
    maxgasprice: number,
    maxtimeuse: number,
    data: Uint8Array | string,
    relatedaddressesList: Array<Uint8Array | string>,
    lastdevicekey: Uint8Array | string,
    newdevicekey: Uint8Array | string,
    nonce: Uint8Array | string,
    fromaddress: Uint8Array | string,
  }
}

export class DeployData extends jspb.Message {
  getCode(): Uint8Array | string;
  getCode_asU8(): Uint8Array;
  getCode_asB64(): string;
  setCode(value: Uint8Array | string): DeployData;

  getStorageaddress(): Uint8Array | string;
  getStorageaddress_asU8(): Uint8Array;
  getStorageaddress_asB64(): string;
  setStorageaddress(value: Uint8Array | string): DeployData;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): DeployData.AsObject;
  static toObject(includeInstance: boolean, msg: DeployData): DeployData.AsObject;
  static serializeBinaryToWriter(message: DeployData, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): DeployData;
  static deserializeBinaryFromReader(message: DeployData, reader: jspb.BinaryReader): DeployData;
}

export namespace DeployData {
  export type AsObject = {
    code: Uint8Array | string,
    storageaddress: Uint8Array | string,
  }
}

export class CallData extends jspb.Message {
  getInput(): Uint8Array | string;
  getInput_asU8(): Uint8Array;
  getInput_asB64(): string;
  setInput(value: Uint8Array | string): CallData;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): CallData.AsObject;
  static toObject(includeInstance: boolean, msg: CallData): CallData.AsObject;
  static serializeBinaryToWriter(message: CallData, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): CallData;
  static deserializeBinaryFromReader(message: CallData, reader: jspb.BinaryReader): CallData;
}

export namespace CallData {
  export type AsObject = {
    input: Uint8Array | string,
  }
}

export class OpenStateChannelData extends jspb.Message {
  getValidatoraddressesList(): Array<Uint8Array | string>;
  setValidatoraddressesList(value: Array<Uint8Array | string>): OpenStateChannelData;
  clearValidatoraddressesList(): OpenStateChannelData;
  addValidatoraddresses(value: Uint8Array | string, index?: number): OpenStateChannelData;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): OpenStateChannelData.AsObject;
  static toObject(includeInstance: boolean, msg: OpenStateChannelData): OpenStateChannelData.AsObject;
  static serializeBinaryToWriter(message: OpenStateChannelData, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): OpenStateChannelData;
  static deserializeBinaryFromReader(message: OpenStateChannelData, reader: jspb.BinaryReader): OpenStateChannelData;
}

export namespace OpenStateChannelData {
  export type AsObject = {
    validatoraddressesList: Array<Uint8Array | string>,
  }
}

export class UpdateStorageHostData extends jspb.Message {
  getStoragehost(): string;
  setStoragehost(value: string): UpdateStorageHostData;

  getStorageaddress(): Uint8Array | string;
  getStorageaddress_asU8(): Uint8Array;
  getStorageaddress_asB64(): string;
  setStorageaddress(value: Uint8Array | string): UpdateStorageHostData;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): UpdateStorageHostData.AsObject;
  static toObject(includeInstance: boolean, msg: UpdateStorageHostData): UpdateStorageHostData.AsObject;
  static serializeBinaryToWriter(message: UpdateStorageHostData, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): UpdateStorageHostData;
  static deserializeBinaryFromReader(message: UpdateStorageHostData, reader: jspb.BinaryReader): UpdateStorageHostData;
}

export namespace UpdateStorageHostData {
  export type AsObject = {
    storagehost: string,
    storageaddress: Uint8Array | string,
  }
}

export class MassTransferData extends jspb.Message {
  getMapaddressamountMap(): jspb.Map<string, Uint8Array | string>;
  clearMapaddressamountMap(): MassTransferData;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): MassTransferData.AsObject;
  static toObject(includeInstance: boolean, msg: MassTransferData): MassTransferData.AsObject;
  static serializeBinaryToWriter(message: MassTransferData, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): MassTransferData;
  static deserializeBinaryFromReader(message: MassTransferData, reader: jspb.BinaryReader): MassTransferData;
}

export namespace MassTransferData {
  export type AsObject = {
    mapaddressamountMap: Array<[string, Uint8Array | string]>,
  }
}

export class Transactions extends jspb.Message {
  getTransactionsList(): Array<Transaction>;
  setTransactionsList(value: Array<Transaction>): Transactions;
  clearTransactionsList(): Transactions;
  addTransactions(value?: Transaction, index?: number): Transaction;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): Transactions.AsObject;
  static toObject(includeInstance: boolean, msg: Transactions): Transactions.AsObject;
  static serializeBinaryToWriter(message: Transactions, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): Transactions;
  static deserializeBinaryFromReader(message: Transactions, reader: jspb.BinaryReader): Transactions;
}

export namespace Transactions {
  export type AsObject = {
    transactionsList: Array<Transaction.AsObject>,
  }
}

export class VerifyTransactionSignRequest extends jspb.Message {
  getHash(): Uint8Array | string;
  getHash_asU8(): Uint8Array;
  getHash_asB64(): string;
  setHash(value: Uint8Array | string): VerifyTransactionSignRequest;

  getPubkey(): Uint8Array | string;
  getPubkey_asU8(): Uint8Array;
  getPubkey_asB64(): string;
  setPubkey(value: Uint8Array | string): VerifyTransactionSignRequest;

  getSign(): Uint8Array | string;
  getSign_asU8(): Uint8Array;
  getSign_asB64(): string;
  setSign(value: Uint8Array | string): VerifyTransactionSignRequest;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): VerifyTransactionSignRequest.AsObject;
  static toObject(includeInstance: boolean, msg: VerifyTransactionSignRequest): VerifyTransactionSignRequest.AsObject;
  static serializeBinaryToWriter(message: VerifyTransactionSignRequest, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): VerifyTransactionSignRequest;
  static deserializeBinaryFromReader(message: VerifyTransactionSignRequest, reader: jspb.BinaryReader): VerifyTransactionSignRequest;
}

export namespace VerifyTransactionSignRequest {
  export type AsObject = {
    hash: Uint8Array | string,
    pubkey: Uint8Array | string,
    sign: Uint8Array | string,
  }
}

export class VerifyTransactionSignResult extends jspb.Message {
  getHash(): Uint8Array | string;
  getHash_asU8(): Uint8Array;
  getHash_asB64(): string;
  setHash(value: Uint8Array | string): VerifyTransactionSignResult;

  getValid(): boolean;
  setValid(value: boolean): VerifyTransactionSignResult;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): VerifyTransactionSignResult.AsObject;
  static toObject(includeInstance: boolean, msg: VerifyTransactionSignResult): VerifyTransactionSignResult.AsObject;
  static serializeBinaryToWriter(message: VerifyTransactionSignResult, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): VerifyTransactionSignResult;
  static deserializeBinaryFromReader(message: VerifyTransactionSignResult, reader: jspb.BinaryReader): VerifyTransactionSignResult;
}

export namespace VerifyTransactionSignResult {
  export type AsObject = {
    hash: Uint8Array | string,
    valid: boolean,
  }
}

export class TransactionError extends jspb.Message {
  getCode(): number;
  setCode(value: number): TransactionError;

  getDescription(): string;
  setDescription(value: string): TransactionError;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): TransactionError.AsObject;
  static toObject(includeInstance: boolean, msg: TransactionError): TransactionError.AsObject;
  static serializeBinaryToWriter(message: TransactionError, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): TransactionError;
  static deserializeBinaryFromReader(message: TransactionError, reader: jspb.BinaryReader): TransactionError;
}

export namespace TransactionError {
  export type AsObject = {
    code: number,
    description: string,
  }
}

export class TransactionHashWithErrorCode extends jspb.Message {
  getTransactionhash(): Uint8Array | string;
  getTransactionhash_asU8(): Uint8Array;
  getTransactionhash_asB64(): string;
  setTransactionhash(value: Uint8Array | string): TransactionHashWithErrorCode;

  getCode(): number;
  setCode(value: number): TransactionHashWithErrorCode;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): TransactionHashWithErrorCode.AsObject;
  static toObject(includeInstance: boolean, msg: TransactionHashWithErrorCode): TransactionHashWithErrorCode.AsObject;
  static serializeBinaryToWriter(message: TransactionHashWithErrorCode, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): TransactionHashWithErrorCode;
  static deserializeBinaryFromReader(message: TransactionHashWithErrorCode, reader: jspb.BinaryReader): TransactionHashWithErrorCode;
}

export namespace TransactionHashWithErrorCode {
  export type AsObject = {
    transactionhash: Uint8Array | string,
    code: number,
  }
}

export class TransactionWithDeviceKey extends jspb.Message {
  getTransaction(): Transaction | undefined;
  setTransaction(value?: Transaction): TransactionWithDeviceKey;
  hasTransaction(): boolean;
  clearTransaction(): TransactionWithDeviceKey;

  getDevicekey(): Uint8Array | string;
  getDevicekey_asU8(): Uint8Array;
  getDevicekey_asB64(): string;
  setDevicekey(value: Uint8Array | string): TransactionWithDeviceKey;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): TransactionWithDeviceKey.AsObject;
  static toObject(includeInstance: boolean, msg: TransactionWithDeviceKey): TransactionWithDeviceKey.AsObject;
  static serializeBinaryToWriter(message: TransactionWithDeviceKey, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): TransactionWithDeviceKey;
  static deserializeBinaryFromReader(message: TransactionWithDeviceKey, reader: jspb.BinaryReader): TransactionWithDeviceKey;
}

export namespace TransactionWithDeviceKey {
  export type AsObject = {
    transaction?: Transaction.AsObject,
    devicekey: Uint8Array | string,
  }
}

export class TransactionsWithBlockNumber extends jspb.Message {
  getTransactionsList(): Array<Transaction>;
  setTransactionsList(value: Array<Transaction>): TransactionsWithBlockNumber;
  clearTransactionsList(): TransactionsWithBlockNumber;
  addTransactions(value?: Transaction, index?: number): Transaction;

  getBlocknumber(): number;
  setBlocknumber(value: number): TransactionsWithBlockNumber;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): TransactionsWithBlockNumber.AsObject;
  static toObject(includeInstance: boolean, msg: TransactionsWithBlockNumber): TransactionsWithBlockNumber.AsObject;
  static serializeBinaryToWriter(message: TransactionsWithBlockNumber, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): TransactionsWithBlockNumber;
  static deserializeBinaryFromReader(message: TransactionsWithBlockNumber, reader: jspb.BinaryReader): TransactionsWithBlockNumber;
}

export namespace TransactionsWithBlockNumber {
  export type AsObject = {
    transactionsList: Array<Transaction.AsObject>,
    blocknumber: number,
  }
}

export class ExecuteSCTransactions extends jspb.Message {
  getTransactionsList(): Array<Transaction>;
  setTransactionsList(value: Array<Transaction>): ExecuteSCTransactions;
  clearTransactionsList(): ExecuteSCTransactions;
  addTransactions(value?: Transaction, index?: number): Transaction;

  getGroupid(): number;
  setGroupid(value: number): ExecuteSCTransactions;

  getBlocknumber(): number;
  setBlocknumber(value: number): ExecuteSCTransactions;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): ExecuteSCTransactions.AsObject;
  static toObject(includeInstance: boolean, msg: ExecuteSCTransactions): ExecuteSCTransactions.AsObject;
  static serializeBinaryToWriter(message: ExecuteSCTransactions, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): ExecuteSCTransactions;
  static deserializeBinaryFromReader(message: ExecuteSCTransactions, reader: jspb.BinaryReader): ExecuteSCTransactions;
}

export namespace ExecuteSCTransactions {
  export type AsObject = {
    transactionsList: Array<Transaction.AsObject>,
    groupid: number,
    blocknumber: number,
  }
}

export class FromNodeTransactionsResult extends jspb.Message {
  getValidtransactionhashesList(): Array<Uint8Array | string>;
  setValidtransactionhashesList(value: Array<Uint8Array | string>): FromNodeTransactionsResult;
  clearValidtransactionhashesList(): FromNodeTransactionsResult;
  addValidtransactionhashes(value: Uint8Array | string, index?: number): FromNodeTransactionsResult;

  getTransactionerrorsList(): Array<TransactionHashWithErrorCode>;
  setTransactionerrorsList(value: Array<TransactionHashWithErrorCode>): FromNodeTransactionsResult;
  clearTransactionerrorsList(): FromNodeTransactionsResult;
  addTransactionerrors(value?: TransactionHashWithErrorCode, index?: number): TransactionHashWithErrorCode;

  getBlocknumber(): number;
  setBlocknumber(value: number): FromNodeTransactionsResult;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): FromNodeTransactionsResult.AsObject;
  static toObject(includeInstance: boolean, msg: FromNodeTransactionsResult): FromNodeTransactionsResult.AsObject;
  static serializeBinaryToWriter(message: FromNodeTransactionsResult, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): FromNodeTransactionsResult;
  static deserializeBinaryFromReader(message: FromNodeTransactionsResult, reader: jspb.BinaryReader): FromNodeTransactionsResult;
}

export namespace FromNodeTransactionsResult {
  export type AsObject = {
    validtransactionhashesList: Array<Uint8Array | string>,
    transactionerrorsList: Array<TransactionHashWithErrorCode.AsObject>,
    blocknumber: number,
  }
}

export class ToNodeTransactionsResult extends jspb.Message {
  getValidtransactionhashesList(): Array<Uint8Array | string>;
  setValidtransactionhashesList(value: Array<Uint8Array | string>): ToNodeTransactionsResult;
  clearValidtransactionhashesList(): ToNodeTransactionsResult;
  addValidtransactionhashes(value: Uint8Array | string, index?: number): ToNodeTransactionsResult;

  getBlocknumber(): number;
  setBlocknumber(value: number): ToNodeTransactionsResult;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): ToNodeTransactionsResult.AsObject;
  static toObject(includeInstance: boolean, msg: ToNodeTransactionsResult): ToNodeTransactionsResult.AsObject;
  static serializeBinaryToWriter(message: ToNodeTransactionsResult, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): ToNodeTransactionsResult;
  static deserializeBinaryFromReader(message: ToNodeTransactionsResult, reader: jspb.BinaryReader): ToNodeTransactionsResult;
}

export namespace ToNodeTransactionsResult {
  export type AsObject = {
    validtransactionhashesList: Array<Uint8Array | string>,
    blocknumber: number,
  }
}

export class TransactionsFromLeader extends jspb.Message {
  getTransactionsList(): Array<Transaction>;
  setTransactionsList(value: Array<Transaction>): TransactionsFromLeader;
  clearTransactionsList(): TransactionsFromLeader;
  addTransactions(value?: Transaction, index?: number): Transaction;

  getAggsign(): Uint8Array | string;
  getAggsign_asU8(): Uint8Array;
  getAggsign_asB64(): string;
  setAggsign(value: Uint8Array | string): TransactionsFromLeader;

  getBlocknumber(): number;
  setBlocknumber(value: number): TransactionsFromLeader;

  getTimestamp(): number;
  setTimestamp(value: number): TransactionsFromLeader;

  serializeBinary(): Uint8Array;
  toObject(includeInstance?: boolean): TransactionsFromLeader.AsObject;
  static toObject(includeInstance: boolean, msg: TransactionsFromLeader): TransactionsFromLeader.AsObject;
  static serializeBinaryToWriter(message: TransactionsFromLeader, writer: jspb.BinaryWriter): void;
  static deserializeBinary(bytes: Uint8Array): TransactionsFromLeader;
  static deserializeBinaryFromReader(message: TransactionsFromLeader, reader: jspb.BinaryReader): TransactionsFromLeader;
}

export namespace TransactionsFromLeader {
  export type AsObject = {
    transactionsList: Array<Transaction.AsObject>,
    aggsign: Uint8Array | string,
    blocknumber: number,
    timestamp: number,
  }
}

export enum ACTION { 
  EMPTY = 0,
  STAKE = 1,
  UNSTAKE = 2,
  DEPLOY_SMART_CONTRACT = 3,
  CALL_SMART_CONTRACT = 4,
  REWARD = 5,
  PUNISH = 6,
  MINE = 7,
  OPEN_CHANNEL = 8,
  JOIN_CHANNEL = 9,
  COMMIT_ACCOUNT_STATE_CHANNEL = 10,
  COMMIT_CHANNEL = 11,
  UPDATE_STORAGE_HOST = 12,
}
export enum FEE_TYPE { 
  USER_CHARGE_FEE = 0,
  SMART_CONTRACT_CHARGE_FEE = 1,
}
