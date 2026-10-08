// ============================================================================
// METANODE RPC & WEB3 CLIENT
// Communicates with Parent Chain (HTTP RPC) & Execution Clusters (JSON-RPC)
// Compatible with Protocol Cutover Chain ID 991, Protobuf Wire, & Co-Attestation
// ============================================================================

import clustersConfig from '../config/clusters.json';
import { ethers, Wallet } from 'ethers';

export const LAN_IP = clustersConfig?.defaultHost || '192.168.1.234';

// Map clusters directly from clusters.json as single source of truth
export const PRESET_CLUSTERS = (clustersConfig?.clusters || [])
  .map((c) => ({ ...c }))
  .filter((cluster, index, self) => index === self.findIndex((c) => c.id === cluster.id || c.rpcUrl === cluster.rpcUrl));

export const DEFAULT_CLUSTER_ID = clustersConfig?.defaultClusterId || 'exec1';

/**
 * LocalStorage Custom Cluster Management
 */
const CUSTOM_CLUSTERS_KEY = 'metanode_portal_custom_clusters_v1';

export function getCustomClusters() {
  if (typeof window === 'undefined') return [];
  try {
    const raw = localStorage.getItem(CUSTOM_CLUSTERS_KEY);
    return raw ? JSON.parse(raw) : [];
  } catch (_) {
    return [];
  }
}

export function saveCustomCluster(cluster) {
  if (typeof window === 'undefined') return;
  try {
    const current = getCustomClusters();
    const filtered = current.filter((c) => c.id !== cluster.id && c.rpcUrl !== cluster.rpcUrl);
    filtered.push(cluster);
    localStorage.setItem(CUSTOM_CLUSTERS_KEY, JSON.stringify(filtered));
  } catch (_) {}
}

export function removeCustomCluster(clusterId) {
  if (typeof window === 'undefined') return;
  try {
    const current = getCustomClusters();
    const filtered = current.filter((c) => c.id !== clusterId);
    localStorage.setItem(CUSTOM_CLUSTERS_KEY, JSON.stringify(filtered));
  } catch (_) {}
}

export function getAllClusters() {
  return [...PRESET_CLUSTERS, ...getCustomClusters()];
}

/**
 * Standard JSON-RPC Call Helper
 */
export async function callJsonRpc(rpcUrl, method, params = [], timeoutMs = 4000) {
  try {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), timeoutMs);

    const res = await fetch(rpcUrl, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        jsonrpc: '2.0',
        id: Date.now(),
        method,
        params,
      }),
      signal: controller.signal,
    });
    clearTimeout(timeoutId);

    if (!res.ok) {
      throw new Error(`HTTP ${res.status}: ${res.statusText}`);
    }
    const data = await res.json();
    if (data.error) {
      throw new Error(data.error.message || JSON.stringify(data.error));
    }
    return data.result;
  } catch (err) {
    throw err;
  }
}

/**
 * Probe a cluster endpoint to auto-detect its type and configuration
 */
export async function probeClusterEndpoint(url) {
  const cleanUrl = url.replace(/\/+$/, '');
  const result = {
    rpcUrl: cleanUrl,
    isParent: false,
    isExec: false,
    chainId: 991,
    online: false,
    name: cleanUrl,
  };

  try {
    // 1. Check if it is a Parent Chain (/status)
    const ctrl1 = new AbortController();
    const t1 = setTimeout(() => ctrl1.abort(), 2000);
    const parentRes = await fetch(`${cleanUrl}/status`, { signal: ctrl1.signal }).catch(() => null);
    clearTimeout(t1);

    if (parentRes && parentRes.ok) {
      const pData = await parentRes.json().catch(() => null);
      if (pData) {
        result.isParent = true;
        result.online = true;
        result.chainId = pData.chain_id || 991;
        result.name = `Parent Chain (${new URL(cleanUrl).port || '80'})`;
        return result;
      }
    }

    // 2. Check if it is an Execution Cluster (JSON-RPC)
    const chainIdHex = await callJsonRpc(cleanUrl, 'eth_chainId', [], 2500).catch(() => null);
    if (chainIdHex) {
      result.isExec = true;
      result.online = true;
      result.chainId = parseInt(chainIdHex, 16);
      result.name = `Exec Cluster ${result.chainId} (${new URL(cleanUrl).port || '80'})`;

      try {
        const identity = await callJsonRpc(cleanUrl, 'mtn_getClusterIdentity', [], 2000);
        if (identity) {
          result.clusterKey = identity.clusterKey;
          result.accountGate = identity.accountGate;
        }
      } catch (_) {}

      return result;
    }
  } catch (err) {
    result.error = err.message;
  }

  return result;
}

