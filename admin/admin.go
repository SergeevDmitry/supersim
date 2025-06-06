package admin

import (
	"context"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"sync"

	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/predeploys"
	supervisortypes "github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
	"github.com/ethereum-optimism/supersim/config"
	"github.com/ethereum-optimism/supersim/interop"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/gin-gonic/gin"
)

type AdminServer struct {
	log log.Logger

	srv    *http.Server
	cancel context.CancelFunc
	wg     sync.WaitGroup

	networkConfig    *config.NetworkConfig
	l2ToL2MsgIndexer *interop.L2ToL2MessageIndexer

	port uint64
}

type RPCMethods struct {
	log              log.Logger
	networkConfig    *config.NetworkConfig
	l2ToL2MsgIndexer *interop.L2ToL2MessageIndexer
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type JSONL2ToL2Message struct {
	Destination uint64         `json:"Destination"`
	Source      uint64         `json:"Source"`
	Nonce       *big.Int       `json:"Nonce"`
	Sender      common.Address `json:"Sender"`
	Target      common.Address `json:"Target"`
	Message     hexutil.Bytes  `json:"Message"`
}

// JSONAccessList represents the access list in a format suitable for JSON RPC response
type JSONAccessList struct {
	AccessList []types.AccessTuple `json:"accessList"`
}

// JSONIdentifierWithPayload is a struct with Identifier fields and message payload
type JSONIdentifierWithPayload struct {
	Origin      common.Address        `json:"origin"`
	BlockNumber *math.HexOrDecimal256 `json:"blockNumber"`
	LogIndex    *math.HexOrDecimal256 `json:"logIndex"`
	Timestamp   *math.HexOrDecimal256 `json:"timestamp"`
	ChainId     *math.HexOrDecimal256 `json:"chainId"`
	Payload     hexutil.Bytes         `json:"payload"`
}

func (e *JSONRPCError) Error() string {
	return e.Message
}

func (err *JSONRPCError) ErrorCode() int {
	return err.Code
}

func NewAdminServer(log log.Logger, port uint64, networkConfig *config.NetworkConfig, indexer *interop.L2ToL2MessageIndexer) *AdminServer {

	adminServer := &AdminServer{log: log, port: port, networkConfig: networkConfig}

	if networkConfig.InteropEnabled && indexer != nil {
		adminServer.l2ToL2MsgIndexer = indexer
	}

	return adminServer
}

func (s *AdminServer) Start(ctx context.Context) error {
	router := s.setupRouter()

	s.srv = &http.Server{Handler: router}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		panic(err)
	}

	addr := listener.Addr().(*net.TCPAddr)

	s.port = uint64(addr.Port)
	s.log.Debug("admin server listening", "port", s.port)

	ctx, s.cancel = context.WithCancel(ctx)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.log.Error("failed to serve", "error", err)
		}
	}()

	go func() {
		<-ctx.Done()
		if err := s.srv.Shutdown(context.Background()); err != nil {
			s.log.Error("failed to shutdown", "error", err)
		}
	}()

	return nil
}

func (s *AdminServer) Stop(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}

	s.wg.Wait()
	return nil
}

func (s *AdminServer) Endpoint() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}

func (s *AdminServer) ConfigAsString() string {
	return fmt.Sprintf("Admin Server: %s\n\n", s.Endpoint())
}

func filterByChainID(chains []config.ChainConfig, chainId uint64) *config.ChainConfig {
	for _, chain := range chains {
		if chain.ChainID == chainId {
			return &chain
		}
	}
	return nil
}

func (s *AdminServer) setupRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	rpcServer := rpc.NewServer()
	rpcMethods := &RPCMethods{
		log:              s.log,
		networkConfig:    s.networkConfig,
		l2ToL2MsgIndexer: s.l2ToL2MsgIndexer,
	}

	if err := rpcServer.RegisterName("admin", rpcMethods); err != nil {
		s.log.Error("failed to register RPC methods:", "error", err)
		return nil
	}

	router.GET("/ready", func(c *gin.Context) {
		c.String(http.StatusOK, "OK")
	})

	router.POST("/", gin.WrapH(rpcServer))

	return router
}

func (m *RPCMethods) GetConfig(args *struct{}) *config.NetworkConfig {

	m.log.Debug("admin_getConfig")
	return m.networkConfig
}

