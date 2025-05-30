package common

import (
	"github.com/libp2p/go-libp2p/core/host"
)

// GetFullMultiaddrs 获取完整的多地址（包含peerID）
func GetFullMultiaddrs(h host.Host) []string {
	var fullAddrs []string

	// 获取主机地址
	addrs := h.Addrs()
	peerID := h.ID().String()

	// 为每个地址添加peerID
	for _, addr := range addrs {
		fullAddrs = append(fullAddrs, addr.String()+"/p2p/"+peerID)
	}

	return fullAddrs
}

// IsNodeType 检查节点类型是否匹配
func IsNodeType(nodeType NodeType, expectedType NodeType) bool {
	return nodeType == expectedType
}

// IsMaster 检查是否是Master节点
func IsMaster(nodeType NodeType) bool {
	return nodeType == TypeMaster
}

// IsAgent 检查是否是Agent节点
func IsAgent(nodeType NodeType) bool {
	return nodeType == TypeAgent
}

// IsClient 检查是否是Client节点
func IsClient(nodeType NodeType) bool {
	return nodeType == TypeClient
}
