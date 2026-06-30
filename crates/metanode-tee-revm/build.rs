fn main() {
    // Configuration for OP-TEE Trusted Application (TA)
    
    // Set Heap size to 10MB (10 * 1024 * 1024)
    println!("cargo:rustc-env=TA_DATA_SIZE=10485760");
    
    // Set Stack size to 1MB (1 * 1024 * 1024)
    println!("cargo:rustc-env=TA_STACK_SIZE=1048576");
    
    // The sum is 11MB, which guarantees execution strictly under the 16MB Secure RAM limit.
}
