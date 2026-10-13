#[cfg(not(target_family = "unix"))]
std::compile_error!("NOMT only supports Unix-based OSs");

use crossbeam_channel::{Receiver, RecvError, RecvTimeoutError, SendError, Sender, TryRecvError};
use page_pool::Page;
use std::{
    fmt,
    fs::File,
    os::fd::RawFd,
    sync::{Arc, Weak},
};
use threadpool::ThreadPool;

#[cfg(target_os = "linux")]
#[path = "linux.rs"]
mod platform;

#[cfg(not(target_os = "linux"))]
#[path = "unix.rs"]
mod platform;

pub mod fsyncer;
pub mod page_pool;

pub const PAGE_SIZE: usize = 4096;

pub use page_pool::{FatPage, PagePool};

/// Whether the current device has permission to use io_uring.
///
/// If not linux, this will always return `NotSupported`.
pub enum IoUringPermission {
    /// The device has permission to use io_uring.
    Allowed,
    /// The device does not have permission to use io_uring.
    Denied,
    /// This version of NOMT was compiled targeting a non-Linux platform, or a version of Linux
    /// where the syscalls or flags are not available, so io_uring is not supported.
    ///
    /// Note that a 6.x or newer Linux kernel is required to support the flags NOMT uses.
    NotSupported,
}

/// Check whether the current device has permission to use io_uring.
///
/// On non-Linux platforms, this will always return `NotSupported`.
pub fn check_iou_permissions() -> IoUringPermission {
    platform::check_iou_permissions()
}

pub enum IoKind {
    Read(RawFd, u64, FatPage),
    Write(RawFd, u64, FatPage),
    WriteArc(RawFd, u64, Arc<FatPage>),
    WriteRaw(RawFd, u64, Page),
}

impl fmt::Debug for IoKind {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            IoKind::Read(fd, pn, _) => write!(f, "Read(fd={}, pn={})", fd, pn),
            IoKind::Write(fd, pn, _) => write!(f, "Write(fd={}, pn={})", fd, pn),
            IoKind::WriteArc(fd, pn, _) => write!(f, "WriteArc(fd={}, pn={})", fd, pn),
            IoKind::WriteRaw(fd, pn, _) => write!(f, "WriteRaw(fd={}, pn={})", fd, pn),
        }
    }
}

pub enum IoKindResult {
    Ok,
    Err,
    Retry,
}

impl IoKind {
    pub fn unwrap_buf(self) -> FatPage {
        match self {
            IoKind::Read(_, _, buf) | IoKind::Write(_, _, buf) => buf,
            IoKind::WriteArc(_, _, _) => panic!("attempted to extract owned buf from write_arc"),
            IoKind::WriteRaw(_, _, _) => panic!("attempted to extract buf from write_raw"),
        }
    }

    /// Expected to be called directly after a failing syscall. IOW, errno should not clobbered.
    pub fn get_result(&self, res: isize) -> IoKindResult {
        match self {
            // pread returns 0 if the file has been read till the end of file
            //
            // This could be a failure because the end of the file could be smaller than PAGE_SIZE.
            // However, as each read operation follows a write operation,
            // there should be no unexpected end-of-file that is not aligned with PAGE_SIZE
            // when all previous writes have succeeded.
            IoKind::Read(_, _, _) if res == 0 => IoKindResult::Ok,
            // pread and pwrite return the number of bytes read or written
            _ if res == PAGE_SIZE as isize => IoKindResult::Ok,
            _ if res == -1 => {
                let os_err = std::io::Error::last_os_error();
                if matches!(os_err.kind(), std::io::ErrorKind::Interrupted) {
                    IoKindResult::Retry
                } else {
                    IoKindResult::Err
                }
            }
            _ => IoKindResult::Retry,
        }
    }
}

pub struct IoCommand {
    pub kind: IoKind,
    // note: this isn't passed to io_uring, it's higher-level userdata.
    pub user_data: u64,
}

pub struct CompleteIo {
    pub command: IoCommand,
    pub result: std::io::Result<()>,
}

struct IoPacket {
    command: IoCommand,
    completion_sender: Sender<CompleteIo>,
}

/// Create an I/O worker managing an io_uring and sending responses back via channels to a number
/// of handles.
pub fn start_io_pool(io_workers: usize, page_pool: PagePool) -> IoPool {
    let io_workers_tp = ThreadPool::with_name("io-worker".to_string(), io_workers);
    let sender = platform::start_io_worker(page_pool.clone(), &io_workers_tp, io_workers);
    let sender = Some(Arc::new(sender));
    IoPool {
        sender,
        page_pool,
        io_workers_tp,
    }
}

#[cfg(test)]
pub fn start_test_io_pool(io_workers: usize, page_pool: PagePool) -> IoPool {
    start_io_pool(io_workers, page_pool)
}

/// A manager for the broader I/O pool. This can be used to create new I/O handles.
pub struct IoPool {
    /// Sender to send I/O commands to the I/O workers.
    ///
    /// Every IoHandle is created with a weak reference to this sender, the only one non-transient
    /// strong reference is held by this struct. We say "non-transient" because the channel might
    /// occasionally be upgraded when sending message.
    ///
    /// Upon shutdown, this only reference is dropped by `take`ing it, causing the channel to close
    /// and the I/O workers to shut down.
    sender: Option<Arc<Sender<IoPacket>>>,
    page_pool: PagePool,
    io_workers_tp: ThreadPool,
}

