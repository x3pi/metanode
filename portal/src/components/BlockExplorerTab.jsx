import React, { useState, useEffect, useCallback } from 'react';
import {
  Boxes,
  Box,
  Search,
  RefreshCw,
  Copy,
  Check,
  Zap,
  Layers,
  Hash,
  Fuel,
  X,
  CheckCircle2,
  AlertCircle,
  FileCode,
  ArrowRight,
  TrendingUp,
} from 'lucide-react';
import {
  fetchBlockByNumberOrHash,
  fetchRecentBlocks,
  fetchTransactionDetails,
  formatWei,
} from '../services/metanodeRpc';

export function BlockExplorerTab({ selectedCluster }) {
  const rpcUrl = selectedCluster?.rpcUrl;

  const [recentBlocks, setRecentBlocks] = useState([]);
  const [selectedBlock, setSelectedBlock] = useState(null);
  const [selectedTx, setSelectedTx] = useState(null);
  const [isLoading, setIsLoading] = useState(false);
  const [isSearching, setIsSearching] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [searchError, setSearchError] = useState(null);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [copiedKey, setCopiedKey] = useState(null);

  // Copy helper
  const handleCopy = (text, key) => {
    if (!text) return;
    navigator.clipboard.writeText(text);
    setCopiedKey(key);
    setTimeout(() => setCopiedKey(null), 2000);
  };

  // Format timestamp helper
  const formatTime = (tsHex) => {
    if (!tsHex) return 'N/A';
    try {
      const ts = typeof tsHex === 'number' ? tsHex : parseInt(tsHex, 16);
      if (ts === 0) return 'Genesis';
      const date = new Date(ts * 1000);
      return date.toLocaleTimeString() + ' ' + date.toLocaleDateString();
    } catch {
      return 'N/A';
    }
  };

  // Format Tx Type Badge
  const renderTxTypeBadge = (typeHex) => {
    const typeInt = typeof typeHex === 'number' ? typeHex : parseInt(typeHex || '0x0', 16);
    switch (typeInt) {
      case 0:
        return <span className="tx-badge tx-badge-legacy">Legacy (0x0)</span>;
      case 1:
        return <span className="tx-badge tx-badge-2930">EIP-2930 (0x1)</span>;
      case 2:
        return <span className="tx-badge tx-badge-1559">EIP-1559 (0x2)</span>;
      case 4:
        return <span className="tx-badge tx-badge-7702">EIP-7702 (0x4)</span>;
      default:
        return <span className="tx-badge tx-badge-unknown">Type {typeHex}</span>;
    }
  };

  // Load latest blocks
  const loadRecentBlocks = useCallback(async () => {
    if (!rpcUrl) return;
    setIsLoading(true);
    try {
      const blocks = await fetchRecentBlocks(rpcUrl, 8);
      setRecentBlocks(blocks);
      if (blocks.length > 0 && !selectedBlock) {
        // Default select the latest block with full txs
        const fullLatest = await fetchBlockByNumberOrHash(rpcUrl, blocks[0].number, true);
        setSelectedBlock(fullLatest || blocks[0]);
      }
    } catch (err) {
      console.warn('Failed to load blocks:', err);
    } finally {
      setIsLoading(false);
    }
  }, [rpcUrl, selectedBlock]);

  useEffect(() => {
    loadRecentBlocks();
  }, [selectedCluster, loadRecentBlocks]);

  useEffect(() => {
    if (!autoRefresh) return;
    const timer = setInterval(() => {
      loadRecentBlocks();
    }, 5000);
    return () => clearInterval(timer);
  }, [autoRefresh, loadRecentBlocks]);

  // Select a specific block to view details
  const handleSelectBlock = async (blockNumOrHash) => {
    setIsLoading(true);
    try {
      const full = await fetchBlockByNumberOrHash(rpcUrl, blockNumOrHash, true);
      if (full) {
        setSelectedBlock(full);
      }
    } catch (err) {
      console.warn('Failed to fetch block:', err);
    } finally {
      setIsLoading(false);
    }
  };

  // Search handler
  const handleSearch = async (e) => {
    e.preventDefault();
    const query = searchQuery.trim();
    if (!query) return;

    setIsSearching(true);
    setSearchError(null);

    try {
      // 1. If 32 bytes hex, might be tx hash or block hash
      if (query.startsWith('0x') && query.length === 66) {
        // Try fetching as transaction first
        const txData = await fetchTransactionDetails(rpcUrl, query);
        if (txData && txData.tx) {
          setSelectedTx(txData);
          setIsSearching(false);
          return;
        }

        // Try as block hash
        const block = await fetchBlockByNumberOrHash(rpcUrl, query, true);
        if (block) {
          setSelectedBlock(block);
          setIsSearching(false);
          return;
        }

        setSearchError('No transaction or block found matching this hash.');
      } else {
        // Treat as block number
        const block = await fetchBlockByNumberOrHash(rpcUrl, query, true);
        if (block) {
          setSelectedBlock(block);
          setIsSearching(false);
          return;
        }
        setSearchError('No block found for block number: ' + query);
      }
    } catch (err) {
      setSearchError('Search failed: ' + err.message);
    } finally {
      setIsSearching(false);
    }
  };

  // Open Tx Modal
  const handleInspectTx = async (tx) => {
    if (!tx || !tx.hash) return;
    setIsLoading(true);
    const details = await fetchTransactionDetails(rpcUrl, tx.hash);
    setSelectedTx(details || { tx, receipt: null });
    setIsLoading(false);
  };

  const blockNumberInt = selectedBlock?.number !== undefined && selectedBlock?.number !== null
    ? parseInt(selectedBlock.number, 16)
    : 0;
  const txCount = Array.isArray(selectedBlock?.transactions) ? selectedBlock.transactions.length : 0;

  return (
    <div className="explorer-container">
      {/* Search & Top Action Bar */}
      <div className="card glass-card" style={{ marginBottom: '20px', padding: '18px 24px' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '16px' }}>
          <div>
            <h2 style={{ fontSize: '1.25rem', fontWeight: 700, margin: 0, display: 'flex', alignItems: 'center', gap: '10px' }}>
              <Boxes className="w-6 h-6 text-primary" style={{ color: 'var(--accent-primary)' }} />
              Metanode Block &amp; FullTx Explorer
            </h2>
            <div style={{ fontSize: '0.82rem', color: 'var(--text-muted)', marginTop: '4px' }}>
              Inspecting <strong>{selectedCluster?.name}</strong> &mdash; Chain ID {selectedCluster?.chainId || 991} (EIP-2718 / EIP-1559 / EIP-7702)
            </div>
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
            <label style={{ display: 'flex', alignItems: 'center', gap: '6px', fontSize: '0.8rem', cursor: 'pointer', color: 'var(--text-secondary)' }}>
              <input
                type="checkbox"
                checked={autoRefresh}
                onChange={(e) => setAutoRefresh(e.target.checked)}
                style={{ accentColor: 'var(--accent-primary)' }}
              />
              Auto Live Stream (5s)
            </label>

            <button
              className="btn btn-secondary"
              onClick={loadRecentBlocks}
              disabled={isLoading}
              style={{ padding: '8px 14px', fontSize: '0.82rem' }}
            >
              <RefreshCw className={`w-4 h-4 ${isLoading ? 'animate-spin' : ''}`} />
              Refresh
            </button>
          </div>
        </div>

        {/* Omni Search Bar */}
        <form onSubmit={handleSearch} style={{ marginTop: '16px', display: 'flex', gap: '10px' }}>
          <div style={{ position: 'relative', flex: 1 }}>
            <Search
              className="w-4 h-4"
              style={{ position: 'absolute', left: '14px', top: '50%', transform: 'translateY(-50%)', color: 'var(--text-muted)' }}
            />
            <input
              type="text"
              className="form-input"
              style={{ paddingLeft: '38px', fontFamily: 'monospace' }}
              placeholder="Search by Block Number (e.g. 3815, 0xee7) or 32-byte Tx / Block Hash (0x...)"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
            />
          </div>
          <button type="submit" className="btn btn-primary" disabled={isSearching} style={{ padding: '0 20px' }}>
            {isSearching ? <RefreshCw className="w-4 h-4 animate-spin" /> : 'Inspect'}
          </button>
        </form>

        {searchError && (
          <div style={{ marginTop: '10px', fontSize: '0.8rem', color: '#f87171', display: 'flex', alignItems: 'center', gap: '6px' }}>
            <AlertCircle className="w-4 h-4" />
            {searchError}
          </div>
        )}
      </div>

      {/* Explorer Main Grid */}
      <div className="explorer-grid">
        {/* Left Column: Recent Blocks Stream */}
        <div className="card glass-card" style={{ padding: '20px' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px' }}>
            <h3 style={{ fontSize: '1rem', fontWeight: 600, margin: 0, display: 'flex', alignItems: 'center', gap: '8px' }}>
              <TrendingUp className="w-4 h-4 text-primary" style={{ color: 'var(--accent-primary)' }} />
              Live Blocks Stream
            </h3>
            <span className="badge-pill" style={{ background: 'rgba(59, 130, 246, 0.15)', color: '#60a5fa' }}>
              {recentBlocks.length} Blocks Cached
            </span>
          </div>

          <div className="block-stream-list">
            {recentBlocks.length === 0 ? (
              <div style={{ padding: '30px', textAlign: 'center', color: 'var(--text-muted)' }}>
                No blocks fetched yet. Ensure cluster RPC is online.
              </div>
            ) : (
              recentBlocks.map((b) => {
                const num = parseInt(b.number, 16);
                const isSelected = selectedBlock && parseInt(selectedBlock.number, 16) === num;
                const txsLength = Array.isArray(b.transactions) ? b.transactions.length : 0;

                return (
                  <div
                    key={b.hash || num}
                    className={`block-stream-item ${isSelected ? 'selected' : ''}`}
                    onClick={() => handleSelectBlock(b.number)}
                  >
                    <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
                      <div className="block-cube-icon">
                        <Box className="w-4 h-4" />
                      </div>
                      <div>
                        <div style={{ fontWeight: 700, fontSize: '0.95rem', color: 'var(--text-primary)' }}>
                          Block #{num}
                        </div>
                        <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', fontFamily: 'monospace' }}>
                          {b.hash ? `${b.hash.slice(0, 10)}...${b.hash.slice(-8)}` : '0x...'}
                        </div>
                      </div>
                    </div>

                    <div style={{ textAlign: 'right' }}>
                      <span className="badge-pill" style={{ background: txsLength > 0 ? 'rgba(16, 185, 129, 0.2)' : 'rgba(255, 255, 255, 0.05)', color: txsLength > 0 ? '#34d399' : 'var(--text-muted)' }}>
                        {txsLength} txs
                      </span>
                      <div style={{ fontSize: '0.72rem', color: 'var(--text-muted)', marginTop: '4px' }}>
                        {formatTime(b.timestamp)}
                      </div>
                    </div>
                  </div>
                );
              })
            )}
          </div>
        </div>

        {/* Right Column: Detailed Block & FullTx Inspector */}
        <div className="card glass-card" style={{ padding: '20px' }}>
          {selectedBlock ? (
            <div>
              {/* Block Header Info */}
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px', flexWrap: 'wrap', gap: '10px' }}>
                <div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
                    <h3 style={{ fontSize: '1.2rem', fontWeight: 700, margin: 0 }}>
                      Block #{blockNumberInt}
                    </h3>
                    <span className="badge-pill" style={{ background: 'rgba(139, 92, 246, 0.2)', color: '#a78bfa' }}>
                      Hex: {selectedBlock.number}
                    </span>
                  </div>
                  <div style={{ fontSize: '0.8rem', color: 'var(--text-muted)', marginTop: '4px' }}>
                    Minted: {formatTime(selectedBlock.timestamp)}
                  </div>
                </div>

                <div style={{ display: 'flex', gap: '8px' }}>
                  <button
                    className="btn btn-secondary"
                    onClick={() => handleCopy(selectedBlock.hash, 'block_hash')}
                    style={{ fontSize: '0.75rem', padding: '6px 10px' }}
                  >
                    {copiedKey === 'block_hash' ? <Check className="w-3.5 h-3.5 text-success" /> : <Copy className="w-3.5 h-3.5" />}
                    Copy Block Hash
                  </button>
                </div>
              </div>

              {/* Technical Block Metrics Cards */}
              <div className="block-meta-grid">
                <div className="meta-card">
                  <div className="meta-label">
                    <Hash className="w-3.5 h-3.5" /> Block Hash
                  </div>
                  <div className="meta-val mono" title={selectedBlock.hash}>
                    {selectedBlock.hash || 'N/A'}
                  </div>
                </div>

                <div className="meta-card">
                  <div className="meta-label">
                    <Layers className="w-3.5 h-3.5" /> Parent Hash
                  </div>
                  <div
                    className="meta-val mono clickable"
                    title="Click to inspect parent block"
                    onClick={() => selectedBlock.parentHash && handleSelectBlock(selectedBlock.parentHash)}
                  >
                    {selectedBlock.parentHash || '0x000... (Genesis)'}
                  </div>
                </div>

                <div className="meta-card">
                  <div className="meta-label">
                    <Zap className="w-3.5 h-3.5" /> State Root (Trie/Nomad)
                  </div>
                  <div className="meta-val mono" title={selectedBlock.stateRoot}>
                    {selectedBlock.stateRoot || '0x00000000...'}
                  </div>
                </div>

                <div className="meta-card">
                  <div className="meta-label">
                    <Fuel className="w-3.5 h-3.5" /> Gas Used / Limit
                  </div>
                  <div className="meta-val">
                    {parseInt(selectedBlock.gasUsed || '0x0', 16).toLocaleString()} / {parseInt(selectedBlock.gasLimit || '0x0', 16).toLocaleString()}
                  </div>
                </div>
              </div>

              {/* Transactions Section */}
              <div style={{ marginTop: '24px' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '12px' }}>
                  <h4 style={{ fontSize: '1rem', fontWeight: 600, margin: 0, display: 'flex', alignItems: 'center', gap: '8px' }}>
                    <FileCode className="w-4 h-4 text-primary" style={{ color: 'var(--accent-primary)' }} />
                    Full Transactions in Block ({txCount})
                  </h4>
                  <span style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>
                    Standard Ethereum RPC Format + Metanode GroupID
                  </span>
                </div>

                {txCount === 0 ? (
                  <div className="empty-tx-box">
                    <Box className="w-8 h-8 text-muted" style={{ opacity: 0.4, marginBottom: '8px' }} />
                    <div style={{ fontWeight: 600, color: 'var(--text-secondary)' }}>No Transactions in Block #{blockNumberInt}</div>
                    <div style={{ fontSize: '0.78rem', color: 'var(--text-muted)', marginTop: '4px' }}>
                      Metanode Core sub-second fast consensus blocks produce continuous state proofs even during idle periods.
                    </div>
                  </div>
                ) : (
                  <div className="tx-table-container">
                    <table className="tx-table">
                      <thead>
                        <tr>
                          <th>Tx Hash</th>
                          <th>Type</th>
                          <th>From / To</th>
                          <th>Value</th>
                          <th>Gas / Fee</th>
                          <th>Group ID</th>
                          <th>Action</th>
                        </tr>
                      </thead>
                      <tbody>
                        {selectedBlock.transactions.map((tx, idx) => {
                          const isFull = typeof tx === 'object' && tx !== null;
                          const hash = isFull ? tx.hash : tx;
                          const from = isFull ? tx.from : 'Unknown';
                          const to = isFull ? tx.to : 'Unknown';
                          const valueMtn = isFull ? formatWei(tx.value) : 'N/A';
                          const type = isFull ? tx.type : '0x0';
                          const groupId = isFull ? (tx.groupId !== undefined ? tx.groupId : null) : null;

                          return (
                            <tr key={hash || idx} className="tx-row">
                              <td className="mono" style={{ fontSize: '0.8rem' }}>
                                <span title={hash}>
                                  {hash ? `${hash.slice(0, 8)}...${hash.slice(-6)}` : 'Unknown'}
                                </span>
                              </td>
                              <td>{renderTxTypeBadge(type)}</td>
                              <td style={{ fontSize: '0.78rem' }}>
                                <div className="mono" style={{ color: 'var(--text-secondary)' }}>
                                  {from ? `${from.slice(0, 6)}...${from.slice(-4)}` : 'N/A'}
                                </div>
                                <div className="mono" style={{ color: 'var(--text-muted)', display: 'flex', alignItems: 'center', gap: '4px' }}>
                                  <ArrowRight className="w-3 h-3" />
                                  {to ? `${to.slice(0, 6)}...${to.slice(-4)}` : 'Contract Creation'}
                                </div>
                              </td>
                              <td style={{ fontWeight: 600, color: '#34d399', fontSize: '0.82rem' }}>
                                {valueMtn}
                              </td>
                              <td style={{ fontSize: '0.75rem', color: 'var(--text-muted)' }}>
                                <div>Gas: {isFull ? parseInt(tx.gas || '0x0', 16).toLocaleString() : 'N/A'}</div>
                                {isFull && tx.maxFeePerGas && (
                                  <div>MaxFee: {parseInt(tx.maxFeePerGas, 16) / 1e9} Gwei</div>
                                )}
                              </td>
                              <td>
                                {groupId !== null ? (
                                  <span className="badge-pill" style={{ background: 'rgba(59, 130, 246, 0.2)', color: '#93c5fd' }}>
                                    Grp {groupId}
                                  </span>
                                ) : (
                                  <span style={{ color: 'var(--text-muted)', fontSize: '0.75rem' }}>-</span>
                                )}
                              </td>
                              <td>
                                <button
                                  className="btn btn-secondary"
                                  onClick={() => handleInspectTx(tx)}
                                  style={{ padding: '4px 8px', fontSize: '0.75rem' }}
                                >
                                  Inspect
                                </button>
                              </td>
                            </tr>
                          );
                        })}
                      </tbody>
                    </table>
                  </div>
                )}
              </div>
            </div>
          ) : (
            <div style={{ padding: '60px', textAlign: 'center', color: 'var(--text-muted)' }}>
              Select a block from the left panel to inspect its header and transactions.
            </div>
          )}
        </div>
      </div>

      {/* Transaction Detail Modal */}
      {selectedTx && (
        <div className="modal-backdrop" onClick={() => setSelectedTx(null)}>
          <div className="modal-card glass-modal" onClick={(e) => e.stopPropagation()}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
                <Zap className="w-5 h-5 text-primary" style={{ color: 'var(--accent-primary)' }} />
                <h3 style={{ fontSize: '1.2rem', fontWeight: 700, margin: 0 }}>Transaction Details</h3>
              </div>
              <button className="icon-btn" onClick={() => setSelectedTx(null)}>
                <X className="w-5 h-5" />
              </button>
            </div>

            <div className="tx-details-content">
              <div className="detail-item">
                <span className="detail-label">Transaction Hash</span>
                <span className="detail-val mono" style={{ wordBreak: 'break-all' }}>
                  {selectedTx.tx?.hash || 'N/A'}
                  <button
                    className="copy-btn"
                    onClick={() => handleCopy(selectedTx.tx?.hash, 'modal_tx')}
                    style={{ marginLeft: '8px' }}
                  >
                    {copiedKey === 'modal_tx' ? <Check className="w-3.5 h-3.5 text-success" /> : <Copy className="w-3.5 h-3.5" />}
                  </button>
                </span>
              </div>

              <div className="detail-item">
                <span className="detail-label">Execution Status</span>
                <span className="detail-val">
                  {selectedTx.receipt?.status === '0x1' ? (
                    <span className="status-badge success">
                      <CheckCircle2 className="w-3.5 h-3.5" /> Success (0x1)
                    </span>
                  ) : selectedTx.receipt?.status === '0x0' ? (
                    <span className="status-badge danger">
                      <AlertCircle className="w-3.5 h-3.5" /> Reverted (0x0)
                    </span>
                  ) : (
                    <span className="status-badge warning">Pending / Finalized in Block</span>
                  )}
                </span>
              </div>

              <div className="detail-item">
                <span className="detail-label">Standard Type</span>
                <span className="detail-val">{renderTxTypeBadge(selectedTx.tx?.type)}</span>
              </div>

              <div className="detail-item">
                <span className="detail-label">Block Number</span>
                <span className="detail-val mono">
                  {selectedTx.tx?.blockNumber ? parseInt(selectedTx.tx.blockNumber, 16) : 'N/A'}
                </span>
              </div>

              <div className="detail-item">
                <span className="detail-label">From</span>
                <span className="detail-val mono">{selectedTx.tx?.from || 'N/A'}</span>
              </div>

              <div className="detail-item">
                <span className="detail-label">To</span>
                <span className="detail-val mono">{selectedTx.tx?.to || 'Contract Creation (0x0)'}</span>
              </div>

              <div className="detail-item">
                <span className="detail-label">Value</span>
                <span className="detail-val" style={{ color: '#34d399', fontWeight: 700 }}>
                  {formatWei(selectedTx.tx?.value)}
                </span>
              </div>

              <div className="detail-item">
                <span className="detail-label">Nonce</span>
                <span className="detail-val mono">
                  {selectedTx.tx?.nonce !== undefined ? parseInt(selectedTx.tx.nonce, 16) : 'N/A'}
                </span>
              </div>

              <div className="detail-item">
                <span className="detail-label">Gas Used</span>
                <span className="detail-val mono">
                  {selectedTx.receipt?.gasUsed
                    ? `${parseInt(selectedTx.receipt.gasUsed, 16).toLocaleString()} (${parseInt(selectedTx.tx?.gas || '0x0', 16).toLocaleString()} limit)`
                    : `${parseInt(selectedTx.tx?.gas || '0x0', 16).toLocaleString()} limit`}
                </span>
              </div>

              {selectedTx.tx?.groupId !== undefined && selectedTx.tx?.groupId !== null && (
                <div className="detail-item">
                  <span className="detail-label">Metanode Group ID</span>
                  <span className="detail-val mono" style={{ color: '#60a5fa' }}>
                    Group #{selectedTx.tx.groupId}
                  </span>
                </div>
              )}

              {/* Raw JSON viewer */}
              <div style={{ marginTop: '16px' }}>
                <span className="detail-label" style={{ marginBottom: '6px', display: 'block' }}>
                  Raw RPCTransaction Payload
                </span>
                <pre className="code-block-viewer">
                  {JSON.stringify(selectedTx.tx, null, 2)}
                </pre>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
