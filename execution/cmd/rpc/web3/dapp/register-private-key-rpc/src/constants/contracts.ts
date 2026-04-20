import type { Abi } from "viem";
import AccountAbi from "~/abi/Account.json";

export interface ContractConfig {
  abi: Abi;
  address: `0x${string}`;
}

export const contracts = {
  AccountManager: {
    abi: AccountAbi as Abi,
    address: "0x00000000000000000000000000000000D844bb55" as `0x${string}`,
  },
  // Thêm các contracts khác ở đây nếu cần
  // VíDụ:
  // TokenContract: {
  //   abi: TokenAbi as Abi,
  //   address: "0x..." as `0x${string}`,
  // },
} as const satisfies Record<string, ContractConfig>;

export type ContractName = keyof typeof contracts;
