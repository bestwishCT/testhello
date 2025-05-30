package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	protoco "github.com/libp2p/go-libp2p/core/protocol"

	_ "github.com/libp2p/go-libp2p/p2p/transport/kcp"
	"github.com/multiformats/go-multiaddr"

	kcp "github.com/libp2p/go-libp2p/p2p/transport/kcp"
	"shiledp2p/pkg/common"
	"shiledp2p/pkg/discovery"
	"shiledp2p/pkg/nat"
	"shiledp2p/pkg/speedtest"
)

// Client 定义客户端对象
type Client struct {
	host      host.Host
	discovery *discovery.PeerDiscovery
	holePunch *nat.HolePunchCoordinator
	heartbeat *common.HeartbeatService
	speedTest *speedtest.SpeedTest

	ctx    context.Context
	cancel context.CancelFunc

	mu              sync.RWMutex
	connectedAgents map[peer.ID]time.Time // 修改为支持多个Agent连接
	connectedPeers  map[peer.ID]time.Time
	sourcePort      int // 与Agent通信的源端口

	currentAgentIndex int       // 当前使用的Agent索引，用于轮询
	agentList         []peer.ID // 所有可用的Agent列表

	proxyStarted bool       // 标记代理服务器是否已启动
	proxyMu      sync.Mutex // 用于保护proxyStarted

	proxyPort       int
	proxyTargetHost string
	proxyTargetPort string
	masterID        peer.ID
	agentInfo       map[peer.ID][]multiaddr.Multiaddr

	// 新增字段
	apiProxyPort    int    // API代理端口
	tunnelProxyPort int    // 隧道代理端口
	apiProtocol     string // API协议类型
	tunnelProtocol  string // 隧道协议类型
}

// NewClient 创建一个新的客户端
func NewClient(listenQuicAddr, listenKcpAddr string, reusePort bool) (*Client, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// 解析QUIC监听地址
	quicAddr, err := multiaddr.NewMultiaddr(listenQuicAddr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("解析QUIC监听地址失败: %w", err)
	}

	// 解析KCP监听地址
	kcpAddr, err := multiaddr.NewMultiaddr(listenKcpAddr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("解析KCP监听地址失败: %w", err)
	}

	// 从监听地址中提取端口号
	var port int
	parts := strings.Split(listenQuicAddr, "/")
	for i, part := range parts {
		if part == "udp" && i+1 < len(parts) {
			if p, err := strconv.Atoi(parts[i+1]); err == nil && p > 0 {
				port = p
				break
			}
		}
	}

	// 构建libp2p选项
	opts := []libp2p.Option{
		libp2p.ListenAddrs(quicAddr, kcpAddr),
		libp2p.DefaultTransports,
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
		// 添加KCP传输层
		libp2p.Transport(kcp.NewTransport),
	}

	// 如果启用端口复用，添加相应选项
	if reusePort {
		opts = append(opts,
			libp2p.EnableRelay(),
			libp2p.DisableRelay()) // 禁用中继，我们只想要复用端口功能

		if port > 0 {
			fmt.Printf("QUIC源端口复用已启用，端口: %d\n", port)
		} else {
			fmt.Println("QUIC源端口复用已启用，使用随机端口")
		}
	}

	// 创建P2P节点
	h, err := libp2p.New(opts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("创建P2P节点失败: %w", err)
	}

	// 创建并返回Client实例
	client := &Client{
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
		sourcePort:      port,
		apiProtocol:     "quic-v1", // 默认API协议
		tunnelProtocol:  "kcp",     // 默认隧道协议
		proxyPort:       port,
		proxyTargetHost: "",
		proxyTargetPort: "",
		masterID:        peer.ID(""),
		agentInfo:       make(map[peer.ID][]multiaddr.Multiaddr),
	}

	// 设置为Client节点类型
	client.heartbeat.SetNodeType(common.TypeClient)

	// 注册连接通知处理器
	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(n network.Network, c network.Conn) {
			remotePeerID := c.RemotePeer()
			// 只处理新连接
			client.mu.Lock()
			_, exists := client.connectedPeers[remotePeerID]
			if !exists {
				client.connectedPeers[remotePeerID] = time.Now()
				fmt.Printf("新的对等连接已建立：%s\n", remotePeerID.String())

				// 如果是Agent连接，记录客户端使用的本地地址（源地址）
				if client.connectedAgents[remotePeerID] != (time.Time{}) {
					localAddr := c.LocalMultiaddr()
					addrStr := localAddr.String()
					fmt.Printf("与Agent连接的本地地址: %s\n", addrStr)

					// 提取源端口
					parts := strings.Split(addrStr, "/")
					for i, part := range parts {
						if part == "udp" && i+1 < len(parts) {
							if p, err := strconv.Atoi(parts[i+1]); err == nil && p > 0 {
								client.sourcePort = p
								fmt.Printf("与Agent通信的源端口: %d\n", p)
								break
							}
						}
					}
				}
			}
			client.mu.Unlock()
		},
		DisconnectedF: func(n network.Network, c network.Conn) {
			remotePeerID := c.RemotePeer()
			// 如果是Agent断开，则尝试重连
			client.mu.Lock()
			if client.connectedAgents[remotePeerID] != (time.Time{}) {
				fmt.Printf("与Agent的连接已断开: %s\n", remotePeerID.String())
				delete(client.connectedAgents, remotePeerID)
			}
			delete(client.connectedPeers, remotePeerID)
			client.mu.Unlock()
		},
	})

	// 打印节点信息
	fmt.Printf("Client 节点已创建：\n")
	fmt.Printf("  PeerID: %s\n", h.ID().String())
	fmt.Printf("  监听地址:\n")
	for _, addr := range h.Addrs() {
		fmt.Printf("    - %s/p2p/%s\n", addr.String(), h.ID().String())
	}

	return client, nil
}

