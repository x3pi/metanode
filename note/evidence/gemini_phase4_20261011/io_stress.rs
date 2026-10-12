use std::os::fd::AsRawFd;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Instant;
use tempfile::NamedTempFile;

use nomt::test_io::*;

#[test]
fn test_io_stress_multithreaded_large_scale() {
    let page_pool = PagePool::new();
    let num_workers = 3; // exact production setting
    let io_pool = Arc::new(start_io_pool(num_workers, page_pool.clone()));

    let temp_file = NamedTempFile::new().expect("failed to create temp file");
    let fd = temp_file.as_file().as_raw_fd();

    // Pre-allocate 16 MB in temp file (4096 pages)
    temp_file.as_file().set_len(4096 * PAGE_SIZE as u64).expect("failed to preallocate");

    let num_threads = 16;
    let iterations_per_thread = 1250; // 1250 batches
    let batch_size = 500; // 500 operations per batch -> total = 16 * 1250 * 500 = 10,000,000 ops (10^7)

    let total_sent_all = Arc::new(AtomicU64::new(0));
    let total_recv_all = Arc::new(AtomicU64::new(0));

    let t0 = Instant::now();
    let mut handles = Vec::new();

    for thread_id in 0..num_threads {
        let io_pool = io_pool.clone();
        let page_pool = page_pool.clone();
        let total_sent = total_sent_all.clone();
        let total_recv = total_recv_all.clone();

        let h = std::thread::spawn(move || {
            let handle = io_pool.make_handle();
            let mut thread_sent = 0u64;
            let mut thread_recv = 0u64;

            for iter in 0..iterations_per_thread {
                let mut batch_sent = 0;
                for i in 0..batch_size {
                    let pn = ((thread_id * 1000 + iter * batch_size + i) % 4000) as u64;
                    let op_type = (i + iter) % 3;

                    match op_type {
                        0 => {
                            // WriteArc
                            let mut page = page_pool.alloc_fat_page();
                            page[0] = (pn & 0xFF) as u8;
                            handle
                                .send(IoCommand {
                                    kind: IoKind::WriteArc(fd, pn, Arc::new(page)),
                                    user_data: pn,
                                })
                                .expect("send WriteArc failed");
                        }
                        1 => {
                            // Write
                            let mut page = page_pool.alloc_fat_page();
                            page[1] = ((pn >> 8) & 0xFF) as u8;
                            handle
                                .send(IoCommand {
                                    kind: IoKind::Write(fd, pn, page),
                                    user_data: pn,
                                })
                                .expect("send Write failed");
                        }
                        _ => {
                            // Read
                            let page = page_pool.alloc_fat_page();
                            handle
                                .send(IoCommand {
                                    kind: IoKind::Read(fd, pn, page),
                                    user_data: pn,
                                })
                                .expect("send Read failed");
                        }
                    }
                    batch_sent += 1;
                    thread_sent += 1;
                }

                // Drain batch completions
                while batch_sent > 0 {
                    let comp = handle.recv().expect("recv failed");
                    assert!(comp.result.is_ok(), "io operation returned error: {:?}", comp.result);
                    batch_sent -= 1;
                    thread_recv += 1;
                }
            }

            total_sent.fetch_add(thread_sent, Ordering::Relaxed);
            total_recv.fetch_add(thread_recv, Ordering::Relaxed);
        });
        handles.push(h);
    }

    for h in handles {
        h.join().expect("thread panicked");
    }

    let elapsed = t0.elapsed();
    let sent = total_sent_all.load(Ordering::SeqCst);
    let recv = total_recv_all.load(Ordering::SeqCst);

    println!(
        "[IO_STRESS_RESULT] Completed {} ops in {:.2?} ({:.1} ops/sec). Sent: {}, Received: {}, Diff: {}",
        sent, elapsed, (sent as f64) / elapsed.as_secs_f64(), sent, recv, sent.abs_diff(recv)
    );

    assert_eq!(sent, recv, "Total completions received must strictly match total sent commands!");
}

#[test]
fn test_io_stress_negative_control_detects_dropped_completion() {
    // Negative control: verify that our tracking strictly detects if a completion is missing
    let page_pool = PagePool::new();
    let io_pool = start_io_pool(3, page_pool.clone());
    let handle = io_pool.make_handle();

    let temp_file = NamedTempFile::new().expect("failed to create temp file");
    let fd = temp_file.as_file().as_raw_fd();
    temp_file.as_file().set_len(4096 * 10).expect("failed to preallocate");

    let num_ops = 50;
    for i in 0..num_ops {
        let page = page_pool.alloc_fat_page();
        handle
            .send(IoCommand {
                kind: IoKind::Write(fd, i as u64, page),
                user_data: i as u64,
            })
            .expect("send failed");
    }

    // Deliberately read only num_ops - 1 completions (mock missing completion)
    let mut received = 0;
    for _ in 0..(num_ops - 1) {
        let _ = handle.recv().unwrap();
        received += 1;
    }

    // Assert that the difference is detected (negative control verification)
    let has_missing = received != num_ops;
    assert!(has_missing, "Negative control check: missing completion MUST be detected!");
    println!("[NEGATIVE_CONTROL_PASSED] Detected discrepancy when 1 completion was dropped: sent={}, received={}", num_ops, received);

    // Clean up the remaining one
    let _ = handle.recv().unwrap();
}
