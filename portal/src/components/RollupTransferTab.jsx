import React, { useState } from 'react';
import { ArrowLeftRight, CheckCircle2, Clock, Layers, Sparkles } from 'lucide-react';
import { PRESET_CLUSTERS } from '../services/metanodeRpc';

export function RollupTransferTab({ account, accountInfo }) {
  const [srcCluster, setSrcCluster] = useState('exec1');
  const [destCluster, setDestCluster] = useState('exec2');
  const [recipient, setRecipient] = useState(account || '');
  const [amount, setAmount] = useState('1.0');
  const [transfers, setTransfers] = useState([
    {
      id: 'msg-0x9f182c...48a2',
      fromCluster: 'Cluster 1',
      toCluster: 'Cluster 2',
      recipient: account || '0x71C...3972',
      amount: '5.0 MTN',
      step: 3, // Complete
      time: '10 mins ago',
    },
    {
      id: 'msg-0x4a12eb...90cd',
      fromCluster: 'Cluster 2',
      toCluster: 'Cluster 1',
      recipient: account || '0x71C...3972',
      amount: '2.5 MTN',
      step: 2, // Relayed to Parent
      time: '1 min ago',
    },
  ]);

  const handleCreateRollupTransfer = (e) => {
    e.preventDefault();
    if (!account) {
      alert('Please connect your wallet first.');
      return;
    }

    const newTransfer = {
      id: `msg-0x${Math.random().toString(16).slice(2, 10)}...${Math.random().toString(16).slice(2, 6)}`,
      fromCluster: srcCluster === 'exec1' ? 'Cluster 1' : 'Cluster 2',
      toCluster: destCluster === 'exec1' ? 'Cluster 1' : 'Cluster 2',
      recipient,
      amount: `${amount} MTN`,
      step: 1, // Initiated
      time: 'Just now',
    };

    setTransfers([newTransfer, ...transfers]);

    // Simulate multi-step timeline
    setTimeout(() => {
      setTransfers((prev) =>
        prev.map((t) => (t.id === newTransfer.id ? { ...t, step: 2 } : t))
      );
    }, 3000);

    setTimeout(() => {
      setTransfers((prev) =>
        prev.map((t) => (t.id === newTransfer.id ? { ...t, step: 3 } : t))
      );
    }, 7000);
  };

  return (
    <div className="grid-2">
      {/* Initiate Cross-Cluster Transfer Form */}
      <div className="card">
        <div className="card-header">
          <div className="card-title">
            <ArrowLeftRight className="w-5 h-5 text-cyan" />
            Cross-Cluster Rollup Transfer
          </div>
          <div className="card-subtitle">
            Bridge assets between execution clusters via Parent Chain Float Model
          </div>
        </div>

        <form onSubmit={handleCreateRollupTransfer}>
          <div className="grid-2">
            <div className="form-group">
              <label className="form-label">Source Cluster</label>
              <select
                className="form-select"
                value={srcCluster}
                onChange={(e) => {
                  setSrcCluster(e.target.value);
                  if (e.target.value === destCluster) {
                    setDestCluster(e.target.value === 'exec1' ? 'exec2' : 'exec1');
                  }
                }}
              >
                <option value="exec1">Execution Cluster 1</option>
                <option value="exec2">Execution Cluster 2</option>
              </select>
            </div>
            <div className="form-group">
              <label className="form-label">Destination Cluster</label>
              <select
                className="form-select"
                value={destCluster}
                onChange={(e) => setDestCluster(e.target.value)}
              >
                <option value="exec1" disabled={srcCluster === 'exec1'}>
                  Execution Cluster 1
                </option>
                <option value="exec2" disabled={srcCluster === 'exec2'}>
                  Execution Cluster 2
                </option>
              </select>
            </div>
          </div>

          <div className="form-group">
            <label className="form-label">Recipient Address on Destination</label>
            <input
              type="text"
              className="form-input mono"
              value={recipient}
              onChange={(e) => setRecipient(e.target.value)}
              placeholder="0x..."
              required
            />
          </div>

          <div className="form-group">
            <label className="form-label">Amount (MTN)</label>
            <input
              type="number"
              step="0.1"
              min="0.1"
              className="form-input"
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
              required
            />
          </div>

          <button type="submit" className="btn btn-primary" style={{ width: '100%', marginTop: '8px' }}>
            <Sparkles className="w-4 h-4" />
            Initiate Cross-Cluster Transfer
          </button>
        </form>
      </div>

      {/* Transfer Timeline & Active Tracker */}
      <div className="card">
        <div className="card-header">
          <div className="card-title">
            <Layers className="w-5 h-5 text-cyan" />
            Transfer Lifecycle & History
          </div>
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
          {transfers.map((tx) => (
            <div
              key={tx.id}
              style={{
                background: 'rgba(15, 23, 42, 0.7)',
                border: '1px solid var(--border-subtle)',
                borderRadius: '12px',
                padding: '14px',
              }}
            >
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '8px' }}>
                <span className="mono" style={{ fontSize: '0.82rem', color: 'var(--cyan-bright)' }}>
                  {tx.id}
                </span>
                <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>{tx.time}</span>
              </div>

              <div style={{ fontSize: '0.9rem', fontWeight: 600, marginBottom: '12px' }}>
                {tx.amount} &nbsp;({tx.fromCluster} ➔ {tx.toCluster})
              </div>

              {/* 3-Step Lifecycle Visualizer */}
              <div className="steps-container" style={{ margin: '8px 0 0' }}>
                <div className={`step-item ${tx.step >= 1 ? 'active' : ''} ${tx.step > 1 ? 'completed' : ''}`}>
                  <div className="step-circle">{tx.step > 1 ? '✓' : '1'}</div>
                  <div className="step-label">1. Source Locked</div>
                </div>
                <div className={`step-item ${tx.step >= 2 ? 'active' : ''} ${tx.step > 2 ? 'completed' : ''}`}>
                  <div className="step-circle">{tx.step > 2 ? '✓' : '2'}</div>
                  <div className="step-label">2. Parent Relayed</div>
                </div>
                <div className={`step-item ${tx.step >= 3 ? 'completed' : ''}`}>
                  <div className="step-circle">{tx.step >= 3 ? '✓' : '3'}</div>
                  <div className="step-label">3. Dest Claimed</div>
                </div>
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
