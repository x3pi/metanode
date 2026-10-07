import React, { useState } from 'react';
import { Shield, Wallet, Plus, Server, Check, X, AlertCircle } from 'lucide-react';
import { switchOrAddNetwork, probeClusterEndpoint } from '../services/metanodeRpc';

export function Header({
  account,
  onConnect,
  onDisconnect,
  selectedCluster,
  onSelectCluster,
  clusterStatus,
  isConnecting,
  isPrivateKeyMode,
  allClusters,
  onAddCustomCluster,
}) {
  const [showAddModal, setShowAddModal] = useState(false);
  const [customUrl, setCustomUrl] = useState('');
  const [probing, setProbing] = useState(false);
  const [probeResult, setProbeResult] = useState(null);
  const [probeError, setProbeError] = useState(null);

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

  const handleProbe = async (e) => {
    e.preventDefault();
    if (!customUrl) return;
    setProbing(true);
    setProbeError(null);
    setProbeResult(null);

    try {
      const info = await probeClusterEndpoint(customUrl);
      if (!info.online) {
        throw new Error(info.error || 'Endpoint unreachable. Ensure node is running with CORS enabled.');
      }
      setProbeResult(info);
    } catch (err) {
      setProbeError(err.message);
    } finally {
      setProbing(false);
    }
  };

  const handleConfirmAdd = () => {
    if (!probeResult) return;
    const newCluster = {
      id: `custom_${Date.now()}`,
      name: probeResult.name || customUrl,
      chainId: probeResult.chainId || 991,
      rpcUrl: probeResult.rpcUrl,
      isParent: probeResult.isParent,
      isExec: probeResult.isExec,
      clusterKey: probeResult.clusterKey || null,
      isCustom: true,
    };
    if (onAddCustomCluster) {
      onAddCustomCluster(newCluster);
    }
    onSelectCluster(newCluster);
    setShowAddModal(false);
    setCustomUrl('');
    setProbeResult(null);
  };

  const clustersList = allClusters || [];

  return (
    <>
      <header className="header">
        <div className="header-content">
          {/* Brand */}
          <div className="brand-section">
            <div className="brand-logo-container">
              <img
                src="https://metanode.co/image/logo.png"
                alt="MetaNode Logo"
                className="brand-logo-img"
                onError={(e) => {
                  e.target.style.display = 'none';
                  if (e.target.nextSibling) e.target.nextSibling.style.display = 'flex';
                }}
              />
              <div className="brand-logo-fallback" style={{ display: 'none' }}>
                <Shield className="w-5 h-5 text-cyan" />
              </div>
            </div>
            <div>
              <div className="brand-title">METANODE PORTAL</div>
              <div className="brand-tagline">Account Gate & Ecosystem Gateway</div>
            </div>
          </div>

          {/* Actions */}
          <div className="header-actions">
            {/* Chain ID Badge */}
            <div
              className="badge-pill"
              style={{
                background: 'rgba(99, 102, 241, 0.15)',
                color: '#818cf8',
                border: '1px solid rgba(99, 102, 241, 0.3)',
                padding: '6px 10px',
                fontSize: '0.78rem',
                fontWeight: 600,
              }}
            >
              Chain ID: {selectedCluster?.chainId || 991}
            </div>

            {/* Cluster Selector */}
            <div className="network-selector">
              <span
                className={`status-dot ${clusterStatus?.online ? 'online' : 'offline'}`}
                title={
                  clusterStatus?.online
                    ? `Online (${clusterStatus.latency}ms) — ${
                        clusterStatus.readiness === false ? 'Unready' : 'Ready'
                      }`
                    : 'Offline'
                }
              />
              <select
                className="network-select-input"
                value={selectedCluster?.id}
                onChange={(e) => {
                  const found = clustersList.find((c) => c.id === e.target.value);
                  if (found) handleSwitchNetwork(found);
                }}
              >
                {clustersList.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </div>

            {/* Add Node Button */}
            <button
              className="btn btn-secondary btn-sm"
              onClick={() => setShowAddModal(true)}
              title="Connect custom RPC endpoint"
              style={{ padding: '6px 10px' }}
            >
              <Plus className="w-3.5 h-3.5" />
              Node
            </button>

            {/* Connect / Account Button */}
            {account ? (
              <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                {isPrivateKeyMode && (
                  <span
                    style={{
                      fontSize: '0.7rem',
                      background: 'rgba(6, 182, 212, 0.15)',
                      color: 'var(--cyan-bright)',
                      border: '1px solid rgba(6, 182, 212, 0.3)',
                      padding: '3px 8px',
                      borderRadius: '6px',
                      fontWeight: 600,
                    }}
                    title="Connected via direct Private Key"
                  >
                    PK Mode
                  </span>
                )}
                <button
                  className="btn btn-secondary"
                  style={{ fontFamily: 'var(--font-mono)', fontSize: '0.82rem' }}
                  onClick={() => {
                    navigator.clipboard.writeText(account);
                    alert('Address copied to clipboard!');
                  }}
                  title="Click to copy full address"
                >
                  <div
                    style={{
                      width: '8px',
                      height: '8px',
                      borderRadius: '50%',
                      background: 'var(--cyan)',
                    }}
                  />
                  {formatAddress(account)}
                </button>
                <button
                  className="btn btn-secondary btn-sm"
                  onClick={onDisconnect}
                  title="Disconnect wallet"
                >
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

      {/* Add Custom Node Modal */}
      {showAddModal && (
        <div
          style={{
            position: 'fixed',
            inset: 0,
            background: 'rgba(0, 0, 0, 0.75)',
            backdropFilter: 'blur(8px)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            zIndex: 1000,
            padding: '20px',
          }}
        >
          <div
            className="card"
            style={{
              width: '100%',
              maxWidth: '480px',
              border: '1px solid var(--border-medium)',
              boxShadow: '0 20px 50px rgba(0,0,0,0.8)',
            }}
          >
            <div className="card-header" style={{ marginBottom: '16px' }}>
              <div className="card-title">
                <Server className="w-5 h-5 text-cyan" />
                Connect Custom Node RPC
              </div>
              <button
                className="btn btn-secondary btn-sm"
                onClick={() => {
                  setShowAddModal(false);
                  setProbeResult(null);
                  setProbeError(null);
                }}
              >
                ✕
              </button>
            </div>

            <p style={{ fontSize: '0.82rem', color: 'var(--text-muted)', marginBottom: '16px' }}>
              Add any local or remote MetaNode node endpoint (Parent Chain REST or Exec Cluster JSON-RPC).
            </p>

            <form onSubmit={handleProbe}>
              <div className="form-group">
                <label className="form-label">RPC Endpoint URL</label>
                <input
                  type="text"
                  className="form-input mono"
                  placeholder="http://127.0.0.1:8747"
                  value={customUrl}
                  onChange={(e) => setCustomUrl(e.target.value)}
                  required
                />
              </div>

              <button
                type="submit"
                className="btn btn-secondary"
                style={{ width: '100%', marginTop: '6px' }}
                disabled={probing || !customUrl}
              >
                {probing ? 'Probing Endpoint...' : 'Probe & Detect Configuration'}
              </button>
            </form>

            {probeError && (
              <div className="alert alert-danger" style={{ marginTop: '14px', fontSize: '0.82rem' }}>
                <AlertCircle className="w-4 h-4" />
                {probeError}
              </div>
            )}

            {probeResult && (
              <div
                style={{
                  marginTop: '16px',
                  padding: '12px',
                  background: 'rgba(16, 185, 129, 0.08)',
                  border: '1px solid rgba(16, 185, 129, 0.25)',
                  borderRadius: '10px',
                }}
              >
                <div style={{ fontWeight: 700, color: 'var(--green)', marginBottom: '6px', fontSize: '0.85rem' }}>
                  ✓ Node Detected Successfully!
                </div>
                <div style={{ fontSize: '0.78rem', color: 'var(--text-secondary)' }}>
                  <div>Type: <strong>{probeResult.isParent ? 'Parent Chain L1' : 'Execution Cluster'}</strong></div>
                  <div>Detected Chain ID: <strong>{probeResult.chainId}</strong></div>
                  {probeResult.accountGate && (
                    <div style={{ color: 'var(--cyan-bright)' }}>🛡️ Account Gate: Enforced (ParentRegistered)</div>
                  )}
                </div>

                <button
                  className="btn btn-primary"
                  style={{ width: '100%', marginTop: '12px' }}
                  onClick={handleConfirmAdd}
                >
                  <Check className="w-4 h-4" />
                  Save & Switch to This Node
                </button>
              </div>
            )}
          </div>
        </div>
      )}
    </>
  );
}
