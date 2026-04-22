wrk.method = "POST"
wrk.headers["Content-Type"] = "application/json"
wrk.body = '{ "jsonrpc": "2.0", "id": 1, "method": "mtn_getAccountState", "params": ["0x781E6EC6EBDCA11Be4B53865a34C0c7f10b6da6e", "latest"] }'