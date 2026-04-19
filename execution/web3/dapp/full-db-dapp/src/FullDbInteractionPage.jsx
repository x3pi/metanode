// src/FullDbInteractionPage.jsx
import React, { useState, useCallback } from 'react';
import { useFullDb } from './hooks/useFullDb'; // Ensure correct path
import DbManager from './components/DbManager';
import AddProductForm from './components/AddProductForm'; // Import Add Product Form
import SearchForm from './components/SearchForm';
import SearchResults from './components/SearchResults';
import ActionStatus from './components/ActionStatus';
import { localhost } from 'viem/chains'; // Or your specific chain
// Import the publicClient if reading totalResults directly in this component
// (Not needed if useFullDb hook handles reading totalResults)
// import { createPublicClient, http } from 'viem';

import './FullDbInteractionPage.css'; // Ensure correct path

// --- CONFIGURATION ---
const contractAddress = '0x68b42A05efB96522d1ce46dBE6b0788C38f3f360'; // <-- *** REPLACE WITH YOUR DEPLOYED CONTRACT ADDRESS ***
const CHAIN = { ...localhost, id: 991 }; // Make sure this matches your useFullDb hook and contract deployment network

// Optional: Create publicClient here if reading totalResults directly
// const publicClient = createPublicClient({ chain: CHAIN, transport: http() });
// const contractABI = [...] // You might need the ABI here too if reading directly

