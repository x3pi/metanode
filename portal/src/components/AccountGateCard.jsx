import React, { useState, useEffect, useCallback } from 'react';
import {
  ShieldCheck,
  ShieldX,
  KeyRound,
  ArrowRight,
  CheckCircle2,
  Clock,
  Sparkles,
  Eye,
  EyeOff,
  Copy,
  Shuffle,
  Wallet,
  Check,
  Search,
} from 'lucide-react';
import {
  PRESET_CLUSTERS,
  getAllClusters,
  getClusterRegistrationMessage,
  registerAccountOnNode,
  getRegistrationStatusFromNode,
  fetchAccountInfo,
  deriveAddressFromPrivateKey,
  generateRandomWallet,
  registerAccountWithPrivateKey,
  checkParentRegistration,
  submitRegistrationToParent,
} from '../services/metanodeRpc';

export function AccountGateCard({
  account,
  accountInfo,
  parentRegInfo,
  selectedCluster,
  onRefresh,
  onConnectCustomWallet,
  onConnectMetaMask,
  isPrivateKeyMode,
  walletPrivateKey,
  allClusters,
}) {
  const clusters = allClusters || getAllClusters();
  const execClusters = clusters.filter((c) => c.isExec);

  const [onboardingMode, setOnboardingMode] = useState(
    isPrivateKeyMode || walletPrivateKey ? 'private_key' : 'metamask'
  );

  const [targetCluster, setTargetCluster] = useState(
    selectedCluster?.isExec
      ? selectedCluster
      : execClusters[0] || selectedCluster || PRESET_CLUSTERS[0]
  );

  useEffect(() => {
    if (selectedCluster?.isExec) {
      setTargetCluster(selectedCluster);
    }
  }, [selectedCluster]);

  // =========================================================================
  // METAMASK MODE STATES & HANDLERS
  // =========================================================================
  const [isRegisteringMetaMask, setIsRegisteringMetaMask] = useState(false);
  const [currentMmStep, setCurrentMmStep] = useState(0); // 0: Idle, 1: Signing, 2: Relay, 3: Parent Consensus, 4: Done
  const [mmRegError, setMmRegError] = useState(null);

  const gateEnforced =
    selectedCluster?.accountGate !== undefined
      ? selectedCluster.accountGate
      : accountInfo?.gateEnforced ?? true;
  const isRegisteredOnExec = accountInfo?.parentRegistered === true;
  const isRegisteredOnParent = parentRegInfo?.registered === true;

  const handleStartMetaMaskOnboarding = async () => {
    if (!account) {
      alert('Please connect your MetaMask wallet first!');
      return;
    }
    setMmRegError(null);
    setIsRegisteringMetaMask(true);
    setCurrentMmStep(1);

    try {
      const msgRes = await getClusterRegistrationMessage(targetCluster.rpcUrl, account);
      if (!msgRes || !msgRes.digest) {
        throw new Error('Failed to retrieve registration message digest from cluster');
      }

      let signature;
      try {
        signature = await window.ethereum.request({
          method: 'personal_sign',
          params: [msgRes.digest, account],
        });
      } catch (signErr) {
        throw new Error(`Signature rejected by user: ${signErr.message}`);
      }

      setCurrentMmStep(2);

      try {
        await registerAccountOnNode(targetCluster.rpcUrl, account, signature);
      } catch (nodeErr) {
        console.warn('Node relay registration notice, attempting direct parent route:', nodeErr.message);
        const parentCluster = clusters.find((c) => c.isParent);
        if (parentCluster) {
          const clusterKey =
            accountInfo?.dynamicClusterKey ||
            targetCluster.clusterKey ||
            '0x944488b425d29336c7913a3b45946adee6b9bfbd0838c6c8f422f4b4277066f26b3da0530c9f9865e6e534a05ae6c128';
          try {
            await submitRegistrationToParent(parentCluster.rpcUrl, account, clusterKey, signature);
          } catch (_) {}
        }
      }

      setCurrentMmStep(3);

      let attempts = 0;
      const maxAttempts = 30;
      const interval = setInterval(async () => {
        attempts++;
        try {
          const statusRes = await getRegistrationStatusFromNode(targetCluster.rpcUrl, account);
          if (statusRes && statusRes.status === 'CONFIRMED') {
            clearInterval(interval);
            if (onRefresh) await onRefresh();
            setCurrentMmStep(4);
            setIsRegisteringMetaMask(false);
            return;
          }
          if (statusRes && statusRes.status === 'REJECTED') {
            clearInterval(interval);
            setMmRegError(
              `Registration rejected: Account is already registered on cluster ${statusRes.homeCluster || 'another cluster'}`
            );
            setIsRegisteringMetaMask(false);
            return;
          }
          if (statusRes && statusRes.status === 'FAILED') {
            clearInterval(interval);
            setMmRegError(`Registration relay failed: ${statusRes.reason || 'Parent Chain rejected'}`);
            setIsRegisteringMetaMask(false);
            return;
          }
        } catch (_) {}

        if (attempts >= maxAttempts) {
          clearInterval(interval);
          if (onRefresh) await onRefresh();
          setMmRegError('Registration submitted. Still awaiting Parent Chain finality. Please refresh soon.');
          setIsRegisteringMetaMask(false);
        }
      }, 1500);
    } catch (err) {
      setMmRegError(err.message);
      setIsRegisteringMetaMask(false);
      setCurrentMmStep(0);
    }
  };

  // =========================================================================
  // PRIVATE KEY MODE STATES & HANDLERS
  // =========================================================================
  const [inputPrivateKey, setInputPrivateKey] = useState(walletPrivateKey || '');
  const [showPrivateKey, setShowPrivateKey] = useState(false);
  const [derivedAddress, setDerivedAddress] = useState(() => deriveAddressFromPrivateKey(walletPrivateKey || ''));
  const [pkAccountState, setPkAccountState] = useState(null);
  const [pkLoading, setPkLoading] = useState(false);
  const [pkCopied, setPkCopied] = useState(false);
  const [addrCopied, setAddrCopied] = useState(false);

  const [isRegisteringPk, setIsRegisteringPk] = useState(false);
  const [pkStep, setPkStep] = useState(0); // 0: Idle, 1: Sign, 2: Relay, 3: Parent Consensus, 4: Done
  const [pkRegError, setPkRegError] = useState(null);
  const [pkRegSuccess, setPkRegSuccess] = useState(null);

  // Sync private key prop if changed externally
  useEffect(() => {
    if (walletPrivateKey && walletPrivateKey !== inputPrivateKey) {
      setInputPrivateKey(walletPrivateKey);
      setDerivedAddress(deriveAddressFromPrivateKey(walletPrivateKey));
    }
  }, [walletPrivateKey]);

  // When inputPrivateKey changes, update derived address
  const handlePrivateKeyChange = (val) => {
    setInputPrivateKey(val);
    setPkRegError(null);
    setPkRegSuccess(null);
    const addr = deriveAddressFromPrivateKey(val);
    setDerivedAddress(addr);
  };

  // Refresh status of derived address on target cluster
  const loadPkStatus = useCallback(async (addr) => {
    if (!addr) {
      setPkAccountState(null);
      return;
    }
    setPkLoading(true);
    try {
      const info = await fetchAccountInfo(targetCluster.rpcUrl, addr, targetCluster);
      setPkAccountState(info);
    } catch (err) {
      console.warn('loadPkStatus error:', err);
    } finally {
      setPkLoading(false);
    }
  }, [targetCluster]);

  useEffect(() => {
    if (derivedAddress) {
      loadPkStatus(derivedAddress);
    } else {
      setPkAccountState(null);
    }
  }, [derivedAddress, loadPkStatus]);

  // Generate random Secp256k1 key
  const handleGenerateRandom = () => {
    const randomW = generateRandomWallet();
    setInputPrivateKey(randomW.privateKey);
    setDerivedAddress(randomW.address);
    setPkRegError(null);
    setPkRegSuccess(null);
  };

  // Paste from clipboard
  const handlePasteClipboard = async () => {
    try {
      const text = await navigator.clipboard.readText();
      if (text) {
        handlePrivateKeyChange(text.trim());
      }
    } catch (err) {
      alert('Clipboard access denied or unavailable: ' + err.message);
    }
  };

  // Register with Private Key directly
  const handleRegisterWithPk = async () => {
    if (!inputPrivateKey || !derivedAddress) {
      setPkRegError('Please provide a valid 64-character Secp256k1 Private Key.');
      return;
    }

    setIsRegisteringPk(true);
    setPkRegError(null);
    setPkRegSuccess(null);
    setPkStep(1); // Local Secp256k1 Sign

    try {
      // Step 1 & 2: Sign digest with private key & submit to node relay
      setPkStep(2);
      await registerAccountWithPrivateKey(targetCluster.rpcUrl, inputPrivateKey);

      // Step 3: Wait for Parent Chain BFT consensus
      setPkStep(3);

      let attempts = 0;
      const maxAttempts = 30;
      const interval = setInterval(async () => {
        attempts++;
        try {
          const statusRes = await getRegistrationStatusFromNode(targetCluster.rpcUrl, derivedAddress);
          if (statusRes && statusRes.status === 'CONFIRMED') {
            clearInterval(interval);
            setPkStep(4);
            setIsRegisteringPk(false);
            setPkRegSuccess('🎉 Account successfully registered to Parent Chain & Execution Cluster!');
            await loadPkStatus(derivedAddress);
            if (onRefresh) await onRefresh();
            return;
          }
          if (statusRes && statusRes.status === 'REJECTED') {
            clearInterval(interval);
            setIsRegisteringPk(false);
            setPkRegError(
              `Registration rejected: Account is already assigned to cluster ${statusRes.homeCluster || 'another cluster'}`
            );
            return;
          }
          if (statusRes && statusRes.status === 'FAILED') {
            clearInterval(interval);
            setIsRegisteringPk(false);
            setPkRegError(`Relay failed: ${statusRes.reason || 'Parent Chain rejected registration'}`);
            return;
          }
        } catch (_) {}

        if (attempts >= maxAttempts) {
          clearInterval(interval);
          setIsRegisteringPk(false);
          await loadPkStatus(derivedAddress);
          if (onRefresh) await onRefresh();
          setPkRegSuccess('Registration relay submitted. Parent Chain consensus finalizing.');
        }
      }, 1500);
    } catch (err) {
      setIsRegisteringPk(false);
      setPkStep(0);
      setPkRegError(err.message || 'Registration failed');
    }
  };

  // Connect this private key as active portal account
  const handleConnectAsPortalWallet = () => {
    if (!derivedAddress || !inputPrivateKey) return;
    if (onConnectCustomWallet) {
      onConnectCustomWallet(derivedAddress, inputPrivateKey);
    }
  };

  const isCurrentActiveWallet =
    account && derivedAddress && account.toLowerCase() === derivedAddress.toLowerCase();

  // =========================================================================
  // ADDRESS REGISTRY LOOKUP TOOL (From dev branch)
  // =========================================================================
  const [lookupAddress, setLookupAddress] = useState('');
  const [lookupLoading, setLookupLoading] = useState(false);
  const [lookupResult, setLookupResult] = useState(null);

  const handleLookupAddress = async (e) => {
    e.preventDefault();
    if (!lookupAddress || !lookupAddress.startsWith('0x') || lookupAddress.length !== 42) {
      alert('Please enter a valid 20-byte 0x address');
      return;
    }
    setLookupLoading(true);
    setLookupResult(null);

    const parentCluster =
      clusters.find((c) => c.isParent) ||
      clusters.find((c) => c.id?.includes('parent'));
    const parentRpcUrl = parentCluster?.rpcUrl || 'http://127.0.0.1:18601';
    const clusterKey = targetCluster?.clusterKey || '';
    const res = await checkParentRegistration(parentRpcUrl, clusterKey, lookupAddress);
    setLookupResult(res);
    setLookupLoading(false);
  };

  return (
    <div>
      {/* Mode Switcher Tabs */}
      <div className="onboarding-mode-pills">
        <button
          className={`mode-pill ${onboardingMode === 'metamask' ? 'active' : ''}`}
          onClick={() => setOnboardingMode('metamask')}
        >
          <Wallet className="w-4 h-4" />
          🦊 MetaMask / Web3 Extension
        </button>
        <button
          className={`mode-pill ${onboardingMode === 'private_key' ? 'active' : ''}`}
          onClick={() => setOnboardingMode('private_key')}
        >
          <KeyRound className="w-4 h-4" />
          🔑 Direct Private Key / Custom Key
        </button>
      </div>

      {/* =================================================================== */}
      {/* MODE 1: METAMASK ONBOARDING                                         */}
      {/* =================================================================== */}
      {onboardingMode === 'metamask' && (
        <>
          {!account ? (
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
                Connect MetaMask to Check Gate Status
              </h2>
              <p style={{ color: 'var(--text-muted)', maxWidth: '480px', margin: '0 auto 20px' }}>
                Metanode enforces a Parent Chain Account Registration Gate on secp256k1 execution clusters
                to prevent cross-cluster transaction replay attacks.
              </p>
              {onConnectMetaMask && (
                <button className="btn btn-primary" onClick={onConnectMetaMask}>
                  <Wallet className="w-4 h-4" />
                  Connect MetaMask
                </button>
              )}
            </div>
          ) : (
            <>
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
                              ? 'Active & Ready — No Account Gate Required'
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
                          ? 'PENDING SYNC'
                          : 'BLOCKED (Code 69)'}
                      </div>
                    </div>

                    {/* Detailed Account Metrics via mtn_getAccountState */}
                    <div className="details-grid">
                      <div className="detail-box">
                        <div className="detail-label">Connected Wallet Address</div>
                        <div className="detail-value mono" style={{ fontSize: '0.82rem' }}>{account}</div>
                      </div>
                      <div className="detail-box">
                        <div className="detail-label">Current Cluster Balance</div>
                        <div className="detail-value">
                          {accountInfo?.balanceMtn || '0.0000'} <span style={{ color: 'var(--cyan)' }}>MTN</span>
                          {accountInfo?.pendingBalanceMtn && accountInfo.pendingBalanceMtn !== '0.0000' && (
                            <span style={{ fontSize: '0.75rem', color: 'var(--amber)', marginLeft: '6px' }}>
                              (+{accountInfo.pendingBalanceMtn} pending)
                            </span>
                          )}
                        </div>
                      </div>
                      <div className="detail-box">
                        <div className="detail-label">Account Type & Nonce</div>
                        <div className="detail-value mono">
                          {accountInfo?.accountType === 0 ? 'EOA (User Wallet)' : `Contract Type ${accountInfo?.accountType || 0}`} • Nonce: {accountInfo?.nonce ?? 0}
                        </div>
                      </div>
                      <div className="detail-box">
                        <div className="detail-label">Parent Registry Status</div>
                        <div className="detail-value mono">
                          {gateHeroStatus === 'open'
                            ? 'Not Required'
                            : isRegisteredOnExec
                            ? 'CONFIRMED (Cluster Local)'
                            : isRegisteredOnParent
                            ? 'CONFIRMED (Parent State)'
                            : 'Unregistered'}
                        </div>
                      </div>
                      {accountInfo?.deviceKey &&
                        accountInfo.deviceKey !== '0x0000000000000000000000000000000000000000000000000000000000000000' && (
                          <div className="detail-box" style={{ gridColumn: 'span 2' }}>
                            <div className="detail-label">Hardware Device Key (CoSign)</div>
                            <div className="detail-value mono" style={{ fontSize: '0.8rem' }}>
                              {accountInfo.deviceKey}
                            </div>
                          </div>
                        )}
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
                        1-Click MetaMask Account Gate Onboarding
                      </div>
                      <div className="card-subtitle">
                        Register your secp256k1 address with this execution cluster via gasless node relay to Parent Chain.
                      </div>
                    </div>
                  </div>

                  <div className="alert alert-info">
                    <div>
                      <strong>Why is registration required?</strong> Under MetaNode's Zero-Fork architecture,
                      each secp account is bound 1-to-1 with an execution cluster via Parent Chain's AccountRegistry.
                      This prevents malicious actors from replaying your signed transactions on another cluster!
                    </div>
                  </div>

                  {mmRegError && <div className="alert alert-danger">{mmRegError}</div>}

                  {/* Stepper Progress */}
                  {isRegisteringMetaMask && (
                    <div className="steps-container">
                      <div className={`step-item ${currentMmStep >= 1 ? 'active' : ''} ${currentMmStep > 1 ? 'completed' : ''}`}>
                        <div className="step-circle">{currentMmStep > 1 ? '✓' : '1'}</div>
                        <div className="step-label">ECDSA Sign</div>
                      </div>
                      <div className={`step-item ${currentMmStep >= 2 ? 'active' : ''} ${currentMmStep > 2 ? 'completed' : ''}`}>
                        <div className="step-circle">{currentMmStep > 2 ? '✓' : '2'}</div>
                        <div className="step-label">Node Relay</div>
                      </div>
                      <div className={`step-item ${currentMmStep >= 3 ? 'active' : ''} ${currentMmStep > 3 ? 'completed' : ''}`}>
                        <div className="step-circle">{currentMmStep > 3 ? '✓' : '3'}</div>
                        <div className="step-label">Parent Consensus</div>
                      </div>
                      <div className={`step-item ${currentMmStep >= 4 ? 'active completed' : ''}`}>
                        <div className="step-circle">{currentMmStep >= 4 ? '✓' : '4'}</div>
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
                        value={targetCluster?.id}
                        onChange={(e) => {
                          const c = clusters.find((x) => x.id === e.target.value);
                          if (c) setTargetCluster(c);
                        }}
                        disabled={isRegisteringMetaMask}
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
                      onClick={handleStartMetaMaskOnboarding}
                      disabled={isRegisteringMetaMask}
                    >
                      {isRegisteringMetaMask ? (
                        <>
                          <Clock className="w-4 h-4 animate-spin" />
                          Processing Onboarding...
                        </>
                      ) : (
                        <>
                          Sign & Register Account
                          <ArrowRight className="w-4 h-4" />
                        </>
                      )}
                    </button>
                  </div>
                </div>
              )}
            </>
          )}
        </>
      )}

      {/* =================================================================== */}
      {/* MODE 2: DIRECT PRIVATE KEY ONBOARDING & CONNECTION                  */}
      {/* =================================================================== */}
      {onboardingMode === 'private_key' && (
        <div className="card">
          <div className="card-header">
            <div>
              <div className="card-title">
                <KeyRound className="w-5 h-5 text-cyan" />
                Direct Private Key Onboarding & Gate Register
              </div>
              <div className="card-subtitle">
                Paste or generate any Secp256k1 Private Key. Sign directly with ethers.js without needing MetaMask!
              </div>
            </div>
          </div>

          {/* Input & Action Tools */}
          <div className="form-group" style={{ marginTop: '16px' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '8px' }}>
              <label className="form-label" style={{ marginBottom: 0 }}>
                Secp256k1 Private Key (64-hex string or 0x...):
              </label>
              <div style={{ display: 'flex', gap: '8px' }}>
                <button
                  type="button"
                  className="pk-icon-btn"
                  onClick={handleGenerateRandom}
                  title="Generate a brand new random Secp256k1 key pair"
                >
                  <Shuffle className="w-3.5 h-3.5" style={{ marginRight: '4px' }} />
                  Random Key
                </button>
                <button
                  type="button"
                  className="pk-icon-btn"
                  onClick={handlePasteClipboard}
                  title="Paste private key from clipboard"
                >
                  Paste Key
                </button>
              </div>
            </div>

            <div className="pk-input-wrapper">
              <input
                type={showPrivateKey ? 'text' : 'password'}
                className="pk-input-field"
                placeholder="e.g. c52e21e6b99a369f0b129aac4a307e1e443120b59c0b40fb845009e20b541459"
                value={inputPrivateKey}
                onChange={(e) => handlePrivateKeyChange(e.target.value)}
                disabled={isRegisteringPk}
                autoComplete="off"
                spellCheck="false"
              />
              <div className="pk-action-icons">
                <button
                  type="button"
                  className="pk-icon-btn"
                  onClick={() => setShowPrivateKey(!showPrivateKey)}
                  title={showPrivateKey ? 'Hide Key' : 'Reveal Key'}
                >
                  {showPrivateKey ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                </button>
                {inputPrivateKey && (
                  <button
                    type="button"
                    className="pk-icon-btn"
                    onClick={() => {
                      navigator.clipboard.writeText(inputPrivateKey);
                      setPkCopied(true);
                      setTimeout(() => setPkCopied(false), 2000);
                    }}
                    title="Copy Private Key"
                  >
                    {pkCopied ? <Check className="w-3.5 h-3.5 text-green" /> : <Copy className="w-3.5 h-3.5" />}
                  </button>
                )}
              </div>
            </div>
          </div>

          {/* Derived Address & Live Status */}
          {derivedAddress ? (
            <div style={{ marginTop: '20px' }}>
              <div
                style={{
                  background: 'rgba(15, 23, 42, 0.75)',
                  border: '1px solid var(--border-medium)',
                  borderRadius: '12px',
                  padding: '16px',
                  marginBottom: '16px',
                }}
              >
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '8px' }}>
                  <span style={{ fontSize: '0.78rem', textTransform: 'uppercase', color: 'var(--text-muted)', fontWeight: 600 }}>
                    Derived Ethereum / MetaNode Address
                  </span>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                    {isCurrentActiveWallet && (
                      <span
                        style={{
                          fontSize: '0.72rem',
                          background: 'rgba(16, 185, 129, 0.2)',
                          color: '#34d399',
                          border: '1px solid rgba(16, 185, 129, 0.3)',
                          padding: '2px 8px',
                          borderRadius: '6px',
                          fontWeight: 600,
                        }}
                      >
                        Active Portal Wallet
                      </span>
                    )}
                    <button
                      type="button"
                      className="pk-icon-btn"
                      onClick={() => {
                        navigator.clipboard.writeText(derivedAddress);
                        setAddrCopied(true);
                        setTimeout(() => setAddrCopied(false), 2000);
                      }}
                      title="Copy Address"
                    >
                      {addrCopied ? <Check className="w-3.5 h-3.5 text-green" /> : <Copy className="w-3.5 h-3.5" />}
                      <span style={{ marginLeft: '4px' }}>{addrCopied ? 'Copied' : 'Copy'}</span>
                    </button>
                  </div>
                </div>

                <div className="detail-value mono" style={{ fontSize: '0.92rem', color: 'var(--cyan-bright)' }}>
                  {derivedAddress}
                </div>

                {/* State metrics */}
                <div className="details-grid" style={{ marginTop: '12px', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))' }}>
                  <div className="detail-box">
                    <div className="detail-label">Cluster Balance</div>
                    <div className="detail-value">
                      {pkLoading ? (
                        <span style={{ color: 'var(--text-muted)' }}>Querying...</span>
                      ) : (
                        `${pkAccountState?.balanceMtn || '0.0000'} MTN`
                      )}
                    </div>
                  </div>

                  <div className="detail-box">
                    <div className="detail-label">Nonce</div>
                    <div className="detail-value mono">
                      {pkLoading ? '...' : pkAccountState?.nonce ?? 0}
                    </div>
                  </div>

                  <div className="detail-box">
                    <div className="detail-label">Parent Gate Status</div>
                    <div className="detail-value">
                      {pkLoading ? (
                        'Checking...'
                      ) : pkAccountState?.parentRegistered ? (
                        <span style={{ color: '#34d399', fontWeight: 700 }}>
                          CONFIRMED (PASS)
                        </span>
                      ) : pkAccountState?.registrationStatus === 'PENDING' ? (
                        <span style={{ color: '#fbbf24', fontWeight: 700 }}>
                          PENDING SYNC
                        </span>
                      ) : (
                        <span style={{ color: '#f87171', fontWeight: 700 }}>
                          UNREGISTERED (GATED)
                        </span>
                      )}
                    </div>
                  </div>
                </div>
              </div>

              {/* Alerts */}
              {pkRegError && <div className="alert alert-danger">{pkRegError}</div>}
              {pkRegSuccess && <div className="alert alert-success">{pkRegSuccess}</div>}

              {/* Stepper Progress */}
              {isRegisteringPk && (
                <div className="steps-container">
                  <div className={`step-item ${pkStep >= 1 ? 'active' : ''} ${pkStep > 1 ? 'completed' : ''}`}>
                    <div className="step-circle">{pkStep > 1 ? '✓' : '1'}</div>
                    <div className="step-label">ECDSA Sign</div>
                  </div>
                  <div className={`step-item ${pkStep >= 2 ? 'active' : ''} ${pkStep > 2 ? 'completed' : ''}`}>
                    <div className="step-circle">{pkStep > 2 ? '✓' : '2'}</div>
                    <div className="step-label">Node Relay</div>
                  </div>
                  <div className={`step-item ${pkStep >= 3 ? 'active' : ''} ${pkStep > 3 ? 'completed' : ''}`}>
                    <div className="step-circle">{pkStep > 3 ? '✓' : '3'}</div>
                    <div className="step-label">Parent Consensus</div>
                  </div>
                  <div className={`step-item ${pkStep >= 4 ? 'active completed' : ''}`}>
                    <div className="step-circle">{pkStep >= 4 ? '✓' : '4'}</div>
                    <div className="step-label">Ready</div>
                  </div>
                </div>
              )}

              {/* Target Cluster Selector & Action Buttons */}
              <div className="form-group" style={{ marginTop: '16px' }}>
                <label className="form-label">Target Execution Cluster:</label>
                <select
                  className="form-select"
                  value={targetCluster?.id}
                  onChange={(e) => {
                    const c = clusters.find((x) => x.id === e.target.value);
                    if (c) setTargetCluster(c);
                  }}
                  disabled={isRegisteringPk}
                >
                  {execClusters.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name} (ChainID: {c.chainId || 991})
                    </option>
                  ))}
                </select>
              </div>

              <div className="grid-2" style={{ marginTop: '16px', gap: '12px' }}>
                <button
                  type="button"
                  className="btn btn-primary"
                  onClick={handleRegisterWithPk}
                  disabled={isRegisteringPk || pkAccountState?.parentRegistered}
                  style={{ height: '44px' }}
                >
                  {isRegisteringPk ? (
                    <>
                      <Clock className="w-4 h-4 animate-spin" />
                      Registering to Parent Chain...
                    </>
                  ) : pkAccountState?.parentRegistered ? (
                    <>
                      <CheckCircle2 className="w-4 h-4 text-green" />
                      Already Registered (Gate Open)
                    </>
                  ) : (
                    <>
                      <Sparkles className="w-4 h-4" />
                      Sign & Register to Chain
                    </>
                  )}
                </button>

                <button
                  type="button"
                  className="btn btn-secondary"
                  onClick={handleConnectAsPortalWallet}
                  style={{
                    height: '44px',
                    borderColor: isCurrentActiveWallet ? 'var(--green)' : 'var(--border-medium)',
                  }}
                >
                  {isCurrentActiveWallet ? (
                    <>
                      <CheckCircle2 className="w-4 h-4 text-green" />
                      Active Portal Wallet
                    </>
                  ) : (
                    <>
                      <Wallet className="w-4 h-4 text-cyan" />
                      Connect to Portal as Active Wallet
                    </>
                  )}
                </button>
              </div>
            </div>
          ) : inputPrivateKey ? (
            <div className="alert alert-warning" style={{ marginTop: '14px' }}>
              Invalid Secp256k1 private key format. Must be 64 hexadecimal characters (with or without 0x prefix).
            </div>
          ) : null}
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
                <strong>✓ Registered:</strong> Address is registered on Parent Chain {lookupResult.parentBlock ? `(Block #${lookupResult.parentBlock})` : ''}.
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
