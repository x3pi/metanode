package listener

import (
	"fmt"
	"math/big"

	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/contracts/cross_chain_contract"
)

// parseSentLogRaw parses raw log data+topics into MessageSent.
// Contract v3 MessageSent signature:
//
//	event MessageSent(
//	    uint256 indexed sourceNationId,       // Topics[1]
//	    uint256 indexed destNationId,         // Topics[2]
//	    bytes32 indexed msgId,                // Topics[3] ← txHash gốc của user
//	    bool isEVM,                           // Data
//	    address sender,                       // Data
//	    address target,                       // Data
//	    uint256 value,                        // Data
//	    bytes payload,                        // Data
//	    uint256 timestamp                     // Data
//	)
func parseSentLogRaw(cfg *tcp_config.ClientConfig, logData []byte, logTopics [][]byte) (*cross_chain_contract.MessageSent, error) {
	eventData := make(map[string]interface{})
	// UnpackIntoMap giải mã tất cả non-indexed fields từ Data
	if err := cfg.CrossChainAbi.UnpackIntoMap(eventData, "MessageSent", logData); err != nil {
		return nil, fmt.Errorf("failed to unpack MessageSent: %w", err)
	}
	// Inject indexed fields từ Topics (UnpackIntoMap không xử lý indexed fields)
	if len(logTopics) >= 3 {
		eventData["sourceNationId"] = new(big.Int).SetBytes(logTopics[1])
		eventData["destNationId"] = new(big.Int).SetBytes(logTopics[2])
	}

	msgSent, err := cross_chain_contract.ParseMessageSent(eventData)
	if err != nil {
		return nil, err
	}

	// Parse msgId từ Topics[3] — txHash gốc của user gửi lên chain nguồn
	if len(logTopics) >= 4 && len(logTopics[3]) == 32 {
		copy(msgSent.MsgId[:], logTopics[3])
	}

	return msgSent, nil
}

// parseReceivedLogRaw parses raw log data+topics into MessageReceived.
// Contract v3 MessageReceived signature:
//
//	event MessageReceived(
//	    uint256 indexed sourceNationId,       // Topics[1]
//	    uint256 indexed destNationId,         // Topics[2]
//	    bytes32 indexed msgId,                // Topics[3] ← txHash gốc của user
//	    MessageType msgType,                  // Data
//	    MessageStatus status,                 // Data
//	    bytes returnData,                     // Data
//	    address sender,                       // Data
//	    uint256 amount                        // Data
//	)
func parseReceivedLogRaw(cfg *tcp_config.ClientConfig, logData []byte, logTopics [][]byte) (*cross_chain_contract.MessageReceived, error) {
	eventData := make(map[string]interface{})
	// UnpackIntoMap giải mã tất cả non-indexed fields từ Data
	if err := cfg.CrossChainAbi.UnpackIntoMap(eventData, "MessageReceived", logData); err != nil {
		return nil, fmt.Errorf("failed to unpack MessageReceived: %w", err)
	}
	// Inject indexed fields từ Topics
	if len(logTopics) >= 3 {
		eventData["sourceNationId"] = new(big.Int).SetBytes(logTopics[1])
		eventData["destNationId"] = new(big.Int).SetBytes(logTopics[2])
	}

	msgReceived, err := cross_chain_contract.ParseMessageReceived(eventData)
	if err != nil {
		return nil, err
	}

	// Parse msgId từ Topics[3] — txHash gốc của user gửi lên chain nguồn
	if len(logTopics) >= 4 && len(logTopics[3]) == 32 {
		copy(msgReceived.MsgId[:], logTopics[3])
	}

	return msgReceived, nil
}
