#!/bin/bash


# Chuyển đến thư mục pkg/mvm
cd ./cmd/simple_chain/cmd/migrate_data
if [ $? -ne 0 ]; then  # Sửa cú pháp của điều kiện
    exit 1            # Sửa cú pháp của exit
fi

go run . account update -f  ../../sample/data.json

echo "Data migrated."

