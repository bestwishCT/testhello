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

// RunSpeedTest runs a speed test with the specified peer
//
//export RunSpeedTest
func (c *MobileClient) RunSpeedTest(targetPeerID string, fileSize int64, duration int) {
	pID, err := peer.Decode(targetPeerID)
	if err != nil {
		if c.onError != nil {
			c.onError(fmt.Sprintf("Invalid peer ID: %v", err))
		}
		return
	}

	// Check connection status
	if c.host.Network().Connectedness(pID) != network.Connected {
		if c.onError != nil {
			c.onError(fmt.Sprintf("Not connected to peer %s, please establish connection first", targetPeerID))
		}
		return
	}

	if c.onStatusChange != nil {
		c.onStatusChange(fmt.Sprintf("Starting speed test with peer %s...", targetPeerID))
	}

	// Start speed test
	go func() {
		ctx, cancel := context.WithTimeout(c.ctx, 10*time.Minute)
		defer cancel()

		// Run speed test
		result, err := c.speedTest.RunSpeedTest(ctx, pID, fileSize, 64*1024, duration)
		if err != nil {
			if c.onError != nil {
				c.onError(fmt.Sprintf("Speed test failed: %v", err))
			}
			return
		}

		// Format result
		resultStr := fmt.Sprintf(
			"Test ID: %s\nTotal data: %.2f MB\nDuration: %.2f seconds\nThroughput: %.2f MB/s\nNetwork rate: %.2f Mbps",
			result.TestID,
			float64(result.TotalBytes)/1024/1024,
			result.Duration,
			result.Throughput,
			result.MbpsThroughput,
		)

		if c.onSpeedTest != nil {
			c.onSpeedTest(resultStr)
		}
	}()
}

// StopSpeedTest cancels the ongoing speed test
//
//export StopSpeedTest
func (c *MobileClient) StopSpeedTest() {
	if c.speedTest != nil {
		c.speedTest.Stop()
	}
}

