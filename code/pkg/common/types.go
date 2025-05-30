package common

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// NodeType 表示网络中节点的类型
type NodeType int

const (
	TypeUnknown NodeType = 0
	TypeMaster  NodeType = 1
	TypeAgent   NodeType = 2
	TypeClient  NodeType = 3
)

// 协议标识符 - 添加版本号以便于未来升级
const (
	// ProtocolPrefix 是所有协议前缀
	ProtocolPrefix = "/shiledp2p/v1.0.0"

	// PeerDiscoveryProtocol 是节点发现协议
	PeerDiscoveryProtocol = ProtocolPrefix + "/discovery"

	// ProxyProtocolID 是代理协议
	ProxyProtocolID = ProtocolPrefix + "/proxy"

	// HolePunchingProtocolID 是NAT穿透协议
	HolePunchingProtocolID = ProtocolPrefix + "/holepunch"

	// SpeedTestProtocolID 是对等节点间测速协议
	SpeedTestProtocolID = ProtocolPrefix + "/speedtest"

	// HeartbeatProtocolID 是心跳协议
	HeartbeatProtocolID = ProtocolPrefix + "/heartbeat"

	// BootstrapProtocolID 是客户端引导协议（用于获取Agent列表）
	BootstrapProtocolID = ProtocolPrefix + "/bootstrap"
)

// 代理相关

// ProxyProtocol 表示代理协议类型
type ProxyProtocol int

const (
	ProtoHTTP ProxyProtocol = iota
	ProtoHTTPS
)

// PeerInfo 节点信息
type PeerInfo struct {
	PeerID     peer.ID  `json:"peer_id"`
	NodeType   NodeType `json:"node_type"`
	Multiaddrs []string `json:"multiaddrs"`
}

// 代理请求消息
type ProxyRequest struct {
	Action     string        `json:"action"` // "proxy"
	NodeType   NodeType      `json:"node_type"`
	From       string        `json:"from"`
	Timestamp  int64         `json:"timestamp"`
	Target     string        `json:"target,omitempty"` // 目标URL
	TargetHost string        `json:"target_host,omitempty"`
	TargetPort int           `json:"target_port,omitempty"`
	Protocol   ProxyProtocol `json:"protocol,omitempty"`
}

// 统一基础消息 - 所有消息的基础结构

// 标准请求消息 - 用于所有请求类型的基础结构
type StandardRequest struct {
	// 操作类型: "register", "bootstrap", "ping", "pong", "proxy", "holepunch"
	Action string `json:"action"`

	// 节点类型
	NodeType NodeType `json:"node_type"`

	// 发送方ID
	From string `json:"from"`

	// 时间戳
	Timestamp int64 `json:"timestamp"`

	// 可选地址列表
	Addrs []string `json:"addrs,omitempty"`

	// 可选目标ID (用于代理、打洞等)
	Target string `json:"target,omitempty"`

	// 其他可选数据
	Data []byte `json:"data,omitempty"`
}

// 标准响应消息
type StandardResponse struct {
	// 状态: "success" 或 "error"
	Status string `json:"status"`

	// 消息
	Message string `json:"message,omitempty"`

	// 错误信息
	Error string `json:"error,omitempty"`

	// 节点类型
	NodeType NodeType `json:"node_type"`

	// 响应方ID
	From string `json:"from"`

	// 时间戳
	Timestamp int64 `json:"timestamp"`

	// 对应请求的Action
	Action string `json:"action"`
}

// Agent信息
type AgentInfo struct {
	PeerID     string   `json:"peer_id"`
	Multiaddrs []string `json:"multiaddrs"`
}

// 引导响应消息 - 专用于Client获取Agent列表
type BootstrapResponse struct {
	// 继承标准响应字段
	Status    string   `json:"status"`
	Message   string   `json:"message,omitempty"`
	Error     string   `json:"error,omitempty"`
	NodeType  NodeType `json:"node_type"`
	From      string   `json:"from"`
	Timestamp int64    `json:"timestamp"`
	Action    string   `json:"action"`

	// 引导特有字段
	Agents []AgentInfo `json:"agents"`
	Count  int         `json:"count"`
}

// 心跳消息 - 简化心跳结构，基于标准请求
type HeartbeatMessage struct {
	Action    string   `json:"action"` // "ping" 或 "pong"
	NodeType  NodeType `json:"node_type"`
	From      string   `json:"from"`
	Timestamp int64    `json:"timestamp"`
	Status    string   `json:"status,omitempty"`
	Error     string   `json:"error,omitempty"` // 错误信息
}

// 统一的JSON序列化方法，确保每个消息末尾有换行符
func JSONMarshalWithNewline(v interface{}) ([]byte, error) {
	buf := &bytes.Buffer{}
	encoder := json.NewEncoder(buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// 创建新的标准请求
func NewStandardRequest(action string, nodeType NodeType, from string) StandardRequest {
	return StandardRequest{
		Action:    action,
		NodeType:  nodeType,
		From:      from,
		Timestamp: time.Now().UnixNano(),
	}
}

// 创建新的标准响应
func NewStandardResponse(action string, status string, message string, nodeType NodeType, from string) StandardResponse {
	return StandardResponse{
		Action:    action,
		Status:    status,
		Message:   message,
		NodeType:  nodeType,
		From:      from,
		Timestamp: time.Now().UnixNano(),
	}
}

// 创建新的心跳消息
func NewHeartbeatMessage(action string, nodeType NodeType, from string) HeartbeatMessage {
	return HeartbeatMessage{
		Action:    action,
		NodeType:  nodeType,
		From:      from,
		Timestamp: time.Now().UnixNano(),
	}
}
