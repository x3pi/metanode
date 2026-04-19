// src/components/SharedHeader.tsx
import React, { useState, useEffect, useRef } from 'react';
import { useWallet } from '../contexts/WalletContext';
import { chain991 } from '../customChain'; //
import type { PageLink, PageName } from '../App';

const LoadingSpinnerIcon = () => (
    <svg className="animate-spin h-5 w-5 text-white" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4"></circle>
        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
    </svg>
);

interface SharedHeaderProps {
    currentPage: PageName;
    setCurrentPage: (page: PageName) => void;
    pageLinks: PageLink[];
}

const SharedHeader: React.FC<SharedHeaderProps> = ({ currentPage, setCurrentPage, pageLinks }) => {
    const {
        connectedAccount,
        isConnecting,
        currentChainId,
        connectWallet,
        disconnectWallet,
        switchNetwork,
        error: walletError,
        status: walletStatus,
        clearError: clearWalletError,
        setStatusMessage: setWalletStatusMessage,
    } = useWallet();

    const [isMobileMenuOpen, setIsMobileMenuOpen] = useState(false);
    const menuRef = useRef<HTMLDivElement>(null);
    const buttonRef = useRef<HTMLButtonElement>(null);


    const handleSwitchToChain991 = () => {
        console.log("SharedHeader: handleSwitchToChain991 called");
        switchNetwork(chain991.id);
        setIsMobileMenuOpen(false);
    };

    const handlePageLinkClick = (pageName: PageName) => {
        console.log("SharedHeader: handlePageLinkClick called for page", pageName);
        setCurrentPage(pageName);
        setIsMobileMenuOpen(false);
    };

    useEffect(() => {
        const handleClickOutside = (event: MouseEvent) => {
            if (
                menuRef.current &&
                !menuRef.current.contains(event.target as Node) &&
                buttonRef.current &&
                !buttonRef.current.contains(event.target as Node)
            ) {
                setIsMobileMenuOpen(false);
            }
        };
        if (isMobileMenuOpen) {
            document.addEventListener('mousedown', handleClickOutside);
        }
        return () => {
            document.removeEventListener('mousedown', handleClickOutside);
        };
    }, [isMobileMenuOpen]);

    const navButtonBaseStyle = "px-3 py-2 rounded-md text-sm font-medium transition-colors duration-150 ease-in-out";
    const activeNavButtonStyle = "bg-indigo-600 text-white";
    const inactiveNavButtonStyle = "text-neutral-300 hover:bg-neutral-700 hover:text-white";

    const walletButtonBaseStyle = "px-3 py-2 rounded-md text-xs font-medium shadow-sm transition-all duration-150 ease-in-out focus:outline-none focus:ring-2 focus:ring-opacity-75";
    const connectWalletButtonStyle = "bg-indigo-600 hover:bg-indigo-700 text-white ring-indigo-500";
    const disconnectWalletButtonStyle = "bg-red-600 hover:bg-red-700 text-white ring-red-500";
    const switchNetworkButtonStyle = "bg-yellow-500 hover:bg-yellow-600 text-neutral-900 ring-yellow-400";


    return (
        <header className="bg-neutral-800 shadow-lg sticky top-0 z-50">
            <div className="container mx-auto px-2 sm:px-4">
                <div className="relative flex items-center justify-between h-16">
                    {/* Logo/Tên ứng dụng */}
                    <div className="flex-shrink-0">
                        <span
                            onClick={() => handlePageLinkClick('Home')}
                            className="text-xl font-bold text-teal-400 cursor-pointer hover:text-teal-300 transition-colors"
                        >
                            Account
                        </span>
                    </div>

                    {/* Điều hướng cho Desktop - Sửa breakpoint ở đây */}
                    {/* Hiển thị khi màn hình là lg (1024px) trở lên */}
                    <div className="hidden lg:flex lg:ml-6">
                        <div className="flex space-x-4">
                            {pageLinks.map((link) => (
                                <button
                                    key={link.name}
                                    onClick={() => handlePageLinkClick(link.name)}
                                    className={`${navButtonBaseStyle} ${currentPage === link.name ? activeNavButtonStyle : inactiveNavButtonStyle}`}
                                    aria-current={currentPage === link.name ? 'page' : undefined}
                                >
                                    {link.label}
                                </button>
                            ))}
                        </div>
                    </div>

                    {/* Thông tin ví và nút kết nối/chuyển mạng cho Desktop */}
                    {/* Có thể giữ md:flex nếu phần này không quá rộng, hoặc đổi thành lg:flex nếu cần */}
                    <div className=" md:ml-auto md:items-center relative flex items-center justify-between h-16">
                        {connectedAccount && (
                            <div className="text-xs text-neutral-400 p-3">
                                <span className={`ml-1 px-1.5 py-0.5 rounded-full text-white text-[10px] `}>
                                    {`${connectedAccount.substring(0, 6)}...${connectedAccount.substring(connectedAccount.length - 4)}`}
                                </span>
                                <span className={`ml-1 px-1.5 py-0.5 rounded-full text-white text-[10px] ${currentChainId === chain991.id ? 'bg-green-500/80' : 'bg-yellow-500/80 text-neutral-900'}`}>
                                    ID: {currentChainId ?? 'N/A'}
                                </span>
                            </div>
                        )}
                        {connectedAccount && (
                            <div className="text-xs text-neutral-400">

                                {currentChainId !== null && currentChainId !== chain991.id && ( //
                                    <button
                                        onClick={handleSwitchToChain991}
                                        className={`mt-1 ${walletButtonBaseStyle} ${switchNetworkButtonStyle} w-full text-[10px] py-1`}
                                    >
                                        Switch to {chain991.name} {/* */}
                                    </button>
                                )}
                            </div>
                        )}
                        {!connectedAccount ? (
                            <button
                                onClick={() => {
                                    console.log("SharedHeader: Connect button clicked (desktop)");
                                    connectWallet();
                                }}
                                disabled={isConnecting}
                                className={`${walletButtonBaseStyle} ${connectWalletButtonStyle} flex items-center`}
                            >
                                {isConnecting && <LoadingSpinnerIcon />}
                                <span className={isConnecting ? 'ml-1.5' : ''}>Connect Wallet</span>
                            </button>
                        ) : (
                            <button
                                onClick={() => {
                                    console.log("SharedHeader: Disconnect button clicked (desktop)");
                                    disconnectWallet();
                                }}
                                className={`hidden md:block ${walletButtonBaseStyle} ${disconnectWalletButtonStyle}`}
                            >
                                Disconnect
                            </button>
                        )}
                    </div>

                    {/* Nút Hamburger cho Mobile/Tablet - Sửa breakpoint ở đây */}
                    {/* Hiển thị khi màn hình nhỏ hơn lg (dưới 1024px) */}
                    <div className="flex  items-center ml-1 md:hidden"> {/* Thay md:hidden thành lg:hidden */}

                        <button
                            ref={buttonRef}
                            onClick={() => {
                                console.log("SharedHeader: Hamburger button clicked. Current mobile menu state:", isMobileMenuOpen);
                                setIsMobileMenuOpen(!isMobileMenuOpen);
                            }}
                            type="button"
                            className="inline-flex items-center justify-center p-2 rounded-md text-neutral-400 hover:text-white hover:bg-neutral-700 focus:outline-none focus:ring-2 focus:ring-inset focus:ring-white"
                            aria-controls="mobile-menu"
                            aria-expanded={isMobileMenuOpen}
                        >
                            <span className="sr-only">Open main menu</span>
                            {isMobileMenuOpen ? (
                                <svg className="block h-6 w-6" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M6 18L18 6M6 6l12 12" /></svg>
                            ) : (
                                <svg className="block h-6 w-6" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" d="M4 6h16M4 12h16M4 18h16" /></svg>
                            )}
                        </button>
                    </div>
                </div>
            </div>

            {/* Menu cho Mobile/Tablet - Sửa breakpoint ở đây */}
            {/* Menu này cũng nên ẩn ở lg trở lên */}
            {isMobileMenuOpen && (
                <div className="lg:hidden absolute top-16 inset-x-0 bg-neutral-800 shadow-xl pb-3 z-40" id="mobile-menu" ref={menuRef}> {/* Thêm lg:hidden */}
                    <div className="px-2 pt-2 pb-3 space-y-1 sm:px-3">
                        {pageLinks.map((link) => (
                            <button
                                key={link.name}
                                onClick={() => handlePageLinkClick(link.name)}
                                className={`${navButtonBaseStyle} w-full text-left block ${currentPage === link.name ? activeNavButtonStyle : inactiveNavButtonStyle}`}
                                aria-current={currentPage === link.name ? 'page' : undefined}
                            >
                                {link.label}
                            </button>
                        ))}
                    </div>
                    {/* Thông tin ví và nút cho Mobile Menu */}
                    <div className="pt-4 pb-3 border-t border-neutral-700">
                        {connectedAccount && (
                            <div className="flex items-center px-4 mb-3">
                                <div className="text-xs text-neutral-300">
                                    <p className="font-medium">Account:</p>
                                    <p className="font-mono break-all">{connectedAccount}</p>
                                    <p className="mt-1">
                                        <span className="font-medium">Chain ID:</span> {currentChainId ?? 'N/A'}
                                        {currentChainId === chain991.id && <span className="text-green-400 ml-1">({chain991.name})</span>} {/* */}
                                    </p>
                                </div>
                            </div>
                        )}
                        <div className="mt-1 px-2 space-y-2">
                            {connectedAccount && currentChainId !== null && currentChainId !== chain991.id && ( //
                                <button
                                    onClick={handleSwitchToChain991}
                                    className={`${walletButtonBaseStyle} ${switchNetworkButtonStyle} w-full justify-center`}
                                >
                                    Switch to {chain991.name} {/* */}
                                </button>
                            )}
                            {!connectedAccount ? (
                                <button
                                    onClick={() => {
                                        console.log("SharedHeader: Connect button clicked (mobile)");
                                        connectWallet();
                                        setIsMobileMenuOpen(false);
                                    }}
                                    disabled={isConnecting}
                                    className={`${walletButtonBaseStyle} ${connectWalletButtonStyle} w-full flex items-center justify-center`}
                                >
                                    {isConnecting && <LoadingSpinnerIcon />}
                                    <span className={isConnecting ? 'ml-1.5' : ''}>Connect Wallet</span>
                                </button>
                            ) : (
                                <button
                                    onClick={() => {
                                        console.log("SharedHeader: Disconnect button clicked (mobile)");
                                        disconnectWallet();
                                        setIsMobileMenuOpen(false);
                                    }}
                                    className={`${walletButtonBaseStyle} ${disconnectWalletButtonStyle} w-full justify-center`}
                                >
                                    Disconnect Wallet
                                </button>
                            )}
                        </div>
                    </div>
                </div>
            )}

            {/* Global Wallet Status/Error Bar */}
            {(walletStatus || walletError) && (
                <div className="container mx-auto px-2 sm:px-4 pb-1">
                    {walletStatus && (
                        <div className="mt-1 text-center text-xs p-1.5 bg-sky-700/80 border border-sky-600/90 text-sky-100 rounded-md">
                            {walletStatus}
                            <button onClick={() => setWalletStatusMessage(null)} className="ml-2 text-sky-100 hover:text-white font-semibold">✕</button>
                        </div>
                    )}
                    {walletError && (
                        <div className="mt-1 text-center text-xs p-1.5 bg-red-700/80 border border-red-600/90 text-red-100 rounded-md">
                            {walletError}
                            <button onClick={clearWalletError} className="ml-2 text-red-100 hover:text-white font-semibold">✕</button>
                        </div>
                    )}
                </div>
            )}
        </header>
    );
};

export default SharedHeader;