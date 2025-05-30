package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"shiledp2p/pkg/common"
	"shiledp2p/pkg/discovery"
	"shiledp2p/pkg/nat"
	"shiledp2p/pkg/relay"
	"shiledp2p/pkg/speedtest"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	kcp "github.com/libp2p/go-libp2p/p2p/transport/kcp"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	"github.com/multiformats/go-multiaddr"
)

// Agent 工作节点
type Agent struct {
	host         host.Host
	discovery    *discovery.PeerDiscovery
	proxyService *relay.ProxyService
	holePunch    *nat.HolePunchCoordinator
	heartbeat    *common.HeartbeatService
	speedTest    *speedtest.SpeedTest

	ctx    context.Context
	cancel context.CancelFunc

	mu               sync.RWMutex
	connectedClients map[peer.ID]time.Time

	// 新增字段
	apiProtocol    string // API协议类型
	tunnelProtocol string // 隧道协议类型
}

// NewAgent 创建新的Agent节点
func NewAgent(listenQuicAddr, listenKcpAddr string, certFile, keyFile string) (*Agent, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// 解析监听地址
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

	// 构建libp2p选项
	opts := []libp2p.Option{
		libp2p.ListenAddrs(quicAddr, kcpAddr),
		libp2p.DefaultTransports,
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
		// 添加KCP传输层
		libp2p.Transport(kcp.NewTransport),
	}

	// 加入安全传输层
	opts = append(opts, libp2p.Security(noise.ID, noise.New))

	// 创建P2P节点
	h, err := libp2p.New(opts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("创建P2P节点失败: %w", err)
	}

	// 创建并返回Agent实例
	agent := &Agent{
		host:             h,
		ctx:              ctx,
		cancel:           cancel,
		connectedClients: make(map[peer.ID]time.Time),
		apiProtocol:      "quic-v1", // 默认API协议
		tunnelProtocol:   "kcp",     // 默认隧道协议
	}

	// 创建节点发现服务
	agent.discovery = discovery.NewPeerDiscovery(h, common.TypeAgent)

	// 创建代理服务
	agent.proxyService = relay.NewProxyService(h)

	// 创建打洞协调器
	agent.holePunch = nat.NewHolePunchCoordinator(h)

	// 创建心跳服务（每10秒检查一次，30秒无心跳则认为离线）
	agent.heartbeat = common.NewHeartbeatService(h, 10*time.Second, 30*time.Second)
	// 设置为Agent节点类型
	agent.heartbeat.SetNodeType(common.TypeAgent)

	// 创建测速服务
	agent.speedTest = speedtest.NewSpeedTest(h)

	// 打印节点信息
	fmt.Printf("Agent 节点已创建：\n")
	fmt.Printf("  PeerID: %s\n", h.ID().String())
	fmt.Printf("  监听地址:\n")
	for _, addr := range h.Addrs() {
		fmt.Printf("    - %s/p2p/%s\n", addr.String(), h.ID().String())
	}

	return agent, nil
}