func (m *RPCMethods) GetL1Addresses(args *uint64) (*map[string]string, error) {
	chain := filterByChainID(m.networkConfig.L2Configs, *args)
	if chain == nil {
		return nil, &JSONRPCError{
			Code:    -32602,
			Message: "chain not found",
		}
	}

	reply := map[string]string{
		"AddressManager":                    chain.L2Config.L1Addresses.AddressManager.String(),
		"L1CrossDomainMessengerProxy":       chain.L2Config.L1Addresses.L1CrossDomainMessengerProxy.String(),
		"L1ERC721BridgeProxy":               chain.L2Config.L1Addresses.L1ERC721BridgeProxy.String(),
		"L1StandardBridgeProxy":             chain.L2Config.L1Addresses.L1StandardBridgeProxy.String(),
		"L2OutputOracleProxy":               chain.L2Config.L1Addresses.L2OutputOracleProxy.String(),
		"OptimismMintableERC20FactoryProxy": chain.L2Config.L1Addresses.OptimismMintableERC20FactoryProxy.String(),
		"OptimismPortalProxy":               chain.L2Config.L1Addresses.OptimismPortalProxy.String(),
		"SystemConfigProxy":                 chain.L2Config.L1Addresses.SystemConfigProxy.String(),
		"ProxyAdmin":                        chain.L2Config.L1Addresses.ProxyAdmin.String(),
	}
	return &reply, nil
}

func (m *RPCMethods) GetL2ToL2MessageByMsgHash(args *common.Hash) (*JSONL2ToL2Message, error) {

	if m.l2ToL2MsgIndexer == nil {
		return nil, &JSONRPCError{
			Code:    -32601,
			Message: "L2ToL2MsgIndexer is not initialized. Ensure that interop is enabled using the --interop.enabled flag.",
		}
	}

	if (args == nil || args == &common.Hash{}) {
		return nil, &JSONRPCError{
			Code:    -32602,
			Message: "Valid msg hash not provided",
		}
	}

	storeEntry, err := m.l2ToL2MsgIndexer.Get(*args)
	if err != nil {
		return nil, &JSONRPCError{
			Code:    -32603,
			Message: fmt.Sprintf("Failed to get message: %v", err),
		}
	}

	msg := storeEntry.Message()

	m.log.Debug("admin_getL2ToL2MessageByMsgHash")
	return &JSONL2ToL2Message{
		Destination: msg.Destination,
		Source:      msg.Source,
		Nonce:       msg.Nonce,
		Sender:      msg.Sender,
		Target:      msg.Target,
		Message:     msg.Message,
	}, nil
}

// GetAccessListForIdentifier calculates and returns the access list for a given Identifier and payload
func (m *RPCMethods) GetAccessListForIdentifier(args *JSONIdentifierWithPayload) (*JSONAccessList, error) {
	m.log.Info("admin_getAccessListForIdentifier")

	if args == nil {
		return nil, &JSONRPCError{
			Code:    -32602,
			Message: "Invalid parameters: identifier is required",
		}
	}

	if args.Origin == (common.Address{}) || args.BlockNumber == nil || args.LogIndex == nil ||
		args.Timestamp == nil || args.ChainId == nil || len(args.Payload) == 0 {
		return nil, &JSONRPCError{
			Code:    -32602,
			Message: "Invalid parameters: all identifier fields and payload are required",
		}
	}

	identifier := supervisortypes.Identifier{
		Origin:      args.Origin,
		BlockNumber: ((*big.Int)(args.BlockNumber)).Uint64(),
		LogIndex:    uint32((*big.Int)(args.LogIndex).Uint64()),
		Timestamp:   ((*big.Int)(args.Timestamp)).Uint64(),
		ChainID:     eth.ChainIDFromBig((*big.Int)(args.ChainId)),
	}

	access := identifier.ChecksumArgs(crypto.Keccak256Hash(args.Payload)).Access()

	accessList := []types.AccessTuple{
		{
			Address:     predeploys.CrossL2InboxAddr,
			StorageKeys: supervisortypes.EncodeAccessList([]supervisortypes.Access{access}),
		},
	}

	return &JSONAccessList{
		AccessList: accessList,
	}, nil
}

// GetAccessListByMsgHash retrieves a message by its hash and returns its access list
func (m *RPCMethods) GetAccessListByMsgHash(args *common.Hash) (*JSONAccessList, error) {
	m.log.Info("admin_getAccessListByMsgHash", "msgHash", args.Hex())

	if m.l2ToL2MsgIndexer == nil {
		return nil, &JSONRPCError{
			Code:    -32603,
			Message: "L2ToL2MsgIndexer is not initialized. Ensure that interop is enabled using the --interop.enabled flag.",
		}
	}

	if (args == nil || args == &common.Hash{}) {
		return nil, &JSONRPCError{
			Code:    -32602,
			Message: "Valid msg hash not provided",
		}
	}

	storeEntry, err := m.l2ToL2MsgIndexer.Get(*args)
	if err != nil {
		return nil, &JSONRPCError{
			Code:    -32602,
			Message: fmt.Sprintf("Failed to get message: %v", err),
		}
	}

	accessList := interop.MessageAccessList(storeEntry.Identifier(), storeEntry.MessagePayload())

	return &JSONAccessList{
		AccessList: accessList,
	}, nil
}
