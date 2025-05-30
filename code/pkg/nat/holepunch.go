package nat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"shiledp2p/pkg/common"
)

// NATType 表示NAT类型
type NATType int

const (
	NATUnknown        NATType = iota
	NATOpen                   // 公网或完全开放NAT
	NATFull                   // 完全锥形NAT
	NATRestricted             // 限制型锥形NAT
	NATPortRestricted         // 端口限制型锥形NAT
	NATSymmetric              // 对称型NAT，最难穿透
)

// HolePunchCoordinator 协调NAT穿透打洞过程
type HolePunchCoordinator struct {
	host         host.Host
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.RWMutex
	peerRequests map[peer.ID]chan peer.ID
	natType      NATType             // 本节点的NAT类型
	peerNATTypes map[peer.ID]NATType // 保存其他节点的NAT类型

	// 请求缓存，保存未注册节点的打洞请求
	cacheMu    sync.RWMutex
	cachedReqs map[peer.ID][]peer.ID // 目标节点ID -> 请求者节点ID列表
}

// NewHolePunchCoordinator 创建新的打洞协调器
func NewHolePunchCoordinator(h host.Host) *HolePunchCoordinator {
	ctx, cancel := context.WithCancel(context.Background())
	return &HolePunchCoordinator{
		host:         h,
		ctx:          ctx,
		cancel:       cancel,
		peerRequests: make(map[peer.ID]chan peer.ID),
		natType:      NATUnknown,
		peerNATTypes: make(map[peer.ID]NATType),
		cachedReqs:   make(map[peer.ID][]peer.ID),
	}
}

// Start 启动打洞协调服务
func (hpc *HolePunchCoordinator) Start() {
	common.RegisterStreamHandler(hpc.host, common.HolePunchingProtocolID, hpc.handleHolePunch)

	// 添加自动NAT类型检测
	go hpc.detectNATType()
}

// Stop 停止打洞协调服务
func (hpc *HolePunchCoordinator) Stop() {
	hpc.cancel()
}

// detectNATType 尝试检测本机的NAT类型
func (hpc *HolePunchCoordinator) detectNATType() {
	// 等待一段时间让连接建立
	time.Sleep(5 * time.Second)

	// 默认假设NAT是受限的
	natType := NATPortRestricted

	// 检查是否有公网地址
	hasPublicAddr := false
	var publicAddrs []string

	for _, addr := range hpc.host.Addrs() {
		addrStr := addr.String()

		// 提取IP地址
		var ip string
		ma := addr
		if val, err := ma.ValueForProtocol(multiaddr.P_IP4); err == nil {
			ip = val
		} else if val, err := ma.ValueForProtocol(multiaddr.P_IP6); err == nil {
			ip = val
		}

		// 检查是否为公网IP
		if ip != "" && isPublicIP(ip) {
			hasPublicAddr = true
			publicAddrs = append(publicAddrs, addrStr)
		}
	}

	if hasPublicAddr {
		natType = NATOpen
		fmt.Println("[NAT检测] 检测到公网地址，NAT类型: 开放")
		for i, addr := range publicAddrs {
			fmt.Printf("[NAT检测] 公网地址 #%d: %s\n", i+1, addr)
		}
	} else {
		// 未来可以实现更复杂的NAT类型检测算法
		// 例如STUN协议测试不同类型的NAT
		fmt.Println("[NAT检测] 未检测到公网地址，假设NAT类型: 端口限制型")
	}

	hpc.mu.Lock()
	hpc.natType = natType
	hpc.mu.Unlock()

	fmt.Printf("[NAT检测] 完成NAT类型检测: %v\n", natType)
}

// GetNATType 获取当前节点的NAT类型
func (hpc *HolePunchCoordinator) GetNATType() NATType {
	hpc.mu.RLock()
	defer hpc.mu.RUnlock()
	return hpc.natType
}