// Start 启动Agent节点
func (a *Agent) Start() error {
	// 启动发现服务
	a.discovery.Start()

	// 启动代理服务
	a.proxyService.Start()

	// 启动打洞协调器
	a.holePunch.Start()

	// 启动心跳服务
	a.heartbeat.Start()

	// 启动测速服务
	a.speedTest.Start()

	// 添加连接通知处理程序，用于自动注册客户端
	notifyBundle := &network.NotifyBundle{
		ConnectedF: func(net network.Network, conn network.Conn) {
			remotePeer := conn.RemotePeer()
			remoteAddr := conn.RemoteMultiaddr()

			// 检查是否已经是Master节点
			if remotePeer.String() == "" {
				return // 忽略无效PeerID
			}

			// 获取节点类型（如果可能）
			// 这里假设客户端都不是Master，可能需要进一步改进以确定节点类型
			isMaster := false

			// 尝试获取Master PeerID
			masterPeerID, err := discovery.GetMasterPeerID(a.ctx)
			if err == nil && masterPeerID == remotePeer {
				isMaster = true
				fmt.Printf("[连接通知] 检测到Master节点连接: %s\n", remotePeer.String())
			}

			if !isMaster {
				fmt.Printf("[连接通知] 检测到新的客户端连接: %s (%s)\n", remotePeer.String(), remoteAddr.String())

				// 使用goroutine，避免阻塞连接处理流程
				go func() {
					// 等待一小段时间，让其他协议握手完成
					time.Sleep(500 * time.Millisecond)

					// 检查连接是否仍然存在
					if a.host.Network().Connectedness(remotePeer) == network.Connected {
						// 将节点注册为客户端
						fmt.Printf("[连接通知] 注册新客户端: %s\n", remotePeer.String())
						a.RegisterClient(remotePeer)
					}
				}()
			}
		},
		DisconnectedF: func(net network.Network, conn network.Conn) {
			remotePeer := conn.RemotePeer()

			// 检查是否是客户端
			a.mu.RLock()
			_, isClient := a.connectedClients[remotePeer]
			a.mu.RUnlock()

			if isClient {
				fmt.Printf("[连接通知] 客户端断开连接: %s\n", remotePeer.String())

				// 如果没有其他连接，注销客户端
				if len(a.host.Network().ConnsToPeer(remotePeer)) == 0 {
					a.UnregisterClient(remotePeer)
				}
			}
		},
	}
	a.host.Network().Notify(notifyBundle)

	// 连接到Master节点
	go a.connectToMaster()

	// 开始监听打洞请求
	go a.holePunch.ListenForHolePunchRequests(a.ctx)

	fmt.Println("Agent 节点已启动")
	return nil
}

// Stop 停止Agent节点
func (a *Agent) Stop() error {
	// 停止发现服务
	a.discovery.Stop()

	// 停止打洞协调器
	a.holePunch.Stop()

	// 停止心跳服务
	a.heartbeat.Stop()

	// 停止代理服务
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.proxyService.Stop(stopCtx)

	// 停止测速服务
	a.speedTest.Stop()

	// 关闭P2P节点
	if err := a.host.Close(); err != nil {
		return fmt.Errorf("关闭P2P节点失败: %w", err)
	}

	// 取消上下文
	a.cancel()

	fmt.Println("Agent 节点已停止")
	return nil
}

