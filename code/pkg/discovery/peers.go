package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"shiledp2p/pkg/common"
)

// PeerDiscovery 管理节点发现
type PeerDiscovery struct {
	host       host.Host
	mu         sync.RWMutex
	peers      map[peer.ID]*common.PeerInfo
	nodeType   common.NodeType
	ctx        context.Context
	cancel     context.CancelFunc
	notifyChan chan struct{}
}

// NewPeerDiscovery 创建新的节点发现服务
func NewPeerDiscovery(h host.Host, nodeType common.NodeType) *PeerDiscovery {
	ctx, cancel := context.WithCancel(context.Background())
	return &PeerDiscovery{
		host:       h,
		peers:      make(map[peer.ID]*common.PeerInfo),
		nodeType:   nodeType,
		ctx:        ctx,
		cancel:     cancel,
		notifyChan: make(chan struct{}, 1),
	}
}

// Start 启动节点发现服务
func (pd *PeerDiscovery) Start() {
	// 注册发现协议处理程序 - 使用统一的处理方式注册
	common.RegisterStreamHandler(pd.host, common.PeerDiscoveryProtocol, pd.handleDiscovery)

	fmt.Printf("[发现服务] 已注册发现协议处理器: %s\n", common.PeerDiscoveryProtocol)
	fmt.Printf("[发现服务] 协议字节表示: %v\n", []byte(common.PeerDiscoveryProtocol))
	fmt.Printf("[发现服务] 协议字符串: %s\n", string(common.PeerDiscoveryProtocol))

	// 开始周期性通知
	go pd.periodicNotify()
}

// Stop 停止节点发现服务
func (pd *PeerDiscovery) Stop() {
	pd.cancel()
}

// AddPeer 添加对等节点
func (pd *PeerDiscovery) AddPeer(peerID peer.ID, multiaddrs []string, nodeType common.NodeType) {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	pd.peers[peerID] = &common.PeerInfo{
		PeerID:     peerID,
		Multiaddrs: multiaddrs,
		NodeType:   nodeType,
	}

	// 触发通知
	select {
	case pd.notifyChan <- struct{}{}:
	default:
	}
}

// RemovePeer 移除对等节点
func (pd *PeerDiscovery) RemovePeer(peerID peer.ID) {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	delete(pd.peers, peerID)

	// 触发通知
	select {
	case pd.notifyChan <- struct{}{}:
	default:
	}
}

// GetPeers 获取所有对等节点
func (pd *PeerDiscovery) GetPeers() []*common.PeerInfo {
	pd.mu.RLock()
	defer pd.mu.RUnlock()

	peers := make([]*common.PeerInfo, 0, len(pd.peers))
	for _, p := range pd.peers {
		peers = append(peers, p)
	}
	return peers
}

// GetPeersByType 获取指定类型的对等节点
func (pd *PeerDiscovery) GetPeersByType(nodeType common.NodeType) []*common.PeerInfo {
	pd.mu.RLock()
	defer pd.mu.RUnlock()

	peers := make([]*common.PeerInfo, 0)
	for _, p := range pd.peers {
		if p.NodeType == nodeType {
			peers = append(peers, p)
		}
	}
	return peers
}

