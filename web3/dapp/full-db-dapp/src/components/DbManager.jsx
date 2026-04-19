// src/components/DbManager.jsx
import React, { useState, useEffect } from 'react';

/**
 * Component quản lý việc Tạo/Chọn Database và tạo dữ liệu mẫu.
 * @param {object} props - Props của component.
 * @param {string} props.currentDbName - Tên DB hiện tại đọc từ contract (nếu có).
 * @param {boolean} props.isLoading - Trạng thái loading chung (để disable nút).
 * @param {function} props.onGetOrCreateDb - Hàm callback gọi khi nhấn nút "Tạo / Chọn DB", truyền về tên DB từ input.
 * @param {function} props.onCreateSampleDb - Hàm callback gọi khi nhấn nút "Tạo DB Mẫu", truyền về tên DB từ input.
 */
function DbManager({ currentDbName, isLoading, onGetOrCreateDb, onCreateSampleDb }) {
  // State nội bộ để quản lý giá trị của ô input tên DB
  const [dbInput, setDbInput] = useState('products'); // Giá trị mặc định, ví dụ 'products'

  // Cập nhật input nếu currentDbName từ contract thay đổi (chỉ khi component mount hoặc currentDbName thực sự thay đổi)
  useEffect(() => {
    if (currentDbName) {
      setDbInput(currentDbName);
    }
    // Nếu muốn input luôn đồng bộ với contract state, bỏ điều kiện if
    // Nếu muốn giữ lại giá trị user nhập thì chỉ cập nhật lần đầu hoặc không cập nhật
  }, [currentDbName]);


  // Hàm xử lý khi click nút "Tạo / Chọn DB"
  const handleCreateClick = () => {
    // Chỉ gọi callback nếu input không rỗng và có hàm được truyền vào
    if (dbInput && typeof onGetOrCreateDb === 'function') {
      onGetOrCreateDb(dbInput);
    } else if (!dbInput) {
        console.warn("DbManager: Input tên DB đang trống.");
        // Có thể thêm setError hoặc thông báo cho người dùng ở đây nếu cần
    }
  };

  // Hàm xử lý khi click nút "Tạo DB Mẫu"
  const handleSampleClick = () => {
    // Chỉ gọi callback nếu input không rỗng và có hàm được truyền vào
    if (dbInput && typeof onCreateSampleDb === 'function') {
      onCreateSampleDb(dbInput);
    } else if (!dbInput) {
        console.warn("DbManager: Input tên DB đang trống để tạo mẫu.");
    }
  };

  return (
    // Sử dụng class `section-box` nếu CSS của bạn có định nghĩa chung
    <div className="db-management section-box">
      <h3>Quản lý Database</h3>
      <div className="control-group">
        <label htmlFor="dbNameInput">Tên DB:</label>
        <input
          type="text"
          id="dbNameInput"
          value={dbInput}
          onChange={(e) => setDbInput(e.target.value)} // Cập nhật state nội bộ khi user nhập
          placeholder="Nhập tên DB (vd: products)"
          disabled={isLoading} // Disable input khi đang loading
        />
        {/* Nút gọi hàm onGetOrCreateDb */}
        <button
          onClick={handleCreateClick}
          disabled={isLoading || !dbInput} // Disable khi loading hoặc input rỗng
          className="btn btn-sm" // Sử dụng class CSS nếu có
          title={`Tạo hoặc chọn database tên '${dbInput}'`}
        >
            Tạo / Chọn DB
        </button>
         {/* Nút gọi hàm onCreateSampleDb */}
        <button
          onClick={handleSampleClick}
          disabled={isLoading || !dbInput} // Disable khi loading hoặc input rỗng
          className="btn btn-sm btn-secondary" // Class CSS khác cho nút này
          title={`Tạo database mẫu tên '${dbInput}' (sẽ ghi đè nếu đã tồn tại)`}
        >
            Tạo DB Mẫu
        </button>
      </div>
      {/* Hiển thị tên DB hiện tại đang được contract sử dụng */}
      <p>DB Hiện tại (trên contract): <span className="current-db">{currentDbName || '(Chưa có)'}</span></p>
    </div>
  );
}

export default DbManager;