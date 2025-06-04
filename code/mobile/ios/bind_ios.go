package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

// MobileClient represents a mobile P2P client for iOS
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

	// Callback functions for iOS platform
	onStatusChange func(status string)
	onError        func(err string)
	onAgentList    func(agents []string)
	onSpeedTest    func(result string)

	agents map[peer.ID]*common.AgentInfo
}

// NewMobileClient creates a new mobile P2P client for iOS
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

	// Start speed test service
	c.speedTest.Start()

	// Start proxy servers
	if err := c.startProxyServer(apiPort, c.apiProtocol); err != nil {
		return fmt.Errorf("failed to start API proxy server: %w", err)
	}
	c.apiProxyPort = apiPort

	if err := c.startProxyServer(tunnelPort, c.tunnelProtocol); err != nil {
		return fmt.Errorf("failed to start tunnel proxy server: %w", err)
	}
	c.tunnelProxyPort = tunnelPort

	// Start bootstrap process
	go c.Bootstrap()

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
		if err := c.host.Close(); err != nil {
			return fmt.Errorf("failed to close host: %w", err)
		}
	}

	return nil
}

// GetConnectedAgents returns the list of connected agents
//
//export GetConnectedAgents
func (c *MobileClient) GetConnectedAgents() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	agents := make([]string, 0, len(c.connectedAgents))
	for agentID := range c.connectedAgents {
		agents = append(agents, agentID.String())
	}

	result, err := json.Marshal(agents)
	if err != nil {
		return "[]"
	}

	return string(result)
}

// Bootstrap starts the bootstrap process
//
//export Bootstrap
func (c *MobileClient) Bootstrap() {
	// Start peer discovery loop
	go c.peerDiscoveryLoop()

	// Start bootstrap from master
	go c.bootstrapFromMaster()
}

// RunSpeedTest runs a speed test against a target peer
//
//export RunSpeedTest
func (c *MobileClient) RunSpeedTest(targetPeerID string, fileSize int64, duration int) {
	// TODO: Implement speed test
	if c.onError != nil {
		c.onError("Speed test not implemented yet")
	}
}

// StopSpeedTest stops the current speed test
//
//export StopSpeedTest
func (c *MobileClient) StopSpeedTest() {
	// TODO: Implement stop speed test
}

// startProxyServer starts a proxy server for the given protocol
func (c *MobileClient) startProxyServer(proxyPort int, protocol string) error {
	c.proxyMu.Lock()
	defer c.proxyMu.Unlock()

	if c.proxyStarted {
		return fmt.Errorf("proxy server already started")
	}

	// Create listener
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", proxyPort))
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	c.proxyStarted = true
	c.proxyPort = proxyPort

	// Start proxy server in a goroutine
	go func() {
		defer listener.Close()
		for {
			select {
			case <-c.ctx.Done():
				return
			default:
				conn, err := listener.Accept()
				if err != nil {
					if c.onError != nil {
						c.onError(fmt.Sprintf("Failed to accept connection: %v", err))
					}
					continue
				}

				go c.handleProxyConnection(conn, protocol)
			}
		}
	}()

	return nil
}

// handleProxyConnection handles a proxy connection
func (c *MobileClient) handleProxyConnection(conn net.Conn, protocol string) {
	defer conn.Close()

	// Get next available agent
	agentID, ok := c.getNextAgent()
	if !ok {
		if c.onError != nil {
			c.onError("No available agents")
		}
		return
	}

	// Create stream to agent
	stream, err := c.host.NewStream(c.ctx, agentID, common.ProxyProtocolID)
	if err != nil {
		if c.onError != nil {
			c.onError(fmt.Sprintf("Failed to create stream: %v", err))
		}
		return
	}
	defer stream.Close()

	// Start bidirectional copy
	go func() {
		_, err := io.Copy(stream, conn)
		if err != nil && c.onError != nil {
			c.onError(fmt.Sprintf("Failed to copy from conn to stream: %v", err))
		}
	}()

	_, err = io.Copy(conn, stream)
	if err != nil && c.onError != nil {
		c.onError(fmt.Sprintf("Failed to copy from stream to conn: %v", err))
	}
}

