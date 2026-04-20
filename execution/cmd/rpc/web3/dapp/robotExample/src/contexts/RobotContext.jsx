import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  useRef,
} from "react";
import {
  createWalletClient,
  createPublicClient,
  http,
  encodeFunctionData,
  decodeEventLog,
  keccak256,
  toHex,
  stringToHex,
  hexToString,
  concat,
  pad,
} from "viem";
import { privateKeyToAccount } from "viem/accounts";
import { signMessage } from "viem/actions";
import { contracts } from "~/constants/contracts";
import { chain991, WSS_RPC, WSS_RPC_INTERCEPTOR, GO_BACKEND_RPC_URL } from "~/constants/customeChain";

// Event types (using JSDoc for documentation)
/**
 * @typedef {Object} RobotEvent
 * @property {string} id
 * @property {"SessionCreated" | "SentenceEmitted" | "AIRequest"} eventName
 * @property {bigint} sessionId
 * @property {bigint} timestamp
 * @property {Object} data
 * @property {string} [data.robot]
 * @property {bigint} [data.sentenceIndex]
 * @property {string} [data.sentence]
 * @property {string} [data.requestData]
 */

const RobotContext = createContext(undefined);

export function RobotProvider({ children }) {
  const [account, setAccount] = useState(null);
  const [chainEvents, setChainEvents] = useState([]); // Events từ chain
  const [interceptorEvents, setInterceptorEvents] = useState([]); // Events từ interceptor
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState(null);

  // Wallet client refs
  const walletClientRef = useRef(null);
  const publicClientRef = useRef(null);

  // WebSocket refs - 2 connections riêng biệt
  const wsChainRef = useRef(null); // WebSocket cho chain (không interceptor)
  const wsInterceptorRef = useRef(null); // WebSocket cho interceptor
  const reconnectChainTimeoutRef = useRef(null);
  const reconnectInterceptorTimeoutRef = useRef(null);
  const keepaliveChainIntervalRef = useRef(null); // Keepalive interval cho chain WebSocket
  const keepaliveInterceptorIntervalRef = useRef(null); // Keepalive interval cho interceptor WebSocket

  // Connect wallet from private key
  const connectWallet = useCallback((privateKey) => {
    try {
      // Remove 0x prefix if present
      const cleanKey = privateKey.startsWith("0x") ? privateKey.slice(2) : privateKey;
      const privateKeyHex = `0x${cleanKey}`;

      // Create account from private key
      const account = privateKeyToAccount(privateKeyHex);

      // Create wallet client
      const walletClient = createWalletClient({
        account,
        chain: chain991,
        transport: http(GO_BACKEND_RPC_URL),
      });

      // Create public client
      const publicClient = createPublicClient({
        chain: chain991,
        transport: http(GO_BACKEND_RPC_URL),
      });

      walletClientRef.current = walletClient;
      publicClientRef.current = publicClient;
      setAccount(account.address);
      setError(null);
    } catch (err) {
      console.error("Error connecting wallet:", err);
      setError(err instanceof Error ? err.message : "Failed to connect wallet");
    }
  }, []);

  // Disconnect wallet
  const disconnectWallet = useCallback(() => {
    walletClientRef.current = null;
    publicClientRef.current = null;
    setAccount(null);
    if (wsChainRef.current) {
      wsChainRef.current.close();
      wsChainRef.current = null;
    }
    if (wsInterceptorRef.current) {
      wsInterceptorRef.current.close();
      wsInterceptorRef.current = null;
    }
    if (reconnectChainTimeoutRef.current) {
      clearTimeout(reconnectChainTimeoutRef.current);
      reconnectChainTimeoutRef.current = null;
    }
    if (reconnectInterceptorTimeoutRef.current) {
      clearTimeout(reconnectInterceptorTimeoutRef.current);
      reconnectInterceptorTimeoutRef.current = null;
    }
    if (keepaliveChainIntervalRef.current) {
      clearInterval(keepaliveChainIntervalRef.current);
      keepaliveChainIntervalRef.current = null;
    }
    if (keepaliveInterceptorIntervalRef.current) {
      clearInterval(keepaliveInterceptorIntervalRef.current);
      keepaliveInterceptorIntervalRef.current = null;
    }
  }, []);

  // Dispatch function - calls dispatch(bytes32 sessionId, bytes32 actionId, bytes calldata data, uint256 timestamp, bytes calldata sig)
  const dispatch = useCallback(
    async (sessionId, actionId, data) => {
      // Validate inputs
      if (!walletClientRef.current || !publicClientRef.current) {
        throw new Error("Wallet not connected");
      }

      if (sessionId === null || sessionId === undefined) {
        console.error("❌ [dispatch] Session ID is null/undefined");
        throw new Error("Session ID is required");
      }
      if (actionId === null || actionId === undefined) {
        console.error("❌ [dispatch] Action ID is null/undefined");
        throw new Error("Action ID is required");
      }
      if (data === null || data === undefined) {
        console.error("❌ [dispatch] Data is null/undefined");
        throw new Error("Data is required");
      }

      setIsLoading(true);
      setError(null);
      
      try {
        // Convert sessionId to bytes32 (32 bytes = 64 hex chars + 0x = 66 chars total)
        let sessionIdBytes32;
        if (typeof sessionId === "string") {
          if (sessionId.startsWith("0x")) {
            // Already hex, ensure it's exactly 32 bytes (64 hex chars)
            const hexPart = sessionId.slice(2);
            sessionIdBytes32 = `0x${hexPart.padStart(64, "0").slice(0, 64)}`;
          } else {
            // Convert string to hex, then pad/truncate to 32 bytes
            const hexStr = stringToHex(sessionId).slice(2);
            sessionIdBytes32 = `0x${hexStr.padStart(64, "0").slice(0, 64)}`;
          }
        } else {
          // Number, convert to hex and pad to 32 bytes
          sessionIdBytes32 = `0x${sessionId.toString(16).padStart(64, "0").slice(0, 64)}`;
        }

        // Convert actionId to bytes32
        let actionIdBytes32;
        if (typeof actionId === "string") {
          if (actionId.startsWith("0x")) {
            const hexPart = actionId.slice(2);
            actionIdBytes32 = `0x${hexPart.padStart(64, "0").slice(0, 64)}`;
          } else {
            const hexStr = stringToHex(actionId).slice(2);
            actionIdBytes32 = `0x${hexStr.padStart(64, "0").slice(0, 64)}`;
          }
        } else {
          actionIdBytes32 = `0x${actionId.toString(16).padStart(64, "0").slice(0, 64)}`;
        }
        
        // Convert data to bytes (can be any length)
        let dataBytes;
        if (typeof data === "string") {
          if (data.startsWith("0x")) {
            dataBytes = data;
          } else {
            // Convert text to hex
            dataBytes = stringToHex(data);
          }
        } else {
          dataBytes = `0x${data.toString(16)}`;
        }

        // Get current timestamp (Unix timestamp in seconds)
        const timestamp = BigInt(Math.floor(Date.now() / 1000));
        const timestampHex = pad(toHex(timestamp), { size: 32 }); // Pad to 32 bytes

        // Build message: sessionId (32 bytes) + actionId (32 bytes) + data (variable) + timestamp (32 bytes)
        const messageBytes = concat([
          sessionIdBytes32,
          actionIdBytes32,
          dataBytes,
          timestampHex,
        ]);

        // Sign message using wallet client
        // viem signMessage with raw bytes will automatically add Ethereum message prefix
        let signature;
        try {
          signature = await signMessage(walletClientRef.current, {
            message: { raw: messageBytes },
          });
          console.log(`✅ [dispatch] Message signed successfully, signature=${signature}`);
        } catch (signErr) {
          console.error(`❌ [dispatch] signMessage error:`, signErr);
          throw signErr;
        }

        // Encode function call with signature
        const encodedData = encodeFunctionData({
          abi: contracts.RobotManager.abi,
          functionName: "dispatch",
          args: [sessionIdBytes32, actionIdBytes32, dataBytes, timestamp, signature],
        });

        let hash;
        try {
          hash = await walletClientRef.current.sendTransaction({
            to: contracts.RobotManager.address,
            data: encodedData,
          });
          console.log(`✅ [dispatch] Transaction sent successfully: hash=${hash}`);
        } catch (sendErr) {
          console.error(`❌ [dispatch] sendTransaction error:`, sendErr);
          throw sendErr;
        }

        return hash;
      } catch (err) {
        const errorMsg =
          err instanceof Error ? err.message : "Failed to dispatch";
        console.error(`❌ [dispatch] Error:`, err);
        setError(errorMsg);
        throw new Error(errorMsg);
      } finally {
        setIsLoading(false);
      }
    },
    []
  );

  // Get data by txHash
  const getDataByTxhash = useCallback(
    async (txHash) => {
      if (  !publicClientRef.current) {
        throw new Error("Wallet not connected");
      }

      setIsLoading(true);
      setError(null);

      try {
        // Convert txHash to bytes (32 bytes for bytes32)
        let txHashBytes;
        if (typeof txHash === "string") {
          // Remove 0x prefix if present
          const cleanHash = txHash.startsWith("0x") ? txHash.slice(2) : txHash;
          // Convert hex string to bytes32
          txHashBytes = `0x${cleanHash.padStart(64, "0")}`;
        } else {
          throw new Error("txHash must be a string");
        }
        console.log("txHashBytes", txHashBytes);
        // Encode function call
        const encodedData = encodeFunctionData({
          abi: contracts.RobotManager.abi,
          functionName: "getDataByTxhash",
          args: [txHashBytes],
        });

        // Call contract (view function)
        const result = await publicClientRef.current.call({
          to: contracts.RobotManager.address,
          data: encodedData,
        });

        if (!result.data || result.data === "0x") {
          throw new Error("No data returned from contract");
        }

        // Backend trả về JSON hex string (eth_call.go marshal object thành JSON hex)
        // Decode hex → JSON string → parse JSON
        const jsonStr = hexToString(result.data);
        const data = JSON.parse(jsonStr);
        
        // Parse inputData JSON string nếu có
        if (data.inputData) {
          try {
            data.inputData = JSON.parse(data.inputData);
          } catch (e) {
            console.warn("⚠️ [getDataByTxhash] Failed to parse inputData JSON:", e);
            // Giữ nguyên inputData nếu parse lỗi
          }
        }
        
        console.log("✅ [getDataByTxhash] Data retrieved:", data);
        return data;
      } catch (err) {
        const errorMsg =
          err instanceof Error ? err.message : "Failed to get data by txHash";
        console.error(`❌ [getDataByTxhash] Error:`, err);
        setError(errorMsg);
        throw new Error(errorMsg);
      } finally {
        setIsLoading(false);
      }
    },
    [] // publicClientRef.current không cần trong dependency array vì refs không trigger re-render
  );

  // Clear events
  const clearChainEvents = useCallback(() => {
    setChainEvents([]);
  }, []);

  const clearInterceptorEvents = useCallback(() => {
    setInterceptorEvents([]);
  }, []);

  const clearAllEvents = useCallback(() => {
    setChainEvents([]);
    setInterceptorEvents([]);
  }, []);

  // Helper function to send keepalive (eth_chainId) request
  const sendKeepalive = (ws, source) => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      const keepaliveId = Date.now(); // Unique ID for each request
      ws.send(
        JSON.stringify({
          jsonrpc: "2.0",
          id: keepaliveId,
          method: "eth_chainId",
          params: [],
        })
      );
      console.log(`💓 [keepalive] Sent eth_chainId to ${source} WebSocket`);
    }
  };

  // Helper function to subscribe to events
  const subscribeToEvents = (ws) => {
    // Calculate event topic hash for EmitSentence
    const sigEmitSentence = "EmitSentence(bytes32,bytes32,address,bytes)";
    const topicEmitSentence = keccak256(toHex(sigEmitSentence));

    // Calculate event topic hash for EmitError
    const sigEmitError = "EmitError(bytes32,string)";
    const topicEmitError = keccak256(toHex(sigEmitError));

    // Subscribe to EmitSentence events from UniversalRobotBus
    ws.send(
      JSON.stringify({
        jsonrpc: "2.0",
        id: 1,
        method: "eth_subscribe",
        params: [
          "logs",
          {
            address: contracts.RobotManager.address,
            topics: [[topicEmitSentence]],
          },
        ],
      })
    );

    // Subscribe to EmitError events
    ws.send(
      JSON.stringify({
        jsonrpc: "2.0",
        id: 2,
        method: "eth_subscribe",
        params: [
          "logs",
          {
            address: contracts.RobotManager.address,
            topics: [[topicEmitError]],
          },
        ],
      })
    );
  };

  // Helper function to handle WebSocket messages
  const handleWebSocketMessage = (event, source) => {
    try {
      const data = JSON.parse(event.data);

      if (data.method === "eth_subscription" && data.params?.result) {
        const log = data.params.result;

        if (log.topics && log.data) {
          try {
            // Decode event log
            const decoded = decodeEventLog({
              abi: contracts.RobotManager.abi,
              data: log.data,
              topics: log.topics,
            });

            const args = decoded.args || {};
            console.log(decoded);
            
            // Create event object for EmitSentence
            if (decoded.eventName === "EmitSentence") {
              const robotEvent = {
                id: `${log.transactionHash}-${log.logIndex}-${source}`,
                eventName: decoded.eventName,
                txHash: log.transactionHash || "0x0", // Transaction hash từ log
                sessionId: args.sessionId || "0x0",
                actionId: args.actionId || "0x0",
                operator: args.operator || "",
                data: args.data || "0x",
                source: source, // "chain" or "interceptor"
                timestamp: BigInt(Math.floor(Date.now() / 1000))
                // Use current timestamp
              };

              // Add to appropriate events list
              if (source === "chain") {
                setChainEvents((prev) => [robotEvent, ...prev]);
              } else {
                setInterceptorEvents((prev) => [robotEvent, ...prev]);
              }
              console.log(`🔔 Robot event received from ${source}:`, robotEvent);
            }
            
            // Create event object for EmitError
            if (decoded.eventName === "EmitError") {
              const robotEvent = {
                id: `${log.transactionHash}-${log.logIndex}-${source}`,
                eventName: decoded.eventName,
                txHash: args.txHash || "0x0",
                message: args.message || "",
                source: source, // "chain" or "interceptor"
                timestamp: BigInt(Math.floor(Date.now() / 1000))
                // Use current timestamp
              };

              // Add to appropriate events list
              if (source === "chain") {
                setChainEvents((prev) => [robotEvent, ...prev]);
              } else {
                setInterceptorEvents((prev) => [robotEvent, ...prev]);
              }
              console.log(`🔔 Error event received from ${source}:`, robotEvent);
            }
          } catch (decodeErr) {
            console.error(`❌ Error decoding event from ${source}:`, decodeErr);
          }
        }
      }
    } catch (err) {
      console.error(`❌ Error handling WebSocket message from ${source}:`, err);
    }
  };

  // WebSocket subscription for CHAIN events (không interceptor)
  useEffect(() => {
    if (!account) {
      // Cleanup when disconnected
      if (wsChainRef.current) {
        wsChainRef.current.close();
        wsChainRef.current = null;
      }
      if (reconnectChainTimeoutRef.current) {
        clearTimeout(reconnectChainTimeoutRef.current);
        reconnectChainTimeoutRef.current = null;
      }
      return;
    }

    const connectChainWebSocket = () => {
      // Close existing connection
      if (wsChainRef.current) {
        wsChainRef.current.close();
        wsChainRef.current = null;
      }

      try {
        const ws = new WebSocket(WSS_RPC);
        wsChainRef.current = ws;

        ws.onopen = () => {
          console.log("✅ Chain WebSocket connected (no interceptor)");
          subscribeToEvents(ws, "chain");
          
          // Setup keepalive: gửi eth_chainId mỗi 20 giây
          if (keepaliveChainIntervalRef.current) {
            clearInterval(keepaliveChainIntervalRef.current);
          }
          keepaliveChainIntervalRef.current = setInterval(() => {
            sendKeepalive(ws, "chain");
          }, 20000); // 20 seconds
        };

        ws.onmessage = (event) => {
          handleWebSocketMessage(event, "chain");
        };

        ws.onerror = (error) => {
          console.error("❌ Chain WebSocket error:", error);
        };

        ws.onclose = (event) => {
          console.log("❌ Chain WebSocket disconnected");
          wsChainRef.current = null;

          // Clear keepalive interval
          if (keepaliveChainIntervalRef.current) {
            clearInterval(keepaliveChainIntervalRef.current);
            keepaliveChainIntervalRef.current = null;
          }

          // Auto-reconnect after 500ms
          if (event.code !== 1000) {
            reconnectChainTimeoutRef.current = setTimeout(() => {
              connectChainWebSocket();
            }, 500);
          }
        };
      } catch (err) {
        console.error("❌ Error setting up Chain WebSocket:", err);
        setError(
          err instanceof Error ? err.message : "Failed to setup Chain WebSocket"
        );
      }
    };

    connectChainWebSocket();

    // Cleanup on unmount
    return () => {
      if (wsChainRef.current) {
        wsChainRef.current.close();
        wsChainRef.current = null;
      }
      if (reconnectChainTimeoutRef.current) {
        clearTimeout(reconnectChainTimeoutRef.current);
        reconnectChainTimeoutRef.current = null;
      }
      if (keepaliveChainIntervalRef.current) {
        clearInterval(keepaliveChainIntervalRef.current);
        keepaliveChainIntervalRef.current = null;
      }
    };
  }, [account]);

  // WebSocket subscription for INTERCEPTOR events (có interceptor)
  useEffect(() => {
    if (!account) {
      // Cleanup when disconnected
      if (wsInterceptorRef.current) {
        wsInterceptorRef.current.close();
        wsInterceptorRef.current = null;
      }
      if (reconnectInterceptorTimeoutRef.current) {
        clearTimeout(reconnectInterceptorTimeoutRef.current);
        reconnectInterceptorTimeoutRef.current = null;
      }
      return;
    }

    const connectInterceptorWebSocket = () => {
      // Close existing connection
      if (wsInterceptorRef.current) {
        wsInterceptorRef.current.close();
        wsInterceptorRef.current = null;
      }

      try {
        const ws = new WebSocket(WSS_RPC_INTERCEPTOR);
        wsInterceptorRef.current = ws;

        ws.onopen = () => {
          console.log("✅ Interceptor WebSocket connected");
          subscribeToEvents(ws);
          
          // Setup keepalive: gửi eth_chainId mỗi 20 giây
          if (keepaliveInterceptorIntervalRef.current) {
            clearInterval(keepaliveInterceptorIntervalRef.current);
          }
          keepaliveInterceptorIntervalRef.current = setInterval(() => {
            sendKeepalive(ws, "interceptor");
          }, 20000); // 20 seconds
        };

        ws.onmessage = (event) => {
          handleWebSocketMessage(event, "interceptor");
        };

        ws.onerror = (error) => {
          console.error("❌ Interceptor WebSocket error:", error);
        };

        ws.onclose = (event) => {
          console.log("❌ Interceptor WebSocket disconnected");
          wsInterceptorRef.current = null;

          // Clear keepalive interval
          if (keepaliveInterceptorIntervalRef.current) {
            clearInterval(keepaliveInterceptorIntervalRef.current);
            keepaliveInterceptorIntervalRef.current = null;
          }

          // Auto-reconnect after 500ms
          if (event.code !== 1000) {
            reconnectInterceptorTimeoutRef.current = setTimeout(() => {
              connectInterceptorWebSocket();
            }, 500);
          }
        };
      } catch (err) {
        console.error("❌ Error setting up Interceptor WebSocket:", err);
        setError(
          err instanceof Error ? err.message : "Failed to setup Interceptor WebSocket"
        );
      }
    };

    connectInterceptorWebSocket();

    // Cleanup on unmount
    return () => {
      if (wsInterceptorRef.current) {
        wsInterceptorRef.current.close();
        wsInterceptorRef.current = null;
      }
      if (reconnectInterceptorTimeoutRef.current) {
        clearTimeout(reconnectInterceptorTimeoutRef.current);
        reconnectInterceptorTimeoutRef.current = null;
      }
      if (keepaliveInterceptorIntervalRef.current) {
        clearInterval(keepaliveInterceptorIntervalRef.current);
        keepaliveInterceptorIntervalRef.current = null;
      }
    };
  }, [account]);

  return (
    <RobotContext.Provider
      value={{
        account,
        isConnected: !!account,
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
      }}
    >
      {children}
    </RobotContext.Provider>
  );
}

export function useRobot() {
  const context = useContext(RobotContext);
  if (!context) {
    throw new Error("useRobot must be used within RobotProvider");
  }
  return context;
}

