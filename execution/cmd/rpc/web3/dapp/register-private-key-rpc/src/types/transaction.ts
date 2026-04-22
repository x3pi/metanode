export interface Transaction {
  blockHash: string;
  blockNumber: number;
  chainId: number;
  data: string;
  exception: string;
  from: string;
  gas: number;
  gasFee: number;
  gasPrice: number;
  gasUsed: number;
  hash: string;
  logs: Record<string, unknown>[];
  nonce: number;
  r: string;
  rHash: string;
  returnValue: string;
  s: string;
  status: string;
  timestamp: number;
  to: string;
  transactionIndex: number;
  v: string;
  value: string;
}

export interface TransactionResponse {
  total: number;
  transactions: Transaction[];
}
