package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/multiformats/go-multiaddr"

	"shiledp2p/pkg/common"
	"shiledp2p/pkg/discovery"
	"shiledp2p/pkg/nat"
	"shiledp2p/pkg/speedtest"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	_ "github.com/libp2p/go-libp2p/p2p/transport/kcp"
)

// ShileP2P 是移动端绑定的主结构体
type ShileP2P struct {
	host      host.Host
	discovery *discovery.PeerDiscovery
	holePunch *nat.HolePunchCoordinator
	heartbeat *common.HeartbeatService
	speedTest *speedtest.SpeedTest

	ctx    context.Context
	cancel context.CancelFunc

	mu              sync.RWMutex
	connectedAgents map[peer.ID]time.Time
	connectedPeers  map[peer.ID]time.Time
	sourcePort      int

	currentAgentIndex int
	agentList         []peer.ID

	proxyStarted bool
	proxyMu      sync.Mutex

	proxyPort       int
	proxyTargetHost string
	proxyTargetPort string
	masterID        peer.ID
	agentInfo       map[peer.ID][]multiaddr.Multiaddr

	apiProxyPort    int
	tunnelProxyPort int
	apiProtocol     string
	tunnelProtocol  string

	// Callback functions for mobile platform
	onStatusChange func(status string)
	onError        func(err string)
	onAgentList    func(agents []string)
	onSpeedTest    func(result string)

	agents map[peer.ID]*common.AgentInfo
}

// NewShileP2P 创建一个新的 ShileP2P 实例
//
//export NewShileP2P
func NewShileP2P() *ShileP2P {
	return &ShileP2P{}
}

// Start 启动 P2P 服务
//
//export Start
func (s *ShileP2P) Start() error {
	log.Println("Starting ShileP2P service...")
	return nil
}

// Stop 停止 P2P 服务
//
//export Stop
func (s *ShileP2P) Stop() error {
	log.Println("Stopping ShileP2P service...")
	return nil
}

// GetVersion 返回当前版本信息
//
//export GetVersion
func (s *ShileP2P) GetVersion() string {
	return "1.0.0"
}

// Echo 用于测试绑定的简单函数
//
//export Echo
func (s *ShileP2P) Echo(message string) string {
	return fmt.Sprintf("Echo: %s", message)
}

// SetStatusChangeCallback sets the callback for status changes
//
//export SetStatusChangeCallback
func (c *ShileP2P) SetStatusChangeCallback(callback func(status string)) {
	c.onStatusChange = callback
}

// SetErrorCallback sets the callback for error messages
//
//export SetErrorCallback
func (c *ShileP2P) SetErrorCallback(callback func(err string)) {
	c.onError = callback
}

// SetAgentListCallback sets the callback for agent list updates
//
//export SetAgentListCallback
func (c *ShileP2P) SetAgentListCallback(callback func(agents []string)) {
	c.onAgentList = callback
}

// SetSpeedTestCallback sets the callback for speed test results
//
//export SetSpeedTestCallback
func (c *ShileP2P) SetSpeedTestCallback(callback func(result string)) {
	c.onSpeedTest = callback
}

// Start starts the mobile client
//
//export Start
func (c *ShileP2P) Start(apiPort, tunnelPort int) error {
	// Start discovery service
	c.discovery.Start()

	// Start hole punch coordinator
	c.holePunch.Start()

	// Start heartbeat service
	c.heartbeat.Start()

	// Start peer discovery loop
	go c.peerDiscoveryLoop()

	// Start agent monitoring
	go c.monitorAgents()

	return nil
}

// Stop stops the mobile client
//
//export Stop
func (c *ShileP2P) Stop() error {
	if c.cancel != nil {
		c.cancel()
	}

	if c.host != nil {
		return c.host.Close()
	}

	return nil
}

// GetConnectedAgents returns a JSON string of connected agents
//
//export GetConnectedAgents
func (c *ShileP2P) GetConnectedAgents() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	agents := make([]map[string]interface{}, 0)
	for id, lastSeen := range c.connectedAgents {
		agent := map[string]interface{}{
			"id":        id.String(),
			"last_seen": lastSeen.Format(time.RFC3339),
		}
		agents = append(agents, agent)
	}

	data, err := json.Marshal(agents)
	if err != nil {
		return fmt.Sprintf(`{"error": "%s"}`, err.Error())
	}

	return string(data)
}

// RunSpeedTest runs a speed test against a target peer
//
//export RunSpeedTest
func (c *ShileP2P) RunSpeedTest(targetPeerID string, fileSize int64, duration int) {
	c.speedTest.RunSpeedTest(targetPeerID, fileSize, duration)
}

// StopSpeedTest stops the current speed test
//
//export StopSpeedTest
func (c *ShileP2P) StopSpeedTest() {
	c.speedTest.StopSpeedTest()
}

// Bootstrap initiates the bootstrap process
//
//export Bootstrap
func (c *ShileP2P) Bootstrap() {
	go c.bootstrapFromMaster()
}

// TestHello returns a test message
//
//export TestHello
func (c *ShileP2P) TestHello() string {
	return fmt.Sprintf("Hello iOS! Server Current time: %s", time.Now().Format("2006-01-02 15:04:05"))
}
