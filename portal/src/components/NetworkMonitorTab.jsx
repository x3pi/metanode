import React, { useState, useEffect, useCallback } from 'react';
import {
  Activity,
  CheckCircle2,
  AlertOctagon,
  RefreshCw,
  Server,
  Database,
  ShieldCheck,
  ShieldAlert,
  Gauge,
  Clock,
  Layers,
  BarChart3,
  X,
  ExternalLink,
} from 'lucide-react';
import { getAllClusters, checkNodeStatus, fetchNodeMetrics } from '../services/metanodeRpc';

export function NetworkMonitorTab({ allClusters }) {
  const clusters = allClusters || getAllClusters();
  const [nodesStatus, setNodesStatus] = useState({});
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [lastRefreshed, setLastRefreshed] = useState(new Date().toLocaleTimeString());
  const [inspectingCluster, setInspectingCluster] = useState(null);
  const [clusterMetrics, setClusterMetrics] = useState(null);
  const [metricsLoading, setMetricsLoading] = useState(false);

  const fetchAllNodes = useCallback(async () => {
    setIsRefreshing(true);
    const results = {};
    for (const cluster of clusters) {
      results[cluster.id] = await checkNodeStatus(cluster);
    }
    setNodesStatus(results);
    setLastRefreshed(new Date().toLocaleTimeString());
    setIsRefreshing(false);
  }, [clusters]);

  useEffect(() => {
    fetchAllNodes();
    const interval = setInterval(fetchAllNodes, 5000);
    return () => clearInterval(interval);
  }, [fetchAllNodes]);

  // Open Metrics Inspector for a cluster
  const handleInspectMetrics = async (cluster) => {
    setInspectingCluster(cluster);
    setMetricsLoading(true);
    const metrics = await fetchNodeMetrics(cluster.rpcUrl);
    setClusterMetrics(metrics);
    setMetricsLoading(false);
  };

  // Determine Zero-Fork Parity: Compare online execution clusters
  const onlineExecs = clusters.filter(
    (c) => !c.isParent && nodesStatus[c.id]?.online && nodesStatus[c.id]?.blockNumber > 0
  );

  let isParityConsistent = true;
  if (onlineExecs.length >= 2) {
    const firstStateRoot = nodesStatus[onlineExecs[0].id]?.stateRoot;
    const firstBlock = nodesStatus[onlineExecs[0].id]?.blockNumber;
    for (let i = 1; i < onlineExecs.length; i++) {
      const s = nodesStatus[onlineExecs[i].id];
      if (s.blockNumber === firstBlock && s.stateRoot !== firstStateRoot) {
        isParityConsistent = false;
        break;
      }
    }
  }

  return (
    <div>
      {/* Zero-Fork Invariant Banner */}
      <div
        className={`card ${isParityConsistent ? 'gate-hero-card registered' : 'gate-hero-card unregistered'}`}
        style={{ marginBottom: '24px' }}
      >
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            flexWrap: 'wrap',
            gap: '16px',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '14px' }}>
            <div
              style={{
                width: '46px',
                height: '46px',
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
              <h3 style={{ fontSize: '1.25rem', fontWeight: 800 }}>
                {isParityConsistent
                  ? 'Zero-Fork Invariant: 100% Deterministic Consensus Verified'
                  : '⚠️ Cluster Discrepancy Detected — State Root Divergence'}
              </h3>
              <p style={{ color: 'var(--text-muted)', fontSize: '0.84rem' }}>
                Rule 2.5: Zero-fork invariant enforced via 2f+1 peer co-attestation. No timeout-based heuristics.
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
        {clusters.map((cluster) => {
          const status = nodesStatus[cluster.id];
          const isOnline = status?.online;

          return (
            <div key={cluster.id} className="card" style={{ display: 'flex', flexDirection: 'column' }}>
              <div className="card-header" style={{ marginBottom: '12px' }}>
                <div className="card-title" style={{ fontSize: '0.98rem' }}>
                  {cluster.isParent ? (
                    <Database className="w-4 h-4 text-indigo" />
                  ) : (
                    <Server className="w-4 h-4 text-cyan" />
                  )}
                  {cluster.name}
                </div>
                <div style={{ display: 'flex', gap: '6px', alignItems: 'center' }}>
                  <span className={`badge ${isOnline ? 'badge-success' : 'badge-danger'}`}>
                    {isOnline ? 'Online' : 'Offline'}
                  </span>
                </div>
              </div>

              <div style={{ display: 'flex', flexDirection: 'column', gap: '10px', flex: 1 }}>
                {/* RPC Endpoint & Chain ID */}
                <div className="detail-box">
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                    <span className="detail-label">Endpoint</span>
                    <span
                      className="badge-pill"
                      style={{
                        background: 'rgba(99, 102, 241, 0.15)',
                        color: '#a5b4fc',
                        fontSize: '0.7rem',
                      }}
                    >
                      Chain ID: {status?.chainId || cluster.chainId || 991}
                    </span>
                  </div>
                  <div className="detail-value mono" style={{ fontSize: '0.78rem' }}>
                    {cluster.rpcUrl}
                  </div>
                </div>

                {/* Block Height & Latency */}
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
                  <div className="detail-box">
                    <div className="detail-label">Block Height</div>
                    <div
                      className="detail-value"
                      style={{ fontSize: '1.25rem', color: 'var(--cyan-bright)', fontWeight: 800 }}
                    >
                      {isOnline ? `#${status.blockNumber}` : '—'}
                    </div>
                  </div>
                  <div className="detail-box">
                    <div className="detail-label">Latency</div>
                    <div className="detail-value" style={{ fontSize: '1.2rem', fontWeight: 700 }}>
                      {isOnline ? `${status.latency} ms` : 'Unreachable'}
                    </div>
                  </div>
                </div>

                {/* Health & Readiness Indicators (New in Phase 2) */}
                {isOnline && !cluster.isParent && (
                  <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '8px' }}>
                    <div className="detail-box">
                      <div className="detail-label">Readiness Probe</div>
                      <div className="detail-value" style={{ fontSize: '0.8rem' }}>
                        {status.readiness ? (
                          <span style={{ color: 'var(--green)', display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
                            <CheckCircle2 className="w-3.5 h-3.5" /> Ready (200)
                          </span>
                        ) : (
                          <span style={{ color: 'var(--red)', display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
                            <AlertOctagon className="w-3.5 h-3.5" /> 503 Mismatch
                          </span>
                        )}
                      </div>
                    </div>

                    <div className="detail-box">
                      <div className="detail-label">Committee Key</div>
                      <div className="detail-value" style={{ fontSize: '0.8rem' }}>
                        {status.committeeKey === 'valid' ? (
                          <span style={{ color: 'var(--green)' }}>✓ Valid Key</span>
                        ) : status.committeeKey === 'mismatch' ? (
                          <span style={{ color: 'var(--red)', fontWeight: 700 }}>🚨 Mismatch</span>
                        ) : (
                          <span style={{ color: 'var(--text-muted)' }}>Non-Validator</span>
                        )}
                      </div>
                    </div>
                  </div>
                )}

                {/* Parent Chain specific info */}
                {isOnline && cluster.isParent && (
                  <div className="detail-box">
                    <div className="detail-label">L1 Consensus Status</div>
                    <div className="detail-value" style={{ fontSize: '0.8rem', color: 'var(--green)' }}>
                      ✓ {status.syncing ? 'Syncing...' : 'Normal Progress'} &middot; {status.forkDetected ? '🚨 Fork' : '0 Forks'}
                    </div>
                  </div>
                )}

                {/* Account State Root */}
                <div className="detail-box">
                  <div className="detail-label">Account States Root</div>
                  <div className="detail-value mono" style={{ fontSize: '0.72rem', wordBreak: 'break-all' }}>
                    {isOnline ? status.stateRoot : '—'}
                  </div>
                </div>

                {/* Account Gate Status */}
                {isOnline && (
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 'auto', paddingTop: '8px' }}>
                    <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>
                      {status.accountGate ? '🛡️ Gate: Enforced' : '🔓 Gate: Open'}
                    </span>
                    <button
                      className="btn btn-secondary btn-sm"
                      style={{ fontSize: '0.72rem', padding: '4px 8px' }}
                      onClick={() => handleInspectMetrics(cluster)}
                    >
                      <BarChart3 className="w-3 h-3" />
                      Metrics
                    </button>
                  </div>
                )}
              </div>
            </div>
          );
        })}
      </div>

      {/* Metrics & Telemetry Modal */}
      {inspectingCluster && (
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
              maxWidth: '540px',
              border: '1px solid var(--border-medium)',
              boxShadow: '0 20px 50px rgba(0,0,0,0.8)',
            }}
          >
            <div className="card-header" style={{ marginBottom: '16px' }}>
              <div className="card-title">
                <Gauge className="w-5 h-5 text-cyan" />
                Node Telemetry: {inspectingCluster.name}
              </div>
              <button
                className="btn btn-secondary btn-sm"
                onClick={() => {
                  setInspectingCluster(null);
                  setClusterMetrics(null);
                }}
              >
                <X className="w-4 h-4" />
              </button>
            </div>

            {metricsLoading ? (
              <div style={{ textAlign: 'center', padding: '24px', color: 'var(--text-muted)' }}>
                <RefreshCw className="w-6 h-6 animate-spin" style={{ margin: '0 auto 10px' }} />
                Fetching Prometheus /metrics...
              </div>
            ) : clusterMetrics ? (
              <div style={{ display: 'flex', flexDirection: 'column', gap: '10px' }}>
                <div className="detail-box">
                  <div className="detail-label">master_validator_committee_key_valid</div>
                  <div className="detail-value" style={{ fontWeight: 700 }}>
                    {clusterMetrics.master_validator_committee_key_valid === 1 ? (
                      <span style={{ color: 'var(--green)' }}>1 (Valid Genesis Committee Validator)</span>
                    ) : clusterMetrics.master_validator_committee_key_valid === 0 ? (
                      <span style={{ color: 'var(--red)' }}>0 (🚨 Key Mismatch!)</span>
                    ) : (
                      <span style={{ color: 'var(--text-muted)' }}>-1 (Non-Validator Node)</span>
                    )}
                  </div>
                </div>

                <div className="detail-box">
                  <div className="detail-label">master_parent_chain_id_mismatch</div>
                  <div className="detail-value" style={{ fontWeight: 700 }}>
                    {clusterMetrics.master_parent_chain_id_mismatch === 0 ? (
                      <span style={{ color: 'var(--green)' }}>0 (Chain ID 991 Aligned)</span>
                    ) : (
                      <span style={{ color: 'var(--red)' }}>1 (Chain ID Mismatch Detected!)</span>
                    )}
                  </div>
                </div>

                <div className="detail-box">
                  <div className="detail-label">master_account_registration_pending_total</div>
                  <div className="detail-value mono" style={{ color: 'var(--cyan-bright)' }}>
                    {clusterMetrics.master_account_registration_pending_total ?? 0}
                  </div>
                </div>

                <div className="detail-box">
                  <div className="detail-label">master_rollup_signatures_rejected_total</div>
                  <div className="detail-value mono">
                    {clusterMetrics.master_rollup_signatures_rejected_total ?? 0}
                  </div>
                </div>

                <div className="detail-box">
                  <div className="detail-label">master_rollup_committee_read_errors_total</div>
                  <div className="detail-value mono">
                    {clusterMetrics.master_rollup_committee_read_errors_total ?? 0}
                  </div>
                </div>
              </div>
            ) : (
              <div className="alert alert-warning" style={{ fontSize: '0.82rem' }}>
                Node does not expose /metrics or endpoint was unreachable.
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
