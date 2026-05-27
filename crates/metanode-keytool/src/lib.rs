use std::path::{Path, PathBuf};
use std::{fs, os::unix::fs::PermissionsExt};

use anyhow::{Context, Result};
use clap::Subcommand;
use fastcrypto::{
    bls12381, ed25519, secp256k1,
    traits::{KeyPair, ToFromBytes},
};
use rand::thread_rng;
use serde::{Deserialize, Serialize};
use tiny_keccak::{Hasher, Keccak};

// ────────────────────────────────────────────────────────────────────────────
// CLI definition
// ────────────────────────────────────────────────────────────────────────────

#[derive(Subcommand, Debug, Clone)]
pub enum Commands {
    /// Generate keys
    Generate {
        #[command(subcommand)]
        kind: GenerateKind,
    },
    /// Show public key info from a saved key file
    Show {
        /// Path to a key JSON file (authority_key.json / protocol_key.json /
        /// network_key.json / eth_key.json)
        file: PathBuf,
    },
}

#[derive(Subcommand, Debug, Clone)]
pub enum GenerateKind {
    /// Generate ALL keys for a validator: BLS authority + Ed25519 protocol +
    /// Ed25519 network + secp256k1 ETH.
    Validator {
        /// Directory to write key files into (created if absent)
        #[arg(long, short, default_value = ".")]
        out_dir: PathBuf,
    },
    /// Generate only the BLS12-381 authority key pair
    Bls {
        #[arg(long, short, default_value = ".")]
        out_dir: PathBuf,
    },
    /// Generate only the Ed25519 protocol key pair
    Protocol {
        #[arg(long, short, default_value = ".")]
        out_dir: PathBuf,
    },
    /// Generate only the Ed25519 network key pair
    Network {
        #[arg(long, short, default_value = ".")]
        out_dir: PathBuf,
    },
    /// Generate only the secp256k1 ETH key pair (address + private key)
    Eth {
        #[arg(long, short, default_value = ".")]
        out_dir: PathBuf,
    },
}

// ────────────────────────────────────────────────────────────────────────────
// On-disk key formats
// ────────────────────────────────────────────────────────────────────────────

/// BLS12-381 authority key: private key stored as lowercase hex,
/// public key as base64 (matches `metanode generate` output format).
#[derive(Serialize, Deserialize)]
pub struct AuthorityKeyFile {
    /// Private key bytes as lowercase hex (48 bytes / 96 hex chars)
    pub private_key_hex: String,
    /// Public key as base64 (96 bytes for BLS12-381 min_sig)
    pub public_key_base64: String,
}

/// Ed25519 key file (protocol or network key)
#[derive(Serialize, Deserialize)]
pub struct Ed25519KeyFile {
    /// Private key bytes as lowercase hex (32 bytes)
    pub private_key_hex: String,
    /// Public key as base64 (32 bytes)
    pub public_key_base64: String,
}

/// ETH secp256k1 key file — compatible with gen_validator_entry.py
#[derive(Serialize, Deserialize)]
pub struct EthKeyFile {
    /// "0x" + 64 hex chars
    #[serde(rename = "ETH_PRIVATE_KEY")]
    pub eth_private_key: String,
    /// "0x" + 40 hex chars (EIP-55 checksum not applied, lowercase)
    #[serde(rename = "ETH_ADDRESS")]
    pub eth_address: String,
}

/// keys_summary.json — all public keys for use in genesis entry
#[derive(Serialize, Deserialize)]
pub struct KeysSummary {
    /// BLS12-381 public key (base64) — "authority_key" in genesis
    pub authority_key: String,
    /// Ed25519 public key (base64) — "protocol_key" in genesis
    pub protocol_key: String,
    /// Ed25519 public key (base64) — "network_key" in genesis
    pub network_key: String,
    /// Ethereum address (0x-prefixed lowercase)
    pub eth_address: String,
}

// ────────────────────────────────────────────────────────────────────────────
// Key generation helpers
// ────────────────────────────────────────────────────────────────────────────

/// Generate BLS12-381 authority keypair (min_sig variant, same as consensus).
pub fn gen_bls() -> (bls12381::min_sig::BLS12381KeyPair, AuthorityKeyFile) {
    let kp = bls12381::min_sig::BLS12381KeyPair::generate(&mut thread_rng());
    let priv_bytes: Vec<u8> = kp.copy().private().as_bytes().to_vec();
    let pub_bytes = kp.public().as_bytes().to_vec();
    let file = AuthorityKeyFile {
        private_key_hex: hex::encode(&priv_bytes),
        public_key_base64: base64_encode(&pub_bytes),
    };
    (kp, file)
}

/// Generate Ed25519 keypair (used for both protocol_key and network_key).
pub fn gen_ed25519() -> (ed25519::Ed25519KeyPair, Ed25519KeyFile) {
    let kp = ed25519::Ed25519KeyPair::generate(&mut thread_rng());
    let priv_bytes = kp.copy().private().as_bytes().to_vec();
    let pub_bytes = kp.public().as_bytes().to_vec();
    let file = Ed25519KeyFile {
        private_key_hex: hex::encode(&priv_bytes),
        public_key_base64: base64_encode(&pub_bytes),
    };
    (kp, file)
}