// startProxyServer starts a proxy server for the given protocol
func (c *MobileClient) startProxyServer(proxyPort int, protocol string) error {
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	fmt.Printf("正在启动%s代理服务器，监听地址: %s\n", protocol, proxyAddr)

	// 创建TCP监听器
	listener, err := net.Listen("tcp4", proxyAddr)
	if err != nil {
		return fmt.Errorf("创建TCP监听失败: %w", err)
	}

	// 在goroutine中启动代理服务器
	go func() {
		for {
			// 接受新的连接
			clientConn, err := listener.Accept()
			if err != nil {
				if c.ctx.Err() != nil {
					return
				}
				fmt.Printf("接受连接失败: %v\n", err)
				continue
			}

			// 打印接收到的连接信息
			remoteAddr := clientConn.RemoteAddr().String()
			localAddr := clientConn.LocalAddr().String()
			fmt.Printf("\n[%s代理] ==== 接收到新的连接请求 ====\n", protocol)
			fmt.Printf("[%s代理] 远程地址: %s\n", protocol, remoteAddr)
			fmt.Printf("[%s代理] 本地地址: %s\n", protocol, localAddr)

			// 每个连接一个goroutine处理
			go func(clientConn net.Conn) {
				defer func() {
					if r := recover(); r != nil {
						fmt.Printf("[%s代理] 处理连接时发生panic: %v\n", protocol, r)
						clientConn.Close()
					}
				}()

				defer clientConn.Close()

				// 获取Agent信息
				agentID, exists := c.getNextAgent()
				if !exists {
					fmt.Printf("[%s代理] 没有可用的Agent节点，关闭连接\n", protocol)
					clientConn.Write([]byte(fmt.Sprintf("HTTP/1.1 503 Service Unavailable\r\nContent-Length: %d\r\n\r\n%s",
						len("没有可用的Agent节点"), "没有可用的Agent节点")))
					return
				}

				// 创建到Agent的流
				ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
				defer cancel()

				// 根据协议类型选择不同的协议ID
				var protocolID protocol.ID
				switch protocol {
				case "quic-v1":
					protocolID = common.APIProxyProtocolID
				case "kcp":
					protocolID = common.TunnelProxyProtocolID
				default:
					fmt.Printf("[%s代理] 不支持的协议类型: %s\n", protocol, protocol)
					return
				}

				stream, err := c.host.NewStream(ctx, agentID, protocolID)
				if err != nil {
					fmt.Printf("[%s代理] 创建到Agent的流失败: %v\n", protocol, err)
					return
				}
				defer stream.Close()

				// 双向转发数据
				var wg sync.WaitGroup
				wg.Add(2)

				// 客户端 -> Agent
				go func() {
					defer wg.Done()
					io.Copy(stream, clientConn)
				}()

				// Agent -> 客户端
				go func() {
					defer wg.Done()
					io.Copy(clientConn, stream)
				}()

				wg.Wait()
			}(clientConn)
		}
	}()

	// 在上下文取消时关闭监听器
	go func() {
		<-c.ctx.Done()
		listener.Close()
		fmt.Printf("[%s代理] 代理服务器已关闭\n", protocol)
	}()

	return nil
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

// monitorAgents 监控已连接的Agent状态
func (c *MobileClient) monitorAgents() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			// 获取当前连接的Agent列表
			c.mu.RLock()
			agents := make([]peer.ID, 0, len(c.connectedAgents))
			for agentID := range c.connectedAgents {
				agents = append(agents, agentID)
			}
			c.mu.RUnlock()

			if len(agents) == 0 {
				if c.onStatusChange != nil {
					c.onStatusChange("当前没有连接的Agent")
				}
				continue
			}

			if c.onStatusChange != nil {
				c.onStatusChange(fmt.Sprintf("正在检查 %d 个Agent的连接状态...", len(agents)))
			}

			// 检查每个Agent的连接状态
			for _, agentID := range agents {
				// 检查连接状态
				if c.host.Network().Connectedness(agentID) != network.Connected {
					if c.onStatusChange != nil {
						c.onStatusChange(fmt.Sprintf("Agent %s 已断开连接，正在移除", agentID.String()))
					}

					// 移除断开的Agent
					c.mu.Lock()
					delete(c.connectedAgents, agentID)
					c.mu.Unlock()

					continue
				}

				// 发送心跳请求
				if c.onStatusChange != nil {
					c.onStatusChange(fmt.Sprintf("正在向Agent %s 发送心跳请求...", agentID.String()))
				}

				// 创建心跳请求
				heartbeatRequest := common.NewHeartbeatMessage("ping", common.TypeClient, c.host.ID().String())
				reqBytes, err := common.JSONMarshalWithNewline(heartbeatRequest)
				if err != nil {
					if c.onError != nil {
						c.onError(fmt.Sprintf("序列化心跳请求失败: %v", err))
					}
					continue
				}

				// 创建流连接
				ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
				s, err := c.host.NewStream(ctx, agentID, common.HeartbeatProtocolID)
				cancel()

				if err != nil {
					if c.onError != nil {
						c.onError(fmt.Sprintf("创建心跳流失败: %v", err))
					}
					continue
				}

				// 发送心跳请求
				_, err = s.Write(reqBytes)
				if err != nil {
					if c.onError != nil {
						c.onError(fmt.Sprintf("发送心跳请求失败: %v", err))
					}
					s.Close()
					continue
				}

				// 读取响应
				respBuf := make([]byte, 1024)
				n, err := s.Read(respBuf)
				s.Close()

				if err != nil {
					if c.onError != nil {
						c.onError(fmt.Sprintf("读取心跳响应失败: %v", err))
					}
					continue
				}

				// 解析响应
				var response common.HeartbeatMessage
				if err := json.Unmarshal(respBuf[:n], &response); err != nil {
					if c.onError != nil {
						c.onError(fmt.Sprintf("解析心跳响应失败: %v", err))
					}
					continue
				}

				// 检查响应
				if response.Action != "pong" {
					if c.onError != nil {
						c.onError(fmt.Sprintf("收到无效的心跳响应: %s", response.Action))
					}
					continue
				}

				// 更新Agent状态
				c.mu.Lock()
				c.connectedAgents[agentID] = time.Now()
				c.mu.Unlock()

				if c.onStatusChange != nil {
					c.onStatusChange(fmt.Sprintf("Agent %s 心跳正常", agentID.String()))
				}
			}

			// 更新Agent列表
			c.mu.RLock()
			agentList := make([]peer.ID, 0, len(c.connectedAgents))
			for agentID := range c.connectedAgents {
				agentList = append(agentList, agentID)
			}
			c.mu.RUnlock()

			if c.onAgentList != nil {
				agentListStr := make([]string, len(agentList))
				for i, agentID := range agentList {
					agentListStr[i] = agentID.String()
				}
				fmt.Printf("Updated agentList: %v\n", agentListStr)
				c.onAgentList(agentListStr)
			}
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

// discoverAndConnectPeers 发现并连接对等节点
func (c *MobileClient) discoverAndConnectPeers() {
	// 检查是否已经连接到Agent，并获取一个Agent
	var agentID peer.ID
	var exists bool

	// 直接调用getNextAgent，它内部会处理锁
	agentID, exists = c.getNextAgent()

	if !exists {
		if c.onStatusChange != nil {
			c.onStatusChange("未连接到Agent，无法发现对等节点")
		}
		return // 未连接到Agent，无法发现对等节点
	}

	// 从Agent获取对等节点信息
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()

	peers, err := c.discovery.QueryPeers(ctx, agentID)
	if err != nil {
		if c.onError != nil {
			c.onError(fmt.Sprintf("查询对等节点失败: %v", err))
		}
		return
	}

	if c.onStatusChange != nil {
		c.onStatusChange(fmt.Sprintf("发现了 %d 个对等节点", len(peers)))
	}

	// 尝试与每个对等节点建立连接
	for _, p := range peers {
		// 跳过自己和已连接的节点
		if p.PeerID == c.host.ID() {
			continue
		}

		c.mu.RLock()
		_, connected := c.connectedPeers[p.PeerID]
		c.mu.RUnlock()

		if connected {
			if c.onStatusChange != nil {
				c.onStatusChange(fmt.Sprintf("节点 %s 已经连接，跳过", p.PeerID.String()))
			}
			continue
		}

		// 尝试使用打洞建立直接连接
		go func(peerInfo *common.PeerInfo) {
			ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
			defer cancel()

			if c.onStatusChange != nil {
				c.onStatusChange(fmt.Sprintf("尝试连接到对等节点: %s", peerInfo.PeerID.String()))
			}

			// 解析并添加对方地址到peerstore
			targetPeerID, err := peer.Decode(peerInfo.PeerID.String())
			if err != nil {
				if c.onError != nil {
					c.onError(fmt.Sprintf("解析对等节点ID失败: %v", err))
				}
				return
			}

			// 添加对方的地址到peerstore
			if len(peerInfo.Multiaddrs) > 0 {
				if c.onStatusChange != nil {
					c.onStatusChange(fmt.Sprintf("将节点 %s 的 %d 个地址添加到peerstore",
						peerInfo.PeerID.String(), len(peerInfo.Multiaddrs)))
				}

				addedCount := 0
				for _, addrStr := range peerInfo.Multiaddrs {
					addr, err := multiaddr.NewMultiaddr(addrStr)
					if err != nil {
						if c.onError != nil {
							c.onError(fmt.Sprintf("解析地址失败: %s - %v", addrStr, err))
						}
						continue
					}

					// 添加地址到peerstore，设置有效期为1小时
					c.host.Peerstore().AddAddr(targetPeerID, addr, time.Hour)
					addedCount++
				}

				if c.onStatusChange != nil {
					c.onStatusChange(fmt.Sprintf("成功添加 %d 个地址", addedCount))
				}
			} else {
				if c.onError != nil {
					c.onError(fmt.Sprintf("警告: 节点 %s 没有可用地址", peerInfo.PeerID.String()))
				}
			}

			err = c.holePunch.RequestHolePunch(ctx, peerInfo.PeerID)
			if err != nil {
				if c.onError != nil {
					c.onError(fmt.Sprintf("连接到对等节点失败: %v", err))
				}
			} else {
				if c.onStatusChange != nil {
					c.onStatusChange(fmt.Sprintf("成功连接到对等节点: %s", peerInfo.PeerID.String()))
				}

				// 打印连接详情
				conns := c.host.Network().ConnsToPeer(peerInfo.PeerID)
				if len(conns) > 0 && c.onStatusChange != nil {
					c.onStatusChange(fmt.Sprintf("与节点 %s 建立了 %d 个连接", peerInfo.PeerID.String(), len(conns)))
					for i, conn := range conns {
						localAddr := conn.LocalMultiaddr()
						remoteAddr := conn.RemoteMultiaddr()
						c.onStatusChange(fmt.Sprintf("连接 #%d: %s -> %s", i+1,
							localAddr.String(), remoteAddr.String()))
					}
				}
			}
		}(p)
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
