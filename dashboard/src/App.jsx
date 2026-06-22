import { useState, useEffect } from 'react';
import { Activity, Search, Box, Database, FileText, Settings, Key, User } from 'lucide-react';
import { getPerformanceMetrics, getBlockByNumber, getTransactionByHash, getAccountState, defaultRpcUrl } from './rpcClient';
import './index.css';

function App() {
  const [activeTab, setActiveTab] = useState('performance');
  const [rpcUrl, setRpcUrl] = useState(() => {
    return localStorage.getItem('rpcUrl') || 'http://localhost:8545';
  });
  
  // Performance State
  const [metrics, setMetrics] = useState(null);
  const [isLive, setIsLive] = useState(true);
  const [connectionError, setConnectionError] = useState(null);

  // Explorer State
  const [searchType, setSearchType] = useState('block');
  const [searchValue, setSearchValue] = useState('');
  const [searchResult, setSearchResult] = useState(null);
  const [searchLoading, setSearchLoading] = useState(false);
  const [searchError, setSearchError] = useState('');

  // Fetch metrics periodically
  useEffect(() => {
    let interval;
    if (activeTab === 'performance' && isLive) {
      const fetchMetrics = async () => {
        try {
          const data = await getPerformanceMetrics(rpcUrl, 100);
          setMetrics(data);
          setConnectionError(null);
        } catch (err) {
          console.error("Failed to fetch metrics", err);
          setConnectionError(err.message || "Connection failed");
        }
      };
      fetchMetrics();
      interval = setInterval(fetchMetrics, 3000);
    }
    return () => clearInterval(interval);
  }, [activeTab, isLive, rpcUrl]);

  const handleRpcUrlChange = (e) => {
    const newUrl = e.target.value;
    setRpcUrl(newUrl);
    localStorage.setItem('rpcUrl', newUrl);
  };

  const handleSearch = async (e) => {
    e.preventDefault();
    if (!searchValue && searchType !== 'latestBlock') return;
    
    setSearchLoading(true);
    setSearchError('');
    setSearchResult(null);
    
    try {
      let result;
      if (searchType === 'block') {
        result = await getBlockByNumber(rpcUrl, searchValue);
      } else if (searchType === 'transaction') {
        result = await getTransactionByHash(rpcUrl, searchValue);
      } else if (searchType === 'account') {
        result = await getAccountState(rpcUrl, searchValue);
      } else if (searchType === 'latestBlock') {
        const { getLatestBlockNumber } = await import('./rpcClient');
        const blockHex = await getLatestBlockNumber(rpcUrl);
        result = await getBlockByNumber(rpcUrl, blockHex);
      }
      setSearchResult(result);
    } catch (err) {
      setSearchError(err.message || "Failed to fetch data");
    } finally {
      setSearchLoading(false);
    }
  };

  return (
    <div className="app-container">
      <nav className="navbar">
        <div className="nav-brand">
          <img src="/logo.png" alt="Logo" style={{ height: '32px' }} />
          <span>Metanode<span style={{color: 'var(--text-secondary)', fontWeight: 300}}>Dashboard</span></span>
        </div>
        <div style={{ display: 'flex', gap: '1rem', alignItems: 'center' }}>
          <div className="input-group" style={{ marginBottom: 0, flexDirection: 'row', alignItems: 'center' }}>
            <Database size={16} color="var(--text-secondary)" />
            <input 
              type="text" 
              value={rpcUrl} 
              onChange={handleRpcUrlChange}
              className="input-field"
              style={{ width: '250px', fontSize: '0.9rem' }}
              placeholder="http://localhost:8545"
            />
          </div>
        </div>
      </nav>

      <main className="main-content">
        {connectionError && (
          <div style={{ padding: '1rem', marginBottom: '1.5rem', background: 'rgba(255, 0, 60, 0.1)', border: '1px solid var(--accent-danger)', borderRadius: 'var(--radius-sm)', color: 'var(--accent-danger)' }}>
            <strong>Connection Error:</strong> {connectionError}. Vui lòng kiểm tra lại RPC URL hoặc trạng thái của Node.
          </div>
        )}
        
        <div className="tabs-container">
          <button 
            className={`tab-btn ${activeTab === 'performance' ? 'active' : ''}`}
            onClick={() => setActiveTab('performance')}
          >
            <Activity size={18} style={{ display: 'inline', marginRight: '8px', verticalAlign: 'text-bottom' }} />
            Performance
          </button>
          <button 
            className={`tab-btn ${activeTab === 'explorer' ? 'active' : ''}`}
            onClick={() => setActiveTab('explorer')}
          >
            <Search size={18} style={{ display: 'inline', marginRight: '8px', verticalAlign: 'text-bottom' }} />
            Explorer
          </button>
        </div>

        {activeTab === 'performance' && (
          <div className="animate-fade-in">
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
              <h2 className="text-gradient">Real-time Telemetry</h2>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', color: 'var(--text-secondary)' }}>
                <span className={isLive ? "live-indicator" : ""}></span>
                {isLive ? 'Live Updates (3s)' : 'Paused'}
              </div>
            </div>
            
            <div className="dashboard-grid">
              <div className="glass-panel metric-card" style={{ '--card-accent': 'var(--accent-orange)' }}>
                <div className="metric-header">
                  <span>Current TPS</span>
                  <Activity size={18} />
                </div>
                <div>
                  <span className="metric-value">{metrics && metrics.tps !== undefined ? metrics.tps.toFixed(2) : '--'}</span>
                  <span className="metric-unit">tx/s</span>
                </div>
                <div style={{ fontSize: '0.85rem', color: 'var(--text-muted)' }}>
                  Transactions per second (real-time)
                </div>
              </div>

              <div className="glass-panel metric-card" style={{ '--card-accent': 'var(--accent-purple)' }}>
                <div className="metric-header">
                  <span>Avg Mempool Latency</span>
                  <Settings size={18} />
                </div>
                <div>
                  <span className="metric-value">{metrics ? metrics.avgMempoolMs.toFixed(1) : '--'}</span>
                  <span className="metric-unit">ms</span>
                </div>
                <div style={{ fontSize: '0.85rem', color: 'var(--text-muted)' }}>
                  Time in pending pool before consensus
                </div>
              </div>

              <div className="glass-panel metric-card" style={{ '--card-accent': 'var(--accent-cyan)' }}>
                <div className="metric-header">
                  <span>Avg Consensus Latency</span>
                  <Database size={18} />
                </div>
                <div>
                  <span className="metric-value">{metrics ? metrics.avgConsensusMs.toFixed(1) : '--'}</span>
                  <span className="metric-unit">ms</span>
                </div>
                <div style={{ fontSize: '0.85rem', color: 'var(--text-muted)' }}>
                  Time spent in Rust DAG consensus
                </div>
              </div>

              <div className="glass-panel metric-card" style={{ '--card-accent': 'var(--accent-green)' }}>
                <div className="metric-header">
                  <span>Avg Execution Latency</span>
                  <Box size={18} />
                </div>
                <div>
                  <span className="metric-value">{metrics ? metrics.avgExecutionMs.toFixed(1) : '--'}</span>
                  <span className="metric-unit">ms</span>
                </div>
                <div style={{ fontSize: '0.85rem', color: 'var(--text-muted)' }}>
                  Time spent in EVM/MVM execution
                </div>
              </div>

              <div className="glass-panel metric-card" style={{ '--card-accent': 'var(--accent-danger)' }}>
                <div className="metric-header">
                  <span>Avg End-to-End Latency</span>
                  <Activity size={18} />
                </div>
                <div>
                  <span className="metric-value">{metrics ? metrics.avgEndToEndMs.toFixed(1) : '--'}</span>
                  <span className="metric-unit">ms</span>
                </div>
                <div style={{ fontSize: '0.85rem', color: 'var(--text-muted)' }}>
                  Total tx lifecycle time
                </div>
              </div>
            </div>
            
            <div className="glass-panel" style={{ padding: '1.5rem' }}>
              <h3 style={{ marginBottom: '1rem', color: 'var(--text-secondary)' }}>Status Info</h3>
              <div className="data-row">
                <span className="data-key">Analyzed Transactions</span>
                <span className="data-value">{metrics ? metrics.analyzedTxCount : '--'}</span>
              </div>
              <div className="data-row">
                <span className="data-key">Node Endpoint</span>
                <span className="data-value" style={{ color: 'var(--accent-cyan)' }}>{rpcUrl}</span>
              </div>
            </div>
          </div>
        )}

        {activeTab === 'explorer' && (
          <div className="animate-fade-in">
            <h2 className="text-gradient" style={{ marginBottom: '1.5rem' }}>Universal Explorer</h2>
            
            <div className="glass-panel" style={{ padding: '2rem', marginBottom: '2rem' }}>
              <form onSubmit={handleSearch} style={{ display: 'flex', gap: '1rem', alignItems: 'flex-end' }}>
                <div className="input-group" style={{ flex: 1, marginBottom: 0 }}>
                  <label className="input-label">Search Type</label>
                  <select 
                    className="input-field" 
                    value={searchType}
                    onChange={(e) => setSearchType(e.target.value)}
                    style={{ appearance: 'none' }}
                  >
                    <option value="block">Block by Number</option>
                    <option value="latestBlock">Latest Block</option>
                    <option value="transaction">Transaction Hash</option>
                    <option value="account">Account Address</option>
                  </select>
                </div>
                
                <div className="input-group" style={{ flex: 3, marginBottom: 0 }}>
                  <label className="input-label">
                    {searchType === 'block' && 'Enter Block Number (e.g., 100)'}
                    {searchType === 'latestBlock' && 'No input required'}
                    {searchType === 'transaction' && 'Enter Tx Hash (0x...)'}
                    {searchType === 'account' && 'Enter Account Address (0x...)'}
                  </label>
                  <input 
                    type="text" 
                    className="input-field" 
                    value={searchValue}
                    onChange={(e) => setSearchValue(e.target.value)}
                    placeholder={searchType === 'block' ? '12345' : '0x...'}
                    disabled={searchType === 'latestBlock'}
                  />
                </div>
                
                <button type="submit" className="btn btn-primary" disabled={searchLoading}>
                  {searchLoading ? 'Searching...' : <><Search size={18} /> Search</>}
                </button>
              </form>
            </div>

            {searchError && (
              <div className="glass-panel" style={{ padding: '1.5rem', borderColor: 'var(--accent-danger)', background: 'rgba(255, 0, 60, 0.05)' }}>
                <span style={{ color: 'var(--accent-danger)' }}>Error: {searchError}</span>
              </div>
            )}

            {searchResult && (
              <div className="glass-panel" style={{ padding: '2rem' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
                  <h3 style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                    {(searchType === 'block' || searchType === 'latestBlock') && <><Box color="var(--accent-cyan)" /> Block Details</>}
                    {searchType === 'transaction' && <><FileText color="var(--accent-purple)" /> Transaction Details</>}
                    {searchType === 'account' && <><User color="var(--accent-orange)" /> Account State</>}
                  </h3>
                </div>

                <div className="data-table-container">
                  {(searchType === 'block' || searchType === 'latestBlock') && searchResult && (
                    <div>
                      <div className="data-row">
                        <span className="data-key">Block Number</span>
                        <span className="data-value">{parseInt(searchResult.number, 16)}</span>
                      </div>
                      <div className="data-row">
                        <span className="data-key">Hash</span>
                        <span className="data-value">{searchResult.hash}</span>
                      </div>
                      <div className="data-row">
                        <span className="data-key">Timestamp</span>
                        <span className="data-value">{new Date(parseInt(searchResult.timestamp, 16) * 1000).toLocaleString()}</span>
                      </div>
                      <div className="data-row">
                        <span className="data-key">Transactions</span>
                        <span className="data-value">{searchResult.transactions?.length || 0}</span>
                      </div>
                      <div className="data-row">
                        <span className="data-key">State Root</span>
                        <span className="data-value">{searchResult.stateRoot}</span>
                      </div>
                    </div>
                  )}

                  {searchType === 'transaction' && searchResult && (
                    <div>
                      <div className="data-row">
                        <span className="data-key">Hash</span>
                        <span className="data-value">{searchResult.hash}</span>
                      </div>
                      <div className="data-row">
                        <span className="data-key">Block Number</span>
                        <span className="data-value">{searchResult.blockNumber ? parseInt(searchResult.blockNumber, 16) : 'Pending'}</span>
                      </div>
                      <div className="data-row">
                        <span className="data-key">From</span>
                        <span className="data-value">{searchResult.from}</span>
                      </div>
                      <div className="data-row">
                        <span className="data-key">To</span>
                        <span className="data-value">{searchResult.to || 'Contract Creation'}</span>
                      </div>
                      <div className="data-row">
                        <span className="data-key">Value</span>
                        <span className="data-value">{parseInt(searchResult.value, 16)} wei</span>
                      </div>
                    </div>
                  )}

                  {searchType === 'account' && searchResult && (
                    <div>
                      <div className="data-row">
                        <span className="data-key">Address</span>
                        <span className="data-value">{searchValue}</span>
                      </div>
                      <div className="data-row">
                        <span className="data-key">Balance</span>
                        <span className="data-value">{searchResult.balance || '0'} wei</span>
                      </div>
                      <div className="data-row">
                        <span className="data-key">Nonce</span>
                        <span className="data-value">{searchResult.nonce || 0}</span>
                      </div>
                      <div className="data-row">
                        <span className="data-key">Account Type</span>
                        <span className="data-value">
                          {searchResult.type === 0 ? 'User/EOA' : searchResult.type === 1 ? 'Smart Contract' : 'Unknown'}
                        </span>
                      </div>
                    </div>
                  )}
                </div>
                
                <div style={{ marginTop: '2rem' }}>
                  <h4 style={{ marginBottom: '0.5rem', color: 'var(--text-secondary)' }}>Raw JSON</h4>
                  <pre>{JSON.stringify(searchResult, null, 2)}</pre>
                </div>
              </div>
            )}
          </div>
        )}
      </main>
    </div>
  );
}

export default App;
