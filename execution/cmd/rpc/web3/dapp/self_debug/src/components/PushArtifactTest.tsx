import { useState } from 'react';
import { GO_BACKEND_RPC_URL } from '../constants/customChain';

interface PushArtifactParams {
  contract_address: string;
  metadata: string;
  source_code: string;
  source_map: string;
  storage_layout: string;
}

interface SourceFile {
  filename: string;
  content: string;
}

interface JSONRPCResponse {
  jsonrpc: string;
  result?: unknown;
  error?: {
    code: number;
    message: string;
    data?: unknown;
  };
  id: number;
}

const sourceCodeObj = {
    "test.sol": `// SPDX-License-Identifier: MIT
pragma solidity ^0.8.30;

error InsufficientBalance(uint256 available, uint256 required);

contract TestDebug {
    uint256 public balance;
    uint256[] public array;

    enum Action { None, Start, Stop }

    // --- CUSTOM ERROR ---
    function testCustomError(uint256 amount) public pure {
        uint256 currentBalance = 50;
        if (amount > currentBalance) {
            // Sẽ trả về Error Selector của InsufficientBalance
            revert InsufficientBalance(currentBalance, amount);
        }
    }

    // --- PANIC CODES (0x...) ---

    // Panic 0x01: assert(false)
    function testPanic0x01() public pure {
        assert(false);
    }

    // Panic 0x11: Arithmetic overflow / underflow
    function testPanic0x11() public {
        balance = 0;
        balance -= 1; // 0.8.x tự động panic nếu không dùng unchecked
    }

    // Panic 0x12: Division by zero
    function testPanic0x12(uint256 denominator) public pure {
        uint256 result = 100 / denominator; // Truyền vào 0 để test
    }

    // Panic 0x21: Invalid enum value
    // Ép kiểu một số ngoài phạm vi enum (ví dụ: truyền vào 3)
    function testPanic0x21(uint256 _value) public pure returns (Action) {
        return Action(_value); 
    }

    // Panic 0x31: Empty array pop
    function testPanic0x31() public {
        // Đảm bảo mảng rỗng rồi pop
        array.pop();
    }

    // Panic 0x32: Array index out of bounds
    function testPanic0x32(uint256 index) public view returns (uint256) {
        // Truyền vào index >= array.length
        return array[index];
    }

    // --- REQUIRE / REVERT WITH STRING ---

    function testRequireString(uint256 value) public pure {
        require(value < 100, "value phai nho hon 100");
    }
}`
  };
