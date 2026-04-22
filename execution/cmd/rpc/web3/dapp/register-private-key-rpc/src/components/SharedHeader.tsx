// src/components/SharedHeader.tsx
import React from "react";
import { Link } from "react-router-dom";
import { useWallet } from "../contexts/WalletContext";
import { useMobileMenu } from "../contexts/MobileMenuContext";
import { chain991 } from "~/constants/customChain";
import type { PageLink } from "../App";
import { ThemeToggle } from "./ThemeToggle";
import { NotificationBell } from "./NotificationBell";

const LoadingSpinnerIcon = () => (
  <svg
    className="animate-spin h-5 w-5 text-white"
    xmlns="http://www.w3.org/2000/svg"
    fill="none"
    viewBox="0 0 24 24"
  >
    <circle
      className="opacity-25"
      cx="12"
      cy="12"
      r="10"
      stroke="currentColor"
      strokeWidth="4"
    ></circle>
    <path
      className="opacity-75"
      fill="currentColor"
      d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
    ></path>
  </svg>
);
// Mobile menu button component
const MobileMenuButton: React.FC = () => {
  const { isMobileMenuOpen, toggleMobileMenu } = useMobileMenu();

  return (
    <button
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        console.log(
          "Mobile menu button clicked, current state:",
          isMobileMenuOpen
        );
        toggleMobileMenu();
      }}
      className="md:hidden w-10 h-10 bg-primary hover:bg-primary-hover rounded-full flex items-center justify-center text-white transition-all duration-200 shadow-md hover:shadow-lg active:scale-[0.98] font-bold dark:bg-primary dark:text-white"
      aria-label="Toggle menu"
    >
      <div className="w-5 h-5 flex flex-col justify-center items-center">
        <span
          className={`bg-white block transition-all duration-300 ease-out h-0.5 w-4 rounded-sm ${
            isMobileMenuOpen ? "rotate-45 translate-y-1" : "-translate-y-0.5"
          }`}
        ></span>
        <span
          className={`bg-white block transition-all duration-300 ease-out h-0.5 w-4 rounded-sm my-0.5 ${
            isMobileMenuOpen ? "opacity-0" : "opacity-100"
          }`}
        ></span>
        <span
          className={`bg-white block transition-all duration-300 ease-out h-0.5 w-4 rounded-sm ${
            isMobileMenuOpen ? "-rotate-45 -translate-y-1" : "translate-y-0.5"
          }`}
        ></span>
      </div>
    </button>
  );
};

interface SharedHeaderProps {
  pageLinks: PageLink[];
}

