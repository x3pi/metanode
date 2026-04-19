// src/components/SearchForm.jsx
import React, { useState } from 'react';

/**
 * Component Form tìm kiếm sản phẩm.
 * @param {object} props - Props của component.
 * @param {string} props.currentDbName - Tên DB hiện tại đang được chọn.
 * @param {boolean} props.isLoading - Trạng thái loading chung của ứng dụng (để disable form).
 * @param {function} props.onSearch - Hàm callback được gọi khi form được submit, truyền về các tham số tìm kiếm.
 */
function SearchForm({ currentDbName, isLoading, onSearch }) {
    // State quản lý giá trị các input trong form này
    const [query, setQuery] = useState('');
    const [minPrice, setMinPrice] = useState('');
    const [maxPrice, setMaxPrice] = useState('');
    const [minDiscountPrice, setMinDiscountPrice] = useState(''); // <-- State cho giá KM min
    const [maxDiscountPrice, setMaxDiscountPrice] = useState(''); // <-- State cho giá KM max
    const [sortOption, setSortOption] = useState('relevance'); // Giá trị mặc định cho dropdown sắp xếp

    // Hàm xử lý khi người dùng submit form tìm kiếm
    const handleSubmit = (e) => {
        e.preventDefault(); // Ngăn chặn hành vi submit form mặc định của trình duyệt

        // Gọi hàm callback `onSearch` được truyền từ component cha,
        // và gửi lên một object chứa các giá trị tìm kiếm hiện tại từ state của form này.
        if (typeof onSearch === 'function') {
            onSearch({
                query: query,           // Từ khóa tìm kiếm
                minPrice: minPrice,     // Giá tối thiểu
                maxPrice: maxPrice,     // Giá tối đa
                minDiscountPrice: minDiscountPrice, // <-- Thêm giá trị lọc KM min
                maxDiscountPrice: maxDiscountPrice, // <-- Thê
                sortOption: sortOption  // Lựa chọn sắp xếp
            });
        } else {
            console.warn("SearchForm: Prop 'onSearch' is not a function!");
        }
    };

    // --- Render giao diện form ---
    return (
        <div className="search-section section-box">
            <h3>Tìm kiếm Sản phẩm (trong DB: "{currentDbName || 'Chưa chọn'}")</h3>

            {/* Sử dụng thẻ form và sự kiện onSubmit để xử lý tìm kiếm khi nhấn Enter hoặc click nút */}
            <form onSubmit={handleSubmit} className="search-controls">

                {/* Input cho từ khóa tìm kiếm */}
                <div className="control-group">
                    <label htmlFor="searchQuery">Từ khóa:</label>
                    <input
                        type="text"
                        id="searchQuery"
                        value={query}
                        onChange={(e) => setQuery(e.target.value)} // Cập nhật state 'query' khi input thay đổi
                        placeholder="Nhập từ khóa (vd: iphone, T:ao thun, B:apple C:electronics)"
                        disabled={isLoading || !currentDbName} // Disable khi đang loading hoặc chưa chọn DB
                        style={{ flexGrow: 2 }} // Cho input này rộng hơn một chút
                    />
                </div>

                {/* Nhóm input cho bộ lọc giá và sắp xếp */}
                <div className="control-group filter-group">
                    {/* Lọc Giá Gốc */}
                    <label>Lọc giá gốc:</label>
                    <input
                        type="number"
                        value={minPrice}
                        onChange={(e) => setMinPrice(e.target.value)}
                        placeholder="Từ giá"
                        disabled={isLoading || !currentDbName}
                        min="0"
                        step="any" // Cho phép số thập phân
                    />
                    <span>-</span>
                    <input
                        type="number"
                        value={maxPrice}
                        onChange={(e) => setMaxPrice(e.target.value)}
                        placeholder="Đến giá"
                        disabled={isLoading || !currentDbName}
                        min="0"
                        step="any"
                    />

                    {/* Lọc Giá Khuyến Mãi */}
                    <label style={{ marginLeft: '20px' }}>Lọc giá KM:</label>
                    <input
                        type="number"
                        value={minDiscountPrice}
                        onChange={(e) => setMinDiscountPrice(e.target.value)} // <-- Cập nhật state giá KM min
                        placeholder="Từ giá"
                        disabled={isLoading || !currentDbName}
                        min="0"
                        step="any"
                    />
                    <span>-</span>
                    <input
                        type="number"
                        value={maxDiscountPrice}
                        onChange={(e) => setMaxDiscountPrice(e.target.value)} // <-- Cập nhật state giá KM max
                        placeholder="Đến giá"
                        disabled={isLoading || !currentDbName}
                        min="0"
                        step="any"
                    />

                    {/* Dropdown chọn cách sắp xếp */}
                    <label htmlFor="sortBy" style={{ marginLeft: '20px' }}>Sắp xếp:</label>
                    <select
                        id="sortBy"
                        value={sortOption}
                        onChange={(e) => setSortOption(e.target.value)}
                        disabled={isLoading || !currentDbName}
                    >
                        <option value="relevance">Liên quan nhất</option>
                        <option value="price_asc">Giá gốc tăng dần</option>
                        <option value="price_desc">Giá gốc giảm dần</option>
                        <option value="discount_price_asc">Giá KM tăng dần</option>
                        <option value="discount_price_desc">Giá KM giảm dần</option>
                    </select>
                </div>

                {/* Nút submit form */}
                <button type="submit" disabled={isLoading || !currentDbName} className="btn btn-search">
                    Tìm kiếm
                </button>
            </form>
        </div>
    );
}

export default SearchForm;