//go:build ignore

package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/command"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
)

type rawWriter struct {
	conn      net.Conn
	writer    *bufio.Writer
	addr      string
	version   string
	toAddrHex string
}

func newRawWriter(addr, version, toAddrHex string) (*rawWriter, error) {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	rw := &rawWriter{
		conn:      conn,
		writer:    bufio.NewWriterSize(conn, 4*1024*1024),
		addr:      addr,
		version:   version,
		toAddrHex: toAddrHex,
	}

	go func() {
		reader := bufio.NewReader(conn)
		for {
			lengthBuf := make([]byte, 8)
			if _, err := io.ReadFull(reader, lengthBuf); err != nil {
				return
			}
			msgLen := binary.LittleEndian.Uint64(lengthBuf)
			if msgLen > 10*1024*1024 {
				return
			}
			msgBuf := make([]byte, msgLen)
			if _, err := io.ReadFull(reader, msgBuf); err != nil {
				return
			}
			var msg pb.Message
			if err := proto.Unmarshal(msgBuf, &msg); err == nil && msg.Header != nil {
				if msg.Header.Command == "TransactionError" {
					var txErr pb.TransactionHashWithError
					if proto.Unmarshal(msg.Body, &txErr) == nil {
						fmt.Printf("\n❌ SERVER REJECTED TX: %s | Code: %d | Msg: %s\n",
							common.BytesToHash(txErr.Hash).Hex(),
							txErr.Code,
							txErr.Description)
					}
				} else if msg.Header.Command != "Receipt" {
					fmt.Printf("\n📩 SERVER RESPONDED WITH COMMAND: %s\n", msg.Header.Command)
				}
			}
		}
	}()

	return rw, nil
}

func (rw *rawWriter) sendRaw(cmd string, body []byte) error {
	toAddr := common.HexToAddress(rw.toAddrHex)
	msgProto := &pb.Message{
		Header: &pb.Header{
			Command:   cmd,
			Version:   rw.version,
			ToAddress: toAddr.Bytes(),
			ID:        uuid.New().String(),
		},
		Body: body,
	}
	b, err := proto.Marshal(msgProto)
	if err != nil {
		return err
	}
	lengthBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(lengthBuf, uint64(len(b)))
	rw.conn.SetWriteDeadline(time.Now().Add(120 * time.Second))
	if _, err := rw.writer.Write(lengthBuf); err != nil {
		return err
	}
	if _, err := rw.writer.Write(b); err != nil {
		return err
	}
	return nil
}

func (rw *rawWriter) flush() error {
	return rw.writer.Flush()
}

func (rw *rawWriter) close() {
	if rw.conn != nil {
		rw.conn.Close()
	}
}

