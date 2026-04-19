package abi

// CCGatewayABI là ABI của CrossChainGateway contract (v2)
// Chứa: lockAndBridge, sendMessage, và MessageSent event
// Dùng bởi CrossChainHandler để parse input data và emit events
const CCGatewayABI = `[
	{
		"inputs": [],
		"stateMutability": "nonpayable",
		"type": "constructor"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": false,
				"internalType": "uint256",
				"name": "confirmationLastBlock",
				"type": "uint256"
			},
			{
				"indexed": false,
				"internalType": "uint256",
				"name": "inboundLastBlock",
				"type": "uint256"
			}
		],
		"name": "ChannelStateSet",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": true,
				"internalType": "uint256",
				"name": "sourceNationId",
				"type": "uint256"
			},
			{
				"indexed": true,
				"internalType": "uint256",
				"name": "destNationId",
				"type": "uint256"
			},
			{
				"indexed": true,
				"internalType": "bytes32",
				"name": "msgId",
				"type": "bytes32"
			},
			{
				"indexed": false,
				"internalType": "enum CrossChainGateway.MessageType",
				"name": "msgType",
				"type": "uint8"
			},
			{
				"indexed": false,
				"internalType": "enum CrossChainGateway.MessageStatus",
				"name": "status",
				"type": "uint8"
			},
			{
				"indexed": false,
				"internalType": "bytes",
				"name": "returnData",
				"type": "bytes"
			},
			{
				"indexed": false,
				"internalType": "address",
				"name": "sender",
				"type": "address"
			},
			{
				"indexed": false,
				"internalType": "uint256",
				"name": "amount",
				"type": "uint256"
			}
		],
		"name": "MessageReceived",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": true,
				"internalType": "uint256",
				"name": "sourceNationId",
				"type": "uint256"
			},
			{
				"indexed": true,
				"internalType": "uint256",
				"name": "destNationId",
				"type": "uint256"
			},
			{
				"indexed": true,
				"internalType": "bytes32",
				"name": "msgId",
				"type": "bytes32"
			},
			{
				"indexed": false,
				"internalType": "bool",
				"name": "isEVM",
				"type": "bool"
			},
			{
				"indexed": false,
				"internalType": "address",
				"name": "sender",
				"type": "address"
			},
			{
				"indexed": false,
				"internalType": "address",
				"name": "target",
				"type": "address"
			},
			{
				"indexed": false,
				"internalType": "uint256",
				"name": "value",
				"type": "uint256"
			},
			{
				"indexed": false,
				"internalType": "bytes",
				"name": "payload",
				"type": "bytes"
			},
			{
				"indexed": false,
				"internalType": "uint256",
				"name": "timestamp",
				"type": "uint256"
			}
		],
		"name": "MessageSent",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": true,
				"internalType": "bytes32",
				"name": "msgId",
				"type": "bytes32"
			},
			{
				"indexed": true,
				"internalType": "address",
				"name": "sender",
				"type": "address"
			},
			{
				"indexed": false,
				"internalType": "enum CrossChainGateway.MessageType",
				"name": "msgType",
				"type": "uint8"
			},
			{
				"indexed": false,
				"internalType": "bool",
				"name": "isSuccess",
				"type": "bool"
			},
			{
				"indexed": false,
				"internalType": "uint256",
				"name": "amount",
				"type": "uint256"
			},
			{
				"indexed": false,
				"internalType": "bytes",
				"name": "reason",
				"type": "bytes"
			}
		],
		"name": "OutboundResult",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": true,
				"internalType": "address",
				"name": "previousOwner",
				"type": "address"
			},
			{
				"indexed": true,
				"internalType": "address",
				"name": "newOwner",
				"type": "address"
			}
		],
		"name": "OwnershipTransferred",
		"type": "event"
	},
	{
		"inputs": [
			{
				"components": [
					{
						"internalType": "enum CrossChainGateway.EventKind",
						"name": "eventKind",
						"type": "uint8"
					},
					{
						"internalType": "uint256",
						"name": "blockNumber",
						"type": "uint256"
					},
					{
						"components": [
							{
								"internalType": "uint256",
								"name": "sourceNationId",
								"type": "uint256"
							},
							{
								"internalType": "uint256",
								"name": "destNationId",
								"type": "uint256"
							},
							{
								"internalType": "uint256",
								"name": "timestamp",
								"type": "uint256"
							},
							{
								"internalType": "address",
								"name": "sender",
								"type": "address"
							},
							{
								"internalType": "address",
								"name": "target",
								"type": "address"
							},
							{
								"internalType": "uint256",
								"name": "value",
								"type": "uint256"
							},
							{
								"internalType": "bytes",
								"name": "payload",
								"type": "bytes"
							}
						],
						"internalType": "struct CrossChainGateway.CrossChainPacket",
						"name": "packet",
						"type": "tuple"
					},
					{
						"components": [
							{
								"internalType": "bytes32",
								"name": "messageId",
								"type": "bytes32"
							},
							{
								"internalType": "uint256",
								"name": "sourceBlockNumber",
								"type": "uint256"
							},
							{
								"internalType": "bool",
								"name": "isSuccess",
								"type": "bool"
							},
							{
								"internalType": "bytes",
								"name": "returnData",
								"type": "bytes"
							},
							{
								"internalType": "address",
								"name": "sender",
								"type": "address"
							},
							{
								"internalType": "uint256",
								"name": "value",
								"type": "uint256"
							}
						],
						"internalType": "struct CrossChainGateway.ConfirmationParam",
						"name": "confirmation",
						"type": "tuple"
					}
				],
				"internalType": "struct CrossChainGateway.EmbassyEvent[]",
				"name": "events",
				"type": "tuple[]"
			},
			{
				"internalType": "bytes",
				"name": "embassyPubKey",
				"type": "bytes"
			}
		],
		"name": "batchSubmit",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "confirmationLastProcessedBlock",
		"outputs": [
			{
				"internalType": "uint256",
				"name": "",
				"type": "uint256"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "inboundLastProcessedBlock",
		"outputs": [
			{
				"internalType": "uint256",
				"name": "",
				"type": "uint256"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "address",
				"name": "recipient",
				"type": "address"
			},
			{
				"internalType": "uint256",
				"name": "destinationId",
				"type": "uint256"
			}
		],
		"name": "lockAndBridge",
		"outputs": [],
		"stateMutability": "payable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "address",
				"name": "",
				"type": "address"
			}
		],
		"name": "lockedBalances",
		"outputs": [
			{
				"internalType": "uint256",
				"name": "",
				"type": "uint256"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "bytes32",
				"name": "",
				"type": "bytes32"
			}
		],
		"name": "messageExecuted",
		"outputs": [
			{
				"internalType": "bool",
				"name": "",
				"type": "bool"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "bytes32",
				"name": "",
				"type": "bytes32"
			}
		],
		"name": "outboundMessages",
		"outputs": [
			{
				"internalType": "bytes32",
				"name": "messageId",
				"type": "bytes32"
			},
			{
				"internalType": "address",
				"name": "sender",
				"type": "address"
			},
			{
				"internalType": "address",
				"name": "target",
				"type": "address"
			},
			{
				"internalType": "uint256",
				"name": "amount",
				"type": "uint256"
			},
			{
				"internalType": "enum CrossChainGateway.MessageType",
				"name": "msgType",
				"type": "uint8"
			},
			{
				"internalType": "bool",
				"name": "isConfirmed",
				"type": "bool"
			},
			{
				"internalType": "bool",
				"name": "isRefunded",
				"type": "bool"
			},
			{
				"internalType": "uint256",
				"name": "timestamp",
				"type": "uint256"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "owner",
		"outputs": [
			{
				"internalType": "address",
				"name": "",
				"type": "address"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "address",
				"name": "target",
				"type": "address"
			},
			{
				"internalType": "bytes",
				"name": "payload",
				"type": "bytes"
			},
			{
				"internalType": "uint256",
				"name": "destinationId",
				"type": "uint256"
			}
		],
		"name": "sendMessage",
		"outputs": [],
		"stateMutability": "payable",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "totalLocked",
		"outputs": [
			{
				"internalType": "uint256",
				"name": "",
				"type": "uint256"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"stateMutability": "payable",
		"type": "receive"
	}
]`
