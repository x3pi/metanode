import { useState, useEffect } from "react";
import { useWallet } from "~/contexts/WalletContext";
import { isAddress, encodeFunctionData, TransactionExecutionError } from "viem";
import { chain991 } from "~/constants/customChain";
import { Button } from "~/components/ui/button";
import { Label } from "~/components/ui/label";
import { Settings, Loader2, CheckCircle2, ChevronDown } from "lucide-react";
import { PageContainer } from "~/components/PageContainer";
import { PageCard } from "~/components/PageCard";
import { StatusDisplay } from "~/components/StatusDisplay";
import { Alert, AlertDescription, AlertTitle } from "~/components/ui/alert";

const AccountManagerAbi = [
  {
    inputs: [
      {
        internalType: "uint8",
        name: "accountType",
        type: "uint8",
      },
    ],
    name: "setAccountType",
    outputs: [
      {
        internalType: "bool",
        name: "",
        type: "bool",
      },
    ],
    stateMutability: "nonpayable",
    type: "function",
  },
  {
    inputs: [],
    name: "blsPublicKey",
    outputs: [
      {
        internalType: "bytes",
        name: "",
        type: "bytes",
      },
    ],
    stateMutability: "view",
    type: "function",
  },
] as const;

const PREDEFINED_CONTRACT_ADDRESS: `0x${string}` =
  "0x00000000000000000000000000000000D844bb55";