// getNextAgent returns the next available agent
func (c *MobileClient) getNextAgent() (peer.ID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.agentList) == 0 {
		return "", false
	}

	agentID := c.agentList[c.currentAgentIndex]
	c.currentAgentIndex = (c.currentAgentIndex + 1) % len(c.agentList)

	return agentID, true
}

// monitorAgents monitors the connected agents
func (c *MobileClient) monitorAgents() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			now := time.Now()
			for agentID, lastSeen := range c.connectedAgents {
				if now.Sub(lastSeen) > 30*time.Second {
					delete(c.connectedAgents, agentID)
					if c.onStatusChange != nil {
						c.onStatusChange(fmt.Sprintf("Agent timed out: %s", agentID.String()))
					}
				}
			}
			c.mu.Unlock()
		}
	}
}

// peerDiscoveryLoop runs the peer discovery loop
func (c *MobileClient) peerDiscoveryLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.discoverAndConnectPeers()
		}
	}
}

// discoverAndConnectPeers discovers and connects to peers
func (c *MobileClient) discoverAndConnectPeers() {
	peers := c.discovery.GetPeers()
	for _, peerInfo := range peers {
		if c.host.Network().Connectedness(peerInfo.PeerID) != network.Connected {
			addrInfo := peer.AddrInfo{
				ID:    peerInfo.PeerID,
				Addrs: make([]multiaddr.Multiaddr, 0, len(peerInfo.Multiaddrs)),
			}
			for _, addrStr := range peerInfo.Multiaddrs {
				if addr, err := multiaddr.NewMultiaddr(addrStr); err == nil {
					addrInfo.Addrs = append(addrInfo.Addrs, addr)
				}
			}
			if err := c.host.Connect(c.ctx, addrInfo); err != nil {
				if c.onError != nil {
					c.onError(fmt.Sprintf("Failed to connect to peer %s: %v", peerInfo.PeerID.String(), err))
				}
			}
		}
	}
}

// bootstrapFromMaster bootstraps from the master node
func (c *MobileClient) bootstrapFromMaster() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			peers := c.discovery.GetPeers()
			for _, peerInfo := range peers {
				response, err := c.getAgentFromMaster(peerInfo.PeerID)
				if err != nil {
					continue
				}

				c.mu.Lock()
				for _, agent := range response.Agents {
					agentID, err := peer.Decode(agent.PeerID)
					if err != nil {
						continue
					}

					// Add agent to list if not already present
					found := false
					for _, existingID := range c.agentList {
						if existingID == agentID {
							found = true
							break
						}
					}
					if !found {
						c.agentList = append(c.agentList, agentID)
					}

					// Update agent info
					c.agents[agentID] = &common.AgentInfo{
						PeerID:     agent.PeerID,
						Multiaddrs: agent.Multiaddrs,
					}
				}
				c.mu.Unlock()

				// Notify agent list update
				if c.onAgentList != nil {
					agentIDs := make([]string, 0, len(c.agentList))
					for _, agentID := range c.agentList {
						agentIDs = append(agentIDs, agentID.String())
					}
					c.onAgentList(agentIDs)
				}

				break
			}
		}
	}
}

// getAgentFromMaster gets agent list from master
func (c *MobileClient) getAgentFromMaster(masterPeerID peer.ID) (*common.BootstrapResponse, error) {
	stream, err := c.host.NewStream(c.ctx, masterPeerID, common.BootstrapProtocolID)
	if err != nil {
		return nil, fmt.Errorf("failed to create stream: %w", err)
	}
	defer stream.Close()

	// Send bootstrap request
	request := &common.StandardRequest{
		Action:    "bootstrap",
		NodeType:  common.TypeClient,
		From:      c.host.ID().String(),
		Timestamp: time.Now().UnixNano(),
	}
	if err := json.NewEncoder(stream).Encode(request); err != nil {
		return nil, fmt.Errorf("failed to send bootstrap request: %w", err)
	}

	// Read bootstrap response
	var response common.BootstrapResponse
	if err := json.NewDecoder(stream).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to read bootstrap response: %w", err)
	}

	return &response, nil
}
