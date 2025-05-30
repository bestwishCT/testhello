package common

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
)

// HeartbeatService 心跳服务
type HeartbeatService struct {
	host              host.Host
	ctx               context.Context
	cancel            context.CancelFunc
	peerCheckInterval time.Duration
	failTimeout       time.Duration
	mu                sync.RWMutex
	peerLastSeen      map[peer.ID]time.Time
	peerFailCallbacks map[peer.ID]func()
	nodeType          NodeType // 节点类型
}

// NewHeartbeatService 创建新的心跳服务
func NewHeartbeatService(h host.Host, checkInterval, failTimeout time.Duration) *HeartbeatService {
	ctx, cancel := context.WithCancel(context.Background())
	return &HeartbeatService{
		host:              h,
		ctx:               ctx,
		cancel:            cancel,
		peerCheckInterval: checkInterval,
		failTimeout:       failTimeout,
		peerLastSeen:      make(map[peer.ID]time.Time),
		peerFailCallbacks: make(map[peer.ID]func()),
	}
}

// SetNodeType 设置节点类型
func (hs *HeartbeatService) SetNodeType(nodeType NodeType) {
	hs.nodeType = nodeType
	// fmt.Printf("[心跳服务] 设置节点类型: %v\n", nodeType)
}

// Start 启动心跳服务
func (hs *HeartbeatService) Start() {
	// 注册心跳处理程序 - 所有节点都需要响应心跳
	RegisterStreamHandler(hs.host, HeartbeatProtocolID, hs.handleHeartbeat)

	// 根据节点类型决定行为
	if hs.nodeType == TypeMaster {
		// fmt.Printf("[心跳服务] 以Master模式启动，仅响应心跳\n")
	} else {
		// fmt.Printf("[心跳服务] 以Agent/Client模式启动，将定期发送心跳\n")
		// 开始心跳检查循环 - 仅Agent/Client节点执行
		go hs.heartbeatLoop()
	}
}

// Stop 停止心跳服务
func (hs *HeartbeatService) Stop() {
	// 取消上下文
	hs.cancel()
}

// RegisterPeer 注册要监控的peer
func (hs *HeartbeatService) RegisterPeer(peerID peer.ID, failCallback func()) {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	hs.peerLastSeen[peerID] = time.Now()
	if failCallback != nil {
		hs.peerFailCallbacks[peerID] = failCallback
	}
}

// UnregisterPeer 取消注册peer
func (hs *HeartbeatService) UnregisterPeer(peerID peer.ID) {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	delete(hs.peerLastSeen, peerID)
	delete(hs.peerFailCallbacks, peerID)
}

// UpdatePeerTime 更新peer的最后活动时间
func (hs *HeartbeatService) UpdatePeerTime(peerID peer.ID) {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	hs.peerLastSeen[peerID] = time.Now()
}

