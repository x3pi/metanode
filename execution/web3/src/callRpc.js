async function sendRequest(method, params) {
    const payload = {
      jsonrpc: '2.0',
      id: 1,
      method: method,
      params: params,
    };
  
    console.log('payload -->', payload);
  
    const response = await fetch('https://client.mygvq.org:8446/', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(payload),
    });
  
    if (!response.ok) {
      throw new Error(`HTTP error ${response.status}`);
    }
  
    const json = await response.json();
  
    if (json.error) {
      throw new Error(json.error.message || 'RPC error');
    }
  
    return json.result;
  }
  
  async function callContractMethod(data) {
    return await sendRequest('eth_call', [data, 'latest']);
  }
  async function readContract() {
    try {
      const abi = [
        {
          inputs: [
            {
              internalType: 'address',
              name: '_user',
              type: 'address',
            },
          ],
          name: 'getUserPurchaseInfo',
          outputs: [
            {
              components: [
                {
                  internalType: 'uint256',
                  name: 'orderID',
                  type: 'uint256',
                },
                {
                  internalType: 'address',
                  name: 'user',
                  type: 'address',
                },
                {
                  internalType: 'address',
                  name: 'buyer',
                  type: 'address',
                },
                {
                  internalType: 'uint256',
                  name: 'discountID',
                  type: 'uint256',
                },
                {
                  internalType: 'uint256[]',
                  name: 'cartItemIds',
                  type: 'uint256[]',
                },
                {
                  internalType: 'uint256[]',
                  name: 'productIds',
                  type: 'uint256[]',
                },
                {
                  internalType: 'bytes32[]',
                  name: 'variantIds',
                  type: 'bytes32[]',
                },
                {
                  internalType: 'uint256[]',
                  name: 'quantities',
                  type: 'uint256[]',
                },
                {
                  internalType: 'uint256[]',
                  name: 'diffPrices',
                  type: 'uint256[]',
                },
                {
                  internalType: 'uint256[]',
                  name: 'prices',
                  type: 'uint256[]',
                },
                {
                  internalType: 'uint256[]',
                  name: 'rewards',
                  type: 'uint256[]',
                },
                {
                  internalType: 'uint256',
                  name: 'totalPrice',
                  type: 'uint256',
                },
                {
                  internalType: 'uint8',
                  name: 'checkoutType',
                  type: 'uint8',
                },
                {
                  internalType: 'uint8',
                  name: 'orderStatus',
                  type: 'uint8',
                },
                {
                  internalType: 'bytes32',
                  name: 'codeRef',
                  type: 'bytes32',
                },
                {
                  internalType: 'uint256',
                  name: 'afterDiscountPrice',
                  type: 'uint256',
                },
                {
                  internalType: 'uint256',
                  name: 'shippingPrice',
                  type: 'uint256',
                },
                {
                  internalType: 'uint8',
                  name: 'paymentType',
                  type: 'uint8',
                },
                {
                  internalType: 'uint256',
                  name: 'createdAt',
                  type: 'uint256',
                },
              ],
              internalType: 'struct Order[]',
              name: '_orders',
              type: 'tuple[]',
            },
            {
              components: [
                {
                  internalType: 'uint256',
                  name: 'id',
                  type: 'uint256',
                },
                {
                  internalType: 'address',
                  name: 'owner',
                  type: 'address',
                },
                {
                  components: [
                    {
                      internalType: 'uint256',
                      name: 'id',
                      type: 'uint256',
                    },
                    {
                      internalType: 'uint256',
                      name: 'productID',
                      type: 'uint256',
                    },
                    {
                      internalType: 'uint256',
                      name: 'quantity',
                      type: 'uint256',
                    },
                    {
                      internalType: 'bytes32',
                      name: 'variantID',
                      type: 'bytes32',
                    },
                    {
                      internalType: 'uint256',
                      name: 'createAt',
                      type: 'uint256',
                    },
                  ],
                  internalType: 'struct CartItem[]',
                  name: 'items',
                  type: 'tuple[]',
                },
              ],
              internalType: 'struct Cart',
              name: '_cart',
              type: 'tuple',
            },
            {
              components: [
                {
                  internalType: 'uint256',
                  name: 'productID',
                  type: 'uint256',
                },
                {
                  internalType: 'uint256',
                  name: 'createdAt',
                  type: 'uint256',
                },
              ],
              internalType: 'struct Favorite[]',
              name: '_productIds',
              type: 'tuple[]',
            },
          ],
          stateMutability: 'view',
          type: 'function',
        },
      ];
  
      const result = await callContractMethod({
        to: '0x87eD6a493030E4947872c37548A9ed8520c002c9', // Địa chỉ contract
        // data: encodedData, // Dữ liệu đã encode sẵn
        data: `0xE6801AC7000000000000000000000000E88FACC9558978D8E0996E7618860AE19606CD26`, // Dữ liệu đã encode sẵn
      });
  
      console.log('Balance:', result);
      return result;
    } catch (error) {
      console.error('Lỗi khi đọc contract:', error);
    }
  }
  await readContract();