export default function PushArtifactTest() {
  const [params, setParams] = useState<PushArtifactParams>({
    contract_address: '',
    metadata: '',
    source_code: '',
    source_map: '',
    storage_layout: '',
  });

  const [sourceFiles, setSourceFiles] = useState<SourceFile[]>([
    { filename: '', content: '' }
  ]);

  const [loading, setLoading] = useState(false);
  const [response, setResponse] = useState<JSONRPCResponse | null>(null);
  const [error, setError] = useState<string | null>(null);

  const addSourceFile = () => {
    setSourceFiles([...sourceFiles, { filename: '', content: '' }]);
  };

  const removeSourceFile = (index: number) => {
    if (sourceFiles.length > 1) {
      setSourceFiles(sourceFiles.filter((_, i) => i !== index));
    }
  };

  const updateSourceFile = (index: number, field: 'filename' | 'content', value: string) => {
    const updated = [...sourceFiles];
    updated[index][field] = value;
    setSourceFiles(updated);
  };

  const handleChange = (
    e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>
  ) => {
    const { name, value, type } = e.target;
    setParams((prev) => ({
      ...prev,
      [name]:
        type === 'checkbox'
          ? (e.target as HTMLInputElement).checked
          : value,
    }));
  };

  const loadExampleData = () => {
    setParams({
      contract_address: '0xE5A7116231033304b6226087A55c7F599D09A579',
      metadata: JSON.stringify({"compiler":{"version":"0.8.30+commit.73712a01"},"language":"Solidity","output":{"abi":[{"inputs":[{"internalType":"uint256","name":"available","type":"uint256"},{"internalType":"uint256","name":"required","type":"uint256"}],"name":"InsufficientBalance","type":"error"},{"inputs":[{"internalType":"uint256","name":"","type":"uint256"}],"name":"array","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},{"inputs":[],"name":"balance","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},{"inputs":[{"internalType":"uint256","name":"amount","type":"uint256"}],"name":"testCustomError","outputs":[],"stateMutability":"pure","type":"function"},{"inputs":[],"name":"testPanic0x01","outputs":[],"stateMutability":"pure","type":"function"},{"inputs":[],"name":"testPanic0x11","outputs":[],"stateMutability":"nonpayable","type":"function"},{"inputs":[{"internalType":"uint256","name":"denominator","type":"uint256"}],"name":"testPanic0x12","outputs":[],"stateMutability":"pure","type":"function"},{"inputs":[{"internalType":"uint256","name":"_value","type":"uint256"}],"name":"testPanic0x21","outputs":[{"internalType":"enum TestDebug.Action","name":"","type":"uint8"}],"stateMutability":"pure","type":"function"},{"inputs":[],"name":"testPanic0x31","outputs":[],"stateMutability":"nonpayable","type":"function"},{"inputs":[{"internalType":"uint256","name":"index","type":"uint256"}],"name":"testPanic0x32","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},{"inputs":[{"internalType":"uint256","name":"value","type":"uint256"}],"name":"testRequireString","outputs":[],"stateMutability":"pure","type":"function"}],"devdoc":{"kind":"dev","methods":{},"version":1},"userdoc":{"kind":"user","methods":{},"version":1}},"settings":{"compilationTarget":{"test.sol":"TestDebug"},"evmVersion":"prague","libraries":{},"metadata":{"bytecodeHash":"ipfs"},"optimizer":{"enabled":true,"runs":200},"remappings":[],"viaIR":true},"sources":{"test.sol":{"keccak256":"0xd7635fb922ae80aef86db4fa744fefa883d2f2077ced0db3038f172892d1ddeb","license":"MIT","urls":["bzz-raw://e89e7b38a5a1f2affd6a4d517c2a2e1453e662519c9ae5c2289164ca908f9673","dweb:/ipfs/QmahoSCHTXGZKbv3pfDKP2hWRGign9V4aYuQAx9cLAYLHH"]}},"version":1},null, 2),
      source_code: '', // Will be built from sourceFiles
      source_map: "58:584:0:-:0;;;;;;;;;;;;;;;;;;;;;;;;;;;588:35;58:584;588:35;;;58:584;;;;;;;;;;;;;-1:-1:-1;;58:584:0;;;;246:2;58:584;;237:11;58:584;;;;;;;-1:-1:-1;;;58:584:0;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;-1:-1:-1;;58:584:0;;;;;;;;;;;;;;;;;;;-1:-1:-1;;58:584:0;;;;;;-1:-1:-1;;;588:35:0;;58:584;;588:35;;58:584;;;;;;;;;;;588:35;;;58:584;;;;;;-1:-1:-1;;58:584:0;;;;;;-1:-1:-1;;58:584:0;;;;;;;;;;;;;;;;;;;;;",
      storage_layout: JSON.stringify({
        "storage": [
            {
                "astId": 3,
                "contract": "test.sol:TestDebug",
                "label": "balance",
                "offset": 0,
                "slot": "0",
                "type": "t_uint256"
            }
        ],
        "types": {
            "t_uint256": {
                "encoding": "inplace",
                "label": "uint256",
                "numberOfBytes": "32"
            }
        }
    }, null, 2),
    });

    // Load example source files
    setSourceFiles([
      {
        filename: 'test.sol',
        content: sourceCodeObj["test.sol"]
      }
    ]);
  };

  const validateJSON = (jsonString: string, fieldName: string): boolean => {
    try {
      JSON.parse(jsonString);
      return true;
    } catch (e) {
      setError(`Invalid JSON in ${fieldName}: ${e instanceof Error ? e.message : 'Unknown error'}`);
      return false;
    }
  };

  const handleSubmitWithValidation = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    
    // Build source_code JSON from sourceFiles array
    const sourceCodeObj: Record<string, string> = {};
    for (const file of sourceFiles) {
      if (file.filename && file.content) {
        sourceCodeObj[file.filename] = file.content;
      }
    }

    // Check if we have at least one source file
    if (Object.keys(sourceCodeObj).length === 0) {
      setError('At least one source file (filename + content) is required');
      return;
    }

    // Convert to JSON string
    const sourceCodeJSON = JSON.stringify(sourceCodeObj, null, 2);
    
    // Validate JSON fields
    if (!validateJSON(params.metadata, 'metadata')) return;
    if (params.storage_layout && !validateJSON(params.storage_layout, 'storage_layout')) return;

    // If validation passes, proceed with submit
    setLoading(true);
    setResponse(null);
    console.log(sourceCodeJSON)
    try {
      const requestBody = {
        jsonrpc: '2.0',
        method: 'rpc_pushArtifact',
        params: {
          ...params,
          source_code: sourceCodeJSON // Use the built JSON string
        },
        id: 1,
      };

      const res = await fetch(GO_BACKEND_RPC_URL, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify(requestBody),
      });

      const data: JSONRPCResponse = await res.json();
      setResponse(data);

      if (data.error) {
        setError(data.error.message);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="max-w-6xl mx-auto p-6 space-y-6">
      <h1 className="text-3xl font-bold text-gray-800 mb-6">
        Test RPC Push Artifact
      </h1>

      <div className="mb-4 flex gap-2">
        <button
          type="button"
          onClick={loadExampleData}
          className="px-4 py-2 bg-gray-600 text-white rounded-md hover:bg-gray-700 focus:outline-none focus:ring-2 focus:ring-gray-500 text-sm"
        >
          Load Example Data
        </button>
        <button
          type="button"
          onClick={() => {
            setParams({
              contract_address: '',
              metadata: '',
              source_code: '',
              source_map: '',
              storage_layout: '',
            });
            setSourceFiles([{ filename: '', content: '' }]);
            setResponse(null);
            setError(null);
          }}
          className="px-4 py-2 bg-gray-400 text-white rounded-md hover:bg-gray-500 focus:outline-none focus:ring-2 focus:ring-gray-500 text-sm"
        >
          Clear All
        </button>
      </div>

      <form onSubmit={handleSubmitWithValidation} className="space-y-4">
        {/* Contract Address */}
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">
            Contract Address *
          </label>
          <input
            type="text"
            name="contract_address"
            value={params.contract_address}
            onChange={handleChange}
            required
            className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500"
            placeholder="0x..."
          />
        </div>

        {/* Metadata (JSON) */}
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">
            Metadata (config.json) *
          </label>
          <textarea
            name="metadata"
            value={params.metadata}
            onChange={handleChange}
            required
            rows={8}
            className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 font-mono text-sm"
            placeholder='{"compiler": {...}, "language": "Solidity", ...}'
          />
        </div>

        {/* Source Code Files - Dynamic Input */}
        <div>
          <div className="flex justify-between items-center mb-2">
            <label className="block text-sm font-medium text-gray-700">
              Source Code Files *
            </label>
            <button
              type="button"
              onClick={addSourceFile}
              className="px-3 py-1 bg-green-600 text-white text-sm rounded-md hover:bg-green-700 focus:outline-none focus:ring-2 focus:ring-green-500"
            >
              + Add File
            </button>
          </div>

          {sourceFiles.map((file, index) => (
            <div key={index} className="mb-4 p-4 border border-gray-200 rounded-md bg-gray-50">
              <div className="flex justify-between items-start mb-2">
                <label className="text-sm font-medium text-gray-600">
                  File #{index + 1}
                </label>
                {sourceFiles.length > 1 && (
                  <button
                    type="button"
                    onClick={() => removeSourceFile(index)}
                    className="px-2 py-1 bg-red-500 text-white text-xs rounded hover:bg-red-600"
                  >
                    Remove
                  </button>
                )}
              </div>
              
              <input
                type="text"
                value={file.filename}
                onChange={(e) => updateSourceFile(index, 'filename', e.target.value)}
                placeholder="Filename (e.g., MyContract.sol)"
                className="w-full px-3 py-2 mb-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 text-sm"
              />
              
              <textarea
                value={file.content}
                onChange={(e) => updateSourceFile(index, 'content', e.target.value)}
                placeholder="// SPDX-License-Identifier: MIT&#10;pragma solidity ^0.8.0;&#10;&#10;contract MyContract {&#10;    // Your code here&#10;}"
                rows={10}
                className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 font-mono text-sm"
              />
            </div>
          ))}
          <p className="text-xs text-gray-500 mt-2">
            Enter each Solidity source file separately. Will be automatically converted to JSON on submit.
          </p>
        </div>

        {/* Source Map */}
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">
            Source Map
          </label>
          <textarea
            name="source_map"
            value={params.source_map}
            onChange={handleChange}
            rows={3}
            className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 font-mono text-sm"
            placeholder="Source map string"
          />
        </div>

        {/* Storage Layout */}
        <div>
          <label className="block text-sm font-medium text-gray-700 mb-1">
            Storage Layout (JSON)
          </label>
          <textarea
            name="storage_layout"
            value={params.storage_layout}
            onChange={handleChange}
            rows={4}
            className="w-full px-3 py-2 border border-gray-300 rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 font-mono text-sm"
            placeholder='{"storage": [...], ...}'
          />
        </div>

        {/* Submit Button */}
        <button
          type="submit"
          disabled={loading}
          className="w-full bg-blue-600 text-white py-3 px-4 rounded-md hover:bg-blue-700 focus:outline-none focus:ring-2 focus:ring-blue-500 disabled:opacity-50 disabled:cursor-not-allowed font-medium"
        >
          {loading ? 'Sending...' : 'Send RPC Request'}
        </button>
      </form>

      {/* Error Display */}
      {error && (
        <div className="mt-6 p-4 bg-red-50 border border-red-200 rounded-md">
          <h3 className="text-lg font-semibold text-red-800 mb-2">Error</h3>
          <p className="text-red-700">{error}</p>
        </div>
      )}

      {/* Response Display */}
      {response && (
        <div className="mt-6">
          <h3 className="text-lg font-semibold text-gray-800 mb-2">
            Response
          </h3>
          <div className="bg-gray-50 border border-gray-200 rounded-md p-4">
            <pre className="text-sm overflow-auto max-h-96">
              {JSON.stringify(response, null, 2)}
            </pre>
          </div>
        </div>
      )}
    </div>
  );
}

