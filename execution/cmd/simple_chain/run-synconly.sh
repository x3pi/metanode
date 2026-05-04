#!/bin/bash
echo "Starting Synconly Node..."
./simple_chain --config=./config-master-synconly.json > logs/synconly.log 2>&1 &
echo "Synconly node started in background. Logs: logs/synconly.log"