/**
 * Check node health, readiness, committee key status, & block height
 */
export async function checkNodeStatus(target) {
  const rpcUrl = typeof target === 'string' ? target : target?.rpcUrl;
  const isParent = typeof target === 'object' ? target?.isParent : false;

  try {
    const start = performance.now();

    // Parent Chain uses HTTP REST endpoint /status
    if (isParent || rpcUrl.includes('18601') || rpcUrl.includes('31601') || rpcUrl.includes('8547')) {
      const controller = new AbortController();
      const timeoutId = setTimeout(() => controller.abort(), 3500);
      const res = await fetch(`${rpcUrl}/status`, { signal: controller.signal });
      clearTimeout(timeoutId);
      const latency = Math.round(performance.now() - start);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      return {
        online: true,
        blockNumber: data.last_block || 0,
        stateRoot: data.state_root || '0x00000000...',
        latency,
        isParent: true,
        chainId: data.chain_id || 991,
        forkDetected: !!data.fork_detected,
        syncing: !!data.syncing,
      };
    }

    // Execution Cluster:
    // 1. JSON-RPC eth_blockNumber & eth_chainId
    const [blockHex, chainIdHex] = await Promise.all([
      callJsonRpc(rpcUrl, 'eth_blockNumber', []),
      callJsonRpc(rpcUrl, 'eth_chainId', []).catch(() => '0x3df'),
    ]);
    const latency = Math.round(performance.now() - start);
    const blockNumber = parseInt(blockHex, 16);
    const chainId = parseInt(chainIdHex, 16);

    let stateRoot = '0x00000000...';
    try {
      const block = await callJsonRpc(rpcUrl, 'eth_getBlockByNumber', [blockHex, false]);
      if (block && block.stateRoot) {
        stateRoot = block.stateRoot;
      }
    } catch (_) {}

    // 2. Health endpoint (/health)
    let healthData = null;
    try {
      const hCtrl = new AbortController();
      const hTimeout = setTimeout(() => hCtrl.abort(), 2000);
      const hRes = await fetch(`${rpcUrl}/health`, { signal: hCtrl.signal });
      clearTimeout(hTimeout);
      if (hRes.ok) {
        healthData = await hRes.json();
      }
    } catch (_) {}

    // 3. Readiness endpoint (/readiness)
    let readinessData = null;
    try {
      const rCtrl = new AbortController();
      const rTimeout = setTimeout(() => rCtrl.abort(), 2000);
      const rRes = await fetch(`${rpcUrl}/readiness`, { signal: rCtrl.signal });
      clearTimeout(rTimeout);
      readinessData = {
        ready: rRes.ok,
        status: rRes.status,
        data: await rRes.json().catch(() => null),
      };
    } catch (_) {}

    // 4. Cluster identity (Account Gate & BLS Cluster Key)
    let clusterIdentity = null;
    try {
      clusterIdentity = await callJsonRpc(rpcUrl, 'mtn_getClusterIdentity', [], 2000);
    } catch (_) {}

    return {
      online: true,
      blockNumber,
      stateRoot,
      latency,
      chainId,
      isExec: true,
      health: healthData?.status || 'ok',
      committeeKey: healthData?.committee_key || 'not_validator',
      committeeKeyWarning: healthData?.committee_key_warning || null,
      epoch: healthData?.epoch !== undefined ? healthData.epoch : 0,
      lastBlockAgeMs: healthData?.last_block_age_ms !== undefined ? healthData.last_block_age_ms : 0,
      readiness: readinessData ? readinessData.ready : true,
      readinessDetails: readinessData?.data || null,
      clusterIdentity,
      accountGate: clusterIdentity?.accountGate || false,
      clusterKey: clusterIdentity?.clusterKey || null,
    };
  } catch (err) {
    return {
      online: false,
      blockNumber: 0,
      stateRoot: 'N/A',
      error: err.message,
    };
  }
}