// handleHolePunch 处理打洞请求
func (hpc *HolePunchCoordinator) handleHolePunch(s network.Stream) {
	defer s.Close()

	remotePeer := s.Conn().RemotePeer()
	fmt.Printf("[打洞处理] 收到来自 %s 的打洞请求\n", remotePeer.String())

	// 读取目标peer ID和附加信息
	buf := make([]byte, 8192) // 增大缓冲区以容纳更多信息
	n, err := s.Read(buf)
	if err != nil && err != io.EOF {
		fmt.Printf("[打洞处理] 读取打洞请求失败: %v\n", err)
		return
	}

	// 尝试解析为结构化消息
	type HolePunchRequest struct {
		TargetID    string   `json:"target_id"`
		NATType     NATType  `json:"nat_type"`
		SelfAddrs   []string `json:"self_addrs,omitempty"`
		TargetAddrs []string `json:"target_addrs,omitempty"`
	}

	var message HolePunchRequest

	if err := json.Unmarshal(buf[:n], &message); err != nil {
		// 如果不是JSON，就按旧格式处理，只包含目标ID
		targetPeerIDStr := string(buf[:n])
		message.TargetID = targetPeerIDStr
		fmt.Printf("[打洞处理] 请求使用简单格式，不包含额外信息\n")
	} else {
		fmt.Printf("[打洞处理] 成功解析增强的打洞请求\n")

		// 存储对方的NAT类型信息
		if message.NATType != NATUnknown {
			hpc.mu.Lock()
			hpc.peerNATTypes[remotePeer] = message.NATType
			hpc.mu.Unlock()
			fmt.Printf("[打洞处理] 接收到对方NAT类型: %v\n", message.NATType)
		}

		// 处理发送方的地址信息
		if len(message.SelfAddrs) > 0 {
			fmt.Printf("[打洞处理] 收到发送方 %s 的 %d 个地址\n",
				remotePeer.String(), len(message.SelfAddrs))

			addedCount := 0
			publicAddrCount := 0

			for i, addrStr := range message.SelfAddrs {
				addr, err := multiaddr.NewMultiaddr(addrStr)
				if err != nil {
					fmt.Printf("[打洞处理] 解析地址失败: %s - %v\n", addrStr, err)
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
				if ip != "" && !isPublicIP(ip) {
					fmt.Printf("[打洞处理] 跳过内网地址: %s (IP: %s)\n", addr.String(), ip)
					continue
				}

				if ip != "" {
					publicAddrCount++
				}

				// 添加发送方地址到peerstore
				hpc.host.Peerstore().AddAddr(remotePeer, addr, time.Hour)
				fmt.Printf("[打洞处理] 添加发送方地址 #%d: %s\n", i+1, addr.String())
				addedCount++
			}

			fmt.Printf("[打洞处理] 成功添加 %d 个地址（其中公网地址: %d 个）\n",
				addedCount, publicAddrCount)
		}

		// 如果包含目标地址信息
		if len(message.TargetAddrs) > 0 {
			targetPeerID, err := peer.Decode(message.TargetID)
			if err == nil {
				fmt.Printf("[打洞处理] 收到目标节点 %s 的 %d 个地址\n",
					message.TargetID, len(message.TargetAddrs))

				addedCount := 0
				publicAddrCount := 0

				for i, addrStr := range message.TargetAddrs {
					addr, err := multiaddr.NewMultiaddr(addrStr)
					if err != nil {
						fmt.Printf("[打洞处理] 解析目标地址失败: %s - %v\n", addrStr, err)
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
					if ip != "" && !isPublicIP(ip) {
						fmt.Printf("[打洞处理] 跳过内网目标地址: %s (IP: %s)\n", addr.String(), ip)
						continue
					}

					if ip != "" {
						publicAddrCount++
					}

					// 添加目标地址到peerstore
					hpc.host.Peerstore().AddAddr(targetPeerID, addr, time.Hour)
					fmt.Printf("[打洞处理] 添加目标地址 #%d: %s\n", i+1, addr.String())
					addedCount++
				}

				fmt.Printf("[打洞处理] 成功添加 %d 个目标地址（其中公网地址: %d 个）\n",
					addedCount, publicAddrCount)
			}
		}
	}

	// 解析目标ID
	targetPeerID, err := peer.Decode(message.TargetID)
	if err != nil {
		fmt.Printf("[打洞处理] 解析目标peer ID失败: %v\n", err)
		return
	}

	// 通知目标peer有连接请求
	hpc.notifyPeer(targetPeerID, remotePeer)

	// 响应请求，包含自己的NAT类型
	hpc.mu.RLock()
	response := struct {
		Status    string   `json:"status"`
		NATType   NATType  `json:"nat_type"`
		SelfAddrs []string `json:"self_addrs,omitempty"`
	}{
		Status:  "ok",
		NATType: hpc.natType,
	}

	// 添加自己的地址信息到响应
	for _, addr := range hpc.host.Addrs() {
		response.SelfAddrs = append(response.SelfAddrs, addr.String())
	}
	hpc.mu.RUnlock()

	// 序列化并发送响应
	respBytes, err := json.Marshal(response)
	if err != nil {
		fmt.Printf("[打洞处理] 序列化响应失败: %v\n", err)
		s.Write([]byte("ok")) // 退化为简单响应
		return
	}

	s.Write(respBytes)
	fmt.Printf("[打洞处理] 已发送响应，包含 %d 个地址信息\n", len(response.SelfAddrs))
}

// notifyPeer 通知目标peer有连接请求
func (hpc *HolePunchCoordinator) notifyPeer(targetPeerID, requesterPeerID peer.ID) {
	hpc.mu.RLock()
	ch, exists := hpc.peerRequests[targetPeerID]
	hpc.mu.RUnlock()

	if exists {
		select {
		case ch <- requesterPeerID:
			fmt.Printf("[打洞通知] 成功通知目标节点 %s 有来自 %s 的打洞请求\n",
				targetPeerID.String(), requesterPeerID.String())
		default:
			fmt.Printf("[打洞通知] 通知通道已满或关闭，无法通知 %s\n", targetPeerID.String())
		}
	} else {
		fmt.Printf("[打洞通知] 目标节点 %s 未注册接收通知，缓存请求\n", targetPeerID.String())

		// 缓存请求，等待目标节点注册
		hpc.cacheMu.Lock()
		defer hpc.cacheMu.Unlock()

		// 检查是否已有该目标节点的缓存
		hpc.cachedReqs[targetPeerID] = append(hpc.cachedReqs[targetPeerID], requesterPeerID)
		fmt.Printf("[打洞通知] 已缓存针对节点 %s 的请求，当前缓存请求数: %d\n",
			targetPeerID.String(), len(hpc.cachedReqs[targetPeerID]))
	}
}

// RegisterForRequests 注册以接收连接请求通知
func (hpc *HolePunchCoordinator) RegisterForRequests(peerID peer.ID) (<-chan peer.ID, func()) {
	ch := make(chan peer.ID, 10)

	hpc.mu.Lock()
	hpc.peerRequests[peerID] = ch
	hpc.mu.Unlock()

	fmt.Printf("[打洞注册] 节点 %s 已注册接收打洞请求\n", peerID.String())

	// 检查是否有该节点的缓存请求
	hpc.cacheMu.Lock()
	cachedRequests, hasCached := hpc.cachedReqs[peerID]
	if hasCached {
		fmt.Printf("[打洞注册] 发现节点 %s 的 %d 个缓存请求，开始处理\n",
			peerID.String(), len(cachedRequests))

		// 将缓存的请求发送到通道
		for _, requesterID := range cachedRequests {
			select {
			case ch <- requesterID:
				fmt.Printf("[打洞注册] 已将来自 %s 的缓存请求通知给 %s\n",
					requesterID.String(), peerID.String())
			default:
				fmt.Printf("[打洞注册] 通道已满，无法发送缓存请求\n")
			}
		}

		// 处理完毕后清除缓存
		delete(hpc.cachedReqs, peerID)
	}
	hpc.cacheMu.Unlock()

	cleanup := func() {
		hpc.mu.Lock()
		delete(hpc.peerRequests, peerID)
		hpc.mu.Unlock()
		close(ch)
		fmt.Printf("[打洞注册] 节点 %s 已取消注册\n", peerID.String())
	}

	return ch, cleanup
}

// findBestRelayPeer 找到最佳的中继节点
func (hpc *HolePunchCoordinator) findBestRelayPeer() peer.ID {
	// 优先选择Agent节点作为中继
	connectedPeers := hpc.host.Network().Peers()

	// 先检查是否有Agent节点
	for _, pid := range connectedPeers {
		// 检查是否是Agent类型
		// 这里需要和系统的节点类型管理集成
		// 如果没有直接方法检查类型，可以检查连接质量作为替代
		conns := hpc.host.Network().ConnsToPeer(pid)
		if len(conns) > 0 {
			// 先假设第一个连接的节点是Agent
			// 在真实系统中，应该更准确地识别Agent节点
			return pid
		}
	}

	// 如果没有找到合适的中继，返回空
	return ""
}

// RequestHolePunch 请求与指定peer进行打洞
func (hpc *HolePunchCoordinator) RequestHolePunch(ctx context.Context, targetPeerID peer.ID) error {
	// 检查是否已连接
	if hpc.host.Network().Connectedness(targetPeerID) == network.Connected {
		fmt.Printf("[打洞] 已与目标节点 %s 连接，无需打洞\n", targetPeerID.String())
		return nil // 已连接，无需打洞
	}

	fmt.Printf("[打洞] 开始向节点 %s 发起打洞请求\n", targetPeerID.String())

	// 检查peerstore中是否有目标节点的地址
	targetAddrs := hpc.host.Peerstore().Addrs(targetPeerID)
	if len(targetAddrs) > 0 {
		fmt.Printf("[打洞] 目标节点 %s 在peerstore中有 %d 个地址\n",
			targetPeerID.String(), len(targetAddrs))
		for i, addr := range targetAddrs {
			fmt.Printf("[打洞] 地址 #%d: %s\n", i+1, addr.String())
		}
	} else {
		fmt.Printf("[打洞] 警告: 目标节点 %s 在peerstore中没有地址，打洞可能失败\n",
			targetPeerID.String())
	}

	// 查找中继节点（这里假设Agent作为中继节点）
	var relayPeerID peer.ID
	for _, conn := range hpc.host.Network().Conns() {
		// 简单粗暴地选择第一个连接的peer作为中继
		// 在实际应用中，应该有更复杂的逻辑来确定Agent节点
		relayPeerID = conn.RemotePeer()
		break
	}

	if relayPeerID == "" {
		return fmt.Errorf("未找到可用的中继节点")
	}

	fmt.Printf("[打洞] 使用中继节点: %s\n", relayPeerID.String())

	// 通过中继节点发送打洞请求
	s, err := hpc.host.NewStream(ctx, relayPeerID, common.HolePunchingProtocolID)
	if err != nil {
		return fmt.Errorf("创建打洞流失败: %w", err)
	}
	defer s.Close()

	// 构造增强的打洞请求，包含目标地址信息
	type HolePunchRequest struct {
		TargetID    string   `json:"target_id"`
		NATType     NATType  `json:"nat_type"`
		SelfAddrs   []string `json:"self_addrs,omitempty"`   // 发送方自己的地址
		TargetAddrs []string `json:"target_addrs,omitempty"` // 目标节点的已知地址
	}

	// 收集自己的地址
	selfAddrs := make([]string, 0, len(hpc.host.Addrs()))
	for _, addr := range hpc.host.Addrs() {
		selfAddrs = append(selfAddrs, addr.String())
	}

	// 收集目标节点的地址
	targetAddrStrings := make([]string, 0, len(targetAddrs))
	for _, addr := range targetAddrs {
		targetAddrStrings = append(targetAddrStrings, addr.String())
	}

	// 构造请求
	hpc.mu.RLock()
	request := HolePunchRequest{
		TargetID:    targetPeerID.String(),
		NATType:     hpc.natType,
		SelfAddrs:   selfAddrs,
		TargetAddrs: targetAddrStrings,
	}
	hpc.mu.RUnlock()

	// 序列化请求
	requestData, err := json.Marshal(request)
	if err != nil {
		fmt.Printf("[打洞] 序列化请求失败: %v，使用简单格式\n", err)
		// 降级为简单请求格式
		_, err = s.Write([]byte(targetPeerID.String()))
	} else {
		fmt.Printf("[打洞] 发送增强的打洞请求，包含 %d 个自身地址和 %d 个目标地址\n",
			len(selfAddrs), len(targetAddrStrings))
		_, err = s.Write(requestData)
	}

	if err != nil {
		return fmt.Errorf("发送打洞请求失败: %w", err)
	}

	// 等待响应
	buf := make([]byte, 1024) // 增加缓冲区大小以接收JSON响应
	n, err := s.Read(buf)
	if err != nil && err != io.EOF {
		return fmt.Errorf("读取打洞响应失败: %w", err)
	}

	responseData := buf[:n]
	fmt.Printf("[打洞] 收到响应: %s\n", string(responseData))

	// 尝试解析JSON响应
	var jsonResp struct {
		Status  string  `json:"status"`
		NATType NATType `json:"nat_type"`
	}

	if err := json.Unmarshal(responseData, &jsonResp); err == nil {
		// JSON解析成功
		if jsonResp.Status == "ok" {
			fmt.Printf("[打洞] 节点 %s 接受了打洞请求，NAT类型: %v\n", relayPeerID.String(), jsonResp.NATType)
			// 保存对方的NAT类型信息
			hpc.mu.Lock()
			hpc.peerNATTypes[relayPeerID] = jsonResp.NATType
			hpc.mu.Unlock()
		} else {
			return fmt.Errorf("打洞请求被拒绝: %s", string(responseData))
		}
	} else {
		// 非JSON格式，检查是否是简单的"ok"
		response := string(responseData)
		if response != "ok" {
			return fmt.Errorf("打洞请求被拒绝: %s", response)
		}
	}

	// 创建较长超时的上下文
	connectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// 多次尝试连接
	var lastError error
	for i := 0; i < 3; i++ {
		fmt.Printf("[打洞] 尝试 %d/3 连接到节点 %s\n", i+1, targetPeerID.String())

		// 由于libp2p内部已经启用了打洞功能，这里只需尝试连接
		err = hpc.host.Connect(connectCtx, peer.AddrInfo{ID: targetPeerID})
		if err == nil {
			fmt.Printf("[打洞] 成功连接到目标节点: %s (尝试 %d/3)\n", targetPeerID.String(), i+1)
			return nil
		}

		lastError = err
		fmt.Printf("[打洞] 连接尝试 %d/3 失败: %v\n", i+1, err)

		// 等待一小段时间再次尝试
		select {
		case <-connectCtx.Done():
			return fmt.Errorf("打洞连接超时: %w", connectCtx.Err())
		case <-time.After(2 * time.Second):
			// 继续尝试
		}
	}

	return fmt.Errorf("打洞连接失败: %w", lastError)
}

// selectHolePunchStrategy 根据NAT类型选择合适的打洞策略
func (hpc *HolePunchCoordinator) selectHolePunchStrategy(targetPeerID peer.ID) string {
	hpc.mu.RLock()
	localNATType := hpc.natType
	remoteNATType, hasRemoteInfo := hpc.peerNATTypes[targetPeerID]
	hpc.mu.RUnlock()

	if !hasRemoteInfo {
		remoteNATType = NATPortRestricted // 假设对方是端口限制型NAT
	}

	// 根据双方的NAT类型选择策略
	if localNATType == NATOpen || remoteNATType == NATOpen {
		return "直接连接" // 一方是开放的，可以直接连接
	}

	if localNATType == NATSymmetric && remoteNATType == NATSymmetric {
		return "中继连接" // 双方都是对称型NAT，很难打洞，使用中继
	}

	// 默认使用标准打洞策略
	return "标准打洞"
}

// ListenForHolePunchRequests 监听打洞请求并自动尝试建立连接
func (hpc *HolePunchCoordinator) ListenForHolePunchRequests(ctx context.Context) {
	// 注册接收连接请求
	selfID := hpc.host.ID()
	requestCh, cleanup := hpc.RegisterForRequests(selfID)
	defer cleanup()

	fmt.Printf("[打洞监听] 开始监听打洞请求，节点ID: %s\n", selfID.String())

	for {
		select {
		case <-ctx.Done():
			fmt.Println("[打洞监听] 停止监听打洞请求")
			return
		case remotePeerID, ok := <-requestCh:
			if !ok {
				fmt.Println("[打洞监听] 请求通道已关闭")
				return
			}

			// 收到打洞请求，尝试连接
			go func(pid peer.ID) {
				fmt.Printf("[打洞监听] 收到与节点 %s 的打洞请求，尝试连接\n", pid.String())

				// 创建较长超时的上下文
				connectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()

				// 检查是否已有该节点的地址信息
				addrs := hpc.host.Peerstore().Addrs(pid)
				if len(addrs) == 0 {
					fmt.Printf("[打洞监听] 警告: 节点 %s 在peerstore中没有地址信息，尝试从连接中获取\n", pid.String())

					// 检查现有连接中是否有该peer的相关信息
					for _, conn := range hpc.host.Network().Conns() {
						remotePeer := conn.RemotePeer()
						if remotePeer == pid {
							remoteAddr := conn.RemoteMultiaddr()

							// 提取IP地址并检查是否为公网IP
							var ip string
							ma := remoteAddr
							if val, err := ma.ValueForProtocol(multiaddr.P_IP4); err == nil {
								ip = val
							} else if val, err := ma.ValueForProtocol(multiaddr.P_IP6); err == nil {
								ip = val
							}

							if ip != "" && !isPublicIP(ip) {
								fmt.Printf("[打洞监听] 跳过内网地址: %s (IP: %s)\n", remoteAddr.String(), ip)
								continue
							}

							fmt.Printf("[打洞监听] 从连接中获取地址: %s\n", remoteAddr.String())
							hpc.host.Peerstore().AddAddr(pid, remoteAddr, time.Hour)
						}
					}

					// 再次检查是否有地址
					addrs = hpc.host.Peerstore().Addrs(pid)
					if len(addrs) == 0 {
						fmt.Printf("[打洞监听] 无法获取节点 %s 的地址信息，打洞可能失败\n", pid.String())
					} else {
						fmt.Printf("[打洞监听] 成功获取节点 %s 的 %d 个地址\n", pid.String(), len(addrs))
						for i, addr := range addrs {
							fmt.Printf("[打洞监听] 地址 #%d: %s\n", i+1, addr.String())
						}
					}
				} else {
					// 过滤掉内网地址
					filteredAddrs := make([]multiaddr.Multiaddr, 0)
					for _, addr := range addrs {
						// 提取IP地址
						var ip string
						ma := addr
						if val, err := ma.ValueForProtocol(multiaddr.P_IP4); err == nil {
							ip = val
						} else if val, err := ma.ValueForProtocol(multiaddr.P_IP6); err == nil {
							ip = val
						}

						// 检查是否为公网IP
						if ip != "" && !isPublicIP(ip) {
							fmt.Printf("[打洞监听] 忽略内网地址: %s (IP: %s)\n", addr.String(), ip)
							continue
						}

						filteredAddrs = append(filteredAddrs, addr)
					}

					if len(filteredAddrs) > 0 {
						fmt.Printf("[打洞监听] 节点 %s 有 %d 个公网地址（共 %d 个地址）\n",
							pid.String(), len(filteredAddrs), len(addrs))
						for i, addr := range filteredAddrs {
							fmt.Printf("[打洞监听] 公网地址 #%d: %s\n", i+1, addr.String())
						}
					} else {
						fmt.Printf("[打洞监听] 警告: 节点 %s 没有可用的公网地址\n", pid.String())
					}
				}

				// 多次尝试连接
				var lastError error
				for i := 0; i < 3; i++ {
					fmt.Printf("[打洞监听] 尝试 %d/3 连接到节点 %s\n", i+1, pid.String())

					err := hpc.host.Connect(connectCtx, peer.AddrInfo{ID: pid})
					if err == nil {
						fmt.Printf("[打洞监听] 成功与节点 %s 建立连接 (尝试 %d/3)\n", pid.String(), i+1)
						return
					}

					lastError = err
					fmt.Printf("[打洞监听] 连接尝试 %d/3 失败: %v\n", i+1, err)

					// 等待一小段时间再次尝试
					select {
					case <-connectCtx.Done():
						fmt.Printf("[打洞监听] 连接超时: %v\n", connectCtx.Err())
						return
					case <-time.After(2 * time.Second):
						// 继续尝试
					}
				}

				fmt.Printf("[打洞监听] 响应打洞请求失败，无法连接到节点 %s: %v\n",
					pid.String(), lastError)
			}(remotePeerID)
		}
	}
}

// sendConnectivityTestMessage 发送连接测试消息以确认连接有效
func (hpc *HolePunchCoordinator) sendConnectivityTestMessage(ctx context.Context, pid peer.ID) {
	// 使用现有协议来测试连接有效性
	// 为避免协议ID类型问题，直接使用已经注册的协议
	s, err := hpc.host.NewStream(ctx, pid, common.HolePunchingProtocolID)
	if err != nil {
		fmt.Printf("[连接测试] 创建测试流失败: %v\n", err)
		return
	}
	defer s.Close()

	// 发送测试消息
	_, err = s.Write([]byte("connectivity_test"))
	if err != nil {
		fmt.Printf("[连接测试] 发送测试消息失败: %v\n", err)
		return
	}

	fmt.Printf("[连接测试] 成功向节点 %s 发送测试消息\n", pid.String())
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