// connectToMaster 连接到Master节点
func (a *Agent) connectToMaster() {
	var masterPeerID peer.ID
	var masterAddr multiaddr.Multiaddr
	var consecutiveFailures int = 0
	var lastDNSQueryTime time.Time
	var dnsQueryInterval = 5 * time.Minute // 设置合理的DNS查询间隔

	retryBackoff := time.Second * 5 // 初始重试间隔
	maxBackoff := time.Minute * 2   // 最大重试间隔

	for {
		select {
		case <-a.ctx.Done():
			return
		default:
			// 检查是否已有连接到Master
			if a.host.Network().Connectedness(masterPeerID) == network.Connected {
				// 已连接，检查连接健康状态
				healthCtx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
				err := a.heartbeat.SendHeartbeat(healthCtx, masterPeerID)
				cancel()

				if err == nil {
					// 心跳正常，重置失败计数
					consecutiveFailures = 0
					// 每2秒检查一次连接状态
					time.Sleep(2 * time.Second)
					continue
				} else {
					// 心跳失败，增加失败计数
					consecutiveFailures++
					fmt.Printf("与Master节点的心跳失败 (%d/3): %v\n", consecutiveFailures, err)

					// 如果连续失败3次，认为连接已断开
					if consecutiveFailures >= 3 {
						fmt.Println("与Master节点的连接健康检查失败，重新建立连接")
						// 关闭现有连接
						a.host.Network().ClosePeer(masterPeerID)
						time.Sleep(1 * time.Second)
					} else {
						// 少于3次失败，等待并重试
						time.Sleep(2 * time.Second)
						continue
					}
				}
			}

			// 计算本次重试的等待时间
			currentBackoff := retryBackoff
			if consecutiveFailures > 0 {
				// 指数退避，但限制最大值
				currentBackoff = retryBackoff * time.Duration(1<<uint(consecutiveFailures-1))
				if currentBackoff > maxBackoff {
					currentBackoff = maxBackoff
				}
			}

			// 查询Master的PeerID（如果需要）
			if masterPeerID == "" || time.Since(lastDNSQueryTime) > dnsQueryInterval {
				fmt.Println("====================DNS解析开始====================")
				var err error
				masterPeerID, err = discovery.GetMasterPeerID(a.ctx)
				if err != nil {
					fmt.Printf("获取Master节点PeerID失败: %v，将在%v后重试\n", err, currentBackoff)
					consecutiveFailures++
					time.Sleep(currentBackoff)
					continue
				}

				// 记录本次DNS查询时间
				lastDNSQueryTime = time.Now()
				fmt.Println("DNS解析成功，将缓存结果", dnsQueryInterval)
				fmt.Println("====================DNS解析结束====================")
			} else {
				fmt.Printf("使用缓存的Master PeerID: %s (距上次查询: %v)\n",
					masterPeerID.String(), time.Since(lastDNSQueryTime))
			}

			// 获取Master的Multiaddr（如果未获取或需要刷新）
			if masterAddr == nil || time.Since(lastDNSQueryTime) <= time.Second {
				var err error
				masterAddr, err = discovery.GetMasterMultiaddr(masterPeerID)
				if err != nil {
					fmt.Printf("构建Master节点地址失败: %v，将在%v后重试\n", err, currentBackoff)
					consecutiveFailures++
					time.Sleep(currentBackoff)
					continue
				}
			}

			// 连接到Master
			fmt.Printf("\n[连接Master] 尝试连接到Master节点: %s\n", masterAddr.String())
			err := common.ConnectToPeer(a.ctx, a.host, masterPeerID, masterAddr)
			if err != nil {
				fmt.Printf("[连接Master] 连接到Master节点失败: %v，将在%v后重试\n", err, currentBackoff)
				consecutiveFailures++
				time.Sleep(currentBackoff)
				continue
			}

			fmt.Println("[连接Master] 成功连接到Master节点")

			// 打印连接信息
			conns := a.host.Network().ConnsToPeer(masterPeerID)
			if len(conns) > 0 {
				for i, conn := range conns {
					localAddr := conn.LocalMultiaddr()
					remoteAddr := conn.RemoteMultiaddr()
					fmt.Printf("[连接Master] 连接 #%d:\n", i+1)
					fmt.Printf("  本地地址: %s\n", localAddr.String())
					fmt.Printf("  远程地址: %s\n", remoteAddr.String())
					fmt.Printf("  连接状态: %+v\n", conn.Stat())
				}
			}

			// 注册Master到心跳服务，当连接断开时会收到通知
			a.heartbeat.RegisterPeer(masterPeerID, func() {
				fmt.Println("[连接Master] 通过心跳检测到Master节点连接断开")
				// 不进行任何处理，让主循环负责重新连接
			})

			// 向Master发送简单注册请求
			fmt.Println("[注册Master] 发送简单注册请求...")
			regCtx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
			err = a.discovery.SendPeerInfo(regCtx, masterPeerID)
			cancel()

			if err != nil {
				fmt.Printf("[注册Master] 注册失败: %v，将重试\n", err)
				// 连接可能仍然有效，继续使用
			} else {
				fmt.Println("[注册Master] 注册请求已发送")
				// 开始定期健康检查
				a.startPeriodicHealthCheck(masterPeerID)
			}

			// 周期性检查连接状态
			connectionOK := true
			for connectionOK {
				select {
				case <-a.ctx.Done():
					return
				case <-time.After(2 * time.Second): // 每2秒检查一次
					if a.host.Network().Connectedness(masterPeerID) != network.Connected {
						fmt.Println("[连接Master] 检测到与Master的连接断开，将尝试重新连接...")
						connectionOK = false
						break
					}
				}
			}

			// 连接断开后，等待一段时间再尝试重连
			fmt.Println("[连接Master] 将在2秒后尝试重新连接到Master...")
			time.Sleep(2 * time.Second)
		}
	}
}

