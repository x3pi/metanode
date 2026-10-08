import React, { useState, useEffect, useCallback } from 'react';
import {
  ShieldCheck,
  Send,
  ArrowLeftRight,
  Activity,
  Layers,
  HelpCircle,
  ExternalLink,
  Boxes,
} from 'lucide-react';
import { Header } from './components/Header';
import { AccountGateCard } from './components/AccountGateCard';
import { TransferTab } from './components/TransferTab';
import { RollupTransferTab } from './components/RollupTransferTab';
import { NetworkMonitorTab } from './components/NetworkMonitorTab';
import { BlockExplorerTab } from './components/BlockExplorerTab';
import {
  PRESET_CLUSTERS,
  DEFAULT_CLUSTER_ID,
  LAN_IP,
  getAllClusters,
  saveCustomCluster,
  connectWallet,
  fetchAccountInfo,
  checkParentRegistration,
  checkNodeStatus,
} from './services/metanodeRpc';

export default function App() {
  const [allClusters, setAllClusters] = useState(getAllClusters());
  const [account, setAccount] = useState(null);
  const [walletPrivateKey, setWalletPrivateKey] = useState(null);
  const [isConnecting, setIsConnecting] = useState(false);
  const [activeTab, setActiveTab] = useState('gate');
  const defaultCluster =
    allClusters.find((c) => c.id === DEFAULT_CLUSTER_ID) || allClusters[0];
  const [selectedCluster, setSelectedCluster] = useState(defaultCluster);
  const [clusterStatus, setClusterStatus] = useState(null);
  const [accountInfo, setAccountInfo] = useState(null);
  const [parentRegInfo, setParentRegInfo] = useState(null);

  // Add custom cluster handler
  const handleAddCustomCluster = (newCluster) => {
    saveCustomCluster(newCluster);
    const updated = getAllClusters();
    setAllClusters(updated);
  };

  // Connect MetaMask Wallet
  const handleConnect = async () => {
    setIsConnecting(true);
    try {
      const res = await connectWallet();
      setAccount(res.address);
      setWalletPrivateKey(null);
    } catch (err) {
      alert(err.message || 'Failed to connect wallet');
    } finally {
      setIsConnecting(false);
    }
  };

  // Connect Custom Private Key Wallet
  const handleConnectCustomWallet = (addr, pk) => {
    setAccount(addr);
    setWalletPrivateKey(pk);
  };

  const handleDisconnect = () => {
    setAccount(null);
    setWalletPrivateKey(null);
    setAccountInfo(null);
    setParentRegInfo(null);
  };

  // Refresh Account & Cluster State
  const refreshState = useCallback(async () => {
    // 1. Cluster Status
    const status = await checkNodeStatus(selectedCluster);
    setClusterStatus(status);

    // 2. Account on Execution Cluster
    if (account) {
      const info = await fetchAccountInfo(selectedCluster.rpcUrl, account, selectedCluster);
      setAccountInfo(info);

      // 3. Account on Parent Chain
      const parentCluster =
        allClusters.find((c) => c.isParent) ||
        allClusters.find((c) => c.id?.includes('parent'));
      const parentRpcUrl = parentCluster?.rpcUrl || 'http://127.0.0.1:18601';
      const clusterKey =
        info?.dynamicClusterKey ||
        selectedCluster?.clusterKey ||
        '0x944488b425d29336c7913a3b45946adee6b9bfbd0838c6c8f422f4b4277066f26b3da0530c9f9865e6e534a05ae6c128';
      const parentInfo = await checkParentRegistration(parentRpcUrl, clusterKey, account);
      setParentRegInfo(parentInfo);
    }
  }, [account, selectedCluster, allClusters]);

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
        isPrivateKeyMode={!!walletPrivateKey}
        allClusters={allClusters}
        onAddCustomCluster={handleAddCustomCluster}
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
            Account Gate &amp; Onboarding
            {accountInfo?.gateEnforced === false || accountInfo?.parentRegistered ? (
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
            className={`tab-btn ${activeTab === 'explorer' ? 'active' : ''}`}
            onClick={() => setActiveTab('explorer')}
          >
            <Boxes className="w-4 h-4" />
            Block &amp; FullTx Explorer
          </button>

          <button
            className={`tab-btn ${activeTab === 'monitor' ? 'active' : ''}`}
            onClick={() => setActiveTab('monitor')}
          >
            <Activity className="w-4 h-4" />
            Network Parity &amp; Zero-Fork
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
            onConnectCustomWallet={handleConnectCustomWallet}
            onConnectMetaMask={handleConnect}
            isPrivateKeyMode={!!walletPrivateKey}
            walletPrivateKey={walletPrivateKey}
            allClusters={allClusters}
          />
        )}

        {activeTab === 'transfer' && (
          <TransferTab
            account={account}
            accountInfo={accountInfo}
            selectedCluster={selectedCluster}
            onRefresh={refreshState}
            walletPrivateKey={walletPrivateKey}
            onNavigateToGate={() => setActiveTab('gate')}
          />
        )}

        {activeTab === 'rollup' && (
          <RollupTransferTab
            account={account}
            accountInfo={accountInfo}
            allClusters={allClusters}
          />
        )}

        {activeTab === 'explorer' && (
          <BlockExplorerTab selectedCluster={selectedCluster} />
        )}

        {activeTab === 'monitor' && <NetworkMonitorTab allClusters={allClusters} />}
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
        <div
          style={{
            maxWidth: '1320px',
            margin: '0 auto',
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            flexWrap: 'wrap',
            gap: '10px',
          }}
        >
          <div>
            <strong>Metanode Portal</strong> &mdash; Chain ID 991 Cutover &amp; Zero-Fork Co-Attestation
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
