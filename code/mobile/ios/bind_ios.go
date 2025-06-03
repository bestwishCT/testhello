package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/multiformats/go-multiaddr"

	kcp "github.com/libp2p/go-libp2p/p2p/transport/kcp"

	"shiledp2p/pkg/common"
	"shiledp2p/pkg/discovery"
	"shiledp2p/pkg/nat"
	"shiledp2p/pkg/speedtest"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	_ "github.com/libp2p/go-libp2p/p2p/transport/kcp"
)

// MobileClient represents a mobile P2P client
//
//export MobileClient
type MobileClient struct {
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

// NewMobileClient creates a new mobile P2P client
//
//export NewMobileClient
func NewMobileClient(listenQuicAddr, listenKcpAddr string) (*MobileClient, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Parse QUIC listen address
	quicAddr, err := multiaddr.NewMultiaddr(listenQuicAddr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to parse QUIC listen address: %w", err)
	}

	// Parse KCP listen address
	kcpAddr, err := multiaddr.NewMultiaddr(listenKcpAddr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to parse KCP listen address: %w", err)
	}

	// Build libp2p options
	opts := []libp2p.Option{
		libp2p.ListenAddrs(quicAddr, kcpAddr),
		libp2p.DefaultTransports,
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
		libp2p.Transport(kcp.NewTransport),
	}

	// Create P2P node
	h, err := libp2p.New(opts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create P2P node: %w", err)
	}

	// Create and return MobileClient instance
	client := &MobileClient{
		host:            h,
		discovery:       discovery.NewPeerDiscovery(h, common.TypeClient),
		holePunch:       nat.NewHolePunchCoordinator(h),
		heartbeat:       common.NewHeartbeatService(h, 5*time.Second, 10*time.Second),
		speedTest:       speedtest.NewSpeedTest(h),
		ctx:             ctx,
		cancel:          cancel,
		connectedAgents: make(map[peer.ID]time.Time),
		connectedPeers:  make(map[peer.ID]time.Time),
		agentList:       make([]peer.ID, 0),
		apiProtocol:     "quic-v1",
		tunnelProtocol:  "kcp",
		agentInfo:       make(map[peer.ID][]multiaddr.Multiaddr),
		agents:          make(map[peer.ID]*common.AgentInfo),
	}

	// Set as Client node type
	client.heartbeat.SetNodeType(common.TypeClient)

	// Register connection notification handler
	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(n network.Network, c network.Conn) {
			remotePeerID := c.RemotePeer()
			client.mu.Lock()
			_, exists := client.connectedPeers[remotePeerID]
			if !exists {
				client.connectedPeers[remotePeerID] = time.Now()
				if client.onStatusChange != nil {
					client.onStatusChange(fmt.Sprintf("New peer connection established: %s", remotePeerID.String()))
				}
			}
			client.mu.Unlock()
		},
		DisconnectedF: func(n network.Network, c network.Conn) {
			remotePeerID := c.RemotePeer()
			client.mu.Lock()
			if client.connectedAgents[remotePeerID] != (time.Time{}) {
				if client.onStatusChange != nil {
					client.onStatusChange(fmt.Sprintf("Agent connection disconnected: %s", remotePeerID.String()))
				}
				delete(client.connectedAgents, remotePeerID)
			}
			delete(client.connectedPeers, remotePeerID)
			client.mu.Unlock()
		},
	})

	return client, nil
}

// SetStatusChangeCallback sets the callback for status changes
//
//export SetStatusChangeCallback
func (c *MobileClient) SetStatusChangeCallback(callback func(status string)) {
	c.onStatusChange = callback
}

// SetErrorCallback sets the callback for error messages
//
//export SetErrorCallback
func (c *MobileClient) SetErrorCallback(callback func(err string)) {
	c.onError = callback
}

// SetAgentListCallback sets the callback for agent list updates
//
//export SetAgentListCallback
func (c *MobileClient) SetAgentListCallback(callback func(agents []string)) {
	c.onAgentList = callback
}

// SetSpeedTestCallback sets the callback for speed test results
//
//export SetSpeedTestCallback
func (c *MobileClient) SetSpeedTestCallback(callback func(result string)) {
	c.onSpeedTest = callback
}

// Start starts the mobile client
//
//export Start
func (c *MobileClient) Start(apiPort, tunnelPort int) error {
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
func (c *MobileClient) Stop() error {
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
func (c *MobileClient) GetConnectedAgents() string {
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
func (c *MobileClient) RunSpeedTest(targetPeerID string, fileSize int64, duration int) {
	c.speedTest.RunSpeedTest(targetPeerID, fileSize, duration)
}

// StopSpeedTest stops the current speed test
//
//export StopSpeedTest
func (c *MobileClient) StopSpeedTest() {
	c.speedTest.StopSpeedTest()
}

// Bootstrap initiates the bootstrap process
//
//export Bootstrap
func (c *MobileClient) Bootstrap() {
	go c.bootstrapFromMaster()
}

// TestHello returns a test message
//
//export TestHello
func (c *MobileClient) TestHello() string {
	return fmt.Sprintf("Hello iOS! Server Current time: %s", time.Now().Format("2006-01-02 15:04:05"))
}
