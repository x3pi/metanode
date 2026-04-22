#!/bin/bash

echo "Testing Ping functionality..."

# Start observer in background
echo "Starting observer..."
cd /home/abc/nhat/chain-1/cmd/observer
./observer -config=config.json > observer.log 2>&1 &
OBSERVER_PID=$!

# Wait for observer to start
sleep 3

# Test ping
# echo "Testing ping..."
# if [ -f "test_client/test_client" ]; then
#     ./test_client/test_client -mode ping -addr 127.0.0.1:4900 -ping-msg "Hello Observer!"
# else
#     echo "test_client not found, building..."
#     cd test_client && go build . && cd .. && ./test_client/test_client -mode ping -addr 127.0.0.1:4900 -ping-msg "Hello Observer!"
# fi
cd test_client && go run main.go -mode ping -addr 127.0.0.1:4900 -ping-msg "Hello Observer!"
# Wait a bit
sleep 2

# Kill observer
echo "Stopping observer..."
kill $OBSERVER_PID 2>/dev/null
wait $OBSERVER_PID 2>/dev/null

echo "Test completed."
