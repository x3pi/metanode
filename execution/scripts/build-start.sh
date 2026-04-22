cd ./pkg/mvm
if [ $? != 0 ]; then  # Sửa cú pháp của điều kiện
    exit 1            # Sửa cú pháp của exit
fi                    # Sửa cú pháp của endif

rm -rf ./linker/build && rm -rf ./c_mvm/build
./build.sh
if [ $? != 0 ]; then  # Sửa cú pháp của điều kiện
    exit 1            # Sửa cú pháp của exit
fi                    # Sửa cú pháp của endif

cd ../../cmd/simple_chain
if [ $? != 0 ]; then  # Sửa cú pháp của điều kiện
    exit 1            # Sửa cú pháp của exit
fi                    # Sửa cú pháp của endif

make clean

make
if [ $? != 0 ]; then  # Sửa cú pháp của điều kiện
    exit 1            # Sửa cú pháp của exit
fi                    # Sửa cú pháp của endif

# Copy the bins to the sample directory
cp simple_chain sample/simple/
cp simple_chain sample/master-master/main/
cp simple_chain sample/master-master/sub/

cp migrate_data sample/simple/
cp migrate_data sample/master-master/main/
cp migrate_data sample/master-master/sub/

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

# move old data to a backup folder if it exists
if [ -d "data" ]; then
  mv data backup_$(date +%Y%m%d%H%M%S)
fi

# run fist time to create data and stop it to migrate genesis data
echo "Starting the application to create data..."
# Replace with the command to start your application
./simple_chain &> /dev/null &
echo "Application started."

sleep 5

echo "Stopping the application..."
pkill -f simple_chain
echo "Application stopped."


echo "Migrating data ..."
./migrate_data account update -f data-50.json
echo "Data migrated."

echo "Starting the application..."
./simple_chain  
