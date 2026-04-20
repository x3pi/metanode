import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  useRef,
  type ReactNode,
} from "react";
import { useWallet } from "./WalletContext";
import { contracts } from "~/constants/contracts";
import {
  decodeEventLog,
  encodeFunctionData,
  hexToString,
  keccak256,
  toHex,
  type Hex,
} from "viem";
import type { Notification, NotificationResponse } from "~/types/notification";
import { WSS_RPC } from "~/constants/customChain";

interface NotificationContextType {
  notifications: Notification[];
  unreadCount: number;
  isLoading: boolean;
  error: string | null;
  loadNotifications: (page?: number, pageSize?: number) => Promise<void>;
  markAsRead: (notificationId: string) => void;
  markAllAsRead: () => void;
  clearNotifications: () => void;
}

const NotificationContext = createContext<NotificationContextType | undefined>(
  undefined
);

export function NotificationProvider({ children }: { children: ReactNode }) {
  const { publicClient, connectedAccount } = useWallet();
  const [notifications, setNotifications] = useState<Notification[]>([]);
  const [unreadCount, setUnreadCount] = useState(0);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // WebSocket refs
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectTimeoutRef = useRef<NodeJS.Timeout | null>(null);
  const reconnectAttemptsRef = useRef(0);

  // Load notifications from contract
  const loadNotifications = useCallback(
    async (page = 0, pageSize = 10) => {
      if (!publicClient || !connectedAccount) return;

      setIsLoading(true);
      setError(null);

      try {
        const data = encodeFunctionData({
          abi: contracts.AccountManager.abi,
          functionName: "getNotifications",
          args: [connectedAccount as Hex, BigInt(page), BigInt(pageSize)],
        });

        const result = await publicClient.call({
          to: contracts.AccountManager.address,
          data: data,
        });

        if (result.data) {
          const jsonStr = hexToString(result.data);
          const response: NotificationResponse = JSON.parse(jsonStr);
          const convertedNotifications: Notification[] =
            response.notifications.map((notif) => {
              return {
                id: notif.id,
                createdAt: notif.createdAt,
                message: notif.message,
              };
            });
          setNotifications(convertedNotifications);
        }
      } catch (err) {
        console.error("Error loading notifications:", err);
        setError(
          err instanceof Error ? err.message : "Failed to load notifications"
        );
      } finally {
        setIsLoading(false);
      }
    },
    [publicClient, connectedAccount]
  );

  // Listen to AccountConfirmed events via WebSocket
  useEffect(() => {
    const cleanup = () => {
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current);
        reconnectTimeoutRef.current = null;
      }
      const ws = wsRef.current;
      if (ws) {
        // ✅ Remove all event listeners trước
        ws.onopen = null;
        ws.onmessage = null;
        ws.onerror = null;
        ws.onclose = null;
        if (
          ws.readyState === WebSocket.OPEN ||
          ws.readyState === WebSocket.CONNECTING
        ) {
          console.log(
            "⚠️ Forcing close old WebSocket (readyState:",
            ws.readyState,
            ")"
          );
          ws.close(1000, "Account changed");
        }
        wsRef.current = null;
      }
    };
    // ✅ Cleanup trước khi tạo WebSocket mới
    cleanup();
    if (!connectedAccount) {
      // Cleanup when disconnected
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.close();
      }
      wsRef.current = null;
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current);
      }
      return;
    }

    const connectWebSocket = () => {
      const oldWs = wsRef.current;
      if (oldWs && oldWs.readyState === WebSocket.OPEN) {
        console.log("🧹 Closing old WebSocket before creating new one");
        oldWs.close(1000, "Creating new connection");
        wsRef.current = null;
      }
      try {
        const ws = new WebSocket(WSS_RPC);
        wsRef.current = ws;
        ws.onopen = () => {
          console.log("✅ WebSocket connected");
          reconnectAttemptsRef.current = 0;
          // Calculate event topic hash
          const sigAccountConfirmed =
            "AccountConfirmed(address,uint256,string)";
          const sigRegisterBls = "RegisterBls(address,uint256,bytes,string)";
          const sigTransferFrom = "TransferFrom(address,address,uint256,uint256,string)";

          const topicRegisterBls = keccak256(toHex(sigRegisterBls));
          const topicAccountConfirmed = keccak256(toHex(sigAccountConfirmed));
          const topicTransferFrom = keccak256(toHex(sigTransferFrom));

          ws.send(
            JSON.stringify({
              jsonrpc: "2.0",
              id: 1,
              method: "eth_subscribe",
              params: [
                "logs",
                {
                  address: contracts.AccountManager.address,
                  topics: [[topicAccountConfirmed]],
                },
              ],
            })
          );

          ws.send(
            JSON.stringify({
              jsonrpc: "2.0",
              id: 2,
              method: "eth_subscribe",
              params: [
                "logs",
                {
                  address: contracts.AccountManager.address,
                  topics: [[topicRegisterBls]],
                },
              ],
            })
          );

          ws.send(
            JSON.stringify({
              jsonrpc: "2.0",
              id: 3,
              method: "eth_subscribe",
              params: [
                "logs",
                {
                  address: contracts.AccountManager.address,
                  topics: [[topicTransferFrom]],
                },
              ],
            })
          );
        };

        ws.onmessage = (event) => {
          try {
            const data = JSON.parse(event.data);

            if (data.method === "eth_subscription" && data.params?.result) {
              const log = data.params.result;

              if (log.topics && log.data) {
                // Decode event log
                const decoded = decodeEventLog({
                  abi: contracts.AccountManager.abi,
                  data: log.data as `0x${string}`,
                  topics: log.topics as [Hex, ...Hex[]],
                });
                const args = decoded.args as {
                  account?: string;
                  from?: string; // For TransferFrom event
                  to?: string; // For TransferFrom event
                  amount?: bigint; // For TransferFrom event
                  time?: bigint;
                  message?: string;
                  publicKey?: string;
                  id?: string;
                };
                let notifMessage = "";
                let notifTitle = "";
                // console.log("🔍 Decoded event:", decoded.eventName, args);
                
                // For TransferFrom event, check if connected account is sender or receiver
                let shouldShowNotification = false;
                if (decoded.eventName === "TransferFrom") {
                  const fromAddr = args.from?.toLowerCase();
                  const toAddr = args.to?.toLowerCase();
                  const connectedAddr = connectedAccount.toLowerCase();
                  
                  // Show notification if user is either sender or receiver
                  shouldShowNotification = (fromAddr === connectedAddr || toAddr === connectedAddr);
                } else {
                  // For other events, check account field
                  const eventAddress = args.account?.toLowerCase();
                  shouldShowNotification = (eventAddress === connectedAccount.toLowerCase());
                }
                
                console.log(
                  "event:",
                  decoded.eventName,
                  "should show:",
                  shouldShowNotification
                );
                
                // Only show notification if it's relevant to the connected account
                if (!shouldShowNotification) {
                  return;
                }
                
                // Check event name
                if (decoded.eventName === "AccountConfirmed") {
                  console.log("name account confirmed");
                  notifTitle = "Account Confirmed";
                  notifMessage = args.message || "Account confirmed";
                } else if (decoded.eventName === "RegisterBls") {
                  console.log("BLS  confirmed");

                  notifTitle = "BLS Registered";
                  const shortKey = args.publicKey
                    ? `${args.publicKey.slice(0, 10)}...`
                    : "";
                  notifMessage = `${args.message} (Key: ${shortKey})`;
                } else if (decoded.eventName === "TransferFrom") {
                  console.log("Transfer event received");
                  notifTitle = "Transfer";
                  // Message already contains the right text from backend
                  notifMessage = args.message || "Transfer completed";
                } else {
                  return;
                }

                // Create notification
                const newNotification: Notification = {
                  id: args.id || `${connectedAccount}-${Date.now()}`,
                  createdAt: Number(args.time || Date.now()),
                  message: args.message || notifMessage,
                };

                setNotifications((prev) => [newNotification, ...prev]);
                setUnreadCount((prev) => prev + 1);
                // Show browser notification
                if (
                  "Notification" in window &&
                  Notification.permission === "granted"
                ) {
                  new Notification(notifTitle, {
                    body: notifMessage,
                    icon: "/favicon.ico",
                  });
                }
              }
            }
          } catch (err) {
            console.error("❌ Error handling WebSocket message:", err);
          }
        };

        ws.onerror = (error) => {
          console.error("❌ WebSocket error:", error);
        };
        ws.onclose = (event) => {
          console.log("❌ WebSocket disconnected");
          wsRef.current = null;
          // ✅ Auto-reconnect sau 100ms
          if (event.code !== 1000) {
            reconnectTimeoutRef.current = setTimeout(() => {
              reconnectAttemptsRef.current++;
              connectWebSocket();
            }, 500);
          } else {
            setError("WebSocket connection failed after multiple attempts");
          }
        };
      } catch (err) {
        console.error("❌ Error setting up WebSocket:", err);
        setError(
          err instanceof Error ? err.message : "Failed to setup WebSocket"
        );
      }
    };

    connectWebSocket();

    // ✅ Cleanup on unmount: Unsubscribe trước khi close
    return cleanup;
  }, [connectedAccount]);

  // Load notifications on mount and when account changes
  useEffect(() => {
    if (connectedAccount) {
      loadNotifications();
    }
  }, [connectedAccount, loadNotifications]);

  // Request browser notification permission
  useEffect(() => {
    if ("Notification" in window && Notification.permission === "default") {
      Notification.requestPermission();
    }
  }, []);

  const markAsRead = useCallback((notificationId: string) => {
    setNotifications((prev) =>
      prev.map((notif) => (notif.id === notificationId ? { ...notif } : notif))
    );
    setUnreadCount((prev) => Math.max(0, prev - 1));
  }, []);

  const markAllAsRead = useCallback(() => {
    setNotifications((prev) => [...prev]);
    setUnreadCount(0);
  }, []);

  const clearNotifications = useCallback(() => {
    setNotifications([]);
    setUnreadCount(0);
  }, []);

  return (
    <NotificationContext.Provider
      value={{
        notifications,
        unreadCount,
        isLoading,
        error,
        loadNotifications,
        markAsRead,
        markAllAsRead,
        clearNotifications,
      }}
    >
      {children}
    </NotificationContext.Provider>
  );
}

export function useNotifications() {
  const context = useContext(NotificationContext);
  if (!context) {
    throw new Error(
      "useNotifications must be used within NotificationProvider"
    );
  }
  return context;
}
