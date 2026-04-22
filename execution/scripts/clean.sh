cd ./pkg/mvm
if [ $? != 0 ]; then  # Sửa cú pháp của điều kiện
    exit 1            # Sửa cú pháp của exit
fi                    # Sửa cú pháp của endif

rm -rf ./linker/build && rm -rf ./c_mvm/build 
# ./build.sh
if [ $? != 0 ]; then  # Sửa cú pháp của điều kiện
    exit 1            # Sửa cú pháp của exit
fi                    # Sửa cú pháp của endif

cd ../../cmd/simple_chain
if [ $? != 0 ]; then  # Sửa cú pháp của điều kiện
    exit 1            # Sửa cú pháp của exit
fi                    # Sửa cú pháp của endif

make clean

# make
if [ $? != 0 ]; then  # Sửa cú pháp của điều kiện
    exit 1            # Sửa cú pháp của exit
fi                    # Sửa cú pháp của endif

# ./copy_bins_to_sample.sh
if [ $? != 0 ]; then  # Sửa cú pháp của điều kiện
    exit 1            # Sửa cú pháp của exit
fi                    # Sửa cú pháp của endif

cd ./sample/simple
rm -rf contract_creation.log 
rm -rf contract_creation2.log 
rm -rf contract_creation1.log
rm -rf ProcessTransactions.log
rm -rf CommitStorageForAddress.log
rm -rf executeTransaction.log
# ./genesis_run.sh
echo "end"