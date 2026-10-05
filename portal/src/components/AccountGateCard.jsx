import React, { useState } from 'react';
import {
  ShieldCheck,
  ShieldAlert,
  ShieldX,
  KeyRound,
  ArrowRight,
  CheckCircle2,
  Clock,
  ExternalLink,
  Layers,
  Sparkles,
} from 'lucide-react';
import { PRESET_CLUSTERS, submitRegistrationToParent } from '../services/metanodeRpc';

export function AccountGateCard({
  account,
  accountInfo,
  parentRegInfo,
  selectedCluster,
  onRefresh,
}) {
  const [targetCluster, setTargetCluster] = useState(
    selectedCluster.isExec ? selectedCluster : PRESET_CLUSTERS[0]
  );
  const [isRegistering, setIsRegistering] = useState(false);
  const [currentStep, setCurrentStep] = useState(0); // 0: Idle, 1: Signing, 2: Parent Chain, 3: Syncing, 4: Done
  const [regError, setRegError] = useState(null);

  const isRegisteredOnExec = accountInfo?.parentRegistered;
  const isRegisteredOnParent = parentRegInfo?.registered;

  const handleStartOnboarding = async () => {
    if (!account) {
      alert('Please connect your MetaMask wallet first!');
      return;
    }
    setRegError(null);
    setIsRegistering(true);
    setCurrentStep(1);

    try {
      // Step 1: User Signs Registration Intent with ECDSA
      const clusterKey = targetCluster.clusterKey || PRESET_CLUSTERS[0].clusterKey;
      const message = `REGISTER_ACCOUNT_V1:${account.toLowerCase()}:${clusterKey.toLowerCase()}`;

      let signature;
      try {
        signature = await window.ethereum.request({
          method: 'personal_sign',
          params: [message, account],
        });
      } catch (signErr) {
        throw new Error(`Signature rejected: ${signErr.message}`);
      }

      setCurrentStep(2);

      // Step 2: Submit to Parent Chain
      const parentRpc = 'http://127.0.0.1:8547';
      try {
        await submitRegistrationToParent(parentRpc, account, clusterKey, signature);
      } catch (submitErr) {
        console.warn('Parent RPC call notification:', submitErr.message);
        // Note: Even if mock/devnet endpoints have specific routes, proceed to sync check
      }

      setCurrentStep(3);

      // Step 3: Wait for Execution Cluster RegistrationWorker to relay and apply
      let attempts = 0;
      const interval = setInterval(async () => {
        attempts++;
        if (onRefresh) await onRefresh();

        if (accountInfo?.parentRegistered || attempts >= 8) {
          clearInterval(interval);
          setCurrentStep(4);
          setIsRegistering(false);
        }
      }, 1500);
    } catch (err) {
      setRegError(err.message);
      setIsRegistering(false);
      setCurrentStep(0);
    }
  };

  if (!account) {
    return (
      <div className="card text-center" style={{ padding: '48px 24px', textAlign: 'center' }}>
        <div
          style={{
            width: '64px',
            height: '64px',
            borderRadius: '50%',
            background: 'rgba(6, 182, 212, 0.1)',
            display: 'inline-flex',
            alignItems: 'center',
            justifyContent: 'center',
            marginBottom: '16px',
          }}
        >
          <KeyRound className="w-8 h-8 text-cyan" />
        </div>
        <h2 style={{ fontSize: '1.4rem', fontWeight: 800, marginBottom: '8px' }}>
          Connect Wallet to Check Gate Status
        </h2>
        <p style={{ color: 'var(--text-muted)', maxWidth: '480px', margin: '0 auto 20px' }}>
          Metanode enforces a Parent Chain Account Registration Gate on secp256k1 execution clusters
          to prevent cross-cluster transaction replay attacks.
        </p>
      </div>
    );
  }

  return (
    <div>
      {/* Hero Gate Status Card */}
      <div
        className={`gate-hero-card ${
          isRegisteredOnExec
            ? 'registered'
            : isRegisteredOnParent
            ? 'pending'
            : 'unregistered'
        }`}
      >
        <div className="gate-hero-header">
          <div style={{ display: 'flex', alignItems: 'center', gap: '14px' }}>
            <div
              style={{
                width: '48px',
                height: '48px',
                borderRadius: '12px',
                background: isRegisteredOnExec
                  ? 'rgba(16, 185, 129, 0.2)'
                  : isRegisteredOnParent
                  ? 'rgba(245, 158, 11, 0.2)'
                  : 'rgba(239, 68, 68, 0.2)',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
              }}
            >
              {isRegisteredOnExec ? (
                <ShieldCheck className="w-6 h-6" style={{ color: 'var(--green)' }} />
              ) : isRegisteredOnParent ? (
                <Clock className="w-6 h-6" style={{ color: 'var(--amber)' }} />
              ) : (
                <ShieldX className="w-6 h-6" style={{ color: 'var(--red)' }} />
              )}
            </div>
            <div>
              <div style={{ fontSize: '0.8rem', color: 'var(--text-muted)', textTransform: 'uppercase', fontWeight: 600 }}>
                Parent Chain Account Gate Status
              </div>
              <h2 style={{ fontSize: '1.35rem', fontWeight: 800, color: '#fff' }}>
                {isRegisteredOnExec
                  ? 'Active & Registered — Ready to Transact'
                  : isRegisteredOnParent
                  ? 'Registered on Parent Chain (Syncing to Exec Cluster)'
                  : 'Unregistered — Outgoing Transactions Gated'}
              </h2>
            </div>
          </div>

          <div
            className="gate-status-pill"
            style={{
              background: isRegisteredOnExec
                ? 'rgba(16, 185, 129, 0.15)'
                : isRegisteredOnParent
                ? 'rgba(245, 158, 11, 0.15)'
                : 'rgba(239, 68, 68, 0.15)',
              color: isRegisteredOnExec
                ? '#34d399'
                : isRegisteredOnParent
                ? '#fbbf24'
                : '#f87171',
              border: `1px solid ${
                isRegisteredOnExec
                  ? 'rgba(16, 185, 129, 0.3)'
                  : isRegisteredOnParent
                  ? 'rgba(245, 158, 11, 0.3)'
                  : 'rgba(239, 68, 68, 0.3)'
              }`,
            }}
          >
            <span
              className={`status-dot ${
                isRegisteredOnExec ? 'online' : isRegisteredOnParent ? 'pending' : 'offline'
              }`}
            />
            {isRegisteredOnExec
              ? 'PASS (Gate Open)'
              : isRegisteredOnParent
              ? 'PENDING SYNC'
              : 'BLOCKED (Code 69)'}
          </div>
        </div>

        {/* Detailed Metrics */}
        <div className="details-grid">
          <div className="detail-box">
            <div className="detail-label">Connected Wallet Address</div>
            <div className="detail-value mono">{account}</div>
          </div>
          <div className="detail-box">
            <div className="detail-label">Current Cluster Balance</div>
            <div className="detail-value">
              {accountInfo?.balanceMtn || '0.0000'} <span style={{ color: 'var(--cyan)' }}>MTN</span>
            </div>
          </div>
          <div className="detail-box">
            <div className="detail-label">Account Nonce</div>
            <div className="detail-value mono">{accountInfo?.nonce ?? 0}</div>
          </div>
          <div className="detail-box">
            <div className="detail-label">Parent Registry Seq</div>
            <div className="detail-value mono">
              {parentRegInfo?.registered ? `#${parentRegInfo.seq}` : 'Not registered yet'}
            </div>
          </div>
        </div>
      </div>

      {/* Onboarding Interactive Flow */}
      {!isRegisteredOnExec && (
        <div className="card" style={{ marginTop: '20px' }}>
          <div className="card-header">
            <div>
              <div className="card-title">
                <Sparkles className="w-5 h-5 text-cyan" />
                1-Click Account Gate Onboarding
              </div>
              <div className="card-subtitle">
                Register your secp256k1 address on the Parent Chain to authorize transactions on this execution cluster.
              </div>
            </div>
          </div>

          <div className="alert alert-info">
            <div>
              <strong>Why is registration required?</strong> Under Metanode's Zero-Fork architecture,
              each secp account is bound 1-to-1 with an execution cluster via Parent Chain's AccountRegistry.
              This prevents malicious actors from replaying your signed transactions on another cluster!
            </div>
          </div>

          {regError && <div className="alert alert-danger">{regError}</div>}

          {/* Stepper Progress */}
          {isRegistering && (
            <div className="steps-container">
              <div className={`step-item ${currentStep >= 1 ? 'active' : ''} ${currentStep > 1 ? 'completed' : ''}`}>
                <div className="step-circle">{currentStep > 1 ? '✓' : '1'}</div>
                <div className="step-label">ECDSA Sign</div>
              </div>
              <div className={`step-item ${currentStep >= 2 ? 'active' : ''} ${currentStep > 2 ? 'completed' : ''}`}>
                <div className="step-circle">{currentStep > 2 ? '✓' : '2'}</div>
                <div className="step-label">Parent Chain</div>
              </div>
              <div className={`step-item ${currentStep >= 3 ? 'active' : ''} ${currentStep > 3 ? 'completed' : ''}`}>
                <div className="step-circle">{currentStep > 3 ? '✓' : '3'}</div>
                <div className="step-label">Cluster Sync</div>
              </div>
              <div className={`step-item ${currentStep >= 4 ? 'completed' : ''}`}>
                <div className="step-circle">{currentStep >= 4 ? '✓' : '4'}</div>
                <div className="step-label">Ready</div>
              </div>
            </div>
          )}

          {/* Target Cluster Selector & Action Button */}
          <div className="grid-2" style={{ alignItems: 'flex-end', marginTop: '16px' }}>
            <div className="form-group" style={{ marginBottom: 0 }}>
              <label className="form-label">Authorize for Execution Cluster:</label>
              <select
                className="form-select"
                value={targetCluster.id}
                onChange={(e) => {
                  const c = PRESET_CLUSTERS.find((x) => x.id === e.target.value);
                  if (c) setTargetCluster(c);
                }}
                disabled={isRegistering}
              >
                {PRESET_CLUSTERS.filter((c) => c.isExec).map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name} (ChainID: {c.chainId})
                  </option>
                ))}
              </select>
            </div>

            <button
              className="btn btn-primary"
              style={{ width: '100%', height: '42px' }}
              onClick={handleStartOnboarding}
              disabled={isRegistering}
            >
              {isRegistering ? (
                <>
                  <Clock className="w-4 h-4 animate-spin" />
                  Processing Onboarding...
                </>
              ) : (
                <>
                  Sign & Register on Parent Chain
                  <ArrowRight className="w-4 h-4" />
                </>
              )}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
