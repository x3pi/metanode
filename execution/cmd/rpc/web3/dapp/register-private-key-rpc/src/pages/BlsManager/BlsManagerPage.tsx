import { useState, useEffect } from "react";
import { useWallet } from "~/contexts/WalletContext";
import { isAddress, encodeFunctionData, TransactionExecutionError, hexToString } from "viem";
import type { Hex } from "viem";
import { chain991 } from "~/constants/customChain";
import { contracts } from "~/constants/contracts";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Alert, AlertDescription, AlertTitle } from "~/components/ui/alert";
import { Badge } from "~/components/ui/badge";
import { Key, Loader2, CheckCircle2 } from "lucide-react";

// Sử dụng contract config từ constants
const { abi: AccountManagerAbi, address: PREDEFINED_CONTRACT_ADDRESS } =
  contracts.AccountManager;

function BlsManagerPage() {
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

  const [publicKeyInput, setPublicKeyInput] = useState<string>("");
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

  // Load public key from backend when connected
  useEffect(() => {
    const loadPublicKey = async () => {
      if (!publicClient || !connectedAccount || !isOnCorrectChain) {
        return;
      }

      try {
        const callData = encodeFunctionData({
          abi: AccountManagerAbi,
          functionName: "getPublickeyBls",
          args: [],
        });

        const result = await publicClient.call({
          to: PREDEFINED_CONTRACT_ADDRESS,
          data: callData,
        });
        console.log("result",result)
        if (result.data && result.data !== "0x") {
          const publicKeyHex = result.data as Hex;
          const jsonString = hexToString(publicKeyHex);
          const publicKeyString = JSON.parse(jsonString) as string;
          setPublicKeyInput(publicKeyString);
          console.log("data",publicKeyHex, "parsed:", publicKeyString)
        }
      } catch (err) {
        console.error("Error loading public key:", err);
        // Don't show error to user, just use default value
      }
    };

    loadPublicKey();
  }, [publicClient, connectedAccount, isOnCorrectChain]);

  const handleSetPublicKey = async () => {
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
    if (!publicKeyInput.startsWith("0x") || publicKeyInput.length < 4) {
      setPageError(
        'BLS Public Key must be a valid hex string with an "0x" prefix.'
      );
      return;
    }

    setIsProcessing(true);
    setPageStatus("Preparing transaction...");

    try {
      const finalContractAddress = PREDEFINED_CONTRACT_ADDRESS;
      const finalPublicKeyInput = publicKeyInput?.trim() as Hex;
      const finalConnectedAccount = connectedAccount as `0x${string}`;

      setPageStatus("Encoding function data...");
      const callData = encodeFunctionData({
        abi: AccountManagerAbi,
        functionName: "setBlsPublicKey",
        args: [finalPublicKeyInput],
      });

      setPageStatus("Fetching nonce...");
      const nonce = await publicClient.getTransactionCount({
        address: finalConnectedAccount,
        blockTag: "pending",
      });
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
        gasPrice: 0n,
        chain: chain991,
      };
      console.log("Transaction Request:", transactionRequest);
      setPageStatus("Requesting wallet to sign and send transaction...");
      const hash = await walletClient.sendTransaction(transactionRequest);

      setPageStatus(
        `Transaction sent! Hash: ${hash} confirmed`
      );
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

  return (
    <div className="min-h-screen bg-app w-full p-4 md:p-8">
      <div className="w-full space-y-6">
        <Card className="border-teal/20">
          <CardHeader className="space-y-3">
            <div className="flex items-center justify-between">
              <CardTitle className="text-3xl font-bold text-teal flex items-center gap-2">
                <Key className="h-7 w-7" />
                Create BLS Public Key
              </CardTitle>
              {connectedAccount && isOnCorrectChain && (
                <Badge variant="success">Connected</Badge>
              )}
            </div>
            <CardDescription className="text-base">
              Enter your BLS public key to initialize the wallet on the{" "}
              {chain991.name} chain.
            </CardDescription>
            {connectedAccount && isOnCorrectChain && (
              <div className="pt-2">
                <Label className="text-xs text-app-muted">
                  Contract Address
                </Label>
                <code className="block mt-1 text-xs text-success font-mono bg-card-secondary p-2 rounded">
                  {PREDEFINED_CONTRACT_ADDRESS}
                </code>
              </div>
            )}
          </CardHeader>

          <CardContent className="space-y-6">
            {connectedAccount && isOnCorrectChain ? (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  handleSetPublicKey();
                }}
                className="space-y-6"
              >
                <div className="space-y-2">
                  <Label htmlFor="blsPublicKey" className="text-teal">
                    BLS Public Key (Hex)
                  </Label>
                  <Input
                    type="text"
                    id="blsPublicKey"
                    value={publicKeyInput}
                    onChange={(e) => setPublicKeyInput(e.target.value)}
                    placeholder="0xabcdef1234..."
                    defaultValue="0x86d5de6f7c9c13cc0d959a553cc0e4853ba5faae45a28da9bddc8ef8e104eb5d3dece8dfaa24f11b4243ec27537e3184"
                    disabled={isProcessing || !isOnCorrectChain}
                    className="font-mono"
                  />
                  <p className="text-xs text-app-muted">
                    Enter the BLS public key as a hex string with "0x" prefix
                  </p>
                </div>

                <Button
                  type="submit"
                  disabled={
                    isProcessing ||
                    !publicKeyInput.trim() ||
                    !isOnCorrectChain ||
                    !isAddress(PREDEFINED_CONTRACT_ADDRESS)
                  }
                  size="lg"
                  className="w-full bg-teal hover:bg-teal-hover text-white hover:text-white font-bold shadow-md hover:shadow-lg dark:bg-teal dark:text-white dark:hover:text-white"
                >
                  {isProcessing ? (
                    <>
                      <Loader2 className="h-4 w-4 animate-spin" />
                      Processing...
                    </>
                  ) : (
                    <>
                      <CheckCircle2 className="h-4 w-4" />
                      Set BLS Key
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

            {pageStatus && (
              <Alert
                variant={pageStatus.includes("successful") ? "success" : "info"}
              >
                <AlertTitle>
                  {pageStatus.includes("successful") ? "Success" : "Status"}
                </AlertTitle>
                <AlertDescription className="text-xs whitespace-pre-wrap wrap-break-word">
                  {pageStatus}
                </AlertDescription>
              </Alert>
            )}

            {pageError && (
              <Alert variant="destructive">
                <AlertTitle>Error</AlertTitle>
                <AlertDescription className="text-xs wrap-break-word">
                  {pageError}
                </AlertDescription>
              </Alert>
            )}
          </CardContent>
        </Card>

        <footer className="text-center text-xs text-app-muted py-4">
          <p>
            Interface to interact with the AccountManager smart contract on the{" "}
            {chain991.name} network.
          </p>
        </footer>
      </div>
    </div>
  );
}

export default BlsManagerPage;
