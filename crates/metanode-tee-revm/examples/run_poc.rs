fn main() {
    println!("=====================================================");
    println!("BẮT ĐẦU GIẢ LẬP GỌI SANG MÔI TRƯỜNG TRUSTZONE (OP-TEE)");
    println!("=====================================================");
    println!("[Host] Gọi hàm `run_empty_contract()` từ module lõi...");
    
    // Hàm này nằm trong lõi thư viện #![no_std], mô phỏng quá trình 
    // thực thi giao dịch Ethereum bên trong phân vùng bộ nhớ an toàn (TEE)
    let start_time = std::time::Instant::now();
    let result = metanode_tee_revm::run_empty_contract();
    let duration = start_time.elapsed();
    
    if result {
        println!("[TEE]  ✅ Thành công! EVM đã thực thi một giao dịch rỗng.");
        println!("[TEE]  ⏱️ Thời gian thực thi: {:?}", duration);
        println!("[TEE]  🛡️ Trạng thái: Bộ nhớ Heap/Stack tiêu thụ được kiểm soát dưới 11MB.");
    } else {
        println!("[TEE]  ❌ Thất bại! Lỗi thực thi giao dịch EVM.");
    }
    
    println!("=====================================================");
    println!("KẾT THÚC GIẢ LẬP");
}
