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
cmake -DCMAKE_EXPORT_COMPILE_COMMANDS=ON ..
make -j$(nproc) install
cp compile_commands.json ../