// handleDiscovery 处理发现协议请求
func (pd *PeerDiscovery) handleDiscovery(s network.Stream) {
	defer s.Close()

	remotePeer := s.Conn().RemotePeer()
	fmt.Printf("[发现协议] 收到来自 %s 的请求\n", remotePeer.String())

	// 改进请求读取逻辑，更好地处理网络问题
	var requestData []byte
	buf := make([]byte, 4096)

	// 设置读取超时
	s.SetReadDeadline(time.Now().Add(5 * time.Second))

	// 多次读取直到EOF或错误
	totalRead := 0
	for {
		n, err := s.Read(buf)
		if n > 0 {
			totalRead += n
			requestData = append(requestData, buf[:n]...)
			// 如果已经读取了一些数据，给更多时间读取剩余部分
			s.SetReadDeadline(time.Now().Add(3 * time.Second))
		}

		if err != nil {
			if err == io.EOF {
				// EOF是正常的结束标志
				fmt.Printf("[发现协议] 读取请求完成(EOF)，共读取 %d 字节\n", totalRead)
				break
			} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// 超时，如果已读取数据则处理，否则报错
				fmt.Printf("[发现协议] 读取请求超时，已读取 %d 字节\n", totalRead)
				if totalRead > 0 {
					break // 已有数据，继续处理
				} else {
					fmt.Printf("[发现协议] 读取请求超时且无数据\n")
					return // 无数据，结束处理
				}
			} else {
				// 其他错误
				fmt.Printf("[发现协议] 读取请求失败: %v\n", err)
				if totalRead > 0 {
					fmt.Printf("[发现协议] 尽管有错误，但将处理已读取的 %d 字节数据\n", totalRead)
					break
				} else {
					return // 无数据且出错，结束处理
				}
			}
		}

		// 没有更多数据但也没有错误，继续读取
		if n == 0 {
			continue
		}
	}

	// 重置读取超时
	s.SetReadDeadline(time.Time{})

	// 打印收到的数据
	if totalRead > 0 {
		fmt.Printf("[发现协议] 收到数据(%d字节): %s\n", totalRead, string(requestData))

		// 尝试解析标准请求格式
		var request common.StandardRequest
		if err := json.Unmarshal(requestData, &request); err != nil {
			fmt.Printf("[发现协议] 解析数据失败: %v，使用默认处理\n", err)
			// 默认为注册请求
			request.Action = "register"
			request.NodeType = common.TypeUnknown
			request.From = remotePeer.String()
		}

		fmt.Printf("[发现协议] 请求类型: Action=%s, NodeType=%v\n", request.Action, request.NodeType)

		// 处理不同类型的请求
		if request.Action == "register" {
			// 当收到注册请求时，根据发送方角色进行处理
			if pd.nodeType == common.TypeMaster {
				// Master收到Agent注册
				fmt.Printf("[发现协议] Master收到注册请求，回应成功\n")

				// 将远程节点添加到发现服务
				addrs := pd.host.Peerstore().Addrs(remotePeer)
				addrStrings := common.GetMultiaddrsString(addrs)
				pd.AddPeer(remotePeer, addrStrings, request.NodeType)

				// 发送成功响应
				response := common.NewStandardResponse(
					"register",
					"success",
					"注册成功",
					pd.nodeType,
					pd.host.ID().String(),
				)

				respBytes, _ := common.JSONMarshalWithNewline(response)
				sendResponse(s, respBytes)
			}
			return
		} else if request.Action == "ping" {
			// 处理ping请求
			fmt.Printf("[发现协议] 收到ping请求，立即响应\n")

			// 构造响应
			response := common.NewHeartbeatMessage(
				"pong",
				pd.nodeType,
				pd.host.ID().String(),
			)
			response.Status = "ok"

			// 发送响应
			respBytes, _ := common.JSONMarshalWithNewline(response)
			sendResponse(s, respBytes)

			// 如果是Agent收到Master的ping，主动注册
			if pd.nodeType == common.TypeAgent && request.From == pd.host.ID().String() {
				fmt.Printf("[发现协议] Master主动ping，Agent将主动注册\n")

				go func() {
					time.Sleep(500 * time.Millisecond)
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()

					fmt.Printf("[发现协议] Agent主动向Master注册...\n")
					pd.SendPeerInfo(ctx, remotePeer)
				}()
			}
			return
		} else if request.Action == "query" {
			// 处理查询请求
			fmt.Printf("[发现协议] 收到查询请求\n")

			// 打印当前已知节点信息
			pd.mu.RLock()
			fmt.Printf("[发现协议] 当前已知节点数量: %d\n", len(pd.peers))
			for id, p := range pd.peers {
				fmt.Printf("[发现协议] 已知节点: %s (类型: %v)\n", id.String(), p.NodeType)
			}
			pd.mu.RUnlock()

			peers := pd.GetPeers()

			// 排除自己和请求方
			filteredPeers := make([]*common.PeerInfo, 0, len(peers))
			for _, p := range peers {
				if p.PeerID != remotePeer && p.PeerID != pd.host.ID() {
					filteredPeers = append(filteredPeers, p)
				}
			}

			fmt.Printf("[发现协议] 将返回 %d 个节点信息给 %s\n", len(filteredPeers), remotePeer.String())
			if len(filteredPeers) > 0 {
				for i, p := range filteredPeers {
					fmt.Printf("[发现协议] 返回节点 #%d: %s (类型: %v)\n", i+1, p.PeerID.String(), p.NodeType)
				}
			} else {
				fmt.Printf("[发现协议] 没有其他节点信息可返回\n")
			}

			// 构造标准响应，但包含peers字段
			response := struct {
				Status    string             `json:"status"`
				Message   string             `json:"message,omitempty"`
				NodeType  common.NodeType    `json:"node_type"`
				From      string             `json:"from"`
				Timestamp int64              `json:"timestamp"`
				Action    string             `json:"action"`
				Peers     []*common.PeerInfo `json:"peers"`
			}{
				Status:    "success",
				Message:   "查询成功",
				NodeType:  pd.nodeType,
				From:      pd.host.ID().String(),
				Timestamp: time.Now().UnixNano(),
				Action:    "query",
				Peers:     filteredPeers,
			}

			respBytes, _ := common.JSONMarshalWithNewline(response)
			success := sendResponse(s, respBytes)
			if success {
				fmt.Printf("[发现协议] 成功发送查询响应\n")
			} else {
				fmt.Printf("[发现协议] 发送查询响应失败\n")
			}
			return
		}
	} else {
		fmt.Printf("[发现协议] 未收到任何数据，仅连接\n")
	}

	// 如果什么都没处理，默认发送一个成功响应
	fmt.Printf("[发现协议] 发送默认成功响应\n")
	response := common.NewStandardResponse(
		"unknown",
		"success",
		"请求已接收",
		pd.nodeType,
		pd.host.ID().String(),
	)

	respBytes, _ := common.JSONMarshalWithNewline(response)
	sendResponse(s, respBytes)
}

