package abi_file

const FileABI = `[
	{
		"inputs": [],
		"stateMutability": "payable",
		"type": "constructor"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": false,
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			},
			{
				"indexed": false,
				"internalType": "uint256",
				"name": "chunkIndex",
				"type": "uint256"
			}
		],
		"name": "ChunkUploaded",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": false,
				"internalType": "bytes32",
				"name": "downloadKey",
				"type": "bytes32"
			},
			{
				"indexed": false,
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			}
		],
		"name": "DownloadKeyConfirmed",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": false,
				"internalType": "bytes32",
				"name": "downloadKey",
				"type": "bytes32"
			},
			{
				"indexed": false,
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			},
			{
				"indexed": false,
				"internalType": "address",
				"name": "user",
				"type": "address"
			},
			{
				"indexed": false,
				"internalType": "uint256",
				"name": "amount",
				"type": "uint256"
			}
		],
		"name": "DownloadKeyGenerated",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": false,
				"internalType": "address",
				"name": "user",
				"type": "address"
			},
			{
				"indexed": false,
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			}
		],
		"name": "FileActivated",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": false,
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			},
			{
				"indexed": false,
				"internalType": "string",
				"name": "name",
				"type": "string"
			},
			{
				"indexed": false,
				"internalType": "uint64",
				"name": "contentLen",
				"type": "uint64"
			}
		],
		"name": "FileAdded",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": false,
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			}
		],
		"name": "FileDeleted",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": false,
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			}
		],
		"name": "FileLocked",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": false,
				"internalType": "address",
				"name": "owner",
				"type": "address"
			},
			{
				"indexed": false,
				"internalType": "uint256",
				"name": "amount",
				"type": "uint256"
			}
		],
		"name": "FundsWithdrawn",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": false,
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			},
			{
				"indexed": false,
				"internalType": "address",
				"name": "payer",
				"type": "address"
			},
			{
				"indexed": false,
				"internalType": "uint256",
				"name": "amount",
				"type": "uint256"
			},
			{
				"indexed": false,
				"internalType": "uint256",
				"name": "downloadCount",
				"type": "uint256"
			}
		],
		"name": "PaymentReceived",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{
				"indexed": false,
				"internalType": "bytes32",
				"name": "downloadKey",
				"type": "bytes32"
			},
			{
				"indexed": false,
				"internalType": "address",
				"name": "storageServer",
				"type": "address"
			},
			{
				"indexed": false,
				"internalType": "uint256",
				"name": "currentConfirmations",
				"type": "uint256"
			}
		],
		"name": "StorageConfirmed",
		"type": "event"
	},
	{
		"inputs": [],
		"name": "CONTRACT_IDENTIFIER",
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
				"name": "_owner",
				"type": "address"
			}
		],
		"name": "addOwner",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "address",
				"name": "_server",
				"type": "address"
			}
		],
		"name": "addStorageServer",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "address",
				"name": "_validator",
				"type": "address"
			}
		],
		"name": "addValidator",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "uint256",
				"name": "numChunks",
				"type": "uint256"
			}
		],
		"name": "calculatePrice",
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
				"name": "fileKey",
				"type": "bytes32"
			}
		],
		"name": "confirmFileActive",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "bytes32",
				"name": "downloadKey",
				"type": "bytes32"
			}
		],
		"name": "confirmServerDownload",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			}
		],
		"name": "deleteFile",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			},
			{
				"internalType": "uint256",
				"name": "start",
				"type": "uint256"
			},
			{
				"internalType": "uint256",
				"name": "limit",
				"type": "uint256"
			}
		],
		"name": "downloadFile",
		"outputs": [
			{
				"internalType": "bytes[]",
				"name": "",
				"type": "bytes[]"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "getContractBalance",
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
				"name": "downloadKey",
				"type": "bytes32"
			}
		],
		"name": "getDownloadSessionInfo",
		"outputs": [
			{
				"components": [
					{
						"internalType": "bytes32",
						"name": "fileKey",
						"type": "bytes32"
					},
					{
						"internalType": "address",
						"name": "user",
						"type": "address"
					},
					{
						"internalType": "address[]",
						"name": "confirmations",
						"type": "address[]"
					},
					{
						"internalType": "bool",
						"name": "isConfirmed",
						"type": "bool"
					}
				],
				"internalType": "struct DownloadSession",
				"name": "",
				"type": "tuple"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			}
		],
		"name": "getFileInfo",
		"outputs": [
			{
				"components": [
					{
						"internalType": "address",
						"name": "owner",
						"type": "address"
					},
					{
						"internalType": "bytes32",
						"name": "merkleRoot",
						"type": "bytes32"
					},
					{
						"internalType": "uint64",
						"name": "contentLen",
						"type": "uint64"
					},
					{
						"internalType": "uint64",
						"name": "totalChunks",
						"type": "uint64"
					},
					{
						"internalType": "uint64",
						"name": "expireTime",
						"type": "uint64"
					},
					{
						"internalType": "string",
						"name": "name",
						"type": "string"
					},
					{
						"internalType": "string",
						"name": "ext",
						"type": "string"
					},
					{
						"internalType": "string",
						"name": "contentDisposition",
						"type": "string"
					},
					{
						"internalType": "string",
						"name": "contentID",
						"type": "string"
					},
					{
						"internalType": "enum FileStatus",
						"name": "status",
						"type": "uint8"
					}
				],
				"internalType": "struct Info",
				"name": "",
				"type": "tuple"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "string[]",
				"name": "names",
				"type": "string[]"
			}
		],
		"name": "getFileKeyFromName",
		"outputs": [
			{
				"internalType": "bytes32[]",
				"name": "",
				"type": "bytes32[]"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			}
		],
		"name": "getFileProgress",
		"outputs": [
			{
				"components": [
					{
						"internalType": "bytes32",
						"name": "lastChunkHash",
						"type": "bytes32"
					},
					{
						"internalType": "uint64",
						"name": "processedChunks",
						"type": "uint64"
					},
					{
						"internalType": "uint256",
						"name": "processedLength",
						"type": "uint256"
					}
				],
				"internalType": "struct FileProgress",
				"name": "",
				"type": "tuple"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "bytes32[]",
				"name": "fileKeys",
				"type": "bytes32[]"
			}
		],
		"name": "getFilesInfo",
		"outputs": [
			{
				"components": [
					{
						"internalType": "address",
						"name": "owner",
						"type": "address"
					},
					{
						"internalType": "bytes32",
						"name": "merkleRoot",
						"type": "bytes32"
					},
					{
						"internalType": "uint64",
						"name": "contentLen",
						"type": "uint64"
					},
					{
						"internalType": "uint64",
						"name": "totalChunks",
						"type": "uint64"
					},
					{
						"internalType": "uint64",
						"name": "expireTime",
						"type": "uint64"
					},
					{
						"internalType": "string",
						"name": "name",
						"type": "string"
					},
					{
						"internalType": "string",
						"name": "ext",
						"type": "string"
					},
					{
						"internalType": "string",
						"name": "contentDisposition",
						"type": "string"
					},
					{
						"internalType": "string",
						"name": "contentID",
						"type": "string"
					},
					{
						"internalType": "enum FileStatus",
						"name": "status",
						"type": "uint8"
					}
				],
				"internalType": "struct Info[]",
				"name": "infos",
				"type": "tuple[]"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "getOwnerList",
		"outputs": [
			{
				"internalType": "address[]",
				"name": "",
				"type": "address[]"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "getRustServerAddresses",
		"outputs": [
			{
				"internalType": "string[]",
				"name": "",
				"type": "string[]"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "getStorageServerList",
		"outputs": [
			{
				"internalType": "address[]",
				"name": "",
				"type": "address[]"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "getValidatorList",
		"outputs": [
			{
				"internalType": "address[]",
				"name": "",
				"type": "address[]"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "address",
				"name": "_address",
				"type": "address"
			}
		],
		"name": "isOwner",
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
				"internalType": "address",
				"name": "_address",
				"type": "address"
			}
		],
		"name": "isStorageServer",
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
				"internalType": "address",
				"name": "_address",
				"type": "address"
			}
		],
		"name": "isValidator",
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
				"name": "fileKey",
				"type": "bytes32"
			}
		],
		"name": "lockFile",
		"outputs": [],
		"stateMutability": "nonpayable",
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
		"name": "mDownloadKeyToSession",
		"outputs": [
			{
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			},
			{
				"internalType": "address",
				"name": "user",
				"type": "address"
			},
			{
				"internalType": "bool",
				"name": "isConfirmed",
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
		"name": "mKeyToFileInfo",
		"outputs": [
			{
				"components": [
					{
						"internalType": "address",
						"name": "owner",
						"type": "address"
					},
					{
						"internalType": "bytes32",
						"name": "merkleRoot",
						"type": "bytes32"
					},
					{
						"internalType": "uint64",
						"name": "contentLen",
						"type": "uint64"
					},
					{
						"internalType": "uint64",
						"name": "totalChunks",
						"type": "uint64"
					},
					{
						"internalType": "uint64",
						"name": "expireTime",
						"type": "uint64"
					},
					{
						"internalType": "string",
						"name": "name",
						"type": "string"
					},
					{
						"internalType": "string",
						"name": "ext",
						"type": "string"
					},
					{
						"internalType": "string",
						"name": "contentDisposition",
						"type": "string"
					},
					{
						"internalType": "string",
						"name": "contentID",
						"type": "string"
					},
					{
						"internalType": "enum FileStatus",
						"name": "status",
						"type": "uint8"
					}
				],
				"internalType": "struct Info",
				"name": "info",
				"type": "tuple"
			},
			{
				"components": [
					{
						"internalType": "bytes32",
						"name": "lastChunkHash",
						"type": "bytes32"
					},
					{
						"internalType": "uint64",
						"name": "processedChunks",
						"type": "uint64"
					},
					{
						"internalType": "uint256",
						"name": "processedLength",
						"type": "uint256"
					}
				],
				"internalType": "struct FileProgress",
				"name": "progress",
				"type": "tuple"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "string",
				"name": "",
				"type": "string"
			}
		],
		"name": "mNameToFileKey",
		"outputs": [
			{
				"internalType": "bytes32",
				"name": "",
				"type": "bytes32"
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
				"internalType": "uint256",
				"name": "",
				"type": "uint256"
			}
		],
		"name": "ownerList",
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
				"name": "",
				"type": "address"
			}
		],
		"name": "owners",
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
				"name": "fileKey",
				"type": "bytes32"
			},
			{
				"internalType": "uint256",
				"name": "downloadTimes",
				"type": "uint256"
			}
		],
		"name": "payForDownload",
		"outputs": [],
		"stateMutability": "payable",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "pricePerChunk",
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
				"components": [
					{
						"internalType": "address",
						"name": "owner",
						"type": "address"
					},
					{
						"internalType": "bytes32",
						"name": "merkleRoot",
						"type": "bytes32"
					},
					{
						"internalType": "uint64",
						"name": "contentLen",
						"type": "uint64"
					},
					{
						"internalType": "uint64",
						"name": "totalChunks",
						"type": "uint64"
					},
					{
						"internalType": "uint64",
						"name": "expireTime",
						"type": "uint64"
					},
					{
						"internalType": "string",
						"name": "name",
						"type": "string"
					},
					{
						"internalType": "string",
						"name": "ext",
						"type": "string"
					},
					{
						"internalType": "string",
						"name": "contentDisposition",
						"type": "string"
					},
					{
						"internalType": "string",
						"name": "contentID",
						"type": "string"
					},
					{
						"internalType": "enum FileStatus",
						"name": "status",
						"type": "uint8"
					}
				],
				"internalType": "struct Info",
				"name": "info",
				"type": "tuple"
			}
		],
		"name": "pushFileInfo",
		"outputs": [
			{
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			}
		],
		"stateMutability": "payable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "address",
				"name": "_owner",
				"type": "address"
			}
		],
		"name": "removeOwner",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "address",
				"name": "_server",
				"type": "address"
			}
		],
		"name": "removeStorageServer",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "address",
				"name": "_validator",
				"type": "address"
			}
		],
		"name": "removeValidator",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "bytes32",
				"name": "fileKey",
				"type": "bytes32"
			},
			{
				"internalType": "uint64",
				"name": "_newExpireTime",
				"type": "uint64"
			}
		],
		"name": "renewTime",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "uint256",
				"name": "",
				"type": "uint256"
			}
		],
		"name": "rustServerAddresses",
		"outputs": [
			{
				"internalType": "string",
				"name": "",
				"type": "string"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "service",
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
				"internalType": "uint256",
				"name": "_newPrice",
				"type": "uint256"
			}
		],
		"name": "setPricePerChunk",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "string[]",
				"name": "_addresses",
				"type": "string[]"
			}
		],
		"name": "setRustServerAddresses",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "uint256",
				"name": "",
				"type": "uint256"
			}
		],
		"name": "storageServerList",
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
				"name": "",
				"type": "address"
			}
		],
		"name": "storageServers",
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
				"name": "fileKey",
				"type": "bytes32"
			},
			{
				"internalType": "bytes",
				"name": "chunkData",
				"type": "bytes"
			},
			{
				"internalType": "uint256",
				"name": "chunkIndex",
				"type": "uint256"
			},
			{
				"internalType": "bytes32[]",
				"name": "merkleProof",
				"type": "bytes32[]"
			}
		],
		"name": "uploadChunk",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "uint256",
				"name": "",
				"type": "uint256"
			}
		],
		"name": "validatorList",
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
				"name": "",
				"type": "address"
			}
		],
		"name": "validators",
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
				"internalType": "uint256",
				"name": "amount",
				"type": "uint256"
			}
		],
		"name": "withdrawAmount",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	},
	{
		"inputs": [],
		"name": "withdrawFunds",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	}
]`