// Start 启动客户端节点
func (c *Client) Start(apiPort, tunnelPort int) error {
	// 启动发现服务
	c.discovery.Start()

	// 启动打洞协调器
	c.holePunch.Start()

	// 启动心跳服务
	c.heartbeat.Start()

	// 启动测速服务
	c.speedTest.Start()

	// 连接到Master并获取Agent
	go c.bootstrapFromMaster()

	// 开始监听打洞请求
	go c.holePunch.ListenForHolePunchRequests(c.ctx)

	// 定期打印当前Agent列表状态
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-c.ctx.Done():
				return
			case <-ticker.C:
				c.printConnectedAgents()
			}
		}
	}()

	// 周期性地发现和连接对等节点
	go c.peerDiscoveryLoop()

	// 检查代理服务器是否已启动，确保只启动一次
	c.proxyMu.Lock()
	if !c.proxyStarted {
		// 启动API代理服务器
		fmt.Printf("正在启动API代理服务器，监听地址: 127.0.0.1:%d\n", apiPort)
		err := c.startProxyServer(apiPort, c.apiProtocol)
		if err != nil {
			c.proxyMu.Unlock()
			return fmt.Errorf("启动API代理服务器失败: %w", err)
		}

		// 启动隧道代理服务器
		fmt.Printf("正在启动隧道代理服务器，监听地址: 127.0.0.1:%d\n", tunnelPort)
		err = c.startProxyServer(tunnelPort, c.tunnelProtocol)
		if err != nil {
			c.proxyMu.Unlock()
			return fmt.Errorf("启动隧道代理服务器失败: %w", err)
		}

		c.proxyStarted = true
		fmt.Println("代理服务器启动成功")
	} else {
		fmt.Println("代理服务器已经在运行中，跳过启动")
	}
	c.proxyMu.Unlock()

	fmt.Println("Client 节点已启动")
	return nil
}

// Stop 停止客户端节点
func (c *Client) Stop() error {
	// 停止发现服务
	c.discovery.Stop()

	// 停止打洞协调器
	c.holePunch.Stop()

	// 停止心跳服务
	c.heartbeat.Stop()

	// 停止测速服务
	c.speedTest.Stop()

	// 关闭P2P节点
	if err := c.host.Close(); err != nil {
		return fmt.Errorf("关闭P2P节点失败: %w", err)
	}

	// 取消上下文
	c.cancel()

	fmt.Println("Client 节点已停止")
	return nil
}

