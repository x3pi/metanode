#!/bin/bash
set -euo pipefail

# cd 3rdparty
# bash build.sh
# cd ..

cd c_mvm
mkdir -p build 
cd build
cmake -DCMAKE_EXPORT_COMPILE_COMMANDS=ON ../ 
make -j$(nproc) install
cp compile_commands.json ../

cd ../../linker
mkdir -p build 
cd build
if [ "${ENABLE_DEBUG_CPP:-false}" = "true" ]; then
    echo "🔨 Building MVM Linker in DEBUG mode"
    cmake -DCMAKE_EXPORT_COMPILE_COMMANDS=ON -DENABLE_DEBUG=ON ..
else
    cmake -DCMAKE_EXPORT_COMPILE_COMMANDS=ON ..
fi
make -j$(nproc) install
cp compile_commands.json ../
