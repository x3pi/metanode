import React, { useState, useEffect, useCallback } from 'react';
import {
  ShieldCheck,
  Send,
  ArrowLeftRight,
  Activity,
  Layers,
  HelpCircle,
  ExternalLink,
} from 'lucide-react';
import { Header } from './components/Header';
import { AccountGateCard } from './components/AccountGateCard';
import { TransferTab } from './components/TransferTab';
import { RollupTransferTab } from './components/RollupTransferTab';
import { NetworkMonitorTab } from './components/NetworkMonitorTab';
import {
  PRESET_CLUSTERS,
  LAN_IP,
  connectWallet,
  fetchAccountInfo,
  checkParentRegistration,
  checkNodeStatus,
} from './services/metanodeRpc';

export default function App() {
  const [account, setAccount] = useState(null);
  const [isConnecting, setIsConnecting] = useState(false);
  const [activeTab, setActiveTab] = useState('gate');
  const [selectedCluster, setSelectedCluster] = useState(PRESET_CLUSTERS[0]); // Default Exec1
  const [clusterStatus, setClusterStatus] = useState(null);
  const [accountInfo, setAccountInfo] = useState(null);
  const [parentRegInfo, setParentRegInfo] = useState(null);

  // Connect Wallet
  const handleConnect = async () => {
    setIsConnecting(true);
    try {
      const res = await connectWallet();
      setAccount(res.address);
    } catch (err) {
      alert(err.message || 'Failed to connect wallet');
    } finally {
      setIsConnecting(false);
    }
  };

  const handleDisconnect = () => {
    setAccount(null);
    setAccountInfo(null);
    setParentRegInfo(null);
  };

  // Refresh Account & Cluster State
  const refreshState = useCallback(async () => {
    // 1. Cluster Status
    const status = await checkNodeStatus(selectedCluster.rpcUrl);
    setClusterStatus(status);

    // 2. Account on Execution Cluster
    if (account) {
      const info = await fetchAccountInfo(selectedCluster.rpcUrl, account);
      setAccountInfo(info);

      // 3. Account on Parent Chain
      const parentCluster = PRESET_CLUSTERS.find((c) => c.isParent);
      const parentRpc = parentCluster ? parentCluster.rpcUrl : `http://${LAN_IP}:18601`;
      const clusterKey = selectedCluster.clusterKey || PRESET_CLUSTERS[0].clusterKey;
      const parentInfo = await checkParentRegistration(parentRpc, clusterKey, account);
      setParentRegInfo(parentInfo);
    }
  }, [account, selectedCluster]);

  useEffect(() => {
    refreshState();
    const interval = setInterval(refreshState, 5000);
    return () => clearInterval(interval);
  }, [refreshState]);

  // EIP-1193 Event Listeners
  useEffect(() => {
    if (typeof window !== 'undefined' && window.ethereum) {
      const handleAccountsChanged = (accounts) => {
        if (accounts.length === 0) {
          handleDisconnect();
        } else {
          setAccount(accounts[0]);
        }
      };
      window.ethereum.on('accountsChanged', handleAccountsChanged);
      return () => {
        window.ethereum.removeListener('accountsChanged', handleAccountsChanged);
      };
    }
  }, []);

  return (
    <div className="app-container">
      {/* Header */}
      <Header
        account={account}
        onConnect={handleConnect}
        onDisconnect={handleDisconnect}
        selectedCluster={selectedCluster}
        onSelectCluster={setSelectedCluster}
        clusterStatus={clusterStatus}
        isConnecting={isConnecting}
      />

      {/* Main Container */}
      <main className="main-content">
        {/* Navigation Tabs */}
        <nav className="tabs-nav">
          <button
            className={`tab-btn ${activeTab === 'gate' ? 'active' : ''}`}
            onClick={() => setActiveTab('gate')}
          >
            <ShieldCheck className="w-4 h-4" />
            Account Gate & Onboarding
            {accountInfo?.parentRegistered ? (
              <span className="badge-pill" style={{ background: 'rgba(16, 185, 129, 0.2)', color: '#34d399' }}>
                Open
              </span>
            ) : account ? (
              <span className="badge-pill" style={{ background: 'rgba(239, 68, 68, 0.2)', color: '#f87171' }}>
                Gated
              </span>
            ) : null}
          </button>

          <button
            className={`tab-btn ${activeTab === 'transfer' ? 'active' : ''}`}
            onClick={() => setActiveTab('transfer')}
          >
            <Send className="w-4 h-4" />
            Send Transactions
          </button>

          <button
            className={`tab-btn ${activeTab === 'rollup' ? 'active' : ''}`}
            onClick={() => setActiveTab('rollup')}
          >
            <ArrowLeftRight className="w-4 h-4" />
            Rollup Cross-Cluster
          </button>

          <button
            className={`tab-btn ${activeTab === 'monitor' ? 'active' : ''}`}
            onClick={() => setActiveTab('monitor')}
          >
            <Activity className="w-4 h-4" />
            Network Parity & Zero-Fork
          </button>
        </nav>

        {/* Tab Views */}
        {activeTab === 'gate' && (
          <AccountGateCard
            account={account}
            accountInfo={accountInfo}
            parentRegInfo={parentRegInfo}
            selectedCluster={selectedCluster}
            onRefresh={refreshState}
          />
        )}

        {activeTab === 'transfer' && (
          <TransferTab
            account={account}
            accountInfo={accountInfo}
            selectedCluster={selectedCluster}
            onRefresh={refreshState}
          />
        )}

        {activeTab === 'rollup' && (
          <RollupTransferTab account={account} accountInfo={accountInfo} />
        )}

        {activeTab === 'monitor' && <NetworkMonitorTab />}
      </main>

      {/* Footer */}
      <footer
        style={{
          borderTop: '1px solid var(--border-subtle)',
          padding: '20px 24px',
          textAlign: 'center',
          fontSize: '0.8rem',
          color: 'var(--text-muted)',
          background: 'rgba(7, 10, 18, 0.6)',
        }}
      >
        <div style={{ maxWidth: '1320px', margin: '0 auto', display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '10px' }}>
          <div>
            <strong>Metanode Portal</strong> — Powered by TrueBlockSTM & Parent Chain Account Gate
          </div>
          <div style={{ display: 'flex', gap: '16px' }}>
            <span>Protocol: secp256k1 + BLS12-381</span>
            <span>Invariant: 100% Zero-Fork Deterministic</span>
          </div>
        </div>
      </footer>
    </div>
  );
}
