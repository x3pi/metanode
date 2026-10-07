import React, { useState, useEffect, useRef } from 'react';
import { ArrowLeftRight, Clock, Layers, Sparkles, AlertCircle } from 'lucide-react';
import { getAllClusters, sendCrossChainTransfer, callJsonRpc } from '../services/metanodeRpc';

export function RollupTransferTab({ account, accountInfo, allClusters }) {
  const clusters = allClusters || getAllClusters();
  const execClusters = clusters.filter((c) => c.isExec);

  const [srcClusterId, setSrcClusterId] = useState(execClusters[0]?.id || 'exec1');
  const [destClusterId, setDestClusterId] = useState(execClusters[1]?.id || 'exec2');
  const [recipient, setRecipient] = useState(account || '');
  const [amount, setAmount] = useState('0.5');
  const [isProcessing, setIsProcessing] = useState(false);
  const [transferError, setTransferError] = useState(null);
  const [transfers, setTransfers] = useState([]);
  const trackerTimers = useRef(new Set());

  useEffect(() => () => {
    trackerTimers.current.forEach((timer) => clearInterval(timer));
    trackerTimers.current.clear();
  }, []);

  // Keep recipient updated when account connects
  useEffect(() => {
    if (account && !recipient) {
      setRecipient(account);
    }
  }, [account]);

  const srcClusterObj = execClusters.find((c) => c.id === srcClusterId) || execClusters[0];
  const destClusterObj = execClusters.find((c) => c.id === destClusterId) || execClusters[1];

  const trackTransfer = (transfer, expectedBalance) => {
    const check = async () => {
      try {
        const receipt = await callJsonRpc(srcClusterObj.rpcUrl, 'eth_getTransactionReceipt', [transfer.id]);
        if (receipt?.status && receipt.status !== '0x1') {
          setTransfers((prev) => prev.map((item) => (
            item.id === transfer.id ? { ...item, failed: true, status: 'Source transaction failed' } : item
          )));
          return true;
        }

        const balanceHex = await callJsonRpc(destClusterObj.rpcUrl, 'eth_getBalance', [transfer.recipient, 'latest']);
        const destinationBalance = BigInt(balanceHex);
        const sourceConfirmed = receipt?.status === '0x1';
        const credited = destinationBalance >= expectedBalance;

        setTransfers((prev) => prev.map((item) => (
          item.id === transfer.id
            ? {
                ...item,
                step: credited ? 3 : (sourceConfirmed ? 2 : 1),
                status: credited
                  ? 'Destination balance credited'
                  : (sourceConfirmed ? 'Source confirmed; waiting for Parent Chain relay' : 'Submitted; waiting for source confirmation'),
                destinationBalance: destinationBalance.toString(),
              }
            : item
        )));
        return credited;
      } catch (error) {
        setTransfers((prev) => prev.map((item) => (
          item.id === transfer.id ? { ...item, status: `Polling: ${error.message}` } : item
        )));
        return false;
      }
    };

    const timer = setInterval(async () => {
      if (await check()) {
        clearInterval(timer);
        trackerTimers.current.delete(timer);
      }
    }, 2000);
    trackerTimers.current.add(timer);
    check().then((complete) => {
      if (complete) {
        clearInterval(timer);
        trackerTimers.current.delete(timer);
      }
    });
  };

  const handleCreateRollupTransfer = async (e) => {
    e.preventDefault();
    if (!recipient || !recipient.startsWith('0x') || recipient.length !== 42) {
      alert('Please enter a valid 20-byte destination address (0x...)');
      return;
    }

    setIsProcessing(true);
    setTransferError(null);

    const amountFloat = parseFloat(amount);
    if (isNaN(amountFloat) || amountFloat <= 0) {
      setTransferError('Invalid transfer amount');
      setIsProcessing(false);
      return;
    }

    // Convert amount in MTN to Wei Hex
    const amountWei = BigInt(Math.floor(amountFloat * 1e18));
    const valueHex = `0x${amountWei.toString(16)}`;

    try {
      if (!srcClusterObj?.rpcUrl || !destClusterObj?.rpcUrl || srcClusterObj.id === destClusterObj.id) {
        throw new Error('Choose two different online execution clusters.');
      }
      const balanceHex = await callJsonRpc(destClusterObj.rpcUrl, 'eth_getBalance', [recipient, 'latest']);
      const expectedBalance = BigInt(balanceHex) + amountWei;
      const txHash = await sendCrossChainTransfer(srcClusterObj.rpcUrl, recipient, valueHex);
      if (!txHash) throw new Error('Source cluster did not return a transaction hash.');

      const newTransfer = {
        id: txHash,
        fromCluster: srcClusterObj.name,
        toCluster: destClusterObj.name,
        recipient,
        amount: `${amount} MTN`,
        step: 1,
        mode: 'Devnet node-signed transfer',
        status: 'Submitted; waiting for source confirmation',
      };
      setTransfers((prev) => [newTransfer, ...prev]);
      trackTransfer(newTransfer, expectedBalance);
    } catch (rpcErr) {
      setTransferError(`Cross-cluster transfer was not submitted: ${rpcErr.message}`);
    } finally {
      setIsProcessing(false);
    }
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
            Bridge assets between execution clusters via Parent Chain Float Model &amp; BLS f+1 Co-Attestation
          </div>
        </div>

        {transferError && (
          <div className="alert alert-warning" style={{ fontSize: '0.82rem' }}>
            <AlertCircle className="w-4 h-4" />
            {transferError}
          </div>
        )}

        <form onSubmit={handleCreateRollupTransfer}>
          <div className="grid-2">
            <div className="form-group">
              <label className="form-label">Source Cluster</label>
              <select
                className="form-select"
                value={srcClusterId}
                onChange={(e) => {
                  setSrcClusterId(e.target.value);
                  if (e.target.value === destClusterId) {
                    const other = execClusters.find((c) => c.id !== e.target.value);
                    if (other) setDestClusterId(other.id);
                  }
                }}
              >
                {execClusters.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </div>
            <div className="form-group">
              <label className="form-label">Destination Cluster</label>
              <select
                className="form-select"
                value={destClusterId}
                onChange={(e) => setDestClusterId(e.target.value)}
              >
                {execClusters.map((c) => (
                  <option key={c.id} value={c.id} disabled={c.id === srcClusterId}>
                    {c.name}
                  </option>
                ))}
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
              step="0.01"
              min="0.01"
              className="form-input"
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
              required
            />
            <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: '4px' }}>
              Source Balance: {accountInfo?.balanceMtn || '0.0000'} MTN
            </div>
          </div>

          <button
            type="submit"
            className="btn btn-primary"
            style={{ width: '100%', marginTop: '8px' }}
            disabled={isProcessing || execClusters.length < 2}
          >
            {isProcessing ? (
              <>
                <Clock className="w-4 h-4 animate-spin" />
                Dispatching Cross-Cluster Transfer...
              </>
            ) : (
              <>
                <Sparkles className="w-4 h-4" />
                Initiate Cross-Cluster Transfer
              </>
            )}
          </button>
        </form>

        <div style={{ marginTop: '16px', padding: '12px', background: 'rgba(15, 23, 42, 0.5)', borderRadius: '8px', fontSize: '0.76rem', color: 'var(--text-muted)' }}>
          <strong>Devnet sender:</strong> this RPC currently signs with the funded node test account, not MetaMask.
          Register the recipient with the destination cluster first. The tracker advances only after source receipt and
          destination balance RPC checks; it never simulates success.
        </div>
      </div>

      {/* Transfer Timeline & Active Tracker */}
      <div className="card">
        <div className="card-header">
          <div className="card-title">
            <Layers className="w-5 h-5 text-cyan" />
            Transfer Lifecycle &amp; History
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
                <span
                  className="badge-pill"
                  style={{
                    background: 'rgba(99, 102, 241, 0.15)',
                    color: '#a5b4fc',
                    fontSize: '0.7rem',
                  }}
                >
                  {tx.mode || 'Co-Attested'}
                </span>
              </div>

              <div style={{ fontSize: '0.9rem', fontWeight: 600, marginBottom: '12px' }}>
                {tx.amount} &nbsp;({tx.fromCluster} &rarr; {tx.toCluster})
              </div>

              <div style={{ fontSize: '0.76rem', color: tx.failed ? 'var(--danger)' : 'var(--text-muted)', marginBottom: '10px' }}>
                {tx.status}
              </div>

              {/* 3-Step Lifecycle Visualizer */}
              <div className="steps-container" style={{ margin: '8px 0 0' }}>
                <div className={`step-item ${tx.step >= 1 ? 'active' : ''} ${tx.step > 1 ? 'completed' : ''}`}>
                  <div className="step-circle">{tx.step > 1 ? '✓' : '1'}</div>
                  <div className="step-label">1. Submitted</div>
                </div>
                <div className={`step-item ${tx.step >= 2 ? 'active' : ''} ${tx.step > 2 ? 'completed' : ''}`}>
                  <div className="step-circle">{tx.step > 2 ? '✓' : '2'}</div>
                  <div className="step-label">2. Source Confirmed</div>
                </div>
                <div className={`step-item ${tx.step >= 3 ? 'completed' : ''}`}>
                  <div className="step-circle">{tx.step >= 3 ? '✓' : '3'}</div>
                  <div className="step-label">3. Dest Credited</div>
                </div>
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