function FullDbInteractionPage({ account, walletClient, onDisconnect }) {

  // --- Hook Initialization ---
  const {
    // States from hook:
    currentDbName,
    isLoading,
    error,
    statusMessage,
    lastTxHash,
    totalResults, // <-- Get totalResults directly from the updated hook
    currentSearchResults,
    newProductId,

    // Functions from hook:
    doGetOrCreateDb,
    doCreateSampleDb,
    doDeleteDocument,
    doAddProduct,
    doSearch,
  } = useFullDb(contractAddress, account, walletClient);

  // --- Component State ---
  const [currentPage, setCurrentPage] = useState(1);
  const [limit] = useState(10); // Products per page
  const [lastSearchParams, setLastSearchParams] = useState({}); // Store last search criteria for pagination

  // --- Helper Function to build contract search params ---
  // (Keep this consistent with useFullDb if it's also defined there, or centralize it)
// --- Helper Function to build contract search params ---
const buildContractParams = (searchUIParams, offset, limit) => {
    const contractParams = {
      queries: searchUIParams.query || "",
      prefixMap: [ // Default prefix map (adjust if your fields/prefixes differ)
        { key: "title", value: "T" }, { key: "T", value: "T" },
        { key: "category", value: "C" }, { key: "C", value: "C" },
        { key: "brand", value: "B" }, { key: "B", value: "B" },
        { key: "color", value: "CO:" }, { key: "CO", value: "CO:" },
        { key: "filter", value: "F:" }, { key: "F", value: "F:" }
      ],
      stopWords: ["the", "a", "an", "of", "in", "is", "on"], // Example stop words
      offset: BigInt(offset),
      limit: BigInt(limit),
      sortByValueSlot: 0, // Default: Relevance (or adjust as needed)
      sortAscending: true, // Default: Descending (assuming reversed logic)
      rangeFilters: [] // Initialize empty array for filters
    };
  
    // --- Sorting Logic (Assuming reversed boolean logic) ---
    if (searchUIParams.sortOption === 'price_asc') { // Ascending Price -> Send false
        contractParams.sortByValueSlot = 0;
        contractParams.sortAscending = false;
    } else if (searchUIParams.sortOption === 'price_desc') { // Descending Price -> Send true
        contractParams.sortByValueSlot = 0;
        contractParams.sortAscending = true;
    } else if (searchUIParams.sortOption === 'discount_price_asc') { // Ascending Discount -> Send false
        contractParams.sortByValueSlot = 1;
        contractParams.sortAscending = false;
    } else if (searchUIParams.sortOption === 'discount_price_desc') { // Descending Discount -> Send true
        contractParams.sortByValueSlot = 1;
        contractParams.sortAscending = true;
    } else { // Default relevance
        contractParams.sortByValueSlot = -1;
        contractParams.sortAscending = true; // Assuming true means descending score
    }
  
  
    // --- Range Filter Logic ---
  
    // Filter for Original Price (Slot 0)
    const priceRangeFilter = { slot: 0, startSerialised: "", endSerialised: "" };
    let hasPriceRangeFilter = false;
    if (searchUIParams.minPrice) {
        priceRangeFilter.startSerialised = searchUIParams.minPrice.toString();
        hasPriceRangeFilter = true;
     }
    if (searchUIParams.maxPrice) {
        priceRangeFilter.endSerialised = searchUIParams.maxPrice.toString();
        hasPriceRangeFilter = true;
     }
    if (hasPriceRangeFilter) {
        contractParams.rangeFilters.push(priceRangeFilter); // Add original price filter
    }
  
    // Filter for Discount Price (Slot 1)
    const discountPriceRangeFilter = { slot: 1, startSerialised: "", endSerialised: "" }; // Slot 1
    let hasDiscountPriceRangeFilter = false;
    if (searchUIParams.minDiscountPrice) { // Use new UI param
        discountPriceRangeFilter.startSerialised = searchUIParams.minDiscountPrice.toString();
        hasDiscountPriceRangeFilter = true;
     }
    if (searchUIParams.maxDiscountPrice) { // Use new UI param
        discountPriceRangeFilter.endSerialised = searchUIParams.maxDiscountPrice.toString();
        hasDiscountPriceRangeFilter = true;
     }
    if (hasDiscountPriceRangeFilter) {
        contractParams.rangeFilters.push(discountPriceRangeFilter); // Add discount price filter
    }
  
    console.log("Built Contract Params:", contractParams);
    return contractParams;
  };

  // --- Event Handlers ---

  // Handle search form submission
  const handleSearchSubmit = useCallback(async (searchParamsFromForm) => {
    setCurrentPage(1); // Reset to page 1 for new search
    const offset = 0;
    const contractParams = buildContractParams(searchParamsFromForm, offset, limit);
    setLastSearchParams(searchParamsFromForm); // Save criteria for pagination
    // Call the search function from the hook
    // The hook now handles reading totalResults internally
    await doSearch(contractParams, 1);
    // No need to read totalResults here anymore if hook provides it
  }, [doSearch, limit]); // Dependency: doSearch function from hook

  // Handle page change requests from pagination component
  const handlePageChange = useCallback(async (newPage) => {
    if (newPage === currentPage) return; // Avoid re-fetching same page

    const offset = (newPage - 1) * limit;
    // Use the *last* search parameters to build params for the new page
    const contractParams = buildContractParams(lastSearchParams, offset, limit);
    setCurrentPage(newPage);
    // Call search function for the new page
    await doSearch(contractParams, newPage);
  }, [doSearch, limit, lastSearchParams, currentPage]); // Dependencies


  // --- Render Logic ---
  return (
    <div className="full-db-page">
      {/* Section 1: Wallet Info & DB Management */}
      <div className="top-section section-box">
        <div className="wallet-display">
          <h3>Thông tin Ví</h3>
          {account ? (
            <>
              <p>Đã kết nối: <span className="address">{`${account.substring(0, 6)}...${account.substring(account.length - 4)}`}</span></p>
              <button onClick={onDisconnect} className="btn btn-disconnect btn-sm">Ngắt kết nối</button>
            </>
          ) : (
            <p>Ví chưa được kết nối.</p> /* Should ideally not happen if this component is rendered */
          )}
        </div>
        <DbManager
          currentDbName={currentDbName}
          isLoading={isLoading}
          onGetOrCreateDb={doGetOrCreateDb}    // Pass function from hook
          onCreateSampleDb={doCreateSampleDb} // Pass function from hook
        />
      </div>

      {/* Warning if contract address is not set */}
      {(!contractAddress || contractAddress === '0x...' || contractAddress === '') &&
        <p className="warning-message">
          Vui lòng cập nhật địa chỉ Smart Contract trong file `src/FullDbInteractionPage.jsx`.
        </p>
      }

      {/* Render main interaction sections only if contract address is valid */}
      {contractAddress && contractAddress !== '0x...' && contractAddress !== '' && (
        <>
          {/* Section 2: Add Product Form */}
          <AddProductForm
            currentDbName={currentDbName}
            isLoading={isLoading}
            onAddProduct={doAddProduct} // Pass function from hook
          />

          {/* Section 3: Action Status Display */}
          {/* Shows loading, errors, success messages, and last tx hash */}
          <ActionStatus
            isLoading={isLoading}
            error={error}
            statusMessage={statusMessage}
            txHash={lastTxHash}
            chain={CHAIN} // Pass chain info for Etherscan link
          />

          {/* Section 4: Search Form */}
          <SearchForm
            currentDbName={currentDbName}
            isLoading={isLoading}
            onSearch={handleSearchSubmit} // Pass handler function
          />

          {/* Section 5: Search Results & Pagination */}
          <SearchResults
            results={currentSearchResults} // Pass detailed results array
            totalResults={totalResults}     // Pass total results count from hook
            // Determine loading state for results specifically (e.g., not loading if only adding product)
            isLoading={isLoading && !statusMessage && !error && !newProductId}
            onDelete={doDeleteDocument}   // Pass delete function from hook
            onPageChange={handlePageChange} // Pass pagination handler
            currentPage={currentPage}
            limit={limit}
          />
        </>
      )}
    </div>
  );
}

export default FullDbInteractionPage;