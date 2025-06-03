package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"flag"
	"shiledp2p/pkg/common"
	"shiledp2p/pkg/discovery"

	manet "github.com/multiformats/go-multiaddr/net"
)

// Master 入口节点
type Master struct {
	host       host.Host
	listenAddr string
	ctx        context.Context
	cancel     context.CancelFunc

	// 仅保留一个互斥锁用于同步
	mu       sync.RWMutex
	agentSet map[peer.ID]struct{} // 仅用于标记节点是否为Agent

	heartbeat *common.HeartbeatService
	discovery *discovery.PeerDiscovery
}

// NewMaster 创建新的Master节点
func NewMaster(listenAddr, keyPath string) (*Master, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// 解析监听地址
	addr, err := multiaddr.NewMultiaddr(listenAddr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("解析监听地址失败: %w", err)
	}

	// 生成或加载私钥
	priv, err := common.GenerateOrLoadPrivateKey(keyPath)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("获取私钥失败: %w", err)
	}

	// 创建P2P节点
	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrs(addr),
		libp2p.DefaultTransports, // 包含QUIC
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("创建P2P节点失败: %w", err)
	}

	// 创建并返回Master实例
	m := &Master{
		host:       h,
		listenAddr: listenAddr,
		ctx:        ctx,
		cancel:     cancel,
		agentSet:   make(map[peer.ID]struct{}),
	}

	// 创建心跳服务
	m.heartbeat = common.NewHeartbeatService(h, 2*time.Second, 10*time.Second)
	// 设置为Master节点类型
	m.heartbeat.SetNodeType(common.TypeMaster)

	// 创建节点发现服务，但不要启动它的处理器
	m.discovery = discovery.NewPeerDiscovery(h, common.TypeMaster)

	// 重要：注册Master的各类协议处理器，避免重复和冲突

	// 1. 注册发现协议处理器
	h.SetStreamHandler(common.PeerDiscoveryProtocol, m.handleDiscovery)
	fmt.Printf("[配置] 已注册节点发现协议处理器: %s\n", common.PeerDiscoveryProtocol)

	// 2. 注册心跳协议处理器
	h.SetStreamHandler(common.HeartbeatProtocolID, m.handleHeartbeat)
	fmt.Printf("[配置] 已注册心跳协议处理器: %s\n", common.HeartbeatProtocolID)

	// 3. 注册客户端引导处理器
	h.SetStreamHandler(common.BootstrapProtocolID, m.sendAgentListToClient)
	fmt.Printf("[配置] 已注册客户端引导协议: %s\n", common.BootstrapProtocolID)

	// 4. 添加通配符处理器捕获所有协议请求，确保不会漏掉任何请求
	h.SetStreamHandler("*", func(s network.Stream) {
		proto := string(s.Protocol())
		remotePeer := s.Conn().RemotePeer()
		fmt.Printf("\n[通配符处理器] ========== 捕获到请求 ==========\n")
		fmt.Printf("[通配符处理器] 协议: %s\n", proto)
		fmt.Printf("[通配符处理器] 对端: %s\n", remotePeer.String())
		fmt.Printf("[通配符处理器] 协议字节表示: %v\n", []byte(s.Protocol()))

		// 检查是否是已知协议
		switch {
		case proto == string(common.PeerDiscoveryProtocol):
			fmt.Printf("[通配符处理器] 检测到发现协议请求，转交标准处理器\n")
			m.handleDiscovery(s)
			return
		case proto == string(common.HeartbeatProtocolID):
			fmt.Printf("[通配符处理器] 检测到心跳协议请求，转交标准处理器\n")
			m.handleHeartbeat(s)
			return
		case proto == string(common.BootstrapProtocolID):
			fmt.Printf("[通配符处理器] 检测到引导协议请求，转交标准处理器\n")
			m.sendAgentListToClient(s)
			return
		}

		// 未能通过协议字符串匹配，读取请求内容进行匹配
		buf := make([]byte, 4096)
		s.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := s.Read(buf)
		s.SetReadDeadline(time.Time{})

		if err != nil && err != io.EOF {
			fmt.Printf("[通配符处理器] 读取数据失败: %v\n", err)
			s.Close()
			return
		}

		if n > 0 {
			fmt.Printf("[通配符处理器] 读取了 %d 字节数据, 前100字节内容: %s\n",
				n, string(buf[:min(n, 100)]))

			// 创建可重复读取的流包装器
			wrapper := &streamWrapper{
				Stream: s,
				buffer: buf[:n],
				read:   false,
			}

			// 分析请求内容
			data := string(buf[:n])
			switch {
			case strings.Contains(data, "\"action\":\"register\""):
				// Agent注册请求
				fmt.Printf("[通配符处理器] 检测到可能的注册请求，转交处理\n")
				m.handleDiscovery(wrapper)
				return

			case strings.Contains(data, "\"action\":\"bootstrap\"") ||
				strings.Contains(data, "\"type\":\"client\""):
				// 客户端引导请求
				fmt.Printf("[通配符处理器] 检测到可能的客户端引导请求，转交处理\n")
				m.sendAgentListToClient(wrapper)
				return

			case strings.Contains(data, "\"action\":\"ping\"") ||
				strings.Contains(data, "\"action\":\"pong\""):
				// 心跳相关请求
				fmt.Printf("[通配符处理器] 检测到心跳相关请求，转交处理\n")
				m.handleHeartbeat(wrapper)
				return

			case strings.Contains(data, "proxy") || strings.Contains(data, "target"):
				// 代理请求 - Master不支持
				fmt.Printf("[通配符处理器] 检测到代理请求，但Master不支持代理，关闭流\n")
				s.Close()
				return
			}

			// 作为最后尝试，将非Agent节点的未知请求转给发现处理器
			if remotePeer != m.host.ID() {
				fmt.Printf("[通配符处理器] 未识别的请求，作为发现请求处理\n")
				m.handleDiscovery(wrapper)
				return
			}
		}

		// 无数据或无法识别，关闭流
		fmt.Printf("[通配符处理器] 未能处理的请求类型，关闭流\n")
		s.Close()
	})

	// 添加连接通知处理，但不自动存储非Agent信息
	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(n network.Network, c network.Conn) {
			remotePeerID := c.RemotePeer()
			fmt.Printf("\n[连接通知] 新连接已建立: %s\n", remotePeerID.String())
			fmt.Printf("[连接通知] 远程地址: %s\n", c.RemoteMultiaddr().String())

			// 只记录连接，但不主动将地址添加到peerstore，除非这是一个Agent
			fmt.Printf("[连接通知] 连接已记录，但地址未添加到peerstore，等待节点发送注册请求\n")

			// 移除任何自动添加的TTL
			m.clearNonAgentAddresses(remotePeerID)
		},
		DisconnectedF: func(n network.Network, c network.Conn) {
			remotePeerID := c.RemotePeer()
			fmt.Printf("\n[连接通知] 连接已断开: %s\n", remotePeerID.String())

			// 只在所有连接都断开时记录
			if len(n.ConnsToPeer(remotePeerID)) == 0 {
				fmt.Printf("[连接通知] 节点 %s 的所有连接已断开\n", remotePeerID.String())

				// 检查是否是Agent
				m.mu.RLock()
				_, isAgent := m.agentSet[remotePeerID]
				m.mu.RUnlock()

				if isAgent {
					// 依靠心跳机制或Agent重新注册，而不是主动移除
					fmt.Printf("[连接通知] Agent节点断开连接，但不会立即移除，等待心跳超时或重新注册\n")
				} else {
					fmt.Printf("[连接通知] 非Agent节点断开，清除其地址信息\n")
					m.clearNonAgentAddresses(remotePeerID)
				}
			}
		},
	})

	// 启动定期清理peerstore的goroutine
	// go m.monitorAgentConnections()

	// 打印节点信息
	fmt.Printf("Master 节点已创建：\n")
	fmt.Printf("  PeerID: %s\n", h.ID().String())
	fmt.Printf("  监听地址: %s\n", listenAddr)
	for _, addr := range h.Addrs() {
		fmt.Printf("  - %s/p2p/%s\n", addr.String(), h.ID().String())
	}

	return m, nil
}

