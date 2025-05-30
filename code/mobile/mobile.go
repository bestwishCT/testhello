package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/multiformats/go-multiaddr"

	kcp "github.com/libp2p/go-libp2p/p2p/transport/kcp"

	protoco "github.com/libp2p/go-libp2p/core/protocol"

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

	// Start speed test service
	c.speedTest.Start()

	// Connect to Master and get Agent
	go c.bootstrapFromMaster()

	// Start listening for hole punch requests
	go c.holePunch.ListenForHolePunchRequests(c.ctx)

	// Start peer discovery loop
	go c.peerDiscoveryLoop()

	// Start agent monitoring
	go c.monitorAgents()

	// Check if proxy server is already started
	c.proxyMu.Lock()
	if !c.proxyStarted {
		// Start API proxy server
		if c.onStatusChange != nil {
			c.onStatusChange(fmt.Sprintf("Starting API proxy server, listening on: 127.0.0.1:%d", apiPort))
		}
		err := c.startProxyServer(apiPort, c.apiProtocol)
		if err != nil {
			c.proxyMu.Unlock()
			return fmt.Errorf("failed to start API proxy server: %w", err)
		}

		// Start tunnel proxy server
		if c.onStatusChange != nil {
			c.onStatusChange(fmt.Sprintf("Starting tunnel proxy server, listening on: 127.0.0.1:%d", tunnelPort))
		}
		err = c.startProxyServer(tunnelPort, c.tunnelProtocol)
		if err != nil {
			c.proxyMu.Unlock()
			return fmt.Errorf("failed to start tunnel proxy server: %w", err)
		}

		c.proxyStarted = true
		if c.onStatusChange != nil {
			c.onStatusChange("Proxy servers started successfully")
		}
	} else {
		if c.onStatusChange != nil {
			c.onStatusChange("Proxy servers already running, skipping start")
		}
	}
	c.proxyMu.Unlock()

	if c.onStatusChange != nil {
		c.onStatusChange("Client node started")
	}
	return nil
}

// TestHello returns a hello message with current time
//
//export TestHello
func (c *MobileClient) TestHello() string {
	return fmt.Sprintf("Hello Android! Server Current time: %s", time.Now().Format("2006-01-02 15:04:05"))
}

// Stop stops the mobile client
//
//export Stop
func (c *MobileClient) Stop() error {
	// Stop discovery service
	c.discovery.Stop()

	// Stop hole punch coordinator
	c.holePunch.Stop()

	// Stop heartbeat service
	c.heartbeat.Stop()

	// Stop speed test service
	c.speedTest.Stop()

	// Close P2P node
	if err := c.host.Close(); err != nil {
		return fmt.Errorf("failed to close P2P node: %w", err)
	}

	// Cancel context
	c.cancel()

	if c.onStatusChange != nil {
		c.onStatusChange("Mobile client stopped")
	}

	return nil
}

// GetConnectedAgents returns the list of connected agents as a JSON string
//
//export GetConnectedAgents
func (c *MobileClient) GetConnectedAgents() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 打印当前连接的Agent数量
	fmt.Printf("GetConnectedAgents: 当前连接的Agent数量: %d\n", len(c.connectedAgents))

	// 使用agentList而不是connectedAgents
	fmt.Printf("GetConnectedAgents: agentList中的Agent数量: %d\n", len(c.agentList))

	// 如果agentList为空，尝试从connectedAgents更新
	if len(c.agentList) == 0 && len(c.connectedAgents) > 0 {
		fmt.Printf("GetConnectedAgents: agentList为空，尝试从connectedAgents更新\n")
		c.mu.RUnlock()
		c.mu.Lock()
		c.agentList = make([]peer.ID, 0, len(c.connectedAgents))
		for agentID := range c.connectedAgents {
			c.agentList = append(c.agentList, agentID)
			fmt.Printf("GetConnectedAgents: 从connectedAgents添加Agent: %s\n", agentID.String())
		}
		c.mu.Unlock()
		c.mu.RLock()
	}

	agents := make([]string, 0, len(c.agentList))
	for _, agentID := range c.agentList {
		agents = append(agents, agentID.String())
		fmt.Printf("GetConnectedAgents: 添加Agent: %s\n", agentID.String())
	}

	data, err := json.Marshal(agents)
	if err != nil {
		fmt.Printf("GetConnectedAgents: JSON序列化失败: %v\n", err)
		return "[]"
	}

	fmt.Printf("GetConnectedAgents: 返回JSON: %s\n", string(data))
	return string(data)
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

