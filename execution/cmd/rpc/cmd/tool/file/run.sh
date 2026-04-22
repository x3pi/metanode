#!/usr/bin/env bash
set -euo pipefail

ROOT="/home/abc/nhat/mtn-simple-2025"
INPUT="/tmp/demo_2g.bin"
OUTPUT="/tmp/demo_out_2g.bin"
CHUNK_SIZE=$((200 * 1024))
PARALLEL=1000
REPORT_INTERVAL="1s"

echo "==> Tạo file nguồn 2GB tại ${INPUT}"
dd if=/dev/urandom of="${INPUT}" bs=1M count=2048 status=progress

echo "==> Chạy chương trình demo với ${PARALLEL} luồng, chunk ${CHUNK_SIZE} bytes"
cd "${ROOT}/cmd/tool/file"
go run . \
  -input="${INPUT}" \
  -output="${OUTPUT}" \
  -chunk-size="${CHUNK_SIZE}" \
  -parallel="${PARALLEL}" \
  -report-interval="${REPORT_INTERVAL}"

echo "==> Hoàn tất. File nhận: ${OUTPUT}"