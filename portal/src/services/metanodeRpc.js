// ============================================================================
// METANODE RPC & WEB3 CLIENT
// Communicates with Parent Chain (HTTP RPC) & Execution Clusters (JSON-RPC)
// ============================================================================

export const LAN_IP = '192.168.1.232';
export const DEFAULT_HOST =
  typeof window !== 'undefined' &&
  window.location.hostname &&
  window.location.hostname !== 'localhost' &&
  window.location.hostname !== '127.0.0.1'
    ? window.location.hostname
    : LAN_IP;

export const PRESET_CLUSTERS = [
  {
    id: 'devnet_31646',
    name: 'Devnet Exec 1 (Gate Active, Port 31646)',
    chainId: 991,
    rpcUrl: `http://${DEFAULT_HOST}:31646`,
    clusterKey: '0x944488b425d29336c7913a3b45946adee6b9bfbd0838c6c8f422f4b4277066f26b3da0530c9f9865e6e534a05ae6c128',
    isExec: true,
  },
  {
    id: 'devnet_31647',
    name: 'Devnet Exec 2 (Gate Active, Port 31647)',
    chainId: 991,
    rpcUrl: `http://${DEFAULT_HOST}:31647`,
    clusterKey: '0x83221629eeff1a69aa96ac6aadea402a7b62a74647633c0743cd517b71dcd5cd39fec42841b953fc481dac039bceb465',
    isExec: true,
  },
  {
    id: 'parent_devnet',
    name: 'Parent Chain (Port 31601)',
    chainId: 990,
    rpcUrl: `http://${DEFAULT_HOST}:31601`,
    isParent: true,
  },
  {
    id: 'exec1',
    name: 'Execution Cluster 1 (No Gate, Port 8646)',
    chainId: 991,
    rpcUrl: `http://${DEFAULT_HOST}:8646`,
    clusterKey: '0x944488b425d29336c7913a3b45946adee6b9bfbd0838c6c8f422f4b4277066f26b3da0530c9f9865e6e534a05ae6c128',
    isExec: true,
  },
  {
    id: 'exec2',
    name: 'Execution Cluster 2 (No Gate, Port 8647)',
    chainId: 991,
    rpcUrl: `http://${DEFAULT_HOST}:8647`,
    clusterKey: '0x83221629eeff1a69aa96ac6aadea402a7b62a74647633c0743cd517b71dcd5cd39fec42841b953fc481dac039bceb465',
    isExec: true,
  },
  {
    id: 'parent_18601',
    name: 'Parent Chain (Port 18601)',
    chainId: 990,
    rpcUrl: `http://${DEFAULT_HOST}:18601`,
    isParent: true,
  },
  {
    id: 'exec_node0',
    name: 'Node-0 RPC (Port 8545)',
    chainId: 1000,
    rpcUrl: `http://${DEFAULT_HOST}:8545`,
    isExec: true,
  },
];

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
 * Fetch Account Information on Execution Cluster
 */
export async function fetchAccountInfo(rpcUrl, address) {
  if (!address) return null;
  try {
    const [balanceHex, nonceHex] = await Promise.all([
      callJsonRpc(rpcUrl, 'eth_getBalance', [address, 'latest']).catch(() => '0x0'),
      callJsonRpc(rpcUrl, 'eth_getTransactionCount', [address, 'latest']).catch(() => '0x0'),
    ]);

    const balanceWei = BigInt(balanceHex || '0x0');
    // Format to whole MTN (with 4 decimals)
    const divisor = BigInt('1000000000000000000');
    const whole = balanceWei / divisor;
    const remainder = balanceWei % divisor;
    const decimals = (remainder / BigInt('100000000000000')).toString().padStart(4, '0');
    const balanceMtn = `${whole}.${decimals}`;
    const nonce = parseInt(nonceHex || '0x0', 16);

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
      } catch (_) {
        try {
          const stateResult = await callJsonRpc(rpcUrl, 'mtn_getAccountState', [address, 'latest']);
          if (stateResult && (stateResult.parent_registered || stateResult.ParentRegistered)) {
            parentRegistered = true;
            registrationStatus = 'CONFIRMED';
          }
        } catch (_) {}
      }
    } else {
      // Gate not enforced on this cluster: user can transact freely!
      parentRegistered = true;
      registrationStatus = 'NOT_REQUIRED';
    }

    return {
      address,
      balanceWei: balanceWei.toString(),
      balanceMtn,
      nonce,
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
      nonce: 0,
      gateEnforced: false,
      parentRegistered: true,
      registrationStatus: 'NONE',
    };
  }
}

/**
 * Check Account Registration on Parent Chain
 */
export async function checkParentRegistration(parentRpcUrl, clusterKey, userAddress) {
  try {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), 3500);

    const cleanClusterKey = clusterKey.replace('0x', '');
    const url = `${parentRpcUrl}/account_registration_events?cluster_key=${cleanClusterKey}&from_seq=0&limit=256`;
    const res = await fetch(url, { signal: controller.signal });
    clearTimeout(timeoutId);

    if (!res.ok) {
      return { registered: false, seq: 0, total: 0 };
    }
    const data = await res.json();
    const events = data.events || [];
    const matched = events.find(
      (ev) => (ev.user_address || ev.UserAddress || '').toLowerCase() === userAddress.toLowerCase()
    );

    if (matched) {
      return {
        registered: true,
        seq: matched.seq !== undefined ? matched.seq : matched.Seq,
        clusterKey,
        parentBlock: matched.parent_block || matched.ParentBlock || 0,
      };
    }
    return { registered: false, seq: 0, total: events.length };
  } catch (err) {
    // If parent HTTP endpoint isn't up, return tentative false
    return { registered: false, error: err.message };
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
