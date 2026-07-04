use nomt_db::RustStateDB;
use std::sync::Arc;

pub static GLOBAL_NOMT_DB: once_cell::sync::Lazy<Arc<RustStateDB>> = once_cell::sync::Lazy::new(|| {
    let db_path = std::env::var("NOMT_DB_PATH").unwrap_or_else(|_| ".data/nomt_db".to_string());
    Arc::new(RustStateDB::open(
        &db_path,
        8,    // concurrency
        2048,  // page cache mb
        2048,  // leaf cache mb
        20000,    // hashtable buckets
        true // preallocate ht
    ).expect("Failed to open NOMT DB"))
});

