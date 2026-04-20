import { useState, useEffect, useCallback, useRef } from "react";
import {
  encodeFunctionData,
  encodePacked,
  type Hex,
  hexToBytes,
  hexToString,
} from "viem";
import { useWallet } from "~/contexts/WalletContext";
import { PageContainer } from "~/components/PageContainer";
import { PageCard } from "~/components/PageCard";
import { AppButton } from "~/components/ui/app-button";
import { Label } from "~/components/ui/label";
import { Badge } from "~/components/ui/badge";
import { Alert } from "~/components/ui/alert";
import { contracts } from "~/constants/contracts";
import { chain991 } from "~/constants/customChain";
import LoadingSpinnerIcon from "~/components/LoadingSpinnerIcon";
import Pagination from "~/components/pagination/Pagination";
import TransactionListPage from "./components/TransactionListPage";

interface BlsAccount {
  address: Uint8Array;
  blsPublicKey: Uint8Array;
  registeredAt: bigint;
  isConfirmed: boolean;
  confirmedAt?: bigint;
  registerTxHash: Uint8Array;
  confirmTxHash?: Uint8Array;
}
interface CachedAuth {
  signature: Hex;
  timestamp: bigint;
  expiry: number;
}
const BLS_PUBLIC_KEY =
  "0x86d5de6f7c9c13cc0d959a553cc0e4853ba5faae45a28da9bddc8ef8e104eb5d3dece8dfaa24f11b4243ec27537e3184" as Hex;

