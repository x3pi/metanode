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
  Search,
  Check,
} from 'lucide-react';
import {
  getAllClusters,
  submitRegistrationToParent,
  getClusterRegistrationMessage,
  registerAccountOnNode,
  getRegistrationStatusFromNode,
  checkParentRegistration,
} from '../services/metanodeRpc';

export function AccountGateCard({
  account,
  accountInfo,
  parentRegInfo,
  selectedCluster,
  onRefresh,
  allClusters,
}) {
  const clusters = allClusters || getAllClusters();
  const execClusters = clusters.filter((c) => c.isExec);

  const [targetCluster, setTargetCluster] = useState(
    selectedCluster.isExec ? selectedCluster : execClusters[0] || selectedCluster
  );
  const [isRegistering, setIsRegistering] = useState(false);
  const [currentStep, setCurrentStep] = useState(0); // 0: Idle, 1: Signing, 2: Parent Chain Relay, 3: Syncing, 4: Done
  const [regError, setRegError] = useState(null);

  // Address Lookup tool
  const [lookupAddress, setLookupAddress] = useState('');
  const [lookupLoading, setLookupLoading] = useState(false);
  const [lookupResult, setLookupResult] = useState(null);

  const gateEnforced = accountInfo?.gateEnforced;
  const isRegisteredOnExec = accountInfo?.parentRegistered;
  const isRegisteredOnParent = parentRegInfo?.registered;

  const isGatePass = gateEnforced === false || isRegisteredOnExec;

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
      const clusterKey =
        accountInfo?.dynamicClusterKey ||
        targetCluster.clusterKey ||
        '0x944488b425d29336c7913a3b45946adee6b9bfbd0838c6c8f422f4b4277066f26b3da0530c9f9865e6e534a05ae6c128';

      let digest;
      let hashToSign;
      try {
        const msgRes = await getClusterRegistrationMessage(targetCluster.rpcUrl, account);
        if (msgRes) {
          digest = msgRes.digest;
          hashToSign = msgRes.hashToSign;
        }
      } catch (msgErr) {
        console.warn('Could not fetch cluster registration message:', msgErr);
      }

      // MetaMask personal_sign takes (data, address). When data is the raw digest (hex string),
      // MetaMask applies "\x19Ethereum Signed Message:\n" + len + digest
      // which exactly matches the Go backend accounts.TextHash(digest) verification!
      const messageToSign =
        digest ||
        hashToSign ||
        `REGISTER_ACCOUNT_V1:${account.toLowerCase()}:${clusterKey.toLowerCase()}`;

      let signature;
      try {
        signature = await window.ethereum.request({
          method: 'personal_sign',
          params: [messageToSign, account],
        });
      } catch (signErr) {
        throw new Error(`Signature rejected: ${signErr.message}`);
      }

      setCurrentStep(2);

      // Step 2: Submit to Execution Node Relay (Gasless for user)
      try {
        const relayRes = await registerAccountOnNode(targetCluster.rpcUrl, account, signature);
        console.log('Node relay registration submitted:', relayRes);
      } catch (nodeErr) {
        console.warn('Node relay registration notice, attempting direct parent route:', nodeErr.message);
        const parentCluster = clusters.find((c) => c.isParent);
        if (parentCluster) {
          try {
            await submitRegistrationToParent(parentCluster.rpcUrl, account, clusterKey, signature);
          } catch (_) {}
        }
      }

      setCurrentStep(3);

      // Step 3: Wait for Execution Cluster RegistrationWorker to relay and apply
      let attempts = 0;
      const interval = setInterval(async () => {
        attempts++;
        try {
          const statusRes = await getRegistrationStatusFromNode(targetCluster.rpcUrl, account);
          if (statusRes && statusRes.status === 'CONFIRMED') {
            clearInterval(interval);
            if (onRefresh) await onRefresh();
            setCurrentStep(4);
            setIsRegistering(false);
            return;
          }
          if (statusRes && statusRes.status === 'REJECTED') {
            clearInterval(interval);
            setRegError(
              `Registration rejected: Account is already registered on cluster ${
                statusRes.homeCluster || 'another cluster'
              }`
            );
            setIsRegistering(false);
            return;
          }
        } catch (_) {}

        if (onRefresh) await onRefresh();

        if (accountInfo?.parentRegistered || attempts >= 10) {
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

  const handleLookupAddress = async (e) => {
    e.preventDefault();
    if (!lookupAddress || !lookupAddress.startsWith('0x') || lookupAddress.length !== 42) {
      alert('Please enter a valid 20-byte 0x address');
      return;
    }
    setLookupLoading(true);
    setLookupResult(null);

    const parentCluster = clusters.find((c) => c.isParent) || clusters[2];
    const clusterKey = targetCluster.clusterKey || '';
    const res = await checkParentRegistration(parentCluster.rpcUrl, clusterKey, lookupAddress);
    setLookupResult(res);
    setLookupLoading(false);
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
      {(() => {
        const isGateActive = gateEnforced !== false;
        const gateHeroStatus =
          !isGateActive
            ? 'open'
            : isRegisteredOnExec
            ? 'registered'
            : isRegisteredOnParent
            ? 'pending'
            : 'unregistered';

        return (
          <div className={`gate-hero-card ${gateHeroStatus === 'open' ? 'registered' : gateHeroStatus}`}>
            <div className="gate-hero-header">
              <div style={{ display: 'flex', alignItems: 'center', gap: '14px' }}>
                <div
                  style={{
                    width: '48px',
                    height: '48px',
                    borderRadius: '12px',
                    background:
                      gateHeroStatus === 'registered' || gateHeroStatus === 'open'
                        ? 'rgba(16, 185, 129, 0.2)'
                        : gateHeroStatus === 'pending'
                        ? 'rgba(245, 158, 11, 0.2)'
                        : 'rgba(239, 68, 68, 0.2)',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                  }}
                >
                  {gateHeroStatus === 'registered' || gateHeroStatus === 'open' ? (
                    <ShieldCheck className="w-6 h-6" style={{ color: 'var(--green)' }} />
                  ) : gateHeroStatus === 'pending' ? (
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
                    {gateHeroStatus === 'open'
                      ? 'Active & Ready — Open Cluster (No Gate Enforced)'
                      : gateHeroStatus === 'registered'
                      ? 'Active & Registered — Ready to Transact'
                      : gateHeroStatus === 'pending'
                      ? 'Registered on Parent Chain (Syncing to Exec Cluster)'
                      : 'Unregistered — Outgoing Transactions Gated'}
                  </h2>
                </div>
              </div>

              <div
                className="gate-status-pill"
                style={{
                  background:
                    gateHeroStatus === 'registered' || gateHeroStatus === 'open'
                      ? 'rgba(16, 185, 129, 0.15)'
                      : gateHeroStatus === 'pending'
                      ? 'rgba(245, 158, 11, 0.15)'
                      : 'rgba(239, 68, 68, 0.15)',
                  color:
                    gateHeroStatus === 'registered' || gateHeroStatus === 'open'
                      ? '#34d399'
                      : gateHeroStatus === 'pending'
                      ? '#fbbf24'
                      : '#f87171',
                  border: `1px solid ${
                    gateHeroStatus === 'registered' || gateHeroStatus === 'open'
                      ? 'rgba(16, 185, 129, 0.3)'
                      : gateHeroStatus === 'pending'
                      ? 'rgba(245, 158, 11, 0.3)'
                      : 'rgba(239, 68, 68, 0.3)'
                  }`,
                }}
              >
                <span
                  className={`status-dot ${
                    gateHeroStatus === 'registered' || gateHeroStatus === 'open'
                      ? 'online'
                      : gateHeroStatus === 'pending'
                      ? 'pending'
                      : 'offline'
                  }`}
                />
                {gateHeroStatus === 'open'
                  ? 'OPEN CLUSTER (No Gate)'
                  : gateHeroStatus === 'registered'
                  ? 'PASS (Gate Open)'
                  : gateHeroStatus === 'pending'
                  ? 'PENDING CO-ATTESTATION'
                  : 'BLOCKED (Code 69)'}
              </div>
            </div>

            {/* Detailed Metrics */}
            <div className="details-grid">
              <div className="detail-box">
                <div className="detail-label">Connected Wallet Address</div>
                <div className="detail-value mono" style={{ fontSize: '0.82rem' }}>{account}</div>
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
                <div className="detail-label">Parent Registry Status</div>
                <div className="detail-value mono">
                  {gateHeroStatus === 'open'
                    ? 'Not Required'
                    : parentRegInfo?.registered
                    ? `Registered (Block #${parentRegInfo.parentBlock || 'L1'})`
                    : 'Unregistered'}
                </div>
              </div>
            </div>
          </div>
        );
      })()}

      {/* Onboarding Interactive Flow */}
      {gateEnforced !== false && !isRegisteredOnExec && (
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
              each secp256k1 account is bound 1-to-1 with an execution cluster via Parent Chain's AccountRegistry.
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
                <div className="step-label">Co-Attestation</div>
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
                  const c = clusters.find((x) => x.id === e.target.value);
                  if (c) setTargetCluster(c);
                }}
                disabled={isRegistering}
              >
                {execClusters.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name} (ChainID: {c.chainId || 991})
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

      {/* Address Registry Lookup Tool */}
      <div className="card" style={{ marginTop: '20px' }}>
        <div className="card-header">
          <div className="card-title">
            <Search className="w-4 h-4 text-cyan" />
            Parent Chain Account Registry Lookup
          </div>
          <div className="card-subtitle">
            Verify whether any secp256k1 address is registered in the Parent Chain Account Registry.
          </div>
        </div>

        <form onSubmit={handleLookupAddress} style={{ display: 'flex', gap: '10px', alignItems: 'flex-end' }}>
          <div className="form-group" style={{ flex: 1, marginBottom: 0 }}>
            <label className="form-label">Query Address (0x...)</label>
            <input
              type="text"
              className="form-input mono"
              placeholder="0x..."
              value={lookupAddress}
              onChange={(e) => setLookupAddress(e.target.value)}
              required
            />
          </div>
          <button
            type="submit"
            className="btn btn-secondary"
            style={{ height: '42px', minWidth: '120px' }}
            disabled={lookupLoading || !lookupAddress}
          >
            {lookupLoading ? 'Checking...' : 'Check Registry'}
          </button>
        </form>

        {lookupResult && (
          <div
            style={{
              marginTop: '14px',
              padding: '12px',
              borderRadius: '8px',
              background: lookupResult.registered ? 'rgba(16, 185, 129, 0.1)' : 'rgba(239, 68, 68, 0.1)',
              border: `1px solid ${lookupResult.registered ? 'rgba(16, 185, 129, 0.3)' : 'rgba(239, 68, 68, 0.3)'}`,
            }}
          >
            {lookupResult.registered ? (
              <div style={{ color: 'var(--green)', fontSize: '0.85rem' }}>
                <strong>✓ Registered:</strong> Address is registered on Parent Chain (Seq #{lookupResult.seq}, Block #{lookupResult.parentBlock}).
              </div>
            ) : (
              <div style={{ color: 'var(--red)', fontSize: '0.85rem' }}>
                <strong>✗ Not Registered:</strong> Address has not yet been registered to any execution cluster in Parent Chain events.
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