// Bootstrap initiates the bootstrap process
//
//export Bootstrap
func (c *MobileClient) Bootstrap() {
	if c.onStatusChange != nil {
		c.onStatusChange("Getting agent list from Master...")
	}

	// 打印当前状态
	c.mu.RLock()
	fmt.Printf("Bootstrap: 当前agentList长度: %d\n", len(c.agentList))
	fmt.Printf("Bootstrap: 当前connectedAgents长度: %d\n", len(c.connectedAgents))
	c.mu.RUnlock()

	// 清空现有的agentList
	c.mu.Lock()
	c.agentList = make([]peer.ID, 0)
	c.mu.Unlock()

	if c.onStatusChange != nil {
		c.onStatusChange("已清空现有Agent列表，开始重新获取...")
	}

	go c.bootstrapFromMaster()
}

// 以下是内部实现方法
func (c *MobileClient) bootstrapFromMaster() {
	retryBackoff := time.Second * 5 // 初始重试间隔5秒
	maxBackoff := time.Minute * 3   // 最大重试间隔3分钟
	maxRetries := 10                // 最大连续重试次数，超过后增加等待时间

	var consecutiveFailures int

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			// 检查是否已经连接到Agent - 减少锁持有时间
			var agentCount int
			c.mu.RLock()
			agentCount = len(c.connectedAgents)
			c.mu.RUnlock()

			if agentCount > 0 {
				// 已有Agent连接，等待一段时间后再检查
				consecutiveFailures = 0 // 重置失败计数
				if c.onStatusChange != nil {
					c.onStatusChange(fmt.Sprintf("已连接到 %d 个Agent，等待30秒后重新检查", agentCount))
				}
				time.Sleep(30 * time.Second)
				continue
			}

			// 检查失败次数，如果连续失败多次，增加等待时间
			if consecutiveFailures >= maxRetries {
				waitTime := retryBackoff * time.Duration(consecutiveFailures/maxRetries+1)
				if waitTime > maxBackoff {
					waitTime = maxBackoff
				}
				if c.onStatusChange != nil {
					c.onStatusChange(fmt.Sprintf("已连续失败 %d 次，等待 %v 后重试", consecutiveFailures, waitTime))
				}
				time.Sleep(waitTime)
			}

			// 1. 查询Master的PeerID
			if c.onStatusChange != nil {
				c.onStatusChange("正在查询Master节点的PeerID...")
			}
			masterPeerID, err := discovery.GetMasterPeerID(c.ctx)
			if err != nil {
				if c.onError != nil {
					c.onError(fmt.Sprintf("获取Master节点PeerID失败: %v，将在%s后重试", err, retryBackoff))
				}
				consecutiveFailures++
				time.Sleep(retryBackoff)
				continue
			}

			// 2. 获取Master的Multiaddr
			if c.onStatusChange != nil {
				c.onStatusChange("正在获取Master节点的地址信息...")
			}
			masterAddr, err := discovery.GetMasterMultiaddr(masterPeerID)
			if err != nil {
				if c.onError != nil {
					c.onError(fmt.Sprintf("构建Master节点地址失败: %v，将在%s后重试", err, retryBackoff))
				}
				consecutiveFailures++
				time.Sleep(retryBackoff)
				continue
			}

			// 3. 连接到Master
			if c.onStatusChange != nil {
				c.onStatusChange(fmt.Sprintf("尝试连接Master节点地址: %s", masterAddr.String()))
				c.onStatusChange(fmt.Sprintf("Master节点ID: %s", masterPeerID.String()))
			}
			err = common.ConnectToPeer(c.ctx, c.host, masterPeerID, masterAddr)
			if err != nil {
				if c.onError != nil {
					c.onError(fmt.Sprintf("连接到Master节点失败: %v，将在%s后重试", err, retryBackoff))
				}
				consecutiveFailures++
				time.Sleep(retryBackoff)
				continue
			}

			if c.onStatusChange != nil {
				c.onStatusChange("成功连接到Master节点")
			}

			// 打印连接信息
			conns := c.host.Network().ConnsToPeer(masterPeerID)
			if len(conns) > 0 {
				if c.onStatusChange != nil {
					c.onStatusChange(fmt.Sprintf("与Master节点建立了 %d 个连接", len(conns)))
					for i, conn := range conns {
						c.onStatusChange(fmt.Sprintf("连接 #%d: %s -> %s", i+1,
							conn.LocalMultiaddr().String(),
							conn.RemoteMultiaddr().String()))
					}
				}
			}

			// 4. 从Master获取Agent信息
			if c.onStatusChange != nil {
				c.onStatusChange("已连接到Master节点，正在获取Agent信息...")
			}

			// 创建一个变量来表示是否需要重试
			var needRetry bool
			for retryCount := 0; retryCount < 3; retryCount++ {
				if retryCount > 0 {
					if c.onStatusChange != nil {
						c.onStatusChange(fmt.Sprintf("第 %d 次重试获取Agent信息...", retryCount+1))
					}
					time.Sleep(time.Second * time.Duration(retryCount+1))
				}

				response, err := c.getAgentFromMaster(masterPeerID)
				if err != nil {
					if c.onError != nil {
						c.onError(fmt.Sprintf("获取Agent信息失败: %v", err))
					}
					needRetry = true
					continue
				}

				// 检查响应状态
				if response.Status != "success" {
					if c.onError != nil {
						c.onError(fmt.Sprintf("Master返回错误状态: %s, 消息: %s", response.Status, response.Message))
					}
					needRetry = true
					continue
				}

				// 检查Agent列表
				if len(response.Agents) == 0 {
					if c.onError != nil {
						c.onError("Master返回的Agent列表为空")
					}
					needRetry = true
					continue
				}

				// 成功获取Agent列表，尝试连接
				if c.onStatusChange != nil {
					c.onStatusChange(fmt.Sprintf("成功获取到 %d 个Agent信息，开始尝试连接...", len(response.Agents)))
				}

				// 清空现有的agentList
				c.mu.Lock()
				c.agentList = make([]peer.ID, 0)
				c.mu.Unlock()

				// 尝试连接到每个Agent
				connectedCount := 0
				for _, agent := range response.Agents {
					agentPeerID, err := peer.Decode(agent.PeerID)
					if err != nil {
						if c.onError != nil {
							c.onError(fmt.Sprintf("解析Agent PeerID失败: %v", err))
						}
						continue
					}

					// 检查是否已经连接
					if c.host.Network().Connectedness(agentPeerID) == network.Connected {
						if c.onStatusChange != nil {
							c.onStatusChange(fmt.Sprintf("已经连接到Agent: %s", agent.PeerID))
						}
						c.mu.Lock()
						c.agentList = append(c.agentList, agentPeerID)
						c.connectedAgents[agentPeerID] = time.Now()
						c.mu.Unlock()
						connectedCount++
						continue
					}

					// 尝试连接
					if c.onStatusChange != nil {
						c.onStatusChange(fmt.Sprintf("正在连接Agent: %s", agent.PeerID))
					}

					// 解析并连接每个地址
					for _, addrStr := range agent.Multiaddrs {
						addr, err := multiaddr.NewMultiaddr(addrStr)
						if err != nil {
							if c.onError != nil {
								c.onError(fmt.Sprintf("解析Agent地址失败: %v", err))
							}
							continue
						}

						err = common.ConnectToPeer(c.ctx, c.host, agentPeerID, addr)
						if err != nil {
							if c.onError != nil {
								c.onError(fmt.Sprintf("连接到Agent地址失败: %v", err))
							}
							continue
						}

						if c.onStatusChange != nil {
							c.onStatusChange(fmt.Sprintf("成功连接到Agent地址: %s", addr.String()))
						}
						c.mu.Lock()
						c.agentList = append(c.agentList, agentPeerID)
						c.connectedAgents[agentPeerID] = time.Now()
						c.mu.Unlock()
						connectedCount++
						break // 成功连接一个地址就足够了
					}
				}

				if connectedCount > 0 {
					if c.onStatusChange != nil {
						c.onStatusChange(fmt.Sprintf("成功连接到 %d 个Agent", connectedCount))
					}
					// 打印当前agentList状态
					c.mu.RLock()
					fmt.Printf("bootstrapFromMaster: 当前agentList长度: %d\n", len(c.agentList))
					for i, agentID := range c.agentList {
						fmt.Printf("bootstrapFromMaster: agentList[%d]: %s\n", i, agentID.String())
					}
					c.mu.RUnlock()
					consecutiveFailures = 0 // 重置失败计数
					needRetry = false
					break
				} else {
					if c.onError != nil {
						c.onError("未能成功连接到任何Agent")
					}
					needRetry = true
				}
			}

			if needRetry {
				consecutiveFailures++
				if c.onStatusChange != nil {
					c.onStatusChange(fmt.Sprintf("所有重试都失败，将在%s后重新开始", retryBackoff))
				}
				time.Sleep(retryBackoff)
				continue
			}

			// 成功连接到至少一个Agent，等待一段时间后继续检查
			time.Sleep(30 * time.Second)
		}
	}
}

