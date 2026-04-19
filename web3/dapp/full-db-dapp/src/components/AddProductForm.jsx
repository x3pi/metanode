// src/components/AddProductForm.jsx
import React, { useState } from 'react';

/**
 * Component Form to add a new product.
 * @param {object} props - Props.
 * @param {string} props.currentDbName - The database to add the product to.
 * @param {boolean} props.isLoading - Global loading state.
 * @param {function} props.onAddProduct - Callback function to execute adding product logic.
 */
function AddProductForm({ currentDbName, isLoading, onAddProduct }) {
    const [title, setTitle] = useState('');
    const [category, setCategory] = useState('');
    const [brand, setBrand] = useState('');
    const [price, setPrice] = useState('');
    const [discountPrice, setDiscountPrice] = useState('');
    const [description, setDescription] = useState('');
    const [content, setContent] = useState('');
    const [colors, setColors] = useState(''); // Comma-separated string for simplicity
    const [filters, setFilters] = useState(''); // Comma-separated string

    const handleSubmit = (e) => {
        e.preventDefault();
        if (!currentDbName) {
            alert("Vui lòng chọn hoặc tạo Database trước khi thêm sản phẩm.");
            return;
        }
        if (!title || !category || !brand || !price) {
            alert("Vui lòng điền các trường bắt buộc: Title, Category, Brand, Price.");
            return;
        }

        // Basic validation for price format (optional but recommended)
         if (isNaN(parseFloat(price)) || (discountPrice && isNaN(parseFloat(discountPrice)))) {
            alert("Giá và Giá KM phải là số hợp lệ (vd: 99.99).");
            return;
         }


        const productData = {
            title,
            category,
            brand,
            price: price, // Keep as string for contract
            discountPrice: discountPrice || "0", // Default to "0" if empty, contract handles values
            description,
            content,
            // Split comma-separated strings into arrays, trim whitespace
            colors: colors.split(',').map(s => s.trim()).filter(s => s),
            filters: filters.split(',').map(s => s.trim()).filter(s => s),
        };

        if (typeof onAddProduct === 'function') {
            onAddProduct(productData); // Pass the structured data
            // Optionally clear form after submission attempt
            // setTitle(''); setCategory(''); setBrand(''); setPrice(''); /* ...etc... */
        } else {
             console.warn("AddProductForm: onAddProduct prop is not a function!");
        }
    };

    return (
        <div className="add-product-section section-box">
            <h3>Thêm Sản phẩm Mới (vào DB: "{currentDbName || 'Chưa chọn'}")</h3>
            <form onSubmit={handleSubmit}>
                {/* Title, Category, Brand */}
                <div className="control-group">
                    <label htmlFor="addTitle">Title*:</label>
                    <input id="addTitle" type="text" value={title} onChange={e => setTitle(e.target.value)} placeholder="Tên sản phẩm" disabled={isLoading || !currentDbName} required />
                    <label htmlFor="addCategory" style={{ marginLeft: '15px' }}>Category*:</label>
                    <input id="addCategory" type="text" value={category} onChange={e => setCategory(e.target.value)} placeholder="VD: electronics, fashion" disabled={isLoading || !currentDbName} required />
                     <label htmlFor="addBrand" style={{ marginLeft: '15px' }}>Brand*:</label>
                     <input id="addBrand" type="text" value={brand} onChange={e => setBrand(e.target.value)} placeholder="VD: apple, coolmate" disabled={isLoading || !currentDbName} required/>
                </div>

                {/* Price, Discount Price */}
                <div className="control-group">
                    <label htmlFor="addPrice">Giá gốc*:</label>
                    <input id="addPrice" type="text" value={price} onChange={e => setPrice(e.target.value)} placeholder="VD: 999.99" disabled={isLoading || !currentDbName} required/>
                    <label htmlFor="addDiscountPrice" style={{ marginLeft: '15px' }}>Giá KM:</label>
                    <input id="addDiscountPrice" type="text" value={discountPrice} onChange={e => setDiscountPrice(e.target.value)} placeholder="VD: 899.99 (bỏ trống nếu không có)" disabled={isLoading || !currentDbName} />
                </div>

                {/* Description */}
                <div className="control-group">
                    <label htmlFor="addDesc">Mô tả:</label>
                    <input id="addDesc" type="text" value={description} onChange={e => setDescription(e.target.value)} placeholder="Mô tả ngắn" disabled={isLoading || !currentDbName} style={{ flexGrow: 3 }}/>
                </div>

                {/* Content */}
                 <div className="control-group">
                     <label htmlFor="addContent">Nội dung:</label>
                     <textarea id="addContent" value={content} onChange={e => setContent(e.target.value)} placeholder="Chi tiết sản phẩm" disabled={isLoading || !currentDbName} rows="3" style={{ flexGrow: 3, minHeight: '60px' }}/>
                 </div>

                {/* Colors, Filters (Comma-separated) */}
                <div className="control-group">
                    <label htmlFor="addColors">Màu sắc:</label>
                    <input id="addColors" type="text" value={colors} onChange={e => setColors(e.target.value)} placeholder="VD: red, black, gold (phân cách bởi dấu phẩy)" disabled={isLoading || !currentDbName} style={{ flexGrow: 2 }} />
                    <label htmlFor="addFilters" style={{ marginLeft: '15px' }}>Filters:</label>
                    <input id="addFilters" type="text" value={filters} onChange={e => setFilters(e.target.value)} placeholder="VD: new, bestseller, men (phân cách bởi dấu phẩy)" disabled={isLoading || !currentDbName} style={{ flexGrow: 2 }} />
                </div>

                <button type="submit" disabled={isLoading || !currentDbName} className="btn btn-success" style={{backgroundColor: '#28a745'}}> {/* Thêm class btn-success hoặc style trực tiếp */}
                    Thêm Sản phẩm
                </button>
            </form>
        </div>
    );
}

export default AddProductForm;