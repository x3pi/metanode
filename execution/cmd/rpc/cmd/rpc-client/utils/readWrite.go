package utils

import (
	"encoding/json"
	"net/http"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/rpc_client"
	"github.com/tidwall/gjson"
)

func ExtractRequestID(body []byte) interface{} {
	idResult := gjson.GetBytes(body, "id")
	if !idResult.Exists() {
		return nil
	}
	var id interface{}
	if err := json.Unmarshal([]byte(idResult.Raw), &id); err != nil {
		return nil
	}
	return id
}

func WriteJSON(w http.ResponseWriter, resp rpc_client.JSONRPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Error("Failed to encode JSON response: %v", err)
	}
}