function BlsAccountListPage() {
  const { walletClient, publicClient } = useWallet();
  const loadAuthCache = useRef<CachedAuth | null>(null);
  const confirmAuthCache = useRef<Record<string, CachedAuth>>({});
  const [filter, setFilter] = useState<"confirmed" | "unconfirmed">(
    "unconfirmed"
  );
  const [accounts, setAccounts] = useState<BlsAccount[]>([]);
  const [currentPage, setCurrentPage] = useState(1);
  const [pageSize] = useState(10);
  const [totalPages, setTotalPages] = useState(0);
  const [total, setTotal] = useState(0);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string>("");
  const [confirmingAddress, setConfirmingAddress] = useState<string | null>(
    null
  );
  const [showTransactionList, setShowTransactionList] = useState(false);
  const [selectedAddressForTx, setSelectedAddressForTx] = useState<string>("");
  const [showTransferModal, setShowTransferModal] = useState(false);
  const [selectedAddressForTransfer, setSelectedAddressForTransfer] =
    useState<string>("");
  const [transferAmount, setTransferAmount] = useState<string>("");
  const [isTransferring, setIsTransferring] = useState(false);

  // Load accounts callback
  const loadAccounts = useCallback(async () => {
    if (!publicClient) return;
    setIsLoading(true);
    setError("");
    try {
      if (!walletClient) {
        throw new Error("Wallet client not connected");
      }
      const account = walletClient.account;
      if (!account) {
        throw new Error("No account connected");
      }
      let signature: Hex;
      let timestamp: bigint;
      const now = Math.floor(Date.now() / 1000);

      if (loadAuthCache.current && loadAuthCache.current.expiry > now + 10) {
        signature = loadAuthCache.current.signature;
        timestamp = loadAuthCache.current.timestamp;
      } else {
        timestamp = BigInt(now);
        const message = encodePacked(
          ["bytes", "uint256"],
          [BLS_PUBLIC_KEY, timestamp]
        );
        const sig = await walletClient.signMessage({
          account: account.address,
          message: { raw: message },
        });
        signature = sig as Hex;

        loadAuthCache.current = {
          signature: signature,
          timestamp: timestamp,
          expiry: now + 270, // 4.5 minutes
        };
      }
      const data = encodeFunctionData({
        abi: contracts.AccountManager.abi,
        functionName: "getAllAccount",
        args: [
          signature as Hex, // Ethereum signature (65 bytes)
          BLS_PUBLIC_KEY,
          BigInt(timestamp),
          BigInt(currentPage - 1), // Convert to 0-indexed for API
          BigInt(pageSize),
          filter === "confirmed",
        ],
      });

      // Call eth_call
      const result = await publicClient.call({
        to: contracts.AccountManager.address,
        data: data,
      });

      if (result.data) {
        const jsonStr = hexToString(result.data);
        const response: {
          accounts: Array<{
            address: string;
            blsPublicKey: string;
            registeredAt: number;
            registerTxHash: string;
            isConfirmed: boolean;
            confirmedAt: number;
            confirmTxHash: string;
          }>;
          total: number;
          page: number;
          pageSize: number;
          totalPage: number;
        } = JSON.parse(jsonStr);
        const convertedAccounts: BlsAccount[] = response.accounts.map(
          (acc) => ({
            address: hexToBytes(acc.address as Hex),
            blsPublicKey: hexToBytes(acc.blsPublicKey as Hex),
            registeredAt: BigInt(acc.registeredAt),
            isConfirmed: acc.isConfirmed,
            confirmedAt: acc.confirmedAt ? BigInt(acc.confirmedAt) : undefined,
            registerTxHash: hexToBytes(acc.registerTxHash as Hex),
            confirmTxHash: acc.confirmTxHash
              ? hexToBytes(acc.confirmTxHash as Hex)
              : undefined,
          })
        );

        setAccounts(convertedAccounts);
        setTotal(response.total);
        setTotalPages(response.totalPage);
      } else {
        setAccounts([]);
        setTotal(0);
        setTotalPages(0);
      }
    } catch (err) {
      console.error("Error loading accounts:", err);
      setError(err instanceof Error ? err.message : "Failed to load accounts");
    } finally {
      setIsLoading(false);
    }
  }, [walletClient, publicClient, currentPage, pageSize, filter]);

  // Load accounts when filter or page changes
  useEffect(() => {
    loadAccounts();
  }, [loadAccounts]);

  // ✅ Only clear cache when address actually changes (not on first mount)
  const previousAddress = useRef<string | undefined>(undefined);
  useEffect(() => {
    const currentAddress = walletClient?.account?.address;
    if (previousAddress.current && previousAddress.current !== currentAddress) {
      // Address changed, clear caches
      loadAuthCache.current = null;
      confirmAuthCache.current = {};
    }
    previousAddress.current = currentAddress;
  }, [walletClient?.account?.address]);
  const handleConfirmAccount = async (accountAddress: Hex) => {
    if (!walletClient || !publicClient) {
      setError("Wallet not connected");
      return;
    }
    console.log("vào confirm");
    setConfirmingAddress(accountAddress);
    let signature: Hex;
    let timestamp: bigint;
    setError("");
    try {
      const account = walletClient.account;
      if (!account) {
        throw new Error("No account connected");
      }
      const now = Math.floor(Date.now() / 1000);
      const cachedAuth = confirmAuthCache.current[accountAddress];

      // ✅ Use cache if it's still valid (with 10 second buffer instead of 30)
      if (cachedAuth && cachedAuth.expiry > now + 10) {
        signature = cachedAuth.signature;
        timestamp = cachedAuth.timestamp;
      } else {
        timestamp = BigInt(now);
        const message = encodePacked(
          ["address", "uint256"],
          [accountAddress, timestamp]
        );

        const sig = await walletClient.signMessage({
          account: account.address,
          message: { raw: message },
        });
        signature = sig as Hex;

        confirmAuthCache.current[accountAddress] = {
          signature: signature,
          timestamp: timestamp,
          expiry: now + 270, // 4.5 minutes
        };
      }
      // Get nonce
      const nonce = await publicClient.getTransactionCount({
        address: account.address,
        blockTag: "pending",
      });
      const data = encodeFunctionData({
        abi: contracts.AccountManager.abi,
        functionName: "confirmAccount",
        args: [accountAddress, BigInt(timestamp), signature as Hex],
      });
      // Estimate gas
      const gasLimit = await publicClient.estimateGas({
        account: account.address,
        to: contracts.AccountManager.address,
        data: data,
        value: 0n,
      });

      // Get gas price
      //   const gasPrice = await publicClient.getGasPrice();

      // Send transaction
      const txHash = await walletClient.sendTransaction({
        account: account.address,
        to: contracts.AccountManager.address,
        data: data,
        value: 0n,
        nonce: nonce,
        gas: gasLimit,
        gasPrice: 0n,
        chain: chain991,
      });
      // Wait for transaction
      const receipt = await publicClient.waitForTransactionReceipt({
        hash: txHash,
      });
      if (receipt.status === "success") {
        await loadAccounts();
        setError("");
        alert(`Account ${accountAddress} confirmed successfully!`);
      } else {
        throw new Error("Transaction failed");
      }
    } catch (err) {
      console.error("Error confirming account:", err);
      setError(
        err instanceof Error ? err.message : "Failed to confirm account"
      );
    } finally {
      setConfirmingAddress(null);
    }
  };

  const handleTransferToAccount = async () => {
    if (!walletClient || !publicClient) {
      setError("Wallet not connected");
      return;
    }
    if (!selectedAddressForTransfer) {
      setError("No recipient selected");
      return;
    }
    if (!transferAmount || parseFloat(transferAmount) <= 0) {
      setError("Please enter a valid amount");
      return;
    }

    setIsTransferring(true);
    setError("");

    try {
      const account = walletClient.account;
      if (!account) {
        throw new Error("No account connected");
      }

      const now = Math.floor(Date.now() / 1000);
      const timestamp = BigInt(now);
      const amount = BigInt(Math.floor(parseFloat(transferAmount) * 1e18)); // Convert to wei

      // Create message: toAddress + amount + timestamp
      const message = encodePacked(
        ["address", "uint256", "uint256"],
        [selectedAddressForTransfer as Hex, amount, timestamp]
      );

      // Sign the message
      const signature = await walletClient.signMessage({
        account: account.address,
        message: { raw: message },
      });

      // Encode function data for transferFrom
      const data = encodeFunctionData({
        abi: contracts.AccountManager.abi,
        functionName: "transferFrom",
        args: [selectedAddressForTransfer as Hex, amount, timestamp, signature as Hex],
      });

      // Get nonce
      const nonce = await publicClient.getTransactionCount({
        address: account.address,
        blockTag: "pending",
      });
      
      // Estimate gas
      const gasLimit = await publicClient.estimateGas({
        account: account.address,
        to: contracts.AccountManager.address,
        data: data,
        value: 0n,
      });

      // Send transaction
      const txHash = await walletClient.sendTransaction({
        account: account.address,
        to: contracts.AccountManager.address,
        data: data,
        value: 0n,
        nonce: nonce,
        gas: gasLimit,
        gasPrice: 0n,
        chain: chain991,
      });

      // Wait for transaction
      const receipt = await publicClient.waitForTransactionReceipt({
        hash: txHash,
      });

      if (receipt.status === "success") {
        setShowTransferModal(false);
        setTransferAmount("");
        setSelectedAddressForTransfer("");
        alert(
          `Successfully transferred ${transferAmount} to ${selectedAddressForTransfer}!`
        );
      } else {
        throw new Error("Transaction failed");
      }
    } catch (err) {
      console.error("Error transferring:", err);
      setError(err instanceof Error ? err.message : "Failed to transfer");
    } finally {
      setIsTransferring(false);
    }
  };

  const formatDate = (timestamp: bigint) => {
    return new Date(Number(timestamp) * 1000).toLocaleString();
  };

  const formatAddress = (addressBytes: Uint8Array) => {
    const hex = Array.from(addressBytes)
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("");
    const address = "0x" + hex;
    return `${address.slice(0, 6)}...${address.slice(-4)}`;
  };

  const formatHash = (hashBytes: Uint8Array) => {
    const hex = Array.from(hashBytes)
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("");
    return "0x" + hex;
  };

  const bytesToAddress = (bytes: Uint8Array): Hex => {
    const hex = Array.from(bytes)
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("");
    return ("0x" + hex) as Hex;
  };

  return (
    <PageContainer maxWidth="full">
      <PageCard
        title="BLS Account Management"
        description={`Manage accounts registered with BLS Public Key: ${BLS_PUBLIC_KEY.slice(
          0,
          20
        )}...`}
      >
        {/* Filter Buttons */}
        <div className="flex gap-4 mb-6">
          <AppButton
            onClick={() => {
              setFilter("unconfirmed");
              setCurrentPage(1);
            }}
            appVariant={filter === "unconfirmed" ? "primary" : "outline"}
            className="flex-1"
          >
            Unconfirmed ({filter === "unconfirmed" ? total : "?"})
          </AppButton>
          <AppButton
            onClick={() => {
              setFilter("confirmed");
              setCurrentPage(1);
            }}
            appVariant={filter === "confirmed" ? "primary" : "outline"}
            className="flex-1"
          >
            Confirmed ({filter === "confirmed" ? total : "?"})
          </AppButton>
        </div>
        {/* Error Alert */}
        {error && (
          <Alert
            variant="destructive"
            className="mb-4"
            style={{
              wordBreak: "break-word",
              overflowWrap: "anywhere",
              whiteSpace: "pre-wrap",
            }}
          >
            {error}
          </Alert>
        )}

        {/* Loading State */}
        {isLoading && (
          <div className="flex justify-center items-center py-12">
            <LoadingSpinnerIcon />
            <span className="ml-2">Loading accounts...</span>
          </div>
        )}

        {/* Accounts List */}
        {!isLoading && accounts.length > 0 && (
          <div className="space-y-4">
            {accounts.map((account) => (
              <div
                key={formatHash(account.address)}
                className="border border-border rounded-lg p-4 hover:bg-card-hover transition-colors"
              >
                <div className="flex items-start justify-between">
                  <div className="flex-1 space-y-2">
                    <div className="flex items-center gap-2">
                      <Label className="font-mono text-sm">
                        {formatHash(account.address)}
                      </Label>
                      <Badge
                        variant={account.isConfirmed ? "success" : "warning"}
                      >
                        {account.isConfirmed ? "Confirmed" : "Pending"}
                      </Badge>
                    </div>

                    <div className="text-xs text-app-muted space-y-1">
                      <div>
                        <span className="font-semibold">Registered:</span>{" "}
                        {formatDate(account.registeredAt)}
                      </div>
                      {account.isConfirmed && account.confirmedAt && (
                        <div>
                          <span className="font-semibold">Confirmed:</span>{" "}
                          {formatDate(account.confirmedAt)}
                        </div>
                      )}
                      <div>
                        <span className="font-semibold">Register TX:</span>{" "}
                        <span className="text-primary">
                          {formatAddress(account.registerTxHash)}
                        </span>
                      </div>
                      {account.confirmTxHash && (
                        <div>
                          <span className="font-semibold">Confirm TX:</span>{" "}
                          <span className="text-primary">
                            {formatAddress(account.confirmTxHash)}
                          </span>
                        </div>
                      )}
                    </div>
                  </div>

                  {/* Action Buttons */}
                  <div className="ml-4 flex gap-2">
                    {!account.isConfirmed && (
                      <AppButton
                        onClick={() =>
                          handleConfirmAccount(bytesToAddress(account.address))
                        }
                        disabled={
                          confirmingAddress === bytesToAddress(account.address)
                        }
                        size="sm"
                        appVariant="success"
                      >
                        {confirmingAddress ===
                        bytesToAddress(account.address) ? (
                          <>
                            <LoadingSpinnerIcon />
                            <span className="ml-2">Confirming...</span>
                          </>
                        ) : (
                          "Confirm"
                        )}
                      </AppButton>
                    )}
                    {account.isConfirmed && (
                      <AppButton
                        onClick={() => {
                          setSelectedAddressForTransfer(
                            bytesToAddress(account.address)
                          );
                          setShowTransferModal(true);
                        }}
                        size="sm"
                        appVariant="primary"
                      >
                        Transfer
                      </AppButton>
                    )}
                    <AppButton
                      onClick={() => {
                        setShowTransactionList(true);
                        setSelectedAddressForTx(
                          bytesToAddress(account.address)
                        );
                      }}
                      size="sm"
                      appVariant="outline"
                    >
                      View TX
                    </AppButton>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}

        {/* Empty State */}
        {!isLoading && accounts.length === 0 && (
          <div className="text-center py-12 text-app-muted">
            <p>No {filter} accounts found.</p>
            <p className="text-xs mt-2">
              Try registering a BLS public key first.
            </p>
          </div>
        )}

        {/* Pagination */}
        {totalPages > 1 && (
          <Pagination
            currentPage={currentPage}
            totalPageCount={totalPages}
            onPageChange={setCurrentPage}
            siblingCount={1}
          />
        )}

        {/* Refresh Button */}
        <div className="mt-6">
          <AppButton
            onClick={loadAccounts}
            disabled={isLoading}
            appVariant="outline"
            className="w-full"
          >
            {isLoading ? "Loading..." : "Refresh"}
          </AppButton>
        </div>
      </PageCard>

      {/* Transfer Modal */}
      {showTransferModal && selectedAddressForTransfer && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-card border border-border rounded-lg p-6 max-w-md w-full mx-4">
            <h3 className="text-lg font-semibold mb-4">Transfer to Account</h3>
            <div className="space-y-4">
              <div>
                <Label className="text-sm font-semibold mb-2">
                  Recipient Address
                </Label>
                <div className="text-xs font-mono text-app-muted break-all">
                  {selectedAddressForTransfer}
                </div>
              </div>
              <div>
                <Label htmlFor="amount" className="text-sm font-semibold mb-2">
                  Amount (in tokens)
                </Label>
                <input
                  id="amount"
                  type="number"
                  step="0.000001"
                  min="0"
                  value={transferAmount}
                  onChange={(e) => setTransferAmount(e.target.value)}
                  className="w-full px-3 py-2 border border-border rounded-md bg-background text-foreground"
                  placeholder="Enter amount"
                  disabled={isTransferring}
                />
              </div>
              {error && (
                <Alert variant="destructive" className="text-sm">
                  {error}
                </Alert>
              )}
              <div className="flex gap-2">
                <AppButton
                  onClick={handleTransferToAccount}
                  disabled={isTransferring || !transferAmount}
                  className="flex-1"
                  appVariant="primary"
                >
                  {isTransferring ? (
                    <>
                      <LoadingSpinnerIcon />
                      <span className="ml-2">Transferring...</span>
                    </>
                  ) : (
                    "Transfer"
                  )}
                </AppButton>
                <AppButton
                  onClick={() => {
                    setShowTransferModal(false);
                    setTransferAmount("");
                    setError("");
                  }}
                  disabled={isTransferring}
                  className="flex-1"
                  appVariant="outline"
                >
                  Cancel
                </AppButton>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Transaction List Modal */}
      {showTransactionList && selectedAddressForTx && (
        <div className="fixed inset-0 z-40 bg-black/20">
          <div className="overflow-auto">
            <TransactionListPage
              address={selectedAddressForTx}
              onClose={() => {
                setShowTransactionList(false);
                setSelectedAddressForTx("");
              }}
            />
          </div>
        </div>
      )}
    </PageContainer>
  );
}

export default BlsAccountListPage;
