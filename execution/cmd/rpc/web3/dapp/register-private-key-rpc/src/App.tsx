import "./App.css";

import SharedHeader from "./components/SharedHeader";
import { Sidebar } from "./components/Sidebar";
import { Route, Routes } from "react-router-dom";
import BlsManagerPage from "./pages/BlsManager/BlsManagerPage";
import MetaMaskSigner from "./pages/MetaMaskSigner/MetaMaskSigner";
import HomePage from "./pages/Home/HomePage";
import AccountTypeManagerPage from "./pages/SetAccountType/AccountTypeManagerPage";
import BlsAccountListPage from "./pages/BlsAccountList/BlsAccountListPage";
import ContractFreeGasPage from "./pages/ContractFreeGas/ContractFreeGasPage";
import { MobileMenuProvider } from "./contexts/MobileMenuContext";
import { ThemeProvider } from "./contexts/ThemeContext";
import { NotificationProvider } from "./contexts/NotificationContext";

export interface PageLink {
  path: string;
  label: string;
}

function App() {
  const pageLinks: PageLink[] = [
    { path: "/", label: "Account" },
    { path: "/bls", label: "Publickey BLS" },
    { path: "/account-type", label: "AccountType" },
    { path: "/register-rpc", label: "Register BLS Rpc" },
    { path: "/accounts", label: "Account List" },
    { path: "/contract-free-gas", label: "Contract Free Gas" },
  ];

  return (
    <ThemeProvider>
        <NotificationProvider>
          <MobileMenuProvider>
            <div className="bg-app text-app min-h-screen w-full flex flex-col transition-colors duration-300">
              {/* Header */}
              <SharedHeader pageLinks={pageLinks} />
            
            {/* Main layout - Full width */}
            <div className="flex flex-1 relative w-full">
              {/* Sidebar component */}
              <Sidebar pageLinks={pageLinks} />
              
              {/* Main content - Full width, responsive padding */}
              <main className="flex-1 w-full min-w-0 pt-4 md:pt-6 px-4 sm:px-6 lg:px-8">
                <Routes>
                  <Route path="/" element={<HomePage />} />
                  <Route path="bls" element={<BlsManagerPage />} />
                  <Route path="account-type" element={<AccountTypeManagerPage />} />
                  <Route path="register-rpc" element={<MetaMaskSigner />} />
                  <Route path="accounts" element={<BlsAccountListPage />} />
                  <Route path="contract-free-gas" element={<ContractFreeGasPage />} />
                  <Route path="*" element={<h1>Not found</h1>} />
                </Routes>
              </main>
            </div>

          </div>
        </MobileMenuProvider>
      </NotificationProvider>
    </ThemeProvider>
  );
}

export default App;