/**
 * Fetch Key Prometheus Metrics for Node Telemetry
 */
export async function fetchNodeMetrics(rpcUrl) {
  try {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), 2500);
    const res = await fetch(`${rpcUrl}/metrics`, { signal: controller.signal });
    clearTimeout(timeoutId);
    if (!res.ok) return null;

    const text = await res.text();
    const metrics = {};

    const targetKeys = [
      'master_validator_committee_key_valid',
      'master_parent_chain_id_mismatch',
      'master_account_registration_pending_total',
      'master_account_registration_pending_max_age_seconds',
      'master_rollup_signatures_rejected_total',
      'master_rollup_committee_read_errors_total',
    ];

    targetKeys.forEach((key) => {
      const regex = new RegExp(`^${key}\\s+([\\d.-]+)`, 'm');
      const match = text.match(regex);
      if (match) {
        metrics[key] = parseFloat(match[1]);
      }
    });

    return metrics;
  } catch (_) {
    return null;
  }
}

/**
 * Fetch Detailed Account Information on Execution Cluster
 * Uses mtn_getAccountState for rich data (balance, pendingBalance, nonce, deviceKey, etc.)
 */
export async function fetchAccountInfo(rpcUrl, address, clusterConfig = null) {
  if (!address) return null;
  const isGateConfigured = clusterConfig?.accountGate !== false;
  try {
    let balanceWei = BigInt(0);
    let pendingBalanceWei = BigInt(0);
    let nonce = 0;
    let accountType = 0;
    let deviceKey = '0x0000000000000000000000000000000000000000000000000000000000000000';
    let lastHash = '0x0000000000000000000000000000000000000000000000000000000000000000';
    let publicKeyBls = '';
    let hasDetailedState = false;

    try {
      const stateResult = await callJsonRpc(rpcUrl, 'mtn_getAccountState', [address, 'latest']);
      if (stateResult) {
        hasDetailedState = true;
        balanceWei = BigInt(stateResult.balance || '0');
        pendingBalanceWei = BigInt(stateResult.pendingBalance || '0');
        nonce =
          typeof stateResult.nonce === 'number'
            ? stateResult.nonce
            : parseInt(stateResult.nonce || '0', 16) || 0;
        accountType = stateResult.accountType || 0;
        deviceKey = stateResult.deviceKey || deviceKey;
        lastHash = stateResult.lastHash || lastHash;
        publicKeyBls = stateResult.publicKeyBls || '';
      }
    } catch (_) {
      // Fallback to standard eth methods
      const [balanceHex, nonceHex] = await Promise.all([
        callJsonRpc(rpcUrl, 'eth_getBalance', [address, 'latest']).catch(() => '0x0'),
        callJsonRpc(rpcUrl, 'eth_getTransactionCount', [address, 'latest']).catch(() => '0x0'),
      ]);
      balanceWei = BigInt(balanceHex || '0x0');
      nonce = parseInt(nonceHex || '0x0', 16) || 0;
    }

    // Format to whole MTN (with 4 decimals)
    const divisor = BigInt('1000000000000000000');
    const whole = balanceWei / divisor;
    const remainder = balanceWei % divisor;
    const decimals = (remainder / BigInt('100000000000000')).toString().padStart(4, '0');
    const balanceMtn = `${whole}.${decimals}`;

    // Format pending balance
    const pWhole = pendingBalanceWei / divisor;
    const pRemainder = pendingBalanceWei % divisor;
    const pDecimals = (pRemainder / BigInt('100000000000000')).toString().padStart(4, '0');
    const pendingBalanceMtn = `${pWhole}.${pDecimals}`;

    let gateEnforced = false;
    let parentRegistered = false;
    let registrationStatus = 'NONE';
    let dynamicClusterKey = null;

    try {
      const clusterIdentity = await callJsonRpc(rpcUrl, 'mtn_getClusterIdentity', []);
      if (clusterIdentity && clusterIdentity.accountGate) {
        gateEnforced = true;
        dynamicClusterKey = clusterIdentity.clusterKey;
      }
    } catch (_) {
      // Cluster does not enforce Account Gate (e.g. open evm cluster)
    }

    if (gateEnforced) {
      try {
        const regInfo = await callJsonRpc(rpcUrl, 'mtn_getRegistrationStatus', [address]);
        if (regInfo) {
          registrationStatus = regInfo.status || 'NONE';
          if (regInfo.status === 'CONFIRMED') {
            parentRegistered = true;
          }
        }
      } catch (_) {}
    } else {
      // Gate not enforced on this cluster: user can transact freely!
      parentRegistered = true;
      registrationStatus = 'NOT_REQUIRED';
    }

    return {
      address,
      balanceWei: balanceWei.toString(),
      balanceMtn,
      pendingBalanceWei: pendingBalanceWei.toString(),
      pendingBalanceMtn,
      nonce,
      accountType,
      deviceKey,
      lastHash,
      publicKeyBls,
      hasDetailedState,
      gateEnforced,
      parentRegistered,
      registrationStatus,
      dynamicClusterKey,
    };
  } catch (err) {
    console.warn('fetchAccountInfo failed:', err);
    return {
      address,
      balanceWei: '0',
      balanceMtn: '0.0000',
      pendingBalanceWei: '0',
      pendingBalanceMtn: '0.0000',
      nonce: 0,
      accountType: 0,
      gateEnforced: isGateConfigured,
      parentRegistered: false,
      registrationStatus: 'NETWORK_ERROR',
      error: err.message,
    };
  }
}

