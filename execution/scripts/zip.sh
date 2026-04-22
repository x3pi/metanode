#!/bin/bash

# Chuyển đến thư mục chứa script
cd "$(dirname "$0")" || exit 1

# Lấy tên thư mục hiện tại
base_name=$(basename "$PWD")

# Lấy ngày tháng năm hiện tại
date=$(date +"%Y-%b-%d" | tr '[:upper:]' '[:lower:]')

# Kiểm tra tham số dòng lệnh
if [[ $# -eq 0 ]]; then
  echo "Vui lòng chỉ định danh sách loại trừ: client hoặc default" >&2
  exit 1
fi

exclude_list_type="$1"

# Đặt tên file dựa trên tùy chọn
archive_name="$base_name"
if [[ "$exclude_list_type" == "client" ]]; then
  archive_name="client"
elif [[ "$exclude_list_type" == "web3" ]]; then
  archive_name="web3"
elif [[ "$exclude_list_type" == "rpc" ]]; then
  archive_name="rpc"
fi
# Tìm số lớn nhất của file đã có
existing_files=$(ls "${archive_name}-${date}-"*.tar.gz 2>/dev/null)
max_number=0

for file in $existing_files; do
  number=$(echo "$file" | sed -E "s/.*-${date}-([0-9]+)\.tar\.gz/\1/")
  if [[ -n "$number" && "$number" -gt "$max_number" ]]; then
    max_number="$number"
  fi
done

# Kiểm tra giới hạn
if ((max_number >= 99)); then
  echo "Lỗi: Đã đạt giới hạn số file (99) cho ngày ${date}." >&2
  exit 1
fi

# Tăng số mới
next_number=$(printf "%02d" $((max_number + 1)))
new_file="${archive_name}-${date}-${next_number}.tar.gz"

declare -a exclude_list_rpc=(
  "./cmd/client" "*.git" "*.gz" "*.dat" "*.log"
  "./cmd/simple_chain/sample/simple"
  "./pkg/mvm/c_mvm/build" "./pkg/mvm/linker/build"
  "./cmd/test" "./cmd/rpc-client/privatekey_db_encrypted"
  "./cmd/client"
  "./cmd/simple_chain"
  "./cmd/tool"
  "./cmd/consensus"
  "./contracts"
  "./docs"
  "./cmd/proxy"
  "./pkg/mvm/3rdparty"
  "./pkg/mvm/linker/include/config.h"
  "./web3"
  "./cmd/simple_chain/cmd/migrate_data/BatchPut_0"
  "./contracts/ecom"
  "./cmd/simple_chain/cmd/migrate_data"
  "./cmd/simple_chain/xapian"
  "TestChain"
  "build_app"
  "node_logs"
  "cmd/simple_chain/simple_chain"
  "pkg/mvm/c_mvm/3rdparty/intx/build"
  "./build"
  "./pkg/trie_databse"

  "./pkg/trie"
  "./pkg/node"
  "./pkg/node_consensus"
  "./pkg/pack"
  "./pkg/transaction_grouper"
  "./pkg/account_state_db"
  "./pkg/block"
  "./pkg/blockchain"
  "./pkg/error_tx_manager"
  "./pkg/filters"
  "./pkg/grouptxns"
  "cmd/storage"
  "pkg/rust_adder"
  "pkg/qmdb"
  "./cmd/exec_node/genesis.json"
  "cmd/demo"
)

# Khởi tạo danh sách loại trừ
declare -a exclude_list_client=(
  "*.git" "*.gz" "*.dat" "*.log"
  "./cmd/rpc-client" "./cmd/simple_chain" "./cmd/validator"
  "./docs" "./cmd/simple_chain/sample/simple"
  "./pkg/mvm/c_mvm/build" "./pkg/mvm/linker/build"
  "./cmd/test" "./cmd/client/client_cli/client_cli"
  "./cmd/client/client_cli" "./cmd/client/call_tool_test_parallel"
  "./cmd/client/bsl_test" "./cmd/client/convert"
  "./pkg/mvm"
  "web3"
  "./cmd/simple_chain/cmd/migrate_data/BatchPut_0"
  "./contracts/ecom"
  "TestChain"
  "build_app"
  "node_logs"
  "./build"
  "./cmd/client/call_tool_test/call_tool_test"
  "./cmd/client/add_account_tx_0/add_account_tx_0"
  "./cmd/client/add_account/add_account"
   "*.vscode"
  "*.cursor"
  "cmd/storage"
  "cmd/storage_client"
  "demo"
  "cmd/simple_chain/genesis.json"
  "pkg/rust_adder"
  "pkg/qmdb"
  "./cmd/exec_node/genesis.json"
  "cmd/demo"

)

declare -a exclude_list_default=(
  "./cmd/client" "*.git" "*.gz" "*.dat" "*.log"
  "./cmd/simple_chain/sample/simple"
  "./pkg/mvm/c_mvm/build" "./pkg/mvm/linker/build"
  "./cmd/test" "./cmd/rpc-client/rpc-client"
  "./pkg/mvm/3rdparty"
  "./pkg/mvm/linker/include/config.h"
  "./web3"
  "./cmd/simple_chain/cmd/migrate_data/BatchPut_0"
  "./contracts/ecom"
  "./cmd/simple_chain/cmd/migrate_data"
  "./cmd/simple_chain/xapian"
  "TestChain"
  "build_app"
  "node_logs"
  "cmd/simple_chain/simple_chain"
  "pkg/mvm/c_mvm/3rdparty/intx/build"
  "./build"
  "*.vscode"
  "*.cursor"
  "cmd/storage/db_data"
  "cmd/explorer"
  "cmd/storage"
  "cmd/storage_client"
  "demo"
  "cmd/simple_chain/genesis.json"
  "pkg/rust_adder"
  "pkg/qmdb"
  "./cmd/exec_node/genesis.json"
  "cmd/demo"
  "target"
)

# Thêm tùy chọn nén riêng thư mục web3
declare -a exclude_list_web3=(
  "./cmd"
  "./contracts"
  "./docs"
  "./types"
  "./pkg"
  "*.git" "*.gz" "*.dat" "*.log"
  "./web3/node_modules"
  "./migrate.sh"
  "./clean.sh"
  "./bulid.sh"
  "./bulid-start.sh"
  "./install-rocksdb.sh"
  "./go.sum"
  "./go.mod"
  "*.js" # Loại trừ các file .js
  "./cmd/simple_chain/cmd/migrate_data/BatchPut_0"
  "./contracts/ecom"
  ".cmd/simple_chain/xapian"
  "node_modules"
  "TestChain"
  "build_app"
  "node_logs"
  "./build"
   "*.vscode"
  "*.cursor"
  "demo"
  "cmd/simple_chain/genesis.json"
  "pkg/rust_adder"
  "pkg/qmdb"
  "./cmd/exec_node/genesis.json"
  "cmd/demo"
  "web3/dapp/register-private-key-rpc"
)

# Chọn danh sách loại trừ dựa trên tham số
if [[ "$exclude_list_type" == "client" ]]; then
  exclude_list=("${exclude_list_client[@]}")
elif [[ "$exclude_list_type" == "default" ]]; then
  exclude_list=("${exclude_list_default[@]}")
elif [[ "$exclude_list_type" == "web3" ]]; then
  exclude_list=("${exclude_list_web3[@]}")
elif [[ "$exclude_list_type" == "rpc" ]]; then
  exclude_list=("${exclude_list_rpc[@]}")
else
  echo "Tham số không hợp lệ. Vui lòng nhập 'client', 'default' hoặc 'web3'." >&2
  exit 1
fi

# Tìm các file thực thi không có đuôi mở rộng hoặc có đuôi .sh và loại trừ
# while IFS= read -r -d $'\0' executable; do
#   if [[ ! "$executable" == *.go ]] && [[ ! "$executable" == *.sh ]]; then # Sửa đổi ở đây
#     exclude_list+=("$executable")
#   fi
# done < <(find . -type f -executable -print0)

# Tạo tham số --exclude cho tar
exclude_params=()
for exclude in "${exclude_list[@]}"; do
  exclude_params+=(--exclude="$exclude")
done

# Tạo file tar.gz mới
if ! tar -czvf "$new_file" "${exclude_params[@]}" .; then
  echo "Lỗi khi tạo file tar.gz." >&2
  exit 1
fi

# Lấy kích thước file sau khi tạo
if [[ -f "$new_file" ]]; then
  if [[ "$OSTYPE" == "darwin"* ]]; then
    file_size=$(stat -f%z "$new_file" 2>/dev/null || wc -c <"$new_file")
  else
    file_size=$(stat -c%s "$new_file" 2>/dev/null || wc -c <"$new_file")
  fi
else
  echo "Lỗi: File không tồn tại." >&2
  exit 1
fi

# Kiểm tra và hiển thị kích thước file
if [[ -n "$file_size" && "$file_size" -gt 0 ]]; then
  echo "File nén đã được tạo: $new_file"
  echo "Kích thước file: $(numfmt --to=iec "$file_size")"
else
  echo "Lỗi: Không thể lấy kích thước file." >&2
  exit 1
fi