func (c *MobileClient) peerDiscoveryLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// 先立即执行一次，不等待第一个ticker
	c.discoverAndConnectPeers()

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

func (c *MobileClient) printConnectedAgents() {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.onStatusChange != nil {
		c.onStatusChange("\n========== 当前已连接的Agent列表 ==========")
		if len(c.agentList) == 0 {
			c.onStatusChange("没有连接到任何Agent")
		} else {
			for i, agentID := range c.agentList {
				c.onStatusChange(fmt.Sprintf("Agent #%d: %s", i+1, agentID.String()))
				conns := c.host.Network().ConnsToPeer(agentID)
				c.onStatusChange(fmt.Sprintf("  连接数: %d", len(conns)))
				for j, conn := range conns {
					c.onStatusChange(fmt.Sprintf("  连接 #%d: %s -> %s", j+1,
						conn.LocalMultiaddr().String(),
						conn.RemoteMultiaddr().String()))
				}
			}
		}
		c.onStatusChange("===========================================")
	}
}

func (c *MobileClient) getNextAgent() (peer.ID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.agentList) == 0 {
		return "", false
	}

	if c.currentAgentIndex >= len(c.agentList) {
		c.currentAgentIndex = 0
	}

	agentID := c.agentList[c.currentAgentIndex]
	c.currentAgentIndex++

	return agentID, true
}

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
				var protocolID protoco.ID
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

