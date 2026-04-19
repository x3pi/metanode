// src/HomePage.tsx
import  { useState, useEffect, useCallback } from 'react';
import { useWallet } from './contexts/WalletContext';
import { chain991 } from './customChain';
import { createPublicClient, http } from 'viem';

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

const LoadingSpinnerIcon = () => (
    <svg className="animate-spin h-5 w-5 text-white" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4"></circle>
        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
    </svg>
);




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
              method: 'mtn_getAccountState' as any,
              params:  [connectedAccount as `0x${string}`, 'latest'],
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
        // Convert camelCase or snake_case to Title Case
        const result = key.replace(/([A-Z])/g, " $1").replace(/_/g, " ");
        return result.charAt(0).toUpperCase() + result.slice(1);
    };

    return (
        <div className="min-h-screen flex flex-col items-center justify-start bg-neutral-900 p-4 font-sans text-neutral-100 selection:bg-indigo-500 selection:text-white">
            <div className="bg-neutral-800 shadow-2xl rounded-3xl p-6 md:p-8 w-full max-w-2xl mt-8">
                <header className="text-center mb-6">
                    <h1 className="text-3xl font-medium text-sky-400 tracking-tight">
                        Account State
                    </h1>
                    <p className="text-sm text-neutral-400 mt-2">
                        Viewing account details for address: <span className="font-mono">{connectedAccount || "N/A"}</span>
                    </p>
           
                </header>

                {!connectedAccount ? (
                     <p className="text-center text-yellow-400 text-sm mt-6">
                        Please connect your wallet via the header to view account state.
                    </p>
                ) : (
                    <>
                        <div className="flex justify-center mb-6">
                            <button
                                onClick={fetchAccountState}
                                disabled={isLoading}
                                className="px-6 py-2 bg-sky-500 hover:bg-sky-600 text-white font-semibold rounded-lg shadow-md transition-colors duration-150 ease-in-out disabled:opacity-50 disabled:cursor-not-allowed flex items-center"
                            >
                                {isLoading && <LoadingSpinnerIcon />}
                                <span className={isLoading ? 'ml-2' : ''}>Refresh Account State</span>
                            </button>
                        </div>

                        {error && (
                            <div className="mt-6 p-3.5 bg-red-800/40 border border-red-700/60 text-red-300 rounded-xl shadow">
                                <p className="font-medium text-sm text-red-200">Error:</p>
                                <p className="text-xs break-words leading-relaxed mt-1">{error}</p>
                            </div>
                        )}

                        {accountState && !error && (
                            <div className="mt-4 bg-neutral-700/60 p-6 rounded-lg shadow-md">
                                <h2 className="text-xl font-semibold text-sky-300 mb-4 border-b border-neutral-600 pb-2">Account Details</h2>
                                <dl className="space-y-3">
                                    {Object.entries(accountState).map(([key, value]) => (
                                        <div key={key} className="grid grid-cols-3 gap-x-4 py-2 border-b border-neutral-700/50 last:border-b-0">
                                            <dt className="text-sm font-medium text-neutral-300 col-span-1 truncate">{formatKey(key)}</dt>
                                            <dd className="text-sm text-sky-200 col-span-2 font-mono break-all">
                                                {value === null ? 'N/A' : String(value)}
                                            </dd>
                                        </div>
                                    ))}
                                </dl>
                            </div>
                        )}
                        {isLoading && !accountState && !error && connectedAccount && (
                             <div className="text-center text-sky-300 mt-6">
                                <LoadingSpinnerIcon />
                                <p className="mt-2">Loading account state...</p>
                            </div>
                        )}
                         {!isLoading && !accountState && !error && connectedAccount && (
                            <p className="text-center text-neutral-400 mt-6">
                                Click "Refresh Account State" to load details or if data is missing.
                            </p>
                        )}
                    </>
                )}
            </div>
            <footer className="text-center text-xs text-neutral-500 mt-10 pb-6 px-4">
                <p>Account state information retrieved via custom RPC method.</p>
            </footer>
        </div>
    );
}

export default HomePage;