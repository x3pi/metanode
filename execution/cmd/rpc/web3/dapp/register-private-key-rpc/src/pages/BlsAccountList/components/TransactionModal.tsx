import { Dialog, DialogContent, DialogHeader, DialogTitle } from "~/components/ui/dialog";
import { Badge } from "~/components/ui/badge";
import { Label } from "~/components/ui/label";
import type { Transaction } from "~/types/transaction";

interface TransactionModalProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  transaction: Transaction | null;
}
const formatHash = (hash: string) => {
  if (!hash) return "";
  return `${hash.slice(0, 10)}...${hash.slice(-8)}`;
};

const getStatusBadgeVariant = (
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

const formatValue = (value: string) => {
  if (!value) return "0";
  // Convert hex to decimal and format
  const decimal = BigInt(value);
  const eth = Number(decimal) / 1e18;
  return eth.toFixed(4);
};

export function TransactionModal({
  open,
  onOpenChange,
  transaction,
}: TransactionModalProps) {
  if (!transaction) return null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl max-h-[90vh] overflow-y-auto bg-white dark:bg-background border-gray-300 dark:border-border">
        <DialogHeader>
          <DialogTitle className="text-gray-900 dark:text-foreground">Transaction Details</DialogTitle>
        </DialogHeader>

        <div className="space-y-4">
          {/* Hash */}
          <div className="space-y-2">
            <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Transaction Hash</Label>
            <div className="bg-gray-100 dark:bg-card p-3 rounded-md font-mono text-xs break-all text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
              {transaction.hash}
            </div>
          </div>

          {/* Block Info */}
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Block Number</Label>
              <div className="bg-gray-100 dark:bg-card p-2 rounded text-sm text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
                {transaction.blockNumber}
              </div>
            </div>
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Block Hash</Label>
              <div className="bg-gray-100 dark:bg-card p-2 rounded text-sm font-mono text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
                {formatHash(transaction.blockHash)}
              </div>
            </div>
          </div>

          {/* From & To */}
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">From</Label>
              <div className="bg-gray-100 dark:bg-card p-2 rounded text-sm font-mono text-gray-900 dark:text-foreground border border-gray-200 dark:border-border break-all">
                {transaction.from}
              </div>
            </div>
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">To</Label>
              <div className="bg-gray-100 dark:bg-card p-2 rounded text-sm font-mono text-gray-900 dark:text-foreground border border-gray-200 dark:border-border break-all">
                {transaction.to}
              </div>
            </div>
          </div>

          {/* Value & Fee */}
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Value (ETH)</Label>
              <div className="bg-gray-100 dark:bg-card p-2 rounded text-sm text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
                {formatValue(transaction.value)}
              </div>
            </div>
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Gas Fee</Label>
              <div className="bg-gray-100 dark:bg-card p-2 rounded text-sm text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
                {transaction.gasFee || "N/A"}
              </div>
            </div>
          </div>

          {/* Gas Info */}
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Gas Limit</Label>
              <div className="bg-gray-100 dark:bg-card p-2 rounded text-sm text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
                {transaction.gas}
              </div>
            </div>
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Gas Used</Label>
              <div className="bg-gray-100 dark:bg-card p-2 rounded text-sm text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
                {transaction.gasUsed}
              </div>
            </div>
          </div>

          {/* Nonce & Chain */}
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Nonce</Label>
              <div className="bg-gray-100 dark:bg-card p-2 rounded text-sm text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
                {transaction.nonce}
              </div>
            </div>
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Chain ID</Label>
              <div className="bg-gray-100 dark:bg-card p-2 rounded text-sm text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
                {transaction.chainId}
              </div>
            </div>
          </div>

          {/* Status & Exception */}
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Status</Label>
              <Badge variant={getStatusBadgeVariant(transaction.status)}>
                {transaction.status}
              </Badge>
            </div>
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Exception</Label>
              <div className="bg-gray-100 dark:bg-card p-2 rounded text-sm text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
                {transaction.exception || "NONE"}
              </div>
            </div>
          </div>

          {/* Timestamp */}
          <div className="space-y-2">
            <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Timestamp</Label>
            <div className="bg-gray-100 dark:bg-card p-2 rounded text-sm text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
              {new Date(Number(transaction.timestamp) * 1000).toLocaleString()}
            </div>
          </div>

          {/* Data */}
          {transaction.data && transaction.data !== "0x" && (
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">Input Data</Label>
              <div className="bg-gray-100 dark:bg-card p-3 rounded-md font-mono text-xs break-all max-h-32 overflow-y-auto text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
                {transaction.data}
              </div>
            </div>
          )}

          {/* Logs */}
          {transaction.logs && transaction.logs.length > 0 && (
            <div className="space-y-2">
              <Label className="text-sm font-semibold text-gray-800 dark:text-foreground-secondary">
                Logs ({transaction.logs.length})
              </Label>
              <div className="bg-gray-100 dark:bg-card p-3 rounded-md font-mono text-xs max-h-40 overflow-y-auto text-gray-900 dark:text-foreground border border-gray-200 dark:border-border">
                <pre>{JSON.stringify(transaction.logs, null, 2)}</pre>
              </div>
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