// monitorAgentConnections 定期清理peerstore，但不主动检查Agent连接状态
func (m *Master) monitorAgentConnections() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			// 只清理非Agent节点的地址信息，不检查Agent的连接状态
			fmt.Println("[监控服务] 执行定期peerstore清理...")
			cleanupCount := 0

			// 获取所有peerstore中的节点
			allPeers := m.host.Peerstore().Peers()
			for _, peerID := range allPeers {
				// 跳过自己
				if peerID == m.host.ID() {
					continue
				}

				// 检查是否是Agent
				m.mu.RLock()
				_, isAgent := m.agentSet[peerID]
				m.mu.RUnlock()

				// 如果不是Agent且有地址信息，清除地址
				if !isAgent {
					addrs := m.host.Peerstore().Addrs(peerID)
					if len(addrs) > 0 {
						for _, addr := range addrs {
							m.host.Peerstore().SetAddr(peerID, addr, 0)
						}
						cleanupCount++
					}
				}
			}

			if cleanupCount > 0 {
				fmt.Printf("[监控服务] peerstore清理完成，已清除 %d 个非Agent节点的地址信息\n",
					cleanupCount)
			}

			// 打印当前Agent数量信息
			m.mu.RLock()
			agentCount := len(m.agentSet)
			m.mu.RUnlock()
			fmt.Printf("[监控服务] 当前注册的Agent数量: %d\n", agentCount)
		}
	}
}

// 清除非Agent节点的地址信息
func (m *Master) clearNonAgentAddresses(peerID peer.ID) {
	// 先检查是否是Agent
	m.mu.RLock()
	_, isAgent := m.agentSet[peerID]
	m.mu.RUnlock()

	// 如果不是Agent，清除其地址信息
	if !isAgent {
		addrs := m.host.Peerstore().Addrs(peerID)
		if len(addrs) > 0 {
			// 将所有地址的TTL设置为0，立即过期
			for _, addr := range addrs {
				m.host.Peerstore().SetAddr(peerID, addr, 0)
			}
			fmt.Printf("[peerstore管理] 已清除非Agent节点 %s 的 %d 个地址\n",
				peerID.String(), len(addrs))
		}
	}
}

// Start 启动Master节点
func (m *Master) Start() error {
	// 启动心跳服务
	m.heartbeat.Start()

	// 不启动discovery服务的处理器
	// m.discovery.Start() // 注释掉这一行，避免注册处理器冲突

	// 启动Agent连接监控
	go m.monitorAgentConnections()
	fmt.Println("已启动Agent连接监控服务")

	fmt.Println("Master 节点已启动")

	// 加密并打印公网多地址信息，用于配置DNS TXT记录
	m.printEncryptedAddresses(true) // 只打印公网地址

	return nil
}

