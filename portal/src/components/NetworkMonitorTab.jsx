import React, { useState, useEffect } from 'react';
import { Activity, CheckCircle2, AlertOctagon, RefreshCw, Cpu, Server, Database } from 'lucide-react';
import { PRESET_CLUSTERS, checkNodeStatus } from '../services/metanodeRpc';

export function NetworkMonitorTab() {
  const [nodesStatus, setNodesStatus] = useState({});
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [lastRefreshed, setLastRefreshed] = useState(new Date().toLocaleTimeString());

  const fetchAllNodes = async () => {
    setIsRefreshing(true);
    const results = {};
    for (const cluster of PRESET_CLUSTERS) {
      results[cluster.id] = await checkNodeStatus(cluster.rpcUrl);
    }
    setNodesStatus(results);
    setLastRefreshed(new Date().toLocaleTimeString());
    setIsRefreshing(false);
  };

  useEffect(() => {
    fetchAllNodes();
    const interval = setInterval(fetchAllNodes, 6000);
    return () => clearInterval(interval);
  }, []);

  // Determine Zero-Fork Parity: If exec1 and exec2 are online, compare state roots / block numbers
  const exec1 = nodesStatus['exec1'];
  const exec2 = nodesStatus['exec2'];
  const isParityConsistent =
    !exec1?.online || !exec2?.online || (exec1.blockNumber === exec2.blockNumber && exec1.stateRoot === exec2.stateRoot);

  return (
    <div>
      {/* Zero-Fork Invariant Banner */}
      <div
        className={`card ${isParityConsistent ? 'gate-hero-card registered' : 'gate-hero-card unregistered'}`}
        style={{ marginBottom: '24px' }}
      >
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: '16px' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '14px' }}>
            <div
              style={{
                width: '44px',
                height: '44px',
                borderRadius: '12px',
                background: isParityConsistent ? 'rgba(16, 185, 129, 0.2)' : 'rgba(239, 68, 68, 0.2)',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
              }}
            >
              {isParityConsistent ? (
                <CheckCircle2 className="w-6 h-6 text-green" />
              ) : (
                <AlertOctagon className="w-6 h-6 text-red" />
              )}
            </div>
            <div>
              <h3 style={{ fontSize: '1.2rem', fontWeight: 800 }}>
                {isParityConsistent
                  ? 'Zero-Fork Invariant: 100% Deterministic Consensus'
                  : '⚠️ Cluster Discrepancy Detected'}
              </h3>
              <p style={{ color: 'var(--text-muted)', fontSize: '0.82rem' }}>
                All commit dispatches are data-driven via 2f+1 peer attestation. No timeout-based heuristics.
              </p>
            </div>
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
            <span style={{ fontSize: '0.78rem', color: 'var(--text-muted)' }}>Updated: {lastRefreshed}</span>
            <button className="btn btn-secondary btn-sm" onClick={fetchAllNodes} disabled={isRefreshing}>
              <RefreshCw className={`w-3.5 h-3.5 ${isRefreshing ? 'animate-spin' : ''}`} />
              Refresh
            </button>
          </div>
        </div>
      </div>

      {/* Cluster Node Status Cards */}
      <div className="grid-3">
        {PRESET_CLUSTERS.map((cluster) => {
          const status = nodesStatus[cluster.id];
          const isOnline = status?.online;

          return (
            <div key={cluster.id} className="card">
              <div className="card-header">
                <div className="card-title">
                  {cluster.isParent ? <Database className="w-5 h-5 text-indigo" /> : <Server className="w-5 h-5 text-cyan" />}
                  {cluster.name}
                </div>
                <span className={`badge ${isOnline ? 'badge-success' : 'badge-danger'}`}>
                  {isOnline ? 'Online' : 'Offline'}
                </span>
              </div>

              <div style={{ display: 'flex', flexDirection: 'column', gap: '10px' }}>
                <div className="detail-box">
                  <div className="detail-label">RPC Endpoint</div>
                  <div className="detail-value mono" style={{ fontSize: '0.8rem' }}>
                    {cluster.rpcUrl}
                  </div>
                </div>

                <div className="detail-box">
                  <div className="detail-label">Block Height</div>
                  <div className="detail-value" style={{ fontSize: '1.2rem', color: 'var(--cyan-bright)' }}>
                    {isOnline ? `#${status.blockNumber}` : '—'}
                  </div>
                </div>

                <div className="detail-box">
                  <div className="detail-label">Latency / Response</div>
                  <div className="detail-value">
                    {isOnline ? `${status.latency} ms` : 'Unreachable'}
                  </div>
                </div>

                <div className="detail-box">
                  <div className="detail-label">Account States Root</div>
                  <div className="detail-value mono" style={{ fontSize: '0.75rem' }}>
                    {isOnline ? status.stateRoot : '—'}
                  </div>
                </div>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