// sendResponse 可靠地发送响应数据
func sendResponse(s network.Stream, data []byte) bool {
	// 设置写入超时
	s.SetWriteDeadline(time.Now().Add(5 * time.Second))

	// 发送数据
	totalSent := 0
	dataLen := len(data)

	for totalSent < dataLen {
		n, err := s.Write(data[totalSent:])
		if n > 0 {
			totalSent += n
			fmt.Printf("[发送响应] 已发送 %d/%d 字节\n", totalSent, dataLen)
		}

		if err != nil {
			fmt.Printf("[发送响应] 发送失败: %v\n", err)
			s.SetWriteDeadline(time.Time{}) // 重置超时
			return false
		}

		// 如果没有写入全部数据，继续写入
		if totalSent < dataLen {
			// 重置超时
			s.SetWriteDeadline(time.Now().Add(3 * time.Second))
		}
	}

	// 重置写入超时
	s.SetWriteDeadline(time.Time{})

	// network.Stream没有Flush方法，但可以尝试关闭写入端
	// 这里我们不关闭写入端，因为可能会影响后续通信
	// 如果实现了相关接口，可以尝试刷新缓冲区
	if flusher, ok := s.(interface{ Flush() error }); ok {
		err := flusher.Flush()
		if err != nil {
			fmt.Printf("[发送响应] 刷新缓冲区失败: %v\n", err)
			return false
		}
	}

	return true
}

// SendPeerInfo 向指定节点发送自身信息
func (pd *PeerDiscovery) SendPeerInfo(ctx context.Context, targetPeerID peer.ID) error {
	// 显示目标协议
	fmt.Printf("[发送注册] 目标节点: %s\n", targetPeerID.String())
	fmt.Printf("[发送注册] 使用协议: %s\n", common.PeerDiscoveryProtocol)

	// 创建流 - 确保使用正确的协议标识符
	fmt.Printf("[发送注册] 尝试建立流连接...\n")
	s, err := pd.host.NewStream(ctx, targetPeerID, common.PeerDiscoveryProtocol)
	if err != nil {
		return fmt.Errorf("创建到节点的流失败 [%s]: %w", targetPeerID.String(), err)
	}
	defer s.Close()

	fmt.Printf("[发送注册] 成功创建流，使用的协议: %s\n", s.Protocol())
	fmt.Printf("[发送注册] 远程地址: %s\n", s.Conn().RemoteMultiaddr().String())

	// 获取本节点的所有地址
	addrs := pd.host.Addrs()
	addrStrings := common.GetMultiaddrsString(addrs)

	// 使用标准请求结构
	request := common.NewStandardRequest("register", pd.nodeType, pd.host.ID().String())
	request.Addrs = addrStrings

	// 使用统一的JSON序列化方法
	jsonBytes, err := common.JSONMarshalWithNewline(request)
	if err != nil {
		return fmt.Errorf("序列化注册请求失败: %w", err)
	}

	fmt.Printf("[发送注册] 发送注册请求: %s\n", string(jsonBytes))

	_, err = s.Write(jsonBytes)
	if err != nil {
		return fmt.Errorf("发送注册请求失败: %w", err)
	}

	fmt.Printf("[发送注册] 注册请求已发送\n")

	// 读取响应
	buf := make([]byte, 4096)
	n, err := s.Read(buf)
	if err != nil && err != io.EOF {
		fmt.Printf("[发送注册] 读取响应失败: %v\n", err)
	} else if n > 0 {
		fmt.Printf("[发送注册] 收到响应: %s\n", string(buf[:n]))

		// 尝试解析响应
		var response common.StandardResponse
		if err := json.Unmarshal(buf[:n], &response); err == nil {
			fmt.Printf("[发送注册] 解析响应: 状态=%s, 消息=%s\n",
				response.Status, response.Message)
		}
	}

	fmt.Printf("[发送注册] 注册完成\n")
	return nil
}

