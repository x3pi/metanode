// src/components/SearchResults.jsx
import React from 'react';

// Component hiển thị 1 item sản phẩm
function ProductItem({ result, isLoading, onDelete }) {
    const handleDeleteClick = () => {
        if (window.confirm(`Bạn có chắc muốn xóa sản phẩm ID ${result.docid}?`)) {
             onDelete(result.docid);
        }
    };

    // --- Logic Parse JSON ---
    let product = null; // Biến để lưu trữ object sản phẩm sau khi parse
    let parseError = false;
    try {
        // Chỉ parse nếu result.data là một chuỗi JSON hợp lệ và không rỗng
        if (result.data && typeof result.data === 'string') {
            console.log( result.data )
            const correctedJsonString = result.data.replace(/\\"/g, '"');
            console.log( correctedJsonString)

            product = JSON.parse(correctedJsonString);
        } else if (result.data) {
           // Handle cases where data might exist but not be valid JSON string format we expect
           console.warn("Data for docId:", result.docid, "is not a valid JSON string:", result.data);
           parseError = true; // Mark as error if not expected format
        }
    } catch (e) {
        console.error("Lỗi parse JSON data cho docId:", result.docid, result.data, e);
        parseError = true; // Đánh dấu nếu có lỗi parse
    }
    // --- Kết thúc Logic Parse JSON ---

   return (
      <div className="product-item">
         <div className="product-info">
            {/* Hiển thị thông tin cơ bản không đổi */}
            <strong>ID:</strong> {result.docid} | {' '}
            <strong>Rank:</strong> {result.rank} | {' '}
            <strong>Score:</strong> {result.percent}% <br/>

            {/* --- Hiển thị dữ liệu đã parse --- */}
            {product ? ( // Nếu parse thành công và product có giá trị
                <>
                    <strong>Title:</strong> {product.title || '(N/A)'}<br/>
                    <strong>Category:</strong> {product.category || '(N/A)'} | {' '}
                    <strong>Brand:</strong> {product.brand || '(N/A)'}<br/>
                    <strong>Giá gốc:</strong> {product.price || '(N/A)'} | {' '}
                    <strong>Giá KM:</strong> {product.discountPrice || '(N/A)'}<br/>
                    {/* Hiển thị mảng colors nếu tồn tại và không rỗng */}
                    {product.colors && Array.isArray(product.colors) && product.colors.length > 0 && (
                        <><strong>Màu sắc:</strong> {product.colors.join(', ')}<br/></>
                    )}
                    {/* Hiển thị mảng filters nếu tồn tại và không rỗng */}
                    {product.filters && Array.isArray(product.filters) && product.filters.length > 0 && (
                        <><strong>Filters:</strong> {product.filters.join(', ')}<br/></>
                    )}
                    {/* Hiển thị description nếu tồn tại */}
                    {product.description && (
                         <><strong>Mô tả:</strong> {product.description}<br/></>
                    )}
                    {/* Optional: Hiển thị content nếu tồn tại */}
                    {/* {product.content && (
                         <><strong>Nội dung:</strong> {product.content}<br/></>
                    )} */}
                </>
            ) : ( // Nếu không parse được hoặc không có data hoặc data không đúng định dạng
                <>
                    <strong>Data:</strong>
                    <span className="product-data" style={{ color: parseError ? 'red' : 'inherit' }}>
                        {result.data || '(không có dữ liệu)'}
                        {parseError && ' (Lỗi định dạng dữ liệu)'}
                    </span>
                </>
            )}
            {/* --- Kết thúc hiển thị dữ liệu parse --- */}
         </div>
          {/* Nút xóa (không đổi) */}
          {onDelete && (
             <button
                onClick={handleDeleteClick}
                disabled={isLoading}
                className="btn btn-delete btn-sm"
                title={`Xóa sản phẩm ID ${result.docid}`}
             >
                Xóa
             </button>
          )}
      </div>
   );
}


// Component chính SearchResults (phần còn lại giữ nguyên)
function SearchResults({
    results,
    totalResults,
    isLoading,
    onDelete,
    onPageChange,
    currentPage,
    limit
}) {
    // ... (logic của SearchResults giữ nguyên) ...

  return (
    <div className="results-section section-box">
       {/* ... (Phần JSX của SearchResults giữ nguyên) ... */}
      <h3>Kết quả Tìm kiếm ({isLoading ? 'Đang tải...' : `${totalResults} sản phẩm`})</h3>

      {!isLoading && results && results.length > 0 && (
        <div className="product-list">
          {results.map(result => (
             <ProductItem // Component ProductItem sử dụng code đã cập nhật ở trên
                key={result.docid}
                result={result}
                isLoading={isLoading}
                onDelete={onDelete}
             />
          ))}
        </div>
      )}

      {/* ... (Các thông báo khác và Phân trang giữ nguyên) ... */}

    </div>
  );
}

export default SearchResults;