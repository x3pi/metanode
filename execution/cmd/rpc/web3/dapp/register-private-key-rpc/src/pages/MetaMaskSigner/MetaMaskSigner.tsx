import { useState, useEffect } from "react";
import { useWallet } from "~/contexts/WalletContext";
import type { Hex } from "viem";
import { Button } from "~/components/ui/button";
import { Alert, AlertDescription, AlertTitle } from "~/components/ui/alert";
import { FileKey, Loader2, Send } from "lucide-react";
import { PageContainer } from "~/components/PageContainer";
import { PageCard } from "~/components/PageCard";
import { FormField } from "~/components/FormField";
import { StatusDisplay } from "~/components/StatusDisplay";
import { GO_BACKEND_RPC_URL } from "~/constants/customChain";


function MetaMaskSigner() {
  const {
    connectedAccount,
    mainnetWalletClient,
    error: walletError,
    status: walletStatus,
    clearError: clearWalletError,
    setStatusMessage: setWalletStatusMessage,
  } = useWallet();
  const [blsInput, setBlsInput] = useState<string>("");
  const [signature, setSignature] = useState<Hex | null>(null);
  const [pageStatus, setPageStatus] = useState<string>("");
  const [pageError, setPageError] = useState<string>("");
  const [isSigning, setIsSigning] = useState<boolean>(false);

  useEffect(() => {
    if (walletError) {
      setPageError("");
      setPageStatus("");
      setSignature(null);
    }
    if (walletStatus && walletStatus.includes("disconnected")) {
      setPageError("");
      setPageStatus("");
      setSignature(null);
    }
  }, [walletError, walletStatus]);

  const handleSignAndSend = async () => {
    clearWalletError();
    setWalletStatusMessage(null);
    setPageError("");
    setPageStatus("");

    if (!mainnetWalletClient || !connectedAccount) {
      setPageError("Please connect your wallet via the header.");
      return;
    }
    if (!blsInput.trim()) {
      setPageError("Please enter BLS Private Key.");
      return;
    }
    if (!blsInput.startsWith("0x") || blsInput.length !== 66) {
      setPageError(
        'Invalid BLS Private Key. Must be a 32-byte hex string with "0x" prefix.'
      );
      return;
    }

    setIsSigning(true);
    setPageStatus("Awaiting signature...");
    setSignature(null);

    try {
      const currentTimestamp = new Date().toISOString();
      const messageToSign = `BLS Data: ${blsInput}\nTimestamp: ${currentTimestamp}`;
      setPageStatus(
        `Requesting signature for:\n${messageToSign.substring(0, 100)}...`
      );

      const signedMessage = await mainnetWalletClient.signMessage({
        account: connectedAccount as `0x${string}`,
        message: messageToSign,
      });

      setSignature(signedMessage);
      setPageStatus("Signature successful! Sending to RPC backend...");
      await sendToBackend(
        connectedAccount,
        blsInput,
        currentTimestamp,
        signedMessage
      );
    } catch (err: unknown) {
      console.error("Error signing or sending RPC:", err);
      const message =
        err instanceof Error
          ? err.message
          : "User rejected signature or an unknown error occurred.";
      setPageError(`Error: ${message}`);
      setSignature(null);
      setPageStatus("");
    } finally {
      setIsSigning(false);
    }
  };

  const sendToBackend = async (
    ethAddress: string,
    blsPk: string,
    timestamp: string,
    sig: Hex
  ) => {
    setPageStatus("Sending data to RPC backend...");
    const rpcPayload = {
      jsonrpc: "2.0",
      method: "rpc_registerBlsKeyWithSignature",
      params: [
        {
          address: ethAddress,
          blsPrivateKey: blsPk,
          timestamp: timestamp,
          signature: sig,
        },
      ],
      id: Date.now(),
    };

    try {
      const response = await fetch(GO_BACKEND_RPC_URL, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify(rpcPayload),
      });
      const responseData = await response.json();

      if (!response.ok || responseData.error) {
        const errorMsg = responseData.error
          ? `Backend Error: ${responseData.error.message} (Code: ${responseData.error.code})`
          : `HTTP Error: ${response.status} - ${response.statusText}`;
        throw new Error(errorMsg);
      }
      setPageStatus(
        `BLS Key registration successful! Server response: ${JSON.stringify(
          responseData.result
        )}`
      );
      setPageError("");
    } catch (backendError: unknown) {
      console.error("Error sending to backend RPC:", backendError);
      const message =
        backendError instanceof Error
          ? backendError.message
          : "Unknown backend error";
      setPageError(`Error sending to backend: ${message}`);
      setPageStatus("");
    }
  };

  return (
    <PageContainer maxWidth="full">
      <PageCard
        title="Register BLS Key (RPC)"
        description="Use MetaMask to sign and send BLS key information to the RPC backend."
        icon={FileKey}
        isConnected={!!connectedAccount}
        colorScheme="indigo"
      >
        {connectedAccount ? (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              handleSignAndSend();
            }}
            className="space-y-6"
          >
            <FormField
              id="blsString"
              label="BLS Private Key (Hex)"
              value={blsInput}
              onChange={setBlsInput}
              placeholder="0xabcdef1234..."
              disabled={isSigning || !connectedAccount}
              helpText='Enter the 32-byte hex string of the BLS private key (with "0x" prefix)'
              inputClassName="font-mono"
            />

            <Button
              type="submit"
              disabled={isSigning || !blsInput.trim() || !connectedAccount}
              size="lg"
              className="w-full bg-indigo hover:bg-indigo-hover text-white hover:text-white font-bold shadow-md hover:shadow-lg dark:bg-indigo dark:text-white dark:hover:text-white"
            >
              {isSigning ? (
                <>
                  <Loader2 className="h-4 w-4 animate-spin" />
                  Processing...
                </>
              ) : (
                <>
                  <Send className="h-4 w-4" />
                  Sign and Send BLS Key Registration
                </>
              )}
            </Button>
          </form>
        ) : (
          <Alert variant="warning">
            <AlertTitle>Wallet Not Connected</AlertTitle>
            <AlertDescription>
              Please connect your MetaMask wallet via the header to continue.
            </AlertDescription>
          </Alert>
        )}

        <StatusDisplay
          status={pageStatus || undefined}
          error={pageError || undefined}
        />

        {signature && !pageError && (
          <Alert variant="info">
            <AlertTitle>Generated Signature</AlertTitle>
            <AlertDescription className="text-xs break-all whitespace-pre-wrap font-mono">
              {signature}
            </AlertDescription>
          </Alert>
        )}
      </PageCard>

      <footer className="text-center text-xs text-neutral-500 dark:text-neutral-600 py-4">
        <p>
          Interface to sign and send BLS key registration requests to the RPC
          backend.
        </p>
        <p className="mt-1">
          Ensure your Go backend is running and listening on{" "}
          <code className="bg-neutral-200 dark:bg-neutral-700 px-1 rounded">
            {GO_BACKEND_RPC_URL}
          </code>
          .
        </p>
      </footer>
    </PageContainer>
  );
}

export default MetaMaskSigner;