// 新增方法: 开始定期健康检查
func (a *Agent) startPeriodicHealthCheck(masterPeerID peer.ID) {
	// 启动单独的goroutine来定期发送心跳
	go func() {
		// 将心跳检查间隔从2秒增加到10秒
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		failedHeartbeats := 0
		maxFailedHeartbeats := 3
		// 添加指数退避策略
		backoffTime := 500 * time.Millisecond

		for {
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
				// 检查连接是否存在
				if a.host.Network().Connectedness(masterPeerID) != network.Connected {
					fmt.Println("[健康检查] 检测到与Master连接断开")
					return // 结束goroutine，让主连接循环处理重连
				}

				// 发送心跳
				healthCtx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
				err := a.heartbeat.SendHeartbeat(healthCtx, masterPeerID)
				cancel()

				if err != nil {
					failedHeartbeats++
					fmt.Printf("[健康检查] 心跳失败 (%d/%d): %v\n",
						failedHeartbeats, maxFailedHeartbeats, err)

					if failedHeartbeats >= maxFailedHeartbeats {
						fmt.Printf("[健康检查] 连续%d次心跳失败，关闭连接\n", maxFailedHeartbeats)
						a.host.Network().ClosePeer(masterPeerID)
						return // 结束goroutine
					}

					// 使用指数退避策略增加等待时间
					wait := backoffTime * time.Duration(1<<uint(failedHeartbeats-1))
					if wait > 30*time.Second {
						wait = 30 * time.Second // 最大30秒
					}
					fmt.Printf("[健康检查] 将在%v后重试心跳\n", wait)
					time.Sleep(wait)
				} else {
					// 重置失败计数和退避时间
					if failedHeartbeats > 0 {
						fmt.Printf("[健康检查] 心跳恢复正常，重置失败计数\n")
						failedHeartbeats = 0
						backoffTime = 500 * time.Millisecond
					}
				}
			}
		}
	}()
}