function AccountTypeManagerPage() {
  const {
    walletClient,
    publicClient,
    connectedAccount,
    isOnCorrectChain,
    error: walletError,
    status: walletStatus,
    clearError: clearWalletError,
    setStatusMessage: setWalletStatusMessage,
  } = useWallet();

  const [accountTypeInput, setAccountTypeInput] = useState<string>("0");
  const [pageStatus, setPageStatus] = useState<string>("");
  const [pageError, setPageError] = useState<string>("");
  const [isProcessing, setIsProcessing] = useState<boolean>(false);

  useEffect(() => {
    if (walletError) {
      setPageError("");
      setPageStatus("");
    }
    if (walletStatus && walletStatus.includes("disconnected")) {
      setPageError("");
      setPageStatus("");
    }
  }, [walletError, walletStatus]);

  useEffect(() => {
    if (
      !PREDEFINED_CONTRACT_ADDRESS ||
      !isAddress(PREDEFINED_CONTRACT_ADDRESS)
    ) {
      setPageError(
        "Configuration Error: Invalid predefined contract address. Please update the code."
      );
    }
  }, []);

  const handleSetAccountType = async () => {
    clearWalletError();
    setWalletStatusMessage(null);
    setPageError("");
    setPageStatus("");

    if (!isAddress(PREDEFINED_CONTRACT_ADDRESS)) {
      setPageError("Configuration Error: Invalid predefined contract address.");
      return;
    }
    if (!walletClient || !publicClient || !connectedAccount) {
      setPageError(
        "Please connect your wallet and ensure you are on the correct network via the header."
      );
      return;
    }
    if (!isOnCorrectChain) {
      setPageError(
        `Please switch to the "${chain991.name}" network via the header to perform this transaction.`
      );
      return;
    }

    const accountTypeNum = parseInt(accountTypeInput, 10);
    if (
      accountTypeInput === "" ||
      isNaN(accountTypeNum) ||
      (accountTypeNum !== 0 && accountTypeNum !== 1)
    ) {
      setPageError("Please select a valid Account Type (0 or 1).");
      return;
    }

    setIsProcessing(true);
    setPageStatus("Preparing transaction...");

    try {
      const finalContractAddress = PREDEFINED_CONTRACT_ADDRESS;
      const finalConnectedAccount = connectedAccount as `0x${string}`;

      setPageStatus("Encoding function data...");
      const callData = encodeFunctionData({
        abi: AccountManagerAbi,
        functionName: "setAccountType",
        args: [accountTypeNum],
      });

      setPageStatus("Fetching nonce...");
      const nonce = await publicClient.getTransactionCount({
        address: finalConnectedAccount,
        blockTag: "pending",
      });

      setPageStatus("Fetching gas price (legacy)...");
      const gasPrice = await publicClient.getGasPrice();
      if (gasPrice === undefined || gasPrice === null) {
        throw new Error("Could not retrieve legacy gas price.");
      }

      setPageStatus("Estimating gas limit...");
      const gasLimit = await publicClient.estimateGas({
        account: finalConnectedAccount,
        to: finalContractAddress,
        data: callData,
        value: 0n,
      });

      const transactionRequest = {
        account: finalConnectedAccount,
        to: finalContractAddress,
        data: callData,
        value: 0n,
        nonce: nonce,
        gas: gasLimit,
        gasPrice: gasPrice,
        chain: chain991,
      };

      setPageStatus("Requesting wallet to sign and send transaction...");
      const hash = await walletClient.sendTransaction(transactionRequest);

      setPageStatus(
        `Transaction sent! Hash: ${hash}. Awaiting confirmation...`
      );
      const receipt = await publicClient.waitForTransactionReceipt({ hash });

      if (receipt.status === "success") {
        setPageStatus(
          `Transaction successful! Account Type updated to ${accountTypeNum}. Block: ${receipt.blockNumber.toString()}`
        );
      } else {
        setPageError(`Transaction failed. Status: ${receipt.status}.`);
        setPageStatus("");
      }
    } catch (err: unknown) {
      console.error(
        "Detailed error sending transaction:",
        JSON.stringify(err, Object.getOwnPropertyNames(err as object), 2)
      );
      let errorMessage = "Unknown error";

      if (err instanceof TransactionExecutionError) {
        errorMessage = `Transaction Execution Error: ${
          err.shortMessage || err.message
        }`;
      } else if (err instanceof Error) {
        if (
          err.message.toLowerCase().includes("user rejected") ||
          (err as { code?: number }).code === 4001
        ) {
          errorMessage = "User rejected the transaction.";
        } else if (err.message.toLowerCase().includes("insufficient funds")) {
          errorMessage = "Insufficient funds to perform the transaction.";
        } else if (err.message.toLowerCase().includes("nonce too low")) {
          errorMessage = "Nonce Error: Nonce too low.";
        } else {
          errorMessage = `Error: ${err.message}`;
        }
      }
      setPageError(errorMessage);
      setPageStatus("");
    } finally {
      setIsProcessing(false);
    }
  };

  const accountTypes = [
    {
      value: "0",
      label: "Regular Account",
      description: "Standard account with normal permissions",
    },
    {
      value: "1",
      label: "Read Write Strict Account",
      description: "Account with strict read/write controls",
    },
  ];

  return (
    <PageContainer maxWidth="full">
      <PageCard
        title="Set Account Type"
        description={`Select the account type for your address on the ${chain991.name} chain.`}
        icon={Settings}
        isConnected={!!(connectedAccount && isOnCorrectChain)}
        colorScheme="purple"
        contractAddress={connectedAccount && isOnCorrectChain ? PREDEFINED_CONTRACT_ADDRESS : undefined}
      >
            {connectedAccount && isOnCorrectChain ? (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  handleSetAccountType();
                }}
                className="space-y-6"
              >
                <div className="space-y-2">
                  <Label htmlFor="accountType" className="text-purple">
                    Account Type
                  </Label>
                  <div className="relative">
                    <select
                      id="accountType"
                      value={accountTypeInput}
                      onChange={(e) => setAccountTypeInput(e.target.value)}
                      disabled={isProcessing || !isOnCorrectChain}
                      className="w-full h-10 rounded-lg border border-border bg-card px-3 py-2 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-purple-500 focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:cursor-not-allowed disabled:opacity-50 appearance-none cursor-pointer transition-colors"
                    >
                      {accountTypes.map((type) => (
                        <option key={type.value} value={type.value}>
                          {type.label}
                        </option>
                      ))}
                    </select>
                    <ChevronDown className="absolute right-3 top-1/2 -translate-y-1/2 h-4 w-4 text-app-muted pointer-events-none" />
                  </div>
                  <div className="mt-3 p-3 bg-card-secondary rounded-lg">
                    <p className="text-xs text-app-muted">
                      {
                        accountTypes.find((t) => t.value === accountTypeInput)
                          ?.description
                      }
                    </p>
                  </div>
                </div>

                <Button
                  type="submit"
                  disabled={
                    isProcessing ||
                    accountTypeInput === "" ||
                    !isOnCorrectChain ||
                    !isAddress(PREDEFINED_CONTRACT_ADDRESS)
                  }
                  size="lg"
                  className="w-full bg-purple hover:bg-purple-hover text-white hover:text-white font-bold shadow-md hover:shadow-lg dark:bg-purple dark:text-white dark:hover:text-white"
                >
                  {isProcessing ? (
                    <>
                      <Loader2 className="h-4 w-4 animate-spin" />
                      Processing...
                    </>
                  ) : (
                    <>
                      <CheckCircle2 className="h-4 w-4" />
                      Set Account Type
                    </>
                  )}
                </Button>
              </form>
            ) : (
              <Alert variant="warning">
                <AlertTitle>Connection Required</AlertTitle>
                <AlertDescription>
                  {!connectedAccount
                    ? "Please connect your MetaMask wallet via the header to continue."
                    : `Please switch to the ${chain991.name} (ID: ${chain991.id}) network via the header.`}
                </AlertDescription>
              </Alert>
            )}

            <StatusDisplay status={pageStatus || undefined} error={pageError || undefined} />
      </PageCard>

      <footer className="text-center text-xs text-neutral-500 dark:text-neutral-600 py-4">
        <p>
          Interface to interact with the AccountManager smart contract on the{" "}
          {chain991.name} network to set an account type.
        </p>
      </footer>
    </PageContainer>
  );
}

export default AccountTypeManagerPage;