impl IoPool {
    /// Create a new I/O handle.
    ///
    /// This will panic if the I/O pool has been shut down.
    pub fn make_handle(&self) -> IoHandle {
        let (completion_sender, completion_receiver) = crossbeam_channel::unbounded();
        let sender = self
            .sender
            .as_ref()
            .expect("call to make_handle after shutdown");
        let sender = Arc::downgrade(sender);
        IoHandle {
            sender,
            completion_sender,
            completion_receiver,
        }
    }

    pub fn page_pool(&self) -> &PagePool {
        &self.page_pool
    }

    /// Initiate the shutdown procedure.
    ///
    /// This will return only after all the I/O workers are shut down.
    pub fn shutdown(&mut self) {
        // There is only a single strong reference to the sender, dropping it will close the
        // channel, causing the I/O workers to shut down.
        let sender = self.sender.take().unwrap();
        drop(sender);
        self.io_workers_tp.join();
    }
}

/// A handle for submitting I/O commands and receiving their completions.
///
/// Only completions for commands submitted on this handle or its clones will be received, and in
/// no guaranteed order.
///
/// Clones, like normal channel clones, do not create a new handle but instead read from the same
/// stream of completions.
///
/// This is safe to use across multiple threads, but care must be taken by the user for correctness.
#[derive(Clone)]
pub struct IoHandle {
    sender: Weak<Sender<IoPacket>>,
    completion_sender: Sender<CompleteIo>,
    completion_receiver: Receiver<CompleteIo>,
}

impl IoHandle {
    /// Send an I/O command. This fails if the channel has hung up, but does not block the thread.
    pub fn send(&self, command: IoCommand) -> Result<(), SendError<IoCommand>> {
        let sender = match self.sender.upgrade() {
            Some(sender) => sender,
            None => return Err(SendError(command)),
        };
        sender
            .send(IoPacket {
                command,
                completion_sender: self.completion_sender.clone(),
            })
            .map_err(|SendError(packet)| SendError(packet.command))
    }

    /// Block the current thread on receiving an I/O completion.
    /// This fails if the channel has hung up.
    ///
    /// LOCAL PATCH (see consensus/vendor/NOMT_PATCHES.md #2): this polls with a short timeout instead of a single
    /// unbounded `recv()`. A live hang was captured with the completion channel NON-EMPTY (11 queued completions)
    /// while the committing thread stayed parked inside `recv()` forever, i.e. a lost wake-up. Polling re-checks the
    /// queue every `RECV_POLL`, so a completion that was queued without waking us is picked up. It still waits for
    /// exactly the completions it was waiting for, however long that takes: no outcome depends on the elapsed time.
    pub fn recv(&self) -> Result<CompleteIo, RecvError> {
        const RECV_POLL: std::time::Duration = std::time::Duration::from_millis(100);
        // Counts completions that needed at least one full poll slice. That is either genuinely slow I/O or a
        // lost wake-up that polling recovered; the two cannot be told apart here, so it is only a signal to watch.
        static SLOW_RECVS: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
        let started = std::time::Instant::now();
        loop {
            match self.completion_receiver.recv_timeout(RECV_POLL) {
                Ok(complete) => {
                    // crossbeam re-checks the queue when the timeout expires and returns the message as Ok (not as
                    // Timeout), so a recovered lost wake-up surfaces here after about one poll slice.
                    if started.elapsed() >= RECV_POLL {
                        let n = SLOW_RECVS.fetch_add(1, std::sync::atomic::Ordering::Relaxed) + 1;
                        if n <= 20 || n % 1000 == 0 {
                            eprintln!(
                                "[NOMT-IO] recv waited {:?} (>= one poll slice; slow I/O or a recovered lost wake-up), occurrence #{}",
                                started.elapsed(),
                                n
                            );
                        }
                    }
                    return Ok(complete);
                }
                Err(RecvTimeoutError::Disconnected) => return Err(RecvError),
                Err(RecvTimeoutError::Timeout) => continue,
            }
        }
    }

    /// Try to receive an I/O completion without blocking.
    pub fn try_recv(&self) -> Result<CompleteIo, TryRecvError> {
        self.completion_receiver.try_recv()
    }

    /// Get the underlying receiver.
    pub fn receiver(&self) -> &Receiver<CompleteIo> {
        &self.completion_receiver
    }

    /// Creates a new handle that can be used to submit I/O commands.
    ///
    /// Unlike [`Self::clone`] this creates a new handle that can be used independently of the
    /// original handle.
    ///
    /// This will panic if the I/O pool has been shut down.
    pub fn make_new_sibiling_handle(&self) -> IoHandle {
        let (completion_sender, completion_receiver) = crossbeam_channel::unbounded();
        IoHandle {
            sender: self.sender.clone(),
            completion_sender,
            completion_receiver,
        }
    }
}

/// Read a page from the file at the given page number.
pub fn read_page(page_pool: &PagePool, fd: &File, pn: u64) -> std::io::Result<FatPage> {
    use std::os::unix::fs::FileExt as _;
    let mut page = page_pool.alloc_fat_page();
    fd.read_exact_at(&mut page[..], pn * PAGE_SIZE as u64)?;
    Ok(page)
}
