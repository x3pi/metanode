#!/bin/bash
echo "Testing TPS Blast with 10k transactions to verify zero-fork..."
./tps_blast -config ./config.json -node "127.0.0.1:4201" -count 10000 -batch 500 -sleep 1 -rpc "127.0.0.1:8757" -skip-verify -no-wait
