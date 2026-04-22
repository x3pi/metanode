#!/bin/bash
# scripts/validate_cluster_state.sh
# Validates consensus parity across all 5 MetaNode instances

echo "========================================"
echo "🔍 MetaNode Cluster Consensus Validation"
echo "========================================"

NODES=(0 1 2 3 4)
RPC_PORTS=(10100 10111 10122 10133 10144)

# Function to get latest block number
get_latest_block() {
    local port=$1
    local res=$(curl -s -X POST -H "Content-Type: application/json" \
        --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
        http://127.0.0.1:$port)
    local hex=$(echo $res | jq -r '.result')
    if [ "$hex" == "null" ] || [ -z "$hex" ]; then
        echo "0"
    else
        printf "%d\n" $hex
    fi
}

# Function to get block hash at a specific height
get_block_hash() {
    local port=$1
    local height=$2
    local hex_height=$(printf "0x%x" $height)
    local res=$(curl -s -X POST -H "Content-Type: application/json" \
        --data "{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBlockByNumber\",\"params\":[\"$hex_height\", false],\"id\":1}" \
        http://127.0.0.1:$port)
    echo $res | jq -r '.result.hash'
}

# Function to get Global Exec Index
get_gei() {
    local port=$1
    local res=$(curl -s -X POST -H "Content-Type: application/json" \
        --data '{"jsonrpc":"2.0","method":"mtn_getGlobalExecIndex","params":[],"id":1}' \
        http://127.0.0.1:$port)
    echo $res | jq -r '.result'
}

echo -e "\n📊 Fetching node status..."
declare -A blocks
declare -A geis

max_block=0

for i in "${!NODES[@]}"; do
    node_id=${NODES[$i]}
    port=${RPC_PORTS[$i]}
    
    block=$(get_latest_block $port)
    gei=$(get_gei $port)
    blocks[$node_id]=$block
    geis[$node_id]=$gei
    
    if [ "$block" -gt "$max_block" ]; then
        max_block=$block
    fi
    
    echo "Node $node_id (Port $port): Block = $block, GEI = $gei"
done

echo -e "\n🔗 Verifying Block Hashes at recent heights..."
# We check at max_block, max_block-10, max_block-100 (if available)

check_heights=($max_block $((max_block-10)) $((max_block-100)))

for height in "${check_heights[@]}"; do
    if [ $height -le 0 ]; then continue; fi
    echo "Checking block hash at height #$height..."
    
    declare -A hashes
    divergence=0
    reference_hash=""
    
    for i in "${!NODES[@]}"; do
        node_id=${NODES[$i]}
        port=${RPC_PORTS[$i]}
        
        # Only check if node has reached this height
        if [ "${blocks[$node_id]}" -ge "$height" ]; then
            hash=$(get_block_hash $port $height)
            hashes[$node_id]=$hash
            
            if [ -z "$reference_hash" ]; then
                reference_hash=$hash
            elif [ "$hash" != "$reference_hash" ]; then
                divergence=1
            fi
            printf "  Node %d: %s\n" $node_id "$hash"
        else
            printf "  Node %d: Syncing (currently at %d)\n" $node_id ${blocks[$node_id]}
        fi
    done
    
    if [ $divergence -eq 1 ]; then
        echo "  ❌ DIVERGENCE DETECTED at block #$height!"
    else
        echo "  ✅ Consensus match at block #$height"
    fi
    echo ""
done

echo "Done."