// getAgentFromMaster 从Master获取Agent信息
func (c *MobileClient) getAgentFromMaster(masterPeerID peer.ID) (*common.BootstrapResponse, error) {
	// 使用标准的引导协议
	protocol := protoco.ID(common.BootstrapProtocolID)

	// 创建与Master的流连接
	if c.onStatusChange != nil {
		c.onStatusChange(fmt.Sprintf("尝试使用引导协议: %s 来连接Master...", protocol))
	}
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()

	s, err := c.host.NewStream(ctx, masterPeerID, protocol)
	if err != nil {
		if c.onError != nil {
			c.onError(fmt.Sprintf("创建流失败: %v", err))
		}
		return nil, fmt.Errorf("无法创建到Master的流: %w", err)
	}

	// 确保在函数结束时关闭流
	defer s.Close()

	// 打印连接状态
	if c.onStatusChange != nil {
		c.onStatusChange(fmt.Sprintf("已建立到Master的流连接，远程地址: %s", s.Conn().RemoteMultiaddr()))
	}

	// 发送引导请求
	if c.onStatusChange != nil {
		c.onStatusChange("正在向Master发送引导请求...")
	}
	// 创建请求对象 - 使用统一的请求构造函数
	bootstrapRequest := common.NewStandardRequest("bootstrap", common.TypeClient, c.host.ID().String())

	// 序列化并发送请求
	reqBytes, err := common.JSONMarshalWithNewline(bootstrapRequest)
	if err != nil {
		return nil, fmt.Errorf("序列化引导请求失败: %w", err)
	}

	if c.onStatusChange != nil {
		c.onStatusChange(fmt.Sprintf("请求JSON: %s", string(reqBytes)))
	}

	_, err = s.Write(reqBytes)
	if err != nil {
		return nil, fmt.Errorf("发送引导请求失败: %w", err)
	}

	if c.onStatusChange != nil {
		c.onStatusChange("已发送引导请求，等待Master响应...")
	}

	// 设置读取超时前先刷新流
	if f, ok := s.(interface{ Flush() error }); ok {
		if err := f.Flush(); err != nil {
			if c.onError != nil {
				c.onError(fmt.Sprintf("刷新流失败: %v，但将继续尝试读取响应", err))
			}
		} else {
			if c.onStatusChange != nil {
				c.onStatusChange("成功刷新流")
			}
		}
	}

	// 读取响应，设置更长的超时时间
	s.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer s.SetReadDeadline(time.Time{})

	// 读取响应数据
	respBuf := make([]byte, 16384) // 使用更大的缓冲区
	n, err := s.Read(respBuf)
	if err != nil {
		if err == io.EOF && n > 0 {
			if c.onStatusChange != nil {
				c.onStatusChange(fmt.Sprintf("虽然收到EOF，但读取了%d字节数据，将尝试处理", n))
			}
		} else {
			return nil, fmt.Errorf("读取Master响应失败: %w", err)
		}
	}

	if n == 0 {
		return nil, fmt.Errorf("收到空响应")
	}

	if c.onStatusChange != nil {
		c.onStatusChange(fmt.Sprintf("收到响应: %d 字节", n))
	}

	// 尝试解析JSON响应
	var response common.BootstrapResponse
	if err := json.Unmarshal(respBuf[:n], &response); err != nil {
		// 如果出错，尝试更宽松的处理
		jsonData := string(respBuf[:n])
		jsonData = strings.TrimSpace(jsonData)

		// 确保是完整的JSON对象
		startIdx := strings.Index(jsonData, "{")
		endIdx := strings.LastIndex(jsonData, "}")
		if startIdx >= 0 && endIdx > startIdx {
			jsonData = jsonData[startIdx : endIdx+1]
			// 尝试再次解析
			if err := json.Unmarshal([]byte(jsonData), &response); err != nil {
				if c.onError != nil {
					c.onError(fmt.Sprintf("JSON解析错误: %v", err))
				}
				return nil, fmt.Errorf("解析Master响应失败: %w", err)
			}
		} else {
			if c.onError != nil {
				c.onError(fmt.Sprintf("JSON解析错误: %v", err))
			}
			return nil, fmt.Errorf("解析Master响应失败: %w", err)
		}
	}

	// 检查是否有错误信息
	if response.Error != "" {
		return nil, fmt.Errorf("Master返回错误: %s", response.Error)
	}

	// 检查响应是否有效
	if len(response.Agents) == 0 {
		return nil, fmt.Errorf("Master返回的Agent列表为空")
	}

	// 打印获取的Agent信息
	if c.onStatusChange != nil {
		c.onStatusChange(fmt.Sprintf("成功获取Master响应: 共%d个Agent", len(response.Agents)))
		for i, agent := range response.Agents {
			c.onStatusChange(fmt.Sprintf("  Agent #%d: %s, 地址数=%d", i+1, agent.PeerID, len(agent.Multiaddrs)))
		}
	}

	return &response, nil
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

// StopSpeedTest cancels the ongoing speed test
//
//export StopSpeedTest
func (c *MobileClient) StopSpeedTest() {
	if c.speedTest != nil {
		c.speedTest.Stop()
	}
}
