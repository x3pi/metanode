package event_helper

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/types"
)

type AbiInput struct {
	Indexed      bool
	InternalType string
	Name         string
	Type         string
}

type AbiEvent struct {
	Anonymous bool
	Inputs    []AbiInput
	Name      string
	Type      string
}

func abiToTopicHash(abi AbiEvent) (string, error) {
	if abi.Name == "" {
		return "", fmt.Errorf("event ABI missing name")
	}
	if abi.Type != "event" {
		return "", fmt.Errorf("ABI %s is not an event", abi.Name)
	}
	types := make([]string, len(abi.Inputs))
	for i, input := range abi.Inputs {
		if input.Type == "" {
			return "", fmt.Errorf("event %s missing input type at index %d", abi.Name, i)
		}
		types[i] = input.Type
	}
	signature := fmt.Sprintf("%s(%s)", abi.Name, strings.Join(types, ","))
	hash := crypto.Keccak256([]byte(signature))
	return "0x" + hex.EncodeToString(hash), nil
}

// BuildTopicMapFromAbiJson builds a topic map from ABI JSON string
func BuildTopicMapFromAbiJson(abiJson string) (map[string]AbiEvent, error) {
	if abiJson == "" {
		return nil, fmt.Errorf("ABI JSON is empty")
	}

	var abiItems []AbiEvent
	err := json.Unmarshal([]byte(abiJson), &abiItems)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal ABI JSON: %w", err)
	}

	result := make(map[string]AbiEvent)
	for _, item := range abiItems {
		// Only process events
		if item.Type != "event" {
			continue
		}
		topic, err := abiToTopicHash(item)
		if err != nil {
			return nil, fmt.Errorf("failed to compute topic hash for %s: %w", item.Name, err)
		}
		result[strings.ToLower(topic)] = item
	}

	return result, nil
}

func DecodeEventLog(event AbiEvent, log types.EventLog) (map[string]interface{}, error) {
	if log == nil {
		return nil, fmt.Errorf("log is nil")
	}

	result := make(map[string]interface{})
	// Parse indexed topics (topics[1:], topics[0] is event signature)
	topics := log.Topics()
	indexedIdx := 1 // Start from topics[1], topics[0] is the event signature hash
	for _, input := range event.Inputs {
		if input.Indexed {
			if indexedIdx >= len(topics) {
				continue
			}
			topicValue := topics[indexedIdx]
			indexedIdx++

			// Parse the indexed value based on type
			parsedValue, err := parseIndexedTopic(input.Type, topicValue)
			if err != nil {
				return nil, fmt.Errorf("failed to parse indexed topic %s: %w", input.Name, err)
			}
			result[input.Name] = parsedValue
		}
	}

	// Parse non-indexed data
	dataHex := log.Data()
	if dataHex != "" {
		nonIndexedData, err := decodeEventData(event, dataHex)
		if err != nil {
			return nil, err
		}
		// Merge non-indexed data into result
		for k, v := range nonIndexedData {
			result[k] = v
		}
	}

	return result, nil
}

// parseIndexedTopic parses an indexed topic value based on its type
func parseIndexedTopic(typeName string, topicHex string) (interface{}, error) {
	if !strings.HasPrefix(topicHex, "0x") {
		topicHex = "0x" + topicHex
	}
	topicBytes := common.FromHex(topicHex)

	typ, err := abi.NewType(typeName, "", nil)
	if err != nil {
		return nil, fmt.Errorf("invalid type %s: %w", typeName, err)
	}

	// For simple types, unpack directly
	args := abi.Arguments{{Type: typ}}
	values, err := args.Unpack(topicBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack topic: %w", err)
	}

	if len(values) > 0 {
		return values[0], nil
	}
	return nil, fmt.Errorf("no value unpacked")
}

func decodeEventData(event AbiEvent, dataHex string) (map[string]interface{}, error) {
	args, err := event.arguments()
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return map[string]interface{}{}, nil
	}
	if !strings.HasPrefix(dataHex, "0x") {
		dataHex = "0x" + dataHex
	}
	rawData := common.FromHex(dataHex)
	values, err := args.Unpack(rawData)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack event %s: %w", event.Name, err)
	}
	result := make(map[string]interface{}, len(values))
	for idx, arg := range args {
		name := arg.Name
		if name == "" {
			name = fmt.Sprintf("arg%d", idx)
		}
		if idx < len(values) {
			result[name] = values[idx]
		}
	}
	return result, nil
}

func (e AbiEvent) arguments() (abi.Arguments, error) {
	args := make(abi.Arguments, 0, len(e.Inputs))
	for _, input := range e.Inputs {
		if input.Indexed {
			continue
		}
		if input.Type == "" {
			return nil, fmt.Errorf("event %s input missing type", e.Name)
		}
		typ, err := abi.NewType(input.Type, "", nil)
		if err != nil {
			return nil, fmt.Errorf("event %s invalid type %s: %w", e.Name, input.Type, err)
		}
		args = append(args, abi.Argument{
			Name:    input.Name,
			Type:    typ,
			Indexed: input.Indexed,
		})
	}
	return args, nil
}