/**
 * Check Account Registration directly on Parent Chain
 * Query: GET /account?address=0x... with fallback to /account_registration_events
 */
export async function checkParentRegistration(parentRpcUrl, clusterKey, userAddress) {
  try {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), 3500);

    // 1. Direct account lookup: /account?address=0x...
    const url = `${parentRpcUrl}/account?address=${encodeURIComponent(userAddress)}`;
    const res = await fetch(url, { signal: controller.signal });
    clearTimeout(timeoutId);

    if (res.ok) {
      const data = await res.json();
      if (data && data.found) {
        return {
          registered: true,
          found: true,
          floatIdentityKey: data.float_identity_key || null,
          clusterKey,
        };
      }
    }

    // 2. Fallback to registration events if available
    try {
      const cleanClusterKey = (clusterKey || '').replace('0x', '');
      const evUrl = `${parentRpcUrl}/account_registration_events?cluster_key=${cleanClusterKey}&from_seq=0&limit=256`;
      const evRes = await fetch(evUrl);
      if (evRes.ok) {
        const evData = await evRes.json();
        const events = Array.isArray(evData) ? evData : evData.events || [];
        const match = events.find((e) => e.address?.toLowerCase() === userAddress?.toLowerCase());
        if (match) {
          return {
            registered: true,
            found: true,
            seq: match.seq,
            parentBlock: match.block,
            clusterKey,
          };
        }
      }
    } catch (_) {}

    return { registered: false, found: false, clusterKey };
  } catch (err) {
    return { registered: false, found: false, error: err.message };
  }
}

/**
 * Get registration message & hash from execution node
 */
export async function getClusterRegistrationMessage(rpcUrl, userAddress) {
  return await callJsonRpc(rpcUrl, 'mtn_getRegistrationMessage', [userAddress]);
}

/**
 * Register account via Execution Node Relay (Gasless for user)
 */
export async function registerAccountOnNode(rpcUrl, userAddress, userSignature) {
  return await callJsonRpc(rpcUrl, 'mtn_registerAccount', [userAddress, userSignature]);
}

/**
 * Query registration status from Execution Node Relay
 */
export async function getRegistrationStatusFromNode(rpcUrl, userAddress) {
  return await callJsonRpc(rpcUrl, 'mtn_getRegistrationStatus', [userAddress]);
}

/**
 * Submit Registration Transaction directly to Parent Chain (Legacy/Direct HTTP fallback)
 */
export async function submitRegistrationToParent(parentRpcUrl, userAddress, clusterKey, userSignature) {
  try {
    const payload = {
      user_address: userAddress,
      cluster_key: clusterKey,
      user_signature: userSignature,
    };

    const res = await fetch(`${parentRpcUrl}/register_account`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });

    if (!res.ok) {
      const errText = await res.text();
      throw new Error(`Parent rejected registration: ${errText}`);
    }

    const data = await res.json();
    return { success: true, data };
  } catch (err) {
    throw err;
  }
}

