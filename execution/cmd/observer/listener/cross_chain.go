package listener

import (
	"crypto/ecdsa"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	clientpkg "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/contracts/cross_chain_contract"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/processor"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

type CrossChainListener struct {
	cli               *clientpkg.Client      // Local client for executing transactions
	cfg               *c_config.ClientConfig // Local config
	remotelCli        *clientpkg.Client      // Remote client for listening events (nil if local)
	remotelCfg        *c_config.ClientConfig // Remote config (nil if local)
	contract          common.Address         // Local contract address
	remoteContract    common.Address         // Remote contract to listen events from (same as contract if local)
	processor         *processor.SupervisorProcessor
	messageQueue      chan messageEvent
	nonceSentState    *processor.NonceOrderState[*cross_chain_contract.MessageSent]     // state cho MessageSent → receiveMessage
	nonceReceiveState *processor.NonceOrderState[*cross_chain_contract.MessageReceived] // state cho MessageReceived → processConfirmation
	isRemote          bool                                                              // Flag to indicate if this is listening to a remote chain
	ethPrivateKey     *ecdsa.PrivateKey                                                 // ETH ECDSA private key for signing messages (ecrecover on contract)

	// Atomic counters: đếm số lượng MessageSent / MessageReceived đang trong messageQueue hoặc đang xử lý.
	// Periodic scan chỉ được phép chạy khi counter tương ứng = 0.
	pendingSentCount     int32 // Số MessageSent đang chờ/đang xử lý trong worker
	pendingReceivedCount int32 // Số MessageReceived đang chờ/đang xử lý trong worker
}

type messageEvent struct {
	eventType string // "MessageSent", "MessageReceived", "ScanMessageSent", "ScanMessageReceived"
	eventData map[string]interface{}
	txHash    common.Hash
	topic0    common.Hash
}

func NewCrossChainListener(cli *clientpkg.Client, cfg *c_config.ClientConfig, contract common.Address, proc *processor.SupervisorProcessor) *CrossChainListener {
	return &CrossChainListener{
		cli:            cli,
		cfg:            cfg,
		remotelCli:     nil, // No remote client for local listener
		remotelCfg:     nil, // No remote config for local listener
		contract:       contract,
		remoteContract: contract, // Same as contract for local
		processor:      proc,
		messageQueue:   make(chan messageEvent, 1000),
		isRemote:       false,
	}
}

// NewRemoteCrossChainListener creates a listener for a remote chain
// It listens to events from remoteContract but executes transactions on local contract
func NewRemoteCrossChainListener(
	localClient *clientpkg.Client,
	localCfg *c_config.ClientConfig,
	localContract common.Address,
	remoteClient *clientpkg.Client,
	remoteCfg *c_config.ClientConfig,
	remoteContract common.Address,
	proc *processor.SupervisorProcessor,
	ethPrivateKeyHex string,
) *CrossChainListener {
	var ethKey *ecdsa.PrivateKey
	if ethPrivateKeyHex != "" {
		var err error
		ethKey, err = crypto.HexToECDSA(ethPrivateKeyHex)
		if err != nil {
			logger.Error("Failed to parse eth_private_key: %v", err)
		} else {
			ethAddr := crypto.PubkeyToAddress(ethKey.PublicKey)
			logger.Info("🔑 ETH signing address: %s", ethAddr.Hex())
		}
	}
	return &CrossChainListener{
		cli:            localClient,  // Use local client for executing transactions
		cfg:            localCfg,     // Use local config for transactions
		remotelCli:     remoteClient, // Use remote client for listening events
		remotelCfg:     remoteCfg,    // Use remote config for events
		contract:       localContract,
		remoteContract: remoteContract,
		processor:      proc,
		messageQueue:   make(chan messageEvent, 1000),
		isRemote:       true,
		ethPrivateKey:  ethKey,
	}
}

func (l *CrossChainListener) Start() {

}

// SubscribeToCrossChainContract subscribe REMOTE contract để lắng nghe MessageSent / MessageReceived.
// Chỉ gọi khi isRemote=true (NewRemoteCrossChainListener).
// Local contract chỉ được dùng để THỰC THI (receiveMessage, processConfirmation) — không subscribe events tại đây.
func (l *CrossChainListener) SubscribeToCrossChainContract() {
	// Luôn dùng remote client và remote contract — không có branch isRemote nữa
	if l.remotelCli == nil {
		logger.Warn("SubscribeToCrossChainContract: remote client nil, bỏ qua")
		return
	}
	if (l.remoteContract == common.Address{}) {
		logger.Warn("SubscribeToCrossChainContract: remote contract address rỗng, bỏ qua")
		return
	}

	logger.Info("📡 Subscribe REMOTE contract %s để lắng nghe MessageSent / MessageReceived", l.remoteContract.Hex())
	eventCh, err := l.remotelCli.ParentSubcribes([]common.Address{l.remoteContract})
	if err != nil {
		logger.Warn("SubscribeToCrossChainContract: subscribe thất bại: %v", err)
		return
	}

	go func() {
		for evt := range eventCh {
			logs := evt.EventLogList()
			if len(logs) == 0 {
				continue
			}
			for idx, logItem := range logs {
				topics := logItem.Topics()
				eventName := "Unknown"
				var decoded map[string]interface{}
				eventSigStr := topics[0]
				if len(topics) > 0 {
					if !strings.HasPrefix(eventSigStr, "0x") {
						eventSigStr = "0x" + eventSigStr
					}
					// Dùng remotelCfg để match event signature từ remote chain
					for name, event := range l.remotelCfg.CrossChainAbi.Events {
						if event.ID.Hex() == eventSigStr {
							eventName = name
							decoded = make(map[string]interface{})
							dataBytes := common.FromHex(logItem.Data())
							// Unpack dùng local cfg ABI (cùng ABI, khác chain)
							if err := l.cfg.CrossChainAbi.UnpackIntoMap(decoded, name, dataBytes); err != nil {
								logger.Warn("Không decode được event %s: %v", eventName, err)
								continue
							}
							topicIdx := 1
							for _, input := range event.Inputs {
								if input.Indexed && topicIdx < len(topics) {
									decoded[input.Name] = common.HexToHash(topics[topicIdx])
									topicIdx++
								}
							}
							break
						}
					}
				}
				if decoded != nil {
					// l.handleDecodedEvent(eventName, decoded, common.HexToHash(logItem.TransactionHash()), eventSigStr)
				} else {
					logger.Info("Remote event #%d (%s): không decode được", idx, eventName)
				}
			}
		}
	}()
}

// Dùng local client (l.cli) và local contract (l.contract).
func (l *CrossChainListener) SubscribeToLocalContract() {
	if l.cli == nil {
		logger.Warn("SubscribeToLocalContract: local client nil, bỏ qua")
		return
	}
	if (l.contract == common.Address{}) {
		logger.Warn("SubscribeToLocalContract: local contract address rỗng, bỏ qua")
		return
	}

	logger.Info("📡 Subscribe local contract %s để bắt ChannelStateSet", l.contract.Hex())

	// Dùng ParentSubcribes — append vào list, không overwrite
	localEventCh, err := l.cli.ParentSubcribes([]common.Address{l.contract})
	if err != nil {
		logger.Warn("SubscribeToLocalContract: subscribe thất bại: %v", err)
		return
	}

	go func() {
		for evt := range localEventCh {
			logs := evt.EventLogList()
			if len(logs) == 0 {
				continue
			}
			for _, logItem := range logs {
				topics := logItem.Topics()
				if len(topics) == 0 {
					continue
				}
				eventSigStr := topics[0]
				if !strings.HasPrefix(eventSigStr, "0x") {
					eventSigStr = "0x" + eventSigStr
				}

				// Tìm event name từ ABI của local config
				for name, event := range l.cfg.CrossChainAbi.Events {
					if event.ID.Hex() != eventSigStr {
						continue
					}
					decoded := make(map[string]interface{})
					dataBytes := common.FromHex(logItem.Data())
					if err := l.cfg.CrossChainAbi.UnpackIntoMap(decoded, name, dataBytes); err != nil {
						logger.Warn("SubscribeToLocalContract: decode event %s thất bại: %v", name, err)
						break
					}
					// Indexed topics (nếu có)
					topicIdx := 1
					for _, input := range event.Inputs {
						if input.Indexed && topicIdx < len(topics) {
							decoded[input.Name] = common.HexToHash(topics[topicIdx])
							topicIdx++
						}
					}
					logger.Info("📨 Local event: %s (contract: %s)", name, l.contract.Hex())
					// Xử lý ngay — không qua messageQueue
					// l.handleLocalDecodedEvent(name, decoded)
					break
				}
			}
		}
	}()
}