// handleHeartbeat 处理心跳请求
func (hs *HeartbeatService) handleHeartbeat(s network.Stream) {
	remotePeer := s.Conn().RemotePeer()

	// 更新peer活动时间
	hs.UpdatePeerTime(remotePeer)

	// 读取心跳数据
	data := make([]byte, 1024)
	n, err := s.Read(data)

	// 即使是EOF，如果有数据也要处理
	if n > 0 {
		// fmt.Printf("[心跳处理] 收到来自 %s 的数据(%d字节): %s\n", remotePeer.String(), n, string(data[:n]))

		heartbeat, parseErr := ReadHeartbeatMessage(data[:n])
		if parseErr != nil {
			// fmt.Printf("[心跳处理] 解析心跳数据失败: %v\n", parseErr)
			s.Close() // 解析失败时关闭流
			return
		}

		// fmt.Printf("[心跳处理] 解析心跳消息: action=%s, from=%s\n", heartbeat.Action, heartbeat.From)

		// 验证是否是有效的ping请求
		if !IsValidHeartbeatPing(heartbeat) {
			// fmt.Printf("[心跳处理] 收到无效的心跳类型: %s\n", heartbeat.Action)
			s.Close() // 无效心跳时关闭流
			return
		}

		// 创建pong响应
		var nodeTypeStr string
		switch hs.nodeType {
		case TypeMaster:
			nodeTypeStr = "Master"
		case TypeAgent:
			nodeTypeStr = "Agent"
		case TypeClient:
			nodeTypeStr = "Client"
		default:
			nodeTypeStr = "Unknown"
		}
		response := CreateHeartbeatPong(nodeTypeStr, hs.host.ID().String())

		// 序列化并发送响应
		respBytes, respErr := WriteMessageToStream(response)
		if respErr != nil {
			// fmt.Printf("[心跳处理] 序列化心跳响应失败: %v\n", respErr)
			s.Close() // 序列化失败时关闭流
			return
		}

		// 设置写入超时
		s.SetWriteDeadline(time.Now().Add(5 * time.Second))

		_, writeErr := s.Write(respBytes)
		if writeErr != nil {
			// fmt.Printf("[心跳处理] 发送心跳响应失败: %v\n", writeErr)
			s.Close() // 发送失败时关闭流
			return
		} else {
			// 注释中不使用n变量
			// fmt.Printf("[心跳处理] 发送心跳响应: %s\n", string(respBytes))
		}

		// 清除写入超时
		s.SetWriteDeadline(time.Time{})

		// 成功发送响应后关闭流，不再复用
		s.Close()
		// fmt.Printf("[心跳处理] 心跳流已处理完毕并关闭\n")
		return
	}

	// 处理读取错误（没有数据的情况）
	if err != nil && err != io.EOF {
		// fmt.Printf("[心跳处理] 读取心跳数据失败: %v\n", err)
		s.Close() // 出错时关闭流
		return
	}

	// 没有数据的情况
	// fmt.Printf("[心跳处理] 收到来自 %s 的空数据\n", remotePeer.String())
	s.Close() // 空数据时关闭流
}

// heartbeatLoop 心跳检查循环
func (hs *HeartbeatService) heartbeatLoop() {
	// Master节点不需要主动发送心跳，只需响应心跳
	if hs.nodeType == TypeMaster {
		// fmt.Printf("[心跳服务] 作为Master节点运行，不执行心跳循环\n")
		return
	}

	// fmt.Printf("[心跳服务] 作为%s节点运行，开始心跳循环，间隔=%v，超时=%v\n",
	//	getNodeTypeString(hs.nodeType),
	//	hs.peerCheckInterval,
	//	hs.failTimeout)

	ticker := time.NewTicker(hs.peerCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-hs.ctx.Done():
			return
		case <-ticker.C:
			hs.checkPeers()
		}
	}
}

// getNodeTypeString 获取节点类型的字符串表示
func getNodeTypeString(nodeType NodeType) string {
	switch nodeType {
	case TypeMaster:
		return "Master"
	case TypeAgent:
		return "Agent"
	case TypeClient:
		return "Client"
	default:
		return "Unknown"
	}
}

