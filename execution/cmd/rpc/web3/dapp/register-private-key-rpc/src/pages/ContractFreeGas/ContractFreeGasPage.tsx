import { useState, useEffect, useCallback } from "react";
import {
  encodeFunctionData,
  encodePacked,
  type Hex,
  isAddress,
  hexToString,
} from "viem";
import { useWallet } from "~/contexts/WalletContext";
import { contracts } from "~/constants/contracts";
interface ContractData {
  contract_address: string;
  added_at: number;
}

interface ContractListResponse {
  contracts: ContractData[];
  total: number;
  page: number;
  page_size: number;
  total_pages: number;
}

// Contract ABI
const CONTRACT_ADDRESS = "0x00000000000000000000000000000000D844bb55" as Hex;

const ContractFreeGasPage = () => {
  const { walletClient, publicClient } = useWallet();
  const [contractsList, setContractsList] = useState<ContractData[]>([]);
  const [currentPage, setCurrentPage] = useState(0);
  const [pageSize] = useState(10);
  const [totalPages, setTotalPages] = useState(0);
  const [totalItems, setTotalItems] = useState(0);
  const [loading, setLoading] = useState(false);
  const [newContractAddress, setNewContractAddress] = useState("");
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");

  // Load danh sách contracts
  const loadContracts = useCallback(async () => {
    if (!publicClient || !walletClient) {
      setError("Please connect your wallet");
      return;
    }

    setLoading(true);
    setError("");

    try {
      const account = walletClient.account;
      if (!account) {
        throw new Error("No account connected");
      }

      const timestamp = BigInt(Math.floor(Date.now() / 1000));

      // Create message: page (32 bytes) + pageSize (32 bytes) + timestamp (32 bytes)
      const message = encodePacked(
        ["uint256", "uint256", "uint256"],
        [BigInt(currentPage), BigInt(pageSize), timestamp]
      );

      // Sign message
      const signature = await walletClient.signMessage({
        account,
        message: { raw: message },
      });

      // Call getAllContractFreeGas
      const data = encodeFunctionData({
        abi: contracts.AccountManager.abi,
        functionName: "getAllContractFreeGas",
        args: [BigInt(currentPage), BigInt(pageSize), timestamp, signature],
      });

      const result = await publicClient.call({
        to: CONTRACT_ADDRESS,
        data: data,
      });

      if (!result.data) {
        throw new Error("No data returned");
      }

      // Parse result - Backend trả về JSON string
      // Decode hex result to JSON using hexToString from viem
      const jsonStr = hexToString(result.data);
      const response: ContractListResponse = JSON.parse(jsonStr);

      setContractsList(response.contracts);
      setTotalPages(response.total_pages);
      setTotalItems(response.total);
    } catch (err) {
      console.error("Error loading contracts:", err);
      setError(
        err instanceof Error ? err.message : "Failed to load contracts"
      );
    } finally {
      setLoading(false);
    }
  }, [currentPage, pageSize, publicClient, walletClient]);

  // Thêm contract mới
  const handleAddContract = async () => {
    if (!publicClient || !walletClient) {
      setError("Please connect your wallet");
      return;
    }

    if (!isAddress(newContractAddress)) {
      setError("Invalid contract address");
      return;
    }

    setLoading(true);
    setError("");
    setSuccess("");

    try {
      const account = walletClient.account;
      if (!account) {
        throw new Error("No account connected");
      }

      const timestamp = BigInt(Math.floor(Date.now() / 1000));

      // Create message: contractAddress (20 bytes) + timestamp (32 bytes)
      const message = encodePacked(
        ["address", "uint256"],
        [newContractAddress as Hex, timestamp]
      );

      // Sign message
      const signature = await walletClient.signMessage({
        account,
        message: { raw: message },
      });

      // Call addContractFreeGas
      const hash = await walletClient.writeContract({
        account,
        address: CONTRACT_ADDRESS,
        abi: contracts.AccountManager.abi,
        functionName: "addContractFreeGas",
        args: [newContractAddress as Hex, timestamp, signature],
        chain: walletClient.chain,
        gas: 500000n,
      });

      // Wait for transaction
      setSuccess(`Contract ${newContractAddress} added successfully! tx: ${hash}`);
      setNewContractAddress("");

      // Reload list
      await loadContracts();
    } catch (err) {
      console.error("Error adding contract:", err);
      setError(err instanceof Error ? err.message : "Failed to add contract");
    } finally {
      setLoading(false);
    }
  };

  // Xóa contract
  const handleRemoveContract = async (contractAddress: string) => {
    if (!publicClient || !walletClient) {
      setError("Please connect your wallet");
      return;
    }

    if (
      !confirm(`Are you sure you want to remove contract ${contractAddress}?`)
    ) {
      return;
    }

    setLoading(true);
    setError("");
    setSuccess("");

    try {
      const account = walletClient.account;
      if (!account) {
        throw new Error("No account connected");
      }

      const timestamp = BigInt(Math.floor(Date.now() / 1000));

      // Create message: contractAddress (20 bytes) + timestamp (32 bytes)
      const message = encodePacked(
        ["address", "uint256"],
        [contractAddress as Hex, timestamp]
      );

      // Sign message
      const signature = await walletClient.signMessage({
        account,
        message: { raw: message },
      });

      // Call removeContractFreeGas
      await walletClient.writeContract({
        account,
        address: CONTRACT_ADDRESS,
        abi: contracts.AccountManager.abi,
        functionName: "removeContractFreeGas",
        args: [contractAddress as Hex, timestamp, signature],
        chain: walletClient.chain,
        gas: 500000n,
      });

      // Wait for transaction
      // await publicClient.waitForTransactionReceipt({ hash });

      setSuccess(`Contract ${contractAddress} removed successfully!`);

      // Reload list
      await loadContracts();
    } catch (err) {
      console.error("Error removing contract:", err);
      setError(
        err instanceof Error ? err.message : "Failed to remove contract"
      );
    } finally {
      setLoading(false);
    }
  };

  // Load contracts khi component mount hoặc page thay đổi
  useEffect(() => {
    if (walletClient && publicClient) {
      loadContracts();
    }
  }, [currentPage, walletClient, publicClient, loadContracts]);

  return (
    <div className="max-w-6xl mx-auto p-4">
      <h1 className="text-3xl font-bold mb-6 text-app">Contract Free Gas Management</h1>

      {/* Error/Success Messages */}
      {error && (
        <div className="bg-red-100 border border-red-400 text-red-700 px-4 py-3 rounded mb-4">
          {error}
        </div>
      )}
      {success && (
        <div className="bg-green-100 border border-green-400 text-green-700 px-4 py-3 rounded mb-4">
          {success}
        </div>
      )}

      {/* Add Contract Form */}
      <div className="bg-card border border-border rounded-lg p-6 mb-6 shadow-sm">
        <h2 className="text-xl font-semibold mb-4 text-app">Add New Contract</h2>
        <div className="flex gap-4">
          <input
            type="text"
            placeholder="Contract Address (0x...)"
            value={newContractAddress}
            onChange={(e) => setNewContractAddress(e.target.value)}
            className="flex-1 px-4 py-2 bg-input border border-border rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500 text-app"
            disabled={loading}
          />
          <button
            onClick={handleAddContract}
            disabled={loading || !newContractAddress}
            className="px-6 py-2 bg-blue-600 text-white rounded-lg hover:bg-blue-700 disabled:bg-gray-400 disabled:cursor-not-allowed transition-colors"
          >
            {loading ? "Adding..." : "Add Contract"}
          </button>
        </div>
      </div>

      {/* Contracts Table */}
      <div className="bg-card border border-border rounded-lg shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-border">
          <h2 className="text-xl font-semibold text-app">
            Contracts List ({totalItems} total)
          </h2>
        </div>

        {loading ? (
          <div className="p-8 text-center text-muted">Loading...</div>
        ) : contractsList.length === 0 ? (
          <div className="p-8 text-center text-muted">No contracts found</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead className="bg-app/5 border-b border-border">
                <tr>
                  <th className="px-6 py-3 text-left text-xs font-medium text-muted uppercase tracking-wider">
                    #
                  </th>
                  <th className="px-6 py-3 text-left text-xs font-medium text-muted uppercase tracking-wider">
                    Contract Address
                  </th>
                  <th className="px-6 py-3 text-left text-xs font-medium text-muted uppercase tracking-wider">
                    Added At
                  </th>
                  <th className="px-6 py-3 text-right text-xs font-medium text-muted uppercase tracking-wider">
                    Actions
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {contractsList.map((contract, index) => (
                  <tr key={contract.contract_address} className="hover:bg-app/5 transition-colors">
                    <td className="px-6 py-4 whitespace-nowrap text-sm text-app">
                      {currentPage * pageSize + index + 1}
                    </td>
                    <td className="px-6 py-4 whitespace-nowrap">
                      <a
                        href={`https://etherscan.io/address/${contract.contract_address}`}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="text-blue-600 hover:text-blue-800 font-mono text-sm"
                      >
                        {contract.contract_address}
                      </a>
                    </td>
                    <td className="px-6 py-4 whitespace-nowrap text-sm text-muted">
                      {new Date(contract.added_at * 1000).toLocaleString()}
                    </td>
                    <td className="px-6 py-4 whitespace-nowrap text-right text-sm">
                      <button
                        onClick={() => handleRemoveContract(contract.contract_address)}
                        disabled={loading}
                        className="px-4 py-2 bg-red-600 text-white rounded-lg hover:bg-red-700 disabled:bg-gray-400 disabled:cursor-not-allowed transition-colors"
                      >
                        Remove
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {/* Pagination */}
        {totalPages > 1 && (
          <div className="px-6 py-4 border-t border-border flex items-center justify-between">
            <button
              onClick={() => setCurrentPage(Math.max(0, currentPage - 1))}
              disabled={currentPage === 0 || loading}
              className="px-4 py-2 bg-gray-200 text-gray-700 rounded-lg hover:bg-gray-300 disabled:bg-gray-100 disabled:cursor-not-allowed transition-colors"
            >
              Previous
            </button>

            <span className="text-sm text-muted">
              Page {currentPage + 1} of {totalPages}
            </span>

            <button
              onClick={() => setCurrentPage(Math.min(totalPages - 1, currentPage + 1))}
              disabled={currentPage >= totalPages - 1 || loading}
              className="px-4 py-2 bg-gray-200 text-gray-700 rounded-lg hover:bg-gray-300 disabled:bg-gray-100 disabled:cursor-not-allowed transition-colors"
            >
              Next
            </button>
          </div>
        )}
      </div>
    </div>
  );
};

export default ContractFreeGasPage;