// validateRegistrationWithMaster 验证与Master的注册状态
func (pd *PeerDiscovery) validateRegistrationWithMaster(ctx context.Context, masterPeerID peer.ID) bool {
	fmt.Println("进行Master注册状态验证...")

	// 1. 检查连接是否存在
	if pd.host.Network().Connectedness(masterPeerID) != network.Connected {
		fmt.Println("❌ 验证失败: 与Master的连接已断开")
		return false
	}

	// 2. 尝试心跳检查
	heartbeat := common.NewHeartbeatService(pd.host, 5*time.Second, 15*time.Second)
	validateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := heartbeat.SendHeartbeat(validateCtx, masterPeerID)
	if err != nil {
		fmt.Printf("❌ 心跳检查失败: %v\n", err)
		return false
	}

	fmt.Println("✓ 心跳检查通过: 与Master连接正常")

	// 3. 等待一段时间后再次发送心跳，确保连接稳定
	time.Sleep(2 * time.Second)

	validateCtx2, cancel2 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel2()

	err = heartbeat.SendHeartbeat(validateCtx2, masterPeerID)
	if err != nil {
		fmt.Printf("❌ 第二次心跳检查失败: %v\n", err)
		return false
	}

	fmt.Println("✓ 双重心跳检查通过: 注册可能已成功")
	return true
}

// QueryPeers 查询对等节点
func (pd *PeerDiscovery) QueryPeers(ctx context.Context, targetPeerID peer.ID) ([]*common.PeerInfo, error) {
	fmt.Printf("[查询节点] 开始向节点 %s 发送查询请求\n", targetPeerID.String())

	// 创建流
	s, err := pd.host.NewStream(ctx, targetPeerID, common.PeerDiscoveryProtocol)
	if err != nil {
		return nil, fmt.Errorf("创建到节点的流失败 [%s]: %w", targetPeerID.String(), err)
	}
	defer s.Close()

	fmt.Printf("[查询节点] 成功创建流连接，远程地址: %s\n", s.Conn().RemoteMultiaddr().String())

	// 发送查询请求
	request := common.NewStandardRequest("query", pd.nodeType, pd.host.ID().String())

	// 序列化并发送
	requestBytes, err := common.JSONMarshalWithNewline(request)
	if err != nil {
		return nil, fmt.Errorf("序列化查询请求失败: %w", err)
	}

	fmt.Printf("[查询节点] 发送查询请求: %s\n", string(requestBytes))

	n, err := s.Write(requestBytes)
	if err != nil {
		return nil, fmt.Errorf("发送查询请求失败: %w", err)
	}
	fmt.Printf("[查询节点] 已发送 %d 字节请求数据\n", n)

	// 读取响应 - 使用更复杂的读取逻辑来处理网络问题
	var responseData []byte
	buf := make([]byte, 4096) // 使用更小的缓冲区，但多次读取
	fmt.Printf("[查询节点] 等待节点 %s 的响应...\n", targetPeerID.String())

	// 设置初始读取超时
	s.SetReadDeadline(time.Now().Add(10 * time.Second))

	// 多次读取直到EOF或错误
	totalBytes := 0
	for {
		n, err := s.Read(buf)
		if n > 0 {
			totalBytes += n
			responseData = append(responseData, buf[:n]...)
			fmt.Printf("[查询节点] 已读取 %d 字节数据（累计: %d 字节）\n", n, totalBytes)

			// 重置读取超时，给更多时间接收剩余数据
			s.SetReadDeadline(time.Now().Add(5 * time.Second))
		}

		if err != nil {
			if err == io.EOF {
				fmt.Printf("[查询节点] 读取完成(EOF)，累计读取 %d 字节\n", totalBytes)
				break // 正常结束读取
			} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				fmt.Printf("[查询节点] 读取超时，已读取 %d 字节\n", totalBytes)
				// 如果已经读取了一些数据，我们继续处理
				if totalBytes > 0 {
					break
				}
				// 如果完全没有数据，返回错误
				return nil, fmt.Errorf("读取响应超时，未收到任何数据")
			} else {
				// 其他错误，但如果已读取数据，尝试继续处理
				if totalBytes > 0 {
					fmt.Printf("[查询节点] 读取时发生错误: %v，但已读取 %d 字节，将尝试处理\n", err, totalBytes)
					break
				}
				return nil, fmt.Errorf("读取响应失败: %w", err)
			}
		}

		// 如果本次没有读取到数据但没有错误，可能需要继续等待
		if n == 0 {
			fmt.Printf("[查询节点] 本次读取0字节，继续等待...\n")
		}
	}

	// 重置读取超时
	s.SetReadDeadline(time.Time{})

	// 检查是否有收到数据
	if totalBytes == 0 {
		return nil, fmt.Errorf("收到空响应")
	}

	// 处理接收到的数据
	fmt.Printf("[查询节点] 完成数据接收，总共 %d 字节\n", totalBytes)
	if totalBytes < 200 {
		fmt.Printf("[查询节点] 响应内容: %s\n", string(responseData))
	} else {
		fmt.Printf("[查询节点] 响应内容(前200字节): %s...\n", string(responseData[:200]))
	}

	// 解析响应
	var response struct {
		Status string             `json:"status"`
		Action string             `json:"action"`
		Peers  []*common.PeerInfo `json:"peers"`
	}

	if err := json.Unmarshal(responseData, &response); err != nil {
		return nil, fmt.Errorf("解析查询响应失败: %w", err)
	}

	if response.Status != "success" {
		return nil, fmt.Errorf("查询失败: %s", response.Status)
	}

	fmt.Printf("[查询节点] 成功解析响应，获取到 %d 个节点信息\n", len(response.Peers))
	for i, p := range response.Peers {
		fmt.Printf("[查询节点] 发现节点 #%d: %s (类型: %v)\n", i+1, p.PeerID.String(), p.NodeType)
	}

	return response.Peers, nil
}