// printEncryptedAddresses 加密并打印Master节点的多地址信息
func (m *Master) printEncryptedAddresses(publicOnly bool) {
	// 获取Master的完整多地址
	peerID := m.host.ID().String()
	addresses := m.host.Addrs()

	// 选择公网地址
	var publicAddrs []string

	// 收集所有公网地址
	for _, addr := range addresses {
		addrStr := addr.String()
		// 使用正确的公网地址判断方法
		if manet.IsPublicAddr(addr) {
			fullAddr := fmt.Sprintf("%s/p2p/%s", addrStr, peerID)
			publicAddrs = append(publicAddrs, fullAddr)
		}
	}

	// 如果没有找到公网地址，打印警告
	if len(publicAddrs) == 0 {
		fmt.Printf("\n警告: 未找到任何公网地址! Master节点可能无法被外网访问。\n")
		fmt.Printf("如需加密所有地址(包括内网地址)，请修改Start方法。\n")
		return
	}

	// 将地址列表转为JSON
	addrInfo := struct {
		PeerID     string   `json:"peer_id"`
		Multiaddrs []string `json:"multiaddrs"`
		Timestamp  int64    `json:"timestamp"`
	}{
		PeerID:     peerID,
		Multiaddrs: publicAddrs,
		Timestamp:  time.Now().Unix(),
	}

	// 序列化为JSON
	jsonData, err := json.Marshal(addrInfo)
	if err != nil {
		fmt.Printf("序列化多地址信息失败: %v\n", err)
		return
	}

	// 使用与DNS解析相同的密钥和IV进行AES加密
	aesKey := "0123456789abcdef0123456789abcdef" // 32字节密钥
	aesIV := "0123456789abcdef"                  // 16字节IV

	encryptedData, err := common.EncryptAES(string(jsonData), []byte(aesKey), []byte(aesIV))
	if err != nil {
		fmt.Printf("加密多地址信息失败: %v\n", err)
		return
	}

	// 打印加密后的信息，格式为 MASTER=<密文>
	fmt.Printf("\n=============================================================\n")
	fmt.Printf("Master节点DNS TXT记录内容 (仅包含公网地址):\n")
	fmt.Printf("MASTER=%s\n", encryptedData)
	fmt.Printf("=============================================================\n")

	// 打印纯密文，方便直接复制
	fmt.Printf("\n纯密文 (不包含MASTER=前缀):\n%s\n\n", encryptedData)

	// 打印DNS配置指南
	fmt.Printf("DNS配置指南:\n")
	fmt.Printf("1. 登录您的DNS提供商控制面板\n")
	fmt.Printf("2. 为域名 test.halochat.dev 添加TXT记录\n")
	fmt.Printf("3. 记录值设置为: MASTER=%s\n", encryptedData)
	fmt.Printf("4. TTL设置为较短时间(如300秒)方便测试，稳定后可设置更长时间\n")

	// 打印被加密的原始信息(调试用)
	fmt.Printf("\n加密前的原始JSON数据:\n%s\n", string(jsonData))
	fmt.Printf("包含 %d 个公网地址:\n", len(publicAddrs))
	for i, addr := range publicAddrs {
		fmt.Printf("  %d. %s\n", i+1, addr)
	}
}

// Stop 停止Master节点
func (m *Master) Stop() error {
	// 停止节点发现服务
	m.discovery.Stop()

	// 停止心跳服务
	m.heartbeat.Stop()

	// 关闭P2P节点
	if err := m.host.Close(); err != nil {
		return fmt.Errorf("关闭P2P节点失败: %w", err)
	}

	// 取消上下文
	m.cancel()

	fmt.Println("Master 节点已停止")
	return nil
}

// handleHeartbeat将在接收到Agent心跳时被调用
func (m *Master) handleHeartbeat(s network.Stream) {
	remotePeer := s.Conn().RemotePeer()

	// 标记为Agent并更新peerstore TTL
	m.mu.Lock()
	m.agentSet[remotePeer] = struct{}{} // 标记为Agent
	m.mu.Unlock()

	// 为peerstore中的地址设置更长的TTL
	for _, addr := range m.host.Peerstore().Addrs(remotePeer) {
		m.host.Peerstore().SetAddr(remotePeer, addr, 24*time.Hour)
	}

	// 添加心跳监控，接收到心跳则刷新TTL，心跳超时才移除Agent
	// 这样依赖Agent主动发送心跳，Master不主动检查，避免误删
	m.heartbeat.RegisterPeer(remotePeer, func() {
		fmt.Printf("[心跳回调] Agent %s 心跳超时，执行移除\n", remotePeer.String())
		m.removeAgent(remotePeer)
	})

	// 读取心跳数据
	buf := make([]byte, 4096)
	n, err := s.Read(buf)
	if err != nil && err != io.EOF {
		fmt.Printf("[Master心跳] 读取心跳数据失败: %v\n", err)
		return
	}

	// 验证读取到的数据
	if n <= 0 {
		fmt.Printf("[Master心跳] 收到来自 %s 的空心跳\n", remotePeer.String())
		return
	}

	// 解析心跳消息
	var heartbeat common.HeartbeatMessage
	if err := common.UnmarshalJSON(buf[:n], &heartbeat); err != nil {
		fmt.Printf("[Master心跳] 解析心跳消息失败: %v\n", err)
		return
	}

	// 记录心跳消息
	fmt.Printf("[Master心跳] 收到来自 %s 的心跳: action=%s, type=%s\n",
		remotePeer.String(), heartbeat.Action, heartbeat.NodeType)

	// 验证是否是ping请求
	if heartbeat.Action != "ping" {
		fmt.Printf("[Master心跳] 收到非ping请求: %s\n", heartbeat.Action)
		// 尝试回复错误
		errorResponse := common.CreateHeartbeatError("invalid_action", "Master", m.host.ID().String())
		respBytes, _ := common.WriteMessageToStream(errorResponse)
		s.Write(respBytes)
		return
	}

	// 创建心跳响应
	response := common.CreateHeartbeatPong("Master", m.host.ID().String())
	respBytes, err := common.WriteMessageToStream(response)
	if err != nil {
		fmt.Printf("[Master心跳] 创建响应失败: %v\n", err)
		return
	}

	// 设置写入超时并发送响应
	s.SetWriteDeadline(time.Now().Add(5 * time.Second))
	n, err = s.Write(respBytes)
	s.SetWriteDeadline(time.Time{})

	if err != nil {
		fmt.Printf("[Master心跳] 写入响应失败: %v\n", err)
		return
	}

	fmt.Printf("[Master心跳] 发送响应到 %s: %s (%d字节)\n", remotePeer.String(), string(respBytes), n)

	// 不要立即关闭流，让它保持打开状态以供后续心跳使用
}