// RegisterClient 注册客户端节点
func (a *Agent) RegisterClient(clientID peer.ID) {
	a.mu.Lock()

	// 检查是否已经注册过该客户端
	if _, exists := a.connectedClients[clientID]; exists {
		fmt.Printf("[客户端注册] 客户端 %s 已经注册，跳过\n", clientID.String())
		a.mu.Unlock()
		return
	}

	// 记录注册时间
	a.connectedClients[clientID] = time.Now()
	a.mu.Unlock() // 提前解锁，避免长时间持有锁

	fmt.Printf("\n====== 开始客户端注册流程 ======\n")
	fmt.Printf("客户端ID: %s\n", clientID.String())

	// 获取当前peerstore中的客户端地址（这些可能是内网地址）
	existingAddrs := a.host.Peerstore().Addrs(clientID)
	fmt.Printf("原始地址数量: %d\n", len(existingAddrs))
	for i, addr := range existingAddrs {
		fmt.Printf("原始地址 #%d: %s\n", i+1, addr.String())
	}

	// 获取连接信息，提取公网地址
	conns := a.host.Network().ConnsToPeer(clientID)
	fmt.Printf("与客户端建立了 %d 个连接\n", len(conns))

	// 用于存储提取的公网地址和规范化的地址
	var publicAddrs []multiaddr.Multiaddr
	var normalizedAddrs []multiaddr.Multiaddr

	// 首先从现有连接中提取远程地址（客户端的公网地址）
	for i, conn := range conns {
		localAddr := conn.LocalMultiaddr()
		remoteAddr := conn.RemoteMultiaddr()
		fmt.Printf("客户端连接 #%d:\n", i+1)
		fmt.Printf("  本地地址: %s\n", localAddr.String())
		fmt.Printf("  远程地址: %s\n", remoteAddr.String())
		fmt.Printf("  连接方向: %s\n", conn.Stat().Direction)
		fmt.Printf("  连接建立时间: %s\n", conn.Stat().Opened.Format("2006-01-02 15:04:05"))

		// 判断remoteAddr是否为公网地址
		if isPublicIP(remoteAddr) {
			fmt.Printf("  检测到公网地址: %s\n", remoteAddr.String())
			publicAddrs = append(publicAddrs, remoteAddr)

			// 提取IP和端口
			ip, port, err := extractPublicAddrInfo(remoteAddr)
			if err == nil && port > 0 {
				// 创建规范化的地址
				normalizedAddr, err := createTransportAgnosticAddr(ip, port)
				if err == nil {
					fmt.Printf("  创建规范化地址: %s\n", normalizedAddr.String())
					normalizedAddrs = append(normalizedAddrs, normalizedAddr)
				} else {
					fmt.Printf("  创建规范化地址失败: %v\n", err)
				}
			} else {
				fmt.Printf("  提取地址信息失败: %v\n", err)
			}
		}
	}

	// 清除所有现有地址
	a.host.Peerstore().ClearAddrs(clientID)

	// 添加地址到peerstore
	addressesAdded := false

	// 1. 首先尝试添加规范化的公网地址
	if len(normalizedAddrs) > 0 {
		fmt.Printf("为客户端添加 %d 个规范化公网地址到peerstore\n", len(normalizedAddrs))
		for _, addr := range normalizedAddrs {
			a.host.Peerstore().AddAddr(clientID, addr, 24*time.Hour)
			fmt.Printf("添加规范化公网地址: %s\n", addr.String())
		}
		addressesAdded = true
	}

	// 2. 如果没有规范化地址，添加原始公网地址
	if !addressesAdded && len(publicAddrs) > 0 {
		fmt.Printf("为客户端添加 %d 个原始公网地址到peerstore\n", len(publicAddrs))
		for _, addr := range publicAddrs {
			a.host.Peerstore().AddAddr(clientID, addr, 24*time.Hour)
			fmt.Printf("添加原始公网地址: %s\n", addr.String())
		}
		addressesAdded = true
	}

	// 3. 如果没有公网地址，保留原始地址
	if !addressesAdded {
		fmt.Printf("警告: 未检测到公网地址，使用原始地址\n")
		for _, addr := range existingAddrs {
			a.host.Peerstore().AddAddr(clientID, addr, 24*time.Hour)
			fmt.Printf("保留原始地址: %s\n", addr.String())
		}
	}

	// 再次获取peerstore中的地址，验证添加结果
	updatedAddrs := a.host.Peerstore().Addrs(clientID)
	fmt.Printf("更新后的地址数量: %d\n", len(updatedAddrs))
	for i, addr := range updatedAddrs {
		fmt.Printf("更新后地址 #%d: %s\n", i+1, addr.String())
	}

	// 注册心跳回调
	a.heartbeat.RegisterPeer(clientID, func() {
		fmt.Printf("[心跳回调] 客户端 %s 心跳超时，注销客户端\n", clientID.String())
		a.UnregisterClient(clientID)
	})

	// 添加到发现服务 - 使用更新后的地址
	addrStrings := common.GetMultiaddrsString(a.host.Peerstore().Addrs(clientID))
	a.discovery.AddPeer(clientID, addrStrings, common.TypeClient)
	fmt.Printf("已将客户端添加到节点发现服务，地址: %v\n", addrStrings)

	fmt.Printf("客户端注册完成: %s\n", clientID.String())
	fmt.Printf("============================\n")
}

