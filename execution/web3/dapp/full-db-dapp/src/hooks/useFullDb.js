// src/hooks/useFulldb.js
import { useState, useEffect, useCallback } from 'react';
import {
    createPublicClient,
    http,
    ContractFunctionExecutionError, // Import specific error type if needed
    createWalletClient, // Needed if interacting with wallet inside hook (though passed as prop)
    custom,           // Needed if interacting with wallet inside hook
    getAddress        // Needed if interacting with wallet inside hook
} from 'viem';
import { localhost } from 'viem/chains'; // Hoặc chain khác bạn dùng
import contractABI_JSON from '../abis/PublicfullDB.json'; // Đảm bảo đường dẫn đúng

// --- CẤU HÌNH ---
// const CHAIN = sepolia; // Ví dụ: dùng Sepolia
const CHAIN = { ...localhost, id: 991 }; // Sử dụng localhost với ID chain của bạn
const contractABI = contractABI_JSON; // Lấy ABI từ file JSON đã import

// Tạo public client để đọc dữ liệu từ contract
const publicClient = createPublicClient({
    chain: CHAIN,
    transport: http(), // Sử dụng HTTP transport mặc định
});

/**
 * Custom Hook để tương tác với PublicfullDB contract.
 * @param {string} contractAddress Địa chỉ của PublicfullDB contract đã deploy.
 * @param {string | null} account Địa chỉ ví của người dùng đang kết nối.
 * @param {object | null} walletClient Instance của Wallet Client từ Viem (đã kết nối).
 * @returns {object} Các state và hàm để tương tác với contract.
 */