// handleDiscovery 处理节点发现请求
func (m *Master) handleDiscovery(s network.Stream) {
	remotePeer := s.Conn().RemotePeer()
	fmt.Printf("\n[发现处理器] ======== 收到连接请求 ========\n")
	fmt.Printf("[发现处理器] 远程节点ID: %s\n", remotePeer.String())
	fmt.Printf("[发现处理器] 远程地址: %s\n", s.Conn().RemoteMultiaddr().String())
	fmt.Printf("[发现处理器] 使用协议: %s\n", s.Protocol())

	// 读取请求内容
	buf := make([]byte, 4096)
	n, err := s.Read(buf)
	if err != nil && err != io.EOF {
		fmt.Printf("[发现处理器] 读取请求失败: %v\n", err)
	}

	isAgentRegistration := false
	isClientBootstrap := false

	if n > 0 {
		// 打印收到的数据
		fmt.Printf("[发现处理器] 收到数据(%d字节): %s\n", n, string(buf[:n]))

		// 尝试解析为标准请求结构
		var request common.StandardRequest
		if err := json.Unmarshal(buf[:n], &request); err == nil {
			fmt.Printf("[发现处理器] 解析请求: Action=%s, NodeType=%v\n",
				request.Action, request.NodeType)

			// 检查请求类型
			if request.Action == "register" && request.NodeType == common.TypeAgent {
				isAgentRegistration = true
				fmt.Printf("[发现处理器] 确认是Agent注册请求, NodeType=%v\n", request.NodeType)

				// 如果提供了地址信息，直接添加到peerstore
				if len(request.Addrs) > 0 {
					fmt.Printf("[发现处理器] 收到 %d 个地址\n", len(request.Addrs))
					publicAddrsCount := 0

					for _, addrStr := range request.Addrs {
						addr, err := multiaddr.NewMultiaddr(addrStr)
						if err != nil {
							fmt.Printf("[发现处理器] 解析地址失败: %s, %v\n", addrStr, err)
							continue
						}

						// 优先保存公网地址
						if manet.IsPublicAddr(addr) {
							m.host.Peerstore().AddAddr(remotePeer, addr, 24*time.Hour)
							fmt.Printf("[发现处理器] 已添加公网地址到peerstore: %s\n", addr.String())
							publicAddrsCount++
						} else if !manet.IsIPLoopback(addr) {
							// 如果不是公网地址，但也不是回环地址，则添加但设置较短的TTL
							m.host.Peerstore().AddAddr(remotePeer, addr, 1*time.Hour)
							fmt.Printf("[发现处理器] 已添加非公网地址到peerstore(较短TTL): %s\n", addr.String())
						} else {
							fmt.Printf("[发现处理器] 忽略回环地址: %s\n", addr.String())
						}
					}

					fmt.Printf("[发现处理器] 成功添加 %d 个公网地址到peerstore\n", publicAddrsCount)
				}
			} else if request.Action == "bootstrap" || request.NodeType == common.TypeClient {
				fmt.Printf("[发现处理器] 检测到客户端引导请求\n")
				isClientBootstrap = true
			}
		}

		// 如果还是无法识别请求类型，尝试通过内容判断
		if !isAgentRegistration && !isClientBootstrap {
			// 尝试再次解析，使用更宽松的方式
			var data map[string]interface{}
			if err := json.Unmarshal(buf[:n], &data); err == nil {
				// 检查action字段
				if action, ok := data["action"].(string); ok {
					// 检查node_type字段
					if nodeType, ok := data["node_type"].(float64); ok {
						if action == "register" && int(nodeType) == int(common.TypeAgent) {
							isAgentRegistration = true
							fmt.Printf("[发现处理器] 通过字段检测确认是Agent注册请求\n")
						} else if action == "bootstrap" || int(nodeType) == int(common.TypeClient) {
							isClientBootstrap = true
							fmt.Printf("[发现处理器] 通过字段检测确认是客户端引导请求\n")
						}
					}
				}
			}
		}
	} else {
		fmt.Printf("[发现处理器] 未收到任何数据，尝试作为Agent注册处理\n")
		// 如果没有收到数据，但协议是discovery，也尝试注册为Agent
		if strings.Contains(string(s.Protocol()), "discovery") {
			fmt.Printf("[发现处理器] 基于协议推断可能是Agent注册\n")
			isAgentRegistration = true
		}
	}

	// 确保连接地址被添加到peerstore，无论是什么请求
	remoteAddr := s.Conn().RemoteMultiaddr()
	if remoteAddr != nil {
		addrStr := remoteAddr.String()
		fmt.Printf("[发现处理器] 添加连接地址到peerstore: %s\n", addrStr)
		// 添加连接地址，设置24小时TTL
		m.host.Peerstore().AddAddr(remotePeer, remoteAddr, 24*time.Hour)
	}

	// 处理Agent注册
	if isAgentRegistration {
		fmt.Printf("[发现处理器] 确认是Agent注册请求，更新Agent状态\n")
		m.updateAgentStatus(remotePeer)

		// 发送成功响应
		response := common.NewStandardResponse(
			"register",
			"success",
			"Agent注册成功",
			common.TypeMaster,
			m.host.ID().String(),
		)

		// 使用统一的JSON序列化方法
		respBytes, err := common.JSONMarshalWithNewline(response)
		if err != nil {
			fmt.Printf("[发现处理器] 序列化响应失败: %v\n", err)
			s.Close()
			return
		}

		_, err = s.Write(respBytes)
		if err != nil {
			fmt.Printf("[发现处理器] 发送注册响应失败: %v\n", err)
		} else {
			fmt.Printf("[发现处理器] 成功发送注册响应\n")
		}
	}

	// 处理客户端引导请求
	if isClientBootstrap {
		fmt.Printf("[发现处理器] 发送Agent列表给客户端\n")
		m.sendAgentListToClient(s)
		return // 已处理完成，不再发送额外响应
	}

	// 对于其他未处理的请求，发送通用成功响应
	if !isAgentRegistration && !isClientBootstrap {
		response := common.NewStandardResponse(
			"unknown",
			"success",
			"请求已处理",
			common.TypeMaster,
			m.host.ID().String(),
		)

		// 使用统一的JSON序列化方法
		respBytes, err := common.JSONMarshalWithNewline(response)
		if err != nil {
			fmt.Printf("[发现处理器] 序列化响应失败: %v\n", err)
			s.Close()
			return
		}

		_, err = s.Write(respBytes)
		if err != nil {
			fmt.Printf("[发现处理器] 发送响应失败: %v\n", err)
		} else {
			fmt.Printf("[发现处理器] 成功发送响应\n")
		}
	}

	// 关闭流
	s.Close()
}

