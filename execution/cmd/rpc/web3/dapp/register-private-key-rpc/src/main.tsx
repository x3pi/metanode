import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import App from "./App.tsx";
import { WalletProvider } from "./contexts/WalletContext";
import { BrowserRouter } from "react-router-dom";
import { ThemeProvider } from "./contexts/ThemeContext";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ThemeProvider>
      <WalletProvider>
          <BrowserRouter basename="/register-bls-key/">
            <App />
          </BrowserRouter>
      </WalletProvider>
    </ThemeProvider>
  </StrictMode>
);
