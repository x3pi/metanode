import { useState, useEffect } from 'react';
import { useWallet } from './contexts/WalletContext';
import type { Hex } from 'viem'; // Changed to import type
import './App.css'; //

const GO_BACKEND_RPC_URL = window.location.origin;
// const GO_BACKEND_RPC_URL =  "http://localhost:8545";

const LoadingSpinnerIcon = () => (
    <svg className="animate-spin h-5 w-5 text-white" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4"></circle>
        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
    </svg>
);


function MetaMaskSigner() {
    const {
        connectedAccount,
        mainnetWalletClient,
        error: walletError,
        status: walletStatus,
        clearError: clearWalletError,
        setStatusMessage: setWalletStatusMessage,
    } = useWallet();

    const [blsInput, setBlsInput] = useState<string>(''); //
    const [signature, setSignature] = useState<Hex | null>(null); //
    const [pageStatus, setPageStatus] = useState<string>('');
    const [pageError, setPageError] = useState<string>('');
    const [isSigning, setIsSigning] = useState<boolean>(false); //

    useEffect(() => {
        if (walletError) {
            setPageError('');
            setPageStatus('');
            setSignature(null);
        }
        if (walletStatus && walletStatus.includes("disconnected")) {
             setPageError('');
            setPageStatus('');
            setSignature(null);
        }
    }, [walletError, walletStatus]);


    const handleSignAndSend = async () => { //
        clearWalletError();
        setWalletStatusMessage(null);
        setPageError('');
        setPageStatus('');

        if (!mainnetWalletClient || !connectedAccount) {
            setPageError('Please connect your wallet via the header.');
            return;
        }
        if (!blsInput.trim()) {
            setPageError('Please enter BLS Private Key.'); //
            return;
        }
        if (!blsInput.startsWith('0x') || blsInput.length !== 66) { //
            setPageError('Invalid BLS Private Key. Must be a 32-byte hex string with "0x" prefix.'); //
            return;
        }

        setIsSigning(true);
        setPageStatus('Awaiting signature...'); //
        setSignature(null);

        try {
            const currentTimestamp = new Date().toISOString(); //
            const messageToSign = `BLS Data: ${blsInput}\nTimestamp: ${currentTimestamp}`; //
            setPageStatus(`Requesting signature for:\n${messageToSign.substring(0,100)}...`); //

            const signedMessage = await mainnetWalletClient.signMessage({ //
                account: connectedAccount as `0x${string}`, //
                message: messageToSign, //
            });

            setSignature(signedMessage); //
            setPageStatus('Signature successful! Sending to RPC backend...'); //
            await sendToBackend(connectedAccount, blsInput, currentTimestamp, signedMessage); //

        } catch (err: any) {
            console.error('Error signing or sending RPC:', err); //
            setPageError(`Error: ${err.message || 'User rejected signature or an unknown error occurred.'}`); //
            setSignature(null);
            setPageStatus('');
        } finally {
            setIsSigning(false); //
        }
    };

    const sendToBackend = async (ethAddress: string, blsPk: string, timestamp: string, sig: Hex) => { //
        setPageStatus('Sending data to RPC backend...'); //
        const rpcPayload = { //
            jsonrpc: "2.0", //
            method: "rpc_registerBlsKeyWithSignature", //
            params: [{ //
                address: ethAddress, //
                blsPrivateKey: blsPk, //
                timestamp: timestamp, //
                signature: sig, //
            }],
            id: Date.now(), //
        };

        try {
            const response = await fetch(GO_BACKEND_RPC_URL, { //
                method: 'POST', //
                headers: { //
                    'Content-Type': 'application/json', //
                },
                body: JSON.stringify(rpcPayload), //
            });
            const responseData = await response.json(); //

            if (!response.ok || responseData.error) { //
                const errorMsg = responseData.error ? //
                                 `Backend Error: ${responseData.error.message} (Code: ${responseData.error.code})` : //
                                 `HTTP Error: ${response.status} - ${response.statusText}`; //
                throw new Error(errorMsg); //
            }
            setPageStatus(`BLS Key registration successful! Server response: ${JSON.stringify(responseData.result)}`); //
            setPageError('');
        } catch (backendError: any) {
            console.error('Error sending to backend RPC:', backendError); //
            setPageError(`Error sending to backend: ${backendError.message}`); //
            setPageStatus('');
        }
    };

    return (
        <div className="min-h-screen flex flex-col items-center justify-center bg-neutral-900 p-4 font-sans text-neutral-100 selection:bg-indigo-500 selection:text-white"> {/* */}
            <div className="bg-neutral-800 shadow-2xl rounded-3xl p-6 md:p-8 w-full max-w-md"> {/* */}
                <header className="text-center mb-8"> {/* */}
                    <h1 className="text-3xl font-medium text-indigo-400 tracking-tight"> {/* */}
                        Register BLS Key (RPC)
                    </h1>
                    <p className="text-sm text-neutral-400 mt-2"> {/* */}
                        Use MetaMask to sign and send BLS key information to the RPC backend.
                    </p>
                </header>

                {connectedAccount ? (
                    <form onSubmit={(e) => { e.preventDefault(); handleSignAndSend(); }} className="space-y-7 mt-6"> {/* */}
                        <div className="relative"> {/* */}
                            <label
                                htmlFor="blsString"
                                className="absolute -top-2.5 left-3 inline-block bg-neutral-800 px-1 text-xs font-medium text-indigo-400" //
                            >
                                BLS Private Key (Hex)
                            </label>
                            <input
                                type="text"
                                id="blsString"
                                value={blsInput}
                                onChange={(e) => setBlsInput(e.target.value)}
                                placeholder="Example: 0xabcdef1234..." //
                                disabled={isSigning || !connectedAccount}
                                className="block w-full px-4 py-3.5 pt-5 bg-neutral-700/60 border border-neutral-600 rounded-xl shadow-sm placeholder-neutral-500 focus:outline-none focus:ring-2 focus:ring-indigo-500 focus:border-indigo-500 sm:text-sm text-neutral-100 disabled:bg-neutral-700/40 disabled:cursor-not-allowed transition-colors duration-150 ease-in-out" //
                            />
                            <p className="text-xs text-neutral-500 mt-2 ml-1"> {/* */}
                                Enter the 32-byte hex string of the BLS private key (with "0x" prefix).
                            </p>
                        </div>

                        <button
                            type="submit"
                            disabled={isSigning || !blsInput.trim() || !connectedAccount}
                            className="w-full flex items-center justify-center bg-indigo-500 hover:bg-indigo-600 text-white font-semibold text-sm py-3.5 px-6 rounded-full shadow-md hover:shadow-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-indigo-400 focus-visible:ring-offset-2 focus-visible:ring-offset-neutral-800 transition-all duration-150 ease-in-out transform active:scale-[0.98] disabled:bg-neutral-600 disabled:text-neutral-400 disabled:cursor-not-allowed disabled:shadow-none" //
                        >
                            {isSigning ? (
                                <><LoadingSpinnerIcon /> <span className="ml-2">Processing...</span></>
                            ) : (
                                'Sign and Send BLS Key Registration'
                            )}
                        </button>
                    </form>
                ) : (
                     <p className="text-center text-yellow-400 text-sm">Please connect your MetaMask wallet via the header to continue.</p>
                )}

                {pageStatus && ( /* */
                    <div className={`mt-6 p-3.5 rounded-xl shadow ${pageStatus.includes("successful") ? 'bg-green-800/40 border border-green-700/60 text-green-300' : 'bg-sky-800/40 border border-sky-700/60 text-sky-300'}`}> {/* */}
                        <p className={`font-medium text-sm ${pageStatus.includes("successful") ? 'text-green-200' : 'text-sky-200'}`}>Page Status:</p> {/* */}
                        <p className="text-xs whitespace-pre-wrap break-words leading-relaxed mt-1">{pageStatus}</p> {/* */}
                    </div>
                )}
                {pageError && ( /* */
                     <div className="mt-6 p-3.5 bg-red-800/40 border border-red-700/60 text-red-300 rounded-xl shadow"> {/* */}
                        <p className="font-medium text-sm text-red-200">Page Error:</p> {/* */}
                        <p className="text-xs break-words leading-relaxed mt-1">{pageError}</p> {/* */}
                    </div>
                )}
                {signature && !pageError && ( /* */
                    <div className="mt-6 p-3.5 bg-sky-800/40 border border-sky-700/60 text-sky-300 rounded-xl shadow"> {/* */}
                        <p className="font-medium text-sm text-sky-200">Generated Signature (before sending):</p> {/* */}
                        <p className="text-xs break-all whitespace-pre-wrap leading-relaxed mt-1 font-mono">{signature}</p> {/* */}
                    </div>
                )}
            </div>
             <footer className="text-center text-xs text-neutral-500 mt-10 pb-6 px-4"> {/* */}
                <p>Interface to sign and send BLS key registration requests to the RPC backend.</p> {/* */}
                <p className="mt-1">Ensure your Go backend is running and listening on <code className="bg-neutral-700 px-1 rounded">{GO_BACKEND_RPC_URL}</code>.</p> {/* */}
            </footer>
        </div>
    );
}

export default MetaMaskSigner;