// 更新Agent状态
func (m *Master) updateAgentStatus(peerID peer.ID) {
	// 先标记为Agent
	m.mu.Lock()
	_, exists := m.agentSet[peerID]
	m.agentSet[peerID] = struct{}{}
	agentCount := len(m.agentSet)
	m.mu.Unlock()

	if !exists {
		fmt.Printf("[Agent注册] ===== 新Agent注册 =====\n")
	} else {
		fmt.Printf("[Agent注册] ===== 更新已有Agent =====\n")
	}
	fmt.Printf("[Agent注册] Agent ID: %s\n", peerID.String())

	// 在peerstore中设置TTL以保持Agent信息
	addresses := m.host.Peerstore().Addrs(peerID)
	fmt.Printf("[Agent注册] peerstore中的地址数量: %d\n", len(addresses))

	// 记录公网地址数量
	publicAddrCount := 0
	for _, addr := range addresses {
		if manet.IsPublicAddr(addr) {
			publicAddrCount++
		}
	}
	fmt.Printf("[Agent注册] 公网地址数量: %d\n", publicAddrCount)

	// 确保至少有一个地址，即使是内网地址
	if len(addresses) == 0 {
		fmt.Printf("[Agent注册] 警告: Agent没有地址信息，尝试从连接中获取...\n")
		// 尝试从当前连接获取地址
		conns := m.host.Network().ConnsToPeer(peerID)
		for _, conn := range conns {
			remoteAddr := conn.RemoteMultiaddr()
			if remoteAddr != nil {
				// 按地址类型添加不同TTL
				if manet.IsPublicAddr(remoteAddr) {
					m.host.Peerstore().AddAddr(peerID, remoteAddr, 24*time.Hour)
					fmt.Printf("[Agent注册] 已从连接添加公网地址: %s (24h TTL)\n", remoteAddr.String())
					publicAddrCount++
				} else if !manet.IsIPLoopback(remoteAddr) {
					m.host.Peerstore().AddAddr(peerID, remoteAddr, 1*time.Hour)
					fmt.Printf("[Agent注册] 已从连接添加内网地址: %s (1h TTL)\n", remoteAddr.String())
				} else {
					fmt.Printf("[Agent注册] 忽略从连接获取的回环地址: %s\n", remoteAddr.String())
				}
			}
		}
		// 再次获取地址列表
		addresses = m.host.Peerstore().Addrs(peerID)
	}

	// 设置地址TTL
	for i, addr := range addresses {
		// 为公网和内网地址设置不同的TTL
		if manet.IsPublicAddr(addr) {
			// 公网地址设置长TTL
			m.host.Peerstore().SetAddr(peerID, addr, 24*time.Hour)
			fmt.Printf("[Agent注册] 公网地址 #%d: %s (TTL已设置为24小时)\n", i+1, addr.String())
		} else if !manet.IsIPLoopback(addr) {
			// 内网非回环地址设置中等TTL
			m.host.Peerstore().SetAddr(peerID, addr, 1*time.Hour)
			fmt.Printf("[Agent注册] 内网地址 #%d: %s (TTL已设置为1小时)\n", i+1, addr.String())
		} else {
			// 回环地址设置短TTL
			m.host.Peerstore().SetAddr(peerID, addr, 5*time.Minute)
			fmt.Printf("[Agent注册] 回环地址 #%d: %s (TTL已设置为5分钟)\n", i+1, addr.String())
		}
	}

	// 注册心跳回调，如果Agent离线了会被调用
	m.heartbeat.RegisterPeer(peerID, func() {
		fmt.Printf("[Agent监控] 检测到Agent %s 离线，将移除\n", peerID.String())
		m.removeAgent(peerID)
	})

	fmt.Printf("[Agent注册] Agent已更新，当前在线Agent数量: %d\n", agentCount)

	// 立即检查并确认Agent是否在agentSet中
	m.mu.RLock()
	_, confirmed := m.agentSet[peerID]
	m.mu.RUnlock()
	fmt.Printf("[Agent注册] 确认Agent是否在agentSet中: %v\n", confirmed)

	// 打印当前所有的Agent及其地址
	m.logAgentsFromPeerstore()
}

