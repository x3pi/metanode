#!/bin/bash
sed -i 's|// case "eth_getTransactionCount":|case "eth_getTransactionCount":|g' /home/abc/nhat/consensus-chain/metanode/execution/cmd/rpc/cmd/rpc-client/internal/proxy/http_handler.go
