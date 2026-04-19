#!/bin/bash

# Tìm tất cả các tập tin .ts trong thư mục hiện tại
find . -name "*.ts" -print0 | while IFS= read -r -d $'\0' file; do
  # Chạy mỗi tập tin bằng npx tsx
  echo "Running: $file"
  npx tsx "$file"
done