// bootstrapFromMaster 从Master节点引导
func (c *Client) bootstrapFromMaster() {
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
				time.Sleep(30 * time.Second)
				continue
			}

			// 检查失败次数，如果连续失败多次，增加等待时间
			if consecutiveFailures >= maxRetries {
				waitTime := retryBackoff * time.Duration(consecutiveFailures/maxRetries+1)
				if waitTime > maxBackoff {
					waitTime = maxBackoff
				}
				fmt.Printf("已连续失败 %d 次，等待 %v 后重试\n", consecutiveFailures, waitTime)
				time.Sleep(waitTime)
			}

			// 1. 查询Master的PeerID
			masterPeerID, err := discovery.GetMasterPeerID(c.ctx)
			if err != nil {
				fmt.Printf("获取Master节点PeerID失败: %v，将在%s后重试\n", err, retryBackoff)
				consecutiveFailures++
				time.Sleep(retryBackoff)
				continue
			}

			// 2. 获取Master的Multiaddr
			masterAddr, err := discovery.GetMasterMultiaddr(masterPeerID)
			if err != nil {
				fmt.Printf("构建Master节点地址失败: %v，将在%s后重试\n", err, retryBackoff)
				consecutiveFailures++
				time.Sleep(retryBackoff)
				continue
			}

			// 3. 连接到Master
			fmt.Printf("尝试连接Master节点地址: %s\n", masterAddr.String())
			fmt.Printf("Master节点ID: %s\n", masterPeerID.String())
			err = common.ConnectToPeer(c.ctx, c.host, masterPeerID, masterAddr)
			if err != nil {
				fmt.Printf("连接到Master节点失败: %v，将在%s后重试\n", err, retryBackoff)
				consecutiveFailures++
				time.Sleep(retryBackoff)
				continue
			}

			fmt.Println("成功连接到Master节点")

			// 打印连接信息
			conns := c.host.Network().ConnsToPeer(masterPeerID)
			if len(conns) > 0 {
				fmt.Printf("与Master节点建立了 %d 个连接\n", len(conns))
				for i, conn := range conns {
					fmt.Printf("连接 #%d: %s -> %s\n", i+1,
						conn.LocalMultiaddr().String(),
						conn.RemoteMultiaddr().String())
					fmt.Printf("  连接状态: %v\n", conn.Stat())
				}
			}

			// 4. 从Master获取Agent信息
			fmt.Println("已连接到Master节点，正在获取Agent信息...")

			// 创建一个变量来表示是否需要重试
			var needRetry bool
			var agentInfo *common.BootstrapResponse

			// 尝试获取Agent信息，最多重试3次
			for attempts := 0; attempts < 3; attempts++ {
				if attempts > 0 {
					fmt.Printf("第 %d 次重试获取Agent信息...\n", attempts)
					// 检查与Master的连接是否仍然存在
					if c.host.Network().Connectedness(masterPeerID) != network.Connected {
						fmt.Println("检测到与Master的连接已断开，将尝试重新连接")
						break
					}
					time.Sleep(2 * time.Second)
				}

				// 获取Agent信息
				agentInfo, err = c.getAgentFromMaster(masterPeerID)
				if err == nil {
					// 成功获取Agent信息
					needRetry = false
					fmt.Println("✓ 成功获取Agent信息!")
					if agentInfo.Count > 0 {
						fmt.Printf("  Agent数量: %d\n", agentInfo.Count)
					} else {
						fmt.Printf("  Agent数量: %d\n", len(agentInfo.Agents))
					}
					fmt.Println("===== Master返回的Agent列表 =====")
					for i, agent := range agentInfo.Agents {
						fmt.Printf("  Agent #%d: %s\n", i+1, agent.PeerID)
						fmt.Printf("    地址数量: %d\n", len(agent.Multiaddrs))
						for j, addr := range agent.Multiaddrs {
							fmt.Printf("      地址 #%d: %s\n", j+1, addr)
						}
					}
					fmt.Println("===============================")
					break
				}

				fmt.Printf("尝试 %d/3 获取Agent信息失败: %v\n", attempts+1, err)

				// 如果错误是EOF，并且连接仍存在，可能是协议问题，直接断开重连
				if err.Error() == "解析Master响应失败: EOF" ||
					strings.Contains(err.Error(), "deadline exceeded") {
					fmt.Println("检测到流通信问题，将断开连接并重新尝试")
					break
				}

				needRetry = true
			}

			// 无论成功失败，都确保断开与Master的连接
			if c.host.Network().Connectedness(masterPeerID) == network.Connected {
				fmt.Println("处理完成，断开与Master的连接")
				c.host.Network().ClosePeer(masterPeerID)
			}

			// 如果需要重试整个过程，等待后继续
			if needRetry || agentInfo == nil {
				fmt.Printf("从Master获取Agent信息失败，将在%s后重试完整流程\n", retryBackoff)
				consecutiveFailures++
				time.Sleep(retryBackoff)
				continue
			}

			// 获取成功，重置失败计数
			consecutiveFailures = 0

			// 处理响应中的Agent列表
			agentList := make([]common.AgentInfo, len(agentInfo.Agents))
			copy(agentList, agentInfo.Agents)

			// 清空当前Agent列表 - 缩短锁持有时间
			c.mu.Lock()
			c.agentList = make([]peer.ID, 0, len(agentList))
			c.mu.Unlock()

			// 连接到所有Agent
			connectedAnyAgent := false
			for _, agent := range agentList {
				agentPeerID, err := peer.Decode(agent.PeerID)
				if err != nil {
					fmt.Printf("解析Agent PeerID失败: %v，跳过该Agent\n", err)
					continue
				}

				// 解析地址
				agentAddrs := make([]multiaddr.Multiaddr, 0, len(agent.Multiaddrs))
				for _, addrStr := range agent.Multiaddrs {
					addr, err := multiaddr.NewMultiaddr(addrStr)
					if err != nil {
						fmt.Printf("解析Agent地址失败: %s - %v\n", addrStr, err)
						continue
					}
					agentAddrs = append(agentAddrs, addr)
				}

				if len(agentAddrs) == 0 {
					fmt.Printf("Agent %s 没有可用的地址，跳过\n", agentPeerID.String())
					continue
				}

				// 尝试连接到Agent
				fmt.Printf("尝试连接到Agent节点: %s\n", agentPeerID.String())
				var agentConnected bool = false
				for i, addr := range agentAddrs {
					fmt.Printf("尝试Agent地址 #%d: %s\n", i+1, addr.String())

					// 添加地址到peerstore
					c.host.Peerstore().AddAddr(agentPeerID, addr, 24*time.Hour)

					// 尝试连接
					ctx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
					err = c.host.Connect(ctx, peer.AddrInfo{
						ID:    agentPeerID,
						Addrs: []multiaddr.Multiaddr{addr},
					})
					cancel()

					if err != nil {
						fmt.Printf("连接到Agent地址 %s 失败: %v\n", addr.String(), err)
					} else {
						fmt.Printf("成功连接到Agent地址: %s\n", addr.String())
						fmt.Println("===== 成功连接到Agent =====")
						fmt.Printf("Agent ID: %s\n", agentPeerID.String())
						fmt.Printf("连接的地址: %s\n", addr.String())
						fmt.Println("==========================")

						// 连接成功，保存Agent ID
						c.mu.Lock()
						c.connectedAgents[agentPeerID] = time.Now()
						c.agentList = append(c.agentList, agentPeerID)
						c.mu.Unlock()

						agentConnected = true
						connectedAnyAgent = true

						// 打印连接详情
						conns := c.host.Network().ConnsToPeer(agentPeerID)
						if len(conns) > 0 {
							fmt.Printf("与Agent建立了 %d 个连接\n", len(conns))
							for i, conn := range conns {
								localAddr := conn.LocalMultiaddr()
								remoteAddr := conn.RemoteMultiaddr()
								connStat := conn.Stat()

								fmt.Printf("连接 #%d:\n", i+1)
								fmt.Printf("  本地地址: %s\n", localAddr.String())
								fmt.Printf("  远程地址: %s\n", remoteAddr.String())
								fmt.Printf("  连接方向: %s\n", connStat.Direction)
								fmt.Printf("  连接建立时间: %s\n", connStat.Opened.Format("2006-01-02 15:04:05"))
							}
						}

						// 注册心跳监控
						c.heartbeat.RegisterPeer(agentPeerID, func() {
							fmt.Printf("Agent %s 心跳超时，将尝试重新连接\n", agentPeerID.String())
							c.mu.Lock()
							delete(c.connectedAgents, agentPeerID)
							// 从Agent列表中移除
							for i, id := range c.agentList {
								if id == agentPeerID {
									c.agentList = append(c.agentList[:i], c.agentList[i+1:]...)
									break
								}
							}
							c.mu.Unlock()
						})

						// 连接成功就跳过其他地址
						break
					}
				}

				if !agentConnected {
					fmt.Printf("未能连接到Agent %s 的任何地址\n", agentPeerID.String())
				}
			}

			// 判断是否成功连接到了任何Agent
			if !connectedAnyAgent {
				fmt.Printf("未能连接到任何Agent，将在%s后重试\n", retryBackoff)
				consecutiveFailures++
				time.Sleep(retryBackoff)
				continue
			}

			// 打印已连接的Agent列表
			c.printConnectedAgents()

			// 启动心跳检查
			fmt.Println("开始监控Agent连接...")
			go c.monitorAgents()

			// 连接成功，等待一段时间后继续检查
			time.Sleep(30 * time.Second)
		}
	}
}

