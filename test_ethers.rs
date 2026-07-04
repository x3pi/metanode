use ethers_core::types::Transaction;
use ethers_core::utils::rlp::Decodable;
use std::str::FromStr;

fn main() {
    let raw = "0x02f86f010b8459682f008502540be4008252089400000000000000000000000000000000000000008080c080a01234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdefa01234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef";
    let bytes = hex::decode(&raw[2..]).unwrap();
    let tx = Transaction::decode(&mut bytes.as_slice()).unwrap();
    println!("{:?}", tx.recover_sender());
}
