// ============================================================================
// METANODE RPC & WEB3 CLIENT
// Communicates with Parent Chain (HTTP RPC) & Execution Clusters (JSON-RPC)
// ============================================================================

import clustersConfig from '../config/clusters.json';
import { ethers, Wallet } from 'ethers';

export const LAN_IP = clustersConfig.defaultHost || '192.168.1.234';
export const DEFAULT_HOST =
  typeof window !== 'undefined' &&
  window.location.hostname &&
  window.location.hostname !== 'localhost' &&
  window.location.hostname !== '127.0.0.1'
    ? window.location.hostname
    : LAN_IP;

// Automatically map clusters from clusters.json, substituting host if loaded via localhost or LAN
export const PRESET_CLUSTERS = (clustersConfig.clusters || []).map((c) => {
  let url = c.rpcUrl;
  if (
    typeof window !== 'undefined' &&
    window.location.hostname &&
    clustersConfig.defaultHost
  ) {
    url = url.replace(clustersConfig.defaultHost, window.location.hostname);
  }
  return {
    ...c,
    rpcUrl: url,
  };
});

export const DEFAULT_CLUSTER_ID = clustersConfig.defaultClusterId || 'exec1';

/**
 * Standard JSON-RPC Call Helper
 */
export async function callJsonRpc(rpcUrl, method, params = []) {
  try {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), 4000);

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
 * Check node health & block height
 */
export async function checkNodeStatus(target) {
  const rpcUrl = typeof target === 'string' ? target : target?.rpcUrl;
  const isParent = typeof target === 'object' ? target?.isParent : false;

  try {
    const start = performance.now();

    // Parent Chain uses HTTP REST endpoint /status
    if (isParent || rpcUrl.includes('18601') || rpcUrl.includes('31601')) {
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
      };
    }

    // Execution Cluster uses JSON-RPC eth_blockNumber
    const blockHex = await callJsonRpc(rpcUrl, 'eth_blockNumber', []);
    const latency = Math.round(performance.now() - start);
    const blockNumber = parseInt(blockHex, 16);

    let stateRoot = '0x00000000...';
    try {
      const block = await callJsonRpc(rpcUrl, 'eth_getBlockByNumber', [blockHex, false]);
      if (block && block.stateRoot) {
        stateRoot = block.stateRoot;
      }
    } catch (_) {}

    return {
      online: true,
      blockNumber,
      stateRoot,
      latency,
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

    try {
      const clusterIdentity = await callJsonRpc(rpcUrl, 'mtn_getClusterIdentity', []);
      if (clusterIdentity && clusterIdentity.accountGate) {
        gateEnforced = true;
      }
    } catch (_) {
      // Cluster does not enforce Account Gate
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
 * Query: GET /account?address=0x...
 */
export async function checkParentRegistration(parentRpcUrl, clusterKey, userAddress) {
  try {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), 3500);

    const url = `${parentRpcUrl}/account?address=${encodeURIComponent(userAddress)}`;
    const res = await fetch(url, { signal: controller.signal });
    clearTimeout(timeoutId);

    if (!res.ok) {
      return { registered: false, found: false };
    }
    const data = await res.json();
    return {
      registered: !!data.found,
      found: !!data.found,
      floatIdentityKey: data.float_identity_key || null,
      clusterKey,
    };
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
  const hexChainId = `0x${cluster.chainId.toString(16)}`;

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