// monitorAgents 监控所有Agent连接
func (c *Client) monitorAgents() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			// 获取需要监控的Agent快照，减少锁的持有时间
			var agents []peer.ID
			c.mu.RLock()
			agentCount := len(c.connectedAgents)
			if agentCount > 0 {
				agents = make([]peer.ID, 0, agentCount)
				for agentID := range c.connectedAgents {
					agents = append(agents, agentID)
				}
			}
			c.mu.RUnlock()

			if agentCount == 0 {
				fmt.Println("没有连接的Agent，停止监控")
				return
			}

			// 在锁外检查每个Agent连接
			for _, agentID := range agents {
				func(agentID peer.ID) {
					// 每个Agent处理添加异常恢复
					defer func() {
						if r := recover(); r != nil {
							fmt.Printf("监控Agent %s 时发生panic: %v\n", agentID.String(), r)
						}
					}()

					// 检查连接是否仍然存在
					if c.host.Network().Connectedness(agentID) != network.Connected {
						fmt.Printf("与Agent %s 的连接已断开，移除\n", agentID.String())
						// 使用单独的锁来移除Agent，避免长时间持有锁
						c.mu.Lock()
						delete(c.connectedAgents, agentID)
						// 从Agent列表中移除
						for i, id := range c.agentList {
							if id == agentID {
								c.agentList = append(c.agentList[:i], c.agentList[i+1:]...)
								break
							}
						}
						c.mu.Unlock()
						c.heartbeat.UnregisterPeer(agentID)
						return
					}

					// 发送心跳确认连接健康，使用较短超时
					ctx, cancel := context.WithTimeout(c.ctx, 3*time.Second)
					defer cancel()

					// 使用Go routine加超时保护心跳发送
					doneCh := make(chan struct{})
					errCh := make(chan error, 1)

					go func() {
						err := c.heartbeat.SendHeartbeat(ctx, agentID)
						if err != nil {
							errCh <- err
							return
						}
						close(doneCh)
					}()

					// 等待心跳结果或超时
					select {
					case <-doneCh:
						fmt.Printf("Agent %s 连接健康\n", agentID.String())
					case err := <-errCh:
						fmt.Printf("向Agent %s 发送心跳失败: %v，将检查连接\n", agentID.String(), err)
					case <-ctx.Done():
						fmt.Printf("向Agent %s 发送心跳超时\n", agentID.String())
					}
				}(agentID)
			}
		}
	}
}

