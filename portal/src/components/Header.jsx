import React from 'react';
import { Shield, Wallet, ExternalLink, RefreshCw, CheckCircle2, AlertCircle } from 'lucide-react';
import { PRESET_CLUSTERS, switchOrAddNetwork } from '../services/metanodeRpc';

export function Header({
  account,
  onConnect,
  onDisconnect,
  selectedCluster,
  onSelectCluster,
  clusterStatus,
  isConnecting,
}) {
  const formatAddress = (addr) => {
    if (!addr) return '';
    return `${addr.slice(0, 6)}...${addr.slice(-4)}`;
  };

  const handleSwitchNetwork = async (cluster) => {
    onSelectCluster(cluster);
    if (account && cluster.chainId) {
      try {
        await switchOrAddNetwork(cluster);
      } catch (err) {
        console.warn('Network switch error:', err);
      }
    }
  };

  return (
    <header className="header">
      <div className="header-content">
        {/* Brand */}
        <div className="brand-section">
          <div className="brand-logo-icon">
            <Shield className="w-5 h-5 text-white" />
          </div>
          <div>
            <div className="brand-title">METANODE PORTAL</div>
            <div className="brand-tagline">Account Gate & Ecosystem Gateway</div>
          </div>
        </div>

        {/* Actions */}
        <div className="header-actions">
          {/* Cluster Selector */}
          <div className="network-selector">
            <span
              className={`status-dot ${clusterStatus?.online ? 'online' : 'offline'}`}
              title={clusterStatus?.online ? `Online (${clusterStatus.latency}ms)` : 'Offline'}
            />
            <select
              className="network-select-input"
              value={selectedCluster.id}
              onChange={(e) => {
                const found = PRESET_CLUSTERS.find((c) => c.id === e.target.value);
                if (found) handleSwitchNetwork(found);
              }}
            >
              {PRESET_CLUSTERS.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </div>

          {/* Connect / Account Button */}
          {account ? (
            <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
              <button
                className="btn btn-secondary"
                style={{ fontFamily: 'var(--font-mono)', fontSize: '0.82rem' }}
                onClick={() => {
                  navigator.clipboard.writeText(account);
                  alert('Address copied to clipboard!');
                }}
                title="Click to copy full address"
              >
                <div style={{ width: '8px', height: '8px', borderRadius: '50%', background: 'var(--cyan)' }} />
                {formatAddress(account)}
              </button>
              <button className="btn btn-secondary btn-sm" onClick={onDisconnect} title="Disconnect wallet">
                ✕
              </button>
            </div>
          ) : (
            <button className="btn btn-primary" onClick={onConnect} disabled={isConnecting}>
              <Wallet className="w-4 h-4" />
              {isConnecting ? 'Connecting...' : 'Connect MetaMask'}
            </button>
          )}
        </div>
      </div>
    </header>
  );
}
