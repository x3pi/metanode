import { useState, useEffect } from "react";
import { useRobot } from "./contexts/RobotContext";
import { privateKey } from "./constants/customeChain";
import { hexToString } from "viem";

function App() {
  const {
    account,
    isConnected,
    connectWallet,
    disconnectWallet,
    dispatch,
    getDataByTxhash,
    chainEvents,
    interceptorEvents,
    clearChainEvents,
    clearInterceptorEvents,
    clearAllEvents,
    isLoading,
    error,
  } = useRobot();

  // Form states
  const [privateKeyInput, setPrivateKeyInput] = useState("");
  const [sessionId, setSessionId] = useState("");
  const [actionId, setActionId] = useState("");
  const [data, setData] = useState("");
  const [activeTab, setActiveTab] = useState("chain"); // "chain" or "interceptor"
  
  // Get data by txHash states
  const [txHashInput, setTxHashInput] = useState("");
  const [txData, setTxData] = useState(null);
  const [isLoadingTxData, setIsLoadingTxData] = useState(false);
  
  // Spam test states
  const [isSpamming, setIsSpamming] = useState(false);
  const [spamProgress, setSpamProgress] = useState({ 
    sent: 0, 
    success: 0, 
    failed: 0, 
    errors: [],
    missing: [] // Danh sách index bị miss
  });
  const [sentIndicesSet, setSentIndicesSet] = useState(new Set()); // Lưu các index đã gửi để check sau
  const [checkResult, setCheckResult] = useState(null); // Kết quả check events

  // Auto-connect with private key from constants on mount
  useEffect(() => {
    if (!isConnected && privateKey) {
      console.log("Auto-connecting with private key from constants...");
      connectWallet(privateKey);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []); // Only run once on mount

  const handleConnect = () => {
    const keyToUse = privateKeyInput.trim() || privateKey;
    if (!keyToUse) {
      alert("Please enter a private key");
      return;
    }
    connectWallet(keyToUse);
  };

  const handleDispatch = async (e) => {
    e.preventDefault();
    if (!sessionId || !actionId || !data) {
      alert("Please fill all fields");
      return;
    }

    try {
      // console.log(`Dispatching sessionId=${sessionId}, actionId=${actionId}, data=${data}`);
      await dispatch(sessionId, actionId, data);
      // console.log(`✅ Dispatch successful! Transaction hash: ${hash}`);
      // Don't reset form to allow spam testing
    } catch (err) {
      alert(`Error: ${err.message}`);
    }
  };

  const handleGetDataByTxhash = async (e) => {
    e.preventDefault();
    if (!txHashInput || txHashInput.trim() === "") {
      alert("Please enter a transaction hash");
      return;
    }

    setIsLoadingTxData(true);
    setTxData(null);
    try {
      const data = await getDataByTxhash(txHashInput.trim());
      setTxData(data);
      console.log("✅ Data retrieved:", data);
    } catch (err) {
      alert(`Error: ${err.message}`);
      setTxData(null);
    } finally {
      setIsLoadingTxData(false);
    }
  };
// Spam dispatch commands để test nonce
const handleSpamDispatch = async () => {
  // Validate input
  if (!sessionId || sessionId.trim() === "") {
    alert("Please fill Session ID field first");
    return;
  }
  if (!actionId || actionId.trim() === "") {
    alert("Please fill Action ID field first");
    return;
  }
  if (!data || data.trim() === "") {
    alert("Please fill Data field first");
    return;
  }

  if (isSpamming) {
    alert("Spam test is already running!");
    return;
  }

  setIsSpamming(true);
  // Reset progress, loại bỏ missing list
  setSpamProgress({ sent: 0, success: 0, failed: 0, errors: [] });
  setCheckResult(null); // Reset check result

  const total = 1000;
  const batchSize = 1; 
  let successCount = 0;
  let failedCount = 0;
  const allErrors = [];
  const sentIndices = new Set(); // Lưu các index đã gửi thành công (1-based)

  try {
    for (let i = 0; i < total; i += batchSize) {
      const batch = [];
      const batchEnd = Math.min(i + batchSize, total);

      for (let j = i; j < batchEnd; j++) {
        const index = j + 1;
        let dataWithIndex;
        if (data.startsWith("0x")) {
          dataWithIndex = data;
        } else {
          dataWithIndex = `${data} [${index}]`;
        }
        
        batch.push(
          dispatch(sessionId, actionId, dataWithIndex)
            .then((hash) => {
              successCount++;
              sentIndices.add(index); // Lưu index đã gửi thành công
              setSpamProgress((prev) => ({
                ...prev,
                sent: prev.sent + 1,
                success: successCount,
              }));
              console.log(`✅ Success at index ${index}, hash: ${hash}`);
            })
            .catch((err) => {
              const errorMsg = err?.message || String(err) || "Unknown error";
              failedCount++;
              const errorObj = { index: index, error: errorMsg };
              allErrors.push(errorObj);
              
              setSpamProgress((prev) => ({
                ...prev,
                sent: prev.sent + 1,
                failed: failedCount,
                errors: [...prev.errors.slice(-9), errorObj], 
              }));
              console.error(`❌ Error at index ${index}:`, err);
            })
        );
      }

      // Đợi batch hiện tại hoàn tất
      await Promise.allSettled(batch);

      // Delay nhỏ giữa các batch để tránh bị rate limit RPC
      // if (batchEnd < total) {
      //   await new Promise((resolve) => setTimeout(resolve, 50));
      // }
    }

    console.log("✅ Spam test completed!", {
      totalSent: total,
      success: successCount,
      failed: failedCount
    });

    // Lưu sentIndices vào state để có thể check lại sau
    setSentIndicesSet(new Set(sentIndices));
    console.log(`📝 Saved ${sentIndices.size} sent indices for checking`);

  } catch (err) {
    console.error("❌ Spam test critical error:", err);
    alert(`Spam test error: ${err.message}`);
  } finally {
    setIsSpamming(false);
  }
};

  // Hàm check events trên chain
  const handleCheckEvents = () => {
    if (sentIndicesSet.size === 0) {
      alert("Chưa có dữ liệu spam để check. Hãy chạy spam test trước.");
      return;
    }

    // Lấy tất cả chain events (chỉ EmitSentence)
    const chainEventsFiltered = (chainEvents || []).filter(
      (e) => e.eventName === "EmitSentence"
    );

    const receivedIndices = new Set();
    // Extract index từ data trong events
    chainEventsFiltered.forEach((event) => {
      try {
        if (event.data && typeof event.data === "string") {
          // Nếu data là hex string, decode nó
          let decodedData = event.data;
          if (event.data.startsWith("0x") && event.data.length > 2) {
            try {
              decodedData = hexToString(event.data);
            } catch {
              decodedData = event.data;
            }
          }
          
          // Tìm pattern `[number]` trong decoded data
          const match = decodedData.match(/\[(\d+)\]/);
          if (match) {
            const index = parseInt(match[1], 10);
            receivedIndices.add(index);
          }
        }
      } catch (e) {
        console.warn("⚠️ Error parsing event data:", e);
      }
    });

    // Tìm các index bị miss (đã gửi nhưng chưa nhận được event)
    const missingIndices = Array.from(sentIndicesSet).filter(
      (index) => !receivedIndices.has(index)
    );
    missingIndices.sort((a, b) => a - b);

    const result = {
      sentCount: sentIndicesSet.size,
      receivedCount: receivedIndices.size,
      totalEvents: chainEventsFiltered.length,
      missingCount: missingIndices.length,
      missingIndices: missingIndices,
      receivedIndices: Array.from(receivedIndices).sort((a, b) => a - b),
    };

    setCheckResult(result);
    console.log("🔍 Check Events Result:", result);
    console.log(`📊 Sent: ${result.sentCount}, Received: ${result.receivedCount}, Missing: ${result.missingCount}`);
    if (missingIndices.length > 0) {
      console.log("⚠️ Missing indices:", missingIndices);
      console.log(`⚠️ First 20 missing: ${missingIndices.slice(0, 20).join(", ")}`);
    } else {
      console.log("✅ All sent events were received on chain!");
    }
  };

  return (
    <div className="min-h-screen bg-gray-100 p-8">
      <div className="max-w-6xl mx-auto">
        <h1 className="text-4xl font-bold text-center mb-8 text-gray-800">
          Robot Contract Demo
        </h1>

        {/* Wallet Connection Section */}
        <div className="bg-white rounded-lg shadow-md p-6 mb-6">
          <h2 className="text-2xl font-semibold mb-4">Wallet Connection</h2>
          {!isConnected ? (
            <div className="space-y-4">
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-2">
                  Private Key (Optional - will use default from constants if empty)
                </label>
                <input
                  type="password"
                  value={privateKeyInput}
                  onChange={(e) => setPrivateKeyInput(e.target.value)}
                  placeholder={
                    privateKey
                      ? `Default: ${privateKey.slice(0, 10)}... (from constants)`
                      : "Enter your private key (0x... or without 0x)"
                  }
                  className="w-full px-4 py-2 border border-gray-300 rounded-md focus:ring-2 focus:ring-blue-500 focus:border-transparent"
                />
              </div>
              <button
                onClick={handleConnect}
                className="w-full bg-blue-600 text-white py-2 px-4 rounded-md hover:bg-blue-700 transition-colors"
              >
                Connect Wallet
              </button>
            </div>
          ) : (
            <div className="space-y-4">
              <div className="bg-green-50 border border-green-200 rounded-md p-4">
                <p className="text-sm text-gray-600">Connected Account:</p>
                <p className="font-mono text-sm text-green-800 break-all">
                  {account}
                </p>
              </div>
              <button
                onClick={disconnectWallet}
                className="w-full bg-red-600 text-white py-2 px-4 rounded-md hover:bg-red-700 transition-colors"
              >
                Disconnect Wallet
              </button>
            </div>
          )}
        </div>

        {/* Error Display */}
        {error && (
          <div className="bg-red-50 border border-red-200 rounded-md p-4 mb-6">
            <p className="text-red-800 text-sm">{error}</p>
          </div>
        )}

        {/* Dispatch Section */}
        {isConnected && (
          <div className="bg-white rounded-lg shadow-md p-6 mb-6">
            <h2 className="text-2xl font-semibold mb-4">Dispatch</h2>
            <form onSubmit={handleDispatch} className="space-y-4">
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-2">
                  Session ID (bytes32 - hex string)
                </label>
                <input
                  type="text"
                  value={sessionId}
                  onChange={(e) => setSessionId(e.target.value)}
                  placeholder="e.g., 0x1234... or hex string"
                  className="w-full px-4 py-2 border border-gray-300 rounded-md focus:ring-2 focus:ring-blue-500 focus:border-transparent"
                />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-2">
                  Action ID (bytes32 - hex string)
                </label>
                <input
                  type="text"
                  value={actionId}
                  onChange={(e) => setActionId(e.target.value)}
                  placeholder="e.g., 0x5678... or hex string"
                  className="w-full px-4 py-2 border border-gray-300 rounded-md focus:ring-2 focus:ring-blue-500 focus:border-transparent"
                />
              </div>
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-2">
                  Data (bytes - hex string or text)
                </label>
                <textarea
                  value={data}
                  onChange={(e) => setData(e.target.value)}
                  placeholder="Enter data (text or hex string starting with 0x)"
                  rows={3}
                  className="w-full px-4 py-2 border border-gray-300 rounded-md focus:ring-2 focus:ring-blue-500 focus:border-transparent"
                />
              </div>
              <div className="flex gap-2">
                <button
                  type="submit"
                  disabled={isLoading || isSpamming}
                  className="flex-1 bg-purple-600 text-white py-2 px-4 rounded-md hover:bg-purple-700 transition-colors disabled:bg-gray-400 disabled:cursor-not-allowed"
                >
                  {isLoading ? "Processing..." : "Dispatch"}
                </button>
                <button
                  type="button"
                  onClick={handleSpamDispatch}
                  disabled={isLoading || isSpamming || !sessionId || !actionId || !data}
                  className="flex-1 bg-orange-600 text-white py-2 px-4 rounded-md hover:bg-orange-700 transition-colors disabled:bg-gray-400 disabled:cursor-not-allowed"
                >
                  {isSpamming ? "Spamming..." : "🚀 Spam 1000x"}
                </button>
              </div>
            </form>

            {/* Spam Progress Display */}
            {isSpamming && (
              <div className="mt-4 p-4 bg-orange-50 border border-orange-200 rounded-md">
                <div className="flex items-center justify-between mb-2">
                  <h3 className="text-sm font-semibold text-orange-800">
                    Spam Test Progress
                  </h3>
                  <span className="text-xs text-orange-600">
                    {spamProgress.sent} / 1000
                  </span>
                </div>
                <div className="w-full bg-orange-200 rounded-full h-2 mb-2">
                  <div
                    className="bg-orange-600 h-2 rounded-full transition-all duration-300"
                    style={{ width: `${(spamProgress.sent / 1000) * 100}%` }}
                  ></div>
                </div>
                <div className="flex gap-4 text-xs">
                  <span className="text-green-600">
                    ✅ Success: {spamProgress.success}
                  </span>
                  <span className="text-red-600">
                    ❌ Failed: {spamProgress.failed}
                  </span>
                  {spamProgress.missing && spamProgress.missing.length > 0 && (
                    <span className="text-orange-600">
                      ⚠️ Missing: {spamProgress.missing.length}
                    </span>
                  )}
                </div>
                {spamProgress.errors.length > 0 && (
                  <div className="mt-2 max-h-32 overflow-y-auto">
                    <p className="text-xs font-semibold text-red-600 mb-1">
                      Recent Errors (showing last 5):
                    </p>
                    {spamProgress.errors.slice(-5).map((err, idx) => (
                      <div
                        key={idx}
                        className="text-xs text-red-700 bg-red-50 p-1 mb-1 rounded"
                      >
                        <span className="font-mono">#{err.index}:</span>{" "}
                        {err.error}
                      </div>
                    ))}
                  </div>
                )}
                {spamProgress.missing && spamProgress.missing.length > 0 && (
                  <div className="mt-2 max-h-32 overflow-y-auto">
                    <p className="text-xs font-semibold text-orange-600 mb-1">
                      Missing Events ({spamProgress.missing.length} total):
                    </p>
                    <div className="text-xs text-orange-700 bg-orange-50 p-1 rounded">
                      <span className="font-mono">
                        {spamProgress.missing.length <= 20
                          ? spamProgress.missing.join(", ")
                          : `${spamProgress.missing.slice(0, 20).join(", ")} ... (${spamProgress.missing.length} total)`}
                      </span>
                    </div>
                  </div>
                )}
              </div>
            )}

            {/* Check Events Button và Result */}
            {sentIndicesSet.size > 0 && (
              <div className="mt-4 p-4 bg-blue-50 border border-blue-200 rounded-md">
                <div className="flex items-center justify-between mb-2">
                  <h3 className="text-sm font-semibold text-blue-800">
                    Check Events on Chain
                  </h3>
                  <button
                    onClick={handleCheckEvents}
                    disabled={isSpamming}
                    className="px-3 py-1 bg-blue-600 text-white text-xs rounded hover:bg-blue-700 transition-colors disabled:bg-gray-400 disabled:cursor-not-allowed"
                  >
                    🔍 Check
                  </button>
                </div>
                
                {checkResult && (
                  <div className="mt-2 space-y-2 text-xs">
                    <div className="flex gap-4">
                      <span className="text-blue-700">
                        📤 Sent: <strong>{checkResult.sentCount}</strong>
                      </span>
                      <span className="text-green-700">
                        ✅ Received: <strong>{checkResult.receivedCount}</strong>
                      </span>
                      <span className="text-orange-700">
                        ⚠️ Missing: <strong>{checkResult.missingCount}</strong>
                      </span>
                      <span className="text-gray-700">
                        📊 Total Events: <strong>{checkResult.totalEvents}</strong>
                      </span>
                    </div>
                    
                    {checkResult.missingCount > 0 && (
                      <div className="mt-2 p-2 bg-orange-50 border border-orange-200 rounded">
                        <p className="text-xs font-semibold text-orange-600 mb-1">
                          Missing Indices ({checkResult.missingCount} total):
                        </p>
                        <div className="text-xs text-orange-700 font-mono break-all">
                          {checkResult.missingIndices.length <= 30
                            ? checkResult.missingIndices.join(", ")
                            : `${checkResult.missingIndices.slice(0, 30).join(", ")} ... (${checkResult.missingIndices.length} total)`}
                        </div>
                      </div>
                    )}
                    
                    {checkResult.missingCount === 0 && (
                      <div className="mt-2 p-2 bg-green-50 border border-green-200 rounded text-green-700">
                        ✅ All sent events were received on chain!
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>
        )}

        {/* Get Data By TxHash Section */}
        {isConnected && (
          <div className="bg-white rounded-lg shadow-md p-6 mb-6">
            <h2 className="text-2xl font-semibold mb-4">Get Data By TxHash</h2>
            <form onSubmit={handleGetDataByTxhash} className="space-y-4">
              <div>
                <label className="block text-sm font-medium text-gray-700 mb-2">
                  Transaction Hash
                </label>
                <input
                  type="text"
                  value={txHashInput}
                  onChange={(e) => setTxHashInput(e.target.value)}
                  placeholder="0x..."
                  className="w-full px-4 py-2 border border-gray-300 rounded-md focus:ring-2 focus:ring-blue-500 focus:border-transparent"
                />
              </div>
              <button
                type="submit"
                disabled={isLoadingTxData}
                className="w-full bg-green-600 text-white py-2 px-4 rounded-md hover:bg-green-700 transition-colors disabled:bg-gray-400 disabled:cursor-not-allowed"
              >
                {isLoadingTxData ? "Loading..." : "Get Data"}
              </button>
            </form>

            {/* Display Error Data */}
            {txData && (
              <div className="mt-4 p-4 bg-red-50 border border-red-200 rounded-md">
                <h3 className="text-lg font-semibold text-red-800 mb-2">
                  Error Data
                </h3>
                <div className="space-y-2 text-sm">
                  <div>
                    <span className="font-semibold">TxHash:</span>{" "}
                    <span className="font-mono">{txData.txHash}</span>
                  </div>
                  <div>
                    <span className="font-semibold">Error Message:</span>{" "}
                    <span className="text-red-700 break-all">{txData.errorMessage || "N/A"}</span>
                  </div>
                  <div>
                    <span className="font-semibold">Created At:</span>{" "}
                    {new Date(txData.createdAt * 1000).toLocaleString()}
                  </div>
                  {txData.inputData && (
                    <div>
                      <span className="font-semibold">Input Data:</span>
                      <div className="mt-2 p-2 bg-white rounded border border-red-200">
                        {typeof txData.inputData === "string" ? (
                          <pre className="text-xs break-all whitespace-pre-wrap">
                            {txData.inputData}
                          </pre>
                        ) : (
                          <div className="space-y-1 text-xs">
                            {txData.inputData.sessionId && (
                              <div>
                                <span className="font-semibold">Session ID:</span>{" "}
                                <span className="font-mono">{txData.inputData.sessionId}</span>
                              </div>
                            )}
                            {txData.inputData.actionId && (
                              <div>
                                <span className="font-semibold">Action ID:</span>{" "}
                                <span className="font-mono">{txData.inputData.actionId}</span>
                              </div>
                            )}
                            {txData.inputData.data && (
                              <div>
                                <span className="font-semibold">Data:</span>{" "}
                                <span className="font-mono break-all">
                                  {txData.inputData.data}
                                </span>
                              </div>
                            )}
                          </div>
                        )}
                      </div>
                    </div>
                  )}
                </div>
              </div>
            )}
          </div>
        )}

        {/* Events Display Section with Tabs */}
        <div className="bg-white rounded-lg shadow-md p-6">
          <div className="flex justify-between items-center mb-4">
            <h2 className="text-2xl font-semibold">Events</h2>
            <div className="flex gap-2">
              {(chainEvents.length > 0 || interceptorEvents.length > 0) && (
                <button
                  onClick={clearAllEvents}
                  className="text-sm text-red-600 hover:text-red-800"
                >
                  Clear All
                </button>
              )}
            </div>
          </div>

          {/* Tabs */}
          <div className="flex border-b border-gray-200 mb-4">
            <button
              onClick={() => setActiveTab("chain")}
              className={`px-4 py-2 font-medium text-sm transition-colors ${
                activeTab === "chain"
                  ? "border-b-2 border-blue-600 text-blue-600"
                  : "text-gray-500 hover:text-gray-700"
              }`}
            >
              Chain Events ({chainEvents.length})
            </button>
            <button
              onClick={() => setActiveTab("interceptor")}
              className={`px-4 py-2 font-medium text-sm transition-colors ${
                activeTab === "interceptor"
                  ? "border-b-2 border-purple-600 text-purple-600"
                  : "text-gray-500 hover:text-gray-700"
              }`}
            >
              Interceptor Events ({interceptorEvents.length})
            </button>
          </div>

          {/* Tab Content */}
          {activeTab === "chain" ? (
            <div>
              <div className="flex justify-between items-center mb-2">
                <p className="text-sm text-gray-600">
                  Events from chain (no interceptor) - Forward trực tiếp lên chain
                </p>
                {chainEvents.length > 0 && (
                  <button
                    onClick={clearChainEvents}
                    className="text-xs text-red-600 hover:text-red-800"
                  >
                    Clear Chain Events
                  </button>
                )}
              </div>
              {chainEvents.length === 0 ? (
                <p className="text-gray-500 text-center py-8">
                  No chain events yet. Events from chain will appear here.
                </p>
              ) : (
                <div className="space-y-3 max-h-96 overflow-y-auto">
                  {chainEvents.map((event) => (
                    <EventCard key={event.id} event={event} source="chain" />
                  ))}
                </div>
              )}
            </div>
          ) : (
            <div>
              <div className="flex justify-between items-center mb-2">
                <p className="text-sm text-gray-600">
                  Events from interceptor - Chặn lại và trả về RPC
                </p>
                {interceptorEvents.length > 0 && (
                  <button
                    onClick={clearInterceptorEvents}
                    className="text-xs text-red-600 hover:text-red-800"
                  >
                    Clear Interceptor Events
                  </button>
                )}
              </div>
              {interceptorEvents.length === 0 ? (
                <p className="text-gray-500 text-center py-8">
                  No interceptor events yet. Events from interceptor will appear here.
                </p>
              ) : (
                <div className="space-y-3 max-h-96 overflow-y-auto">
                  {interceptorEvents.map((event) => (
                    <EventCard key={event.id} event={event} source="interceptor" />
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// EventCard component để hiển thị event
function EventCard({ event, source }) {
  const formatTimestamp = (timestamp) => {
    if (!timestamp) return "N/A";
    return new Date(Number(timestamp) * 1000).toLocaleString();
  };

  const formatBytes32 = (bytes32) => {
    if (!bytes32) return "N/A";
    const str = typeof bytes32 === "string" ? bytes32 : bytes32.toString();
    return str.length > 20 ? `${str.slice(0, 10)}...${str.slice(-8)}` : str;
  };

  const formatBytes = (bytes) => {
    if (!bytes) return "N/A";
    const str = typeof bytes === "string" ? bytes : bytes.toString();
    if (str.startsWith("0x")) {
      // Try to decode as UTF-8 if possible
      try {
        const hex = str.slice(2);
        // Convert hex string to Uint8Array, then to UTF-8 string
        const bytesArray = new Uint8Array(
          hex.match(/.{1,2}/g)?.map((byte) => parseInt(byte, 16)) || []
        );
        const decoder = new TextDecoder("utf-8", { fatal: false });
        const decoded = decoder.decode(bytesArray);
        // Check if it's valid UTF-8 (no null bytes in the middle and not empty)
        if (decoded && !decoded.includes("\x00") && decoded.length > 0) {
          return decoded;
        }
      } catch {
        // Not valid UTF-8, show hex
      }
    }
    return str.length > 50 ? `${str.slice(0, 25)}...${str.slice(-25)}` : str;
  };

  return (
    <div
      className={`border rounded-md p-4 hover:bg-gray-50 transition-colors ${
        event.eventName === "EmitError"
          ? "border-red-200 bg-red-50"
          : source === "chain"
          ? "border-blue-200 bg-blue-50"
          : "border-purple-200 bg-purple-50"
      }`}
    >
      <div className="flex items-start justify-between mb-2">
        <div className="flex items-center gap-2">
          <span
            className={`px-2 py-1 rounded text-xs font-semibold ${
              event.eventName === "EmitSentence"
                ? "bg-green-100 text-green-800"
                : event.eventName === "EmitError"
                ? "bg-red-100 text-red-800"
                : "bg-purple-100 text-purple-800"
            }`}
          >
            {event.eventName}
          </span>
          <span
            className={`px-2 py-1 rounded text-xs font-semibold ${
              source === "chain"
                ? "bg-blue-200 text-blue-800"
                : "bg-purple-200 text-purple-800"
            }`}
          >
            {source === "chain" ? "🔗 Chain" : "🛡️ Interceptor"}
          </span>
        </div>
        <span className="text-xs text-gray-500">
          {formatTimestamp(event.timestamp)}
        </span>
      </div>
      <div className="space-y-1 text-sm">
        {event.eventName === "EmitError" ? (
          <>
            <p>
              <span className="font-medium">TxHash:</span>{" "}
              <span className="font-mono text-xs">{event.txHash}</span>
            </p>
            <p>
              <span className="font-medium">Error Message:</span>{" "}
              <span className="text-red-700 break-all">{event.message || "N/A"}</span>
            </p>
          </>
        ) : (
          // Hiển thị cho EmitSentence event
          <>
            <p>
              <span className="font-medium">Session ID:</span>{" "}
              <span className="font-mono text-xs">{formatBytes32(event.sessionId)}</span>
            </p>
            <p>
              <span className="font-medium">Action ID:</span>{" "}
              <span className="font-mono text-xs">{formatBytes32(event.actionId)}</span>
            </p>
            {event.operator && (
              <p>
                <span className="font-medium">Operator:</span>{" "}
                <span className="font-mono text-xs">{event.operator}</span>
              </p>
            )}
            {event.data && (
              <p>
                <span className="font-medium">Data:</span>{" "}
                <span className="font-mono text-xs break-all">
                  {formatBytes(event.data)}
                </span>
              </p>
            )}
          </>
        )}
      </div>
    </div>
  );
}

export default App;