// getNextAgent 获取下一个要使用的Agent（简单轮询算法）
// 不再内部使用锁，避免死锁
func (c *Client) getNextAgent() (peer.ID, bool) {
	// 读取必要的数据 - 不在函数内部使用锁
	c.mu.RLock()
	if len(c.agentList) == 0 {
		c.mu.RUnlock()
		return "", false
	}

	// 使用本地变量，避免在锁内执行复杂逻辑
	agentList := make([]peer.ID, len(c.agentList))
	copy(agentList, c.agentList)
	currentIndex := c.currentAgentIndex
	c.mu.RUnlock()

	// 在锁外执行逻辑操作
	if currentIndex >= len(agentList) {
		currentIndex = 0
	}

	// 获取当前Agent
	agent := agentList[currentIndex]

	// 更新索引
	nextIndex := (currentIndex + 1) % len(agentList)

	// 只在需要更新状态时获取锁
	c.mu.Lock()
	c.currentAgentIndex = nextIndex
	c.mu.Unlock()

	return agent, true
}

// getAgentFromMaster 从Master获取Agent信息
func (c *Client) getAgentFromMaster(masterPeerID peer.ID) (*common.BootstrapResponse, error) {
	// 使用标准的引导协议
	protocol := protoco.ID(common.BootstrapProtocolID)

	// 创建与Master的流连接
	fmt.Printf("尝试使用引导协议: %s 来连接Master...\n", protocol)
	ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
	defer cancel()

	s, err := c.host.NewStream(ctx, masterPeerID, protocol)
	if err != nil {
		fmt.Printf("创建流失败: %v\n", err)
		return nil, fmt.Errorf("无法创建到Master的流: %w", err)
	}

	// 确保在函数结束时关闭流
	defer s.Close()

	// 打印连接状态
	fmt.Printf("已建立到Master的流连接，远程地址: %s\n", s.Conn().RemoteMultiaddr())

	// 发送引导请求
	fmt.Printf("正在向Master发送引导请求...\n")
	// 创建请求对象 - 使用统一的请求构造函数
	bootstrapRequest := common.NewStandardRequest("bootstrap", common.TypeClient, c.host.ID().String())

	// 序列化并发送请求
	reqBytes, err := common.JSONMarshalWithNewline(bootstrapRequest)
	if err != nil {
		return nil, fmt.Errorf("序列化引导请求失败: %w", err)
	}

	fmt.Printf("请求JSON: %s\n", string(reqBytes))

	_, err = s.Write(reqBytes)
	if err != nil {
		return nil, fmt.Errorf("发送引导请求失败: %w", err)
	}

	fmt.Println("已发送引导请求，等待Master响应...")

	// 设置读取超时前先刷新流
	if f, ok := s.(interface{ Flush() error }); ok {
		if err := f.Flush(); err != nil {
			fmt.Printf("刷新流失败: %v，但将继续尝试读取响应\n", err)
		} else {
			fmt.Println("成功刷新流")
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
			fmt.Printf("虽然收到EOF，但读取了%d字节数据，将尝试处理\n", n)
		} else {
			return nil, fmt.Errorf("读取Master响应失败: %w", err)
		}
	}

	if n == 0 {
		return nil, fmt.Errorf("收到空响应")
	}

	fmt.Printf("收到响应: %d 字节\n", n)

	// 直接解析JSON响应，不需要预处理
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
				fmt.Printf("JSON解析错误: %v\n", err)
				return nil, fmt.Errorf("解析Master响应失败: %w", err)
			}
		} else {
			fmt.Printf("JSON解析错误: %v\n", err)
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
	fmt.Printf("成功获取Master响应: 共%d个Agent\n", len(response.Agents))
	for i, agent := range response.Agents {
		fmt.Printf("  Agent #%d: %s, 地址数=%d\n", i+1, agent.PeerID, len(agent.Multiaddrs))
	}

	return &response, nil
}

