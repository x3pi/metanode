import { useState, useEffect, useCallback } from "react";
import { useWallet } from "~/contexts/WalletContext";
import { chain991 } from "~/constants/customChain";
import { createPublicClient, http } from "viem";
import { Badge } from "~/components/ui/badge";
import { RefreshCw, Wallet, Key, Hash, Database } from "lucide-react";
import { PageContainer } from "~/components/PageContainer";
import { PageCard } from "~/components/PageCard";
import { Button } from "~/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "~/components/ui/alert";
import { StatusDisplay } from "~/components/StatusDisplay";

interface AccountState {
  address: string;
  balance: string;
  pending_balance: string;
  last_hash: string;
  device_key: string;
  smart_contract_state: string | null;
  nonce: number;
  publicKeyBls: string;
  accountType: number;
}

function HomePage() {
  const { connectedAccount, isOnCorrectChain, currentChainId } = useWallet();
  const [accountState, setAccountState] = useState<AccountState | null>(null);
  const [isLoading, setIsLoading] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);

  const fetchAccountState = useCallback(async () => {
    if (!connectedAccount) {
      setAccountState(null);
      setError("Please connect your wallet.");
      return;
    }
    setIsLoading(true);
    setError(null);

    try {
      const client = createPublicClient({
        chain: chain991,
        transport: http(chain991.rpcUrls.default.http[0]),
      });
      const state = (await client.request({
        method: "mtn_getAccountState" as any,
        params: [connectedAccount as `0x${string}`, "latest"],
      })) as AccountState;

      if (state) {
        setAccountState(state);
      } else {
        setError("No account state data received from the backend.");
        setAccountState(null);
      }
    } catch (err: any) {
      console.error("Error fetching account state:", err);
      let errorMessage = err.message || "An unknown error occurred.";
      if (err.shortMessage) errorMessage = err.shortMessage;

      setError(`Failed to fetch account state: ${errorMessage}`);
      setAccountState(null);
    } finally {
      setIsLoading(false);
    }
  }, [connectedAccount, isOnCorrectChain, currentChainId]);

  useEffect(() => {
    if (connectedAccount) {
      fetchAccountState();
    } else {
      setAccountState(null);
      setError("Please connect your wallet.");
    }
  }, [connectedAccount, fetchAccountState]);

  const formatKey = (key: string) => {
    const result = key.replace(/([A-Z])/g, " $1").replace(/_/g, " ");
    return result.charAt(0).toUpperCase() + result.slice(1);
  };

  const getAccountTypeLabel = (type: number) => {
    switch (type) {
      case 0:
        return "Regular Account";
      case 1:
        return "Read Write Strict";
      default:
        return `Type ${type}`;
    }
  };

  const getIconForKey = (key: string) => {
    if (key.toLowerCase().includes("address"))
      return <Wallet className="h-4 w-4" />;
    if (key.toLowerCase().includes("balance"))
      return <Database className="h-4 w-4" />;
    if (key.toLowerCase().includes("hash")) return <Hash className="h-4 w-4" />;
    if (key.toLowerCase().includes("key")) return <Key className="h-4 w-4" />;
    return null;
  };

  return (
    <PageContainer maxWidth="full">
      <PageCard
        title="Account State"
        description={
          connectedAccount
            ? `Wallet: ${connectedAccount}`
            : "Please connect your wallet to view account details"
        }
        icon={Database}
        isConnected={!!connectedAccount}
        colorScheme="sky"
      >
        {!connectedAccount ? (
          <Alert variant="warning">
            <AlertTitle>Wallet Not Connected</AlertTitle>
            <AlertDescription>
              Please connect your wallet via the header to view account state.
            </AlertDescription>
          </Alert>
        ) : (
          <>
            <div className="flex justify-center">
              <Button
                onClick={fetchAccountState}
                disabled={isLoading}
                size="lg"
                className="gap-2"
              >
                <RefreshCw
                  className={`h-4 w-4 ${isLoading ? "animate-spin" : ""}`}
                />
                {isLoading ? "Refreshing..." : "Refresh Account State"}
              </Button>
            </div>

            <StatusDisplay status={undefined} error={error || undefined} />
            {accountState && !error && (
              <div className="space-y-4">
                <div className="flex items-center gap-2 border-b border-neutral-300 dark:border-neutral-700 pb-2">
                  <Database className="h-5 w-5 text-sky-600 dark:text-sky-400" />
                  <h2 className="text-xl font-semibold text-sky-700 dark:text-sky-300">
                    Account Details
                  </h2>
                </div>

                <div className="grid gap-3">
                  {Object.entries(accountState).map(([key, value]) => (
                    <div
                      key={key}
                      className="flex flex-col sm:flex-row sm:items-center gap-2 p-3 rounded-lg bg-neutral-100 dark:bg-neutral-700/30 hover:bg-neutral-200 dark:hover:bg-neutral-700/50 transition-colors"
                    >
                      <div className="flex items-center gap-2 sm:w-1/3">
                        {getIconForKey(key)}
                        <span className="text-sm font-medium text-neutral-700 dark:text-neutral-300">
                          {formatKey(key)}
                        </span>
                      </div>
                      <div className="sm:w-2/3 flex items-center gap-2">
                        {key === "accountType" ? (
                          <Badge
                            variant={value === 1 ? "warning" : "secondary"}
                          >
                            {getAccountTypeLabel(value as number)}
                          </Badge>
                        ) : (
                          <code className="text-sm text-sky-700 dark:text-sky-200 font-mono break-all">
                            {value === null ? "N/A" : String(value)}
                          </code>
                        )}
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            )}
            {isLoading && !accountState && !error && (
              <Alert variant="info">
                <AlertTitle>Loading</AlertTitle>
                <AlertDescription>Fetching account state...</AlertDescription>
              </Alert>
            )}

            {!isLoading && !accountState && !error && (
              <Alert variant="default">
                <AlertTitle>No Data</AlertTitle>
                <AlertDescription>
                  Click "Refresh Account State" to load details.
                </AlertDescription>
              </Alert>
            )}
          </>
        )}
      </PageCard>
      <footer className="text-center text-xs text-neutral-500 dark:text-neutral-600 py-4">
        <p>Account state information retrieved via custom RPC method.</p>
      </footer>
    </PageContainer>
  );
}

export default HomePage;
