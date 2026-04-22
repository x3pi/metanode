// src/contexts/WalletContext.tsx
import React, { createContext, useState, useEffect, useCallback } from 'react';
import type { ReactNode } from 'react';
import { createWalletClient, custom, isAddress, createPublicClient, http } from 'viem';
import type { WalletClient, PublicClient } from 'viem';
import { mainnet } from 'viem/chains';
import { chain991 } from '../constants/customChain'; //

type WalletContextType = {
    walletClient: WalletClient | null;
    publicClient: PublicClient | null;
    mainnetWalletClient: WalletClient | null;
    connectedAccount: string;
    isConnecting: boolean;
    isOnCorrectChain: boolean;
    currentChainId: number | null;
    connectWallet: () => Promise<void>;
    disconnectWallet: () => void;
    switchNetwork: (chainId: number) => Promise<void>;
    error: string | null;
    clearError: () => void;
    status: string | null;
    setStatusMessage: (message: string | null) => void;
};

export const WalletContext = createContext<WalletContextType | undefined>(undefined);

interface WalletProviderProps {
    children: ReactNode;
}

export const WalletProvider: React.FC<WalletProviderProps> = ({ children }) => {
    const [walletClient, setWalletClient] = useState<WalletClient | null>(null);
    const [publicClient, setPublicClient] = useState<PublicClient | null>(null);
    const [mainnetWalletClient, setMainnetWalletClient] = useState<WalletClient | null>(null);
    const [connectedAccount, setConnectedAccount] = useState<string>('');
    const [isConnecting, setIsConnecting] = useState<boolean>(false);
    const [isOnCorrectChain, setIsOnCorrectChain] = useState<boolean>(false);
    const [currentChainId, setCurrentChainId] = useState<number | null>(null);
    const [error, setError] = useState<string | null>(null);
    const [status, setStatus] = useState<string | null>(null);

    const [userDisconnectedFromApp, setUserDisconnectedFromApp] = useState<boolean>(() => {
        // Khởi tạo cờ từ sessionStorage khi component mount
        return sessionStorage.getItem('userDisconnectedFromApp') === 'true';
    });

    const clearError = () => setError(null);
    const setStatusMessage = (message: string | null) => setStatus(message);

    const setupClientsForChain = useCallback(async (account: `0x${string}`, chainId: number) => {
        console.log("WalletContext: setupClientsForChain called for account", account, "chainId", chainId);
        try {
            const newMainnetWalletClient = createWalletClient({
                chain: mainnet,
                transport: custom(window.ethereum!),
                account: account,
            });
            setMainnetWalletClient(newMainnetWalletClient);

            if (chainId === chain991.id) { //
                const newWalletClientForChain991 = createWalletClient({
                    chain: chain991, //
                    transport: custom(window.ethereum!),
                    account: account,
                });
                setWalletClient(newWalletClientForChain991);

                const newPublicClient = createPublicClient({
                    chain: chain991, //
                    transport: http(chain991.rpcUrls.default.http[0]), //
                });
                setPublicClient(newPublicClient);
                setIsOnCorrectChain(true);
            } else {
                 const currentActiveWalletClient = createWalletClient({
                    chain: {id: chainId, name: "Current Network", rpcUrls: {default: {http:[""]}}, nativeCurrency: {name: "ETH", symbol:"ETH", decimals:18}},
                    transport: custom(window.ethereum!),
                    account: account,
                });
                setWalletClient(currentActiveWalletClient);
                setPublicClient(null);
                setIsOnCorrectChain(false);
            }
            setError(null);
        } catch (e: any) {
            console.error("WalletContext: Error setting up clients:", e);
            setError(`Error setting up clients: ${e.message}`);
            setWalletClient(null);
            setPublicClient(null);
            setMainnetWalletClient(null);
            setIsOnCorrectChain(false);
        }
    }, []);


    const handleAccountsChanged = useCallback(async (accounts: string[]) => {
        console.log("WalletContext: accountsChanged event", accounts);
        if (accounts.length === 0) {
            setConnectedAccount('');
            setWalletClient(null);
            setPublicClient(null);
            setMainnetWalletClient(null);
            setIsOnCorrectChain(false);
            setCurrentChainId(null);
            setStatus('Wallet disconnected via MetaMask or locked.');
            setError(null);
            setUserDisconnectedFromApp(true);
            sessionStorage.setItem('userDisconnectedFromApp', 'true');
        } else if (isAddress(accounts[0])) {
            const newAccount = accounts[0] as `0x${string}`;
            setConnectedAccount(newAccount);
            setUserDisconnectedFromApp(false);
            sessionStorage.removeItem('userDisconnectedFromApp');
            const chainIdHex = await window.ethereum.request({ method: 'eth_chainId' });
            const newChainId = parseInt(chainIdHex, 16);
            setCurrentChainId(newChainId);
            await setupClientsForChain(newAccount, newChainId);
        } else {
            console.warn("WalletContext: Invalid account received from MetaMask", accounts[0]);
            setError('Invalid MetaMask account received.');
            setConnectedAccount('');
            setWalletClient(null);
            setPublicClient(null);
            setMainnetWalletClient(null);
        }
    }, [setupClientsForChain]);

    const handleChainChanged = useCallback(async (newChainIdHex: string) => {
        console.log("WalletContext: chainChanged event", newChainIdHex);
        if (window.ethereum && connectedAccount && isAddress(connectedAccount)) {
            // Nếu người dùng thay đổi mạng khi đã kết nối, không nên tự động đặt userDisconnectedFromApp = false
            // vì họ có thể đã ngắt kết nối từ app trước đó và chỉ đang đổi mạng trên MetaMask.
            // Việc kết nối lại chỉ nên xảy ra khi người dùng chủ động Connect.
            const newChainId = parseInt(newChainIdHex, 16);
            setCurrentChainId(newChainId);
            setStatus(`MetaMask network changed. Updating...`);
            // Chỉ gọi setupClientsForChain nếu userDisconnectedFromApp là false
            // hoặc có thể luôn gọi để cập nhật client với chain mới, nhưng giao diện vẫn tôn trọng userDisconnectedFromApp
            if (!userDisconnectedFromApp) {
                await setupClientsForChain(connectedAccount as `0x${string}`, newChainId);
            } else {
                // Nếu user đã disconnect từ app, chỉ cập nhật chainId và isCorrectChain
                setIsOnCorrectChain(newChainId === chain991.id); //
                // Các client vẫn nên là null
                setWalletClient(null);
                setPublicClient(null);
                // mainnetWalletClient có thể giữ lại nếu logic cho phép dùng nó độc lập
            }

        } else {
            console.log("WalletContext: chainChanged ignored, no connected account or invalid account.");
            setCurrentChainId(parseInt(newChainIdHex, 16));
            setIsOnCorrectChain(false);
            setPublicClient(null);
        }
    }, [connectedAccount, setupClientsForChain, userDisconnectedFromApp]);


    useEffect(() => {
        if (typeof window.ethereum !== 'undefined') {
            console.log("WalletContext: Attaching MetaMask event listeners.");
            const init = async () => {
                const disconnectedFlag = sessionStorage.getItem('userDisconnectedFromApp') === 'true';
                setUserDisconnectedFromApp(disconnectedFlag); // Đồng bộ state với sessionStorage

                if (disconnectedFlag) {
                    console.log("WalletContext: init() skipped due to user manual disconnect from app flag in session.");
                    setConnectedAccount('');
                    setWalletClient(null);
                    setPublicClient(null);
                    setMainnetWalletClient(null);
                    setIsOnCorrectChain(false);
                    setCurrentChainId(null); // Đảm bảo chainId cũng được reset
                    // setStatus('Ready to connect.'); // Hoặc không set gì cả
                    return;
                }

                try {
                    const accounts = await window.ethereum.request({ method: 'eth_accounts' }) as string[];
                    if (accounts && accounts.length > 0 && isAddress(accounts[0])) {
                        const initialAccount = accounts[0] as `0x${string}`;
                        setConnectedAccount(initialAccount);
                        // setUserDisconnectedFromApp(false); // Đã được xử lý ở trên
                        // sessionStorage.removeItem('userDisconnectedFromApp'); // Đã được xử lý ở trên
                        const chainIdHex = await window.ethereum.request({ method: 'eth_chainId' });
                        const initialChainId = parseInt(chainIdHex, 16);
                        setCurrentChainId(initialChainId);
                        await setupClientsForChain(initialAccount, initialChainId);
                    } else {
                        console.log("WalletContext: No accounts found initially connected.");
                         // Nếu không có tài khoản và cũng không có cờ disconnect, đảm bảo trạng thái là chưa kết nối
                        setConnectedAccount('');
                        setCurrentChainId(null);
                        setIsOnCorrectChain(false);
                        setWalletClient(null);
                        setPublicClient(null);
                        setMainnetWalletClient(null);
                    }
                } catch (e: any) {
                     console.error("WalletContext: Error during auto-connection attempt:", e);
                     setError("Could not auto-connect wallet. Please connect manually.");
                }
            };
            init();

            window.ethereum.on('accountsChanged', handleAccountsChanged);
            window.ethereum.on('chainChanged', handleChainChanged);

            return () => {
                console.log("WalletContext: Removing MetaMask event listeners.");
                if (window.ethereum.removeListener) {
                    window.ethereum.removeListener('accountsChanged', handleAccountsChanged);
                    window.ethereum.removeListener('chainChanged', handleChainChanged);
                }
            };
        } else {
            console.log("WalletContext: MetaMask (window.ethereum) not found.");
        }
    // Không nên thêm setupClientsForChain vào đây nếu nó không thay đổi
    }, [handleAccountsChanged, handleChainChanged]); // Bỏ userDisconnectedFromApp vì nó được quản lý bởi init


    const connectWallet = async () => {
        console.log("WalletContext: connectWallet called");
        setUserDisconnectedFromApp(false);
        sessionStorage.removeItem('userDisconnectedFromApp');

        if (typeof window.ethereum === 'undefined') {
            setError('MetaMask is not installed.');
            return;
        }
        setIsConnecting(true);
        setError(null);
        setStatus("Attempting to connect wallet...");
        try {
            const accounts = await window.ethereum.request({ method: 'eth_requestAccounts' }) as string[];
            if (accounts && accounts.length > 0 && isAddress(accounts[0])) {
                const newAccount = accounts[0] as `0x${string}`;
                setConnectedAccount(newAccount); // Sẽ trigger useEffect nếu account thay đổi
                const chainIdHex = await window.ethereum.request({ method: 'eth_chainId' });
                const newChainId = parseInt(chainIdHex, 16);
                setCurrentChainId(newChainId);
                await setupClientsForChain(newAccount, newChainId);
            } else {
                setError('No valid accounts found. Please check MetaMask.');
                setConnectedAccount('');
                setStatus(null);
            }
        } catch (e: any) {
            console.error('WalletContext: Error connecting wallet:', e);
            if (e.code === 4001) {
                setError('Connection request rejected by user.');
            } else {
                setError(`Failed to connect wallet: ${e.message || 'Unknown error.'}`);
            }
            setConnectedAccount('');
            setStatus(null);
        } finally {
            setIsConnecting(false);
        }
    };

    const disconnectWallet = () => {
        console.log("WalletContext: disconnectWallet called");
        setUserDisconnectedFromApp(true);
        sessionStorage.setItem('userDisconnectedFromApp', 'true');

        setConnectedAccount('');
        setWalletClient(null);
        setPublicClient(null);
        setMainnetWalletClient(null);
        setIsOnCorrectChain(false);
        setCurrentChainId(null);
        setStatus('Wallet disconnected by user from app.');
        setError(null);
    };

    const switchNetwork = async (targetChainId: number) => {
        console.log("WalletContext: switchNetwork called for targetChainId", targetChainId);
        if (!window.ethereum) {
            setError("MetaMask is not installed.");
            return;
        }
        if (!connectedAccount) {
            setError("Please connect your wallet first to switch networks.");
            return;
        }
        // Không reset userDisconnectedFromApp ở đây, vì người dùng có thể muốn đổi mạng
        // ngay cả khi họ đã "ngắt kết nối" khỏi logic của app.
        // Việc kết nối lại logic app nên được thực hiện qua nút "Connect Wallet".

        setStatus(`Attempting to switch to network ID: ${targetChainId}...`);
        try {
            await window.ethereum.request({
                method: 'wallet_switchEthereumChain',
                params: [{ chainId: `0x${targetChainId.toString(16)}` }],
            });
            // Sự kiện 'chainChanged' sẽ được kích hoạt và handleChainChanged sẽ xử lý cập nhật state
        } catch (switchError: any) {
            console.error("WalletContext: Error switching network:", switchError);
            if (switchError.code === 4902) {
                setStatusMessage(`Network ID ${targetChainId} not found in MetaMask. Attempting to add...`);
                try {
                    if (targetChainId === chain991.id) { //
                         await window.ethereum.request({
                            method: 'wallet_addEthereumChain',
                            params: [
                                {
                                    chainId: `0x${chain991.id.toString(16)}`, //
                                    chainName: chain991.name, //
                                    rpcUrls: chain991.rpcUrls.default.http, //
                                    nativeCurrency: chain991.nativeCurrency, //
                                },
                            ],
                        });
                    } else {
                         setError(`Network with ID ${targetChainId} is not pre-configured for auto-add.`);
                         setStatusMessage(null);
                    }
                } catch (addError: any) {
                    console.error("WalletContext: Error adding network:", addError);
                    setError(`Failed to add network: ${addError.message}`);
                    setStatusMessage(null);
                }
            } else if (switchError.code === 4001) {
                 setError(`Network switch request rejected by user.`);
                 setStatusMessage(null);
            } else {
                setError(`Failed to switch network: ${switchError.message || 'Unknown error.'}`);
                setStatusMessage(null);
            }
        }
    };

    return (
        <WalletContext.Provider value={{
            walletClient,
            publicClient,
            mainnetWalletClient,
            connectedAccount,
            isConnecting,
            isOnCorrectChain,
            currentChainId,
            connectWallet,
            disconnectWallet,
            switchNetwork,
            error,
            clearError,
            status,
            setStatusMessage
        }}>
            {children}
        </WalletContext.Provider>
    );
};

export const useWallet = () => {
    const context = React.useContext(WalletContext);
    if (context === undefined) {
        throw new Error('useWallet must be used within a WalletProvider');
    }
    return context;
};