// isPublicIP 检查地址是否包含公网IP
func isPublicIP(addr multiaddr.Multiaddr) bool {
	// 提取IP地址部分
	addrStr := addr.String()

	// 检查是否是本地回环地址
	if strings.Contains(addrStr, "/ip4/127.") || strings.Contains(addrStr, "/ip6/::1") {
		return false
	}

	// 检查私有IP范围
	if strings.Contains(addrStr, "/ip4/10.") ||
		strings.Contains(addrStr, "/ip4/192.168.") ||
		strings.HasPrefix(addrStr, "/ip4/172.16.") ||
		strings.HasPrefix(addrStr, "/ip4/172.17.") ||
		strings.HasPrefix(addrStr, "/ip4/172.18.") ||
		strings.HasPrefix(addrStr, "/ip4/172.19.") ||
		strings.HasPrefix(addrStr, "/ip4/172.20.") ||
		strings.HasPrefix(addrStr, "/ip4/172.21.") ||
		strings.HasPrefix(addrStr, "/ip4/172.22.") ||
		strings.HasPrefix(addrStr, "/ip4/172.23.") ||
		strings.HasPrefix(addrStr, "/ip4/172.24.") ||
		strings.HasPrefix(addrStr, "/ip4/172.25.") ||
		strings.HasPrefix(addrStr, "/ip4/172.26.") ||
		strings.HasPrefix(addrStr, "/ip4/172.27.") ||
		strings.HasPrefix(addrStr, "/ip4/172.28.") ||
		strings.HasPrefix(addrStr, "/ip4/172.29.") ||
		strings.HasPrefix(addrStr, "/ip4/172.30.") ||
		strings.HasPrefix(addrStr, "/ip4/172.31.") {
		return false
	}

	// 检查链路本地地址
	if strings.Contains(addrStr, "/ip4/169.254.") {
		return false
	}

	// 检查IPv6私有地址
	if strings.Contains(addrStr, "/ip6/fc") || strings.Contains(addrStr, "/ip6/fd") {
		return false
	}

	// 检查IPv6内部地址
	if strings.Contains(addrStr, "/ip6/fe80:") {
		return false
	}

	// 检查是否包含IP地址（排除仅含有其他协议的地址）
	if !strings.Contains(addrStr, "/ip4/") && !strings.Contains(addrStr, "/ip6/") {
		return false
	}

	// 检查是否包含公网端口信息
	hasPort := strings.Contains(addrStr, "/tcp/") ||
		strings.Contains(addrStr, "/udp/") ||
		strings.Contains(addrStr, "/quic") ||
		strings.Contains(addrStr, "/quic-v1")

	if !hasPort {
		fmt.Printf("警告: 地址 %s 不包含端口信息，可能不完整\n", addrStr)
	}

	fmt.Printf("地址 %s 被识别为公网地址\n", addrStr)
	return true
}

// UnregisterClient 注销客户端节点
func (a *Agent) UnregisterClient(clientID peer.ID) {
	a.mu.Lock()

	// 检查客户端是否已注册
	_, exists := a.connectedClients[clientID]
	if !exists {
		fmt.Printf("[客户端注销] 客户端 %s 未注册，跳过\n", clientID.String())
		a.mu.Unlock()
		return
	}

	// 从连接列表中移除
	delete(a.connectedClients, clientID)
	a.mu.Unlock()

	fmt.Printf("\n====== 开始客户端注销流程 ======\n")
	fmt.Printf("客户端ID: %s\n", clientID.String())

	// 取消心跳监控
	a.heartbeat.UnregisterPeer(clientID)
	fmt.Printf("已移除心跳监控\n")

	// 从发现服务中移除
	a.discovery.RemovePeer(clientID)
	fmt.Printf("已从节点发现服务移除\n")

	// 检查是否仍有活跃连接
	conns := a.host.Network().ConnsToPeer(clientID)
	if len(conns) > 0 {
		fmt.Printf("警告: 注销后仍有 %d 个活跃连接\n", len(conns))
		for i, conn := range conns {
			fmt.Printf("  连接 #%d: 本地=%s, 远程=%s\n",
				i+1,
				conn.LocalMultiaddr().String(),
				conn.RemoteMultiaddr().String())
		}
	} else {
		fmt.Printf("所有连接已关闭\n")
	}

	// 可选：从peerstore中清除地址
	// 注意：这可能会影响后续的NAT穿透，因此默认不执行
	// a.host.Peerstore().ClearAddrs(clientID)

	fmt.Printf("客户端注销完成: %s\n", clientID.String())
	fmt.Printf("==============================\n")
}

// GetConnectedClientCount 获取已连接的客户端数量
func (a *Agent) GetConnectedClientCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.connectedClients)
}