// checkPeers 检查所有注册的peer
func (hs *HeartbeatService) checkPeers() {
	// Master节点不应主动发送心跳
	if hs.nodeType == TypeMaster {
		return
	}

	now := time.Now()

	hs.mu.RLock()
	peersToCheck := make([]peer.ID, 0, len(hs.peerLastSeen))
	for peerID := range hs.peerLastSeen {
		peersToCheck = append(peersToCheck, peerID)
	}
	hs.mu.RUnlock()

	// 添加检查数量日志
	if len(peersToCheck) > 0 {
		// fmt.Printf("[心跳检查] 将检查 %d 个节点的状态\n", len(peersToCheck))
	}

	for _, peerID := range peersToCheck {
		// 使用匿名函数而不是goroutine，避免同时发送过多心跳
		func(pid peer.ID) {
			hs.mu.RLock()
			lastSeen, exists := hs.peerLastSeen[pid]
			callback := hs.peerFailCallbacks[pid]
			hs.mu.RUnlock()

			if !exists {
				return
			}

			timeSinceLastSeen := now.Sub(lastSeen)
			// 检查最后活动时间是否超过超时时间
			if timeSinceLastSeen > hs.failTimeout {
				// fmt.Printf("[心跳检查] 节点 %s 已 %v 无活动，尝试发送心跳\n",
				//	pid.String(), timeSinceLastSeen.Round(time.Second))

				// 尝试发送一次心跳
				ctx, cancel := context.WithTimeout(hs.ctx, 5*time.Second)
				defer cancel()

				err := hs.SendHeartbeat(ctx, pid)
				if err != nil {
					// fmt.Printf("[心跳检查] 发送心跳失败: %v\n", err)
					// 心跳失败，调用回调函数
					if callback != nil {
						callback()
					}

					// 节点连接已断开，移除监控
					hs.UnregisterPeer(pid)
				}
			} else if timeSinceLastSeen > (hs.failTimeout / 2) {
				// 如果已经过了超时时间的一半，发送一次预防性心跳
				// fmt.Printf("[心跳检查] 节点 %s 已 %v 无活动（超过一半超时时间），发送预防性心跳\n",
				//	pid.String(), timeSinceLastSeen.Round(time.Second))

				ctx, cancel := context.WithTimeout(hs.ctx, 5*time.Second)
				defer cancel()

				// 忽略错误，只是预防性检查
				_ = hs.SendHeartbeat(ctx, pid)
			} else if hs.host.Network().Connectedness(pid) != network.Connected {
				// 连接已断开但还没有超时，尝试发送心跳
				// fmt.Printf("[心跳检查] 节点 %s 连接已断开但还未超时，尝试发送心跳\n", pid.String())

				ctx, cancel := context.WithTimeout(hs.ctx, 5*time.Second)
				defer cancel()

				err := hs.SendHeartbeat(ctx, pid)
				if err != nil {
					// fmt.Printf("[心跳检查] 发送心跳失败: %v\n", err)
					// 心跳失败，调用回调函数
					if callback != nil {
						callback()
					}

					// 节点连接已断开，移除监控
					hs.UnregisterPeer(pid)
				}
			}
		}(peerID)

		// 每个节点检查之间添加短暂延迟，避免同时发送大量心跳
		if len(peersToCheck) > 1 {
			time.Sleep(500 * time.Millisecond)
		}
	}
}

