import sys
import json
import time
import glob
from pathlib import Path

try:
    from web3 import Web3
    from eth_account import Account
except ImportError:
    print("Please install web3: pip install web3 eth-account")
    sys.exit(1)

RPC_URL = "http://127.0.0.1:9099"
CHAIN_IDS = ["101", "102", "103", "104"]
GATEWAY_ADDR = Web3.to_checksum_address("0x0000000000000000000000000000000000001002")

ABI = """
[
    {
        "inputs": [
            {"internalType": "uint8", "name": "kind", "type": "uint8"},
            {"internalType": "bytes", "name": "payload", "type": "bytes"},
            {"internalType": "uint64", "name": "proposedAt", "type": "uint64"}
        ],
        "name": "propose",
        "outputs": [{"internalType": "bytes32", "name": "proposalId", "type": "bytes32"}],
        "stateMutability": "payable",
        "type": "function"
    }
]
"""

def main():
    w3 = Web3(Web3.HTTPProvider(RPC_URL))
    
    # Use the known dev account we just injected into the root anchor
    dev_priv_key = "0xd3ae7482f46f11cee2447bc711e9eb0fb79d4f2549781554cb962f54604e50f8"
        
    chain_id = w3.eth.chain_id
    print(f"Connected to Root Anchor (ChainID: {chain_id})")
    
    account = Account.from_key(dev_priv_key)
    gateway = w3.eth.contract(address=GATEWAY_ADDR, abi=json.loads(ABI))
    
    nonce = w3.eth.get_transaction_count(account.address)
    
    for cid_str in CHAIN_IDS:
        print(f"Registering chain {cid_str}...")
        
        # Read genesis to find validators
        genesis_path = Path(__file__).parent / f"private_chains_data/chain_{cid_str}/genesis.json"
            
        with open(genesis_path, "r") as f:
            gen = json.load(f)
            
        committee = []
        total_stake = 0
        for v in gen.get("validators", []):
            auth_key = v.get("authority_key", "")
            import base64
            bls_bytes = base64.b64decode(auth_key)
            bls_hex = "0x" + bls_bytes.hex()
            
            stake = 0
            if v.get("delegator_stakes"):
                stake = int(v["delegator_stakes"][0]["amount"])
                
            committee.append({
                "pubkey_bls": bls_bytes.hex(),
                "stake": stake
            })
            total_stake += stake
            
        quorum = (total_stake * 2 // 3) + 1
        
        registry = {
            "chain_id": int(cid_str),
            "epoch": 0,
            "committee": committee,
            "quorum_threshold": quorum,
            "gateway_contract": "0x0000000000000000000000000000000000000000",
            "state_root": "0000000000000000000000000000000000000000000000000000000000000000",
            "account_tree_root": "0000000000000000000000000000000000000000000000000000000000000000",
            "archival_endpoint": "",
            "registered_at": 0
        }
        
        payload_bytes = json.dumps(registry).encode('utf-8')
        now = int(time.time() * 1000)
        
        current_nonce = w3.eth.get_transaction_count(account.address)
        print(f"  Using nonce: {current_nonce}")
        
        tx = gateway.functions.propose(
            0, # ProposalRegisterChain (0)
            payload_bytes,
            now
        ).build_transaction({
            'from': account.address,
            'nonce': current_nonce,
            'gas': 2000000,
            'gasPrice': w3.eth.gas_price or 1000000000,
            'value': 100000000000000000, # 0.1 MTN
            'chainId': chain_id
        })
        
        signed_tx = w3.eth.account.sign_transaction(tx, private_key=dev_priv_key)
        tx_hash = w3.eth.send_raw_transaction(signed_tx.raw_transaction)
        print(f"  Tx sent! Hash: {tx_hash.hex()}")
        
        # Wait for receipt
        print(f"  Waiting for confirmation...")
        for _ in range(15):
            time.sleep(1)
            try:
                receipt = w3.eth.get_transaction_receipt(tx_hash)
                if receipt is not None:
                    print(f"  ✅ Chain {cid_str} registered successfully in block #{receipt.blockNumber} (status={receipt.status})")
                    break
            except Exception:
                pass
        else:
            print(f"  ⚠️ Warning: confirmation timed out for chain {cid_str}")

if __name__ == "__main__":
    main()