// peerDiscoveryLoop 周期性发现和连接对等节点
func (c *Client) peerDiscoveryLoop() {
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
func (c *Client) discoverAndConnectPeers() {
	// 检查是否已经连接到Agent，并获取一个Agent
	var agentID peer.ID
	var exists bool

	// 直接调用getNextAgent，它内部会处理锁
	agentID, exists = c.getNextAgent()

	if !exists {
		return // 未连接到Agent，无法发现对等节点
	}

	// 从Agent获取对等节点信息
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()

	peers, err := c.discovery.QueryPeers(ctx, agentID)
	if err != nil {
		fmt.Printf("查询对等节点失败: %v\n", err)
		return
	}

	fmt.Printf("发现了 %d 个对等节点\n", len(peers))
	for i, p := range peers {
		fmt.Printf("发现的节点 #%d: %s\n", i+1, p.PeerID.String())
		fmt.Printf("  节点类型: %v\n", p.NodeType)
		fmt.Printf("  可用地址: %v\n", p.Multiaddrs)
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
			fmt.Printf("节点 %s 已经连接，跳过\n", p.PeerID.String())
			continue
		}

		// 尝试使用打洞建立直接连接
		go func(peerInfo *common.PeerInfo) {
			ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
			defer cancel()

			fmt.Printf("尝试连接到对等节点: %s\n", peerInfo.PeerID.String())

			// 解析并添加对方地址到peerstore
			targetPeerID, err := peer.Decode(peerInfo.PeerID.String())
			if err != nil {
				fmt.Printf("解析对等节点ID失败: %v\n", err)
				return
			}

			// 添加对方的地址到peerstore
			if len(peerInfo.Multiaddrs) > 0 {
				fmt.Printf("将节点 %s 的 %d 个地址添加到peerstore\n",
					peerInfo.PeerID.String(), len(peerInfo.Multiaddrs))

				addedCount := 0
				publicAddrCount := 0

				for _, addrStr := range peerInfo.Multiaddrs {
					addr, err := multiaddr.NewMultiaddr(addrStr)
					if err != nil {
						fmt.Printf("解析地址失败: %s - %v\n", addrStr, err)
						continue
					}

					// 提取IP地址
					var ip string
					ma := addr
					if val, err := ma.ValueForProtocol(multiaddr.P_IP4); err == nil {
						ip = val
					} else if val, err := ma.ValueForProtocol(multiaddr.P_IP6); err == nil {
						ip = val
					}

					// 检查是否为公网IP
					//if ip != "" && !isPublicIP(ip) {
					//	fmt.Printf("跳过内网地址: %s (IP: %s)\n", addr.String(), ip)
					//	continue
					//}
					// 内网IP暂时不跳过
					if ip == "" {
						continue
					}
					if ip != "" {
						publicAddrCount++
					}

					// 添加地址到peerstore，设置有效期为1小时
					c.host.Peerstore().AddAddr(targetPeerID, addr, time.Hour)
					fmt.Printf("已添加地址: %s\n", addr.String())
					addedCount++
				}

				fmt.Printf("成功添加 %d 个地址（其中公网地址: %d 个）\n", addedCount, publicAddrCount)
			} else {
				fmt.Printf("警告: 节点 %s 没有可用地址\n", peerInfo.PeerID.String())
			}

			err = c.holePunch.RequestHolePunch(ctx, peerInfo.PeerID)
			if err != nil {
				fmt.Printf("连接到对等节点失败: %v\n", err)
			} else {
				fmt.Printf("成功连接到对等节点: %s\n", peerInfo.PeerID.String())

				// 打印连接详情
				conns := c.host.Network().ConnsToPeer(peerInfo.PeerID)
				if len(conns) > 0 {
					fmt.Printf("与节点 %s 建立了 %d 个连接\n", peerInfo.PeerID.String(), len(conns))
					for i, conn := range conns {
						localAddr := conn.LocalMultiaddr()
						remoteAddr := conn.RemoteMultiaddr()
						fmt.Printf("连接 #%d:\n", i+1)
						fmt.Printf("  本地地址: %s\n", localAddr.String())
						fmt.Printf("  远程地址: %s\n", remoteAddr.String())
					}
				}
			}
		}(p)
	}
}