// periodicNotify 周期性地通知所有已连接的节点
func (pd *PeerDiscovery) periodicNotify() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pd.ctx.Done():
			return
		case <-ticker.C:
			pd.notifyAllPeers()
		case <-pd.notifyChan:
			// 当有节点变化时立即通知
			pd.notifyAllPeers()
			// 防止频繁通知，重置定时器
			ticker.Reset(30 * time.Second)
		}
	}
}

// notifyAllPeers 通知所有已连接的节点
func (pd *PeerDiscovery) notifyAllPeers() {
	// 只有Agent和Master才发送通知
	if pd.nodeType == common.TypeClient {
		return
	}

	// 获取所有连接的对等节点
	connectedPeers := pd.host.Network().Peers()
	for _, peerID := range connectedPeers {
		// 对于每个已连接的节点，发送当前已知的节点信息
		go func(pid peer.ID) {
			ctx, cancel := context.WithTimeout(pd.ctx, 5*time.Second)
			defer cancel()

			// 获取要发送给这个节点的节点信息（排除自己和目标节点）
			pd.mu.RLock()
			peersToSend := make([]*common.PeerInfo, 0, len(pd.peers))
			for _, p := range pd.peers {
				// Agent不向Master发送Client信息
				if pd.nodeType == common.TypeAgent && pid == getMasterPeerID() && p.NodeType == common.TypeClient {
					continue
				}

				if p.PeerID != pid && p.PeerID != pd.host.ID() {
					peersToSend = append(peersToSend, p)
				}
			}
			pd.mu.RUnlock()

			// 创建到节点的流
			if len(peersToSend) > 0 {
				s, err := pd.host.NewStream(ctx, pid, common.PeerDiscoveryProtocol)
				if err != nil {
					return
				}
				defer s.Close()

				// 发送节点信息 - 使用标准格式
				updateMsg := struct {
					Action    string             `json:"action"`
					NodeType  common.NodeType    `json:"node_type"`
					From      string             `json:"from"`
					Timestamp int64              `json:"timestamp"`
					Peers     []*common.PeerInfo `json:"peers"`
				}{
					Action:    "update",
					NodeType:  pd.nodeType,
					From:      pd.host.ID().String(),
					Timestamp: time.Now().UnixNano(),
					Peers:     peersToSend,
				}

				// 序列化并发送
				updateBytes, err := common.JSONMarshalWithNewline(updateMsg)
				if err != nil {
					fmt.Printf("序列化更新消息失败: %v\n", err)
					return
				}

				s.Write(updateBytes)
			}
		}(peerID)
	}
}

// getMasterPeerID 获取Master节点的PeerID
// 这是一个辅助函数，用于在notifyAllPeers中判断是否是Master节点
func getMasterPeerID() peer.ID {
	masterID, err := GetMasterPeerID(context.Background())
	if err != nil {
		return ""
	}
	return masterID
}
