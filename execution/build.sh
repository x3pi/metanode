#!/bin/bash

# Kiểm tra đầu vào
if [ -z "$1" ]; then
    echo "Vui lòng chỉ định hệ điều hành cần kiểm tra: 'mac' hoặc 'linux'."
    exit 1
fi

# Chuyển đến thư mục pkg/mvm
cd ./pkg/mvm
if [ $? -ne 0 ]; then  # Sửa cú pháp của điều kiện
    exit 1            # Sửa cú pháp của exit
fi
MVM_DIR="$(pwd)"

# Các file linker cố định
LINKER="./linker/build/CMakeCache.txt"
EXPECTED_TYPE=""

OS_TYPE=$1
LINKER_FILE_TYPE=""

# Kiểm tra sự tồn tại của file linker
if [ -f "$LINKER" ]; then
    echo "exists File linker $LINKER..."
	# Kiểm tra kiểu file của linker
	if grep -q "_OSX_" "$LINKER"; then
	    LINKER_FILE_TYPE="mac"
	else
	    LINKER_FILE_TYPE="linux"
	fi

fi


if [[ "$LINKER_FILE_TYPE" != "$OS_TYPE" ]]; then
	# echo "File linker $LINKER building... $LINKER_FILE_TYPE - $OS_TYPE"
	# exit 1
    # Khác kiểu, cần build lại
    rm -rf ./linker/build && rm -rf ./c_mvm/build
    bash ./build.sh
    if [ $? -ne 0 ]; then  # Sửa cú pháp của điều kiện
        exit 1            # Sửa cú pháp của exit
    fi


	# exit 1
	# Chuyển đến thư mục cmd/simple_chain
	cd ../../cmd/simple_chain
	if [ $? -ne 0 ]; then  # Sửa cú pháp của điều kiện
	    exit 1            # Sửa cú pháp của exit
	fi
	
	# Thực hiện lệnh make clean
	make clean
	if [ $? -ne 0 ]; then  # Sửa cú pháp của điều kiện
	    exit 1            # Sửa cú pháp của exit
	fi
else
    echo "File linker $LINKER có kiểu đúng: $EXPECTED_TYPE."
fi

# ═══════════════════════════════════════════════════════════════
# ALWAYS rebuild C++ EVM linker (handles code changes in .cpp/.h)
# This is fast (~2s) when nothing changed (make skips up-to-date targets)
# ═══════════════════════════════════════════════════════════════
cd "$MVM_DIR"
echo "🔨 [EVM] Rebuilding C++ EVM linker..."
cd ./linker/build 2>/dev/null || { mkdir -p ./linker/build && cd ./linker/build && cmake .. ; }
make -j$(nproc)
if [ $? -ne 0 ]; then
    echo "❌ [EVM] C++ EVM linker build FAILED"
    exit 1
fi
echo "✅ [EVM] C++ EVM linker build OK"
cd ../../

# ═══════════════════════════════════════════════════════════════
# Build NOMT (Nearly Optimal Merkle Trie) Rust FFI library
# This produces libmtn_nomt.a used by Go CGo bindings
# ═══════════════════════════════════════════════════════════════
NOMT_FFI_DIR="$(cd ../../pkg/nomt_ffi/rust_lib && pwd)"
if [ -d "$NOMT_FFI_DIR" ]; then
    echo "🔨 [NOMT] Building Rust NOMT FFI library..."
    if command -v cargo &> /dev/null; then
        cd "$NOMT_FFI_DIR"
        cargo build --release 2>&1
        if [ $? -ne 0 ]; then
            echo "❌ [NOMT] Rust NOMT FFI build FAILED"
            exit 1
        fi
        echo "✅ [NOMT] Rust NOMT FFI build OK ($(ls -lh target/release/libmtn_nomt.a | awk '{print $5}'))"
    else
        echo "⚠️ [NOMT] Rust toolchain (cargo) not found, skipping NOMT build"
    fi
    cd "$MVM_DIR"
fi


# Thực hiện lệnh make
# make
# if [ $? -ne 0 ]; then  # Sửa cú pháp của điều kiện
#     exit 1            # Sửa cú pháp của exit
# fi

# Các dòng lệnh đã bị comment (có thể bỏ nếu cần thiết):
# # Copy the bins to the sample directory
# cp simple_chain sample/simple/
# cp simple_chain sample/master-master/main/
# cp simple_chain sample/master-master/sub/

# cp migrate_data sample/simple/
# cp migrate_data sample/master-master/main/
# cp migrate_data sample/master-master/sub/

# if [ $? != 0 ]; then  # Sửa cú pháp của điều kiện
#     exit 1            # Sửa cú pháp của exit
# fi                    # Sửa cú pháp của endif

# cd ./sample/simple
# rm -rf contract_creation.log 
# rm -rf contract_creation2.log 
# rm -rf contract_creation1.log
# rm -rf ProcessTransactions.log
# rm -rf CommitStorageForAddress.log
# rm -rf executeTransaction.log
# # ./genesis_run.sh
# echo "end"

# # move old data to a backup folder if it exists
# if [ -d "data" ]; then
#   mv data backup_$(date +%Y%m%d%H%M%S)
# fi

# # run fist time to create data and stop it to migrate genesis data
# echo "Starting the application to create data..."
# # Replace with the command to start your application
# nohup ./simple_chain &> /dev/null &
# echo "Application started."

# sleep 5

# echo "Stopping the application..."
# pkill -f simple_chain
# echo "Application stopped."


# echo "Migrating data ..."
# cd /Users/nguyennam/per/chain/mtn-simple/cmd/simple_chain/
# ./migrate_data account update -f  /Users/nguyennam/per/chain/mtn-simple/cmd/simple_chain/sample/simple/data-50.json
# ./migrate_data account update -f data-50.json
# echo "Data migrated."

# echo "Starting the application..."
# ./simple_chain
