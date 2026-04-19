#!/bin/bash

if [ -z "$1" ]; then
  echo "❌ Lỗi: Bạn chưa cung cấp đường dẫn file .sol"
  echo "👉 Cú pháp: ./build_contract.sh <đường/dẫn/tới/file.sol>"
  echo "👉 Ví dụ:   ./build_contract.sh contract/demo-test.sol"
  exit 1
fi

SOL_FILE="$1"

if [ ! -f "$SOL_FILE" ]; then
  echo "❌ Lỗi: File '$SOL_FILE' không tồn tại!"
  exit 1
fi

# Phân tích đường dẫn file
FILENAME=$(basename -- "$SOL_FILE")
DIRNAME=$(dirname -- "$SOL_FILE")
BASENAME="${FILENAME%.*}"

# Khởi tạo thư mục build nằm cùng cấp với file sol
BUILD_DIR="${DIRNAME}/build"
mkdir -p "$BUILD_DIR"

echo "⏳ Đang compile '$SOL_FILE' bằng solc@0.8.20..."

# Sử dụng solc qua Node.js để có thể truyền tham số evmVersion: london (do solcjs không hỗ trợ cờ CLI này)
cat << 'EOF' > "$BUILD_DIR/compile.js"
const fs = require('fs');
const solc = require('solc');
const path = require('path');

const solFile = process.argv[2];
const buildDir = process.argv[3];
const sourceCode = fs.readFileSync(solFile, 'utf8');

const input = {
    language: 'Solidity',
    sources: { [path.basename(solFile)]: { content: sourceCode } },
    settings: {
        evmVersion: 'london', // FIX OPCODES ERRORS (NO PUSH0)
        outputSelection: { '*': { '*': ['abi', 'evm.bytecode.object'] } }
    }
};

const output = JSON.parse(solc.compile(JSON.stringify(input)));

if (output.errors) {
    let hasError = false;
    output.errors.forEach(err => {
        console.error(err.formattedMessage);
        if (err.severity === 'error') hasError = true;
    });
    if (hasError) process.exit(1);
}

for (let file in output.contracts) {
    for (let contractName in output.contracts[file]) {
        const contract = output.contracts[file][contractName];
        const abiPath = path.join(buildDir, file.replace('.sol', '') + "_" + contractName + ".abi");
        const binPath = path.join(buildDir, file.replace('.sol', '') + "_" + contractName + ".bin");
        
        fs.writeFileSync(abiPath, JSON.stringify(contract.abi, null, 2));
        fs.writeFileSync(binPath, contract.evm.bytecode.object);
    }
}
EOF

# Chạy trình biên dịch
if [ ! -d "node_modules/solc" ]; then
    echo "📦 Đang cài đặt tạm thư viện solc@0.8.20..."
    npm install solc@0.8.20 --no-save > /dev/null 2>&1
fi

node "$BUILD_DIR/compile.js" "$SOL_FILE" "$BUILD_DIR"

if [ $? -eq 0 ]; then
  echo ""
  echo "============================================================"
  echo "🎉 BIÊN DỊCH THÀNH CÔNG!"
  echo "📁 Thư mục output: $BUILD_DIR"
  echo "============================================================"
  
  # Tạo thêm file .json từ .abi theo yêu cầu của Go
  for abi_file in "$BUILD_DIR"/*.abi; do
    if [ -f "$abi_file" ]; then
      cp "$abi_file" "${abi_file%.abi}.json"
    fi
  done

  # Tìm và in nhanh nội dung file .bin (Bytecode) ra cho người dùng tiện Copy
  BIN_FILE=$(ls "$BUILD_DIR"/*.bin 2>/dev/null | head -n 1)
  if [ -f "$BIN_FILE" ]; then
     echo "📌 BYTECODE (Copy cục Hex phía dưới dán vào config nhé):"
     echo ""
     cat "$BIN_FILE"
     echo ""
     echo ""
  fi
else
  echo "❌ Lỗi: Quá trình biên dịch thất bại."
  exit 1
fi