/**
 * Dispatch Real Cross-Cluster Rollup Transfer via mtn_sendCrossChainTransfer
 */
export async function sendCrossChainTransfer(rpcUrl, recipientAddress, amountWeiHex) {
  return await callJsonRpc(rpcUrl, 'mtn_sendCrossChainTransfer', [recipientAddress, amountWeiHex]);
}

/**
 * Connect to Web3 Provider (MetaMask / EIP-1193)
 */
export async function connectWallet() {
  if (typeof window === 'undefined' || !window.ethereum) {
    throw new Error('No Web3 wallet detected. Please install MetaMask or Rabby extension.');
  }

  const accounts = await window.ethereum.request({ method: 'eth_requestAccounts' });
  if (!accounts || accounts.length === 0) {
    throw new Error('User rejected wallet connection.');
  }

  const chainIdHex = await window.ethereum.request({ method: 'eth_chainId' });
  const chainId = parseInt(chainIdHex, 16);

  return {
    address: accounts[0],
    chainId,
  };
}

/**
 * Switch or Add Network in MetaMask
 */
export async function switchOrAddNetwork(cluster) {
  if (!window.ethereum) return;
  const targetChainId = cluster.chainId || 991;
  const hexChainId = `0x${targetChainId.toString(16)}`;

  try {
    await window.ethereum.request({
      method: 'wallet_switchEthereumChain',
      params: [{ chainId: hexChainId }],
    });
  } catch (switchError) {
    // Error 4902: Chain has not been added to MetaMask
    if (switchError.code === 4902 || switchError.data?.originalError?.code === 4902) {
      await window.ethereum.request({
        method: 'wallet_addEthereumChain',
        params: [
          {
            chainId: hexChainId,
            chainName: `Metanode - ${cluster.name}`,
            nativeCurrency: {
              name: 'Metanode Token',
              symbol: 'MTN',
              decimals: 18,
            },
            rpcUrls: [cluster.rpcUrl],
            blockExplorerUrls: null,
          },
        ],
      });
    } else {
      throw switchError;
    }
  }
}

/**
 * Derive Ethereum Address from Secp256k1 Private Key
 */
export function deriveAddressFromPrivateKey(privateKey) {
  if (!privateKey) return null;
  try {
    let pk = privateKey.trim();
    if (!pk.startsWith('0x')) {
      pk = '0x' + pk;
    }
    if (pk.length !== 66) {
      return null;
    }
    const wallet = new Wallet(pk);
    return wallet.address;
  } catch (_) {
    return null;
  }
}

/**
 * Generate a New Random Secp256k1 Wallet
 */
export function generateRandomWallet() {
  const wallet = Wallet.createRandom();
  return {
    privateKey: wallet.privateKey,
    address: wallet.address,
  };
}

/**
 * Register Account via Direct Private Key Signing (Gasless Node Relay)
 */
export async function registerAccountWithPrivateKey(rpcUrl, privateKey) {
  let pk = privateKey.trim();
  if (!pk.startsWith('0x')) {
    pk = '0x' + pk;
  }
  const wallet = new Wallet(pk);
  const address = wallet.address;

  // Step 1: Request registration message & hash from execution node
  const msgRes = await getClusterRegistrationMessage(rpcUrl, address);
  if (!msgRes || !msgRes.hashToSign) {
    throw new Error('Failed to retrieve registration message digest from cluster');
  }

  // Step 2: Sign the 32-byte hashToSign directly using ECDSA secp256k1
  const sig = wallet.signingKey.sign(msgRes.hashToSign);

  // Step 3: Submit to node registration relay
  const relayRes = await registerAccountOnNode(rpcUrl, address, sig.serialized);

  return {
    address,
    status: relayRes?.status || 'PENDING',
    relayResult: relayRes,
  };
}

/**
 * Send Native MTN Transaction with Private Key (for non-MetaMask mode)
 */
