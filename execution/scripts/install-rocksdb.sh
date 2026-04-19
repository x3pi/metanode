#!/bin/bash

# Dừng ngay khi có lỗi xảy ra
set -e

# Cài đặt các gói cần thiết
sudo apt update
sudo apt install -y build-essential libgflags-dev libsnappy-dev zlib1g-dev libbz2-dev liblz4-dev libzstd-dev git cmake

# Clone RocksDB từ GitHub
git clone https://github.com/facebook/rocksdb.git
cd rocksdb

# Biên dịch RocksDB với shared library
make shared_lib -j$(nproc)

# Cài đặt RocksDB
sudo make install-shared -j$(nproc)

# Xác minh cài đặt
ldconfig -p | grep rocksdb && echo "RocksDB đã được cài đặt thành công!" || echo "Cài đặt thất bại."