// UnmarshalJSON 是一个辅助函数，用于解析JSON数据
func UnmarshalJSON(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// GetNodeType 返回节点类型
func (hs *HeartbeatService) GetNodeType() NodeType {
	return hs.nodeType
}

// SendHeartbeat 向指定节点发送心跳
func (hs *HeartbeatService) SendHeartbeat(ctx context.Context, peerID peer.ID) error {
	// Master节点不应主动发送心跳
	if hs.nodeType == TypeMaster {
		return fmt.Errorf("Master节点不应主动发送心跳")
	}

	// 添加重试次数限制
	return hs.sendHeartbeatWithRetry(ctx, peerID, 0)
}

// sendHeartbeatWithRetry 带重试次数限制的心跳发送
func (hs *HeartbeatService) sendHeartbeatWithRetry(ctx context.Context, peerID peer.ID, retryCount int) error {
	// 如果重试次数超过限制，返回错误
	if retryCount >= 2 {
		return fmt.Errorf("发送心跳失败: 超过最大重试次数(2次)")
	}

	// fmt.Printf("[心跳] 开始发送心跳到: %s%s\n",
	//	peerID.String(),
	//	func() string {
	//		if retryCount > 0 {
	//			return fmt.Sprintf(" (重试 #%d)", retryCount)
	//		}
	//		return ""
	//	}())

	// 创建新流
	// fmt.Printf("[心跳] 创建新的心跳流到 %s\n", peerID.String())
	stream, err := hs.host.NewStream(ctx, peerID, HeartbeatProtocolID)
	if err != nil {
		return fmt.Errorf("创建心跳流失败: %w", err)
	}
	// 使用defer确保流在函数结束时关闭
	defer stream.Close()

	// 创建心跳ping消息
	heartbeat := CreateHeartbeatPing(hs.nodeType, hs.host.ID().String())

	// 序列化并发送
	pingBytes, err := WriteMessageToStream(heartbeat)
	if err != nil {
		return fmt.Errorf("序列化心跳失败: %w", err)
	}

	// 设置写入超时
	stream.SetWriteDeadline(time.Now().Add(5 * time.Second))

	_, err = stream.Write(pingBytes)
	if err != nil {
		// fmt.Printf("[心跳] 发送失败: %v\n", err)
		return fmt.Errorf("发送心跳失败: %w", err)
	}

	// 清除写入超时
	stream.SetWriteDeadline(time.Time{})

	// 读取响应
	data := make([]byte, 2048) // 增大缓冲区大小
	// fmt.Printf("[心跳] 等待 %s 的响应...\n", peerID.String())

	// 设置读取超时
	stream.SetReadDeadline(time.Now().Add(8 * time.Second))

	// 读取响应
	n, readErr := stream.Read(data)

	// 即使收到EOF，也要检查是否有数据
	if n > 0 {
		// 打印收到的原始数据进行调试
		// fmt.Printf("[心跳] 收到响应(%d字节): %s\n", n, string(data[:n]))

		// 尝试解析响应
		response, parseErr := ReadHeartbeatMessage(data[:n])
		if parseErr == nil && IsValidHeartbeatPong(response) {
			// 成功解析有效响应
			// fmt.Printf("[心跳] 成功解析响应: action=%s, from=%s\n", response.Action, response.From)
			// fmt.Printf("[心跳] 心跳成功完成\n")

			// 更新peer活动时间
			hs.UpdatePeerTime(peerID)
			return nil
		} else if parseErr != nil {
			// fmt.Printf("[心跳] 解析响应失败: %v\n", parseErr)
		} else {
			// fmt.Printf("[心跳] 响应不是有效的pong: %s\n", response.Action)
		}
	}

	// 处理EOF和其他错误
	if readErr == io.EOF {
		// 如果收到EOF且已经处理了数据，但数据无效，需要重试
		// fmt.Printf("[心跳] 收到EOF，流已关闭\n")

		// 如果已经最大重试次数，返回错误
		if retryCount >= 1 {
			return fmt.Errorf("读取心跳响应失败: 流已关闭(EOF)")
		}

		// 添加延迟时间，避免立即重试
		retryDelay := 1 * time.Second * time.Duration(retryCount+1)
		// fmt.Printf("[心跳] 将在 %v 后重试\n", retryDelay)
		time.Sleep(retryDelay)

		// 创建新的上下文，避免使用已过期的上下文
		newCtx, cancel := context.WithTimeout(hs.ctx, 5*time.Second)
		defer cancel()

		// 增加重试计数并重试
		return hs.sendHeartbeatWithRetry(newCtx, peerID, retryCount+1)
	} else if readErr != nil {
		// 其他错误
		if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
			// fmt.Printf("[心跳] 读取超时: %v\n", readErr)

			// 如果超时且已到达重试上限，返回错误
			if retryCount >= 1 {
				return fmt.Errorf("读取心跳响应超时: %w", readErr)
			}

			// 重试
			retryDelay := 500 * time.Millisecond * time.Duration(retryCount+1)
			// fmt.Printf("[心跳] 读取超时，将在 %v 后重试\n", retryDelay)
			time.Sleep(retryDelay)

			newCtx, cancel := context.WithTimeout(hs.ctx, 5*time.Second)
			defer cancel()
			return hs.sendHeartbeatWithRetry(newCtx, peerID, retryCount+1)
		}

		// 其他错误，直接返回
		return fmt.Errorf("读取心跳响应失败: %w", readErr)
	}

	// 如果没有错误但响应无效
	return fmt.Errorf("未收到有效的心跳响应")
}
