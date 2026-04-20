import { useState, useEffect, useCallback } from "react";
import { PageContainer } from "~/components/PageContainer";
import { PageCard } from "~/components/PageCard";
import { AppButton } from "~/components/ui/app-button";
import { Badge } from "~/components/ui/badge";
import { Alert } from "~/components/ui/alert";
import LoadingSpinnerIcon from "~/components/LoadingSpinnerIcon";
import Pagination from "~/components/pagination/Pagination";
import { TransactionModal } from "./TransactionModal";
import type { Transaction, TransactionResponse } from "~/types/transaction";
import { GO_BACKEND_RPC_URL } from "~/constants/customChain";

interface TransactionListPageProps {
  address: string;
  onClose?: () => void;
}

export function TransactionListPage({
  address,
  onClose,
}: TransactionListPageProps) {
  const [transactions, setTransactions] = useState<Transaction[]>([]);
  const [currentPage, setCurrentPage] = useState(1);
  const [pageSize] = useState(10);
  const [total, setTotal] = useState(0);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string>("");
  const [selectedTx, setSelectedTx] = useState<Transaction | null>(null);
  const [isModalOpen, setIsModalOpen] = useState(false);

  const handleViewDetails = (tx: Transaction) => {
    setSelectedTx(tx);
    setIsModalOpen(true);
  };

  const loadTransactions = useCallback(async () => {
    setIsLoading(true);
    setError("");
    try {
      // Call JSON-RPC via fetch (page starts from 0 in API)
      const response = await fetch(GO_BACKEND_RPC_URL, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          jsonrpc: "2.0",
          method: "mtn_searchTransactions",
          params: [`from:${address}`, currentPage - 1, pageSize], // Convert to 0-indexed
          id: 1,
        }),
      });

      const result = await response.json();
      
      if (result.error) {
        throw new Error(result.error.message);
      }
      const data = result.result as TransactionResponse;
      console.log(data);
      setTransactions(data.transactions || []);
      setTotal(data.total || 0);
    } catch (err) {
      console.error("Error loading transactions:", err);
      setError(
        err instanceof Error ? err.message : "Failed to load transactions"
      );
    } finally {
      setIsLoading(false);
    }
  }, [address, currentPage, pageSize]);

  useEffect(() => {
    loadTransactions();
  }, [loadTransactions]);

  const formatDate = (timestamp: number) => {
    return new Date(timestamp * 1000).toLocaleString();
  };

  const formatAddress = (addr: string) => {
    return `${addr.slice(0, 6)}...${addr.slice(-4)}`;
  };

  const formatValue = (value: string) => {
    if (!value) return "0";
    const decimal = BigInt(value);
    const eth = Number(decimal) / 1e18;
    return eth.toFixed(4);
  };

  const getStatusColor = (
    status: string
  ): "default" | "secondary" | "destructive" | "outline" => {
    switch (status?.toUpperCase()) {
      case "SUCCESS":
      case "RETURNED":
        return "default";
      case "FAILED":
        return "destructive";
      default:
        return "secondary";
    }
  };

  const totalPages = Math.ceil(total / pageSize);

  return (
    <PageContainer maxWidth="full" className="mt-20">
      <PageCard
        title="Transaction History"
        description={`Transactions from ${formatAddress(address)}`}
      >
        {/* Header with Close Button */}
        <div className="flex justify-between items-center mb-6">
          <div className="flex gap-2">
            <AppButton 
              onClick={loadTransactions} 
              disabled={isLoading} 
              appVariant="outline"
              size="sm"
            >
              {isLoading ? "Loading..." : "Refresh"}
            </AppButton>
          </div>
          {onClose && (
            <AppButton 
              onClick={onClose} 
              appVariant="outline"
              size="sm"
            >
              Close
            </AppButton>
          )}
        </div>

        {/* Error Alert */}
        {error && (
          <Alert variant="destructive" className="mb-4">
            {error}
          </Alert>
        )}

        {/* Loading State */}
        {isLoading && (
          <div className="flex justify-center items-center py-12">
            <LoadingSpinnerIcon />
            <span className="ml-2">Loading transactions...</span>
          </div>
        )}

        {/* Transactions List */}
        {!isLoading && transactions.length > 0 && (
          <div className="space-y-3">
            {transactions.map((tx) => (
              <div
                key={tx.hash}
                className="border border-gray-300 text-primary border-border rounded-lg p-4 bg-card hover:bg-gray-50 hover:bg-card-hover transition-colors cursor-pointer shadow-sm"
                onClick={() => handleViewDetails(tx)}
              >
                <div className="flex items-start justify-between">
                  <div className="flex-1 space-y-2">
                    <div className="flex items-center gap-2">
                      <span className="font-mono text-sm font-semibold  text-primary text-foreground">
                        {tx.hash.slice(0, 20)}...
                      </span>
                      <Badge variant={getStatusColor(tx.status)}>
                        {tx.status}
                      </Badge>
                    </div>

                    <div className="text-xs text-gray-600 text-app-muted space-y-1 ">
                      <div>
                        <span className="font-semibold text-primary text-foreground-secondary">To:</span>{" "}
                        <span className="text-blue-600 text-primary">{formatAddress(tx.to)}</span>
                      </div>
                      <div>
                        <span className="font-semibold text-primary text-foreground-secondary">Value:</span>{" "}
                        <span className=" text-primary text-foreground">{formatValue(tx.value)} ETH</span>
                      </div>
                      <div>
                        <span className="font-semibold text-primary text-foreground-secondary">Block:</span>{" "}
                        <span className=" text-primary text-foreground">{tx.blockNumber}</span>
                      </div>
                      <div>
                        <span className="font-semibold text-primary text-foreground-secondary">Time:</span>{" "}
                        <span className=" text-primary text-foreground">{formatDate(tx.timestamp)}</span>
                      </div>
                    </div>
                  </div>

                  <AppButton
                    onClick={(e) => {
                      e.stopPropagation();
                      handleViewDetails(tx);
                    }}
                    size="sm"
                    appVariant="primary"
                    className="ml-4"
                  >
                    View Details
                  </AppButton>
                </div>
              </div>
            ))}
          </div>
        )}

        {/* Empty State */}
        {!isLoading && transactions.length === 0 && (
          <div className="text-center py-12 text-gray-500 text-primary text-app-muted">
            <p>No transactions found.</p>
          </div>
        )}

        {/* Pagination */}
        {totalPages > 1 && (
          <Pagination
            currentPage={currentPage}
            totalPageCount={totalPages}
            onPageChange={setCurrentPage}
            siblingCount={1}
          />
        )}
      </PageCard>

      {/* Transaction Details Modal */}
      <TransactionModal
        open={isModalOpen}
        onOpenChange={setIsModalOpen}
        transaction={selectedTx}
      />
    </PageContainer>
  );
}

export default TransactionListPage;