// 从peerstore获取并打印Agent信息
func (m *Master) logAgentsFromPeerstore() {
	m.mu.RLock()
	agents := make([]peer.ID, 0, len(m.agentSet))
	for agentID := range m.agentSet {
		agents = append(agents, agentID)
	}
	agentCount := len(m.agentSet)
	m.mu.RUnlock()

	fmt.Printf("[Agent信息] ===== 当前标记的Agent (%d个) =====\n", agentCount)
	if agentCount == 0 {
		fmt.Printf("[Agent信息] 警告: 没有标记的Agent!\n")
	}

	for i, agentID := range agents {
		// 获取地址信息
		addrs := m.host.Peerstore().Addrs(agentID)
		// 检查连接状态
		isConnected := m.host.Network().Connectedness(agentID) == network.Connected

		fmt.Printf("[Agent信息] Agent #%d: %s\n", i+1, agentID.String())
		fmt.Printf("[Agent信息]   连接状态: %v\n", isConnected)
		fmt.Printf("[Agent信息]   地址数量: %d\n", len(addrs))

		// 打印所有地址，因为对诊断非常重要
		for j, addr := range addrs {
			fmt.Printf("[Agent信息]   地址 #%d: %s\n", j+1, addr.String())
		}

		if len(addrs) == 0 {
			fmt.Printf("[Agent信息]   警告: 此Agent没有地址信息!\n")
		}
	}

	// 额外检查: 打印所有在peerstore中有地址信息的节点
	allPeers := m.host.Peerstore().Peers()
	fmt.Printf("[Agent信息] ===== peerstore中有地址的节点 (总计%d个) =====\n", len(allPeers))
	for i, peerID := range allPeers {
		// 跳过自己
		if peerID == m.host.ID() {
			continue
		}

		// 检查是否标记为Agent
		m.mu.RLock()
		_, isAgent := m.agentSet[peerID]
		m.mu.RUnlock()

		// 获取地址信息
		addrs := m.host.Peerstore().Addrs(peerID)
		if len(addrs) == 0 {
			continue // 跳过没有地址信息的节点
		}

		fmt.Printf("[Agent信息] 节点 #%d: %s\n", i+1, peerID.String())
		fmt.Printf("[Agent信息]   是否标记为Agent: %v\n", isAgent)
		fmt.Printf("[Agent信息]   地址数量: %d\n", len(addrs))
		fmt.Printf("[Agent信息]   地址示例: %s\n", addrs[0].String())

		if !isAgent {
			fmt.Printf("[Agent信息]   警告: 此节点有地址但未标记为Agent!\n")
		}
	}

	fmt.Printf("[Agent信息] ===================================\n")
}

// 移除Agent
func (m *Master) removeAgent(peerID peer.ID) {
	// 从标记集合中移除
	m.mu.Lock()
	delete(m.agentSet, peerID)
	agentCount := len(m.agentSet)
	m.mu.Unlock()

	// 注销心跳监控
	m.heartbeat.UnregisterPeer(peerID)

	// 清除peerstore中的地址信息
	addrs := m.host.Peerstore().Addrs(peerID)
	if len(addrs) > 0 {
		fmt.Printf("[Agent下线] 清除Agent %s 的 %d 个地址信息\n",
			peerID.String(), len(addrs))

		// 将所有地址的TTL设置为0，立即过期
		for _, addr := range addrs {
			m.host.Peerstore().SetAddr(peerID, addr, 0)
		}

		// 确认地址已被清除
		remainingAddrs := m.host.Peerstore().Addrs(peerID)
		if len(remainingAddrs) > 0 {
			fmt.Printf("[Agent下线] 警告：地址清除不完全，仍有 %d 个地址\n",
				len(remainingAddrs))
		} else {
			fmt.Printf("[Agent下线] 地址清除成功\n")
		}
	}

	fmt.Printf("[Agent下线] Agent已移除: %s，剩余Agent数量: %d\n",
		peerID.String(), agentCount)
}