// extractPublicAddrInfo 从multiaddr中提取IP和端口信息
func extractPublicAddrInfo(addr multiaddr.Multiaddr) (string, int, error) {
	// 提取IP地址
	ipComponent, _ := addr.ValueForProtocol(multiaddr.P_IP4)
	if ipComponent == "" {
		ipComponent, _ = addr.ValueForProtocol(multiaddr.P_IP6)
		if ipComponent == "" {
			return "", 0, fmt.Errorf("无法从地址中提取IP: %s", addr.String())
		}
	}

	// 优先尝试提取UDP/QUIC端口
	port := 0
	udpPort, err := addr.ValueForProtocol(multiaddr.P_UDP)
	if err == nil && udpPort != "" {
		port, _ = strconv.Atoi(udpPort)
		fmt.Printf("从地址中提取UDP端口: %d\n", port)
	} else {
		// 尝试提取QUIC端口
		quicPort, err := addr.ValueForProtocol(multiaddr.P_QUIC)
		if err == nil && quicPort != "" {
			port, _ = strconv.Atoi(quicPort)
			fmt.Printf("从地址中提取QUIC端口: %d\n", port)
		} else {
			// 如果没有UDP/QUIC端口，尝试TCP端口但打印警告
			tcpPort, err := addr.ValueForProtocol(multiaddr.P_TCP)
			if err == nil && tcpPort != "" {
				port, _ = strconv.Atoi(tcpPort)
				fmt.Printf("警告：只找到TCP端口 %d，但我们更希望使用UDP/QUIC端口\n", port)
			}
		}
	}

	if port == 0 {
		return ipComponent, 0, fmt.Errorf("无法从地址中提取端口: %s", addr.String())
	}

	return ipComponent, port, nil
}

// createTransportAgnosticAddr 创建QUIC传输协议的公网地址
func createTransportAgnosticAddr(ip string, port int) (multiaddr.Multiaddr, error) {
	var addrStr string
	if strings.Contains(ip, ":") {
		// IPv6地址
		addrStr = fmt.Sprintf("/ip6/%s", ip)
	} else {
		// IPv4地址
		addrStr = fmt.Sprintf("/ip4/%s", ip)
	}

	// 只创建QUIC地址，不再使用TCP
	quicAddr := fmt.Sprintf("%s/udp/%d/quic-v1", addrStr, port)

	// 创建多地址
	quicMA, err := multiaddr.NewMultiaddr(quicAddr)
	if err != nil {
		return nil, fmt.Errorf("无法创建QUIC multiaddr: %v", err)
	}

	return quicMA, nil
}

func main() {
	// 解析命令行参数
	listenQuic := flag.String("quic", "/ip4/0.0.0.0/udp/0/quic-v1", "QUIC监听地址")
	listenKcp := flag.String("kcp", "/ip4/0.0.0.0/udp/0/kcp", "KCP监听地址")
	certFile := flag.String("cert", "", "TLS证书文件路径（已不使用）")
	keyFile := flag.String("key", "", "TLS私钥文件路径（已不使用）")
	apiProtocol := flag.String("api-protocol", "quic-v1", "API代理协议")
	tunnelProtocol := flag.String("tunnel-protocol", "kcp", "隧道代理协议")
	flag.Parse()

	// 创建Agent节点
	agent, err := NewAgent(*listenQuic, *listenKcp, *certFile, *keyFile)
	if err != nil {
		log.Printf("创建Agent节点失败: %v\n", err)
		os.Exit(1)
	}

	// 设置协议类型
	agent.apiProtocol = *apiProtocol
	agent.tunnelProtocol = *tunnelProtocol

	// 启动Agent节点
	if err := agent.Start(); err != nil {
		log.Printf("启动Agent节点失败: %v\n", err)
		os.Exit(1)
	}

	log.Println("Agent节点已启动并运行中...")
	log.Println("按Ctrl+C停止节点")

	// 等待中断信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("接收到停止信号，正在停止Agent节点...")

	// 停止Agent节点
	if err := agent.Stop(); err != nil {
		log.Printf("停止Agent节点失败: %v\n", err)
		os.Exit(1)
	}

	log.Println("Agent节点已成功停止")
}