export async function sendTransactionWithPrivateKey(rpcUrl, privateKey, to, amountMtn) {
  let pk = privateKey.trim();
  if (!pk.startsWith('0x')) {
    pk = '0x' + pk;
  }
  const provider = new ethers.JsonRpcProvider(rpcUrl);
  const wallet = new Wallet(pk, provider);
  const valueWei = ethers.parseEther(amountMtn.toString());

  const tx = await wallet.sendTransaction({
    to,
    value: valueWei,
  });
  return tx.hash;
}

/**
 * Fetch Block by Number (hex/int/'latest') or Hash (0x 66 chars)
 * Supports fullTx=true returning RPCTransaction objects with EIP-2718 / EIP-1559 / groupId
 */
export async function fetchBlockByNumberOrHash(rpcUrl, tagOrHash = 'latest', fullTx = true) {
  if (!rpcUrl) return null;
  try {
    let block;
    if (typeof tagOrHash === 'string' && tagOrHash.startsWith('0x') && tagOrHash.length === 66) {
      block = await callJsonRpc(rpcUrl, 'eth_getBlockByHash', [tagOrHash, fullTx]);
    } else {
      let blockNumHex = tagOrHash;
      if (typeof tagOrHash === 'number') {
        blockNumHex = `0x${tagOrHash.toString(16)}`;
      } else if (typeof tagOrHash === 'string' && !tagOrHash.startsWith('0x') && tagOrHash !== 'latest') {
        const parsed = parseInt(tagOrHash, 10);
        if (!isNaN(parsed)) {
          blockNumHex = `0x${parsed.toString(16)}`;
        }
      }
      block = await callJsonRpc(rpcUrl, 'eth_getBlockByNumber', [blockNumHex, fullTx]);
    }
    return block;
  } catch (err) {
    console.warn(`fetchBlockByNumberOrHash failed for ${tagOrHash}:`, err.message);
    return null;
  }
}

/**
 * Fetch Recent Blocks Stream for Live Explorer Feed
 */
export async function fetchRecentBlocks(rpcUrl, limit = 8) {
  if (!rpcUrl) return [];
  try {
    const latestBlock = await fetchBlockByNumberOrHash(rpcUrl, 'latest', true);
    if (!latestBlock || latestBlock.number === null || latestBlock.number === undefined) {
      return [];
    }
    const latestNum = parseInt(latestBlock.number, 16);
    const blocks = [latestBlock];
    const fetchPromises = [];
    for (let i = 1; i < limit && latestNum - i >= 0; i++) {
      const numHex = `0x${(latestNum - i).toString(16)}`;
      fetchPromises.push(
        callJsonRpc(rpcUrl, 'eth_getBlockByNumber', [numHex, false]).catch(() => null)
      );
    }
    const olderBlocks = await Promise.all(fetchPromises);
    for (const b of olderBlocks) {
      if (b) blocks.push(b);
    }
    return blocks;
  } catch (err) {
    console.warn('fetchRecentBlocks failed:', err.message);
    return [];
  }
}

/**
 * Fetch Full Transaction and its Receipt by 32-byte Tx Hash
 */
export async function fetchTransactionDetails(rpcUrl, txHash) {
  if (!rpcUrl || !txHash) return null;
  try {
    const [tx, receipt] = await Promise.all([
      callJsonRpc(rpcUrl, 'eth_getTransactionByHash', [txHash]).catch(() => null),
      callJsonRpc(rpcUrl, 'eth_getTransactionReceipt', [txHash]).catch(() => null),
    ]);
    return { tx, receipt };
  } catch (err) {
    console.warn('fetchTransactionDetails failed:', err.message);
    return null;
  }
}

/**
 * Format Hex/Dec Wei Value into MTN Human-Readable String
 */
export function formatWei(weiValue) {
  if (!weiValue) return '0.0000 MTN';
  try {
    let bn;
    if (typeof weiValue === 'string' && weiValue.startsWith('0x')) {
      bn = BigInt(weiValue);
    } else {
      bn = BigInt(weiValue.toString());
    }
    const divisor = BigInt('1000000000000000000');
    const whole = bn / divisor;
    const remainder = bn % divisor;
    const decimals = (remainder / BigInt('100000000000000')).toString().padStart(4, '0');
    return `${whole}.${decimals} MTN`;
  } catch (_) {
    return '0.0000 MTN';
  }
}