/// Generate secp256k1 keypair and derive the ETH address.
///
/// ETH address = keccak256(uncompressed_pub[1..])[12..] (last 20 bytes)
pub fn gen_eth() -> EthKeyFile {
    let kp = secp256k1::Secp256k1KeyPair::generate(&mut thread_rng());
    let priv_bytes = kp.copy().private().as_bytes().to_vec();

    // Derive ETH address from uncompressed public key (65 bytes, starts with 0x04)
    let uncompressed = kp.public().as_bytes().to_vec();
    let eth_address = eth_address_from_uncompressed_pubkey(&uncompressed);

    EthKeyFile {
        eth_private_key: format!("0x{}", hex::encode(&priv_bytes)),
        eth_address: format!("0x{}", eth_address),
    }
}

/// Compute ETH address from an uncompressed secp256k1 public key (65 bytes).
/// Follows EIP-55: keccak256(pubkey[1..])[12..]
pub fn eth_address_from_uncompressed_pubkey(uncompressed: &[u8]) -> String {
    // Strip the 0x04 prefix — we hash only the 64 coordinate bytes
    let coords = if uncompressed.len() == 65 && uncompressed[0] == 0x04 {
        &uncompressed[1..]
    } else {
        uncompressed
    };

    let mut keccak = Keccak::v256();
    let mut output = [0u8; 32];
    keccak.update(coords);
    keccak.finalize(&mut output);

    // Last 20 bytes = ETH address
    hex::encode(&output[12..])
}

fn base64_encode(bytes: &[u8]) -> String {
    base64_std(bytes)
}

/// Minimal standard base64 encoder (RFC 4648 without padding variants).
fn base64_std(input: &[u8]) -> String {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::new();
    let mut iter = input.chunks(3);
    while let Some(chunk) = iter.next() {
        let b0 = chunk[0] as usize;
        let b1 = if chunk.len() > 1 { chunk[1] as usize } else { 0 };
        let b2 = if chunk.len() > 2 { chunk[2] as usize } else { 0 };
        out.push(TABLE[b0 >> 2] as char);
        out.push(TABLE[((b0 & 0x3) << 4) | (b1 >> 4)] as char);
        if chunk.len() > 1 {
            out.push(TABLE[((b1 & 0xf) << 2) | (b2 >> 6)] as char);
        } else {
            out.push('=');
        }
        if chunk.len() > 2 {
            out.push(TABLE[b2 & 0x3f] as char);
        } else {
            out.push('=');
        }
    }
    out
}

// ────────────────────────────────────────────────────────────────────────────
// File write helpers
// ────────────────────────────────────────────────────────────────────────────

fn write_secret_json<T: Serialize>(path: &Path, value: &T) -> Result<()> {
    let json = serde_json::to_string_pretty(value)?;
    fs::write(path, &json).with_context(|| format!("write {}", path.display()))?;
    // chmod 600 — owner read/write only
    fs::set_permissions(path, fs::Permissions::from_mode(0o600))
        .with_context(|| format!("chmod {}", path.display()))?;
    Ok(())
}

fn write_json<T: Serialize>(path: &Path, value: &T) -> Result<()> {
    let json = serde_json::to_string_pretty(value)?;
    fs::write(path, &json).with_context(|| format!("write {}", path.display()))?;
    Ok(())
}

// ────────────────────────────────────────────────────────────────────────────
// Subcommand handlers
// ────────────────────────────────────────────────────────────────────────────

pub fn cmd_generate_validator(out_dir: &Path) -> Result<()> {
    fs::create_dir_all(out_dir)?;

    println!("🔑 Generating validator keys → {}", out_dir.display());

    // 1. BLS authority key
    let (_, bls_file) = gen_bls();
    let bls_pub = bls_file.public_key_base64.clone();
    let auth_path = out_dir.join("authority_key.json");
    write_secret_json(&auth_path, &bls_file)?;
    println!("  ✅ authority_key.json   (BLS12-381)");

    // 2. Ed25519 protocol key
    let (_, proto_file) = gen_ed25519();
    let proto_pub = proto_file.public_key_base64.clone();
    let proto_path = out_dir.join("protocol_key.json");
    write_secret_json(&proto_path, &proto_file)?;
    println!("  ✅ protocol_key.json    (Ed25519)");

    // 3. Ed25519 network key
    let (_, net_file) = gen_ed25519();
    let net_pub = net_file.public_key_base64.clone();
    let net_path = out_dir.join("network_key.json");
    write_secret_json(&net_path, &net_file)?;
    println!("  ✅ network_key.json     (Ed25519)");

    // 4. ETH secp256k1 key
    let eth_file = gen_eth();
    let eth_addr = eth_file.eth_address.clone();
    let eth_path = out_dir.join("eth_key.json");
    write_secret_json(&eth_path, &eth_file)?;
    println!("  ✅ eth_key.json         (secp256k1 / ETH)");

    // 5. Summary for genesis
    let summary = KeysSummary {
        authority_key: bls_pub,
        protocol_key: proto_pub,
        network_key: net_pub,
        eth_address: eth_addr.clone(),
    };
    let summary_path = out_dir.join("keys_summary.json");
    write_json(&summary_path, &summary)?;
    println!("  ✅ keys_summary.json    (public keys for genesis entry)");

    println!();
    println!("  ETH address : {}", eth_addr);
    println!();
    println!("⚠️  BACKUP YOUR KEYS IMMEDIATELY:");
    println!("  cp -r {} ~/backup_keys_$(date +%Y%m%d)", out_dir.display());
    println!();
    println!("📋 Next step: pass keys_summary.json values into your genesis entry");
    println!("   (authority_key, protocol_key, network_key, address)");

    Ok(())
}