// 修改: 启动代理服务器
func (c *Client) startProxyServer(proxyPort int, protocol string) error {
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

// printConnectedAgents 打印当前已连接的Agent列表
func (c *Client) printConnectedAgents() {
	c.mu.RLock()
	defer c.mu.RUnlock()

	fmt.Println("\n========== 当前已连接的Agent列表 ==========")
	if len(c.agentList) == 0 {
		fmt.Println("没有连接到任何Agent")
	} else {
		for i, agentID := range c.agentList {
			fmt.Printf("Agent #%d: %s\n", i+1, agentID.String())
			conns := c.host.Network().ConnsToPeer(agentID)
			fmt.Printf("  连接数: %d\n", len(conns))
			for j, conn := range conns {
				fmt.Printf("  连接 #%d: %s -> %s\n", j+1,
					conn.LocalMultiaddr().String(),
					conn.RemoteMultiaddr().String())
			}
		}
	}
	fmt.Println("===========================================")
}

// isPublicIP 判断一个IP是否是公网IP
func isPublicIP(ip string) bool {
	// 去除IPv6地址的方括号
	ip = strings.TrimPrefix(ip, "[")
	ip = strings.TrimSuffix(ip, "]")

	// 解析IP地址
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false // 无效IP
	}

	// 检查是否为本地回环地址
	if parsedIP.IsLoopback() {
		return false
	}

	// 检查是否为私有地址
	if parsedIP.IsPrivate() {
		return false
	}

	// 检查是否为链路本地地址
	if parsedIP.IsLinkLocalUnicast() || parsedIP.IsLinkLocalMulticast() {
		return false
	}

	// 额外检查一些特殊网段
	// 检查RFC1918私有地址范围
	if ip4 := parsedIP.To4(); ip4 != nil {
		// 10.0.0.0/8
		if ip4[0] == 10 {
			return false
		}
		// 172.16.0.0/12
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return false
		}
		// 192.168.0.0/16
		if ip4[0] == 192 && ip4[1] == 168 {
			return false
		}
		// 169.254.0.0/16 (链路本地)
		if ip4[0] == 169 && ip4[1] == 254 {
			return false
		}
		// 127.0.0.0/8 (本地回环)
		if ip4[0] == 127 {
			return false
		}
	}

	return true
}

// 添加日志函数，替换fmt.Printf，在输出日志后重新显示命令提示符
func logMessage(format string, args ...interface{}) {
	// 清除当前行
	fmt.Print("\r\033[K")
	// 输出日志信息
	fmt.Printf(format, args...)
	// 重新显示命令提示符
	fmt.Print("\n> ")
}

