
# cd 3rdparty
# bash build.sh
# cd ..

cd c_mvm
mkdir -p build 
cd build
cmake ../ 
make -j$(nproc) install

cd ../../linker
mkdir -p build 
cd build
cmake ..
make -j$(nproc) install
 
