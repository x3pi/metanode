fn main() {
    println!("=====================================================");
    println!("BẮT ĐẦU GIẢ LẬP GỌI SANG MÔI TRƯỜNG TRUSTZONE (OP-TEE)");
    println!("=====================================================");
    println!("\n[Test 1] Gọi hàm `run_empty_contract()`...");
    
    let start_time = std::time::Instant::now();
    let debug_res1 = metanode_tee_revm::run_empty_contract();
    let duration = start_time.elapsed();
    
    if debug_res1.contains("Success") {
        println!("[TEE]  ✅ Thành công! EVM đã thực thi một giao dịch rỗng.");
        println!("[TEE]  ⏱️ Thời gian thực thi: {:?}", duration);
    } else {
        println!("[TEE]  ❌ Thất bại! Lỗi thực thi giao dịch EVM.");
        println!("[TEE]  Chi tiết lỗi: {}", debug_res1);
    }

    println!("\n[Test 2] Bài test phức tạp (Cấp phát 5MB RAM trong EVM + Vòng lặp tới khi cạn kiệt 50 triệu Gas)...");
    let start_time_complex = std::time::Instant::now();
    let (complex_res, gas_used, debug_str) = metanode_tee_revm::run_complex_contract();
    let duration_complex = start_time_complex.elapsed();

    if complex_res {
        println!("[TEE]  ✅ Bài test hoàn tất. EVM đã chủ động ngắt vòng lặp (Halt/OOG).");
        println!("[TEE]  🔥 Tổng lượng Gas tiêu thụ: {}", gas_used);
        println!("[TEE]  ⏱️ Thời gian thực thi: {:?}", duration_complex);
        println!("[TEE]  🛡️ Trạng thái: 5MB RAM đã được cấp phát an toàn bên trong mức trần Heap 10MB mà không bị Crash.");
    } else {
        println!("[TEE]  ❌ Thất bại hoặc Crash khi chạy bài test nặng!");
        println!("[TEE]  Chi tiết lỗi: {}", debug_str);
    }
    
    println!("\n[Test 3] Tấn công DDoS bằng Calldata khổng lồ (Gửi chuỗi dữ liệu 5MB vào EVM)...");
    let start_time_large = std::time::Instant::now();
    let (large_res, large_debug_str) = metanode_tee_revm::run_large_calldata_contract();
    let duration_large = start_time_large.elapsed();

    if large_res {
        println!("[TEE]  ✅ Bài test hoàn tất. Bảo mật thành công.");
        println!("[TEE]  🛡️ Trạng thái: Giao dịch độc hại đã bị chặn ngay lập tức. {}", large_debug_str);
        println!("[TEE]  ⏱️ Thời gian phát hiện và chặn: {:?}", duration_large);
    } else {
        println!("[TEE]  ❌ Thất bại bảo mật!");
        println!("[TEE]  Chi tiết lỗi: {}", large_debug_str);
    }

    println!("\n[Test 4] Đỉnh giới hạn RAM toán học (Gas Limit 30 Triệu)");
    println!("Theo công thức tính Gas của Ethereum: Mở rộng bộ nhớ cần (Words^2)/512 + 3*Words Gas.");
    println!("-> Với 30 triệu Gas, hợp đồng chỉ có thể yêu cầu cấp phát tối đa ~3.94 MB.");
    
    // Test 4A: 3.94MB
    let (res_4a, gas_4a, _str_4a) = metanode_tee_revm::run_peak_limit_test(3_940_000, 30_000_000);
    if res_4a {
        println!("[TEE]  ✅ [4A - Safe Zone] Mở rộng 3.94 MB thành công! Đã tiêu thụ: {} Gas.", gas_4a);
    } else {
        println!("[TEE]  ❌ [4A] Lỗi bất ngờ!");
    }

    // Test 4B: 4.0MB
    let (res_4b, gas_4b, str_4b) = metanode_tee_revm::run_peak_limit_test(4_000_000, 30_000_000);
    if !res_4b && str_4b.contains("OutOfGas") {
        println!("[TEE]  ✅ [4B - OutOfGas] Yêu cầu 4.0 MB bị EVM TỪ CHỐI (OutOfGas). Đã dùng: {} Gas.", gas_4b);
        println!("[TEE]  🛡️ KẾT LUẬN: Bức tường Gas Limit hoàn toàn khóa chặt lượng RAM mà một giao dịch có thể ăn (<< 10MB Heap). TEE an toàn tuyệt đối!");
    } else {
        println!("[TEE]  ❌ [4B] Xuyên thủng bảo vệ? Lỗi: {}", str_4b);
    }

    println!("\n=====================================================");
    println!("KẾT THÚC GIẢ LẬP");
}
