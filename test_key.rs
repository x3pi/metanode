use base64::{engine::general_purpose, Engine as _};
fn main() {
    let b = general_purpose::STANDARD.decode("PeeuyRNJtDHP/3q9AMa9dhGdQOEdlb/DxzthQwu1zdGkEi8Sm3W7P2/n4IOqlwP6tugmZyyQB7b/wSON+XkOUA==").unwrap();
    println!("Length: {}", b.len());
    println!("Bytes: {:?}", b);
}
