use fastcrypto::ed25519::Ed25519KeyPair;
use fastcrypto::traits::ToFromBytes;
use fastcrypto::traits::KeyPair;

fn main() {
    let priv_hex = "c6fbbd7e660fa055c6eb913d8fe4d7bc0b3feffecb450083159577a85536fe78";
    let pub_hex = "9fa320cf6e25656878c82adccad1b78eaa173e8184ccbfa282da2cc4f9ad010f";
    
    let priv_bytes = hex::decode(priv_hex).unwrap();
    let pub_bytes = hex::decode(pub_hex).unwrap();
    
    let mut bytes = [0u8; 64];
    bytes[0..32].copy_from_slice(&priv_bytes);
    bytes[32..64].copy_from_slice(&pub_bytes);
    
    let kp = Ed25519KeyPair::from_bytes(&bytes).unwrap();
    let derived_pub_bytes = kp.public().as_ref();
    
    println!("Original public : {}", pub_hex);
    println!("Derived public  : {}", hex::encode(derived_pub_bytes));
}