// 向客户端发送Agent列表
func (m *Master) sendAgentListToClient(s network.Stream) {
	remotePeer := s.Conn().RemotePeer()
	fmt.Printf("\n[Client引导] ========== 处理Client引导请求 ==========\n")
	fmt.Printf("[Client引导] Client ID: %s\n", remotePeer.String())
	fmt.Printf("[Client引导] 使用协议: %s\n", s.Protocol())

	// 先读取请求内容，确保能够正确处理请求
	buf := make([]byte, 4096)
	n, err := s.Read(buf)

	if err != nil && err != io.EOF {
		fmt.Printf("[Client引导] 读取请求失败: %v\n", err)
		// 发送错误响应
		resp := common.BootstrapResponse{
			Status:    "error",
			Message:   fmt.Sprintf("读取请求失败: %v", err),
			Error:     fmt.Sprintf("读取请求失败: %v", err),
			NodeType:  common.TypeMaster,
			From:      m.host.ID().String(),
			Timestamp: time.Now().UnixNano(),
			Action:    "bootstrap",
			Agents:    []common.AgentInfo{},
			Count:     0,
		}
		respBytes, _ := common.JSONMarshalWithNewline(resp)
		s.Write(respBytes)
		s.Close()
		return
	}

	// 如果收到数据，打印出来以便调试
	if n > 0 {
		fmt.Printf("[Client引导] 收到请求数据(%d字节): %s\n", n, string(buf[:n]))

		// 尝试解析请求，但不强制要求
		var request common.StandardRequest
		if err := json.Unmarshal(buf[:n], &request); err == nil {
			fmt.Printf("[Client引导] 解析请求: Action=%s, NodeType=%v\n",
				request.Action, request.NodeType)
		}
	} else {
		fmt.Printf("[Client引导] 未收到请求数据，继续处理\n")
	}

	// 获取所有标记为Agent的节点
	m.mu.RLock()
	var agents []peer.ID
	for agentID := range m.agentSet {
		agents = append(agents, agentID)
	}
	agentCount := len(agents)
	m.mu.RUnlock()

	fmt.Printf("[Client引导] 已标记的Agent总数: %d\n", agentCount)

	// 过滤出在线的Agent节点，放宽条件
	var onlineAgents []peer.ID
	var maybeOnlineAgents []peer.ID
	for _, agentID := range agents {
		// 检查连接状态
		isConnected := m.host.Network().Connectedness(agentID) == network.Connected

		// 检查是否有地址信息
		addrs := m.host.Peerstore().Addrs(agentID)
		hasAddrs := len(addrs) > 0

		if isConnected {
			onlineAgents = append(onlineAgents, agentID)
			fmt.Printf("[Client引导] Agent %s 在线，将被添加\n", agentID.String())
		} else if hasAddrs {
			// 即使不是Connected状态，只要有地址信息，也视为可能在线
			maybeOnlineAgents = append(maybeOnlineAgents, agentID)
			fmt.Printf("[Client引导] Agent %s 可能在线(有地址信息)，也将被添加\n", agentID.String())
		} else {
			fmt.Printf("[Client引导] Agent %s 无连接且无地址信息，将被过滤\n", agentID.String())
		}
	}

	// 合并确定在线和可能在线的Agent
	if len(onlineAgents) == 0 && len(maybeOnlineAgents) > 0 {
		fmt.Printf("[Client引导] 未找到确定在线的Agent，将使用 %d 个可能在线的Agent\n", len(maybeOnlineAgents))
		onlineAgents = maybeOnlineAgents
	} else if len(maybeOnlineAgents) > 0 {
		fmt.Printf("[Client引导] 除了 %d 个确定在线的Agent外，还添加 %d 个可能在线的Agent\n",
			len(onlineAgents), len(maybeOnlineAgents))
		onlineAgents = append(onlineAgents, maybeOnlineAgents...)
	}

	fmt.Printf("[Client引导] 最终使用的Agent数量: %d/%d\n", len(onlineAgents), agentCount)

	// 如果没有在线Agent，尝试从peerstore中获取所有可能的Agent
	if len(onlineAgents) == 0 {
		fmt.Printf("[Client引导] 警告: 没有在线Agent，尝试从peerstore寻找可能的Agent\n")
		// 直接从peerstore获取所有节点
		allPeers := m.host.Peerstore().Peers()
		fmt.Printf("[Client引导] peerstore中的节点总数: %d\n", len(allPeers))

		// 过滤掉自己和客户端，放宽条件，不仅要连接的，有地址信息的也要
		for _, peerID := range allPeers {
			// 不包括自己和请求的客户端
			if peerID == m.host.ID() || peerID == remotePeer {
				continue
			}

			// 检查节点是否有地址信息
			addrs := m.host.Peerstore().Addrs(peerID)
			isConnected := m.host.Network().Connectedness(peerID) == network.Connected

			// 只要有地址信息就添加，不要求必须已连接
			if len(addrs) > 0 {
				// 立即标记为Agent
				m.mu.Lock()
				m.agentSet[peerID] = struct{}{}
				m.mu.Unlock()
				onlineAgents = append(onlineAgents, peerID)

				if isConnected {
					fmt.Printf("[Client引导] 将已连接节点 %s 视为可能的Agent\n", peerID.String())
				} else {
					fmt.Printf("[Client引导] 将有地址但未连接的节点 %s 视为可能的Agent\n", peerID.String())
				}
			}
		}

		fmt.Printf("[Client引导] 从peerstore找到可能的Agent数量: %d\n", len(onlineAgents))
	}

	// 打印所有可用Agent的详细信息
	fmt.Printf("[Client引导] 可用Agent详细信息:\n")
	for i, agentID := range onlineAgents {
		// 获取地址信息
		agentAddrs := m.host.Peerstore().Addrs(agentID)
		isConnected := m.host.Network().Connectedness(agentID) == network.Connected

		fmt.Printf("  Agent #%d: %s\n", i+1, agentID.String())
		fmt.Printf("    连接状态: %v\n", isConnected)
		fmt.Printf("    地址数量: %d\n", len(agentAddrs))

		// 优先使用公网地址，其次是内网非回环地址
		primaryAddrs := make([]multiaddr.Multiaddr, 0)
		backupAddrs := make([]multiaddr.Multiaddr, 0)

		for _, addr := range agentAddrs {
			if manet.IsPublicAddr(addr) {
				primaryAddrs = append(primaryAddrs, addr)
			} else if !manet.IsIPLoopback(addr) {
				backupAddrs = append(backupAddrs, addr)
			}
		}

		// 使用的地址列表: 如果有公网地址，优先使用；否则使用内网非回环地址
		var finalAddrs []multiaddr.Multiaddr
		if len(primaryAddrs) > 0 {
			finalAddrs = primaryAddrs
			fmt.Printf("    使用 %d 个公网地址\n", len(primaryAddrs))
		} else if len(backupAddrs) > 0 {
			finalAddrs = backupAddrs
			fmt.Printf("    没有公网地址，使用 %d 个内网地址\n", len(backupAddrs))
		} else {
			fmt.Printf("    没有可用地址，跳过\n")
			continue
		}

		// 获取地址字符串
		addrStrings := common.GetMultiaddrsString(finalAddrs)

		// 打印地址信息
		fmt.Printf("    地址数量: %d\n", len(addrStrings))
		for j, addr := range addrStrings {
			if j < 3 {
				fmt.Printf("      %d. %s\n", j+1, addr)
			} else if j == 3 {
				fmt.Printf("      ... 还有 %d 个地址未显示\n", len(addrStrings)-3)
				break
			}
		}
	}

	// 如果没有可用的Agent，返回错误
	if len(onlineAgents) == 0 {
		fmt.Println("[Client引导] 没有可用的Agent节点，发送错误响应")
		response := common.BootstrapResponse{
			Status:    "error",
			Message:   "没有可用的Agent节点",
			Error:     "没有找到有效的Agent节点",
			NodeType:  common.TypeMaster,
			From:      m.host.ID().String(),
			Timestamp: time.Now().UnixNano(),
			Action:    "bootstrap",
			Agents:    []common.AgentInfo{},
			Count:     0,
		}

		// 发送响应
		jsonBytes, err := common.JSONMarshalWithNewline(response)
		if err != nil {
			fmt.Printf("[Client引导] JSON序列化失败: %v\n", err)
			s.Close()
			return
		}

		fmt.Printf("[Client引导] 错误响应JSON: %s\n", string(jsonBytes))
		s.Write(jsonBytes)
		s.Close()
		fmt.Println("[Client引导] 错误响应已发送，流已关闭")
		return
	}

	// 准备Agent信息列表
	agentInfoList := make([]common.AgentInfo, 0, len(onlineAgents))

	for _, agentID := range onlineAgents {
		// 从peerstore获取Agent的地址信息
		agentAddrs := m.host.Peerstore().Addrs(agentID)

		// 如果没有地址，跳过这个Agent
		if len(agentAddrs) == 0 {
			fmt.Printf("[Client引导] Agent %s 没有地址信息，跳过\n", agentID.String())
			continue
		}

		// 优先使用公网地址，其次是内网非回环地址
		primaryAddrs := make([]multiaddr.Multiaddr, 0)
		backupAddrs := make([]multiaddr.Multiaddr, 0)

		for _, addr := range agentAddrs {
			if manet.IsPublicAddr(addr) {
				primaryAddrs = append(primaryAddrs, addr)
			} else if !manet.IsIPLoopback(addr) {
				backupAddrs = append(backupAddrs, addr)
			}
		}

		// 使用的地址列表: 如果有公网地址，优先使用；否则使用内网非回环地址
		var finalAddrs []multiaddr.Multiaddr
		if len(primaryAddrs) > 0 {
			finalAddrs = primaryAddrs
			fmt.Printf("[Client引导] Agent %s 使用 %d 个公网地址\n", agentID.String(), len(primaryAddrs))
		} else if len(backupAddrs) > 0 {
			finalAddrs = backupAddrs
			fmt.Printf("[Client引导] Agent %s 没有公网地址，使用 %d 个内网地址\n", agentID.String(), len(backupAddrs))
		} else {
			fmt.Printf("[Client引导] Agent %s 没有可用地址，跳过\n", agentID.String())
			continue
		}

		// 获取地址字符串
		addrStrings := common.GetMultiaddrsString(finalAddrs)

		// 添加Agent信息
		info := common.AgentInfo{
			PeerID:     agentID.String(),
			Multiaddrs: addrStrings,
		}

		agentInfoList = append(agentInfoList, info)
		fmt.Printf("[Client引导] 已添加Agent: %s, 地址数量: %d\n",
			agentID.String(), len(addrStrings))
	}

	// 检查是否有有效的Agent信息
	if len(agentInfoList) == 0 {
		fmt.Println("[Client引导] 所有Agent都没有有效的地址信息，发送错误响应")
		response := common.BootstrapResponse{
			Status:    "error",
			Message:   "所有Agent都没有有效的地址信息",
			Error:     "所有Agent都没有有效的地址信息",
			NodeType:  common.TypeMaster,
			From:      m.host.ID().String(),
			Timestamp: time.Now().UnixNano(),
			Action:    "bootstrap",
			Agents:    []common.AgentInfo{},
			Count:     0,
		}

		// 发送响应
		jsonBytes, err := common.JSONMarshalWithNewline(response)
		if err != nil {
			fmt.Printf("[Client引导] JSON序列化失败: %v\n", err)
			s.Close()
			return
		}

		fmt.Printf("[Client引导] 错误响应JSON: %s\n", string(jsonBytes))
		s.Write(jsonBytes)
		s.Close()
		fmt.Println("[Client引导] 错误响应已发送，流已关闭")
		return
	}

	// 准备成功响应
	response := common.BootstrapResponse{
		Status:    "success",
		Message:   "成功获取Agent列表",
		NodeType:  common.TypeMaster,
		From:      m.host.ID().String(),
		Timestamp: time.Now().UnixNano(),
		Action:    "bootstrap",
		Agents:    agentInfoList,
		Count:     len(agentInfoList),
	}

	// 发送响应
	fmt.Println("[Client引导] 开始发送Agent信息响应")
	jsonBytes, err := common.JSONMarshalWithNewline(response)
	if err != nil {
		fmt.Printf("[Client引导] JSON序列化失败: %v\n", err)
		s.Close()
		return
	}

	fmt.Printf("[Client引导] 响应JSON (前200字节): %s\n", string(jsonBytes[:min(200, len(jsonBytes))]))

	_, err = s.Write(jsonBytes)
	if err != nil {
		fmt.Printf("[Client引导] 发送响应失败: %v\n", err)
	} else {
		fmt.Printf("[Client引导] 成功发送响应，共 %d 个Agent\n", len(agentInfoList))
	}

	// 关闭流
	s.Close()
	fmt.Printf("[Client引导] ========== 引导请求处理完成 ===========\n\n")
}

// 创建一个流包装器，用于在通配符处理器中重用已读取的数据
type streamWrapper struct {
	network.Stream
	buffer []byte
	read   bool
}

func (s *streamWrapper) Read(p []byte) (int, error) {
	if !s.read && len(s.buffer) > 0 {
		n := copy(p, s.buffer)
		s.read = true
		return n, nil
	}
	return s.Stream.Read(p)
}

// 添加min函数
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// main 函数是程序入口点
func main() {
	// 解析命令行参数
	listenQuic := flag.String("quic", "/ip4/0.0.0.0/udp/8440/quic-v1", "QUIC监听地址")
	keyPath := flag.String("key", "data/master.key", "私钥文件路径")
	flag.Parse()

	// 创建Master节点
	master, err := NewMaster(*listenQuic, *keyPath)
	if err != nil {
		fmt.Printf("创建Master节点失败: %v\n", err)
		os.Exit(1)
	}

	// 启动Master节点
	if err := master.Start(); err != nil {
		fmt.Printf("启动Master节点失败: %v\n", err)
		os.Exit(1)
	}

	// 等待中断信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	// 停止Master节点
	if err := master.Stop(); err != nil {
		fmt.Printf("停止Master节点失败: %v\n", err)
		os.Exit(1)
	}
}
