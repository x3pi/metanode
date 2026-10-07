import React, { useState } from 'react';
import { Send, AlertTriangle, CheckCircle, Clock, ExternalLink, ArrowRight, ShieldCheck } from 'lucide-react';
import { callJsonRpc, switchOrAddNetwork, sendTransactionWithPrivateKey } from '../services/metanodeRpc';

export function TransferTab({
  account,
  accountInfo,
  selectedCluster,
  onRefresh,
  walletPrivateKey,
  onNavigateToGate,
}) {
  const [recipient, setRecipient] = useState('');
  const [amount, setAmount] = useState('0.1');
  const [isSending, setIsSending] = useState(false);
  const [txReceipt, setTxReceipt] = useState(null);
  const [txError, setTxError] = useState(null);

  const gateEnforced = accountInfo?.gateEnforced;
  const isGateBlocked = gateEnforced && !accountInfo?.parentRegistered;

  const handleSendTransaction = async (e) => {
    e.preventDefault();
    if (!account) {
      alert('Please connect your wallet first.');
      return;
    }
    if (!recipient || !recipient.startsWith('0x') || recipient.length !== 42) {
      alert('Please enter a valid 20-byte recipient address (0x...)');
      return;
    }

    setIsSending(true);
    setTxError(null);
    setTxReceipt(null);

    try {
      let txHash;
      if (walletPrivateKey) {
        // Direct private key mode: sign and broadcast directly with ethers
        txHash = await sendTransactionWithPrivateKey(
          selectedCluster.rpcUrl,
          walletPrivateKey,
          recipient,
          amount
        );
      } else {
        // MetaMask mode:
        // 1. Ensure wallet is on correct Chain ID
        const targetChainId = selectedCluster.chainId || 991;
        const currentChainIdHex = await window.ethereum.request({ method: 'eth_chainId' });
        const currentChainId = parseInt(currentChainIdHex, 16);

        if (currentChainId !== targetChainId) {
          try {
            await switchOrAddNetwork(selectedCluster);
          } catch (switchErr) {
            throw new Error(`Please switch your wallet network to Chain ID ${targetChainId}`);
          }
        }

        // 2. Convert amount in MTN to Wei Hex
        const amountWei = BigInt(Math.floor(parseFloat(amount) * 1e18));
        const valueHex = `0x${amountWei.toString(16)}`;

        const txParams = {
          from: account,
          to: recipient,
          value: valueHex,
        };

        // 3. Request MetaMask to send transaction
        txHash = await window.ethereum.request({
          method: 'eth_sendTransaction',
          params: [txParams],
        });
      }

      setTxReceipt({
        hash: txHash,
        recipient,
        amount,
        status: 'Submitted',
        timestamp: new Date().toLocaleTimeString(),
      });

      // 4. Poll for receipt
      setTimeout(async () => {
        try {
          const receipt = await callJsonRpc(selectedCluster.rpcUrl, 'eth_getTransactionReceipt', [txHash]);
          if (receipt) {
            setTxReceipt((prev) => ({
              ...prev,
              status: receipt.status === '0x1' ? 'Success' : 'Failed',
              blockNumber: parseInt(receipt.blockNumber, 16),
            }));
          }
        } catch (_) {}
        if (onRefresh) onRefresh();
      }, 2500);
    } catch (err) {
      console.error('Send error:', err);
      // Detect Code 69 / AccountNotRegistered specifically
      if (err.message?.includes('account not registered') || err.message?.includes('69')) {
        setTxError('❌ Transaction rejected by Account Registration Gate (Code 69): Sender is not registered on Parent Chain.');
      } else {
        setTxError(err.message || 'Transaction submission failed');
      }
    } finally {
      setIsSending(false);
    }
  };

  return (
    <div className="grid-2">
      {/* Send Form */}
      <div className="card">
        <div className="card-header">
          <div className="card-title">
            <Send className="w-5 h-5 text-cyan" />
            Send MTN Transaction
          </div>
          <div className="card-subtitle">
            Transfer funds on {selectedCluster.name} (Chain ID: {selectedCluster.chainId || 991})
          </div>
        </div>

        {isGateBlocked && (
          <div className="alert alert-warning">
            <AlertTriangle className="w-5 h-5 text-amber" style={{ flexShrink: 0 }} />
            <div>
              <strong>Account Gate Enforcement:</strong> Your account is currently <strong>unregistered</strong> on the Parent Chain.
              Transactions sent from this wallet will be rejected by the node with error <code>AccountNotRegistered (Code 69)</code>.
              <div style={{ marginTop: '8px' }}>
                <button
                  className="btn btn-primary btn-sm"
                  onClick={onNavigateToGate}
                  style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}
                >
                  <ShieldCheck className="w-3.5 h-3.5" />
                  Go to Account Gate Onboarding
                  <ArrowRight className="w-3 h-3" />
                </button>
              </div>
            </div>
          </div>
        )}

        {txError && <div className="alert alert-danger">{txError}</div>}

        {txReceipt && (
          <div className="alert alert-success">
            <CheckCircle className="w-5 h-5 text-green" style={{ flexShrink: 0 }} />
            <div>
              <strong>Transaction Broadcast!</strong>
              <div style={{ fontFamily: 'var(--font-mono)', fontSize: '0.8rem', marginTop: '4px', wordBreak: 'break-all' }}>
                Hash: {txReceipt.hash}
              </div>
              <div style={{ fontSize: '0.8rem', marginTop: '2px' }}>
                Status: {txReceipt.status} {txReceipt.blockNumber && `(Block #${txReceipt.blockNumber})`}
              </div>
            </div>
          </div>
        )}

        <form onSubmit={handleSendTransaction}>
          <div className="form-group">
            <label className="form-label">Recipient Address (0x...)</label>
            <input
              type="text"
              className="form-input mono"
              placeholder="0x71C...3972"
              value={recipient}
              onChange={(e) => setRecipient(e.target.value)}
              required
            />
          </div>

          <div className="form-group">
            <label className="form-label">Amount (MTN)</label>
            <div style={{ position: 'relative' }}>
              <input
                type="number"
                step="0.0001"
                min="0.0001"
                className="form-input"
                placeholder="0.1"
                value={amount}
                onChange={(e) => setAmount(e.target.value)}
                required
              />
              <span
                style={{
                  position: 'absolute',
                  right: '14px',
                  top: '10px',
                  color: 'var(--text-muted)',
                  fontSize: '0.85rem',
                  fontWeight: 600,
                }}
              >
                MTN
              </span>
            </div>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: '4px' }}>
              Available Balance: {accountInfo?.balanceMtn || '0.0000'} MTN
            </div>
          </div>

          <button
            type="submit"
            className="btn btn-primary"
            style={{ width: '100%', marginTop: '10px' }}
            disabled={isSending || !account}
          >
            {isSending ? (
              <>
                <Clock className="w-4 h-4 animate-spin" />
                Signing &amp; Sending...
              </>
            ) : (
              <>
                <Send className="w-4 h-4" />
                Submit Transaction
              </>
            )}
          </button>
        </form>
      </div>

      {/* Account Info & Security Details */}
      <div className="card">
        <div className="card-header">
          <div className="card-title">Account &amp; Network Quick Stats</div>
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
          <div className="detail-box">
            <div className="detail-label">Current Cluster RPC</div>
            <div className="detail-value mono" style={{ fontSize: '0.8rem' }}>
              {selectedCluster.rpcUrl}
            </div>
          </div>
          <div className="detail-box">
            <div className="detail-label">EIP-155 Chain ID</div>
            <div className="detail-value mono" style={{ color: 'var(--cyan-bright)', fontWeight: 700 }}>
              {selectedCluster.chainId || 991}
            </div>
          </div>
          <div className="detail-box">
            <div className="detail-label">Mempool Nonce</div>
            <div className="detail-value mono">{accountInfo?.nonce ?? 0}</div>
          </div>
          <div className="detail-box">
            <div className="detail-label">Signature Mode</div>
            <div className="detail-value">
              <span className="badge badge-info">secp256k1 (ETH Replay Protected)</span>
            </div>
          </div>
          <div className="detail-box">
            <div className="detail-label">Gate Verification Seam</div>
            <div className="detail-value">
              {!gateEnforced ? (
                <span className="badge badge-info">Open Cluster (Gate Not Required)</span>
              ) : accountInfo?.parentRegistered ? (
                <span className="badge badge-success">✓ Authorized by Parent Chain</span>
              ) : (
                <span className="badge badge-danger">✗ Restricted by Account Gate</span>
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