pub fn cmd_generate_bls(out_dir: &Path) -> Result<()> {
    fs::create_dir_all(out_dir)?;
    let (_, file) = gen_bls();
    let path = out_dir.join("authority_key.json");
    write_secret_json(&path, &file)?;
    println!("✅ authority_key.json saved to {}", path.display());
    println!("   Public key (base64): {}", file.public_key_base64);
    Ok(())
}

pub fn cmd_generate_protocol(out_dir: &Path) -> Result<()> {
    fs::create_dir_all(out_dir)?;
    let (_, file) = gen_ed25519();
    let path = out_dir.join("protocol_key.json");
    write_secret_json(&path, &file)?;
    println!("✅ protocol_key.json saved to {}", path.display());
    println!("   Public key (base64): {}", file.public_key_base64);
    Ok(())
}

pub fn cmd_generate_network(out_dir: &Path) -> Result<()> {
    fs::create_dir_all(out_dir)?;
    let (_, file) = gen_ed25519();
    let path = out_dir.join("network_key.json");
    write_secret_json(&path, &file)?;
    println!("✅ network_key.json saved to {}", path.display());
    println!("   Public key (base64): {}", file.public_key_base64);
    Ok(())
}

pub fn cmd_generate_eth(out_dir: &Path) -> Result<()> {
    fs::create_dir_all(out_dir)?;
    let file = gen_eth();
    let path = out_dir.join("eth_key.json");
    println!("  ETH address : {}", file.eth_address);
    write_secret_json(&path, &file)?;
    println!("✅ eth_key.json saved to {}", path.display());
    Ok(())
}

pub fn cmd_show(file_path: &Path) -> Result<()> {
    let content = fs::read_to_string(file_path)
        .with_context(|| format!("Cannot read {}", file_path.display()))?;
    let name = file_path.file_name().unwrap_or_default().to_string_lossy();

    if name.contains("authority") {
        let f: AuthorityKeyFile = serde_json::from_str(&content)?;
        println!("Key type    : BLS12-381 authority key");
        println!("Public key  : {}", f.public_key_base64);
    } else if name.contains("protocol") {
        let f: Ed25519KeyFile = serde_json::from_str(&content)?;
        println!("Key type    : Ed25519 protocol key");
        println!("Public key  : {}", f.public_key_base64);
    } else if name.contains("network") {
        let f: Ed25519KeyFile = serde_json::from_str(&content)?;
        println!("Key type    : Ed25519 network key");
        println!("Public key  : {}", f.public_key_base64);
    } else if name.contains("eth") {
        let f: EthKeyFile = serde_json::from_str(&content)?;
        println!("Key type    : secp256k1 ETH key");
        println!("ETH address : {}", f.eth_address);
        println!("(private key is not displayed for security)");
    } else if name.contains("summary") {
        let f: KeysSummary = serde_json::from_str(&content)?;
        println!("Authority key : {}", f.authority_key);
        println!("Protocol key  : {}", f.protocol_key);
        println!("Network key   : {}", f.network_key);
        println!("ETH address   : {}", f.eth_address);
    } else {
        // Try to pretty-print as generic JSON
        let v: serde_json::Value = serde_json::from_str(&content)?;
        println!("{}", serde_json::to_string_pretty(&v)?);
    }

    Ok(())
}

// ────────────────────────────────────────────────────────────────────────────
// Entry runner
// ────────────────────────────────────────────────────────────────────────────

pub fn run_keytool(command: Commands) -> Result<()> {
    match command {
        Commands::Generate { kind } => match kind {
            GenerateKind::Validator { out_dir } => cmd_generate_validator(&out_dir),
            GenerateKind::Bls { out_dir } => cmd_generate_bls(&out_dir),
            GenerateKind::Protocol { out_dir } => cmd_generate_protocol(&out_dir),
            GenerateKind::Network { out_dir } => cmd_generate_network(&out_dir),
            GenerateKind::Eth { out_dir } => cmd_generate_eth(&out_dir),
        },
        Commands::Show { file } => cmd_show(&file),
    }
}
