# CMake toolchain file for cross-compiling pkg/mvm's c_mvm/linker (real EVM
# interpreter + Xapian core) for aarch64-linux-gnu (glibc), NOT the
# musl/chcore TrustZone TA target (that's a separate, unrelated toolchain —
# see execution/pkg/mvm/ta/README.md and note/tee_dual_mode_execution_plan.md
# for that one).
#
# This is Giai đoạn 1 of cross-compiling metanode's Go binary
# (cmd/simple_chain) for the real board (Orange Pi 5 Max, aarch64) — see
# note/tee_dual_mode_execution_plan.md's cross-compile section for full
# context and remaining Giai đoạn 2 (Rust/RocksDB) scope.
#
# Uses the standard Debian/Ubuntu gcc-aarch64-linux-gnu cross package
# (already installed on this machine, /usr/bin/aarch64-linux-gnu-{gcc,g++})
# together with the arm64 -dev packages installed via
# `dpkg --add-architecture arm64` + `apt install ...:arm64` (gmp, mpfr, tbb,
# xapian, leveldb, uuid) — these land in the standard multiarch paths
# (/usr/include/aarch64-linux-gnu, /usr/lib/aarch64-linux-gnu), which the
# cross compiler already knows to search by default. No separate sysroot is
# needed; CMAKE_FIND_ROOT_PATH_MODE_* below just makes sure CMake's own
# find_package/find_library calls (if any get added later) prefer the
# target's libs over the host's, rather than needing an explicit
# CMAKE_FIND_ROOT_PATH.
#
# Usage: cmake -DCMAKE_TOOLCHAIN_FILE=<this file> -DMVM_INSTALL_PREFIX=<out-of-tree dir> ...
# (MVM_INSTALL_PREFIX / MVM_C_MVM_BUILD_DIR overrides already exist in
# c_mvm/CMakeLists.txt and linker/CMakeLists.txt specifically so a cross
# build can never clobber the real x86 in-tree build/ — see those files'
# own comments for the 2026-08-16 incident that motivated them. ALWAYS pass
# an out-of-tree MVM_INSTALL_PREFIX with this toolchain file.)

set(CMAKE_SYSTEM_NAME Linux)
set(CMAKE_SYSTEM_PROCESSOR aarch64)

set(CMAKE_C_COMPILER aarch64-linux-gnu-gcc)
set(CMAKE_CXX_COMPILER aarch64-linux-gnu-g++)

# Don't let CMake pick up host (x86_64) binaries/libs/headers by accident.
set(CMAKE_FIND_ROOT_PATH_MODE_PROGRAM NEVER)
set(CMAKE_FIND_ROOT_PATH_MODE_LIBRARY ONLY)
set(CMAKE_FIND_ROOT_PATH_MODE_INCLUDE ONLY)
set(CMAKE_FIND_ROOT_PATH_MODE_PACKAGE ONLY)