export function useFullDb(contractAddress, account, walletClient) {
    // --- State Variables ---
    const [currentDbName, setCurrentDbName] = useState(''); // Tên DB đang được chọn trên contract
    const [isLoading, setIsLoading] = useState(false);      // Trạng thái loading chung cho các hành động
    const [error, setError] = useState('');                 // Lưu trữ thông báo lỗi
    const [statusMessage, setStatusMessage] = useState(''); // Lưu trữ thông báo thành công/trạng thái
    const [lastTxHash, setLastTxHash] = useState('');       // Hash của giao dịch ghi cuối cùng
    const [totalResults, setTotalResults] = useState(0);    // <-- State lưu trữ tổng số kết quả tìm kiếm
    const [currentSearchResults, setCurrentSearchResults] = useState([]); // Kết quả tìm kiếm chi tiết của trang hiện tại
    const [newProductId, setNewProductId] = useState(null); // ID của sản phẩm mới thêm (optional)

    // --- Read Operations ---

    // Đọc tên DB hiện tại từ contract
    const readDbNameState = useCallback(async () => {
        if (!contractAddress || contractAddress === '0x...') return;
        console.log("Hook: Reading dbName...");
        try {
            const fetchedDbName = await publicClient.readContract({
                address: contractAddress,
                abi: contractABI,
                functionName: 'dbName',
            });
            setCurrentDbName(fetchedDbName || '');
            console.log("Hook: dbName read:", fetchedDbName);
        } catch (readError) {
            console.error("Hook: Error reading dbName:", readError);
            // Không set lỗi chung ở đây để tránh ghi đè lỗi khác
        }
    }, [contractAddress]); // Phụ thuộc vào contractAddress

    // --- Write Operations Helper ---

    // Hàm helper chung để thực thi các hàm ghi (write) của contract
    const executeWrite = useCallback(async (functionName, args, ignoreStatusRead = false) => {
        if (!walletClient || !account || !contractAddress || contractAddress === '0x...') {
            const errMsg = "Ví chưa sẵn sàng hoặc địa chỉ contract chưa cấu hình.";
            setError(errMsg);
            return { success: false, hash: null, error: errMsg };
        }
        // Reset trạng thái trước khi thực hiện
        setIsLoading(true);
        setError('');
        setStatusMessage('');
        setLastTxHash(''); // Xóa hash cũ

        try {
            console.log(`Hook: Calling ${functionName} with args:`, args);
            // Gửi yêu cầu ghi đến contract
            const hash = await walletClient.writeContract({
                address: contractAddress,
                abi: contractABI,
                functionName: functionName,
                args: args,
                account: account, // Account thực hiện giao dịch
                chain: CHAIN,     // Đảm bảo đúng chain
            });
            setLastTxHash(hash); // Lưu hash mới
            console.log(`Hook: Tx sent (${functionName}). Hash:`, hash);
            setStatusMessage(`Đang chờ xác nhận giao dịch ${functionName}...`);

            // Chờ xác nhận giao dịch
            const receipt = await publicClient.waitForTransactionReceipt({ hash });
            console.log(`Hook: Tx receipt (${functionName}):`, receipt);

            if (receipt.status === 'success') {
                setStatusMessage(`Giao dịch ${functionName} thành công.`);
                let contractStatus = null;
                // Đọc trạng thái 'status' từ contract sau khi thành công (nếu cần)
                if (!ignoreStatusRead) {
                    try {
                        contractStatus = await publicClient.readContract({
                            address: contractAddress,
                            abi: contractABI,
                            functionName: 'status', // Hàm đọc trạng thái chung
                        });
                        console.log(`Hook: Contract 'status' after ${functionName}:`, contractStatus);
                         // Cập nhật status message với trạng thái đọc được (tùy chọn)
                         // setStatusMessage(`Tx ${functionName} thành công (Contract Status: ${contractStatus}).`);
                    } catch (statusError) {
                        console.error(`Hook: Lỗi đọc 'status' sau ${functionName}:`, statusError);
                        // Có thể thêm thông báo phụ vào statusMessage
                        // setStatusMessage(prev => prev + " (Không đọc được status contract).");
                    }
                }
                return { success: true, hash: hash, status: contractStatus, error: null }; // Trả về thành công và trạng thái (nếu đọc)
            } else {
                // Giao dịch bị reverted
                throw new Error(`Giao dịch ${functionName} thất bại (reverted).`);
            }
        } catch (err) {
            console.error(`Hook: Lỗi khi gọi ${functionName}:`, err);
            // Xử lý và hiển thị lỗi thân thiện hơn
            let userMessage = `Lỗi thực thi ${functionName}: `;
            if (err instanceof ContractFunctionExecutionError) {
                userMessage += `Lỗi Contract: ${err.shortMessage || err.message}`;
            } else if (err.message?.includes('User rejected')) { // Bắt lỗi người dùng từ chối
                userMessage += 'Người dùng đã từ chối giao dịch.';
            } else if (err.message?.toLowerCase().includes('insufficient funds')) { // Bắt lỗi thiếu gas
                userMessage += 'Không đủ gas để thực hiện giao dịch.';
            } else {
                userMessage += err.message; // Lỗi khác
            }
            setError(userMessage);
            // Vẫn trả về hash nếu đã có (cho phép xem tx thất bại trên explorer)
            return { success: false, hash: lastTxHash || null, error: userMessage };
        } finally {
            setIsLoading(false); // Kết thúc loading
        }
    }, [account, walletClient, contractAddress]); // Phụ thuộc vào các yếu tố này

    // --- Specific Action Functions ---

    // Gọi hàm getOrCreateDb trên contract
    const doGetOrCreateDb = useCallback(async (dbNameToCreate) => {
        const result = await executeWrite('getOrCreateDb', [dbNameToCreate]);
        if (result.success) {
            await readDbNameState(); // Cập nhật lại tên DB sau khi thành công
            setStatusMessage(`Tạo/Mở DB '${dbNameToCreate}' thành công.`);
        }
        // Lỗi đã được set trong executeWrite nếu thất bại
    }, [executeWrite, readDbNameState]);

    // Gọi hàm createSampleProductDatabase trên contract
    const doCreateSampleDb = useCallback(async (dbNameToCreate) => {
        const result = await executeWrite('createSampleProductDatabase', [dbNameToCreate]);
        if (result.success) {
            await readDbNameState(); // Cập nhật lại tên DB
            setStatusMessage(`Tạo dữ liệu mẫu cho DB '${dbNameToCreate}' thành công.`);
        }
    }, [executeWrite, readDbNameState]);

    // Gọi hàm deleteDocument trên contract
    const doDeleteDocument = useCallback(async (docId) => {
        if (!currentDbName) {
            setError("Chưa chọn DB để xóa document.");
            return false; // Trả về false nếu không thể thực hiện
        }
        const result = await executeWrite('deleteDocument', [currentDbName, BigInt(docId)]);
        if (result.success) {
            setStatusMessage(`Xóa document ID ${docId} khỏi DB '${currentDbName}' thành công.`);
        }
        return result.success; // Trả về true/false dựa trên kết quả tx
    }, [executeWrite, currentDbName]);

    // Gọi hàm addProduct trên contract
    const doAddProduct = useCallback(async (productData) => {
        if (!currentDbName) {
            setError("Chưa chọn DB để thêm sản phẩm.");
            return { success: false, docId: null };
        }
        setNewProductId(null);
        console.log("Hook: Calling addProduct with data:", productData);

        // Gọi hàm addProduct, không cần đọc 'status' sau đó vì chúng ta sẽ đọc 'id'
        const result = await executeWrite('addProduct', [currentDbName, productData], true);

        if (result.success) {
            let addedId = null;
            try {
                // Đọc state 'id' từ contract để lấy ID của sản phẩm vừa thêm
                addedId = await publicClient.readContract({
                    address: contractAddress,
                    abi: contractABI,
                    functionName: 'id', // Hàm đọc ID của document mới nhất (theo logic contract)
                });
                setNewProductId(addedId);
                setStatusMessage(`Thêm sản phẩm "${productData.title}" thành công! ID: ${addedId}.`);
                console.log("Hook: Product added successfully, read new ID:", addedId);
            } catch (readIdError) {
                console.error("Hook: Lỗi đọc ID sản phẩm mới:", readIdError);
                setStatusMessage(`Thêm sản phẩm "${productData.title}" thành công (không đọc được ID mới).`);
            }
            return { success: true, docId: addedId };
        } else {
            // Lỗi đã được set bởi executeWrite
            return { success: false, docId: null };
        }
    }, [executeWrite, currentDbName, contractAddress]);

    // Gọi hàm querySearch và đọc kết quả
    const doSearch = useCallback(async (params, page = 1) => {
        if (!currentDbName) {
            setError("Chưa chọn DB để tìm kiếm.");
            setCurrentSearchResults([]); // Xóa kết quả cũ
            setTotalResults(0);         // Reset total
            return;
        }

        // Reset trạng thái trước khi tìm kiếm mới
        setCurrentSearchResults([]);
        setTotalResults(0);
        // Gọi hàm querySearch (write transaction), không cần đọc 'status' sau đó
        const searchTxResult = await executeWrite('querySearch', [currentDbName, params], true);

        if (searchTxResult.success) {
            console.log("Hook: Search tx successful, reading results details...");
            let fetchedPageResults = [];
            let readErrorMsg = null;
            let currentTotal = 0; // Biến tạm để lưu total đọc được

            try {
                // *** BƯỚC 1: Đọc tổng số kết quả (lastQueryResults) ***
                try {
                    const totalQueryResult = await publicClient.readContract({
                        address: contractAddress,
                        abi: contractABI,
                        functionName: 'lastQueryResults', // Đọc biến public lastQueryResults
                    });
                    // totalQueryResult sẽ là một tuple/array chứa [total, results[]] theo struct SearchResultsPage
                    // Chúng ta cần phần tử 'total' (thường là index 0 hoặc tên key nếu ABI định nghĩa tên)
                    // Kiểm tra cấu trúc trả về từ ABI của bạn. Giả sử nó trả về object { total: bigint, results: array }
                    if (totalQueryResult && typeof totalQueryResult.total !== 'undefined') {
                         currentTotal = Number(totalQueryResult.total); // Chuyển bigint sang number
                         setTotalResults(currentTotal); // Cập nhật state totalResults
                         console.log("Hook: Total results read:", currentTotal);
                    } else {
                        console.log("totalQueryResult", totalQueryResult)
                          // Nếu là một số bigint, chuyển đổi và sử dụng
                          if (typeof totalQueryResult === 'bigint') {
                            currentTotal = Number(totalQueryResult);
                            setTotalResults(currentTotal);
                            console.log("Hook: Total results read (is a bigint):", currentTotal);
                          } else {
                             console.warn("Hook: Không thể đọc 'total' từ lastQueryResults. Cấu trúc không đúng:", totalQueryResult);
                             setTotalResults(0); // Reset nếu không đọc được
                          }
                     
                    }

                } catch (totalReadError) {
                    console.error("Hook: Lỗi đọc lastQueryResults:", totalReadError);
                    readErrorMsg = `Lỗi đọc tổng số kết quả: ${totalReadError.message}`;
                    setTotalResults(0); // Reset total nếu đọc lỗi
                }

                // *** BƯỚC 2: Đọc kết quả chi tiết của trang hiện tại (searchResults) ***
                const limit = Number(params.limit); // Lấy limit từ params (đã là BigInt)
                console.log("Hook: Limit per page:", limit)
                console.log("Hook: Total results found:", currentTotal)


                // Chỉ đọc chi tiết nếu có tổng kết quả > 0 và limit > 0
                if (currentTotal > 0 && limit > 0) {
                    const readPromises = [];
                    // Số lượng cần đọc cho trang này: tối đa là limit, hoặc ít hơn nếu là trang cuối
                    // Vòng lặp chỉ nên chạy tối đa `limit` lần. Contract sẽ trả lỗi nếu index vượt quá số kết quả thực tế của trang.
                    for (let i = 0; i < limit; i++) {
                        readPromises.push(
                            publicClient.readContract({
                                address: contractAddress,
                                abi: contractABI,
                                functionName: 'searchResults', // Đọc mảng public searchResults
                                args: [BigInt(i)]              // Truyền index cần đọc
                            }).catch(err => {
                                // Lỗi có thể xảy ra nếu index không tồn tại (đã hết kết quả trên trang)
                                // Hoặc lỗi RPC khác. Trả về null để Promise.all không bị reject hoàn toàn.
                                console.warn(`Hook: Lỗi đọc searchResults[${i}]: ${err.message}. Giả định hết kết quả cho index này.`);
                                return null;
                            })
                        );
                    }

                    // Chờ tất cả các promise đọc hoàn thành
                    const resultsOrNulls = await Promise.all(readPromises);

                    // Lọc bỏ các kết quả null (do lỗi đọc hoặc index không tồn tại)
                    // và chỉ lấy những kết quả hợp lệ (phải là mảng có đủ phần tử)
                    fetchedPageResults = resultsOrNulls.filter(r => Array.isArray(r) && r.length >= 4); // Kiểm tra là mảng và đủ 4 phần tử

                    console.log("Hook: Fetched page results (raw):", fetchedPageResults);


                    // Format kết quả đọc được sang cấu trúc dễ dùng hơn
                    const formattedResults = fetchedPageResults.map(r_array => ({
                        // Truy cập bằng index [0], [1], [2], [3] vì contract trả về tuple/mảng
                        docid: r_array[0] !== undefined ? r_array[0].toString() : 'Lỗi ID', // Chuyển bigint sang string
                        rank: r_array[1] !== undefined ? Number(r_array[1]) : 0,          // Chuyển bigint sang number
                        percent: r_array[2] !== undefined ? Number(r_array[2]) : 0,       // Chuyển bigint sang number
                        data: r_array[3] !== undefined ? r_array[3] : '(Lỗi Data)'         // Dữ liệu gốc (string)
                    }));

                    setCurrentSearchResults(formattedResults); // Cập nhật state kết quả trang

                    // Cập nhật Status Message dựa trên kết quả đọc được
                    if (formattedResults.length > 0) {
                        setStatusMessage(`Tìm thấy tổng cộng ${currentTotal} sản phẩm. Hiển thị ${formattedResults.length} kết quả cho trang ${page}.`);
                    } else if (currentTotal > 0) {
                         // Có tổng kết quả nhưng trang này không có (ví dụ: trang không tồn tại)
                         setStatusMessage(`Tìm thấy tổng cộng ${currentTotal} sản phẩm, nhưng không có kết quả nào cho trang ${page}.`);
                         setCurrentSearchResults([]); // Đảm bảo mảng rỗng
                    }
                     // else: trường hợp currentTotal=0 đã được xử lý ở Status Message dưới

                } else {
                    // Không có kết quả nào hoặc limit = 0
                    setCurrentSearchResults([]); // Đảm bảo mảng rỗng
                }

                 // Nếu không đọc được kết quả nào VÀ total cũng là 0 -> Không tìm thấy gì
                 if(currentTotal === 0) {
                    setStatusMessage("Không tìm thấy sản phẩm nào khớp với tiêu chí tìm kiếm.");
                 }


            } catch (processError) {
                // Lỗi không mong muốn trong quá trình xử lý chung
                console.error("Hook: Lỗi xử lý kết quả sau search:", processError);
                readErrorMsg = `Lỗi xử lý kết quả: ${processError.message}`;
                setCurrentSearchResults([]); // Reset nếu lỗi
                setTotalResults(0);         // Reset total
            }

            // Set lỗi cuối cùng nếu có lỗi trong quá trình đọc kết quả
            if (readErrorMsg) setError(readErrorMsg);

        } else {
            // searchTxResult không thành công (lỗi đã được set trong executeWrite)
            setCurrentSearchResults([]); // Reset kết quả
            setTotalResults(0);         // Reset total
        }
        // isLoading đã được set false trong finally của executeWrite
    }, [executeWrite, currentDbName, contractAddress]); // Thêm contractAddress vào dependencies

    // --- useEffect Hook ---

    // Chạy một lần khi component mount hoặc khi account/contractAddress thay đổi
    // để đọc trạng thái ban đầu của dbName
    useEffect(() => {
        if (account && contractAddress && contractAddress !== '0x...') {
            readDbNameState();
        } else {
             setCurrentDbName(''); // Reset nếu không có account hoặc contract address
        }
    }, [account, contractAddress, readDbNameState]); // Phụ thuộc account, contractAddress

    // --- Return Values ---
    // Trả về các state và hàm để component có thể sử dụng
    return {
        // States
        currentDbName,
        isLoading,
        error,
        statusMessage,
        lastTxHash,
        totalResults,           // <-- Trả về totalResults
        currentSearchResults,
        newProductId,

        // Functions
        doGetOrCreateDb,
        doCreateSampleDb,
        doDeleteDocument,
        doAddProduct,
        doSearch,
        // Có thể thêm các hàm read khác nếu cần (ví dụ: getAllFields, getAllTags,...)
    };
}