import { useState, useEffect } from 'react';
import { useWallet } from './contexts/WalletContext';
import {
    isAddress,
    encodeFunctionData,
    TransactionExecutionError
} from 'viem';
import { chain991 } from './customChain';
import './App.css';

const AccountManagerAbi = [
    {
        "inputs": [
            {
                "internalType": "uint8",
                "name": "accountType",
                "type": "uint8"
            }
        ],
        "name": "setAccountType",
        "outputs": [
            {
                "internalType": "bool",
                "name": "",
                "type": "bool"
            }
        ],
        "stateMutability": "nonpayable",
        "type": "function"
    },
    {
        "inputs": [],
        "name": "blsPublicKey",
        "outputs": [
            {
                "internalType": "bytes",
                "name": "",
                "type": "bytes"
            }
        ],
        "stateMutability": "view",
        "type": "function"
    }
] as const;

const LoadingSpinnerIcon = () => (
    <svg className="animate-spin h-5 w-5 text-white" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4"></circle>
        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
    </svg>
);
const PREDEFINED_CONTRACT_ADDRESS: `0x${string}` = '0x00000000000000000000000000000000D844bb55';

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

    const [accountTypeInput, setAccountTypeInput] = useState<string>('0'); // Default to '0'
    const [pageStatus, setPageStatus] = useState<string>('');
    const [pageError, setPageError] = useState<string>('');
    const [isProcessing, setIsProcessing] = useState<boolean>(false);

     useEffect(() => {
         if (walletError) {
            setPageError('');
            setPageStatus('');
        }
        if (walletStatus && walletStatus.includes("disconnected")) {
             setPageError('');
            setPageStatus('');
        }
    }, [walletError, walletStatus]);

     useEffect(() => {
        if (!PREDEFINED_CONTRACT_ADDRESS || !isAddress(PREDEFINED_CONTRACT_ADDRESS)) {
            setPageError("Configuration Error: Invalid predefined contract address. Please update the code.");
        }
    }, []);


    const handleSetAccountType = async () => {
        clearWalletError();
        setWalletStatusMessage(null);
        setPageError('');
        setPageStatus('');

        if (!isAddress(PREDEFINED_CONTRACT_ADDRESS)) {
             setPageError("Configuration Error: Invalid predefined contract address.");
             return;
        }
        if (!walletClient || !publicClient || !connectedAccount) {
            setPageError("Please connect your wallet and ensure you are on the correct network via the header.");
            return;
        }
        if (!isOnCorrectChain) {
            setPageError(`Please switch to the "${chain991.name}" network via the header to perform this transaction.`);
            return;
        }

        const accountTypeNum = parseInt(accountTypeInput, 10);
        if (accountTypeInput === '' || isNaN(accountTypeNum) || (accountTypeNum !== 0 && accountTypeNum !== 1)) {
             setPageError('Please select a valid Account Type (0 or 1).');
             return;
        }

        setIsProcessing(true);
        setPageStatus('Preparing transaction...');

        try {
            const finalContractAddress = PREDEFINED_CONTRACT_ADDRESS;
            const finalConnectedAccount = connectedAccount as `0x${string}`;

            setPageStatus('Encoding function data...');
            const callData = encodeFunctionData({
                abi: AccountManagerAbi,
                functionName: 'setAccountType',
                args: [accountTypeNum],
            });

            setPageStatus('Fetching nonce...');
            const nonce = await publicClient.getTransactionCount({
                address: finalConnectedAccount,
                blockTag: 'pending',
            });

            setPageStatus('Fetching gas price (legacy)...');
            const gasPrice = await publicClient.getGasPrice();
             if (gasPrice === undefined || gasPrice === null) {
                 throw new Error('Could not retrieve legacy gas price.');
             }

            setPageStatus('Estimating gas limit...');
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

            setPageStatus('Requesting wallet to sign and send transaction...');
            const hash = await walletClient.sendTransaction(transactionRequest);

            setPageStatus(`Transaction sent! Hash: ${hash}. Awaiting confirmation...`);
            const receipt = await publicClient.waitForTransactionReceipt({ hash });

            if (receipt.status === 'success') {
                setPageStatus(`Transaction successful! Account Type updated to ${accountTypeNum}. Block: ${receipt.blockNumber.toString()}`);
                // setAccountTypeInput('0'); // Optionally reset to default
            } else {
                setPageError(`Transaction failed. Status: ${receipt.status}.`);
                setPageStatus('');
            }

        } catch (err: any) {
            console.error('Detailed error sending transaction:', JSON.stringify(err, Object.getOwnPropertyNames(err), 2));
            let errorMessage = `Unknown error. (Message: ${err.message || 'N/A'})`;

            if (err instanceof TransactionExecutionError) {
                errorMessage = `Transaction Execution Error: ${err.shortMessage || err.message}`;
            } else if (err.message) {
                 if (err.message.toLowerCase().includes("user rejected") || err.code === 4001) {
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
            setPageStatus('');
        } finally {
            setIsProcessing(false);
        }
    };

    return (
        <div className="min-h-screen flex flex-col items-center justify-center bg-neutral-900 p-4 font-sans text-neutral-100 selection:bg-indigo-500 selection:text-white">
            <div className="bg-neutral-800 shadow-2xl rounded-3xl p-6 md:p-8 w-full max-w-md">
                <header className="text-center mb-6">
                    <h1 className="text-3xl font-medium text-teal-400 tracking-tight">
                    Set Account Type
                    </h1>
                    { connectedAccount && isOnCorrectChain && (
                        <p className="text-sm text-green-400 mt-2">
                            Contract: <span className="font-mono text-xs">{PREDEFINED_CONTRACT_ADDRESS}</span>
                        </p>
                    )}
                     <p className="text-sm text-neutral-400 mt-2">
                         Select the account type for your address on the {chain991.name} chain.
                    </p>
                </header>

                {connectedAccount && isOnCorrectChain ? (
                    <form onSubmit={(e) => { e.preventDefault(); handleSetAccountType(); }} className="space-y-7 mt-2">
                        <div className="relative">
                            <label
                                htmlFor="accountType"
                                className="absolute -top-2.5 left-3 inline-block bg-neutral-800 px-1 text-xs font-medium text-teal-400"
                            >
                                Account Type
                            </label>
                            <select
                                id="accountType"
                                value={accountTypeInput}
                                onChange={(e) => setAccountTypeInput(e.target.value)}
                                disabled={isProcessing || !isOnCorrectChain}
                                className="block w-full px-4 py-3.5 pt-5 bg-neutral-700/60 border border-neutral-600 rounded-xl shadow-sm focus:outline-none focus:ring-2 focus:ring-teal-500 focus:border-teal-500 sm:text-sm text-neutral-100 disabled:bg-neutral-700/40 disabled:cursor-not-allowed appearance-none transition-colors duration-150 ease-in-out"
                            >
                                <option value="0">Regular Account (0)</option>
                                <option value="1">Read Write Strict Account (1)</option>
                            </select>
                            <div className="pointer-events-none absolute inset-y-0 right-0 flex items-center px-2 pt-2 text-neutral-400">
                                <svg className="h-5 w-5" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor" aria-hidden="true">
                                <path fillRule="evenodd" d="M5.23 7.21a.75.75 0 011.06.02L10 10.94l3.71-3.71a.75.75 0 111.06 1.06l-4.25 4.25a.75.75 0 01-1.06 0L5.23 8.29a.75.75 0 01.02-1.06z" clipRule="evenodd" />
                                </svg>
                            </div>
                            <p className="text-xs text-neutral-500 mt-2 ml-1">
                                Select the desired account type.
                            </p>
                        </div>
                        <button
                            type="submit"
                            disabled={isProcessing || accountTypeInput === '' || !isOnCorrectChain || !isAddress(PREDEFINED_CONTRACT_ADDRESS) }
                            className="w-full flex items-center justify-center bg-teal-500 hover:bg-teal-600 text-white font-semibold text-sm py-3.5 px-6 rounded-full shadow-md hover:shadow-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-teal-400 focus-visible:ring-offset-2 focus-visible:ring-offset-neutral-800 transition-all duration-150 ease-in-out transform active:scale-[0.98] disabled:bg-neutral-600 disabled:text-neutral-400 disabled:cursor-not-allowed disabled:shadow-none"
                        >
                            {isProcessing ? <><LoadingSpinnerIcon /><span className="ml-2">Processing...</span></> : 'Set Account Type'}
                        </button>
                    </form>
                ) : (
                    <p className="text-center text-yellow-400 text-sm mt-6">
                        { !connectedAccount ? "Please connect your MetaMask wallet via the header to continue." :
                          !isOnCorrectChain ? `Please switch to the ${chain991.name} (ID: ${chain991.id}) network via the header.` : ""
                        }
                    </p>
                )}

                {pageStatus && (
                    <div className={`mt-6 p-3.5 rounded-xl shadow ${pageStatus.includes("successful") ? 'bg-green-800/40 border border-green-700/60 text-green-300' : 'bg-sky-800/40 border border-sky-700/60 text-sky-300'}`}>
                        <p className={`font-medium text-sm ${pageStatus.includes("successful") ? 'text-green-200' : 'text-sky-200'}`}>Page Status:</p>
                        <p className="text-xs whitespace-pre-wrap break-words leading-relaxed mt-1">{pageStatus}</p>
                    </div>
                )}
                {pageError && (
                     <div className="mt-6 p-3.5 bg-red-800/40 border border-red-700/60 text-red-300 rounded-xl shadow">
                        <p className="font-medium text-sm text-red-200">Page Error:</p>
                        <p className="text-xs break-words leading-relaxed mt-1">{pageError}</p>
                    </div>
                )}
            </div>
             <footer className="text-center text-xs text-neutral-500 mt-10 pb-6 px-4">
                <p>Interface to interact with the AccountManager smart contract on the {chain991.name} network to set an account type.</p>
            </footer>
        </div>
    );
}

export default AccountTypeManagerPage;