func main() {
	var nodeOverride string
	var configPath string
	flag.StringVar(&nodeOverride, "node", "", "Override the ParentConnectionAddress directly (e.g., 192.168.1.233:6200)")
	flag.StringVar(&configPath, "config", "cmd/tool/tps_blast/config.json", "Client config")
	flag.Parse()

	configIface, err := c_config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}
	config := configIface.(*c_config.ClientConfig)

	targetAddress := config.ParentConnectionAddress
	if nodeOverride != "" {
		targetAddress = nodeOverride
	}

	// Generate a new random Ethereum account
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		log.Fatalf("Failed to generate private key: %v", err)
	}
	privKeyBytes := crypto.FromECDSA(privateKey)
	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	fmt.Printf("Generated Account:\n")
	fmt.Printf("Address:     %s\n", address.Hex())
	fmt.Printf("Private Key: %s\n", hex.EncodeToString(privKeyBytes))

	blsKeyPair := bls.GenerateKeyPair()
	blsPubKeyHex := blsKeyPair.PublicKey().String()
	fmt.Printf("BLS PubKey:  %s\n", blsPubKeyHex)

	pKey := blsKeyPair.PrivateKey() // For signing the tx (even though it's not verified on first tx)

	blsPubKeyBytes, _ := hex.DecodeString(strings.TrimPrefix(blsPubKeyHex, "0x"))

	abiJSON := `[{"inputs":[{"internalType":"bytes","name":"publicKey","type":"bytes"}],"name":"setBlsPublicKey","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"}]`
	parsedABI, _ := abi.JSON(strings.NewReader(abiJSON))
	dataTx, _ := parsedABI.Pack("setBlsPublicKey", blsPubKeyBytes)

	toAddr := utils.GetAddressSelector(p_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	bigChainId := new(big.Int).SetUint64(config.ChainId)

	tx := e_types.NewTransaction(0, toAddr, big.NewInt(0), 1000000000, big.NewInt(100000), dataTx)
	signer := e_types.LatestSignerForChainID(bigChainId)
	ethTx, err := e_types.SignTx(tx, signer, privateKey)
	if err != nil {
		log.Fatalf("Failed to sign tx: %v", err)
	}

	internalTx, err := transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		log.Fatalf("Failed to convert tx: %v", err)
	}

	internalTx.UpdateRelatedAddresses([][]byte{})
	internalTx.UpdateDeriver(common.Hash{}, common.Hash{})
	internalTx.SetSign(pKey)

	txBytes, err := internalTx.Marshal()
	if err != nil {
		log.Fatalf("Failed to marshal tx: %v", err)
	}
	
	txProto := &pb.Transaction{}
	err = proto.Unmarshal(txBytes, txProto)
	if err != nil {
		log.Fatalf("Failed to unmarshal back to proto: %v", err)
	}

	batchProto := &pb.Transactions{Transactions: []*pb.Transaction{txProto}}

	rw, err := newRawWriter(targetAddress, config.Version(), config.ParentAddress)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer rw.close()

	initMsg := &pb.InitConnection{
		Address: address.Bytes(),
		Type:    config.NodeType(),
		Replace: true,
	}
	initBody, err := proto.Marshal(initMsg)
	if err != nil {
		log.Fatalf("Failed to marshal InitConnection: %v", err)
	}

	if err := rw.sendRaw(command.InitConnection, initBody); err != nil {
		log.Fatalf("InitConnection failed: %v", err)
	}
	rw.flush()

	time.Sleep(1 * time.Second)

	fmt.Printf("Sending transaction %s ...\n", internalTx.Hash().Hex())
	batchBytes, err := proto.Marshal(batchProto)
	if err != nil {
		log.Fatalf("Failed to marshal batch: %v", err)
	}
	if err := rw.sendRaw(command.SendTransactions, batchBytes); err != nil {
		log.Fatalf("Failed to send transaction: %v", err)
	}
	rw.flush()

	fmt.Println("Transaction sent successfully!")
	
	ip := strings.Split(targetAddress, ":")[0]
	rpcUrl := fmt.Sprintf("http://%s:8650", ip)
	
	fmt.Println("\n=== VERIFICATION COMMANDS ===")
	fmt.Println("To verify the transaction receipt:")
	fmt.Printf("curl -s -X POST -H \"Content-Type: application/json\" --data '{\"jsonrpc\":\"2.0\",\"method\":\"eth_getTransactionReceipt\",\"params\":[\"%s\"],\"id\":1}' %s\n\n", internalTx.Hash().Hex(), rpcUrl)
	
	fmt.Println("To check the account balance (ETH standard):")
	fmt.Printf("curl -s -X POST -H \"Content-Type: application/json\" --data '{\"jsonrpc\":\"2.0\",\"method\":\"eth_getBalance\",\"params\":[\"%s\",\"latest\"],\"id\":1}' %s\n\n", address.Hex(), rpcUrl)
	
	fmt.Println("To check the full account state (Meta-node custom):")
	fmt.Printf("curl -s -X POST -H \"Content-Type: application/json\" --data '{\"jsonrpc\":\"2.0\",\"method\":\"mtn_getAccountState\",\"params\":[\"%s\",\"latest\"],\"id\":1}' %s\n", address.Hex(), rpcUrl)

	// Sleep a bit to allow connection to flush and wait for responses
	time.Sleep(5 * time.Second)
}