func main() {
	// 解析命令行参数
	listenQuic := flag.String("quic", "/ip4/0.0.0.0/udp/0/quic-v1", "QUIC监听地址")
	listenKcp := flag.String("kcp", "/ip4/0.0.0.0/udp/0/kcp", "KCP监听地址")
	apiPort := flag.Int("api-port", 58080, "API代理端口")
	tunnelPort := flag.Int("tunnel-port", 58081, "隧道代理端口")
	reusePort := flag.Bool("reuse", true, "启用QUIC端口复用")
	apiProtocol := flag.String("api-protocol", "quic-v1", "API代理协议")
	tunnelProtocol := flag.String("tunnel-protocol", "kcp", "隧道代理协议")
	flag.Parse()

	// 创建客户端
	client, err := NewClient(*listenQuic, *listenKcp, *reusePort)
	if err != nil {
		fmt.Printf("创建客户端失败: %v\n", err)
		os.Exit(1)
	}

	// 设置协议类型
	client.apiProtocol = *apiProtocol
	client.tunnelProtocol = *tunnelProtocol

	// 启动客户端
	if err := client.Start(*apiPort, *tunnelPort); err != nil {
		fmt.Printf("启动客户端失败: %v\n", err)
		os.Exit(1)
	}

	// 替换原来的标准输出，重定向日志到文件
	logFile, err := os.OpenFile("client.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err == nil {
		// 创建一个同时写入控制台和文件的writer
		multiWriter := io.MultiWriter(os.Stdout, logFile)
		log.SetOutput(multiWriter)
	}

	// 等待中断信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// 命令行处理循环
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("\n输入 help 查看可用命令")
	fmt.Print("> ")

commandLoop:
	for {
		// 读取命令，支持中断
		cmdChan := make(chan string)
		errChan := make(chan error)

		go func() {
			cmdline, err := reader.ReadString('\n')
			if err != nil {
				errChan <- err
			} else {
				cmdChan <- cmdline
			}
		}()

		var cmdline string
		select {
		case <-sigCh:
			fmt.Println("\n收到中断信号，正在退出...")
			break commandLoop
		case err := <-errChan:
			fmt.Printf("\n读取命令出错: %v\n", err)
			fmt.Print("> ")
			continue
		case cmdline = <-cmdChan:
			// 成功读取命令
		}

		cmdline = strings.TrimSpace(cmdline)

		if cmdline == "" {
			fmt.Print("> ")
			continue
		}

		// 解析命令和参数
		parts := strings.Split(cmdline, " ")
		cmd := parts[0]

		switch cmd {
		case "exit", "quit":
			fmt.Println("正在退出...")
			break commandLoop

		case "help":
			fmt.Println("可用命令:")
			fmt.Println("  connect <peerID> - 连接到指定的peer")
			fmt.Println("  list - 列出已连接的节点")
			fmt.Println("  agents - 显示当前连接的Agent列表")
			fmt.Println("  bootstrap - 从Master获取Agent列表并连接")
			fmt.Println("  speedtest <peerID> [fileSize MB] [duration seconds] - 与指定节点进行测速")
			fmt.Println("  exit/quit - 退出程序")
			fmt.Print("> ")

		case "connect":
			if len(parts) < 2 {
				fmt.Println("用法: connect <peerID>")
				fmt.Print("> ")
				continue
			}
			peerIDStr := parts[1]
			peerID, err := peer.Decode(peerIDStr)
			if err != nil {
				fmt.Printf("无效的peer ID: %v\n", err)
				fmt.Print("> ")
				continue
			}

			// 启动goroutine连接到peer
			go func() {
				ctx, cancel := context.WithTimeout(client.ctx, 30*time.Second)
				defer cancel()

				fmt.Printf("正在连接到peer: %s\n", peerID.String())
				err := client.host.Connect(ctx, peer.AddrInfo{ID: peerID})
				if err != nil {
					fmt.Printf("连接失败: %v\n", err)
					fmt.Print("> ")
				} else {
					fmt.Printf("成功连接到peer: %s\n", peerID.String())
					fmt.Print("> ")
				}
			}()

		case "list":
			fmt.Println("已连接的节点:")
			for _, p := range client.host.Network().Peers() {
				fmt.Printf("  %s\n", p.String())
				for _, addr := range client.host.Peerstore().Addrs(p) {
					fmt.Printf("    - %s\n", addr.String())
				}
			}
			fmt.Print("> ")

		case "agents":
			client.printConnectedAgents()
			fmt.Print("> ")

		case "bootstrap":
			fmt.Println("正在从Master获取Agent列表...")
			go client.bootstrapFromMaster()
			fmt.Print("> ")

		case "speedtest", "测速":
			// 解析参数
			targetPeerID := ""
			fileSize := int64(0)
			duration := 0

			if len(parts) > 1 {
				targetPeerID = parts[1]
			}

			if len(parts) > 2 {
				size, err := strconv.ParseInt(parts[2], 10, 64)
				if err == nil {
					fileSize = size * 1024 * 1024 // 转换为MB
				}
			}

			if len(parts) > 3 {
				dur, err := strconv.Atoi(parts[3])
				if err == nil {
					duration = dur
				}
			}

			// 如果没有指定对等节点ID，列出当前连接的所有节点
			if targetPeerID == "" {
				fmt.Println("请指定要测速的对等节点ID")
				fmt.Println("已连接的对等节点:")

				client.mu.RLock()
				count := 0
				for peerID := range client.connectedPeers {
					fmt.Printf("%d. %s\n", count+1, peerID.String())
					count++
				}
				client.mu.RUnlock()

				if count == 0 {
					fmt.Println("没有已连接的对等节点")
				}
				fmt.Print("> ")
				continue
			}

			// 解析对等节点ID
			pID, err := peer.Decode(targetPeerID)
			if err != nil {
				fmt.Printf("无效的对等节点ID: %v\n", err)
				fmt.Print("> ")
				continue
			}

			// 检查连接状态
			if client.host.Network().Connectedness(pID) != network.Connected {
				fmt.Printf("未与节点 %s 建立连接，请先打通连接\n", targetPeerID)
				fmt.Print("> ")
				continue
			}

			fmt.Printf("开始与节点 %s 进行测速...\n", targetPeerID)
			fmt.Print("> ")

			// 启动测速
			go func() {
				ctx, cancel := context.WithTimeout(client.ctx, 10*time.Minute)
				defer cancel()

				// 调用测速
				result, err := client.speedTest.RunSpeedTest(ctx, pID, fileSize, 64*1024, duration)
				if err != nil {
					fmt.Printf("\r\033[K测速失败: %v\n> ", err)
					return
				}

				// 打印结果
				fmt.Print("\r\033[K") // 清除当前行
				fmt.Println("\n========== 测速结果 ==========")
				fmt.Printf("测试ID: %s\n", result.TestID)
				fmt.Printf("总传输数据: %.2f MB\n", float64(result.TotalBytes)/1024/1024)
				fmt.Printf("测试时长: %.2f 秒\n", result.Duration)
				fmt.Printf("吞吐量: %.2f MB/s\n", result.Throughput)
				fmt.Printf("网络速率: %.2f Mbps\n", result.MbpsThroughput)
				fmt.Println("============================")
				fmt.Print("> ")
			}()

		default:
			fmt.Printf("未知命令: %s\n", cmd)
			fmt.Println("输入 help 查看可用命令")
			fmt.Print("> ")
		}
	}

	// 停止客户端
	fmt.Println("正在停止客户端...")
	client.Stop()
	fmt.Println("客户端已停止")
}