const SharedHeader: React.FC<SharedHeaderProps> = () => {
  const {
    connectedAccount,
    isConnecting,
    currentChainId,
    connectWallet,
    disconnectWallet,
    switchNetwork,
    // error: walletError,
    // status: walletStatus,
    // clearError: clearWalletError,
    // setStatusMessage: setWalletStatusMessage,
  } = useWallet();

  const handleSwitchToChain991 = () => {
    switchNetwork(chain991.id);
  };

  // === 1. ĐỊNH NGHĨA STYLE M3 BẰNG TAILWIND ===

  // Material 3 Button Styles - rounded-full với elevation
  const walletPillBaseStyle =
    "px-4 py-2 rounded-full text-xs font-medium shadow-sm hover:shadow-md transition-all duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-offset-2 flex items-center justify-center active:scale-[0.98]";

  // Material 3 Filled Button - Text trắng, luôn rõ khi hover
  const connectWalletButtonStyle =
    "bg-primary hover:bg-primary-hover text-white hover:text-white ring-primary font-bold dark:bg-primary dark:text-white dark:hover:text-white";

  // Material 3 Tonal Button - Text trắng, luôn rõ khi hover
  const disconnectWalletButtonStyle =
    "bg-gray-800 hover:bg-gray-900 text-white hover:text-white ring-border font-bold shadow-md hover:shadow-lg dark:bg-gray-600 dark:text-white dark:hover:bg-gray-500 dark:hover:text-white";

  // Material 3 Warning Button - Text trắng, luôn rõ khi hover
  const switchNetworkButtonStyle =
    "bg-warning hover:bg-warning text-white hover:text-white ring-warning font-bold dark:bg-warning dark:text-white dark:hover:text-white";

  return (
    // === 2. HEADER: Material 3 "Top App Bar" - Full width ===
    <header className="bg-card shadow-md border-b border-border sticky top-0 z-50 transition-all duration-300 w-full dark:bg-card dark:border-border">
      <div className="w-full px-4 sm:px-6 lg:px-8">
        <div className="relative flex items-center justify-between h-16">
          {/* Logo (Material 3 Primary Color) */}
          <div className="shrink-0">
            <Link
              to="/"
              className="text-xl font-bold text-primary cursor-pointer hover:text-primary-hover transition-colors duration-200"
            >
              Account Manager
            </Link>
          </div>

          {/* === 3. CỤM VÍ PHIÊN BẢN DESKTOP (M3 Shape: rounded-full) === */}
          <div className="hidden lg:flex lg:ml-auto lg:items-center lg:space-x-2">
            <ThemeToggle />
            <NotificationBell />

            {connectedAccount ? (
              <>
                {/* Material 3 Chip cho Địa chỉ và Chain ID */}
                <div className="bg-app-secondary rounded-full px-4 py-2 text-xs flex items-center space-x-2 shadow-sm border border-border">
                  <span className="text-foreground font-medium">
                    {`${connectedAccount.substring(
                      0,
                      6
                    )}...${connectedAccount.substring(
                      connectedAccount.length - 4
                    )}`}
                  </span>
                  <span
                    className={`px-2 py-0.5 rounded-full text-white text-[10px] font-medium shadow-sm ${
                      currentChainId === chain991.id
                        ? "bg-success"
                        : "bg-warning"
                    }`}
                  >
                    ID: {currentChainId ?? "N/A"}
                  </span>
                </div>

                {currentChainId !== null && currentChainId !== chain991.id && (
                  <button
                    onClick={handleSwitchToChain991}
                    className={`${walletPillBaseStyle} ${switchNetworkButtonStyle} py-2`}
                  >
                    Switch
                  </button>
                )}

                <button
                  onClick={() => disconnectWallet()}
                  className={`${walletPillBaseStyle} ${disconnectWalletButtonStyle}`}
                >
                  Disconnect
                </button>
              </>
            ) : (
              // Nút Connect (M3 Filled Button)
              <button
                onClick={() => connectWallet()}
                disabled={isConnecting}
                className={`${walletPillBaseStyle} ${connectWalletButtonStyle}`}
              >
                {isConnecting && <LoadingSpinnerIcon />}
                <span className={isConnecting ? "ml-1.5" : ""}>
                  Connect Wallet
                </span>
              </button>
            )}
          </div>

          {/* Mobile menu button and theme toggle */}
          <div className="flex items-center ml-2 lg:hidden gap-2">
            <ThemeToggle />
            <NotificationBell />
            {/* Mobile menu button from context */}
            <MobileMenuButton />
          </div>
        </div>
      </div>

      {/* === 5. THANH STATUS (M3 "Snackbar" Component) === */}
      {/* {(walletStatus || walletError) && (
        <div className="w-full px-4 sm:px-6 lg:px-8 pb-1">
          {walletStatus && (
            <div className="relative mt-1 flex items-center justify-between text-xs p-3 bg-info-container border-2 border-info/30 text-info rounded-2xl shadow-md">
              <span className="font-medium">{walletStatus}</span>
              <button
                onClick={() => setWalletStatusMessage(null)}
                className="ml-3 shrink-0 hover:bg-info/20 rounded-full p-1 -mr-1 -my-1 transition-colors"
              >
                <span className="sr-only">Close</span>
                <CloseIcon />
              </button>
            </div>
          )}
          {walletError && (
            <div className="relative mt-1 flex items-center justify-between text-xs p-3 bg-error-container border-2 border-error/30 text-error rounded-2xl shadow-md">
              <span className="font-medium">{walletError}</span>
              <button
                onClick={clearWalletError}
                className="ml-3 shrink-0 hover:bg-error/20 rounded-full p-1 -mr-1 -my-1 transition-colors"
              >
                <span className="sr-only">Close</span>
                <CloseIcon />
              </button>
            </div>
          )}
        </div>
      )} */}
    </header>
  );
};

export default SharedHeader;
